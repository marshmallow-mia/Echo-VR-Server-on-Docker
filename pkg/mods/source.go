package mods

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// Origin is release.echovr.de, where the launcher's catalogues are published.
const Origin = "https://release.echovr.de/"

// Source reads the catalogues the launcher reads, from release.echovr.de over
// TLS (the launcher's source of truth), each checked against the signed index.
type Source struct {
	// Origin defaults to Origin (tests point it at a local server).
	Origin string
	HTTP   *http.Client
	// Keys default to IndexKeys.
	Keys [][]byte
	// DevCodes are launcher dev codes whose folders' mods go over published ones.
	DevCodes []string
}

var codeRe = regexp.MustCompile(`^[a-z2-7]{26}$`)

// NormalizeCode returns a dev code as the launcher keeps it (no dashes or
// spaces, lowercase), or "" when it isn't one.
func NormalizeCode(in string) string {
	c := strings.ToLower(strings.NewReplacer("-", "", " ", "").Replace(strings.TrimSpace(in)))
	if !codeRe.MatchString(c) {
		return ""
	}
	return c
}

type index struct {
	Serial uint64            `json:"serial"`
	Files  map[string]string `json:"files"`
}

func (s Source) origin() string {
	if s.Origin != "" {
		return strings.TrimSuffix(s.Origin, "/") + "/"
	}
	return Origin
}

func (s Source) keys() [][]byte {
	if s.Keys != nil {
		return s.Keys
	}
	return IndexKeys
}

func (s Source) client() *http.Client {
	if s.HTTP != nil {
		return s.HTTP
	}
	return &http.Client{Timeout: time.Minute}
}

var errNotFound = errors.New("not published")

func (s Source) get(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.origin()+path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, errNotFound
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, fmt.Errorf("%s needs an EchoVRCE sign-in (%s)", path, resp.Status)
	default:
		return nil, fmt.Errorf("%s: %s", path, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 32<<20))
}

// index reads dir's index.json (dir "" or "launcher/dev/<code>/") and refuses it
// without a valid signature in index.json.sig.
func (s Source) index(ctx context.Context, dir string) (index, error) {
	text, err := s.get(ctx, dir+"index.json")
	if err != nil {
		return index{}, err
	}
	sig, err := s.get(ctx, dir+"index.json.sig")
	if err != nil {
		return index{}, fmt.Errorf("%sindex.json.sig: %w", dir, err)
	}
	if !signed(text, sig, s.keys()) {
		return index{}, fmt.Errorf("%sindex.json has no valid signature: refused", dir)
	}
	var idx index
	if err := json.Unmarshal(text, &idx); err != nil {
		return index{}, fmt.Errorf("%sindex.json: %w", dir, err)
	}
	return idx, nil
}

// checked reads path, which the signed index idx (of dir) must list, and checks
// it against the listed checksum.
func (s Source) checked(ctx context.Context, idx index, dir, path string) ([]byte, error) {
	want, ok := idx.Files[path]
	if !ok {
		return nil, fmt.Errorf("%s%s: %w (the signed index doesn't list it)", dir, path, errNotFound)
	}
	b, err := s.get(ctx, dir+path)
	if err != nil {
		return nil, err
	}
	if got := sha256Hex(b); !strings.EqualFold(got, want) {
		return nil, fmt.Errorf("%s%s doesn't match the signed index (got %s, listed %s): try again in a minute", dir, path, got, want)
	}
	return b, nil
}

// Fetched is what Fetch read: the catalogue, and what it left out and why.
type Fetched struct {
	Catalog *Catalog
	Skipped []string
}

// Fetch reads mods.json, catalogue.json and rollback.json, then each dev code's
// mods.json, into one catalogue.
func (s Source) Fetch(ctx context.Context) (Fetched, error) {
	idx, err := s.index(ctx, "")
	if err != nil {
		return Fetched{}, err
	}
	c := &Catalog{Versions: map[string][]Entry{}}
	var out Fetched
	out.Catalog = c
	text, err := s.checked(ctx, idx, "", "launcher/mods.json")
	if err != nil {
		return Fetched{}, err
	}
	own, skipped, err := parseMods(text, "published")
	if err != nil {
		return Fetched{}, fmt.Errorf("launcher/mods.json: %w", err)
	}
	out.Skipped = append(out.Skipped, skipped...)
	c.add(own, false)
	if text, err = s.checked(ctx, idx, "", "launcher/catalogue.json"); err == nil {
		community, skipped, err := parseCatalogue(text)
		if err != nil {
			return Fetched{}, fmt.Errorf("launcher/catalogue.json: %w", err)
		}
		out.Skipped = append(out.Skipped, skipped...)
		c.add(community, false)
	} else if !errors.Is(err, errNotFound) {
		return Fetched{}, err
	}
	if text, err = s.checked(ctx, idx, "", "launcher/rollback.json"); err == nil {
		if err := json.Unmarshal(text, &c.Rollback); err != nil {
			return Fetched{}, fmt.Errorf("launcher/rollback.json: %w", err)
		}
	} else if !errors.Is(err, errNotFound) {
		return Fetched{}, err
	}
	for _, raw := range s.DevCodes {
		code := NormalizeCode(raw)
		if code == "" {
			return Fetched{}, fmt.Errorf("%q isn't a dev code", raw)
		}
		entries, skipped, err := s.dev(ctx, code)
		if err != nil {
			return Fetched{}, fmt.Errorf("dev code %s…: %w", code[:4], err)
		}
		out.Skipped = append(out.Skipped, skipped...)
		c.add(entries, true)
	}
	return out, nil
}

// dev reads a dev code's folder: its signed index and mods.json, every download
// kept inside the folder.
func (s Source) dev(ctx context.Context, code string) ([]Entry, []string, error) {
	dir := "launcher/dev/" + code + "/"
	idx, err := s.index(ctx, dir)
	if errors.Is(err, errNotFound) {
		return nil, nil, errors.New("no such dev folder (a gated one, behind the EchoVRCE sign-in, works only in the launcher)")
	}
	if err != nil {
		return nil, nil, err
	}
	text, err := s.checked(ctx, idx, dir, "mods.json")
	if err != nil {
		return nil, nil, err
	}
	base := s.origin() + dir
	inside := func(u string) (string, bool) {
		full := u
		if !strings.Contains(u, "://") {
			if !SafePath(u) {
				return "", false
			}
			full = base + u
		}
		rest, ok := strings.CutPrefix(full, base)
		return full, ok && SafePath(rest) && !strings.ContainsAny(rest, "?#")
	}
	source := "dev:" + sha256Hex([]byte(code))[:12]
	var raw struct {
		Mods []json.RawMessage `json:"mods"`
	}
	if err := json.Unmarshal(text, &raw); err != nil {
		return nil, nil, err
	}
	var out []Entry
	var skipped []string
	seen := map[string]bool{}
	for i, m := range raw.Mods {
		var e Entry
		if err := json.Unmarshal(m, &e); err != nil {
			skipped = append(skipped, fmt.Sprintf("dev entry %d: %v", i, err))
			continue
		}
		var ok bool
		switch {
		case e.Pack != nil:
			e.Pack.URL, ok = inside(e.Pack.URL)
		case e.Release != nil:
			e.Release.Base, ok = inside(e.Release.Base)
		default:
			e.URL, ok = inside(e.URL)
		}
		if !ok {
			skipped = append(skipped, fmt.Sprintf("%s doesn't download from its dev folder", e.ID))
			continue
		}
		e.Shipped = false
		e.Target = targetOf(string(e.Target))
		e.Source = source
		// Its files are on release.echovr.de, as the folder is.
		if err := e.validateDev(base); err != nil {
			skipped = append(skipped, err.Error())
			continue
		}
		if seen[e.ID] {
			skipped = append(skipped, "duplicate dev id "+e.ID)
			continue
		}
		seen[e.ID] = true
		out = append(out, e)
	}
	return out, skipped, nil
}

// validateDev is Validate for an entry whose downloads were resolved into its
// dev folder at base (absolute URLs there, which a test server may serve): each
// is checked as the path inside the folder.
func (e Entry) validateDev(base string) error {
	rel := func(u string) string {
		r := strings.Trim(strings.TrimPrefix(u, base), "/")
		if r == "" {
			return "dev"
		}
		return "dev/" + r
	}
	c := e
	switch {
	case c.Pack != nil:
		p := *c.Pack
		p.URL = rel(p.URL)
		c.Pack = &p
	case c.Release != nil:
		r := *c.Release
		r.Base = rel(r.Base)
		c.Release = &r
	default:
		c.URL = rel(c.URL)
	}
	return c.Validate()
}
