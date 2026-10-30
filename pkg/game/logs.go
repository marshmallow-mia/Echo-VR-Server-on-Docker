package game

import (
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Limits for a server's nEVR run logs (PruneLogs).
var (
	// LogKeep is how long compressed run logs are kept.
	LogKeep = 14 * 24 * time.Hour
	// LogMaxBytes caps the compressed run logs of one server.
	LogMaxBytes int64 = 200 << 20
)

// PruneLogs tidies the nEVR root's logs folder before a start. The runtime
// writes one nevr-<start>.jsonl per run (a new one after every match) and
// removes none. The newest run log stays as it is; older ones are gzipped;
// compressed ones older than LogKeep, then the oldest beyond LogMaxBytes, are
// removed. Other files (nevr-bootstrap.jsonl, crash records) are not touched.
// It must run while the game is stopped, so no log is open.
func PruneLogs(dir string, now time.Time) (compressed, removed int, err error) {
	es, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return 0, 0, nil
	} else if err != nil {
		return 0, 0, err
	}
	var plain []string
	for _, e := range es {
		if n := e.Name(); e.Type().IsRegular() && isRunLog(n) && strings.HasSuffix(n, ".jsonl") {
			plain = append(plain, n)
		}
	}
	sort.Strings(plain) // nevr-<UTC start>.jsonl: by name is by start
	var errs []error
	for i, n := range plain {
		if i == len(plain)-1 {
			break // the newest run
		}
		if err := gzipFile(filepath.Join(dir, n)); err != nil {
			errs = append(errs, err)
			continue
		}
		compressed++
	}

	type gz struct {
		name string
		size int64
		mod  time.Time
	}
	var gzs []gz
	if es, err = os.ReadDir(dir); err != nil {
		return compressed, 0, errors.Join(append(errs, err)...)
	}
	for _, e := range es {
		if n := e.Name(); e.Type().IsRegular() && isRunLog(n) && strings.HasSuffix(n, ".jsonl.gz") {
			if fi, err := e.Info(); err == nil {
				gzs = append(gzs, gz{n, fi.Size(), fi.ModTime()})
			}
		}
	}
	sort.Slice(gzs, func(i, j int) bool { return gzs[i].name < gzs[j].name })
	var total int64
	for _, g := range gzs {
		total += g.size
	}
	for _, g := range gzs {
		if now.Sub(g.mod) <= LogKeep && total <= LogMaxBytes {
			break
		}
		if err := os.Remove(filepath.Join(dir, g.name)); err != nil {
			errs = append(errs, err)
			continue
		}
		total -= g.size
		removed++
	}
	return compressed, removed, errors.Join(errs...)
}

func isRunLog(name string) bool {
	return strings.HasPrefix(name, "nevr-") && !strings.HasPrefix(name, "nevr-bootstrap")
}

// gzipFile replaces path with path.gz, keeping its modification time.
func gzipFile(path string) error {
	in, err := os.Open(path)
	if err != nil {
		return err
	}
	defer in.Close()
	fi, err := in.Stat()
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".gz-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // a no-op after the rename
	w := gzip.NewWriter(tmp)
	w.Name = filepath.Base(path)
	if _, err := io.Copy(w, in); err != nil {
		tmp.Close()
		return err
	}
	if err := w.Close(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(fi.Mode().Perm()); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chtimes(tmp.Name(), fi.ModTime(), fi.ModTime()); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path+".gz"); err != nil {
		return err
	}
	return os.Remove(path)
}
