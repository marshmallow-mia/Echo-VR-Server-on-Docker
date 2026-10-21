package notify

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/status"
)

// Observation is one server as an agent sees it on a poll.
type Observation struct {
	Name    string
	State   string // the product's one-word state ("failed", "registered", …)
	Status  status.Status
	Running bool // container/process is up
	Enabled bool // expected to be running
	Port    int
}

// Watcher turns successive observations into alerts: a server stopping on
// an error, restarting in a loop, staying down, and recovering.
type Watcher struct {
	Notifier *Notifier
	Host     string
	Log      func(string)

	mu   sync.Mutex
	seen map[string]*memory
}

type memory struct {
	state     string
	restarts  []time.Time
	lastRest  int
	downSince time.Time
	problem   bool
}

func (w *Watcher) log(f string, a ...any) {
	if w.Log != nil {
		w.Log(fmt.Sprintf(f, a...))
	}
}

// Observe processes one poll. The first observation of a server only sets
// the baseline.
func (w *Watcher) Observe(ctx context.Context, obs []Observation) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.seen == nil {
		w.seen = map[string]*memory{}
	}
	now := time.Now()
	for _, o := range obs {
		m := w.seen[o.Name]
		if m == nil {
			w.seen[o.Name] = &memory{state: o.State, lastRest: o.Status.Restarts}
			continue
		}
		st := o.Status
		fields := map[string]string{"Server": o.Name, "Port": fmt.Sprint(o.Port), "Runtime": st.RuntimeVersion}
		send := func(ev Event) {
			ev.Host, ev.Fields = w.Host, fields
			if err := w.Notifier.Send(ctx, ev); err != nil {
				w.log("webhook: %v", err)
			}
		}
		if o.State != m.state {
			w.log("%s: %s → %s", o.Name, m.state, o.State)
			switch {
			case o.State == "failed":
				kind, title := EventServerFailed, o.Name+" stopped: "+st.Reason
				if st.Reason == "bad_credentials" {
					kind, title = EventCredentialsFailed, o.Name+": the backend rejected the account"
				}
				m.problem = true
				send(Event{Kind: kind, Level: Critical, Title: title, Text: st.LastError + "\nFix the settings, then restart the server."})
			case o.State == "registered" && m.problem:
				m.problem = false
				send(Event{Kind: EventServerRecovered, Level: Good, Title: o.Name + " is back", Text: fmt.Sprintf("Registered as %s:%d.", st.PublicIP, st.Port)})
			}
			m.state = o.State
		}
		if st.Restarts < m.lastRest { // container recreated: counters start over
			m.lastRest = 0
		}
		for i := m.lastRest; i < st.Restarts; i++ {
			m.restarts = append(m.restarts, now)
		}
		m.lastRest = st.Restarts
		recent := m.restarts[:0]
		for _, t := range m.restarts {
			if now.Sub(t) < 15*time.Minute {
				recent = append(recent, t)
			}
		}
		m.restarts = recent
		// Servers restart after every match; only quick restarts without a
		// single match and with an error count as a loop.
		if len(recent) >= 5 && st.SessionsTotal == 0 && st.LastError != "" {
			m.problem = true
			send(Event{Kind: EventCrashLoop, Level: Warning, Title: o.Name + " keeps restarting",
				Text: fmt.Sprintf("%d restarts in 15 minutes. Last error: %s", len(recent), st.LastError)})
		}
		if !o.Running && o.Enabled {
			if m.downSince.IsZero() {
				m.downSince = now
			} else if now.Sub(m.downSince) > time.Minute && !m.problem {
				m.problem = true
				send(Event{Kind: EventServerDown, Level: Critical, Title: o.Name + " is not running", Text: "State: " + o.State})
			}
		} else {
			m.downSince = time.Time{}
		}
	}
}
