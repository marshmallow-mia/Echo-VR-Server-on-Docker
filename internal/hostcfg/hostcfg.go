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
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/nevr/config"
)

// Default locations.
const (
	DefaultDir     = "/etc/evr-docker"
	DefaultDataDir = "/srv/echovr"
	DefaultProject = "evr"
	DefaultImage   = "ghcr.io/marshmallow-mia/echovr-server:edge" // switch to :2 with the v2.0.0 release
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
	Image    string `yaml:"image"`
	BindIP   string `yaml:"bind_ip,omitempty"`   // host address ports are published on; empty = all
	PublicIP string `yaml:"public_ip,omitempty"` // expected registration address, for checks
	UID      int    `yaml:"uid"`                 // user the containers run as

	Defaults  instance.Settings `yaml:"defaults"`
	Resources Resources         `yaml:"resources,omitempty"`
	Instances []Instance        `yaml:"instances"`
	Runtime   RuntimePolicy     `yaml:"runtime"`
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

// RuntimePolicy controls which nevr-runtime "current" points at.
type RuntimePolicy struct {
	// Channel is "pre" (newest release, including pre-releases; every 5.x
	// release is one), "stable" (full releases only) or "pinned".
	Channel    string `yaml:"channel"`
	Pinned     string `yaml:"pinned,omitempty"`
	AutoUpdate bool   `yaml:"auto_update"`
	// CheckHours is how often the agent looks for updates.
	CheckHours int `yaml:"check_hours,omitempty"`
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
		Runtime:  RuntimePolicy{Channel: "pre", AutoUpdate: true, CheckHours: 6},
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
var ErrNotInitialized = errors.New("evr-docker is not set up yet (run: evr-docker init)")

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
	if err := yaml.Unmarshal(b, c); err != nil {
		return nil, fmt.Errorf("%s: %w", p, err)
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
	b, err := yaml.Marshal(c)
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
	switch c.Runtime.Channel {
	case "pre", "stable":
	case "pinned":
		if c.Runtime.Pinned == "" {
			errs = append(errs, errors.New("runtime.channel is pinned but runtime.pinned is empty"))
		}
	default:
		errs = append(errs, fmt.Errorf("runtime.channel must be pre, stable or pinned, got %q", c.Runtime.Channel))
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

// Missing lists the account secrets that are not set. With a device link
// neither the password nor the Discord ID (taken from the backend) is needed.
func (s Secrets) Missing(deviceLinked bool) []string {
	if deviceLinked {
		return nil
	}
	var out []string
	for _, k := range []string{config.EnvDiscordID, config.EnvPassword} {
		if s[k] == "" {
			out = append(out, k)
		}
	}
	return out
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
