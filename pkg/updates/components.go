package updates

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/game"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/mods"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/nevr"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/selfupdate"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/update"
)

// Self updates the tool binary. After runs once the new binary is in place
// (e.g. its post-update step).
type Self struct {
	U     *selfupdate.Updater
	After func(ctx context.Context, say func(string)) error
}

func (s *Self) Name() string  { return "self" }
func (s *Self) Title() string { return s.U.Binary }

func (s *Self) Check(ctx context.Context) (Status, error) {
	st := Status{Installed: s.U.Current}
	rels, err := s.U.Releases(ctx)
	if err != nil {
		return st, err
	}
	r, err := selfupdate.Pick(rels, s.U.Current, "")
	if err != nil {
		return st, err
	}
	st.Latest, st.Available = r.Tag, selfupdate.Newer(s.U.Current, r.Tag)
	return st, nil
}

func (s *Self) Versions(ctx context.Context) ([]string, error) {
	rels, err := s.U.Releases(ctx)
	var out []string
	for _, r := range rels {
		out = append(out, r.Tag)
	}
	return out, err
}

func (s *Self) Apply(ctx context.Context, version string, say func(string)) error {
	rels, err := s.U.Releases(ctx)
	if err != nil {
		return err
	}
	r, err := selfupdate.Pick(rels, s.U.Current, version)
	if err != nil {
		return err
	}
	if err := s.U.Apply(ctx, r, say); err != nil {
		return err
	}
	if s.After != nil {
		return s.After(ctx, say)
	}
	return nil
}

// Game repairs or updates the shared game files from the mirror. Echo VR no
// longer changes, so in practice this restores missing or damaged files.
type Game struct {
	In     game.Installer
	Mirror string
}

func (g *Game) Name() string  { return "game" }
func (g *Game) Title() string { return "game files" }

func (g *Game) Check(ctx context.Context) (Status, error) {
	info, err := g.In.Current()
	if err != nil {
		return Status{Installed: "missing", Latest: "mirror", Available: true, Notes: err.Error()}, nil
	}
	st := Status{Installed: info.Build.Version, Latest: info.Build.Version}
	changes, err := g.In.Changes(ctx, g.Mirror)
	if err != nil {
		return st, err
	}
	if len(changes) > 0 {
		st.Available, st.Latest = true, fmt.Sprintf("%s (%d file(s) differ from the mirror)", info.Build.Version, len(changes))
		st.Notes = fmt.Sprint(changes[:min(len(changes), 5)])
	}
	return st, nil
}

func (g *Game) Versions(context.Context) ([]string, error) { return nil, nil }

func (g *Game) Apply(ctx context.Context, version string, say func(string)) error {
	in := g.In
	last := ""
	in.Progress = func(p game.Progress) {
		line := p.Step
		if p.Percent >= 0 {
			line = fmt.Sprintf("%s %d%%", p.Step, p.Percent/10*10)
		}
		if line != last {
			last = line
			say(line + " " + p.Detail)
		}
	}
	info, err := in.FromMirror(ctx, g.Mirror)
	if err != nil {
		return err
	}
	say("game build " + info.Build.Version + " verified; servers use it from their next restart")
	return nil
}

// Runtime rolls out nevr-runtime releases with the canary updater.
type Runtime struct {
	U      *update.Updater
	Policy func() update.Policy
}

func (r *Runtime) Name() string  { return "runtime" }
func (r *Runtime) Title() string { return "nevr-runtime" }

func (r *Runtime) Check(ctx context.Context) (Status, error) {
	cur, target, available, err := r.U.Check(ctx, r.Policy())
	return Status{Installed: orNone(cur), Latest: target.Tag, Available: available}, err
}

func (r *Runtime) Versions(ctx context.Context) ([]string, error) {
	c := r.U.Client
	if c == nil {
		c = &nevr.Client{}
	}
	rels, err := c.List(ctx)
	var out []string
	for _, rel := range rels {
		if rel.Blocked == "" {
			out = append(out, rel.Tag)
		}
	}
	return out, err
}

func (r *Runtime) Apply(ctx context.Context, version string, say func(string)) error {
	p := r.Policy()
	if version != "" {
		p = update.Policy{Channel: "pinned", Pinned: version}
	}
	u := *r.U
	u.Log = say
	res, err := u.Run(ctx, p)
	if err != nil {
		return err
	}
	switch res.Action {
	case "up_to_date":
		say("already on " + res.From)
	case "switched":
		say(fmt.Sprintf("now on %s; servers switch at their next match boundary", res.To))
	case "rolled_back":
		return fmt.Errorf("%s failed on canary %s: %s; staying on %s", res.To, res.Canary, res.Detail, res.From)
	default:
		return errors.New(res.To + " installed but not rolled out yet: " + res.Detail)
	}
	return nil
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// Mods rolls the servers' mods forward to the newest versions they may run:
// new versions on one idle server first, marked bad when they fail there.
type Mods struct {
	Host    func(log func(string)) mods.Host
	Servers func() []mods.ServerMods
}

func (m *Mods) Name() string  { return "mods" }
func (m *Mods) Title() string { return "server mods" }

func (m *Mods) Check(ctx context.Context) (Status, error) {
	plans, _, err := m.Host(nil).Plan(ctx, m.Servers(), false)
	if err != nil {
		return Status{Installed: "unknown"}, err
	}
	running := map[string]bool{}
	var changed []string
	for _, p := range plans {
		for id, v := range p.Old.Versions() {
			running[id+" "+v] = true
		}
		if p.Changed() {
			changed = append(changed, p.Server)
		}
	}
	st := Status{Installed: fmt.Sprintf("%d mod version(s) on %d server(s)", len(running), len(plans))}
	if len(changed) == 0 {
		st.Latest = st.Installed
		return st, nil
	}
	st.Available = true
	if fresh := mods.Fresh(plans); len(fresh) > 0 {
		st.Latest = strings.Join(fresh, ", ")
	} else {
		st.Latest = "changes for " + strings.Join(changed, ", ")
	}
	return st, nil
}

func (m *Mods) Versions(context.Context) ([]string, error) { return nil, nil }

func (m *Mods) Apply(ctx context.Context, _ string, say func(string)) error {
	res, err := m.Host(say).Rollout(ctx, m.Servers())
	if err != nil {
		return err
	}
	switch res.Action {
	case "up_to_date":
		say("the servers' mods are up to date")
	case "switched":
		say(fmt.Sprintf("new mods for %s; each server takes them at its next match boundary", strings.Join(res.Servers, ", ")))
	case "rolled_back":
		return fmt.Errorf("%s failed on canary %s: %s; the servers keep their mods", strings.Join(res.Fresh, ", "), res.Canary, res.Detail)
	default:
		return errors.New("mods downloaded but not rolled out yet: " + res.Detail)
	}
	return nil
}
