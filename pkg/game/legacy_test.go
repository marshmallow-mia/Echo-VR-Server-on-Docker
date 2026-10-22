package game

import (
	"os"
	"path/filepath"
	"testing"
)

func touch(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestMoveLogs(t *testing.T) {
	old, data := t.TempDir(), t.TempDir()
	touch(t, filepath.Join(old, "logs", "abc123", "a.log"))
	touch(t, filepath.Join(old, "logs", "old", "def456", "b.log"))
	dst := filepath.Join(data, V1LogsDir)
	touch(t, filepath.Join(dst, "abc123", "earlier.log")) // a second migration
	n, err := MoveLogs(old, dst)
	if err != nil || n != 2 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	for _, p := range []string{"abc123/earlier.log", "abc123-2/a.log", "old/def456/b.log"} {
		if !exists(filepath.Join(dst, p)) {
			t.Errorf("missing %s", p)
		}
	}
	if exists(filepath.Join(old, "logs")) {
		t.Error("old logs folder left")
	}
	if n, err := MoveLogs(t.TempDir(), dst); n != 0 || err != nil {
		t.Errorf("no logs: n=%d err=%v", n, err)
	}
}

func TestRemoveEntries(t *testing.T) {
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	touch(t, filepath.Join(repo, "run.sh"))
	touch(t, filepath.Join(repo, "scripts", "start-echo.sh"))
	touch(t, filepath.Join(repo, "mine.txt"))
	if err := RemoveEntries(repo, []string{"run.sh", "scripts", "missing"}); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(repo, "run.sh")) || !exists(filepath.Join(repo, "mine.txt")) {
		t.Fatal("wrong entries removed")
	}
	os.Remove(filepath.Join(repo, "mine.txt"))
	touch(t, filepath.Join(repo, "run.sh"))
	if err := RemoveEntries(repo, []string{"run.sh"}); err != nil || exists(repo) {
		t.Fatalf("empty folder kept: %v", err)
	}
}

func TestSize(t *testing.T) {
	for n, want := range map[int64]string{12: "12 B", 2048: "2.0 KiB", 5 << 30: "5.0 GiB"} {
		if got := Size(n); got != want {
			t.Errorf("%d: %q want %q", n, got, want)
		}
	}
}
