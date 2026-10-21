package game

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const stockNetconfig = `{
    "broadcaster_init": {
        "name": "R15NetDedicatedServer",
        "port": 6792,
        "retries": 10,
        "allow_incoming": false,
    }
}
`

func fakeGame(t *testing.T) string {
	t.Helper()
	g := t.TempDir()
	files := map[string]string{
		"bin/win10/echovr.exe":     "exe",
		"bin/win10/pnsrad.dll":     "dll",
		"bin/win10/BugSplat64.dll": "stock crash reporter",
		"bin/win10/dbgcore.dll":    "legacy loader",
		"bin/win10/scripts/a.dll":  "script",
		"content/x.pak":            "content",
		"_local/gui/ui.json":       "gui",
		"_local/config.json":       `{"publisher_lock":"rad15_live"}`,
		"sourcedb/rad15/json/r14/config/netconfig_dedicatedserver.json": stockNetconfig,
		"sourcedb/rad15/json/r14/config/netconfig_client.json":          "{}",
		"sourcedb/rad15/json/r14/other.json":                            "{}",
		"sourcedb/rad15/other/b.json":                                   "{}",
	}
	for rel, body := range files {
		p := filepath.Join(g, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o444); err != nil {
			t.Fatal(err)
		}
	}
	return g
}

func readlink(t *testing.T, p string) string {
	t.Helper()
	target, err := os.Readlink(p)
	if err != nil {
		t.Fatalf("%s is not a symlink: %v", p, err)
	}
	return target
}

func TestFarmBuild(t *testing.T) {
	g := fakeGame(t)
	dll := filepath.Join(t.TempDir(), "BugSplat64.dll")
	os.WriteFile(dll, []byte("nevr"), 0o644)
	root := filepath.Join(t.TempDir(), "root")
	f := Farm{GameDir: g, Root: root, RuntimeDLL: dll, Port: 6795, PublisherLock: "echovrce", ConfigYAML: []byte("version: 1\n")}
	if err := f.Build(); err != nil {
		t.Fatal(err)
	}

	if got := readlink(t, filepath.Join(root, "bin/win10/echovr.exe")); got != filepath.Join(g, "bin/win10/echovr.exe") {
		t.Errorf("echovr.exe -> %s", got)
	}
	if got := readlink(t, filepath.Join(root, "bin/win10/BugSplat64.dll")); got != dll {
		t.Errorf("BugSplat64.dll -> %s, want runtime", got)
	}
	if _, err := os.Lstat(filepath.Join(root, "bin/win10/dbgcore.dll")); !os.IsNotExist(err) {
		t.Errorf("dbgcore.dll must not be linked: %v", err)
	}
	readlink(t, filepath.Join(root, "content"))
	readlink(t, filepath.Join(root, "_local/gui"))
	readlink(t, filepath.Join(root, "sourcedb/rad15/other"))
	readlink(t, filepath.Join(root, "sourcedb/rad15/json/r14/other.json"))
	readlink(t, filepath.Join(root, "sourcedb/rad15/json/r14/config/netconfig_client.json"))

	nc, _ := os.ReadFile(filepath.Join(root, NetconfigRel))
	if !strings.Contains(string(nc), `"port": 6795`) || !strings.Contains(string(nc), `"retries": 0`) || !strings.Contains(string(nc), "allow_incoming") {
		t.Errorf("netconfig not rewritten:\n%s", nc)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "_local/config.json")); string(b) != `{"publisher_lock":"echovrce"}` {
		t.Errorf("config.json = %s", b)
	}
	for _, d := range []string{"_local", "_local/r14logs", "bin", "bin/win10", "bin/win10/logs", "_temp"} {
		fi, err := os.Lstat(filepath.Join(root, d))
		if err != nil || !fi.IsDir() {
			t.Errorf("%s should be a real directory", d)
		}
	}

	// Instance-owned files survive a rebuild; a removed port override is dropped.
	cred := filepath.Join(root, "_local/.credentials.json")
	os.WriteFile(cred, []byte("{}"), 0o600)
	f.Port = 0
	if err := f.Build(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cred); err != nil {
		t.Errorf("credentials removed: %v", err)
	}
	if got := readlink(t, filepath.Join(root, "sourcedb")); got != filepath.Join(g, "sourcedb") {
		t.Errorf("sourcedb -> %s after dropping the port override", got)
	}
}

func TestFarmRemovesStaleLinks(t *testing.T) {
	g := fakeGame(t)
	dll := filepath.Join(t.TempDir(), "BugSplat64.dll")
	os.WriteFile(dll, []byte("nevr"), 0o644)
	root := filepath.Join(t.TempDir(), "root")
	f := Farm{GameDir: g, Root: root, RuntimeDLL: dll}
	if err := f.Build(); err != nil {
		t.Fatal(err)
	}
	os.Symlink("/nonexistent/old.dll", filepath.Join(root, "bin/win10/old.dll"))
	if err := f.Build(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(root, "bin/win10/old.dll")); !os.IsNotExist(err) {
		t.Error("stale link kept")
	}
	if _, err := os.Lstat(filepath.Join(root, "_local/config.json")); !os.IsNotExist(err) {
		t.Error("config.json written without a publisher lock")
	}
}
