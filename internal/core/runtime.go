package core

import (
	"context"
	"fmt"

	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/nevr"
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

// Target returns the release the runtime policy wants.
func (a *App) Target(ctx context.Context) (nevr.Release, error) {
	c := &nevr.Client{}
	switch a.Cfg.Runtime.Channel {
	case "pinned":
		return c.Find(ctx, a.Cfg.Runtime.Pinned)
	case "stable":
		return c.Newest(ctx, false)
	default:
		return c.Newest(ctx, true)
	}
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
