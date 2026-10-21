// Package agent is evr-docker's background service: it serves Prometheus
// metrics, sends Discord alerts on state changes and runs runtime updates.
package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/internal/core"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/internal/hostcfg"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/metrics"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/notify"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/selfupdate"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/status"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/update"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/updates"
)

// Agent watches the servers of one evr-docker host.
type Agent struct {
	Dir string
	Log func(string)

	mu      sync.Mutex
	samples []metrics.Sample
	watcher notify.Watcher
	host    string
}

func (a *Agent) log(f string, args ...any) {
	if a.Log != nil {
		a.Log(fmt.Sprintf(f, args...))
	}
}

// Run blocks until ctx ends.
func (a *Agent) Run(ctx context.Context) error {
	app, err := core.Open(a.Dir)
	if err != nil {
		return err
	}
	a.host, _ = os.Hostname()
	a.watcher.Host, a.watcher.Log = a.host, func(s string) { a.log("%s", s) }
	if app.Cfg.Metrics.Listen != "" {
		mux := http.NewServeMux()
		mux.Handle("/metrics", metrics.Handler(a.snapshot))
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok\n")) })
		srv := &http.Server{Addr: app.Cfg.Metrics.Listen, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
		go func() {
			a.log("metrics on http://%s/metrics", app.Cfg.Metrics.Listen)
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				a.log("metrics server: %v", err)
			}
		}()
		defer srv.Close()
	}
	poll := time.NewTicker(10 * time.Second)
	defer poll.Stop()
	nextUpdate := time.Now().Add(2 * time.Minute)
	for {
		app, err := core.Open(a.Dir) // pick up config changes
		if err != nil {
			a.log("config: %v", err)
		} else {
			a.watch(ctx, app)
			if time.Now().After(nextUpdate) {
				a.checkUpdates(ctx, app)
				nextUpdate = time.Now().Add(app.Cfg.Updates.Settings().Interval())
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-poll.C:
		}
	}
}

func (a *Agent) snapshot() []metrics.Sample {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]metrics.Sample(nil), a.samples...)
}

func notifier(app *core.App) *notify.Notifier {
	return &notify.Notifier{URL: app.Secrets[hostcfg.SecretWebhook], Events: app.Cfg.Notify.Events}
}

func (a *Agent) watch(ctx context.Context, app *core.App) {
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	servers, err := app.Servers(cctx)
	if err != nil {
		a.log("listing servers: %v", err)
		return
	}
	var samples []metrics.Sample
	var obs []notify.Observation
	for _, s := range servers {
		st := status.Status{Instance: s.Instance.Name, Port: s.Instance.Port}
		if s.Status != nil {
			st = *s.Status
		}
		samples = append(samples, metrics.Sample{Status: st, Running: s.Running()})
		if _, configured := app.Cfg.Instance(s.Instance.Name); configured {
			obs = append(obs, notify.Observation{Name: s.Instance.Name, State: s.State(), Status: st, Running: s.Running(), Enabled: !s.Instance.Disabled, Port: s.Instance.Port})
		}
	}
	a.watcher.Notifier = notifier(app)
	a.watcher.Observe(ctx, obs)
	a.mu.Lock()
	a.samples = samples
	a.mu.Unlock()
}

func (a *Agent) checkUpdates(ctx context.Context, app *core.App) {
	n := notifier(app)
	sched := &updates.Scheduler{Dir: app.Dir, Log: func(s string) { a.log("%s", s) }, Event: func(e updates.Event) {
		_ = n.Send(ctx, UpdateEvent(e, a.host))
	}}
	sched.Run(ctx, Components(app, func(s string) { a.log("%s", s) }), app.Cfg.Updates.Settings())
}

// UpdateEvent turns a scheduler event into a Discord alert.
func UpdateEvent(e updates.Event, host string) notify.Event {
	title, st := e.Component.Title(), e.Status
	switch e.Kind {
	case updates.EventApplied:
		return notify.Event{Kind: notify.EventUpdateApplied, Level: notify.Good, Host: host, Title: title + " updated to " + st.Latest,
			Text: "Installed automatically (was " + st.Installed + ")."}
	case updates.EventFailed:
		return notify.Event{Kind: notify.EventUpdateRolledBack, Level: notify.Warning, Host: host, Title: title + " " + st.Latest + " was not installed",
			Text: e.Err.Error() + "\nStaying on " + st.Installed + "."}
	}
	return notify.Event{Kind: notify.EventUpdateAvailable, Level: notify.Info, Host: host, Title: title + " " + st.Latest + " is available",
		Text: "Installed: " + st.Installed + ". Install it in evr-docker (U: updates) or with `evr-docker update apply " + e.Component.Name() + "`."}
}

// Components are the parts evr-docker updates: itself, the game files and
// nevr-runtime.
func Components(app *core.App, log func(string)) []updates.Component {
	self := &updates.Self{U: &selfupdate.Updater{Repo: core.Repo, Binary: "evr-docker", Current: core.Version}, After: PostUpdate(app.Dir)}
	game := &updates.Game{In: app.Games(nil)}
	rt := &updates.Runtime{U: Updater(app, log), Policy: func() update.Policy {
		return update.Policy{Channel: app.Cfg.Runtime.Channel, Pinned: app.Cfg.Runtime.Pinned}
	}}
	return []updates.Component{self, game, rt}
}

// PostUpdate runs the new binary's post-update step and restarts the agent
// service, so both use the new version.
func PostUpdate(dir string) func(ctx context.Context, say func(string)) error {
	return func(ctx context.Context, say func(string)) error {
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		if exe, err = filepath.EvalSymlinks(exe); err != nil {
			return err
		}
		cmd := exec.CommandContext(ctx, exe, "--config-dir", dir, "post-update")
		out, err := cmd.CombinedOutput()
		for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if l != "" {
				say(l)
			}
		}
		if err != nil {
			return fmt.Errorf("post-update: %w", err)
		}
		if ServiceInstalled() {
			// --no-block: when the agent itself runs this, it is the one restarted.
			_ = exec.Command("systemctl", "restart", "--no-block", "evr-docker-agent.service").Run()
			say("background service restarting with the new version")
		}
		return nil
	}
}

// Updater returns an updater wired to the Docker fleet.
func Updater(app *core.App, log func(string)) *update.Updater {
	return &update.Updater{Store: app.Store(), Fleet: fleet{dir: app.Dir}, Log: log}
}

// fleet pins a canary by setting its runtime in the config and recreating it.
type fleet struct{ dir string }

func (f fleet) Statuses(ctx context.Context) ([]status.Status, error) {
	app, err := core.Open(f.dir)
	if err != nil {
		return nil, err
	}
	servers, err := app.Servers(ctx)
	if err != nil {
		return nil, err
	}
	var out []status.Status
	for _, s := range servers {
		if s.Status == nil || !s.Running() {
			continue
		}
		st := *s.Status
		// a status written before the container was (re)created is not this run's
		if s.State() == "starting" {
			st.State = status.StateStarting
		}
		out = append(out, st)
	}
	return out, nil
}

func (f fleet) Pin(ctx context.Context, instance, tag string) error {
	app, err := core.Open(f.dir)
	if err != nil {
		return err
	}
	in, ok := app.Cfg.Instance(instance)
	if !ok {
		return fmt.Errorf("no server named %q", instance)
	}
	in.Settings.Runtime = tag
	if err := app.Save(); err != nil {
		return err
	}
	done, err := app.Apply(ctx, false, nil)
	for _, d := range done {
		if strings.HasPrefix(d, instance+":") && strings.Contains(d, "after its match") {
			return fmt.Errorf("%s is hosting a match", instance)
		}
	}
	return err
}
