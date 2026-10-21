package updates

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fake struct {
	name, installed, latest string
	applied                 []string
	fail                    bool
}

func (f *fake) Name() string  { return f.name }
func (f *fake) Title() string { return f.name }
func (f *fake) Check(context.Context) (Status, error) {
	return Status{Installed: f.installed, Latest: f.latest, Available: f.latest != f.installed}, nil
}
func (f *fake) Versions(context.Context) ([]string, error) { return []string{f.latest}, nil }
func (f *fake) Apply(_ context.Context, v string, _ func(string)) error {
	if f.fail {
		return errors.New("boom")
	}
	f.applied = append(f.applied, v)
	f.installed = f.latest
	return nil
}

func TestWindow(t *testing.T) {
	at := func(h, m int) time.Time { return time.Date(2026, 1, 1, h, m, 0, 0, time.Local) }
	for _, c := range []struct {
		w    string
		t    time.Time
		want bool
	}{{"", at(12, 0), true}, {"04:00-06:00", at(5, 0), true}, {"04:00-06:00", at(6, 0), false}, {"23:00-02:00", at(1, 30), true}, {"23:00-02:00", at(12, 0), false}} {
		got, err := InWindow(c.w, c.t)
		if err != nil || got != c.want {
			t.Errorf("%q at %s: %v %v", c.w, c.t.Format("15:04"), got, err)
		}
	}
	if _, err := InWindow("4-6", time.Now()); err == nil {
		t.Error("bad window accepted")
	}
}

func TestSchedulerModes(t *testing.T) {
	dir := t.TempDir()
	auto := &fake{name: "runtime", installed: "v1", latest: "v2"}
	notify := &fake{name: "self", installed: "v1", latest: "v2"}
	off := &fake{name: "game", installed: "a", latest: "b"}
	var events []string
	s := &Scheduler{Dir: dir, Event: func(e Event) { events = append(events, e.Kind+":"+e.Component.Name()) }}
	set := Settings{Modes: map[string]Mode{"runtime": Auto, "self": Notify, "game": Off}}
	rec := s.Run(context.Background(), []Component{auto, notify, off}, set)
	if len(auto.applied) != 1 || len(notify.applied) != 0 || len(off.applied) != 0 {
		t.Fatalf("applied auto=%v notify=%v off=%v", auto.applied, notify.applied, off.applied)
	}
	if st, _ := rec.Get("runtime"); st.Available || st.Installed != "v2" {
		t.Errorf("runtime after auto: %+v", st)
	}
	if _, ok := rec.Get("game"); ok {
		t.Error("an off component was checked")
	}
	want := []string{"available:runtime", "applied:runtime", "available:self"}
	if len(events) != len(want) {
		t.Fatalf("events %v", events)
	}
	// A second run does not alert again for the same version.
	events = nil
	s.Run(context.Background(), []Component{auto, notify, off}, set)
	if len(events) != 0 {
		t.Errorf("repeated alerts %v", events)
	}
	if got := LoadRecord(dir).Available(); len(got) != 1 || got[0] != "self" {
		t.Errorf("available %v", got)
	}
}

func TestSchedulerWindowClosed(t *testing.T) {
	c := &fake{name: "runtime", installed: "v1", latest: "v2"}
	s := &Scheduler{Dir: t.TempDir(), Now: func() time.Time { return time.Date(2026, 1, 1, 12, 0, 0, 0, time.Local) }}
	s.Run(context.Background(), []Component{c}, Settings{Window: "04:00-06:00", Modes: map[string]Mode{"runtime": Auto}})
	if len(c.applied) != 0 {
		t.Error("installed outside the window")
	}
}
