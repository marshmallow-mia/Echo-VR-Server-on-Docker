// Package instance holds the per-server settings both products (Docker and
// Pterodactyl) translate into evr-supervisor's environment.
package instance

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Settings configure one game server. Zero values mean "use the default".
type Settings struct {
	Regions         []string `yaml:"regions,omitempty,flow" json:"regions,omitempty"`
	Guilds          []string `yaml:"guilds,omitempty,flow" json:"guilds,omitempty"`
	TickRate        int      `yaml:"tick_rate,omitempty" json:"tick_rate,omitempty"`
	EmptyTTL        int      `yaml:"empty_ttl,omitempty" json:"empty_ttl,omitempty"`
	PublisherLock   string   `yaml:"publisher_lock,omitempty" json:"publisher_lock,omitempty"`
	Runtime         string   `yaml:"runtime,omitempty" json:"runtime,omitempty"` // tag or "current"
	SocketURI       string   `yaml:"socket_uri,omitempty" json:"socket_uri,omitempty"`
	ServerDBURI     string   `yaml:"serverdb_uri,omitempty" json:"serverdb_uri,omitempty"`
	HTTPURI         string   `yaml:"http_uri,omitempty" json:"http_uri,omitempty"`
	RegisterTimeout int      `yaml:"register_timeout,omitempty" json:"register_timeout,omitempty"`
	LogAll          bool     `yaml:"log_all,omitempty" json:"log_all,omitempty"`
}

// Merge returns s with every non-zero field of over applied.
func (s Settings) Merge(over Settings) Settings {
	if over.Regions != nil {
		s.Regions = over.Regions
	}
	if over.Guilds != nil {
		s.Guilds = over.Guilds
	}
	setInt(&s.TickRate, over.TickRate)
	setInt(&s.EmptyTTL, over.EmptyTTL)
	setInt(&s.RegisterTimeout, over.RegisterTimeout)
	setStr(&s.PublisherLock, over.PublisherLock)
	setStr(&s.Runtime, over.Runtime)
	setStr(&s.SocketURI, over.SocketURI)
	setStr(&s.ServerDBURI, over.ServerDBURI)
	setStr(&s.HTTPURI, over.HTTPURI)
	s.LogAll = s.LogAll || over.LogAll
	return s
}

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}$`)
var tagRe = regexp.MustCompile(`^(current|v\d+\.\d+\.\d+(-[0-9A-Za-z.]+)?)$`)

// ValidName reports whether name can be used as an instance name (it becomes
// part of a container name and a directory).
func ValidName(name string) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("instance name %q: use 1-31 lowercase letters, digits and dashes", name)
	}
	return nil
}

// Validate checks the values the supervisor would reject.
func (s Settings) Validate() error {
	if s.TickRate != 0 && (s.TickRate < 10 || s.TickRate > 1000) {
		return fmt.Errorf("tick rate must be 10-1000, got %d", s.TickRate)
	}
	if s.EmptyTTL < 0 || s.EmptyTTL > 86400 {
		return fmt.Errorf("empty server TTL must be 0-86400 seconds, got %d", s.EmptyTTL)
	}
	if s.Runtime != "" && !tagRe.MatchString(s.Runtime) {
		return fmt.Errorf("runtime must be \"current\" or a tag like v5.4.2, got %q", s.Runtime)
	}
	for _, l := range [][]string{s.Regions, s.Guilds} {
		for _, v := range l {
			if v == "" || strings.ContainsAny(v, ", ") {
				return fmt.Errorf("invalid list entry %q", v)
			}
		}
	}
	return nil
}

// Env returns the evr-supervisor environment for these settings (secrets and
// the port are added by the caller).
func (s Settings) Env() []string {
	var env []string
	add := func(k, v string) {
		if v != "" {
			env = append(env, k+"="+v)
		}
	}
	add("NEVR_REGIONS", strings.Join(s.Regions, ","))
	add("NEVR_GUILDS", strings.Join(s.Guilds, ","))
	if s.TickRate != 0 {
		add("NEVR_TICK_RATE", strconv.Itoa(s.TickRate))
	}
	if s.EmptyTTL != 0 {
		add("NEVR_EMPTY_TTL", strconv.Itoa(s.EmptyTTL))
	}
	add("NEVR_PUBLISHER_LOCK", s.PublisherLock)
	add("NEVR_VERSION", s.Runtime)
	add("NEVR_SOCKET_URI", s.SocketURI)
	add("NEVR_SERVERDB_URI", s.ServerDBURI)
	add("NEVR_HTTP_URI", s.HTTPURI)
	if s.RegisterTimeout != 0 {
		add("EVR_REGISTER_TIMEOUT", strconv.Itoa(s.RegisterTimeout))
	}
	if s.LogAll {
		add("EVR_LOG", "all")
	}
	return env
}

func setInt(dst *int, v int) {
	if v != 0 {
		*dst = v
	}
}

func setStr(dst *string, v string) {
	if v != "" {
		*dst = v
	}
}
