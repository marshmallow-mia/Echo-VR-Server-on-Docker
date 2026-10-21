package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/internal/docker"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/internal/hostcfg"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/game"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/nevr/config"
)

// LegacyImage is the image name the v1 run.sh built.
const LegacyImage = "ready-at-dawn-echo-arena"

// Legacy describes a v1 (bash) installation found on the host.
type Legacy struct {
	Containers []LegacyContainer
	GameDir    string
	GameOK     bool
	GameErr    string
	DiscordID  string
	Password   string `json:"-"`
	HTTPKey    string `json:"-"`
	HasHTTPKey bool
	CronFile   string
	CronLines  []string
}

// LegacyContainer is one v1 game server container.
type LegacyContainer struct {
	ID, Name, State string
	Port            int
}

// Found reports whether anything from v1 exists.
func (l Legacy) Found() bool { return len(l.Containers) > 0 || l.GameDir != "" }

// DetectLegacy looks for v1 containers, their game directory, credentials and cron jobs.
// gameDir may point at the old game tree when no container is left.
func DetectLegacy(ctx context.Context, gameDir string) (Legacy, error) {
	var l Legacy
	d, err := docker.New()
	if err != nil {
		return l, err
	}
	all, err := d.ListAll(ctx)
	if err != nil {
		return l, err
	}
	for _, c := range all {
		if c.Image != LegacyImage && !strings.HasPrefix(c.Image, LegacyImage+":") {
			continue
		}
		info, err := d.Inspect(ctx, c.ID)
		if err != nil {
			continue
		}
		lc := LegacyContainer{ID: c.ID, Name: c.Name, State: c.State}
		for _, kv := range info.Env {
			if v, ok := strings.CutPrefix(kv, "port="); ok {
				lc.Port, _ = strconv.Atoi(v)
			}
		}
		for _, m := range info.Mounts {
			if m.Destination == "/ready-at-dawn-echo-arena" && gameDir == "" {
				gameDir = m.Source
			}
		}
		l.Containers = append(l.Containers, lc)
	}
	sort.Slice(l.Containers, func(i, j int) bool { return l.Containers[i].Port < l.Containers[j].Port })
	if gameDir != "" {
		l.GameDir = gameDir
		if _, err := game.Verify(gameDir); err != nil {
			l.GameErr = err.Error()
		} else {
			l.GameOK = true
		}
		l.readCredentials(gameDir)
	}
	for _, p := range []string{"/var/spool/cron/crontabs/root", "/var/spool/cron/root", "/etc/crontab"} {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(b), "\n") {
			if strings.Contains(line, "echo_update.sh") && !strings.HasPrefix(strings.TrimSpace(line), "#") {
				l.CronFile = p
				l.CronLines = append(l.CronLines, line)
			}
		}
	}
	return l, nil
}

// readCredentials takes the account from config.yaml (nevr 4.x) or the
// serverdb/login URLs in config.json (EchoRelay era).
func (l *Legacy) readCredentials(gameDir string) {
	if b, err := os.ReadFile(filepath.Join(gameDir, "_local", "config.yaml")); err == nil {
		if c, err := config.Parse(b); err == nil {
			l.DiscordID, l.Password, l.HTTPKey = c.Identity.DiscordID, c.Auth.Password, c.Auth.HTTPKey
		}
	}
	if l.DiscordID == "" || l.Password == "" {
		var cj map[string]any
		if b, err := os.ReadFile(filepath.Join(gameDir, "_local", "config.json")); err == nil && json.Unmarshal(b, &cj) == nil {
			for _, k := range []string{"serverdb_host", "loginservice_host"} {
				s, _ := cj[k].(string)
				u, err := url.Parse(s)
				if err != nil {
					continue
				}
				q := u.Query()
				if id := q.Get("discordid"); id != "" && l.DiscordID == "" {
					l.DiscordID = id
				}
				if pw := q.Get("password"); pw != "" && l.Password == "" {
					l.Password = pw
				}
			}
		}
	}
	l.HasHTTPKey = l.HTTPKey != ""
}

// Migrate sets evr-docker up from a v1 installation: same account, the old
// game files (hard-linked, not copied, when on the same disk), one server per
// old container on the same port. Old containers are stopped and removed and
// the old update cron job is commented out.
func Migrate(ctx context.Context, dir string, l Legacy, httpKey, image, dataDir, project string, say func(string, string)) (*App, error) {
	if !l.GameOK {
		return nil, fmt.Errorf("no usable v1 game directory: %s", l.GameErr)
	}
	if httpKey == "" {
		httpKey = l.HTTPKey
	}
	var an Answers
	an.Account.DiscordID, an.Account.Password, an.Account.HTTPKey = l.DiscordID, l.Password, httpKey
	an.Game.Source, an.Game.Path = "dir", l.GameDir
	an.Image, an.PublicIP, an.DataDir, an.Project = image, "auto", dataDir, project
	if dataDir == "" {
		dataDir = hostcfg.DefaultDataDir
	}
	if _, err := (game.Installer{Dir: filepath.Join(dataDir, "game")}).Current(); err == nil {
		an.Game.Source = "skip" // a verified build is already installed there
	}
	// Stop the old servers first so their ports are free.
	d, err := docker.New()
	if err != nil {
		return nil, err
	}
	for _, c := range l.Containers {
		say("Old servers", "stopping and removing "+c.Name)
		_ = d.Stop(ctx, c.ID, 30*time.Second)
		if err := d.Remove(ctx, c.ID, true); err != nil && !errors.Is(err, docker.ErrNotFound) {
			return nil, err
		}
	}
	if l.CronFile != "" {
		if err := commentOut(l.CronFile, "echo_update.sh"); err != nil {
			say("Old servers", "could not disable the old update cron job: "+err.Error())
		} else {
			say("Old servers", "disabled the old echo_update.sh cron job in "+l.CronFile)
		}
	}
	used := map[int]bool{}
	for i, c := range l.Containers {
		port := c.Port
		for port == 0 || used[port] {
			port = hostcfg.FirstPort + i
			for used[port] {
				port++
			}
		}
		used[port] = true
		an.Instances = append(an.Instances, hostcfg.Instance{Name: fmt.Sprintf("echo-%d", i+1), Port: port})
	}
	an.Start = true
	return Init(ctx, dir, an, say)
}

func commentOut(path, needle string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(string(b), "\n")
	for i, l := range lines {
		if strings.Contains(l, needle) && !strings.HasPrefix(strings.TrimSpace(l), "#") {
			lines[i] = "# disabled by evr-docker migrate: " + l
		}
	}
	fi, _ := os.Stat(path)
	mode := os.FileMode(0o600)
	if fi != nil {
		mode = fi.Mode().Perm()
	}
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")), mode)
}
