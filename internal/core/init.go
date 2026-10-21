package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/internal/hostcfg"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/game"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/instance"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/nevr/auth"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/nevr/config"
)

// Answers drive a non-interactive setup (`evr-docker init --answers f.yaml`)
// and are what the TUI wizard fills in.
type Answers struct {
	Project   string   `yaml:"project,omitempty"`
	DataDir   string   `yaml:"data_dir,omitempty"`
	Image     string   `yaml:"image,omitempty"`
	BindIP    string   `yaml:"bind_ip,omitempty"`
	PublicIP  string   `yaml:"public_ip,omitempty"`
	Servers   int      `yaml:"servers"`
	FirstPort int      `yaml:"first_port,omitempty"`
	Regions   []string `yaml:"regions,omitempty"`
	Guilds    []string `yaml:"guilds,omitempty"`
	Account   struct {
		DiscordID string `yaml:"discord_id"`
		Password  string `yaml:"password"`
		HTTPKey   string `yaml:"http_key,omitempty"` // optional; defaults to the public key
	} `yaml:"account"`
	Game struct {
		Source string `yaml:"source"` // mirror (default), dir, archive, skip
		Path   string `yaml:"path,omitempty"`
		Mirror string `yaml:"mirror,omitempty"`
	} `yaml:"game"`
	Runtime struct {
		Channel string `yaml:"channel,omitempty"` // pre (default), stable, pinned
		Pinned  string `yaml:"pinned,omitempty"`
	} `yaml:"runtime"`
	Start bool `yaml:"start"`
	// Instances are added as-is (used by migrate to keep the old ports).
	Instances []hostcfg.Instance `yaml:"-"`
}

// LoadAnswers reads an answers file; secrets may be given as ${ENV} references.
func LoadAnswers(path string) (Answers, error) {
	var a Answers
	b, err := os.ReadFile(path)
	if err != nil {
		return a, err
	}
	if err := yaml.Unmarshal([]byte(os.ExpandEnv(string(b))), &a); err != nil {
		return a, fmt.Errorf("%s: %w", path, err)
	}
	return a, nil
}

// Validate checks answers before anything is changed.
func (an *Answers) Validate() error {
	var errs []error
	// Without a password the host signs in with an echovrce.com device link
	// during setup, and the Discord ID comes from the backend.
	if an.Account.Password != "" && (an.Account.DiscordID == "" || strings.Trim(an.Account.DiscordID, "0123456789") != "") {
		errs = append(errs, errors.New("account.discord_id must be your numeric Discord user ID"))
	}
	if an.Servers < 0 || an.Servers > 64 {
		errs = append(errs, errors.New("servers must be between 0 and 64"))
	}
	switch an.Game.Source {
	case "", "mirror", "skip":
	case "dir", "archive":
		if an.Game.Path == "" {
			errs = append(errs, fmt.Errorf("game.path is required for source %s", an.Game.Source))
		}
	default:
		errs = append(errs, fmt.Errorf("game.source must be mirror, dir, archive or skip, got %q", an.Game.Source))
	}
	if err := (instance.Settings{Regions: an.Regions, Guilds: an.Guilds}).Validate(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// Init sets up evr-docker from answers. Every step can be re-run safely.
func Init(ctx context.Context, dir string, an Answers, progress func(step, detail string)) (*App, error) {
	say := func(step, detail string) {
		if progress != nil {
			progress(step, detail)
		}
	}
	if err := an.Validate(); err != nil {
		return nil, err
	}
	cfg, err := hostcfg.Load(dir)
	if errors.Is(err, hostcfg.ErrNotInitialized) {
		cfg, err = hostcfg.New(), nil
	}
	if err != nil {
		return nil, err
	}
	if an.DataDir != "" {
		cfg.DataDir = an.DataDir
	}
	if an.Project != "" {
		cfg.Project = an.Project
	}
	if an.Image != "" {
		cfg.Image = an.Image
	}
	cfg.BindIP = an.BindIP
	if an.PublicIP == "auto" {
		if ip, err := PublicIP(ctx); err == nil {
			cfg.PublicIP = ip
		}
	} else if an.PublicIP != "" {
		cfg.PublicIP = an.PublicIP
	}
	if an.Regions != nil {
		cfg.Defaults.Regions = an.Regions
	}
	if an.Guilds != nil {
		cfg.Defaults.Guilds = an.Guilds
	}
	if an.Runtime.Channel != "" {
		cfg.Runtime.Channel, cfg.Runtime.Pinned = an.Runtime.Channel, an.Runtime.Pinned
	}
	sec, err := hostcfg.LoadSecrets(dir)
	if err != nil {
		return nil, err
	}
	if an.Account.DiscordID != "" {
		sec[config.EnvDiscordID] = an.Account.DiscordID
	}
	if an.Account.Password != "" {
		sec[config.EnvPassword] = an.Account.Password
	}
	if an.Account.HTTPKey != "" {
		sec[config.EnvHTTPKey] = an.Account.HTTPKey
	}
	a := &App{Dir: dir, Cfg: cfg, Secrets: sec}

	var s auth.Session
	if an.Account.Password == "" && !a.DeviceLinked() {
		say("Account", "no password: linking this host at echovrce.com/device")
		say("Account", ResetPasswordNote)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
		s, err = a.LinkDevice(ctx, func(url, code string, expires time.Time) {
			say("Account", "open "+url+" and approve the code "+code+" (valid until "+expires.Local().Format(time.TimeOnly)+")")
		})
		if err != nil {
			return nil, fmt.Errorf("device link: %w", err)
		}
	} else {
		say("Account", "checking the EchoVRCE login")
		if s, err = a.Login(ctx); err != nil {
			return nil, fmt.Errorf("account check: %w", err)
		}
	}
	switch {
	case !s.MembershipsKnown:
		say("Account", s.Username+" signed in with the device link")
	case len(s.HostGuilds()) == 0:
		say("Account", "warning: this account has the server host role in no guild; servers will register but get no matches")
	default:
		say("Account", fmt.Sprintf("%s can host for %d guild(s)", s.Username, len(s.HostGuilds())))
	}
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return nil, err
	}
	if err := a.Save(); err != nil {
		return nil, err
	}
	say("Config", "saved to "+cfg.Path())

	games := a.Games(func(p game.Progress) {
		detail := p.Detail
		if p.Percent >= 0 {
			detail = fmt.Sprintf("%d%% %s", p.Percent, p.Detail)
		}
		say("Game: "+p.Step, detail)
	})
	if _, err := games.Current(); err != nil || an.Game.Source == "dir" || an.Game.Source == "archive" {
		switch an.Game.Source {
		case "skip":
			say("Game", "skipped; install it later with `evr-docker game install`")
		case "dir":
			_, err = games.FromDir(ctx, an.Game.Path)
		case "archive":
			_, err = games.FromArchive(ctx, an.Game.Path)
		default:
			_, err = games.FromMirror(ctx, an.Game.Mirror)
		}
		if err != nil {
			return a, fmt.Errorf("game install: %w", err)
		}
	} else {
		say("Game", "already installed")
	}

	say("Runtime", "installing nevr-runtime")
	tag, err := a.EnsureRuntime(ctx)
	if err != nil {
		return a, err
	}
	say("Runtime", "nevr-runtime "+tag)

	for _, in := range an.Instances {
		if _, exists := a.Cfg.Instance(in.Name); !exists {
			a.Cfg.Instances = append(a.Cfg.Instances, in)
		}
	}
	if err := a.Cfg.Validate(); err != nil {
		return a, err
	}
	if n := an.Servers - len(a.Cfg.Instances); n > 0 {
		if an.FirstPort != 0 && len(a.Cfg.Instances) == 0 {
			for i := 0; i < n; i++ {
				a.Cfg.Instances = append(a.Cfg.Instances, hostcfg.Instance{Name: fmt.Sprintf("echo-%d", i+1), Port: an.FirstPort + i})
			}
		} else if _, err := a.AddInstances(n, instance.Settings{}); err != nil {
			return a, err
		}
		if err := a.Save(); err != nil {
			return a, err
		}
	}
	if err := a.Save(); err != nil {
		return a, err
	}
	if an.Start {
		if _, err := a.Apply(ctx, false, func(s string) { say("Servers", s) }); err != nil {
			return a, err
		}
	}
	say("Done", fmt.Sprintf("%d server(s) configured", len(a.Cfg.Instances)))
	return a, nil
}
