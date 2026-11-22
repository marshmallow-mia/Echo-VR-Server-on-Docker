package core

import (
	"context"
	"fmt"
	"strings"

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
// release can't yet (before nevr.UpdateMin): evr-docker keeps it current
// until it can.
func (a *App) SelfUpdateNote(versions map[string]string) string {
	r := a.Cfg.Runtime
	if r.Updater != hostcfg.UpdaterNevr {
		return ""
	}
	if v := versions[r.Channel]; v != "" && !nevr.SelfUpdates(v) {
		return fmt.Sprintf("nEVR %s on %s can't update itself yet (that came in %s); evr-docker keeps it current until it can", v, r.Channel, nevr.UpdateMin)
	}
	return ""
}
