package game

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Farm describes a per-instance view of a shared game tree: directories the
// game writes to are real, everything else is a symlink into GameDir.
type Farm struct {
	// GameDir is the shared, possibly read-only game tree.
	GameDir string
	// Root is the instance's own tree; echovr.exe runs from Root/bin/win10.
	Root string
	// RuntimeDLL is the nevr-runtime BugSplat64.dll to link in.
	RuntimeDLL string
	// Port overrides the broadcaster port in netconfig_dedicatedserver.json.
	// nevr-runtime 5.4.2 ignores -port, so this is the only way to set it.
	// Zero keeps the game's value.
	Port int
	// PublisherLock is written to _local/config.json. Empty writes no file,
	// which makes the runtime use "echotools".
	PublisherLock string
	// ConfigYAML is written to _local/config.yaml when not nil.
	ConfigYAML []byte
}

// skipInBin are game files the farm never links: the runtime replaces
// BugSplat64.dll, a legacy dbgcore.dll is fatal, and the rest are written per
// instance or are left-overs of older runtimes.
var skipInBin = map[string]bool{
	"BugSplat64.dll": true, "dbgcore.dll": true, "logs": true, "_temp": true,
	"plugins": true, "plugin_logs": true,
}

// skipInLocal are per-instance files in _local that must not come from the
// shared tree.
var skipInLocal = map[string]bool{
	"config.json": true, "config.yaml": true, ".credentials.json": true, "r14logs": true,
}

// Build creates or refreshes the farm. It is idempotent and keeps everything
// the instance wrote itself (credentials, logs).
func (f Farm) Build() error {
	if f.GameDir == "" || f.Root == "" || f.RuntimeDLL == "" {
		return errors.New("farm: GameDir, Root and RuntimeDLL are required")
	}
	if _, err := os.Stat(f.RuntimeDLL); err != nil {
		return fmt.Errorf("farm: runtime DLL: %w", err)
	}
	if f.Port == 0 {
		// Drop a port override from an earlier run; the tree only holds links
		// and the generated netconfig.
		top := filepath.Join(f.Root, strings.Split(NetconfigRel, "/")[0])
		if fi, err := os.Lstat(top); err == nil && fi.IsDir() {
			if err := os.RemoveAll(top); err != nil {
				return err
			}
		}
	}
	for _, d := range []string{"_local/r14logs", "_temp", BinDir + "/logs", BinDir + "/_temp"} {
		if err := os.MkdirAll(filepath.Join(f.Root, d), 0o755); err != nil {
			return err
		}
	}
	steps := []struct {
		rel  string
		skip map[string]bool
	}{
		{"", map[string]bool{"bin": true, "_local": true, "_temp": true}},
		{"bin", map[string]bool{"win10": true}},
		{BinDir, skipInBin},
		{"_local", skipInLocal},
	}
	for _, s := range steps {
		if err := f.linkDir(s.rel, s.skip); err != nil {
			return err
		}
	}
	if err := replaceSymlink(f.RuntimeDLL, filepath.Join(f.Root, BinDir, "BugSplat64.dll")); err != nil {
		return err
	}
	if err := f.writeNetconfig(); err != nil {
		return err
	}
	if f.PublisherLock != "" {
		b, _ := json.Marshal(map[string]string{"publisher_lock": f.PublisherLock})
		if err := writeFileAtomic(filepath.Join(f.Root, "_local/config.json"), b, 0o644); err != nil {
			return err
		}
	} else if err := os.Remove(filepath.Join(f.Root, "_local/config.json")); err != nil && !os.IsNotExist(err) {
		return err
	}
	if f.ConfigYAML != nil {
		return writeFileAtomic(filepath.Join(f.Root, "_local/config.yaml"), f.ConfigYAML, 0o640)
	}
	return nil
}

// ExeDir is the directory echovr.exe must be started from.
func (f Farm) ExeDir() string { return filepath.Join(f.Root, BinDir) }

// linkDir makes Root/rel a real directory holding symlinks to every entry of
// GameDir/rel that is not skipped, and removes links that no longer point at a
// game entry (e.g. after switching game versions).
func (f Farm) linkDir(rel string, skip map[string]bool) error {
	src, dst := filepath.Join(f.GameDir, rel), filepath.Join(f.Root, rel)
	if err := ensureRealDir(dst); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	want := map[string]bool{}
	for _, e := range entries {
		if skip[e.Name()] {
			continue
		}
		want[e.Name()] = true
		target := filepath.Join(src, e.Name())
		link := filepath.Join(dst, e.Name())
		if fi, err := os.Lstat(link); err == nil && fi.Mode()&os.ModeSymlink == 0 {
			continue // the instance has its own file or directory here; keep it
		}
		if err := replaceSymlink(target, link); err != nil {
			return err
		}
	}
	existing, err := os.ReadDir(dst)
	if err != nil {
		return err
	}
	for _, e := range existing {
		if want[e.Name()] || e.Type()&os.ModeSymlink == 0 {
			continue
		}
		if rel == BinDir && e.Name() == "BugSplat64.dll" {
			continue
		}
		if err := os.Remove(filepath.Join(dst, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

var portRe = regexp.MustCompile(`("port"\s*:\s*)\d+`)
var retriesRe = regexp.MustCompile(`("retries"\s*:\s*)\d+`)

// writeNetconfig gives the instance its own netconfig_dedicatedserver.json by
// turning each directory on the way into a real one.
func (f Farm) writeNetconfig() error {
	if f.Port == 0 {
		return nil
	}
	if f.Port < 1 || f.Port > 65535 {
		return fmt.Errorf("farm: port %d out of range", f.Port)
	}
	dir := filepath.Dir(NetconfigRel)
	parts := strings.Split(dir, "/")
	for i := range parts {
		rel := filepath.Join(parts[:i+1]...)
		skip := map[string]bool{}
		if i+1 < len(parts) {
			skip[parts[i+1]] = true
		} else {
			skip[filepath.Base(NetconfigRel)] = true
		}
		if err := f.linkDir(rel, skip); err != nil {
			return err
		}
	}
	// The file is JSON with comments and trailing commas, so edit it textually.
	orig, err := os.ReadFile(filepath.Join(f.GameDir, NetconfigRel))
	if err != nil {
		return err
	}
	if !portRe.Match(orig) {
		return fmt.Errorf("farm: no \"port\" key in %s", NetconfigRel)
	}
	out := portRe.ReplaceAll(orig, []byte("${1}"+strconv.Itoa(f.Port)))
	out = retriesRe.ReplaceAll(out, []byte("${1}0"))
	return writeFileAtomic(filepath.Join(f.Root, NetconfigRel), out, 0o644)
}

func ensureRealDir(p string) error {
	fi, err := os.Lstat(p)
	if err == nil && fi.Mode()&os.ModeSymlink != 0 {
		if err := os.Remove(p); err != nil {
			return err
		}
	} else if err == nil && !fi.IsDir() {
		return fmt.Errorf("farm: %s exists and is not a directory", p)
	}
	return os.MkdirAll(p, 0o755)
}

func replaceSymlink(target, link string) error {
	if cur, err := os.Readlink(link); err == nil && cur == target {
		return nil
	}
	tmp := link + ".tmp-link"
	_ = os.Remove(tmp)
	if err := os.Symlink(target, tmp); err != nil {
		return err
	}
	return os.Rename(tmp, link)
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}
