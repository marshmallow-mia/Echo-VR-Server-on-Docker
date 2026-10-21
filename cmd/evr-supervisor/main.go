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
  NEVR_DISCORD_ID, NEVR_PASSWORD, NEVR_HTTP_KEY   EchoVRCE host account (required)
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
		return o, nil
	}
	for _, k := range []string{config.EnvDiscordID, config.EnvPassword, config.EnvHTTPKey} {
		if strings.TrimSpace(os.Getenv(k)) == "" {
			return o, fmt.Errorf("%s is not set (EchoVRCE host account)", k)
		}
	}
	c := config.Default()
	c.Services.SocketURI = env("NEVR_SOCKET_URI", c.Services.SocketURI)
	c.Services.ServerDBURI = env("NEVR_SERVERDB_URI", c.Services.ServerDBURI)
	c.Auth.HTTPURI = env("NEVR_HTTP_URI", c.Auth.HTTPURI)
	if c.Network.TickRateHz, err = intEnv("NEVR_TICK_RATE", config.DefaultTickRate); err != nil {
		return o, err
	}
	if c.Network.EmptyServerTTLSeconds, err = intEnv("NEVR_EMPTY_TTL", 0); err != nil {
		return o, err
	}
	if err := c.SetMatchmaking(list("NEVR_REGIONS"), list("NEVR_GUILDS")); err != nil {
		return o, err
	}
	o.ConfigYAML, err = c.Render()
	return o, err
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
