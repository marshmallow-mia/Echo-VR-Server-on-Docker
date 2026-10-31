// Package mods gives game servers the mods the Echo VR launcher has: the same
// catalogues on release.echovr.de (mods.json, catalogue.json, rollback.json, dev
// folders), the same checks (a signed index, pinned SHA-256s, releases in
// release-signer's shape), resolved on the host into a lock, downloaded into a
// store, and placed into each server's nEVR root (or plugins folder before nEVR 6)
// by evr-supervisor before every start.
package mods

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// Target is where a mod loads: players' games, game servers, or both.
type Target string

const (
	Client Target = "client"
	Server Target = "server"
	Both   Target = "both"
)

// targetOf reads an entry's target leniently: none, or a word this version
// doesn't know, is both (as every mod loaded before servers were told apart).
func targetOf(s string) Target {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "client":
		return Client
	case "server":
		return Server
	}
	return Both
}

// OnServers reports whether a mod with this target loads on game servers.
func (t Target) OnServers() bool { return targetOf(string(t)) != Client }

// Requires is what a mod version needs.
type Requires struct {
	Nevr string       `json:"nevr,omitempty"`
	Mods []Dependency `json:"mods,omitempty"`
}

// Dependency is a mod that must be on beside another, in a version range.
type Dependency struct {
	ID      string `json:"id"`
	Version string `json:"version,omitempty"`
}

// PackSpec is a content pack: plugins and game data in one zip.
type PackSpec struct {
	URL     string   `json:"url"`
	SHA256  string   `json:"sha256"`
	Size    int64    `json:"size,omitempty"`
	Content string   `json:"content"`
	Plugins []string `json:"plugins"`
}

// Asset is one of a release's three files, as the release workflow built it.
type Asset struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size,omitempty"`
}

// Digest is what release-signer recorded for a file it replaced.
type Digest struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size,omitempty"`
}

// ReleaseSpec is a release in release-signer's shape: a zip, its echo-mod.json
// and a SHA256SUMS, under Base.
type ReleaseSpec struct {
	Base   string `json:"base"`
	Tag    string `json:"tag"`
	Assets struct {
		Package  Asset `json:"package"`
		Manifest Asset `json:"manifest"`
		Sums     Asset `json:"sums"`
	} `json:"assets"`
	Signer struct {
		Status  string  `json:"status"`
		Package *Digest `json:"package,omitempty"`
		Sums    *Digest `json:"sums,omitempty"`
	} `json:"signer"`
}

// Entry is one mod version from a catalogue.
type Entry struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Summary    string         `json:"summary,omitempty"`
	Version    string         `json:"version"`
	File       string         `json:"file,omitempty"`
	URL        string         `json:"url,omitempty"`
	SHA256     string         `json:"sha256,omitempty"`
	Size       int64          `json:"size,omitempty"`
	Args       map[string]any `json:"args,omitempty"`
	Shipped    bool           `json:"shipped,omitempty"`
	Required   bool           `json:"required,omitempty"`
	Prerelease bool           `json:"prerelease,omitempty"`
	Target     Target         `json:"target,omitempty"`
	Requires   Requires       `json:"requires"`
	Conflicts  []string       `json:"conflicts,omitempty"`
	Pack       *PackSpec      `json:"pack,omitempty"`
	Release    *ReleaseSpec   `json:"release,omitempty"`
	// Blocked is set on a catalogue.json version blocked for everyone.
	Blocked *struct {
		Action string `json:"action"`
		Note   string `json:"note"`
	} `json:"blocked,omitempty"`
	// Source is where it came from: "published", "catalogue", or "dev:<code>".
	Source string `json:"source,omitempty"`
}

var (
	idRe     = regexp.MustCompile(`^[a-z0-9_-][a-z0-9._-]{0,63}$`)
	fileRe   = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}\.dll$`)
	shaRe    = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)
	plainRe  = regexp.MustCompile(`^[A-Za-z0-9._+-]{1,128}$`)
	trusted  = map[string]bool{"release.echovr.de": true, "files.echovr.de": true, "evr.echo.taxi": true}
	reserved = map[string]bool{"bugsplat64.dll": true, "nevr-runtime.dll": true, "dbgcore.dll": true}
)

// SafePath reports whether p is a relative path that stays below its base.
func SafePath(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") || strings.Contains(p, "://") {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

// safeURL is a download the launcher would fetch: relative to the mirrors, or
// https on a trusted host.
func safeURL(u string) bool {
	if !strings.Contains(u, "://") {
		return SafePath(u)
	}
	p, err := url.Parse(u)
	return err == nil && p.Scheme == "https" && trusted[p.Hostname()]
}

// releaseBase is where a release may be fetched from: a safe URL, or the
// EchoTools fork's GitHub release of tag.
func releaseBase(base, tag string) bool {
	if safeURL(strings.TrimSuffix(base, "/")) {
		return true
	}
	p, err := url.Parse(base)
	if err != nil || p.Scheme != "https" || p.Host != "github.com" || p.RawQuery != "" || p.Fragment != "" {
		return false
	}
	parts := strings.Split(strings.Trim(p.Path, "/"), "/")
	return len(parts) == 5 && parts[0] == "EchoTools" && plainRe.MatchString(parts[1]) &&
		parts[2] == "releases" && parts[3] == "download" && parts[4] == tag
}

// PluginFile reports whether name can be a plugin's file name.
func PluginFile(name string) bool {
	return fileRe.MatchString(name) && !reserved[strings.ToLower(name)] && !strings.HasPrefix(name, ".")
}

// Validate refuses an entry the launcher would refuse: an unsafe id or file, a
// download that isn't on a trusted host, or one without a checksum.
func (e Entry) Validate() error {
	if !idRe.MatchString(e.ID) || strings.HasPrefix(e.ID, ".") {
		return fmt.Errorf("invalid mod id %q", e.ID)
	}
	if _, ok := parseVersion(e.Version); !ok && !e.Shipped {
		return fmt.Errorf("%s: %q isn't a version", e.ID, e.Version)
	}
	switch {
	case e.Pack != nil:
		if e.Shipped || e.Required || e.File != "" {
			return fmt.Errorf("%s is a pack: no file, never shipped or required", e.ID)
		}
		if !safeURL(e.Pack.URL) || !shaRe.MatchString(e.Pack.SHA256) {
			return fmt.Errorf("%s: untrusted or unchecked pack download", e.ID)
		}
		if !SafePath(e.Pack.Content) || strings.Contains(e.Pack.Content, "/") || len(e.Pack.Plugins) == 0 {
			return fmt.Errorf("%s: invalid pack", e.ID)
		}
		for _, p := range e.Pack.Plugins {
			if !PluginFile(p) {
				return fmt.Errorf("%s: invalid pack plugin %q", e.ID, p)
			}
		}
	case !PluginFile(e.File):
		return fmt.Errorf("%s: invalid plugin file %q", e.ID, e.File)
	case e.Release != nil:
		return e.Release.validate(e.ID, e.Version)
	case !e.Shipped && (!safeURL(e.URL) || !shaRe.MatchString(e.SHA256)):
		return fmt.Errorf("%s: untrusted or unchecked download %q", e.ID, e.URL)
	}
	return nil
}

func (r ReleaseSpec) validate(id, ver string) error {
	want := []struct {
		a    Asset
		name string
	}{
		{r.Assets.Package, fmt.Sprintf("%s-v%s-windows.zip", id, ver)},
		{r.Assets.Manifest, "echo-mod.json"},
		{r.Assets.Sums, "SHA256SUMS"},
	}
	for _, w := range want {
		if w.a.Name != w.name {
			return fmt.Errorf("%s's release names %q, the spec's is %q", id, w.a.Name, w.name)
		}
		if !shaRe.MatchString(w.a.SHA256) {
			return fmt.Errorf("%s's release has no valid sha256 for %s", id, w.name)
		}
	}
	if r.Tag != "v"+ver {
		return fmt.Errorf("%s's release tag %q isn't v%s", id, r.Tag, ver)
	}
	if !releaseBase(r.Base, r.Tag) {
		return fmt.Errorf("untrusted download for %s: %s", id, r.Base)
	}
	_, err := r.pins()
	return err
}

// pins is what the release's zip, manifest and sums must hash to: a signed (or
// revoked) release's zip and sums are the signer's, anything else what the
// release workflow attested.
func (r ReleaseSpec) pins() ([3]string, error) {
	pkg, sums := r.Assets.Package.SHA256, r.Assets.Sums.SHA256
	if r.Signer.Status == "signed" || r.Signer.Status == "revoked" {
		if r.Signer.Package == nil || r.Signer.Sums == nil {
			return [3]string{}, fmt.Errorf("a signed release without the signer's digests")
		}
		pkg, sums = r.Signer.Package.SHA256, r.Signer.Sums.SHA256
	}
	if !shaRe.MatchString(pkg) || !shaRe.MatchString(sums) {
		return [3]string{}, fmt.Errorf("the release's digests aren't sha256")
	}
	return [3]string{strings.ToLower(pkg), strings.ToLower(r.Assets.Manifest.SHA256), strings.ToLower(sums)}, nil
}

// installable refuses a release whose signature was revoked, or whose signer
// status this version doesn't know.
func (r ReleaseSpec) installable() error {
	switch r.Signer.Status {
	case "", "unsigned", "pending", "approved", "signed":
		return nil
	case "revoked":
		return fmt.Errorf("its signature was revoked")
	}
	return fmt.Errorf("its signer status %q isn't one this tool knows", r.Signer.Status)
}

// Files are the plugin files the entry puts into the plugins folder.
func (e Entry) Files() []string {
	if e.Pack != nil {
		return append([]string{OverlayPlugin}, e.Pack.Plugins...)
	}
	return []string{e.File}
}

// OverlayPlugin serves a content pack's game data; it loads early, with the pack.
const OverlayPlugin = "NvrContentOverlay.dll"

// Block is a rollback.json block: versions of a mod nobody should run.
type Block struct {
	Part     string `json:"part"`
	ID       string `json:"id"`
	Versions string `json:"versions"`
	SHA      string `json:"sha"`
	Action   string `json:"action"`
	Note     string `json:"note"`
}

// Rollback is rollback.json: what was pulled and what is blocked.
type Rollback struct {
	Pulled []struct {
		Part    string `json:"part"`
		Version string `json:"version"`
		ID      string `json:"id"`
		Note    string `json:"note"`
	} `json:"pulled"`
	Blocked []Block `json:"blocked"`
}

// BlockFor returns why entry e may not run, if a block covers it ("" if none).
// A pulled mod version counts as blocked too: a server has no one to ask.
func (r Rollback) BlockFor(e Entry) string {
	shas := []string{e.SHA256}
	if e.Pack != nil {
		shas = append(shas, e.Pack.SHA256)
	}
	if e.Release != nil {
		shas = append(shas, e.Release.Assets.Package.SHA256)
		if e.Release.Signer.Package != nil {
			shas = append(shas, e.Release.Signer.Package.SHA256)
		}
	}
	for _, b := range r.Blocked {
		if b.Part != "mod" || b.ID == "" || !(b.ID == e.ID || strings.EqualFold(b.ID, e.File)) {
			continue
		}
		hit := false
		switch v := strings.TrimSpace(b.Versions); {
		case v == "" && b.SHA != "":
			for _, s := range shas {
				hit = hit || (s != "" && strings.EqualFold(s, b.SHA))
			}
		case v == "":
		case v == "all" || v == "*":
			hit = true
		default:
			hit = InRange(e.Version, v, true)
		}
		if hit {
			return noteOr(b.Note)
		}
	}
	for _, p := range r.Pulled {
		if p.Part == "mod" && (p.ID == e.ID || strings.EqualFold(p.ID, e.File)) && p.Version == e.Version {
			return noteOr(p.Note)
		}
	}
	return ""
}

func noteOr(n string) string {
	if n == "" {
		return "a serious problem"
	}
	return n
}

// Catalog is every mod version a server can choose from, by id.
type Catalog struct {
	Versions map[string][]Entry
	Rollback Rollback
}

// parseMods reads a mods.json: each entry that doesn't pass Validate is left
// out (the rest still count). source is noted on each.
func parseMods(text []byte, source string) ([]Entry, []string, error) {
	var raw struct {
		Mods []json.RawMessage `json:"mods"`
	}
	if err := json.Unmarshal(text, &raw); err != nil {
		return nil, nil, err
	}
	var out []Entry
	var skipped []string
	for i, m := range raw.Mods {
		var e Entry
		if err := json.Unmarshal(m, &e); err != nil {
			skipped = append(skipped, fmt.Sprintf("entry %d: %v", i, err))
			continue
		}
		e.Target = targetOf(string(e.Target))
		e.Source = source
		if err := e.Validate(); err != nil {
			skipped = append(skipped, err.Error())
			continue
		}
		out = append(out, e)
	}
	return out, skipped, nil
}

// parseCatalogue reads catalogue.json: every version of every third-party mod.
func parseCatalogue(text []byte) ([]Entry, []string, error) {
	var raw struct {
		Mods []struct {
			ID       string            `json:"id"`
			Versions []json.RawMessage `json:"versions"`
		} `json:"mods"`
	}
	if err := json.Unmarshal(text, &raw); err != nil {
		return nil, nil, err
	}
	var out []Entry
	var skipped []string
	for _, m := range raw.Mods {
		for _, v := range m.Versions {
			var e Entry
			if err := json.Unmarshal(v, &e); err != nil {
				skipped = append(skipped, fmt.Sprintf("%s: %v", m.ID, err))
				continue
			}
			e.Target = targetOf(string(e.Target))
			e.Source = "catalogue"
			if e.ID != m.ID {
				skipped = append(skipped, fmt.Sprintf("%s: a version of another id (%s)", m.ID, e.ID))
				continue
			}
			if err := e.Validate(); err != nil {
				skipped = append(skipped, err.Error())
				continue
			}
			out = append(out, e)
		}
	}
	return out, skipped, nil
}

// add puts entries into c. Dev folders' entries go over published ones with
// the same id or file, as in the launcher.
func (c *Catalog) add(entries []Entry, over bool) {
	if c.Versions == nil {
		c.Versions = map[string][]Entry{}
	}
	for _, e := range entries {
		if over {
			for id, vs := range c.Versions {
				if id == e.ID || sharesFile(vs, e) {
					if !strings.HasPrefix(vs[0].Source, "dev:") {
						delete(c.Versions, id)
					}
				}
			}
		} else if _, taken := c.Versions[e.ID]; !taken && c.fileTaken(e) {
			continue // a third-party mod never takes over the launcher's own files
		}
		c.Versions[e.ID] = append(c.Versions[e.ID], e)
	}
}

func sharesFile(vs []Entry, e Entry) bool {
	for _, v := range vs {
		for _, f := range v.Files() {
			for _, g := range e.Files() {
				if strings.EqualFold(f, g) && f != OverlayPlugin {
					return true
				}
			}
		}
	}
	return false
}

func (c *Catalog) fileTaken(e Entry) bool {
	for _, vs := range c.Versions {
		if sharesFile(vs, e) {
			return true
		}
	}
	return false
}

// Choose returns the version of mod id to run: pin when set (a version, or a
// range), else the newest that no block covers, prereleases only when asked.
func (c *Catalog) Choose(id, pin string, prereleases bool) (Entry, error) {
	vs := c.Versions[id]
	if len(vs) == 0 {
		return Entry{}, fmt.Errorf("no mod %q in the catalogues", id)
	}
	var best *Entry
	var why string
	for i := range vs {
		e := vs[i]
		if e.Blocked != nil {
			why = "blocked: " + noteOr(e.Blocked.Note)
			continue
		}
		if b := c.Rollback.BlockFor(e); b != "" {
			why = "blocked: " + b
			continue
		}
		pv, ok := parseVersion(e.Version)
		pre := e.Prerelease || (ok && pv.prerelease())
		switch {
		case pin != "":
			if !InRange(e.Version, pin, true) {
				continue
			}
		case pre && !prereleases:
			continue
		}
		if best == nil || newer(e.Version, best.Version) || (!ok && e.Version != best.Version) {
			best = &vs[i]
		}
	}
	if best == nil {
		if pin != "" {
			return Entry{}, fmt.Errorf("%s: no version %s to run%s", id, pin, because(why))
		}
		return Entry{}, fmt.Errorf("%s: nothing to run%s", id, because(why))
	}
	return *best, nil
}

func because(why string) string {
	if why == "" {
		return ""
	}
	return " (" + why + ")"
}
