package game

import (
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeLog(t *testing.T, dir, name, body string, mod time.Time) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mod, mod); err != nil {
		t.Fatal(err)
	}
	return p
}

func gunzip(t *testing.T, p string) string {
	t.Helper()
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	r, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func names(t *testing.T, dir string) string {
	t.Helper()
	es, _ := os.ReadDir(dir)
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	return strings.Join(out, " ")
}

// The runtime writes one nevr-<start>.jsonl per run into the root's logs and
// never removes one: older runs are compressed, the newest stays plain.
func TestPruneLogsCompresses(t *testing.T) {
	dir, now := t.TempDir(), time.Now()
	writeLog(t, dir, "nevr-2026-10-28T10-00-00.000.jsonl", "old run\n", now.Add(-48*time.Hour))
	writeLog(t, dir, "nevr-2026-10-29T10-00-00.000.jsonl", "previous run\n", now.Add(-24*time.Hour))
	writeLog(t, dir, "nevr-2026-10-30T10-00-00.000.jsonl", "last run\n", now.Add(-time.Hour))
	writeLog(t, dir, "nevr-bootstrap.jsonl", "host\n", now.Add(-72*time.Hour))
	writeLog(t, dir, "other.txt", "x", now.Add(-72*time.Hour))
	c, r, err := PruneLogs(dir, now)
	if err != nil || c != 2 || r != 0 {
		t.Fatalf("compressed %d removed %d err %v", c, r, err)
	}
	want := "nevr-2026-10-28T10-00-00.000.jsonl.gz nevr-2026-10-29T10-00-00.000.jsonl.gz nevr-2026-10-30T10-00-00.000.jsonl nevr-bootstrap.jsonl other.txt"
	if got := names(t, dir); got != want {
		t.Fatalf("files:\n%s\nwant:\n%s", got, want)
	}
	if got := gunzip(t, filepath.Join(dir, "nevr-2026-10-29T10-00-00.000.jsonl.gz")); got != "previous run\n" {
		t.Errorf("compressed content %q", got)
	}
	// Idempotent: nothing left to do.
	if c, r, err := PruneLogs(dir, now); c != 0 || r != 0 || err != nil {
		t.Errorf("second run: compressed %d removed %d err %v", c, r, err)
	}
}

func TestPruneLogsAge(t *testing.T) {
	dir, now := t.TempDir(), time.Now()
	writeLog(t, dir, "nevr-2026-10-01T10-00-00.000.jsonl", "too old\n", now.Add(-(LogKeep + time.Hour)))
	writeLog(t, dir, "nevr-2026-10-20T10-00-00.000.jsonl", "kept\n", now.Add(-LogKeep+time.Hour))
	writeLog(t, dir, "nevr-2026-10-30T10-00-00.000.jsonl", "last\n", now)
	if _, r, err := PruneLogs(dir, now); r != 1 || err != nil {
		t.Fatalf("removed %d err %v", r, err)
	}
	if got := names(t, dir); got != "nevr-2026-10-20T10-00-00.000.jsonl.gz nevr-2026-10-30T10-00-00.000.jsonl" {
		t.Fatalf("files: %s", got)
	}
	// The newest run is never removed, however old.
	dir2 := t.TempDir()
	writeLog(t, dir2, "nevr-2025-01-01T10-00-00.000.jsonl", "ancient\n", now.Add(-365*24*time.Hour))
	if _, r, _ := PruneLogs(dir2, now); r != 0 {
		t.Errorf("newest run removed")
	}
}

func TestPruneLogsSize(t *testing.T) {
	dir, now := t.TempDir(), time.Now()
	old := LogMaxBytes
	LogMaxBytes = 3000
	t.Cleanup(func() { LogMaxBytes = old })
	// Random-ish lines so gzip cannot shrink them below the limit.
	for i := 0; i < 4; i++ {
		var b strings.Builder
		for j := 0; j < 200; j++ {
			fmt.Fprintf(&b, "%d %x\n", j, uint64(j*7919+i)*0x9E3779B97F4A7C15)
		}
		writeLog(t, dir, fmt.Sprintf("nevr-2026-10-2%dT10-00-00.000.jsonl", i), b.String(), now.Add(time.Duration(i-4)*time.Hour))
	}
	_, r, err := PruneLogs(dir, now)
	if err != nil || r == 0 {
		t.Fatalf("removed %d err %v", r, err)
	}
	var total int64
	es, _ := os.ReadDir(dir)
	for _, e := range es {
		if fi, _ := e.Info(); strings.HasSuffix(e.Name(), ".gz") {
			total += fi.Size()
		}
	}
	if total > LogMaxBytes {
		t.Errorf("compressed logs total %d > %d: %s", total, LogMaxBytes, names(t, dir))
	}
	if !strings.Contains(names(t, dir), "nevr-2026-10-23T10-00-00.000.jsonl") {
		t.Errorf("newest run removed: %s", names(t, dir))
	}
	if !strings.Contains(names(t, dir), "nevr-2026-10-22T10-00-00.000.jsonl.gz") {
		t.Errorf("removed more than the limit needs: %s", names(t, dir))
	}
	if strings.Contains(names(t, dir), "2026-10-20") {
		t.Errorf("oldest kept while over the limit: %s", names(t, dir))
	}
}

func TestPruneLogsMissingDir(t *testing.T) {
	if c, r, err := PruneLogs(filepath.Join(t.TempDir(), "nope"), time.Now()); c != 0 || r != 0 || err != nil {
		t.Errorf("missing dir: %d %d %v", c, r, err)
	}
}
