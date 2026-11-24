package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/internal/hostcfg"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/nevr"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/update"
)

// RuntimeInfo combines upstream releases with what is installed.
type RuntimeInfo struct {
	Tag       string
	Installed bool
	Current   bool
	Previous  bool
	Bad       string
	Blocked   string
	Remote    *nevr.Release
}

// Runtimes lists installed and available nevr-runtime versions. Offline it
// still returns the installed ones together with the error.
func (a *App) Runtimes(ctx context.Context) ([]RuntimeInfo, error) {
	st := a.Store()
	installed, _ := st.List()
	cur, prev := st.Current(), st.Previous()
	byTag := map[string]*RuntimeInfo{}
	var order []string
	add := func(tag string) *RuntimeInfo {
		if r, ok := byTag[tag]; ok {
			return r
		}
		r := &RuntimeInfo{Tag: tag, Current: tag == cur, Previous: tag == prev}
		byTag[tag] = r
		order = append(order, tag)
		return r
	}
	rels, err := (&nevr.Client{}).List(ctx)
	for i := range rels {
		r := add(rels[i].Tag)
		r.Remote = &rels[i]
		r.Blocked = rels[i].Blocked
	}
	for _, in := range installed {
		r := add(in.Tag)
		r.Installed, r.Bad = true, in.Bad
	}
	out := make([]RuntimeInfo, 0, len(order))
	for _, t := range order {
		out = append(out, *byTag[t])
	}
	return out, err
}

// Target returns the release the runtime policy wants: the one nEVR's
// channel names, or the pinned one.
func (a *App) Target(ctx context.Context) (nevr.Release, error) {
	return (&update.Updater{}).Target(ctx, a.Policy())
}

// Policy is the runtime policy of the host config.
func (a *App) Policy() update.Policy {
	return update.Policy{Channel: a.Cfg.Runtime.Channel, Pinned: a.Cfg.Runtime.Pinned}
}

// InstallRuntime installs tag (or the policy target when tag is empty) and,
// with use, makes it current.
func (a *App) InstallRuntime(ctx context.Context, tag string, use bool) (nevr.Installed, error) {
	var rel nevr.Release
	var err error
	if tag == "" {
		rel, err = a.Target(ctx)
	} else {
		rel, err = (&nevr.Client{}).Find(ctx, tag)
	}
	if err != nil {
		return nevr.Installed{}, err
	}
	inst, err := a.Store().Install(ctx, rel)
	if err != nil {
		return inst, err
	}
	if use {
		err = a.Store().Use(rel.Tag)
	}
	return inst, err
}

// EnsureRuntime installs the policy target if no runtime is current yet.
func (a *App) EnsureRuntime(ctx context.Context) (string, error) {
	if cur := a.Store().Current(); cur != "" {
		return cur, nil
	}
	inst, err := a.InstallRuntime(ctx, "", true)
	if err != nil {
		return "", fmt.Errorf("installing nevr-runtime: %w", err)
	}
	return inst.Tag, nil
}

// Channels returns nEVR's channels (name → version), stable first in names.
func (a *App) Channels(ctx context.Context) (names []string, versions map[string]string, err error) {
	versions, err = (&nevr.Client{}).Channels(ctx)
	return nevr.ChannelNames(versions), versions, err
}

// SetRuntimePolicy saves who updates the runtime and the channel it follows.
// An empty updater keeps the current one. A channel nEVR's manifest doesn't
// list is refused (when the manifest can be read); a pin needs evr-docker
// updating. The servers get it when they are next applied (SelfUpdateEnv).
func (a *App) SetRuntimePolicy(ctx context.Context, updater, channel, pinned string) error {
	r := a.Cfg.Runtime
	if updater != "" {
		r.Updater = updater
	}
	if channel != "" {
		r.Channel, r.Pinned = nevr.ChannelName(channel), pinned
	}
	if r.Channel == "pinned" && r.Updater == hostcfg.UpdaterNevr {
		if updater != "" {
			return fmt.Errorf("nEVR can't update itself while the runtime is pinned to %s: pick a channel first", r.Pinned)
		}
		r.Updater = hostcfg.UpdaterHost // pinning hands updating to evr-docker
	}
	if r.Channel != "pinned" {
		if names, chs, err := a.Channels(ctx); err == nil {
			if _, ok := chs[r.Channel]; !ok {
				return fmt.Errorf("nEVR has no channel %q (it has %s)", r.Channel, strings.Join(names, ", "))
			}
		}
	}
	old := a.Cfg.Runtime
	a.Cfg.Runtime = r
	if err := a.Save(); err != nil {
		a.Cfg.Runtime = old
		return err
	}
	return nil
}

// RuntimePolicyText describes who updates the runtime and from where, for
// listings ("nEVR updates itself (channel beta, now 6.17.0)").
func (a *App) RuntimePolicyText(versions map[string]string) string {
	r := a.Cfg.Runtime
	from := "channel " + r.Channel
	if r.Channel == "pinned" {
		from = "pinned to " + r.Pinned
	} else if v := versions[r.Channel]; v != "" {
		from += ", now " + v
	}
	if r.Updater == hostcfg.UpdaterNevr {
		return "nEVR updates itself (" + from + ")"
	}
	return "evr-docker updates it (" + from + ")"
}

// SelfUpdateNote warns when nEVR is to update itself but its channel's
// release can't yet (before nevr.UpdateConfigMin): evr-docker keeps it current
// until it can.
func (a *App) SelfUpdateNote(versions map[string]string) string {
	r := a.Cfg.Runtime
	if r.Updater != hostcfg.UpdaterNevr {
		return ""
	}
	if v := versions[r.Channel]; v != "" && !nevr.UpdateConfigurable(v) {
		return fmt.Sprintf("nEVR %s on %s can't be left to update itself yet (that needs %s); evr-docker keeps it current until it can", v, r.Channel, nevr.UpdateConfigMin)
	}
	return ""
}

// StayOn keeps the servers on the version tag, as picking a version does in
// the launcher: tag is installed if it isn't yet and made current, and the
// policy is pinned to it, so neither the next update check nor nEVR's own
// update (a pin hands updating to evr-docker) moves the servers away again.
// Picking a channel (`runtime channel NAME`) ends it. The servers get the
// policy when they are next applied.
func (a *App) StayOn(ctx context.Context, tag string) error {
	st := a.Store()
	inst, err := st.Get(tag)
	if err != nil {
		if inst, err = a.InstallRuntime(ctx, tag, false); err != nil {
			return err
		}
	}
	if inst.Bad != "" {
		return fmt.Errorf("nevr-runtime %s is marked bad: %s (`evr-docker runtime unmark %s` allows it again)", tag, inst.Bad, tag)
	}
	// The pin first: should the switch fail, the next update check still
	// moves the servers to the version picked, not away from it.
	if err := a.SetRuntimePolicy(ctx, "", "pinned", tag); err != nil {
		return err
	}
	return st.Use(tag)
}

// ApplyRuntime gives the servers a changed runtime policy: idle ones are
// recreated now and one in a match once its match has ended, waiting up to
// MatchWait. Apply alone leaves a server in a match for a later apply, and
// one left on the nevr updater would keep a runtime nEVR updated itself
// (game.SelfUpdated) instead of the version picked.
func (a *App) ApplyRuntime(ctx context.Context, say func(string)) error {
	if len(a.Cfg.Instances) == 0 {
		return nil
	}
	if _, err := a.Apply(ctx, false, say); err != nil {
		return err
	}
	if err := a.restartIdleOnOtherVersion(ctx, say); err != nil {
		return err
	}
	deadline := time.Now().Add(MatchWait)
	for {
		waiting, err := a.inMatchWithDrift(ctx)
		if err != nil || len(waiting) == 0 {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s still in a match after %s: `evr-docker server apply` once it ends gives it the change", strings.Join(waiting, ", "), MatchWait)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s not updated yet (%w): `evr-docker server apply` once its match ends gives it the change", strings.Join(waiting, ", "), ctx.Err())
		case <-time.After(matchPoll):
		}
		// only the servers whose match ended are recreated; the others'
		// "will update after its match" lines aren't repeated
		quiet := func(s string) {
			if !strings.Contains(s, "will update after its match") {
				say(s)
			}
		}
		if _, err := a.Apply(ctx, false, quiet); err != nil {
			return err
		}
	}
}

// restartIdleOnOtherVersion restarts the game of each idle server that
// follows current but runs another version while evr-docker updates, so a
// version picked (or a channel's release installed) takes effect now. A
// server picks up current at each start of its game, which is otherwise only
// after its next match; one in a match still switches when the match ends.
func (a *App) restartIdleOnOtherVersion(ctx context.Context, say func(string)) error {
	cur := a.Store().Current()
	if a.Cfg.Runtime.Updater != hostcfg.UpdaterHost || cur == "" {
		return nil // nEVR updating: a server may rightly run a newer one it put in place
	}
	servers, err := a.Servers(ctx)
	if err != nil {
		return err
	}
	for _, s := range servers {
		in, ok := a.Cfg.Instance(s.Instance.Name)
		if !ok || in.Disabled || !s.Running() || s.Status == nil || !s.Status.State.Idle() {
			continue
		}
		if rt := a.Cfg.Effective(*in).Runtime; rt != "" && rt != "current" {
			continue // pinned to its own version
		}
		if v := s.Status.RuntimeVersion; v == "" || nevr.SameRelease(v, cur) {
			continue
		}
		if err := a.Restart(ctx, in.Name, true); err != nil {
			return fmt.Errorf("%s: %w", in.Name, err)
		}
		say(fmt.Sprintf("%s: idle on %s, restarting its game on %s", in.Name, s.Status.RuntimeVersion, cur))
	}
	return nil
}

// MatchWait is how long ApplyRuntime waits for matches to end; matchPoll how
// often it looks.
var (
	MatchWait = 45 * time.Minute
	matchPoll = 10 * time.Second
)

// inMatchWithDrift names the running servers whose container differs from
// the configuration and that host a match.
func (a *App) inMatchWithDrift(ctx context.Context) ([]string, error) {
	servers, err := a.Servers(ctx)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, s := range servers {
		if s.Container != nil && s.Drift != "" && s.Running() && s.Status != nil && !s.Status.State.Idle() {
			out = append(out, s.Instance.Name)
		}
	}
	return out, nil
}

// StayOnPrevious is a rollback that stays: StayOn the previous version.
func (a *App) StayOnPrevious(ctx context.Context) (string, error) {
	prev := a.Store().Previous()
	if prev == "" {
		return "", errors.New("no previous runtime to roll back to")
	}
	return prev, a.StayOn(ctx, prev)
}

// Pickable returns the versions the servers can be kept on, newest first
// (nevr.Pickable of a Runtimes list).
func Pickable(rts []RuntimeInfo) []string {
	var rels []nevr.Release
	var installed []nevr.Installed
	for _, r := range rts {
		if r.Remote != nil {
			rels = append(rels, *r.Remote)
		}
		if r.Installed {
			installed = append(installed, nevr.Installed{Tag: r.Tag, Bad: r.Bad})
		}
	}
	return nevr.Pickable(rels, installed)
}
