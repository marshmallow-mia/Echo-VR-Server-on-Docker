package game

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// V1LogsDir is where the logs of a v1 installation are kept, below the data directory.
const V1LogsDir = "logs/v1"

// MoveLogs moves the logs folder of an old game tree (logs/<server>/…,
// logs/old/…) into dst, keeping its layout. An entry that exists in dst
// already gets a numeric suffix. It returns how many entries were moved.
func MoveLogs(gameDir, dst string) (int, error) {
	src := filepath.Join(gameDir, "logs")
	entries, err := os.ReadDir(src)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	} else if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return 0, err
	}
	n := 0
	for _, e := range entries {
		from, to := filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())
		for i := 2; exists(to); i++ {
			to = filepath.Join(dst, fmt.Sprintf("%s-%d", e.Name(), i))
		}
		if err := move(from, to); err != nil {
			return n, err
		}
		n++
	}
	_ = os.Remove(src)
	return n, nil
}

// move renames, or copies and deletes across filesystems.
func move(from, to string) error {
	err := os.Rename(from, to)
	if err == nil || !errors.Is(err, syscall.EXDEV) {
		return err
	}
	if out, err := exec.Command("cp", "-a", from, to).CombinedOutput(); err != nil {
		return fmt.Errorf("copy %s: %v: %s", from, err, lastLine(out))
	}
	return os.RemoveAll(from)
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// RemoveEntries deletes the named entries of dir, then dir itself when
// nothing else is left in it. Used for old installations, so files the
// user added next to them are kept.
func RemoveEntries(dir string, names []string) error {
	for _, n := range names {
		if err := os.RemoveAll(filepath.Join(dir, n)); err != nil {
			return err
		}
	}
	if isEmpty(dir) {
		return os.Remove(dir)
	}
	return nil
}

// DirSize is the size of the files below p, in bytes.
func DirSize(p string) int64 {
	var n int64
	_ = filepath.WalkDir(p, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if fi, err := d.Info(); err == nil {
				n += fi.Size()
			}
		}
		return nil
	})
	return n
}

// Size formats a byte count for people.
func Size(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
