// Package updates checks and applies updates of every part of an Echo VR
// host (the tool itself, the game files, nevr-runtime, and on panel hosts the
// panel and Wings) under one set of user settings: per part off, notify or
// auto, an optional time window for automatic installs, and a check interval.
package updates

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Mode is what happens when a part has an update.
type Mode string

const (
	Off    Mode = "off"    // not checked
	Notify Mode = "notify" // checked; shown in the TUI and sent as an alert
	Auto   Mode = "auto"   // installed automatically (inside the window)
)

// ParseMode validates a mode; empty means def.
func ParseMode(s string, def Mode) (Mode, error) {
	switch m := Mode(strings.ToLower(strings.TrimSpace(s))); m {
	case "":
		return def, nil
	case Off, Notify, Auto:
		return m, nil
	}
	return "", fmt.Errorf("update mode must be off, notify or auto, got %q", s)
}

// Status is the result of one check.
type Status struct {
	Component string    `json:"component"`
	Installed string    `json:"installed"`
	Latest    string    `json:"latest,omitempty"`
	Available bool      `json:"available"`
	Notes     string    `json:"notes,omitempty"`
	Error     string    `json:"error,omitempty"`
	Checked   time.Time `json:"checked"`
}

// Component is one updatable part.
type Component interface {
	// Name is the stable key used in settings and records, e.g. "runtime".
	Name() string
	// Title is shown to people, e.g. "nevr-runtime".
	Title() string
	Check(ctx context.Context) (Status, error)
	// Versions lists what Apply accepts, newest first (may be nil).
	Versions(ctx context.Context) ([]string, error)
	// Apply installs version ("" = what Check reported as latest).
	Apply(ctx context.Context, version string, say func(string)) error
}

// Settings are the user's choices.
type Settings struct {
	CheckHours int
	Window     string // "HH:MM-HH:MM", may cross midnight; empty = any time
	Modes      map[string]Mode
}

// Mode returns the mode of a component (notify when unset).
func (s Settings) Mode(name string) Mode {
	if m, ok := s.Modes[name]; ok && m != "" {
		return m
	}
	return Notify
}

// Interval is the time between checks.
func (s Settings) Interval() time.Duration {
	if s.CheckHours <= 0 {
		return 6 * time.Hour
	}
	return time.Duration(s.CheckHours) * time.Hour
}

// ValidWindow checks a window string.
func ValidWindow(w string) error {
	_, err := InWindow(w, time.Now())
	return err
}

// InWindow reports whether now is inside "HH:MM-HH:MM" (local time).
func InWindow(w string, now time.Time) (bool, error) {
	if strings.TrimSpace(w) == "" {
		return true, nil
	}
	a, b, ok := strings.Cut(w, "-")
	if !ok {
		return false, fmt.Errorf("update window must look like 04:00-06:00, got %q", w)
	}
	parse := func(s string) (int, error) {
		t, err := time.Parse("15:04", strings.TrimSpace(s))
		if err != nil {
			return 0, fmt.Errorf("update window must look like 04:00-06:00, got %q", w)
		}
		return t.Hour()*60 + t.Minute(), nil
	}
	from, err := parse(a)
	if err != nil {
		return false, err
	}
	to, err := parse(b)
	if err != nil {
		return false, err
	}
	m := now.Hour()*60 + now.Minute()
	if from <= to {
		return m >= from && m < to, nil
	}
	return m >= from || m < to, nil
}

// Record is what the last checks found; the TUI reads it without going to
// the network.
type Record struct {
	Checked  time.Time         `json:"checked"`
	Status   []Status          `json:"status"`
	Notified map[string]string `json:"notified,omitempty"` // component → version an alert was sent for
}

// RecordFile is the record's name next to the configuration.
const RecordFile = "updates.json"

// LoadRecord reads the record; a missing or broken one is empty.
func LoadRecord(dir string) Record {
	var r Record
	if b, err := os.ReadFile(filepath.Join(dir, RecordFile)); err == nil {
		_ = json.Unmarshal(b, &r)
	}
	if r.Notified == nil {
		r.Notified = map[string]string{}
	}
	return r
}

// Save writes the record.
func (r Record) Save(dir string) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, "."+RecordFile+".tmp")
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, RecordFile))
}

// Get returns the status of one component.
func (r Record) Get(name string) (Status, bool) {
	for _, s := range r.Status {
		if s.Component == name {
			return s, true
		}
	}
	return Status{}, false
}

// Available lists the titles of components with an update.
func (r Record) Available() []string {
	var out []string
	for _, s := range r.Status {
		if s.Available {
			out = append(out, s.Component)
		}
	}
	return out
}

func (r *Record) put(st Status) {
	for i, s := range r.Status {
		if s.Component == st.Component {
			r.Status[i] = st
			return
		}
	}
	r.Status = append(r.Status, st)
}

// Event is something the scheduler reports (alerts, logs).
type Event struct {
	Kind      string // available, applied, failed
	Component Component
	Status    Status
	Err       error
}

// Event kinds.
const (
	EventAvailable = "available"
	EventApplied   = "applied"
	EventFailed    = "failed"
)

// CheckOne checks c and stores the result in the record at dir.
func CheckOne(ctx context.Context, dir string, c Component) Status {
	st := check(ctx, c)
	rec := LoadRecord(dir)
	rec.put(st)
	rec.Checked = st.Checked
	_ = rec.Save(dir)
	return st
}

// CheckAll checks every component that is not off and stores the results.
func CheckAll(ctx context.Context, dir string, comps []Component, set Settings) []Status {
	rec := LoadRecord(dir)
	var out []Status
	for _, c := range comps {
		if set.Mode(c.Name()) == Off {
			continue
		}
		st := check(ctx, c)
		rec.put(st)
		out = append(out, st)
	}
	rec.Checked = time.Now()
	_ = rec.Save(dir)
	return out
}

func check(ctx context.Context, c Component) Status {
	st, err := c.Check(ctx)
	st.Component, st.Checked = c.Name(), time.Now()
	if err != nil {
		st.Error, st.Available = err.Error(), false
	}
	return st
}

// Scheduler runs the periodic checks of an agent.
type Scheduler struct {
	Dir   string
	Event func(Event)
	Log   func(string)
	Now   func() time.Time
}

func (s *Scheduler) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Scheduler) log(f string, a ...any) {
	if s.Log != nil {
		s.Log(fmt.Sprintf(f, a...))
	}
}

// Run checks every component once: it alerts once per new version, and
// installs it when the component is on auto and the window is open.
func (s *Scheduler) Run(ctx context.Context, comps []Component, set Settings) Record {
	rec := LoadRecord(s.Dir)
	open, err := InWindow(set.Window, s.now())
	if err != nil {
		s.log("%v; automatic updates wait until it is fixed", err)
		open = false
	}
	for _, c := range comps {
		mode := set.Mode(c.Name())
		if mode == Off {
			continue
		}
		st := check(ctx, c)
		if st.Error != "" {
			s.log("%s: check failed: %s", c.Title(), st.Error)
		}
		if st.Available && rec.Notified[c.Name()] != st.Latest {
			rec.Notified[c.Name()] = st.Latest
			s.emit(Event{Kind: EventAvailable, Component: c, Status: st})
		}
		if st.Available && mode == Auto && open {
			s.log("%s: installing %s", c.Title(), st.Latest)
			if err := c.Apply(ctx, "", func(l string) { s.log("%s: %s", c.Title(), l) }); err != nil {
				s.emit(Event{Kind: EventFailed, Component: c, Status: st, Err: err})
			} else {
				s.emit(Event{Kind: EventApplied, Component: c, Status: st})
				st = check(ctx, c)
			}
		}
		rec.put(st)
	}
	rec.Checked = s.now()
	if err := rec.Save(s.Dir); err != nil {
		s.log("saving %s: %v", RecordFile, err)
	}
	return rec
}

func (s *Scheduler) emit(e Event) {
	if s.Event != nil {
		s.Event(e)
	}
}

// ErrNotHere means a component does not exist on this host (e.g. Wings on a
// host without Wings); such components are left out of the list.
var ErrNotHere = errors.New("not installed on this host")
