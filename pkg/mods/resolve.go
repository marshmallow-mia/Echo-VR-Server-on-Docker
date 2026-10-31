package mods

import (
	"fmt"
	"sort"
	"strings"
)

// Spec is one mod in a host's list (the fleet's, or a server's own over it).
type Spec struct {
	ID string `yaml:"id" json:"id"`
	// Version pins it: a version or a range ("" = the newest the catalogues offer).
	Version string `yaml:"version,omitempty" json:"version,omitempty"`
	// Prerelease lets the newest prerelease run too.
	Prerelease bool `yaml:"prerelease,omitempty" json:"prerelease,omitempty"`
	// Enabled false in a server's list leaves out a mod of the fleet's.
	Enabled *bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	// Args are its arguments, over the catalogue's defaults.
	Args map[string]string `yaml:"args,omitempty" json:"args,omitempty"`
}

func (s Spec) on() bool { return s.Enabled == nil || *s.Enabled }

// MergeSpecs is a server's list over the fleet's: matched by id, a server's
// entry adds a mod, leaves one out (enabled: false), pins it, or sets arguments
// (merged over the fleet's).
func MergeSpecs(fleet, own []Spec) []Spec {
	out := append([]Spec(nil), fleet...)
	for _, o := range own {
		i := -1
		for j := range out {
			if out[j].ID == o.ID {
				i = j
			}
		}
		if i < 0 {
			out = append(out, o)
			continue
		}
		m := out[i]
		if o.Version != "" {
			m.Version = o.Version
		}
		m.Prerelease = m.Prerelease || o.Prerelease
		if o.Enabled != nil {
			m.Enabled = o.Enabled
		}
		if len(o.Args) > 0 {
			args := map[string]string{}
			for k, v := range m.Args {
				args[k] = v
			}
			for k, v := range o.Args {
				args[k] = v
			}
			m.Args = args
		}
		out[i] = m
	}
	return out
}

// Locked is one mod version a server runs.
type Locked struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version"`
	// Plugins are its files for the plugins folder, in load order.
	Plugins []string `json:"plugins"`
	// Content is a pack's content folder.
	Content string `json:"content,omitempty"`
	// Early loads it in nEVR's early pass (a pack's overlay).
	Early bool `json:"early,omitempty"`
	// Args are its arguments as config.yaml gets them.
	Args map[string]string `json:"args,omitempty"`
	// Shipped comes with the game's files (bin/win10/plugins): taken from there.
	Shipped bool   `json:"shipped,omitempty"`
	Source  string `json:"source,omitempty"`
	// Entry is what the catalogue said (for the store to fetch it).
	Entry Entry `json:"-"`
}

// Lock is what one server runs: written by the host, read by evr-supervisor.
type Lock struct {
	Mods []Locked `json:"mods"`
	// LocalPlugins loads the DLLs in the server's mods/local folder too (unchecked).
	LocalPlugins bool `json:"local_plugins,omitempty"`
	// Runtime is the nEVR version it was resolved for.
	Runtime string `json:"runtime,omitempty"`
}

// Problem is a mod left out, and why.
type Problem struct {
	ID  string
	Why string
}

func (p Problem) String() string { return p.ID + ": " + p.Why }

// Resolve turns a list into a lock for a server on nEVR runtime (a tag such as
// v6.9.0; "" skips the nEVR check): each mod's version chosen, the mods the game
// needs added, and anything that can't run left out with the reason, as the
// launcher leaves them out of a start (for players' games only, the wrong nEVR,
// blocked, a conflict, or a mod it needs missing).
func Resolve(c *Catalog, specs []Spec, runtime string) (Lock, []Problem) {
	var probs []Problem
	type pick struct {
		e    Entry
		spec Spec
	}
	var picks []pick
	have := map[string]bool{}
	add := func(s Spec) {
		if have[s.ID] {
			return
		}
		have[s.ID] = true
		e, err := c.Choose(s.ID, s.Version, s.Prerelease)
		if err != nil {
			probs = append(probs, Problem{s.ID, err.Error()})
			return
		}
		if !e.Target.OnServers() {
			probs = append(probs, Problem{s.ID, "only for players' games"})
			return
		}
		picks = append(picks, pick{e, s})
	}
	off := map[string]bool{}
	for _, s := range specs {
		if !s.on() {
			off[s.ID] = true
		}
	}
	// What the game needs (and that loads on servers) first, as the launcher has it. A
	// shipped one comes with the game's files, whose copy can't be checked here (an
	// older one can end a nEVR 6 server): it loads only when the list names it.
	var required []string
	for id, vs := range c.Versions {
		for _, e := range vs {
			if e.Required && !e.Shipped && e.Target.OnServers() {
				required = append(required, id)
				break
			}
		}
	}
	sort.Strings(required)
	for _, id := range required {
		add(Spec{ID: id})
	}
	for _, s := range specs {
		if s.on() && !off[s.ID] {
			add(s)
		}
	}
	// A spec's own entry wins over the one added for being required (its pin, its args).
	for i, p := range picks {
		for _, s := range specs {
			if s.ID == p.e.ID && s.on() && p.spec.Version == "" && len(p.spec.Args) == 0 {
				picks[i].spec = s
			}
		}
	}
	runtimeVer := strings.TrimPrefix(runtime, "v")
	kept := picks[:0]
	for _, p := range picks {
		if r := p.e.Requires.Nevr; r != "" && runtimeVer != "" && !InRange(runtimeVer, r, true) {
			probs = append(probs, Problem{p.e.ID, fmt.Sprintf("needs nEVR %s (the servers run %s)", r, runtime)})
			continue
		}
		kept = append(kept, p)
	}
	picks = kept
	// A conflict leaves out the mod that declares it (of two that declare each other, the later).
	var gone []string
	for i, p := range picks {
		for j, q := range picks {
			if i == j || contains(gone, q.e.ID) || !contains(p.e.Conflicts, q.e.ID) {
				continue
			}
			if j < i || !contains(q.e.Conflicts, p.e.ID) {
				gone = append(gone, p.e.ID)
				probs = append(probs, Problem{p.e.ID, "can't run beside " + q.e.ID})
				break
			}
		}
	}
	picks = filter(picks, func(p pick) bool { return !contains(gone, p.e.ID) })
	// What each needs on beside it, until nothing more drops out.
	for {
		on := map[string]string{}
		for _, p := range picks {
			on[p.e.ID] = p.e.Version
		}
		var dropped []string
		for _, p := range picks {
			for _, d := range p.e.Requires.Mods {
				r := d.Version
				if r == "" {
					r = "*"
				}
				v, ok := on[d.ID]
				switch {
				case !ok:
					probs = append(probs, Problem{p.e.ID, "needs " + d.ID + ": add it"})
				case !InRange(v, r, true):
					probs = append(probs, Problem{p.e.ID, fmt.Sprintf("needs %s %s (it has %s)", d.ID, r, v)})
				default:
					continue
				}
				dropped = append(dropped, p.e.ID)
				break
			}
		}
		if len(dropped) == 0 {
			break
		}
		picks = filter(picks, func(p pick) bool { return !contains(dropped, p.e.ID) })
	}
	// One content pack at a time (its overlay serves one); it needs the overlay mod.
	var lock Lock
	lock.Runtime = runtime
	pack := ""
	for _, p := range picks {
		if p.e.Pack == nil {
			continue
		}
		if pack != "" {
			probs = append(probs, Problem{p.e.ID, "only one content pack runs at a time (" + pack + " does)"})
			continue
		}
		overlay := c.byFile(OverlayPlugin)
		if overlay == "" {
			probs = append(probs, Problem{p.e.ID, "the content overlay mod isn't in the catalogues"})
			continue
		}
		ov, err := c.Choose(overlay, "", false)
		if err != nil {
			probs = append(probs, Problem{p.e.ID, "its content overlay: " + err.Error()})
			continue
		}
		pack = p.e.ID
		lock.Mods = append(lock.Mods, Locked{
			ID: ov.ID, Name: ov.Name, Version: ov.Version, Plugins: []string{ov.File},
			Early: true, Args: map[string]string{"active": p.e.Pack.Content}, Source: ov.Source, Entry: ov,
		})
		lock.Mods = append(lock.Mods, locked(p.e, p.spec))
	}
	for _, p := range picks {
		if p.e.Pack == nil && !(pack != "" && strings.EqualFold(p.e.File, OverlayPlugin)) {
			lock.Mods = append(lock.Mods, locked(p.e, p.spec))
		}
	}
	return lock, probs
}

func locked(e Entry, s Spec) Locked {
	l := Locked{ID: e.ID, Name: e.Name, Version: e.Version, Shipped: e.Shipped, Source: e.Source, Entry: e}
	if e.Pack != nil {
		l.Plugins = append([]string(nil), e.Pack.Plugins...)
		l.Content = e.Pack.Content
	} else {
		l.Plugins = []string{e.File}
	}
	args := map[string]string{}
	for k, v := range e.Args {
		args[k] = argText(v)
	}
	for k, v := range s.Args {
		args[k] = v
	}
	if len(args) > 0 {
		l.Args = args
	}
	return l
}

func argText(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case nil:
		return ""
	case bool:
		if t {
			return "true"
		}
		return "false"
	case float64:
		return strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%f", t), "0"), ".")
	}
	return fmt.Sprint(v)
}

// byFile returns the id of the mod with plugin file f ("" if none).
func (c *Catalog) byFile(f string) string {
	for id, vs := range c.Versions {
		for _, e := range vs {
			if strings.EqualFold(e.File, f) {
				return id
			}
		}
	}
	return ""
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func filter[T any](in []T, keep func(T) bool) []T {
	var out []T
	for _, x := range in {
		if keep(x) {
			out = append(out, x)
		}
	}
	return out
}
