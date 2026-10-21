// Package nevr finds, downloads and installs nevr-runtime releases.
//
// Every v5.x release is published as a GitHub pre-release, so "latest" on
// GitHub points at v3.2.0; releases are always listed and picked explicitly.
package nevr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// DefaultRepo is the upstream repository.
const DefaultRepo = "EchoTools/nevr-runtime"

// MinVersion is the oldest release that works as a dedicated server.
var MinVersion = MustParseVersion("v5.4.2")

// Blocked lists releases that must never be installed on servers, with the reason.
var Blocked = map[string]string{
	"v5.3.0": "servers begin multiplayer twice and exit with code 62 (nevr-runtime #719)",
	"v5.4.0": "build failed, no assets",
	"v5.4.1": "servers begin multiplayer twice and exit with code 62 (nevr-runtime #719)",
}

// Release is one nevr-runtime release usable by servers.
type Release struct {
	Tag        string
	Version    Version
	Prerelease bool
	Published  time.Time
	Notes      string
	Asset      Asset // the server zip, nevr-runtime-<tag>.zip
	Blocked    string
}

// Asset is a downloadable release file.
type Asset struct {
	Name   string
	URL    string
	Size   int64
	SHA256 string // from GitHub's asset digest
}

// Client talks to the GitHub releases API.
type Client struct {
	Repo    string
	BaseURL string // default https://api.github.com
	Token   string // optional, raises the rate limit
	HTTP    *http.Client
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 60 * time.Second}
}

type ghRelease struct {
	Tag        string    `json:"tag_name"`
	Draft      bool      `json:"draft"`
	Prerelease bool      `json:"prerelease"`
	Published  time.Time `json:"published_at"`
	Body       string    `json:"body"`
	Assets     []struct {
		Name   string `json:"name"`
		Size   int64  `json:"size"`
		Digest string `json:"digest"`
		URL    string `json:"browser_download_url"`
	} `json:"assets"`
}

// List returns server-capable releases, newest first. Releases older than
// MinVersion are left out; blocked ones are included with Blocked set so a UI
// can explain why they are not offered.
func (c *Client) List(ctx context.Context) ([]Release, error) {
	base := c.BaseURL
	if base == "" {
		base = "https://api.github.com"
	}
	repo := c.Repo
	if repo == "" {
		repo = DefaultRepo
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/repos/%s/releases?per_page=50", base, repo), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub releases: %s", resp.Status)
	}
	var raw []ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	var out []Release
	for _, r := range raw {
		v, err := ParseVersion(r.Tag)
		if err != nil || r.Draft || v.Less(MinVersion) {
			continue
		}
		rel := Release{Tag: r.Tag, Version: v, Prerelease: r.Prerelease, Published: r.Published, Notes: r.Body, Blocked: Blocked[r.Tag]}
		for _, a := range r.Assets {
			if a.Name == "nevr-runtime-"+r.Tag+".zip" {
				rel.Asset = Asset{Name: a.Name, URL: a.URL, Size: a.Size, SHA256: strings.TrimPrefix(a.Digest, "sha256:")}
			}
		}
		if rel.Asset.URL == "" {
			if rel.Blocked == "" {
				rel.Blocked = "no server package in this release"
			}
		} else if rel.Asset.SHA256 == "" && rel.Blocked == "" {
			rel.Blocked = "GitHub reports no checksum for the server package"
		}
		out = append(out, rel)
	}
	sort.Slice(out, func(i, j int) bool { return out[j].Version.Less(out[i].Version) })
	return out, nil
}

// Find returns the release with the given tag.
func (c *Client) Find(ctx context.Context, tag string) (Release, error) {
	all, err := c.List(ctx)
	if err != nil {
		return Release{}, err
	}
	for _, r := range all {
		if r.Tag == tag {
			return r, nil
		}
	}
	return Release{}, fmt.Errorf("nevr-runtime %s not found (or older than %s)", tag, MinVersion)
}

// Newest returns the newest usable release. Pre-releases are included when pre
// is true; while every 5.x release is a pre-release that is the normal case.
func (c *Client) Newest(ctx context.Context, pre bool) (Release, error) {
	all, err := c.List(ctx)
	if err != nil {
		return Release{}, err
	}
	for _, r := range all {
		if r.Blocked == "" && (pre || !r.Prerelease) {
			return r, nil
		}
	}
	return Release{}, fmt.Errorf("no usable nevr-runtime release found")
}

// Version is a parsed vMAJOR.MINOR.PATCH[-pre] tag.
type Version struct {
	Major, Minor, Patch int
	Pre                 string
}

// ParseVersion parses tags like v5.4.2 or v6.0.0-rc.1.
func ParseVersion(tag string) (Version, error) {
	s := strings.TrimPrefix(tag, "v")
	core, pre, _ := strings.Cut(s, "-")
	core, _, _ = strings.Cut(core, "+")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return Version{}, fmt.Errorf("not a version: %q", tag)
	}
	var n [3]int
	for i, p := range parts {
		v, err := strconv.Atoi(p)
		if err != nil {
			return Version{}, fmt.Errorf("not a version: %q", tag)
		}
		n[i] = v
	}
	return Version{n[0], n[1], n[2], pre}, nil
}

// MustParseVersion is ParseVersion for constants.
func MustParseVersion(tag string) Version {
	v, err := ParseVersion(tag)
	if err != nil {
		panic(err)
	}
	return v
}

// Less orders versions; a pre-release sorts before its release.
func (v Version) Less(o Version) bool {
	if v.Major != o.Major {
		return v.Major < o.Major
	}
	if v.Minor != o.Minor {
		return v.Minor < o.Minor
	}
	if v.Patch != o.Patch {
		return v.Patch < o.Patch
	}
	if v.Pre == "" || o.Pre == "" {
		return v.Pre != "" && o.Pre == ""
	}
	return v.Pre < o.Pre
}

func (v Version) String() string {
	s := fmt.Sprintf("v%d.%d.%d", v.Major, v.Minor, v.Patch)
	if v.Pre != "" {
		s += "-" + v.Pre
	}
	return s
}
