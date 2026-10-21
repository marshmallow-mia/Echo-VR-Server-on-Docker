// Package agent is evr-docker's background service: it serves Prometheus
// metrics, sends Discord alerts on state changes and runs runtime updates.
package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/internal/core"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/internal/hostcfg"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/metrics"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/notify"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/status"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/update"
)

// Agent watches the servers of one evr-docker host.
type Agent struct {
	Dir string
	Log func(string)

	mu      sync.Mutex
	samples []metrics.Sample
	seen    map[string]*memory
	host    string
}

type memory struct {
	state     string
	restarts  []time.Time
	lastRest  int
	downSince time.Time
	problem   bool
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
	a.seen = map[string]*memory{}
	a.host, _ = os.Hostname()
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
				hours := app.Cfg.Runtime.CheckHours
				if hours <= 0 {
					hours = 6
				}
				nextUpdate = time.Now().Add(time.Duration(hours) * time.Hour)
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
	n := notifier(app)
	var samples []metrics.Sample
	now := time.Now()
	for _, s := range servers {
		name := s.Instance.Name
		st := status.Status{Instance: name, Port: s.Instance.Port}
		if s.Status != nil {
			st = *s.Status
		}
		samples = append(samples, metrics.Sample{Status: st, Running: s.Running()})
		if _, configured := app.Cfg.Instance(name); !configured {
			continue
		}
		m := a.seen[name]
		if m == nil {
			m = &memory{state: s.State(), lastRest: st.Restarts}
			a.seen[name] = m
			continue
		}
		state := s.State()
		fields := map[string]string{"Server": name, "Port": fmt.Sprint(s.Instance.Port), "Runtime": st.RuntimeVersion}
		send := func(ev notify.Event) {
			ev.Host, ev.Fields = a.host, fields
			if err := n.Send(ctx, ev); err != nil {
				a.log("webhook: %v", err)
			}
		}
		if state != m.state {
			a.log("%s: %s → %s", name, m.state, state)
			switch {
			case state == "failed":
				kind, title := notify.EventServerFailed, name+" stopped: "+st.Reason
				if st.Reason == "bad_credentials" {
					kind, title = notify.EventCredentialsFailed, name+": the backend rejected the account"
				}
				m.problem = true
				send(notify.Event{Kind: kind, Level: notify.Critical, Title: title, Text: st.LastError + "\nFix the settings, then restart the server."})
			case state == "registered" && m.problem:
				m.problem = false
				send(notify.Event{Kind: notify.EventServerRecovered, Level: notify.Good, Title: name + " is back", Text: fmt.Sprintf("Registered as %s:%d.", st.PublicIP, st.Port)})
			}
			m.state = state
		}
		if st.Restarts > m.lastRest {
			for i := m.lastRest; i < st.Restarts; i++ {
				m.restarts = append(m.restarts, now)
			}
		}
		m.lastRest = st.Restarts
		recent := m.restarts[:0]
		for _, t := range m.restarts {
			if now.Sub(t) < 15*time.Minute {
				recent = append(recent, t)
			}
		}
		m.restarts = recent
		// Normal operation restarts once per match; flag only quick, failed restarts.
		if len(recent) >= 5 && st.SessionsTotal == 0 && st.LastError != "" {
			m.problem = true
			send(notify.Event{Kind: notify.EventCrashLoop, Level: notify.Warning, Title: name + " keeps restarting",
				Text: fmt.Sprintf("%d restarts in 15 minutes. Last error: %s", len(recent), st.LastError)})
		}
		if !s.Running() && !s.Instance.Disabled {
			if m.downSince.IsZero() {
				m.downSince = now
			} else if now.Sub(m.downSince) > time.Minute && !m.problem {
				m.problem = true
				send(notify.Event{Kind: notify.EventServerDown, Level: notify.Critical, Title: name + " is not running", Text: "Container state: " + state})
			}
		} else {
			m.downSince = time.Time{}
		}
	}
	a.mu.Lock()
	a.samples = samples
	a.mu.Unlock()
}

func (a *Agent) checkUpdates(ctx context.Context, app *core.App) {
	u := Updater(app, func(s string) { a.log("%s", s) })
	pol := update.Policy{Channel: app.Cfg.Runtime.Channel, Pinned: app.Cfg.Runtime.Pinned}
	n := notifier(app)
	if !app.Cfg.Runtime.AutoUpdate {
		cur, target, available, err := u.Check(ctx, pol)
		if err == nil && available {
			_ = n.Send(ctx, notify.Event{Kind: notify.EventUpdateAvailable, Level: notify.Info, Host: a.host,
				Title: "nevr-runtime " + target.Tag + " is available", Text: "Current: " + cur + ". Install it with `evr-docker update apply`."})
		}
		return
	}
	res, err := u.Run(ctx, pol)
	if err != nil {
		a.log("update: %v", err)
		return
	}
	switch res.Action {
	case "switched":
		_ = n.Send(ctx, notify.Event{Kind: notify.EventUpdateApplied, Level: notify.Good, Host: a.host,
			Title: "nevr-runtime " + res.To + " rolled out", Text: fmt.Sprintf("Canary %s registered; the other servers switch at their next match boundary (was %s).", res.Canary, res.From)})
	case "rolled_back":
		_ = n.Send(ctx, notify.Event{Kind: notify.EventUpdateRolledBack, Level: notify.Warning, Host: a.host,
			Title: "nevr-runtime " + res.To + " failed on " + res.Canary, Text: "Staying on " + res.From + ". " + res.Detail})
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
