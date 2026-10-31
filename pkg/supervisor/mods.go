package supervisor

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/game"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/mods"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/nevr/config"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/status"
)

// placeMods puts the lock's mods where this runtime loads them (the nEVR root
// for a two-file runtime, else the instance's bin/win10/plugins) and returns
// cfg with their plugins list. Without a lock it takes away what an earlier
// start placed and returns cfg as it is.
func (s *Supervisor) placeMods(twoFile bool, version string, cfg []byte) ([]byte, error) {
	lock, err := mods.ReadLock(s.o.ModsLock)
	if err != nil {
		return nil, fmt.Errorf("mods lock %s: %w", s.o.ModsLock, err)
	}
	var skipped []string
	if v := strings.TrimPrefix(version, "v"); v != "" {
		kept := lock.Mods[:0]
		for _, m := range lock.Mods {
			if m.Nevr != "" && mods.ParsesAsVersion(v) && !mods.InRange(v, m.Nevr, true) {
				skipped = append(skipped, fmt.Sprintf("%s %s needs nEVR %s (this server runs %s)", m.ID, m.Version, m.Nevr, version))
				continue
			}
			kept = append(kept, m)
		}
		lock.Mods = kept
	}
	d := mods.Dirs{Store: s.o.ModsStore, GamePlugins: filepath.Join(s.o.GameDir, game.BinDir, "plugins"), Local: s.o.LocalPlugins}
	if twoFile {
		d.Plugins = filepath.Join(s.NevrRoot(), "plugins")
		d.Content = filepath.Join(s.NevrRoot(), "content")
		d.ExeContent = filepath.Join(s.root(), game.BinDir, "content")
		d.Record = filepath.Join(s.o.Home, "mods-placed.root.json")
	} else {
		d.Plugins = filepath.Join(s.root(), game.BinDir, "plugins")
		d.Content = filepath.Join(s.root(), game.BinDir, "content")
		d.Record = filepath.Join(s.o.Home, "mods-placed.bin.json")
	}
	placed, err := mods.Place(lock, d)
	if err != nil {
		return nil, err
	}
	skipped = append(skipped, placed.Skipped...)
	var plugins []config.Plugin
	var st []status.Mod
	for _, l := range placed.Lines {
		plugins = append(plugins, config.Plugin{Name: l.Name, File: l.File, Early: l.Early, Args: l.Args})
	}
	for _, m := range lock.Mods {
		pack := len(m.Plugins) == 0 && m.Content != "" && !skippedMod(placed.Skipped, m)
		if pack || placedAll(placed.Lines, m.Plugins) {
			st = append(st, status.Mod{ID: m.ID, Version: m.Version, Files: m.Plugins})
		}
	}
	if lock.LocalPlugins {
		for _, l := range placed.Lines {
			if !inLock(lock, l.File) {
				st = append(st, status.Mod{ID: "local:" + l.File, Version: "local", Files: []string{l.File}})
			}
		}
	}
	for _, sk := range skipped {
		s.logf("mods: %s", sk)
	}
	if len(plugins) > 0 {
		s.logf("mods: %d plugin(s) for this start: %s", len(plugins), pluginNames(plugins))
	}
	s.update(func(x *status.Status) { x.Mods, x.ModsSkipped = st, skipped })
	if cfg == nil {
		return nil, nil
	}
	return config.WithPlugins(cfg, plugins)
}

func placedAll(lines []mods.Line, files []string) bool {
	for _, f := range files {
		ok := false
		for _, l := range lines {
			ok = ok || strings.EqualFold(l.File, f)
		}
		if !ok {
			return false
		}
	}
	return len(files) > 0
}

func skippedMod(skipped []string, m mods.Locked) bool {
	for _, sk := range skipped {
		if strings.HasPrefix(sk, m.ID+" ") || strings.HasPrefix(sk, m.ID+":") {
			return true
		}
	}
	return false
}

func inLock(lock mods.Lock, file string) bool {
	for _, m := range lock.Mods {
		for _, f := range m.Plugins {
			if strings.EqualFold(f, file) {
				return true
			}
		}
	}
	return false
}

func pluginNames(ps []config.Plugin) string {
	var n []string
	for _, p := range ps {
		n = append(n, p.File)
	}
	return strings.Join(n, ", ")
}

// modEvent notes nEVR's plugin load results in status.json.
func (s *Supervisor) modEvent(ev Event) {
	s.update(func(st *status.Status) {
		switch ev.Kind {
		case KindPluginFailed, KindFatal:
			for i, m := range st.Mods {
				for _, f := range m.Files {
					if strings.EqualFold(f, ev.File) {
						st.Mods[i].Loaded, st.Mods[i].Error = false, ev.Error
					}
				}
			}
		case KindPluginsLoaded:
			for i := range st.Mods {
				st.Mods[i].Loaded = st.Mods[i].Error == ""
			}
		}
	})
}
