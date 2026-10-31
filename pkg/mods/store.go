package mods

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Store keeps downloaded mods: <Dir>/<id>/<version>/ with the plugin files in
// plugins/, a content pack's data in content/<content>/, and manifest.json with
// every file's SHA-256 (checked again when a server's files are placed).
type Store struct {
	Dir  string
	HTTP *http.Client
	// Mirrors resolve a relative download; they default to the launcher's.
	Mirrors []string
}

// Mirrors are where a relative mod download is fetched from, in order.
var Mirrors = []string{"https://release.echovr.de/", "https://evr.echo.taxi/"}

const manifestName = "manifest.json"

// maxEntry is release-signer's limit for one file; packs may hold bigger data.
const (
	maxEntry     = 16 << 20
	maxPackEntry = 512 << 20
)

// Installed is a stored mod version.
type Installed struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Target  Target `json:"target"`
	// Plugins are the files for the plugins folder, in load order.
	Plugins []string `json:"plugins"`
	// Content is a pack's content folder name ("" for a plain mod).
	Content string `json:"content,omitempty"`
	// Files maps each stored file (path under the version's folder, "/") to its SHA-256.
	Files       map[string]string `json:"files"`
	Source      string            `json:"source"`
	InstalledAt time.Time         `json:"installed_at"`
	// Bad is set when a rollout of this version failed.
	Bad string `json:"bad,omitempty"`
}

func (s Store) dir(id, ver string) string { return filepath.Join(s.Dir, id, ver) }

// Get returns a stored version's manifest.
func (s Store) Get(id, ver string) (Installed, error) {
	var in Installed
	b, err := os.ReadFile(filepath.Join(s.dir(id, ver), manifestName))
	if err != nil {
		return in, err
	}
	return in, json.Unmarshal(b, &in)
}

// List returns every stored version, by id then newest first.
func (s Store) List() ([]Installed, error) {
	ids, err := os.ReadDir(s.Dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Installed
	for _, id := range ids {
		if !id.IsDir() || strings.HasPrefix(id.Name(), ".") {
			continue
		}
		vers, _ := os.ReadDir(filepath.Join(s.Dir, id.Name()))
		for _, v := range vers {
			if in, err := s.Get(id.Name(), v.Name()); err == nil {
				out = append(out, in)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].ID != out[j].ID {
			return out[i].ID < out[j].ID
		}
		return newer(out[i].Version, out[j].Version)
	})
	return out, nil
}

// MarkBad records that a rollout of id's version failed, so updates skip it.
func (s Store) MarkBad(id, ver, why string) error {
	in, err := s.Get(id, ver)
	if err != nil {
		return err
	}
	in.Bad = why
	b, _ := json.MarshalIndent(in, "", "  ")
	return os.WriteFile(filepath.Join(s.dir(id, ver), manifestName), b, 0o644)
}

// Unmark clears MarkBad.
func (s Store) Unmark(id, ver string) error { return s.MarkBad(id, ver, "") }

// Remove deletes a stored version.
func (s Store) Remove(id, ver string) error {
	if _, err := s.Get(id, ver); err != nil {
		return fmt.Errorf("%s %s isn't stored", id, ver)
	}
	err := os.RemoveAll(s.dir(id, ver))
	os.Remove(filepath.Join(s.Dir, id)) // only when empty
	return err
}

// Install downloads entry e, checks everything the launcher checks, and stores
// it. It is a no-op when that version is stored with the same files. A shipped
// mod (it comes with the game's files) can't be downloaded: shipped is where it is.
func (s Store) Install(ctx context.Context, e Entry) (Installed, error) {
	if e.Shipped {
		return Installed{}, fmt.Errorf("%s comes with the game's files: it isn't downloaded", e.ID)
	}
	if err := e.Validate(); err != nil && !strings.HasPrefix(e.Source, "dev:") {
		return Installed{}, err
	}
	if in, err := s.Get(e.ID, e.Version); err == nil && in.Bad == "" && s.intact(in) {
		return in, nil
	}
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return Installed{}, err
	}
	stage, err := os.MkdirTemp(s.Dir, ".stage-"+e.ID+"-")
	if err != nil {
		return Installed{}, err
	}
	defer os.RemoveAll(stage)
	in := Installed{ID: e.ID, Version: e.Version, Target: targetOf(string(e.Target)), Source: e.Source, Files: map[string]string{}}
	switch {
	case e.Pack != nil:
		err = s.installPack(ctx, e, stage, &in)
	case e.Release != nil:
		err = s.installRelease(ctx, e, stage, &in)
	default:
		var b []byte
		if b, err = s.fetch(ctx, e.URL, e.SHA256, maxEntry); err == nil {
			err = put(stage, "plugins/"+e.File, b, &in)
			in.Plugins = []string{e.File}
		}
	}
	if err != nil {
		return Installed{}, fmt.Errorf("%s %s: %w", e.ID, e.Version, err)
	}
	in.InstalledAt = time.Now().UTC()
	b, _ := json.MarshalIndent(in, "", "  ")
	if err := os.WriteFile(filepath.Join(stage, manifestName), b, 0o644); err != nil {
		return Installed{}, err
	}
	if err := filepath.Walk(stage, func(p string, fi os.FileInfo, err error) error {
		if err == nil && fi.IsDir() {
			return os.Chmod(p, 0o755)
		}
		return err
	}); err != nil {
		return Installed{}, err
	}
	final := s.dir(e.ID, e.Version)
	if err := os.MkdirAll(filepath.Dir(final), 0o755); err != nil {
		return Installed{}, err
	}
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
	return in, nil
}

// intact reports whether every file of a stored version still has its checksum.
func (s Store) intact(in Installed) bool {
	for rel, want := range in.Files {
		got, err := fileSHA(filepath.Join(s.dir(in.ID, in.Version), filepath.FromSlash(rel)))
		if err != nil || got != want {
			return false
		}
	}
	return len(in.Files) > 0
}

// Path is where a stored file is (rel as in Installed.Files).
func (s Store) Path(in Installed, rel string) string {
	return filepath.Join(s.dir(in.ID, in.Version), filepath.FromSlash(rel))
}

func put(stage, rel string, b []byte, in *Installed) error {
	p := filepath.Join(stage, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(p, b, 0o644); err != nil {
		return err
	}
	in.Files[rel] = sha256Hex(b)
	return nil
}

// urlOf resolves a catalogue download: absolute as it is, relative on each mirror.
func (s Store) urls(u string) []string {
	if strings.Contains(u, "://") {
		return []string{u}
	}
	ms := s.Mirrors
	if ms == nil {
		ms = Mirrors
	}
	var out []string
	for _, m := range ms {
		out = append(out, strings.TrimSuffix(m, "/")+"/"+u)
	}
	return out
}

// fetch downloads u (from the first mirror that has it) and checks it against want.
func (s Store) fetch(ctx context.Context, u, want string, limit int64) ([]byte, error) {
	hc := s.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Minute}
	}
	var last error
	for _, full := range s.urls(u) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, full, nil)
		if err != nil {
			return nil, err
		}
		resp, err := hc.Do(req)
		if err != nil {
			last = err
			continue
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
		resp.Body.Close()
		switch {
		case resp.StatusCode != http.StatusOK:
			last = fmt.Errorf("download %s: %s", full, resp.Status)
			continue
		case err != nil:
			last = err
			continue
		case int64(len(b)) > limit:
			return nil, fmt.Errorf("%s is bigger than %d MiB", full, limit>>20)
		}
		if got := sha256Hex(b); !strings.EqualFold(got, want) {
			return nil, fmt.Errorf("%s doesn't match its checksum (got %s, the catalogue says %s)", path.Base(full), got, want)
		}
		return b, nil
	}
	return nil, last
}

// installRelease fetches a release's three files, checks them against the pins
// and the top SHA256SUMS, the manifest against the entry, and the zip's entries
// against its own SHA256SUMS.
func (s Store) installRelease(ctx context.Context, e Entry, stage string, in *Installed) error {
	r := e.Release
	if err := r.installable(); err != nil {
		return err
	}
	pins, err := r.pins()
	if err != nil {
		return err
	}
	base := strings.TrimSuffix(r.Base, "/") + "/"
	zipB, err := s.fetch(ctx, base+r.Assets.Package.Name, pins[0], maxEntry*4)
	if err != nil {
		return err
	}
	manB, err := s.fetch(ctx, base+r.Assets.Manifest.Name, pins[1], 1<<20)
	if err != nil {
		return err
	}
	sumsB, err := s.fetch(ctx, base+r.Assets.Sums.Name, pins[2], 1<<20)
	if err != nil {
		return err
	}
	top, err := parseSums(sumsB)
	if err != nil {
		return err
	}
	for name, b := range map[string][]byte{r.Assets.Package.Name: zipB, r.Assets.Manifest.Name: manB} {
		if want, ok := top[name]; !ok || want != sha256Hex(b) {
			return fmt.Errorf("%s doesn't match SHA256SUMS", name)
		}
	}
	var m struct{ ID, Version, File, Package string }
	if err := json.Unmarshal(manB, &m); err != nil {
		return fmt.Errorf("echo-mod.json isn't valid: %w", err)
	}
	for _, c := range [][3]string{{"id", m.ID, e.ID}, {"version", m.Version, e.Version}, {"file", m.File, e.File}, {"package", m.Package, r.Assets.Package.Name}} {
		if c[1] != c[2] {
			return fmt.Errorf("echo-mod.json's %s is %q, the catalogue's is %q", c[0], c[1], c[2])
		}
	}
	zr, err := zip.NewReader(bytes.NewReader(zipB), int64(len(zipB)))
	if err != nil {
		return fmt.Errorf("the release's zip isn't a zip: %w", err)
	}
	stem := strings.TrimSuffix(e.File, ".dll")
	allowed := map[string]bool{e.File: true, "LICENSE": true, "NOTICE": true, "SHA256SUMS": true, stem + ".plugin.json": true}
	entries := map[string][]byte{}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || !f.Mode().IsRegular() || !plainRe.MatchString(f.Name) || strings.HasPrefix(f.Name, ".") {
			return fmt.Errorf("the zip has an entry that isn't a plain file: %q", f.Name)
		}
		if !allowed[f.Name] {
			return fmt.Errorf("the zip has a file it shouldn't: %s", f.Name)
		}
		for k := range entries {
			if strings.EqualFold(k, f.Name) {
				return fmt.Errorf("the zip has %s twice", f.Name)
			}
		}
		b, err := readEntry(f, maxEntry)
		if err != nil {
			return err
		}
		entries[f.Name] = b
	}
	for _, need := range []string{e.File, "LICENSE", "SHA256SUMS"} {
		if _, ok := entries[need]; !ok {
			return fmt.Errorf("the zip lacks %s", need)
		}
	}
	inner, err := parseSums(entries["SHA256SUMS"])
	if err != nil {
		return err
	}
	for name, b := range entries {
		if name == "SHA256SUMS" {
			continue
		}
		if want, ok := inner[name]; !ok || want != sha256Hex(b) {
			return fmt.Errorf("%s doesn't match the zip's SHA256SUMS", name)
		}
	}
	for name := range inner {
		if _, ok := entries[name]; !ok {
			return fmt.Errorf("the zip's SHA256SUMS lists %s, which the zip lacks", name)
		}
	}
	in.Plugins = []string{e.File}
	if err := put(stage, "plugins/"+e.File, entries[e.File], in); err != nil {
		return err
	}
	if b, ok := entries[stem+".plugin.json"]; ok {
		return put(stage, "plugins/"+stem+".plugin.json", b, in)
	}
	return nil
}

// installPack fetches a content pack's zip and keeps its plugins (and their
// settings files) and its content folder; anything else refuses the pack. The
// overlay plugin comes from the catalogue as a mod of its own (content-overlay).
func (s Store) installPack(ctx context.Context, e Entry, stage string, in *Installed) error {
	p := e.Pack
	b, err := s.fetch(ctx, p.URL, p.SHA256, 2<<30)
	if err != nil {
		return err
	}
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return fmt.Errorf("the pack isn't a zip: %w", err)
	}
	root := "content/" + p.Content + "/"
	found := map[string]bool{}
	for _, f := range zr.File {
		name := strings.TrimPrefix(f.Name, "./")
		if f.FileInfo().IsDir() {
			continue
		}
		if !SafePath(name) {
			return fmt.Errorf("the pack has an unsafe path: %s", f.Name)
		}
		parts := strings.Split(name, "/")
		var rel string
		switch {
		case len(parts) == 1:
			continue
		case len(parts) == 2 && parts[0] == "plugins":
			listed := ""
			for _, q := range p.Plugins {
				if strings.EqualFold(q, parts[1]) {
					listed = q
				}
			}
			switch {
			case listed != "":
				found[listed] = true
			case settingsFile(parts[1]):
			default:
				return fmt.Errorf("the pack has a file in plugins/ it doesn't list: %s", parts[1])
			}
			rel = name
		case strings.HasPrefix(name, root):
			rel = name
		default:
			return fmt.Errorf("the pack has a file outside plugins/ and %s: %s", root, name)
		}
		data, err := readEntry(f, maxPackEntry)
		if err != nil {
			return err
		}
		if err := put(stage, rel, data, in); err != nil {
			return err
		}
	}
	for _, q := range p.Plugins {
		if !found[q] {
			return fmt.Errorf("the pack lacks its plugin %s", q)
		}
	}
	if _, ok := in.Files[root+"content.json"]; !ok {
		return fmt.Errorf("the pack has no %scontent.json", root)
	}
	in.Plugins = append([]string(nil), p.Plugins...)
	in.Content = p.Content
	return nil
}

func settingsFile(name string) bool {
	l := strings.ToLower(name)
	for _, ext := range []string{".ini", ".cfg", ".txt", ".json"} {
		if strings.HasSuffix(l, ext) && len(l) > len(ext) && len(name) <= 64 && !strings.HasPrefix(name, ".") &&
			plainRe.MatchString(name) && !strings.Contains(name, "+") {
			return true
		}
	}
	return false
}

func readEntry(f *zip.File, limit int64) ([]byte, error) {
	if f.UncompressedSize64 > uint64(limit) {
		return nil, fmt.Errorf("%s is bigger than %d MiB", f.Name, limit>>20)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s is bigger than %d MiB", f.Name, limit>>20)
	}
	return b, nil
}

// parseSums reads a SHA256SUMS ("<hex>  <name>" per line); a line that doesn't
// fit, a name that isn't plain, or a name twice refuses it.
func parseSums(b []byte) (map[string]string, error) {
	out := map[string]string{}
	for i, line := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		hash, rest, ok := strings.Cut(line, " ")
		name := strings.TrimLeft(rest, " *")
		if !ok || len(rest)-len(name) != 1 || !shaRe.MatchString(hash) || !plainRe.MatchString(name) || strings.HasPrefix(name, ".") {
			return nil, fmt.Errorf("SHA256SUMS line %d isn't `<sha256>  <name>`", i+1)
		}
		for k := range out {
			if strings.EqualFold(k, name) {
				return nil, fmt.Errorf("SHA256SUMS lists %s twice", name)
			}
		}
		out[name] = strings.ToLower(hash)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("SHA256SUMS lists nothing")
	}
	return out, nil
}

func fileSHA(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
