// Package config models the nevr-runtime config.yaml of a dedicated server.
//
// A one-file runtime (5.x) reads <root>/_local/config.yaml. A two-file runtime
// (v6.0.0 on) reads only the nEVR root's config.yaml (or -config-file) and has
// EchoVRCE's service addresses and public HTTP key built in, so its file
// (RootDefault) carries only the account and the server's own settings.
//
// Key names follow the table in nevr-runtime src/runtime/lifecycle/service_map.cpp
// (v5.4.2), not docs/reference/example-config.yaml, which disagrees with the code
// in several places (identity.password, top-level regions/asset_cdn_url).
package config

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"

	"gopkg.in/yaml.v3"
)

// Environment variable names the rendered file refers to. The runtime expands
// ${VAR:?msg} itself, so secrets never have to be written to disk.
const (
	EnvDiscordID = "NEVR_DISCORD_ID"
	EnvPassword  = "NEVR_PASSWORD"
	EnvHTTPKey   = "NEVR_HTTP_KEY"
)

// Defaults for the public EchoVRCE backend.
const (
	DefaultSocketURI   = "ws://g.echovrce.com:80/spr"
	DefaultServerDBURI = "ws://g.echovrce.com:80/nevr"
	DefaultHTTPURI     = "https://g.echovrce.com:7350"
	// DefaultHTTPKey is the EchoVRCE backend's public HTTP key. nevr-runtime
	// embeds it in every client build ("public by design",
	// config/public-defaults.env.example); servers need it but do not get the
	// built-in defaults, so it is set explicitly.
	DefaultHTTPKey  = "1c65a082-6712-4a02-8c6d-23d5c32f7409"
	DefaultTickRate = 120
)

// Session router modes accepted by network.session_router; anything else is
// fatal at boot.
var sessionRouters = map[string]bool{"": true, "legacy": true, "config": true, "all": true}

// Config is the subset of config.yaml a dedicated server uses.
type Config struct {
	Version   int        `yaml:"version"`
	Services  Services   `yaml:"services,omitempty"`
	Identity  Identity   `yaml:"identity"`
	Auth      Auth       `yaml:"auth,omitempty"`
	Network   Network    `yaml:"network,omitempty"`
	Guilds    List       `yaml:"guilds,omitempty,flow"`
	Telemetry *Telemetry `yaml:"telemetry,omitempty"`
	Arena     *Arena     `yaml:"arena,omitempty"`

	// DeviceLinked means the instance has a device-link refresh token in
	// _local/.credentials.json, so a missing password is not rejected here.
	// It does not count for Root: a two-file runtime refuses a server login
	// without auth.password.
	DeviceLinked bool `yaml:"-"`
	// Root marks the config of a two-file runtime (RootDefault): the service
	// keys are optional overrides of the built-in values.
	Root bool `yaml:"-"`
}

type Services struct {
	SocketURI          string `yaml:"socket_uri,omitempty"`
	ServerDBURI        string `yaml:"serverdb_uri,omitempty"`
	AllowOfflineServer bool   `yaml:"allow_offline_server,omitempty"`
}

type Identity struct {
	DiscordID string `yaml:"discord_id"`
}

type Auth struct {
	HTTPURI  string `yaml:"http_uri,omitempty"`
	HTTPKey  string `yaml:"http_key,omitempty"`
	Password string `yaml:"password,omitempty"`
}

type Network struct {
	Regions               List   `yaml:"regions,omitempty,flow"`
	InternalIP            string `yaml:"internal_ip,omitempty"`
	ExternalIP            string `yaml:"external_ip,omitempty"`
	TickRateHz            int    `yaml:"tick_rate_hz,omitempty"`
	EmptyServerTTLSeconds int    `yaml:"empty_server_ttl_seconds,omitempty"`
	SessionRouter         string `yaml:"session_router,omitempty"`
}

type Telemetry struct {
	URI   string `yaml:"uri"`
	Token string `yaml:"token,omitempty"`
}

type Arena struct {
	RoundTime       float64 `yaml:"round_time,omitempty"`
	CelebrationTime float64 `yaml:"celebration_time,omitempty"`
	MercyScore      float64 `yaml:"mercy_score,omitempty"`
}

// List is a YAML list that may also be written as one comma-separated
// string, as the runtime accepts both ("uwu" or [euw, default]).
type List []string

// UnmarshalYAML accepts a sequence or a comma-separated scalar.
func (l *List) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		*l = nil
		for _, p := range strings.Split(n.Value, ",") {
			if p = strings.TrimSpace(p); p != "" {
				*l = append(*l, p)
			}
		}
		return nil
	case yaml.SequenceNode:
		var s []string
		if err := n.Decode(&s); err != nil {
			return err
		}
		*l = s
		return nil
	}
	return fmt.Errorf("line %d: expected a list or a comma-separated string", n.Line)
}

// EnvRef returns the runtime's required-variable syntax for name.
func EnvRef(name string) string {
	return fmt.Sprintf("${%s:?%s is not set}", name, name)
}

// Default returns a server config for the public backend with every secret
// taken from the environment.
func Default() Config {
	return Config{
		Version:  1,
		Services: Services{SocketURI: DefaultSocketURI, ServerDBURI: DefaultServerDBURI},
		Identity: Identity{DiscordID: EnvRef(EnvDiscordID)},
		Auth:     Auth{HTTPURI: DefaultHTTPURI, HTTPKey: DefaultHTTPKey, Password: EnvRef(EnvPassword)},
		Network:  Network{TickRateHz: DefaultTickRate},
	}
}

// RootDefault returns a two-file runtime's server config for EchoVRCE: no
// services or HTTP endpoints (the runtime's built-in ones are EchoVRCE's),
// the account from the environment.
func RootDefault() Config {
	return Config{
		Version:  1,
		Identity: Identity{DiscordID: EnvRef(EnvDiscordID)},
		Auth:     Auth{Password: EnvRef(EnvPassword)},
		Network:  Network{TickRateHz: DefaultTickRate},
		Root:     true,
	}
}

// ErrRootNeedsPassword is why a two-file runtime cannot start on a device link.
var ErrRootNeedsPassword = errors.New("auth.password is required: nevr-runtime 6.x servers log in with the host account's Discord ID and password, and refuse a device link alone (nevr-runtime #1339)")

// Validate mirrors the checks that make a server exit at boot (v5.4.2, and
// v6.6.0 for Root), so they surface in the TUI instead of in a crash loop.
func (c Config) Validate() error {
	var errs []error
	if c.Root {
		if c.Auth.Password == "" && !c.Services.AllowOfflineServer {
			errs = append(errs, ErrRootNeedsPassword)
		}
	} else if c.Services.SocketURI == "" && !c.Services.AllowOfflineServer {
		errs = append(errs, errors.New("services.socket_uri is required for a dedicated server"))
	}
	for name, v := range map[string]string{"services.socket_uri": c.Services.SocketURI, "services.serverdb_uri": c.Services.ServerDBURI} {
		if v == "" {
			continue
		}
		if u, err := url.Parse(v); err != nil || (u.Scheme != "ws" && u.Scheme != "wss") {
			errs = append(errs, fmt.Errorf("%s must be a ws:// or wss:// URL, got %q", name, v))
		}
	}
	if c.Identity.DiscordID == "" {
		errs = append(errs, errors.New("identity.discord_id is required"))
	}
	if !c.Root && (c.Auth.HTTPURI == "" || c.Auth.HTTPKey == "") {
		errs = append(errs, errors.New("auth.http_uri and auth.http_key are required for ServerDB sign-in"))
	}
	// Without a password a 5.x server signs in with the device link
	// (_local/.credentials.json). That works for accounts without a password;
	// with one set, the /spr login answered HTTP 400 (tested with v5.4.2).
	if !c.Root && c.Auth.Password == "" && !c.Services.AllowOfflineServer && !c.DeviceLinked {
		errs = append(errs, errors.New("auth.password is required for a dedicated server"))
	}
	if r := c.Network.TickRateHz; r != 0 && (r < 10 || r > 1000) {
		errs = append(errs, fmt.Errorf("network.tick_rate_hz must be 0 or 10-1000, got %d", r))
	}
	if t := c.Network.EmptyServerTTLSeconds; t < 0 || t > 86400 {
		errs = append(errs, fmt.Errorf("network.empty_server_ttl_seconds must be 0-86400, got %d", t))
	}
	if !sessionRouters[c.Network.SessionRouter] {
		errs = append(errs, fmt.Errorf("network.session_router must be legacy, config or all, got %q", c.Network.SessionRouter))
	}
	for name, ip := range map[string]string{"network.internal_ip": c.Network.InternalIP, "network.external_ip": c.Network.ExternalIP} {
		if ip != "" && net.ParseIP(ip).To4() == nil {
			errs = append(errs, fmt.Errorf("%s must be an IPv4 address, got %q", name, ip))
		}
	}
	for _, r := range c.Network.Regions {
		if strings.TrimSpace(r) == "" || strings.Contains(r, ",") {
			errs = append(errs, fmt.Errorf("network.regions entry %q is invalid", r))
		}
	}
	return errors.Join(errs...)
}

// SetMatchmaking sets the regions and guilds a server offers itself for. Nakama
// may only read them from the login (/spr) URL, so they are added there as well
// as to network.regions/guilds, which the runtime appends to serverdb_uri.
func (c *Config) SetMatchmaking(regions, guilds []string) error {
	c.Network.Regions, c.Guilds = regions, guilds
	if c.Services.SocketURI == "" {
		return nil // the built-in login address; the runtime sends network.regions and guilds itself
	}
	u, err := url.Parse(c.Services.SocketURI)
	if err != nil {
		return fmt.Errorf("services.socket_uri: %w", err)
	}
	q := u.Query()
	setList := func(key string, v []string) {
		if len(v) == 0 {
			q.Del(key)
		} else {
			q.Set(key, strings.Join(v, ","))
		}
	}
	setList("regions", regions)
	setList("guilds", guilds)
	u.RawQuery = q.Encode()
	c.Services.SocketURI = u.String()
	return nil
}

// Render validates c and returns the YAML document.
func (c Config) Render() ([]byte, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	buf.WriteString("# Generated by evr tooling. Edits are overwritten on the next start;\n# change the instance settings instead.\n")
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(c); err != nil {
		return nil, err
	}
	return buf.Bytes(), enc.Close()
}

// Parse reads a config.yaml, rejecting unknown top-level keys the same way the
// runtime does.
func Parse(data []byte) (Config, error) {
	var top map[string]yaml.Node
	if err := yaml.Unmarshal(data, &top); err != nil {
		return Config{}, err
	}
	for k := range top {
		if !allowedTopLevel[k] && !strings.HasPrefix(k, "x-") {
			return Config{}, fmt.Errorf("unknown top-level key %q (the runtime refuses to start)", k)
		}
	}
	var c Config
	err := yaml.Unmarshal(data, &c)
	return c, err
}

var allowedTopLevel = map[string]bool{
	"version": true, "identity": true, "auth": true, "services": true, "network": true, "assets": true,
	"arena": true, "guilds": true, "telemetry": true, "plugins": true, "profiles": true, "social": true, "diagnostics": true,
}
