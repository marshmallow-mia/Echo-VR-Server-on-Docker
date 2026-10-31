package mods

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/status"
)

// ServerMods is one server's mods as the host tool sees them.
type ServerMods struct {
	Name string
	// Specs are its mods: the fleet's list with its own over it (MergeSpecs).
	Specs []Spec
	// LocalPlugins loads its own DLLs too.
	LocalPlugins bool
	// Runtime is the nEVR tag it runs ("" when unknown: no nEVR check).
	Runtime string
}

// Fleet is how a rollout sees the servers; each tool implements it.
type Fleet interface {
	// Statuses returns the running servers' status.json.
	Statuses(ctx context.Context) ([]status.Status, error)
	ReadLock(server string) (Lock, error)
	// WriteLock stores a server's lock; it places it at its next start.
	WriteLock(server string, l Lock) error
	// Restart restarts an idle server's game now, so it places its lock.
	Restart(ctx context.Context, server string) error
}

// Host resolves, downloads and rolls out the servers' mods.
type Host struct {
	Source Source
	Store  Store
	Fleet  Fleet
	// CanaryTimeout bounds the wait for an idle server and for its start
	// (default 30 minutes); Poll is the status interval (default 10s).
	CanaryTimeout, Poll time.Duration
	Log                 func(string)
}

func (h Host) log(f string, a ...any) {
	if h.Log != nil {
		h.Log(fmt.Sprintf(f, a...))
	}
}

// Planned is one server's lock now and as the catalogues have it.
type Planned struct {
	Server   string
	Old, New Lock
	Problems []Problem
}

// Changed reports whether the server would run something else.
func (p Planned) Changed() bool { return !p.Old.Same(p.New) }

// Plan reads the catalogues and resolves each server's lock. With keep, a mod
// stays at the version its server runs (an edit); without, every mod moves to
// the newest it may run (an update). Versions a rollout marked bad are left out.
func (h Host) Plan(ctx context.Context, servers []ServerMods, keep bool) ([]Planned, Fetched, error) {
	f, err := h.Source.Fetch(ctx)
	if err != nil {
		return nil, f, err
	}
	c := f.Catalog.Without(func(id, ver string) bool {
		in, err := h.Store.Get(id, ver)
		return err == nil && in.Bad != ""
	})
	var out []Planned
	for _, s := range servers {
		old, err := h.Fleet.ReadLock(s.Name)
		if err != nil {
			return nil, f, fmt.Errorf("%s: %w", s.Name, err)
		}
		var kept map[string]string
		if keep {
			kept = old.Versions()
		}
		lock, probs := ResolveKeeping(c, s.Specs, s.Runtime, kept)
		lock.LocalPlugins = s.LocalPlugins
		out = append(out, Planned{Server: s.Name, Old: old, New: lock, Problems: probs})
	}
	return out, f, nil
}

// Download stores every mod the plans run that isn't stored yet.
func (h Host) Download(ctx context.Context, plans []Planned) error {
	done := map[string]bool{}
	for _, p := range plans {
		for _, m := range p.New.Mods {
			key := m.ID + "@" + m.Version
			if m.Shipped || done[key] {
				continue
			}
			done[key] = true
			if _, err := h.Store.Get(m.ID, m.Version); err != nil {
				h.log("downloading %s %s", m.ID, m.Version)
			}
			if _, err := h.Store.Install(ctx, m.Entry); err != nil {
				return fmt.Errorf("%s %s: %w", m.ID, m.Version, err)
			}
		}
	}
	return nil
}

// Write stores the changed locks; each server places its own at its next start
// (after its match). For the host's own edits: no canary.
func (h Host) Write(plans []Planned) ([]string, error) {
	var changed []string
	for _, p := range plans {
		if !p.Changed() {
			continue
		}
		if err := h.Fleet.WriteLock(p.Server, p.New); err != nil {
			return changed, fmt.Errorf("%s: %w", p.Server, err)
		}
		changed = append(changed, p.Server)
	}
	return changed, nil
}

// Fresh lists the mod versions the plans bring that no server runs yet (as
// "id version"): what a rollout tries on a canary first.
func Fresh(plans []Planned) []string {
	running := map[string]bool{}
	for _, p := range plans {
		for id, v := range p.Old.Versions() {
			running[id+" "+v] = true
		}
	}
	set := map[string]bool{}
	for _, p := range plans {
		for id, v := range p.New.Versions() {
			if !running[id+" "+v] {
				set[id+" "+v] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Result is what a rollout did.
type Result struct {
	// Action: up_to_date, switched (every server has its new lock), rolled_back
	// (the canary failed; Fresh is marked bad) or installed_only (downloaded,
	// no idle server to try it on: the next check tries again).
	Action  string
	Fresh   []string
	Canary  string
	Detail  string
	Servers []string
}

// Rollout moves every server to the newest mods it may run. New versions run on
// one idle server first; only when it registers with all of them loaded do the
// others get theirs (at their next start). When it fails they are marked bad,
// and the canary goes back to its lock.
func (h Host) Rollout(ctx context.Context, servers []ServerMods) (Result, error) {
	plans, _, err := h.Plan(ctx, servers, false)
	if err != nil {
		return Result{}, err
	}
	res := Result{Action: "up_to_date", Fresh: Fresh(plans)}
	changed := false
	for _, p := range plans {
		changed = changed || p.Changed()
	}
	if !changed {
		return res, nil
	}
	if err := h.Download(ctx, plans); err != nil {
		return res, err
	}
	sts, _ := h.Fleet.Statuses(ctx)
	if len(res.Fresh) == 0 || len(sts) == 0 {
		// Nothing new to try (a mod left out, or one going back), or no server runs.
		res.Action = "switched"
		res.Servers, err = h.Write(plans)
		return res, err
	}
	timeout, poll := h.CanaryTimeout, h.Poll
	if timeout == 0 {
		timeout = 30 * time.Minute
	}
	if poll == 0 {
		poll = 10 * time.Second
	}
	canary, err := h.waitIdle(ctx, plans, res.Fresh, timeout, poll)
	if err != nil {
		res.Action, res.Detail = "installed_only", err.Error()
		return res, nil
	}
	res.Canary = canary.Server
	h.log("canary %s: trying %s", canary.Server, strings.Join(res.Fresh, ", "))
	if err := h.Fleet.WriteLock(canary.Server, canary.New); err != nil {
		return res, err
	}
	if err := h.Fleet.Restart(ctx, canary.Server); err != nil {
		_ = h.Fleet.WriteLock(canary.Server, canary.Old)
		return res, fmt.Errorf("restarting canary %s: %w", canary.Server, err)
	}
	ok, failed, detail := h.waitLoaded(ctx, canary, timeout, poll)
	if ok {
		h.log("canary %s registered with every mod loaded", canary.Server)
		res.Action = "switched"
		res.Servers, err = h.Write(plans)
		return res, err
	}
	// Only the versions that failed are bad when the server says which; else every new one on it.
	bad := failed
	if len(bad) == 0 {
		for id, v := range canary.New.Versions() {
			if contains(res.Fresh, id+" "+v) {
				bad = append(bad, id+" "+v)
			}
		}
	}
	for _, b := range bad {
		id, v, _ := strings.Cut(b, " ")
		_ = h.Store.MarkBad(id, v, detail)
	}
	res.Action, res.Detail, res.Fresh = "rolled_back", detail, bad
	h.log("canary %s failed (%s): %s marked bad, going back", canary.Server, detail, strings.Join(bad, ", "))
	if err := h.Fleet.WriteLock(canary.Server, canary.Old); err != nil {
		return res, err
	}
	return res, h.Fleet.Restart(ctx, canary.Server)
}

// waitIdle returns the plan of a registered server without a match whose new
// lock has something fresh.
func (h Host) waitIdle(ctx context.Context, plans []Planned, fresh []string, timeout, poll time.Duration) (Planned, error) {
	deadline := time.Now().Add(timeout)
	for {
		sts, err := h.Fleet.Statuses(ctx)
		if err == nil {
			for _, p := range plans {
				if !p.Changed() || !bringsAny(p.New, fresh) {
					continue
				}
				for _, s := range sts {
					if s.Instance == p.Server && s.State == status.StateRegistered {
						return p, nil
					}
				}
			}
		}
		if time.Now().After(deadline) {
			return Planned{}, errors.New("no idle registered server to try the new mods on")
		}
		select {
		case <-ctx.Done():
			return Planned{}, ctx.Err()
		case <-time.After(poll):
		}
	}
}

func bringsAny(l Lock, fresh []string) bool {
	for id, v := range l.Versions() {
		if contains(fresh, id+" "+v) {
			return true
		}
	}
	return false
}

// waitLoaded waits until the canary registered with its new lock's mods (nEVR 6
// on: each one loaded). failed names the mods nEVR said failed ("id version").
func (h Host) waitLoaded(ctx context.Context, p Planned, timeout, poll time.Duration) (ok bool, failed []string, detail string) {
	deadline := time.Now().Add(timeout)
	last := "no status"
	want := p.New.Versions()
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return false, nil, ctx.Err().Error()
		case <-time.After(poll):
		}
		sts, err := h.Fleet.Statuses(ctx)
		if err != nil {
			last = err.Error()
			continue
		}
		for _, s := range sts {
			if s.Instance != p.Server {
				continue
			}
			last = string(s.State)
			if s.LastError != "" {
				last += ": " + s.LastError
			}
			if !runs(s, want) {
				continue // still the run before the restart
			}
			for _, m := range s.Mods {
				if m.Error != "" {
					failed = append(failed, m.ID+" "+m.Version)
					last = m.ID + " " + m.Version + ": " + m.Error
				}
			}
			if len(failed) > 0 {
				return false, failed, last
			}
			switch s.State {
			case status.StateRegistered, status.StateInSession:
				if allLoaded(s) {
					return true, nil, ""
				}
			case status.StateFailed:
				return false, nil, last
			}
		}
	}
	return false, nil, "the canary did not register with its mods in time (" + last + ")"
}

// runs reports whether a status is of a start that placed exactly want.
func runs(s status.Status, want map[string]string) bool {
	have := map[string]string{}
	for _, m := range s.Mods {
		if !strings.HasPrefix(m.ID, "local:") {
			have[m.ID] = m.Version
		}
	}
	for id, v := range want {
		if have[id] != v {
			return false
		}
	}
	return true
}

// allLoaded: nEVR 6 says which plugins loaded; a 5.x server registering is all there is.
func allLoaded(s status.Status) bool {
	if v := strings.TrimPrefix(s.RuntimeVersion, "v"); ParsesAsVersion(v) && InRange(v, "<6.0.0-0", true) {
		return true
	}
	for _, m := range s.Mods {
		if !m.Loaded {
			return false
		}
	}
	return true
}
