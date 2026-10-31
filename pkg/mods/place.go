package mods

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Dirs are where Place reads from and writes to, for one server start.
type Dirs struct {
	// Store holds the mods the host downloaded (read-only in a container).
	Store string
	// GamePlugins is the game's bin/win10/plugins (shipped mods are taken from there).
	GamePlugins string
	// Local is the server's own folder of DLLs (loaded with LocalPlugins only).
	Local string
	// Plugins is where nEVR loads them: <nEVR root>/plugins from nEVR 6 on, before
	// that the server's own bin/win10/plugins.
	Plugins string
	// Content is where a pack's data goes: beside Plugins (<root>/content, or
	// bin/win10/content), where NvrContentOverlay reads it.
	Content string
	// Record notes what Place put there, so the next run takes away what the lock
	// no longer has (and nothing else).
	Record string
}

// Line is one plugin for config.yaml's plugins list.
type Line struct {
	Name  string
	File  string
	Early bool
	Args  map[string]string
}

// Placed is the result of Place: the plugin list, and what was skipped and why.
type Placed struct {
	Lines   []Line
	Skipped []string
}

type record struct {
	Files []string `json:"files"`
}

// Place puts the lock's files where nEVR loads them, each checked against the
// checksum the host noted when it downloaded it, and returns config.yaml's plugin
// list. Files placed by an earlier run that the lock no longer has go; files
// anyone else put there stay (nEVR loads only what config.yaml lists).
func Place(lock Lock, d Dirs) (Placed, error) {
	var out Placed
	store := Store{Dir: d.Store}
	var placed []string
	put := func(src, rel, want string) error {
		dst := filepath.Join(d.Plugins, filepath.FromSlash(rel))
		if strings.HasPrefix(rel, "content/") {
			dst = filepath.Join(d.Content, filepath.FromSlash(strings.TrimPrefix(rel, "content/")))
		}
		if err := copyChecked(src, dst, want); err != nil {
			return err
		}
		placed = append(placed, relTo(d, dst))
		return nil
	}
	for _, m := range lock.Mods {
		if m.Shipped {
			ok := true
			for _, f := range m.Plugins {
				src := filepath.Join(d.GamePlugins, f)
				if _, err := os.Stat(src); err != nil {
					out.Skipped = append(out.Skipped, fmt.Sprintf("%s: the game's files have no %s", m.ID, f))
					ok = false
					break
				}
				if err := put(src, f, ""); err != nil {
					return out, fmt.Errorf("%s: %w", m.ID, err)
				}
			}
			if ok {
				out.Lines = append(out.Lines, lines(m)...)
			}
			continue
		}
		in, err := store.Get(m.ID, m.Version)
		if err != nil {
			out.Skipped = append(out.Skipped, fmt.Sprintf("%s %s isn't downloaded yet (evr mods update)", m.ID, m.Version))
			continue
		}
		if in.Bad != "" {
			out.Skipped = append(out.Skipped, fmt.Sprintf("%s %s is marked bad: %s", m.ID, m.Version, in.Bad))
			continue
		}
		rels := make([]string, 0, len(in.Files))
		for rel := range in.Files {
			rels = append(rels, rel)
		}
		sort.Strings(rels)
		for _, rel := range rels {
			if !strings.HasPrefix(rel, "plugins/") && !strings.HasPrefix(rel, "content/") {
				continue
			}
			if err := put(store.Path(in, rel), strings.TrimPrefix(rel, "plugins/"), in.Files[rel]); err != nil {
				return out, fmt.Errorf("%s %s: %w", m.ID, m.Version, err)
			}
		}
		out.Lines = append(out.Lines, lines(m)...)
	}
	if lock.LocalPlugins && d.Local != "" {
		ents, _ := os.ReadDir(d.Local)
		for _, e := range ents {
			name := e.Name()
			if e.IsDir() || !PluginFile(name) || taken(out.Lines, name) {
				continue
			}
			if err := put(filepath.Join(d.Local, name), name, ""); err != nil {
				return out, fmt.Errorf("local %s: %w", name, err)
			}
			out.Lines = append(out.Lines, Line{Name: stem(name), File: name})
			out.Skipped = append(out.Skipped, fmt.Sprintf("%s: a local plugin, not checked (local plugins are on)", name))
		}
	}
	return out, sweep(d, placed)
}

func lines(m Locked) []Line {
	var out []Line
	for i, f := range m.Plugins {
		l := Line{Name: stem(f), File: f, Early: m.Early}
		if i == 0 && len(m.Args) > 0 {
			l.Args = m.Args
		}
		out = append(out, l)
	}
	return out
}

func taken(ls []Line, f string) bool {
	for _, l := range ls {
		if strings.EqualFold(l.File, f) {
			return true
		}
	}
	return false
}

func stem(f string) string { return strings.TrimSuffix(f, filepath.Ext(f)) }

// relTo names a placed file in the record: "plugins/<rel>" or "content/<rel>".
func relTo(d Dirs, p string) string {
	if r, err := filepath.Rel(d.Content, p); err == nil && !strings.HasPrefix(r, "..") {
		return "content/" + filepath.ToSlash(r)
	}
	r, _ := filepath.Rel(d.Plugins, p)
	return "plugins/" + filepath.ToSlash(r)
}

// sweep takes away what the last run placed and this one didn't, then notes
// this run's files.
func sweep(d Dirs, now []string) error {
	var before record
	if b, err := os.ReadFile(d.Record); err == nil {
		_ = json.Unmarshal(b, &before)
	}
	keep := map[string]bool{}
	for _, f := range now {
		keep[f] = true
	}
	for _, f := range before.Files {
		if keep[f] || !SafePath(f) {
			continue
		}
		var p string
		switch {
		case strings.HasPrefix(f, "plugins/"):
			p = filepath.Join(d.Plugins, filepath.FromSlash(strings.TrimPrefix(f, "plugins/")))
		case strings.HasPrefix(f, "content/"):
			p = filepath.Join(d.Content, filepath.FromSlash(strings.TrimPrefix(f, "content/")))
		default:
			continue
		}
		os.Remove(p)
		// Folders a pack brought go once empty.
		for dir := filepath.Dir(p); dir != d.Content && dir != d.Plugins && strings.HasPrefix(dir, d.Content); dir = filepath.Dir(dir) {
			if os.Remove(dir) != nil {
				break
			}
		}
	}
	sort.Strings(now)
	b, _ := json.MarshalIndent(record{Files: now}, "", "  ")
	if err := os.MkdirAll(filepath.Dir(d.Record), 0o755); err != nil {
		return err
	}
	return os.WriteFile(d.Record, b, 0o644)
}

// copyChecked copies src to dst (through a temporary file) when dst isn't
// already the same bytes; with want set, the copy must hash to it.
func copyChecked(src, dst, want string) error {
	if have, err := fileSHA(dst); err == nil {
		if want == "" {
			if srcSHA, err := fileSHA(src); err == nil && srcSHA == have {
				return nil
			}
		} else if have == want {
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".mod-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if want != "" {
		got, err := fileSHA(tmp.Name())
		if err != nil {
			return err
		}
		if got != want {
			return fmt.Errorf("%s doesn't match the checksum noted at download (got %s, want %s)", filepath.Base(src), got, want)
		}
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dst)
}

// ReadLock reads a lock file; a missing one is an empty lock (no mods).
func ReadLock(path string) (Lock, error) {
	var l Lock
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return l, nil
	}
	if err != nil {
		return l, err
	}
	return l, json.Unmarshal(b, &l)
}

// WriteLock writes a lock file atomically.
func WriteLock(path string, l Lock) error {
	b, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
