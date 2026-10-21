// Package ghrel lists GitHub releases and downloads their assets with
// checksum verification. The tools use it to update themselves, the panels
// and Wings.
package ghrel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/nevr"
)

// Release is one published release.
type Release struct {
	Tag        string
	Version    nevr.Version
	Prerelease bool
	Published  time.Time
	Notes      string
	Assets     map[string]Asset
}

// Asset is a downloadable file of a release.
type Asset struct {
	URL    string
	Size   int64
	SHA256 string // GitHub's digest, when it has one
}

// Client talks to the GitHub API.
type Client struct {
	BaseURL string // default https://api.github.com
	Token   string
	HTTP    *http.Client
}

func (c *Client) http() *http.Client {
	if c != nil && c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 5 * time.Minute}
}

// List returns the repo's releases with a semver tag, newest first. Drafts
// are left out.
func (c *Client) List(ctx context.Context, repo string) ([]Release, error) {
	base := "https://api.github.com"
	if c != nil && c.BaseURL != "" {
		base = c.BaseURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/repos/%s/releases?per_page=50", base, repo), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if c != nil && c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub releases of %s: %s", repo, resp.Status)
	}
	var raw []struct {
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
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	var out []Release
	for _, r := range raw {
		v, err := nevr.ParseVersion(r.Tag)
		if err != nil || r.Draft {
			continue
		}
		rel := Release{Tag: r.Tag, Version: v, Prerelease: r.Prerelease, Published: r.Published, Notes: r.Body, Assets: map[string]Asset{}}
		for _, a := range r.Assets {
			rel.Assets[a.Name] = Asset{URL: a.URL, Size: a.Size, SHA256: strings.TrimPrefix(a.Digest, "sha256:")}
		}
		out = append(out, rel)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[j].Version.Less(out[i].Version) })
	return out, nil
}

// Fetch downloads url into w and returns the content's sha256.
func (c *Client) Fetch(ctx context.Context, url string, w io.Writer) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("downloading %s: %s", url, resp.Status)
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(w, h), resp.Body); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Checksum returns the sha256 of name in a release's checksum file
// ("<sha256>  <name>" lines), or "" when the release has none.
func (c *Client) Checksum(ctx context.Context, rel Release, file, name string) (string, error) {
	a, ok := rel.Assets[file]
	if !ok {
		return "", nil
	}
	var b strings.Builder
	if _, err := c.Fetch(ctx, a.URL, &b); err != nil {
		return "", err
	}
	for _, line := range strings.Split(b.String(), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name {
			return strings.ToLower(f[0]), nil
		}
	}
	return "", fmt.Errorf("%s has no checksum for %s", file, name)
}
