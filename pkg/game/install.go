package game

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// DefaultMirror is the public rsync module with the game files.
const DefaultMirror = "evr@files.echovr.de::files"

// CurrentLink names the active build inside a games directory.
const CurrentLink = "current"

// Progress reports an install step.
type Progress struct {
	Step    string
	Percent int // -1 when unknown
	Detail  string
}

// Installer puts verified game trees into Dir/<version> and points Dir/current
// at the active one.
type Installer struct {
	Dir      string
	Progress func(Progress)
}

func (in Installer) report(step string, pct int, detail string) {
	if in.Progress != nil {
		in.Progress(Progress{Step: step, Percent: pct, Detail: detail})
	}
}

func (in Installer) incoming() string { return filepath.Join(in.Dir, ".incoming") }

// Current returns the active build directory, if any.
func (in Installer) Current() (Info, error) {
	return Verify(filepath.Join(in.Dir, CurrentLink))
}

// FromMirror downloads the game with rsync. An interrupted download resumes
// where it stopped.
func (in Installer) FromMirror(ctx context.Context, mirror string) (Info, error) {
	if mirror == "" {
		mirror = DefaultMirror
	}
	if _, err := exec.LookPath("rsync"); err != nil {
		return Info{}, errors.New("rsync is not installed (apt install rsync)")
	}
	dst := in.incoming()
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return Info{}, err
	}
	// Seed from the active build so an update only transfers differences.
	if cur := filepath.Join(in.Dir, CurrentLink); isDir(cur) && isEmpty(dst) {
		in.report("Preparing", -1, "copying the installed build as a starting point")
		if err := copyTree(ctx, cur+"/.", dst, true); err != nil {
			return Info{}, err
		}
	}
	in.report("Downloading", 0, mirror)
	args := []string{"-rt", "--partial", "--delete", "--compress-level=0", "--no-inc-recursive", "--info=progress2", strings.TrimSuffix(mirror, "/") + "/", dst + "/"}
	cmd := exec.CommandContext(ctx, "rsync", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Info{}, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return Info{}, err
	}
	parseRsyncProgress(stdout, func(pct int, detail string) { in.report("Downloading", pct, detail) })
	if err := cmd.Wait(); err != nil {
		return Info{}, fmt.Errorf("rsync from %s failed: %v: %s", mirror, err, strings.TrimSpace(stderr.String()))
	}
	return in.activate(dst)
}

// FromDir imports an existing game directory. Files are hard-linked when src
// is on the same filesystem, otherwise copied; src is never modified.
func (in Installer) FromDir(ctx context.Context, src string) (Info, error) {
	if _, err := Verify(src); err != nil {
		return Info{}, err
	}
	dst := in.incoming()
	os.RemoveAll(dst)
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return Info{}, err
	}
	in.report("Copying", -1, src)
	if err := copyTree(ctx, strings.TrimSuffix(src, "/")+"/.", dst, true); err != nil {
		return Info{}, err
	}
	return in.activate(dst)
}

// FromArchive unpacks a .zip, .7z or .tar(.gz/.xz/.zst) archive of the game.
func (in Installer) FromArchive(ctx context.Context, archive string) (Info, error) {
	dst := in.incoming()
	os.RemoveAll(dst)
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return Info{}, err
	}
	lower := strings.ToLower(archive)
	var cmd *exec.Cmd
	switch {
	case strings.HasSuffix(lower, ".zip"):
		cmd = exec.CommandContext(ctx, "unzip", "-q", archive, "-d", dst)
	case strings.HasSuffix(lower, ".7z"):
		cmd = exec.CommandContext(ctx, "7z", "x", "-y", "-o"+dst, archive)
	case strings.Contains(lower, ".tar"), strings.HasSuffix(lower, ".tgz"):
		cmd = exec.CommandContext(ctx, "tar", "-xf", archive, "-C", dst)
	default:
		return Info{}, fmt.Errorf("unsupported archive %s (use .zip, .7z or .tar.*)", archive)
	}
	in.report("Unpacking", -1, archive)
	if out, err := cmd.CombinedOutput(); err != nil {
		return Info{}, fmt.Errorf("unpacking %s: %v: %s", archive, err, lastLine(out))
	}
	// Archives usually contain a top-level ready-at-dawn-echo-arena folder.
	root, err := findGameRoot(dst)
	if err != nil {
		return Info{}, err
	}
	if root != dst {
		tmp := dst + ".root"
		os.RemoveAll(tmp)
		if err := os.Rename(root, tmp); err != nil {
			return Info{}, err
		}
		os.RemoveAll(dst)
		if err := os.Rename(tmp, dst); err != nil {
			return Info{}, err
		}
	}
	return in.activate(dst)
}

// activate verifies dir, moves it to Dir/<version> and switches current.
func (in Installer) activate(dir string) (Info, error) {
	in.report("Verifying", -1, "")
	info, err := Verify(dir)
	if err != nil {
		return info, err
	}
	final := filepath.Join(in.Dir, info.Build.Version)
	if final != dir {
		old := final + ".old"
		os.RemoveAll(old)
		if isDir(final) {
			if err := os.Rename(final, old); err != nil {
				return info, err
			}
		}
		if err := os.Rename(dir, final); err != nil {
			return info, err
		}
		os.RemoveAll(old)
	}
	// The shared tree is mounted read-only; make sure every container user can read it.
	if out, err := exec.Command("chmod", "-R", "a+rX,go-w", final).CombinedOutput(); err != nil {
		return info, fmt.Errorf("chmod %s: %v: %s", final, err, lastLine(out))
	}
	link := filepath.Join(in.Dir, CurrentLink)
	tmp := link + ".tmp"
	os.Remove(tmp)
	if err := os.Symlink(info.Build.Version, tmp); err != nil {
		return info, err
	}
	if err := os.Rename(tmp, link); err != nil {
		return info, err
	}
	info.Dir = final
	in.report("Done", 100, final)
	return info, nil
}

func findGameRoot(dir string) (string, error) {
	var found string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && strings.Count(strings.TrimPrefix(p, dir), string(os.PathSeparator)) > 3 {
			return filepath.SkipDir
		}
		if !d.IsDir() && d.Name() == ExeName && filepath.Base(filepath.Dir(p)) == "win10" {
			found = filepath.Dir(filepath.Dir(filepath.Dir(p)))
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if found == "" {
		return "", errors.New("no bin/win10/echovr.exe found in the archive")
	}
	return found, nil
}

// copyTree copies src into dst, preferring hard links.
func copyTree(ctx context.Context, src, dst string, link bool) error {
	if link {
		if err := exec.CommandContext(ctx, "cp", "-al", src, dst).Run(); err == nil {
			return nil
		}
	}
	if out, err := exec.CommandContext(ctx, "cp", "-a", src, dst).CombinedOutput(); err != nil {
		return fmt.Errorf("copy %s: %v: %s", src, err, lastLine(out))
	}
	return nil
}

var progress2Re = regexp.MustCompile(`([\d,]+)\s+(\d+)%\s+(\S+/s)\s+(\S+)`)

// parseRsyncProgress reads --info=progress2 output, which rewrites one line with \r.
func parseRsyncProgress(r io.Reader, f func(int, string)) {
	sc := bufio.NewScanner(r)
	sc.Split(func(data []byte, atEOF bool) (int, []byte, error) {
		for i, b := range data {
			if b == '\r' || b == '\n' {
				return i + 1, data[:i], nil
			}
		}
		if atEOF && len(data) > 0 {
			return len(data), data, nil
		}
		return 0, nil, nil
	})
	last := -1
	for sc.Scan() {
		m := progress2Re.FindStringSubmatch(sc.Text())
		if m == nil {
			continue
		}
		pct, _ := strconv.Atoi(m[2])
		if pct != last {
			last = pct
			f(pct, m[3]+", "+m[4]+" left")
		}
	}
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func isEmpty(p string) bool {
	entries, err := os.ReadDir(p)
	return err == nil && len(entries) == 0
}

func lastLine(b []byte) string {
	s := strings.TrimSpace(string(b))
	if i := strings.LastIndex(s, "\n"); i >= 0 {
		s = s[i+1:]
	}
	return s
}
