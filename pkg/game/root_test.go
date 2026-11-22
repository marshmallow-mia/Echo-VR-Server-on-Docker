package game

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNevrRoot(t *testing.T) {
	if got, want := NevrRoot("/home/container/.wine", "container"), "/home/container/.wine/drive_c/users/container/AppData/Local/nevr/echovr"; got != want {
		t.Errorf("got %s want %s", got, want)
	}
}

func TestRootPrepare(t *testing.T) {
	dir := t.TempDir()
	rt := filepath.Join(dir, "store", RuntimeName)
	os.MkdirAll(filepath.Dir(rt), 0o755)
	os.WriteFile(rt, []byte("runtime 1"), 0o644)
	legacy := filepath.Join(dir, "_local", CredentialsName)
	os.MkdirAll(filepath.Dir(legacy), 0o755)
	r := Root{Dir: filepath.Join(dir, "root"), RuntimeDLL: rt, ConfigYAML: []byte("version: 1\n"), LegacyCredentials: legacy}

	// No sign-in anywhere yet: nothing to copy, and that is not an error.
	if err := r.Prepare(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(r.Dir, CredentialsName)); !os.IsNotExist(err) {
		t.Error("sign-in made up")
	}
	read := func(name string) string { b, _ := os.ReadFile(filepath.Join(r.Dir, name)); return string(b) }
	if read(RuntimeName) != "runtime 1" || read("config.yaml") != "version: 1\n" {
		t.Errorf("runtime %q config %q", read(RuntimeName), read("config.yaml"))
	}
	if fi, err := os.Lstat(filepath.Join(r.Dir, RuntimeName)); err != nil || !fi.Mode().IsRegular() {
		t.Error("runtime must be a copy, not a link")
	}

	os.WriteFile(legacy, []byte("old link"), 0o600)
	if err := r.Prepare(); err != nil {
		t.Fatal(err)
	}
	if read(CredentialsName) != "old link" {
		t.Errorf("legacy sign-in not copied: %q", read(CredentialsName))
	}
	if fi, _ := os.Stat(filepath.Join(r.Dir, CredentialsName)); fi.Mode().Perm() != 0o600 {
		t.Errorf("sign-in mode %v", fi.Mode().Perm())
	}

	// The runtime rotated the root's token; a newer runtime arrives.
	os.WriteFile(filepath.Join(r.Dir, CredentialsName), []byte("rotated"), 0o600)
	os.WriteFile(rt, []byte("runtime 2"), 0o644)
	if err := r.Prepare(); err != nil {
		t.Fatal(err)
	}
	if read(CredentialsName) != "rotated" {
		t.Errorf("root sign-in overwritten: %q", read(CredentialsName))
	}
	if read(RuntimeName) != "runtime 2" {
		t.Errorf("runtime not replaced: %q", read(RuntimeName))
	}
}

func TestHasRuntime(t *testing.T) {
	dir := t.TempDir()
	if HasRuntime(dir) {
		t.Error("empty folder")
	}
	os.WriteFile(filepath.Join(dir, RuntimeName), []byte("x"), 0o644)
	if !HasRuntime(dir) {
		t.Error("two-file folder")
	}
}

// writeState writes update/state.json as nEVR's host and installer do.
func writeState(t *testing.T, root, version, sha string) {
	t.Helper()
	os.MkdirAll(filepath.Join(root, UpdateDir), 0o755)
	b := `{"channel":"beta","current":{"version":"` + version + `","runtime_sha256":"` + sha + `"},"owned":[],"quarantine":[]}`
	if err := os.WriteFile(filepath.Join(root, UpdateDir, "state.json"), []byte(b), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSelfUpdated(t *testing.T) {
	root := t.TempDir()
	if _, ok := SelfUpdated(root); ok {
		t.Error("no state: not self-updated")
	}
	os.WriteFile(filepath.Join(root, RuntimeName), []byte("nevr's 6.18.1"), 0o644)
	sha, _ := fileSHA256(filepath.Join(root, RuntimeName))
	writeState(t, root, "6.18.1", strings.ToUpper(sha))
	if v, ok := SelfUpdated(root); !ok || v != "6.18.1" {
		t.Errorf("got %q %v", v, ok)
	}
	// The host tool copied its own runtime over it: no longer nEVR's.
	os.WriteFile(filepath.Join(root, RuntimeName), []byte("the store's 6.17.0"), 0o644)
	if _, ok := SelfUpdated(root); ok {
		t.Error("a runtime state.json doesn't name is not self-updated")
	}
	os.WriteFile(filepath.Join(root, UpdateDir, "state.json"), []byte("{not json"), 0o644)
	if _, ok := SelfUpdated(root); ok {
		t.Error("damaged state")
	}
}

func TestRootPrepareSelfUpdate(t *testing.T) {
	dir := t.TempDir()
	rt := filepath.Join(dir, "store", RuntimeName)
	os.MkdirAll(filepath.Dir(rt), 0o755)
	os.WriteFile(rt, []byte("store"), 0o644)
	root := filepath.Join(dir, "root")
	read := func() string { b, _ := os.ReadFile(filepath.Join(root, RuntimeName)); return string(b) }

	// KeepRuntime with an empty root still puts the store's runtime there.
	r := Root{Dir: root, RuntimeDLL: rt, KeepRuntime: true}
	if err := r.Prepare(); err != nil || read() != "store" {
		t.Fatalf("%v %q", err, read())
	}
	os.WriteFile(filepath.Join(root, RuntimeName), []byte("nevr's"), 0o644)
	if err := r.Prepare(); err != nil || read() != "nevr's" {
		t.Fatalf("kept runtime replaced: %v %q", err, read())
	}

	// ClearUpdate removes what nEVR staged or swapped, keeps state.json.
	writeState(t, root, "6.17.0", "00")
	for _, name := range []string{"staged.json", "pending.json", "swap.json", "crashed"} {
		os.WriteFile(filepath.Join(root, UpdateDir, name), []byte("{}"), 0o644)
	}
	os.MkdirAll(filepath.Join(root, UpdateDir, "staging", "6.18.1"), 0o755)
	r = Root{Dir: root, RuntimeDLL: rt, ClearUpdate: true}
	if err := r.Prepare(); err != nil {
		t.Fatal(err)
	}
	if read() != "store" {
		t.Errorf("runtime %q", read())
	}
	left, _ := os.ReadDir(filepath.Join(root, UpdateDir))
	if len(left) != 1 || left[0].Name() != "state.json" {
		t.Errorf("update folder after clearing: %v", left)
	}
}
