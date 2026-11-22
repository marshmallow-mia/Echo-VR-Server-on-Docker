// Package hostcfg is evr-docker's configuration: one YAML file for settings
// and an env file (0600) for secrets.
package hostcfg

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/instance"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/nevr"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/nevr/config"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/updates"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/version"
)

// ImageRepo is where the server image is published.
const ImageRepo = "ghcr.io/marshmallow-mia/echovr-server"

// DefaultImage is the server image released with this build (see SetVersion).
// It is not written to config.yaml, so updating evr-docker also moves the
// servers to the matching image; set image there to pin another one.
var DefaultImage = ImageRepo + ":edge"

// SetVersion selects the image released with tool version v.
func SetVersion(v string) { DefaultImage = ImageRepo + ":" + version.ImageTag(v) }

// Default locations.
const (
	DefaultDir     = "/etc/evr-docker"
	DefaultDataDir = "/srv/echovr"
	DefaultProject = "evr"
	DefaultUID     = 999
	FirstPort      = 6792
)

// Secret names in secrets.env.
const (
	SecretWebhook = "EVR_DISCORD_WEBHOOK"
)

// Config is the host configuration.
type Config struct {
	Version int `yaml:"version"`
	// Project names this setup's containers (<project>-<instance>); change it
	// only to run several independent setups on one Docker host.
	Project  string `yaml:"project"`
	DataDir  string `yaml:"data_dir"`
	Image    string `yaml:"image,omitempty"`     // empty = DefaultImage
	BindIP   string `yaml:"bind_ip,omitempty"`   // host address ports are published on; empty = all
	PublicIP string `yaml:"public_ip,omitempty"` // expected registration address, for checks
	UID      int    `yaml:"uid"`                 // user the containers run as

	Defaults  instance.Settings `yaml:"defaults"`
	Resources Resources         `yaml:"resources,omitempty"`
	Instances []Instance        `yaml:"instances"`
	Runtime   RuntimePolicy     `yaml:"runtime"`
	Updates   UpdatePolicy      `yaml:"updates"`
	Notify    Notify            `yaml:"notify,omitempty"`
	Metrics   Metrics           `yaml:"metrics,omitempty"`

	path string
}

// Resources limit one container. Zero means no limit.
type Resources struct {
	CPUs     float64 `yaml:"cpus,omitempty"`
	MemoryMB int     `yaml:"memory_mb,omitempty"`
}

// Instance is one game server.
type Instance struct {
	Name      string            `yaml:"name"`
	Port      int               `yaml:"port"`
	Disabled  bool              `yaml:"disabled,omitempty"`
	Settings  instance.Settings `yaml:",inline"`
	Resources Resources         `yaml:"resources,omitempty"`
}

// RuntimePolicy controls which nevr-runtime "current" points at, and who
// keeps it current.
type RuntimePolicy struct {
	// Updater is UpdaterNevr (from v6.17.0 on the runtime updates itself to
	// its channel's release; evr-docker keeps the store, and so the host DLL,
	// on that release) or UpdaterHost (evr-docker updates it, with the
	// canary, and turns the runtime's own update off).
	Updater string `yaml:"updater"`
	// Channel is one of nEVR's channels (stable, beta, ...: the release nEVR's
	// manifest names) or "pinned" (Pinned; only with UpdaterHost: nEVR would
	// leave a pin). Before updater existed it was "pre" (GitHub's newest),
	// "stable" (GitHub's newest full release) or "pinned".
	Channel string `yaml:"channel"`
	Pinned  string `yaml:"pinned,omitempty"`
	// Before updates: existed; read once and moved there.
	AutoUpdate *bool `yaml:"auto_update,omitempty"`
	CheckHours int   `yaml:"check_hours,omitempty"`
}

// Runtime updaters (RuntimePolicy.Updater).
const (
	UpdaterNevr = "nevr"
	UpdaterHost = "evr-docker"
)

// SelfUpdateEnv is the container environment for RuntimePolicy: who keeps a
// runtime current (evr-supervisor's EVR_RUNTIME_UPDATER) and the channel.
func (r RuntimePolicy) SelfUpdateEnv() []string {
	who := nevr.UpdaterHost
	if r.Updater == UpdaterNevr {
		who = nevr.UpdaterNevr
	}
	ch := nevr.ChannelName(r.Channel)
	if ch == "pinned" { // a pin is evr-docker's; the channel is not used then
		ch = nevr.DefaultChannel
	}
	return []string{"EVR_RUNTIME_UPDATER=" + who, "NEVR_UPDATE_CHANNEL=" + ch}
}

// UpdatePolicy says how each part is updated: off, notify or auto.
type UpdatePolicy struct {
	CheckHours int    `yaml:"check_hours,omitempty"` // default 6
	Window     string `yaml:"window,omitempty"`      // "04:00-06:00": automatic installs only then
	Self       string `yaml:"self"`                  // evr-docker itself
	Game       string `yaml:"game"`
	Runtime    string `yaml:"runtime"`
}

// Settings converts the policy for the updates package.
func (u UpdatePolicy) Settings() updates.Settings {
	return updates.Settings{CheckHours: u.CheckHours, Window: u.Window,
		Modes: map[string]updates.Mode{"self": updates.Mode(u.Self), "game": updates.Mode(u.Game), "runtime": updates.Mode(u.Runtime)}}
}

// migrate moves settings from older config versions.
func (c *Config) migrate() {
	// A config from before updater: evr-docker kept updating, on nEVR's
	// stable channel from now on (the old channels followed GitHub's
	// releases, which no nEVR channel does). A pin stays.
	if c.Runtime.Updater == "" {
		c.Runtime.Updater = UpdaterHost
		if c.Runtime.Channel != "pinned" {
			c.Runtime.Channel = nevr.DefaultChannel
		}
	}
	c.Runtime.Channel = nevr.ChannelName(c.Runtime.Channel)
	if c.Runtime.AutoUpdate != nil {
		if c.Updates.Runtime == "" {
			c.Updates.Runtime = string(updates.Notify)
			if *c.Runtime.AutoUpdate {
				c.Updates.Runtime = string(updates.Auto)
			}
		}
		c.Runtime.AutoUpdate = nil
	}
	if c.Runtime.CheckHours > 0 {
		if c.Updates.CheckHours == 0 {
			c.Updates.CheckHours = c.Runtime.CheckHours
		}
		c.Runtime.CheckHours = 0
	}
	d := New().Updates
	for _, f := range []struct {
		v   *string
		def string
	}{{&c.Updates.Self, d.Self}, {&c.Updates.Game, d.Game}, {&c.Updates.Runtime, d.Runtime}} {
		if *f.v == "" {
			*f.v = f.def
		}
	}
}

// Notify configures Discord alerts; the webhook URL is a secret.
type Notify struct {
	Events []string `yaml:"events,omitempty"` // empty = all
}

// Metrics configures the agent's Prometheus endpoint.
type Metrics struct {
	Listen string `yaml:"listen,omitempty"` // e.g. 127.0.0.1:9469; empty = off
}

// New returns a config with defaults.
func New() *Config {
	return &Config{
		Version: 1, Project: DefaultProject, DataDir: DefaultDataDir, Image: DefaultImage, UID: DefaultUID,
		Defaults: instance.Settings{TickRate: config.DefaultTickRate, PublisherLock: "echovrce", Runtime: "current"},
		Runtime:  RuntimePolicy{Updater: UpdaterNevr, Channel: nevr.DefaultChannel},
		Updates:  UpdatePolicy{CheckHours: 6, Self: "notify", Game: "notify", Runtime: "auto"},
		Metrics:  Metrics{Listen: "127.0.0.1:9469"},
	}
}

// Dir returns the config directory: $EVR_DOCKER_CONFIG_DIR or /etc/evr-docker.
func Dir() string {
	if d := os.Getenv("EVR_DOCKER_CONFIG_DIR"); d != "" {
		return d
	}
	return DefaultDir
}

// ErrNotInitialized means there is no config yet.
var ErrNotInitialized = errors.New("evr-docker is not set up yet")

// Load reads <dir>/config.yaml.
func Load(dir string) (*Config, error) {
	p := filepath.Join(dir, "config.yaml")
	b, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return nil, ErrNotInitialized
	}
	if err != nil {
		return nil, err
	}
	c := New()
	c.Updates = UpdatePolicy{}
	c.Runtime = RuntimePolicy{} // what the file leaves out, migrate fills in
	if err := yaml.Unmarshal(b, c); err != nil {
		return nil, fmt.Errorf("%s: %w", p, err)
	}
	c.migrate()
	if c.Image == "" {
		c.Image = DefaultImage
	}
	c.path = p
	return c, c.Validate()
}

// Path is where the config was loaded from or will be saved.
func (c *Config) Path() string { return c.path }

// Save writes the config atomically.
func (c *Config) Save(dir string) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	out := *c
	if out.Image == DefaultImage {
		out.Image = ""
	}
	b, err := yaml.Marshal(&out)
	if err != nil {
		return err
	}
	b = append([]byte("# evr-docker configuration. Edit by hand or with `evr-docker`; apply with `evr-docker server apply`.\n"), b...)
	c.path = filepath.Join(dir, "config.yaml")
	return writeAtomic(c.path, b, 0o644)
}

// Validate checks the whole config.
func (c *Config) Validate() error {
	var errs []error
	if err := instance.ValidName(c.Project); err != nil {
		errs = append(errs, fmt.Errorf("project: %w", err))
	}
	if !filepath.IsAbs(c.DataDir) {
		errs = append(errs, fmt.Errorf("data_dir must be absolute, got %q", c.DataDir))
	}
	if c.Image == "" {
		errs = append(errs, errors.New("image is empty"))
	}
	if c.BindIP != "" && net.ParseIP(c.BindIP) == nil {
		errs = append(errs, fmt.Errorf("bind_ip %q is not an IP address", c.BindIP))
	}
	switch u := c.Runtime.Updater; {
	case u != UpdaterNevr && u != UpdaterHost:
		errs = append(errs, fmt.Errorf("runtime.updater must be %s or %s, got %q", UpdaterNevr, UpdaterHost, u))
	case c.Runtime.Channel == "pinned" && u == UpdaterNevr:
		errs = append(errs, fmt.Errorf("runtime.channel pinned needs runtime.updater %s (nEVR would update itself away from the pin)", UpdaterHost))
	}
	switch ch := c.Runtime.Channel; {
	case ch == "pinned":
		if c.Runtime.Pinned == "" {
			errs = append(errs, errors.New("runtime.channel is pinned but runtime.pinned is empty"))
		}
	case !nevr.ChannelOK(ch):
		errs = append(errs, fmt.Errorf("runtime.channel must be one of nEVR's channels (stable, beta, ...) or pinned, got %q", ch))
	}
	for k, m := range map[string]string{"self": c.Updates.Self, "game": c.Updates.Game, "runtime": c.Updates.Runtime} {
		if _, err := updates.ParseMode(m, updates.Notify); err != nil {
			errs = append(errs, fmt.Errorf("updates.%s: %w", k, err))
		}
	}
	if err := updates.ValidWindow(c.Updates.Window); err != nil {
		errs = append(errs, fmt.Errorf("updates.window: %w", err))
	}
	if err := c.Defaults.Validate(); err != nil {
		errs = append(errs, fmt.Errorf("defaults: %w", err))
	}
	names, ports := map[string]bool{}, map[int]string{}
	for _, in := range c.Instances {
		if err := instance.ValidName(in.Name); err != nil {
			errs = append(errs, err)
		}
		if names[in.Name] {
			errs = append(errs, fmt.Errorf("instance %s is defined twice", in.Name))
		}
		names[in.Name] = true
		if in.Port < 1024 || in.Port > 65535 {
			errs = append(errs, fmt.Errorf("instance %s: port %d out of range", in.Name, in.Port))
		} else if other, ok := ports[in.Port]; ok {
			errs = append(errs, fmt.Errorf("instances %s and %s both use port %d", other, in.Name, in.Port))
		}
		ports[in.Port] = in.Name
		if err := in.Settings.Validate(); err != nil {
			errs = append(errs, fmt.Errorf("instance %s: %w", in.Name, err))
		}
	}
	return errors.Join(errs...)
}

// Instance returns the named instance.
func (c *Config) Instance(name string) (*Instance, bool) {
	for i := range c.Instances {
		if c.Instances[i].Name == name {
			return &c.Instances[i], true
		}
	}
	return nil, false
}

// Effective returns an instance's settings with the defaults applied.
func (c *Config) Effective(in Instance) instance.Settings { return c.Defaults.Merge(in.Settings) }

// EffectiveResources returns an instance's limits with the defaults applied.
func (c *Config) EffectiveResources(in Instance) Resources {
	r := c.Resources
	if in.Resources.CPUs != 0 {
		r.CPUs = in.Resources.CPUs
	}
	if in.Resources.MemoryMB != 0 {
		r.MemoryMB = in.Resources.MemoryMB
	}
	return r
}

// NextFree returns the next unused port and a free name like echo-3.
func (c *Config) NextFree() (string, int) {
	used := map[int]bool{}
	names := map[string]bool{}
	for _, in := range c.Instances {
		used[in.Port] = true
		names[in.Name] = true
	}
	port := FirstPort
	for used[port] {
		port++
	}
	n := 1
	for names["echo-"+strconv.Itoa(n)] {
		n++
	}
	return "echo-" + strconv.Itoa(n), port
}

// Paths under DataDir.
func (c *Config) GameDir() string                { return filepath.Join(c.DataDir, "game") }
func (c *Config) NevrDir() string                { return filepath.Join(c.DataDir, "nevr") }
func (c *Config) InstanceDir(name string) string { return filepath.Join(c.DataDir, "instances", name) }

// Secrets are KEY=VALUE pairs from secrets.env.
type Secrets map[string]string

// LoadSecrets reads <dir>/secrets.env; a missing file is empty.
func LoadSecrets(dir string) (Secrets, error) {
	s := Secrets{}
	f, err := os.Open(filepath.Join(dir, "secrets.env"))
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		s[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return s, sc.Err()
}

// Save writes secrets.env with mode 0600.
func (s Secrets) Save(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	keys := make([]string, 0, len(s))
	for k := range s {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("# evr-docker secrets (mode 0600). EchoVRCE host account and webhook.\n")
	for _, k := range keys {
		if strings.ContainsAny(s[k], "\n\r") {
			return fmt.Errorf("secret %s contains a line break", k)
		}
		fmt.Fprintf(&b, "%s=%s\n", k, s[k])
	}
	return writeAtomic(filepath.Join(dir, "secrets.env"), []byte(b.String()), 0o600)
}

// Missing lists the account secrets a password sign-in still lacks: the
// Discord ID. Without a password nothing is missing: from nevr-runtime v6.9.0
// on each server signs in by a code it prints on its console (a 5.x server by
// a device link made earlier).
func (s Secrets) Missing() []string {
	if s[config.EnvPassword] != "" && s[config.EnvDiscordID] == "" {
		return []string{config.EnvDiscordID}
	}
	return nil
}

// GameEnv returns the secrets the game container needs.
func (s Secrets) GameEnv() []string {
	var env []string
	for _, k := range []string{config.EnvDiscordID, config.EnvPassword, config.EnvHTTPKey} {
		if v := s[k]; v != "" {
			env = append(env, k+"="+v)
		}
	}
	return env
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
