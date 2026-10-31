package core

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/mods"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/status"
)

// LockName is a server's mods lock in its instance directory (the container's
// /home/container, where evr-supervisor reads it).
const LockName = "mods.lock.json"

// ModStore keeps the downloaded mods, mounted read-only into every container.
func (a *App) ModStore() mods.Store { return mods.Store{Dir: a.Cfg.ModsDir()} }

// ModHost resolves, downloads and rolls out the servers' mods from the
// launcher's catalogues (and the dev codes in secrets.env).
func (a *App) ModHost(log func(string)) mods.Host {
	return mods.Host{Source: mods.Source{DevCodes: a.Secrets.DevCodes()}, Store: a.ModStore(), Fleet: modFleet{a}, Log: log}
}

// ModServers are the configured servers with their mods (defaults and own).
func (a *App) ModServers() []mods.ServerMods {
	cur := a.Store().Current()
	var out []mods.ServerMods
	for _, in := range a.Cfg.Instances {
		set := a.Cfg.Effective(in)
		rt := set.Runtime
		if rt == "" || rt == "current" {
			rt = cur
		}
		out = append(out, mods.ServerMods{Name: in.Name, Specs: set.Mods, LocalPlugins: set.Local(), Runtime: rt})
	}
	return out
}

// SyncMods writes each server's lock after an edit: what it names changes, the
// rest stays at the versions the servers run (new versions come with updates).
// Servers place their lock at their next start, after their match.
func (a *App) SyncMods(ctx context.Context, log func(string)) ([]mods.Planned, []string, error) {
	h := a.ModHost(log)
	plans, _, err := h.Plan(ctx, a.ModServers(), true)
	if err != nil {
		return nil, nil, err
	}
	if err := h.Download(ctx, plans); err != nil {
		return plans, nil, err
	}
	changed, err := h.Write(plans)
	return plans, changed, err
}

// Statuses returns the status of every running server; one written before its
// container was (re)created counts as starting.
func (a *App) Statuses(ctx context.Context) ([]status.Status, error) {
	servers, err := a.Servers(ctx)
	if err != nil {
		return nil, err
	}
	var out []status.Status
	for _, s := range servers {
		if s.Status == nil || !s.Running() {
			continue
		}
		st := *s.Status
		if s.State() == "starting" {
			st.State = status.StateStarting
		}
		out = append(out, st)
	}
	return out, nil
}

// modFleet is the Docker fleet as a mod rollout sees it.
type modFleet struct{ a *App }

func (f modFleet) Statuses(ctx context.Context) ([]status.Status, error) { return f.a.Statuses(ctx) }

func (f modFleet) ReadLock(server string) (mods.Lock, error) {
	return mods.ReadLock(filepath.Join(f.a.Cfg.InstanceDir(server), LockName))
}

func (f modFleet) WriteLock(server string, l mods.Lock) error {
	if err := f.a.prepareInstanceDir(server); err != nil {
		return err
	}
	p := filepath.Join(f.a.Cfg.InstanceDir(server), LockName)
	if err := mods.WriteLock(p, l); err != nil {
		return err
	}
	if err := os.Chown(p, f.a.Cfg.UID, f.a.Cfg.UID); err != nil && os.Geteuid() == 0 {
		return err
	}
	return nil
}

func (f modFleet) Restart(ctx context.Context, server string) error {
	return f.a.Restart(ctx, server, true)
}

// usesMods reports whether any server has mods listed or a lock to update.
func (a *App) usesMods() bool {
	for _, in := range a.Cfg.Instances {
		set := a.Cfg.Effective(in)
		if len(set.Mods) > 0 || set.Local() {
			return true
		}
		if _, err := os.Stat(filepath.Join(a.Cfg.InstanceDir(in.Name), LockName)); err == nil {
			return true
		}
	}
	return false
}

// ModRow is one mod a server runs, as `mods ls` and the TUI show it.
type ModRow struct {
	Server, ID, Version, State string
}

// ModRows reads each server's lock (only: one server) and what its last start
// made of it.
func (a *App) ModRows(ctx context.Context, only string) ([]ModRow, error) {
	servers, err := a.Servers(ctx)
	if err != nil {
		return nil, err
	}
	var rows []ModRow
	for _, in := range a.Cfg.Instances {
		if only != "" && in.Name != only {
			continue
		}
		lock, err := mods.ReadLock(filepath.Join(a.Cfg.InstanceDir(in.Name), LockName))
		if err != nil {
			return nil, err
		}
		state := map[string]string{}
		for _, s := range servers {
			if s.Instance.Name != in.Name || s.Status == nil {
				continue
			}
			for _, m := range s.Status.Mods {
				switch {
				case m.Error != "":
					state[m.ID+" "+m.Version] = "failed: " + m.Error
				case m.Loaded:
					state[m.ID+" "+m.Version] = "loaded"
				default:
					state[m.ID+" "+m.Version] = "placed"
				}
			}
			for _, sk := range s.Status.ModsSkipped {
				rows = append(rows, ModRow{in.Name, "-", "-", "left out: " + sk})
			}
		}
		for _, m := range lock.Mods {
			st := state[m.ID+" "+m.Version]
			if st == "" {
				st = "from the next start"
			}
			rows = append(rows, ModRow{in.Name, m.ID, m.Version, st})
		}
		if lock.LocalPlugins {
			rows = append(rows, ModRow{in.Name, "(local DLLs)", "-", "on"})
		}
	}
	return rows, nil
}

// SpecList is a mod list in one line.
func SpecList(ss []mods.Spec) string {
	if len(ss) == 0 {
		return "none"
	}
	var out []string
	for _, s := range ss {
		t := s.ID
		if s.Version != "" {
			t += "@" + s.Version
		}
		if s.Enabled != nil && !*s.Enabled {
			t += " (off)"
		}
		if len(s.Args) > 0 {
			var kv []string
			for k, v := range s.Args {
				kv = append(kv, k+"="+v)
			}
			sort.Strings(kv)
			t += " [" + strings.Join(kv, " ") + "]"
		}
		out = append(out, t)
	}
	return strings.Join(out, ", ")
}
