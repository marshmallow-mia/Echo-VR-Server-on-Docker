package nevr

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Store is a directory of installed runtimes: <dir>/<tag>/BugSplat64.dll (and,
// from RootLayoutMin on, <dir>/<tag>/nevr-runtime.dll), plus the symlinks
// "current" and "previous". Instances resolve "current" each time
// they start, so switching it rolls out at match boundaries.
type Store struct {
	Dir  string
	HTTP *http.Client
}

const (
	CurrentLink  = "current"
	PreviousLink = "previous"
	DLLName      = "BugSplat64.dll"
	RuntimeName  = "nevr-runtime.dll" // the signed runtime of the two-file layout (game.RuntimeName); it goes into the nEVR root
	sumsName     = "SHA256SUMS"
	manifestName = "manifest.json"
)

// Installed describes one installed runtime.
type Installed struct {
	Tag       string `json:"tag"`
	ZipSHA256 string `json:"zip_sha256"`
	DLLSHA256 string `json:"dll_sha256"`
	// RuntimeSHA256 is set for a two-file release (nevr-runtime.dll beside the host).
	RuntimeSHA256 string    `json:"runtime_sha256,omitempty"`
	Published     time.Time `json:"published"`
	InstalledAt   time.Time `json:"installed_at"`
	Bad           string    `json:"bad,omitempty"` // set when a rollout failed with this tag
}

// files extracted from the server zip, by base name.
var wanted = map[string]bool{DLLName: true, RuntimeName: true, "echovr_server.exe": true, "README.md": true, "INSTALL.md": true, "README.txt": true}

// Install downloads rel, checks it against GitHub's digest and unpacks it. It is
// a no-op if the tag is already installed with the same checksum.
func (s Store) Install(ctx context.Context, rel Release) (Installed, error) {
	if rel.Blocked != "" {
		return Installed{}, fmt.Errorf("nevr-runtime %s is not installable: %s", rel.Tag, rel.Blocked)
	}
	if rel.Asset.SHA256 == "" {
		return Installed{}, fmt.Errorf("nevr-runtime %s has no checksum", rel.Tag)
	}
	if cur, err := s.Get(rel.Tag); err == nil && cur.ZipSHA256 == rel.Asset.SHA256 {
		return cur, nil
	}
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return Installed{}, err
	}
	tmp, err := os.CreateTemp(s.Dir, ".download-*.zip")
	if err != nil {
		return Installed{}, err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	sum, err := s.download(ctx, rel.Asset.URL, tmp)
	if err != nil {
		return Installed{}, err
	}
	if sum != rel.Asset.SHA256 {
		return Installed{}, fmt.Errorf("checksum mismatch for %s: got %s, GitHub says %s", rel.Asset.Name, sum, rel.Asset.SHA256)
	}
	stage, err := os.MkdirTemp(s.Dir, ".stage-"+rel.Tag+"-")
	if err != nil {
		return Installed{}, err
	}
	defer os.RemoveAll(stage)
	sums, err := unpack(tmp.Name(), stage)
	if err != nil {
		return Installed{}, err
	}
	inst := Installed{Tag: rel.Tag, ZipSHA256: sum, DLLSHA256: sums[DLLName], RuntimeSHA256: sums[RuntimeName], Published: rel.Published, InstalledAt: time.Now().UTC()}
	b, _ := json.MarshalIndent(inst, "", "  ")
	if err := os.WriteFile(filepath.Join(stage, manifestName), b, 0o644); err != nil {
		return Installed{}, err
	}
	if err := os.Chmod(stage, 0o755); err != nil {
		return Installed{}, err
	}
	final := filepath.Join(s.Dir, rel.Tag)
	old := final + ".old"
	os.RemoveAll(old)
	if _, err := os.Stat(final); err == nil {
		if err := os.Rename(final, old); err != nil {
			return Installed{}, err
		}
	}
	if err := os.Rename(stage, final); err != nil {
		return Installed{}, err
	}
	os.RemoveAll(old)
	return inst, nil
}

func (s Store) download(ctx context.Context, url string, w io.Writer) (string, error) {
	hc := s.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Minute}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download %s: %s", url, resp.Status)
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(w, h), resp.Body); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// unpack extracts the wanted files of the zip into dst and returns their
// SHA-256 by name. When the zip carries SHA256SUMS, every extracted file it
// lists must match it.
func unpack(zipPath, dst string) (map[string]string, error) {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	sums := map[string]string{}
	var listed map[string]string
	for _, f := range zr.File {
		name := filepath.Base(f.Name)
		if f.FileInfo().IsDir() {
			continue
		}
		if name == sumsName {
			if listed, err = readSums(f); err != nil {
				return nil, err
			}
			continue
		}
		if !wanted[name] {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		out, err := os.OpenFile(filepath.Join(dst, name), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			rc.Close()
			return nil, err
		}
		h := sha256.New()
		_, err = io.Copy(io.MultiWriter(out, h), io.LimitReader(rc, 256<<20))
		rc.Close()
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return nil, err
		}
		sums[name] = hex.EncodeToString(h.Sum(nil))
	}
	if sums[DLLName] == "" {
		return nil, fmt.Errorf("%s does not contain %s", filepath.Base(zipPath), DLLName)
	}
	for name, got := range sums {
		if want, ok := listed[name]; ok && want != got {
			return nil, fmt.Errorf("%s in %s does not match its SHA256SUMS line: got %s, listed %s", name, filepath.Base(zipPath), got, want)
		}
	}
	return sums, nil
}

// readSums parses a sha256sum-style file ("<hex>  <name>" per line).
func readSums(f *zip.File) (map[string]string, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, 1<<20))
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		out[filepath.Base(strings.TrimPrefix(fields[1], "*"))] = strings.ToLower(fields[0])
	}
	return out, nil
}

// Get returns an installed runtime's manifest.
func (s Store) Get(tag string) (Installed, error) {
	var inst Installed
	b, err := os.ReadFile(filepath.Join(s.Dir, tag, manifestName))
	if err != nil {
		return inst, err
	}
	err = json.Unmarshal(b, &inst)
	return inst, err
}

// List returns installed runtimes, newest version first.
func (s Store) List() ([]Installed, error) {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Installed
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if inst, err := s.Get(e.Name()); err == nil {
			out = append(out, inst)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		vi, ei := ParseVersion(out[i].Tag)
		vj, ej := ParseVersion(out[j].Tag)
		if ei != nil || ej != nil {
			return out[i].Tag > out[j].Tag
		}
		return vj.Less(vi)
	})
	return out, nil
}

// Current returns the tag "current" points at ("" if unset).
func (s Store) Current() string { return s.link(CurrentLink) }

// Previous returns the tag that was current before the last Use.
func (s Store) Previous() string { return s.link(PreviousLink) }

func (s Store) link(name string) string {
	t, err := os.Readlink(filepath.Join(s.Dir, name))
	if err != nil {
		return ""
	}
	return filepath.Base(t)
}

// Use points "current" at tag and remembers the old one as "previous".
func (s Store) Use(tag string) error {
	inst, err := s.Get(tag)
	if err != nil {
		return fmt.Errorf("nevr-runtime %s is not installed", tag)
	}
	if inst.Bad != "" {
		return fmt.Errorf("nevr-runtime %s is marked bad: %s (use Unmark to allow it again)", tag, inst.Bad)
	}
	if cur := s.Current(); cur != "" && cur != tag {
		if err := s.setLink(PreviousLink, cur); err != nil {
			return err
		}
	}
	return s.setLink(CurrentLink, tag)
}

// Rollback switches "current" back to "previous".
func (s Store) Rollback() (string, error) {
	prev := s.Previous()
	if prev == "" {
		return "", errors.New("no previous runtime to roll back to")
	}
	cur := s.Current()
	if err := s.setLink(CurrentLink, prev); err != nil {
		return "", err
	}
	if cur != "" {
		if err := s.setLink(PreviousLink, cur); err != nil {
			return "", err
		}
	}
	return prev, nil
}

// MarkBad records that a rollout of tag failed, so updaters skip it.
func (s Store) MarkBad(tag, why string) error {
	inst, err := s.Get(tag)
	if err != nil {
		return err
	}
	inst.Bad = why
	b, _ := json.MarshalIndent(inst, "", "  ")
	return os.WriteFile(filepath.Join(s.Dir, tag, manifestName), b, 0o644)
}

// Unmark clears MarkBad.
func (s Store) Unmark(tag string) error { return s.MarkBad(tag, "") }

// Remove deletes an installed runtime that is neither current nor previous.
func (s Store) Remove(tag string) error {
	if tag == s.Current() || tag == s.Previous() {
		return fmt.Errorf("nevr-runtime %s is in use (current or previous)", tag)
	}
	if _, err := s.Get(tag); err != nil {
		return fmt.Errorf("nevr-runtime %s is not installed", tag)
	}
	return os.RemoveAll(filepath.Join(s.Dir, tag))
}

func (s Store) setLink(name, tag string) error {
	link := filepath.Join(s.Dir, name)
	tmp := link + ".tmp"
	os.Remove(tmp)
	if err := os.Symlink(tag, tmp); err != nil { // relative, so it works inside containers
		return err
	}
	return os.Rename(tmp, link)
}
