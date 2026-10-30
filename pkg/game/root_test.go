package game

import (
	"os"
	"path/filepath"
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
