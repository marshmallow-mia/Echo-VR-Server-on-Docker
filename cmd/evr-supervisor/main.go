// evr-supervisor is the entrypoint of the Echo VR server image. It is
// configured through environment variables so that plain Docker, compose and
// Pterodactyl eggs can all drive it.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/nevr/config"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/supervisor"
)

var version = "dev"

const usage = `evr-supervisor runs one Echo VR dedicated server (nevr-runtime) under Wine.

Environment:
  EVR_PORT / SERVER_PORT   UDP game port, published 1:1 (required)
  EVR_HTTP_PORT            game HTTP API port on 127.0.0.1 (optional)
  EVR_INSTANCE             instance name (default: hostname)
  EVR_HOME / HOME          writable instance directory
  EVR_GAME_DIR             shared game tree (default /opt/echovr/game/current)
  EVR_NEVR_DIR             runtime directory with one folder per tag (default /opt/echovr/nevr)
  NEVR_VERSION             runtime tag or "current" (default current)
  NEVR_PASSWORD            host account's server password (optional: without it nevr-runtime
                           v6.9.0+ prints a sign-in code on the console to approve once)
  NEVR_DISCORD_ID          host account's Discord ID (needed with NEVR_PASSWORD)
  NEVR_HTTP_KEY            backend HTTP key (default: the public key nevr-runtime embeds)
  NEVR_REGIONS, NEVR_GUILDS                       comma-separated lists (optional)
  NEVR_TICK_RATE (120), NEVR_EMPTY_TTL (0), NEVR_PUBLISHER_LOCK (echovrce)
  NEVR_SOCKET_URI, NEVR_SERVERDB_URI, NEVR_HTTP_URI   backend overrides
  EVR_CONFIG_YAML          use this config.yaml as-is instead of generating one
  EVR_REGISTER_TIMEOUT     seconds until an unregistered server is restarted (180)
  EVR_LOG                  "default" hides noisy runtime lines, "all" shows everything
`

func main() {
	flag.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	showVersion := flag.Bool("version", false, "print the version and exit")
	check := flag.Bool("check", false, "validate the configuration and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}
	opts, err := options()
	if err == nil && *check {
		fmt.Printf("[evr] configuration ok: instance %s, port %d\n", opts.Instance, opts.Port)
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err != nil {
		// Stay up so Docker or Wings do not restart us in a tight loop; the
		// message is visible in the console until the settings are fixed.
		fmt.Printf("[evr] configuration error: %v\n", err)
		if *check {
			os.Exit(2)
		}
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				fmt.Printf("[evr] configuration error: %v (fix it and restart)\n", err)
			}
		}
	}
	fmt.Printf("[evr] evr-supervisor %s\n", version)
	s, err := supervisor.New(opts)
	if err != nil {
		fmt.Printf("[evr] %v\n", err)
		os.Exit(2)
	}
	_ = s.Run(ctx)
}

func options() (supervisor.Options, error) {
	o := supervisor.Options{
		Instance:       env("EVR_INSTANCE", hostname()),
		Home:           env("EVR_HOME", env("HOME", "/home/container")),
		GameDir:        env("EVR_GAME_DIR", "/opt/echovr/game/current"),
		NevrDir:        env("EVR_NEVR_DIR", "/opt/echovr/nevr"),
		Runtime:        env("NEVR_VERSION", "current"),
		PrefixTemplate: env("EVR_PREFIX_TEMPLATE", "/opt/wine-template"),
		PublisherLock:  env("NEVR_PUBLISHER_LOCK", "echovrce"),
		Commands:       os.Stdin,
		ControlFile:    filepath.Join(env("EVR_HOME", env("HOME", "/home/container")), "control"),
		Env:            []string{"USER=" + env("USER", "container")},
	}
	var err error
	if o.Port, err = intEnv("EVR_PORT", intOr(os.Getenv("SERVER_PORT"), 0)); err != nil {
		return o, err
	}
	if o.Port == 0 {
		return o, fmt.Errorf("no game port: set EVR_PORT (or SERVER_PORT)")
	}
	if o.HTTPPort, err = intEnv("EVR_HTTP_PORT", 0); err != nil {
		return o, err
	}
	timeout, err := intEnv("EVR_REGISTER_TIMEOUT", 180)
	if err != nil {
		return o, err
	}
	o.RegisterTimeout = time.Duration(timeout) * time.Second
	if env("EVR_LOG", "default") == "all" {
		o.Filter = func(string) bool { return true }
	}
	if _, err := os.Stat(o.GameDir); err != nil {
		return o, fmt.Errorf("game files not found at %s (mount the shared game tree there)", o.GameDir)
	}

	if path := os.Getenv("EVR_CONFIG_YAML"); path != "" {
		if o.ConfigYAML, err = os.ReadFile(path); err != nil {
			return o, err
		}
		if _, err := config.Parse(o.ConfigYAML); err != nil {
			return o, fmt.Errorf("%s: %w", path, err)
		}
		o.ConfigYAMLRoot = o.ConfigYAML // the operator's own file, for either layout
		return o, nil
	}
	// Without a password a two-file runtime from v6.9.0 on signs in by a code
	// the host approves on the console; a 5.x runtime only with a device link
	// made earlier (_local/.credentials.json).
	noPassword := strings.TrimSpace(os.Getenv(config.EnvPassword)) == ""
	noDiscordID := strings.TrimSpace(os.Getenv(config.EnvDiscordID)) == ""
	linked := false
	if _, err := os.Stat(filepath.Join(o.Home, "root", "_local", ".credentials.json")); err == nil {
		linked = true
	}
	c := config.Default()
	if noPassword {
		c.Auth.Password, c.DeviceLinked = "", linked
		o.DeviceLinked, o.DeviceCode = linked, true
		fmt.Println("[evr] no password set: nevr-runtime v6.9.0+ prints a sign-in code to approve (5.x: the device link in _local, if any)")
	}
	if noDiscordID {
		c.Identity.DiscordID = ""
	}
	c.Services.SocketURI = env("NEVR_SOCKET_URI", c.Services.SocketURI)
	c.Services.ServerDBURI = env("NEVR_SERVERDB_URI", c.Services.ServerDBURI)
	c.Auth.HTTPURI = env("NEVR_HTTP_URI", c.Auth.HTTPURI)
	c.Auth.HTTPKey = env(config.EnvHTTPKey, config.DefaultHTTPKey)
	// A two-file runtime (6.x) has EchoVRCE's addresses and key built in: its
	// file names them only when the operator overrides them.
	r := config.RootDefault()
	r.Services.SocketURI = os.Getenv("NEVR_SOCKET_URI")
	r.Services.ServerDBURI = os.Getenv("NEVR_SERVERDB_URI")
	r.Auth.HTTPURI = os.Getenv("NEVR_HTTP_URI")
	r.Auth.HTTPKey = os.Getenv(config.EnvHTTPKey)
	if c.Network.TickRateHz, err = intEnv("NEVR_TICK_RATE", config.DefaultTickRate); err != nil {
		return o, err
	}
	if c.Network.EmptyServerTTLSeconds, err = intEnv("NEVR_EMPTY_TTL", 0); err != nil {
		return o, err
	}
	r.Network.TickRateHz, r.Network.EmptyServerTTLSeconds = c.Network.TickRateHz, c.Network.EmptyServerTTLSeconds
	regions, guilds := list("NEVR_REGIONS"), list("NEVR_GUILDS")
	if err := c.SetMatchmaking(regions, guilds); err != nil {
		return o, err
	}
	if err := r.SetMatchmaking(regions, guilds); err != nil {
		return o, err
	}
	if o.ConfigYAML, err = c.Render(); err != nil {
		o.ConfigYAML, o.ConfigErr = nil, err.Error()
		if noPassword && !linked {
			o.ConfigErr = "NEVR_PASSWORD is not set: nevr-runtime 5.x servers need the host account's password (v6.9.0 and later sign in by a code to approve)"
		}
	}
	if noPassword {
		r.Auth.Password, r.Identity.DiscordID = "", "" // the device code is the whole credential
	} else if noDiscordID {
		r.Identity.DiscordID = ""
	}
	if o.ConfigYAMLRoot, err = r.Render(); err != nil {
		o.ConfigYAMLRoot, o.RootConfigErr = nil, err.Error()
	}
	return o, nil
}

func env(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

func intEnv(k string, def int) (int, error) {
	v := strings.TrimSpace(os.Getenv(k))
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s=%q is not a number", k, v)
	}
	return n, nil
}

func intOr(s string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return n
	}
	return def
}

func list(k string) []string {
	var out []string
	for _, p := range strings.Split(os.Getenv(k), ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func hostname() string {
	h, _ := os.Hostname()
	return h
}
