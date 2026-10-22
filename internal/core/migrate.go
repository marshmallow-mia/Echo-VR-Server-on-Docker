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
	// RepoDir is the v1 checkout with run.sh and scripts/ ("" if not found).
	RepoDir string
	// Images are the old ready-at-dawn-echo-arena images.
	Images []docker.Image
	// LogsSize is the size of the old game folder's logs folder.
	LogsSize int64
}

// v1RepoEntries are the files of a v1 checkout; anything else in it is kept.
var v1RepoEntries = []string{".git", ".github", "Dockerfile", "README.md", "files", "install.sh", "rsyncHosts", "run.sh", "scripts"}

// Things a migration can delete afterwards.
const (
	RemoveScripts = "scripts"
	RemoveImage   = "image"
	RemoveGame    = "game"
)

// Log handling after a migration.
const (
	LogsMove   = "move"
	LogsKeep   = "keep"
	LogsDelete = "delete"
)

// MigrateOptions says what happens to the old installation once the new servers run.
type MigrateOptions struct {
	Logs   string   // LogsMove (default), LogsKeep or LogsDelete
	Remove []string // RemoveScripts, RemoveImage, RemoveGame
}

// Validate rejects combinations that would lose what was asked to be kept.
func (o MigrateOptions) Validate() error {
	switch o.Logs {
	case "", LogsMove, LogsKeep, LogsDelete:
	default:
		return fmt.Errorf("logs must be move, keep or delete, not %q", o.Logs)
	}
	for _, r := range o.Remove {
		switch r {
		case RemoveScripts, RemoveImage, RemoveGame:
		default:
			return fmt.Errorf("can remove scripts, image or game, not %q", r)
		}
		if r == RemoveGame && o.Logs == LogsKeep {
			return errors.New("the old logs are inside the old game folder: move them, or keep the folder")
		}
	}
	return nil
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
			if m.Destination == "/scripts" && l.RepoDir == "" && isFile(filepath.Join(filepath.Dir(m.Source), "run.sh")) {
				l.RepoDir = filepath.Dir(m.Source)
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
		l.LogsSize = game.DirSize(filepath.Join(gameDir, "logs"))
		if parent := filepath.Dir(gameDir); l.RepoDir == "" && isFile(filepath.Join(parent, "run.sh")) {
			l.RepoDir = parent
		}
	}
	l.Images, _ = d.Images(ctx, LegacyImage)
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

// Migrate replaces a v1 installation with new evr-docker servers: same
// account, the old game files (hard-linked, not copied, when on the same
// disk), one new server per old container on the same port. Old containers
// are stopped and removed and the old update cron job is commented out.
// Once the new servers run, opts decides what happens to the old logs,
// scripts, image and game folder.
func Migrate(ctx context.Context, dir string, l Legacy, httpKey, image, dataDir, project string, opts MigrateOptions, say func(string, string)) (*App, error) {
	if !l.GameOK {
		return nil, fmt.Errorf("no usable v1 game directory: %s", l.GameErr)
	}
	if err := opts.Validate(); err != nil {
		return nil, err
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
	app, err := Init(ctx, dir, an, say)
	if err != nil {
		return app, err
	}
	return app, cleanupLegacy(ctx, l, app.Cfg.DataDir, opts, func(s string) { say("Old installation", s) })
}

// Removal is a part of a v1 installation that can be deleted after the move.
type Removal struct{ Key, What string }

// Removable lists what of l can be deleted after the move.
func (l Legacy) Removable() []Removal {
	var out []Removal
	if l.RepoDir != "" {
		out = append(out, Removal{RemoveScripts, "the old scripts in " + l.RepoDir + " and their cron job"})
	}
	if len(l.Images) > 0 {
		var size int64
		for _, img := range l.Images {
			size += img.Size
		}
		out = append(out, Removal{RemoveImage, "the old Docker image " + LegacyImage + " (" + game.Size(size) + ")"})
	}
	if l.GameDir != "" {
		out = append(out, Removal{RemoveGame, "the old game folder " + l.GameDir})
	}
	return out
}

// cleanupLegacy moves or deletes the old logs, then removes what opts lists.
func cleanupLegacy(ctx context.Context, l Legacy, dataDir string, opts MigrateOptions, say func(string)) error {
	if l.GameDir != "" {
		switch opts.Logs {
		case "", LogsMove:
			dst := filepath.Join(dataDir, game.V1LogsDir)
			n, err := game.MoveLogs(l.GameDir, dst)
			if err != nil {
				return fmt.Errorf("moving the old logs: %w", err)
			}
			if n > 0 {
				say(fmt.Sprintf("moved the old logs to %s", dst))
			}
		case LogsDelete:
			if err := os.RemoveAll(filepath.Join(l.GameDir, "logs")); err != nil {
				return err
			}
			say("deleted the old logs")
		}
	}
	remove := map[string]bool{}
	for _, r := range opts.Remove {
		remove[r] = true
	}
	if remove[RemoveImage] && len(l.Images) > 0 {
		d, err := docker.New()
		if err != nil {
			return err
		}
		for _, img := range l.Images {
			if err := d.RemoveImage(ctx, img.ID, true); err != nil && !errors.Is(err, docker.ErrNotFound) {
				return fmt.Errorf("deleting the old image: %w", err)
			}
		}
		say("deleted the old image " + LegacyImage)
	}
	if remove[RemoveGame] && l.GameDir != "" {
		if err := os.RemoveAll(l.GameDir); err != nil {
			return fmt.Errorf("deleting the old game folder: %w", err)
		}
		say("deleted the old game folder " + l.GameDir)
	}
	if remove[RemoveScripts] && l.RepoDir != "" {
		if err := game.RemoveEntries(l.RepoDir, v1RepoEntries); err != nil {
			return fmt.Errorf("deleting the old scripts: %w", err)
		}
		say("deleted the old scripts in " + l.RepoDir)
		if l.CronFile != "" {
			if err := dropLines(l.CronFile, "echo_update.sh"); err != nil {
				return err
			}
			say("removed the old cron job from " + l.CronFile)
		}
	}
	return nil
}

func isFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

// dropLines deletes every line containing needle, commented out or not.
func dropLines(path, needle string) error {
	return editLines(path, func(l string) (string, bool) { return l, !strings.Contains(l, needle) })
}

func commentOut(path, needle string) error {
	return editLines(path, func(l string) (string, bool) {
		if strings.Contains(l, needle) && !strings.HasPrefix(strings.TrimSpace(l), "#") {
			return "# disabled by evr-docker migrate: " + l, true
		}
		return l, true
	})
}

// editLines rewrites a file line by line; keep=false drops the line.
func editLines(path string, f func(string) (string, bool)) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var lines []string
	for _, l := range strings.Split(string(b), "\n") {
		if l, keep := f(l); keep {
			lines = append(lines, l)
		}
	}
	fi, _ := os.Stat(path)
	mode := os.FileMode(0o600)
	if fi != nil {
		mode = fi.Mode().Perm()
	}
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")), mode)
}
