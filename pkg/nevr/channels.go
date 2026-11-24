package nevr

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
)

// ManifestURL is nEVR's release manifest. Its "channels" map each channel
// (stable, beta, ...) to the release a runtime on that channel updates itself
// to (nevr-runtime ADR 0024). The manifest only picks the tag: the release
// itself still comes from GitHub and is checked against GitHub's sha256.
const ManifestURL = "https://evr.echo.taxi/nevr-runtime/manifest.json"

// DefaultChannel is nEVR's own default: a runtime with no update.channel
// follows stable.
const DefaultChannel = "stable"

// UpdateMin is the first release that updates itself at launch to its
// channel's release, in either direction, unless it runs with -no-update.
// Older runtimes refuse the flag, so it is never given to them.
var UpdateMin = MustParseVersion("v6.17.0")

// UpdateConfigMin is the first release whose config.yaml takes an update
// section (update.enabled, update.channel). 6.17.0 reads those keys, but its
// config check refuses the section ("unknown_top_level_key", a fatal error
// for a server), so 6.17.x only takes -no-update and follows a channel of
// its own choosing. Letting nEVR update itself needs this release.
var UpdateConfigMin = MustParseVersion("v6.18.0")

// Updater says who keeps a server's nevr-runtime current.
const (
	// UpdaterNevr: the runtime updates itself to its channel's release; the
	// host tool keeps its store (and the host BugSplat64.dll) on that release
	// and never moves a runtime the runtime put in place backwards.
	UpdaterNevr = "nevr"
	// UpdaterHost: the host tool (evr-docker, evr-ptero) updates the runtime;
	// the runtime's own update is turned off.
	UpdaterHost = "host"
)

var channelRe = regexp.MustCompile(`^[a-z]+$`)

// ChannelOK reports whether name can be a channel of nEVR's manifest.
func ChannelOK(name string) bool { return channelRe.MatchString(name) }

// ChannelName normalises a configured channel: empty is stable, and so is
// "pre", evr-docker's old name for "newest GitHub release, pre-releases too"
// (no nEVR channel follows GitHub's pre-releases).
func ChannelName(name string) string {
	if name == "" || name == "pre" {
		return DefaultChannel
	}
	return name
}

type manifest struct {
	Channels map[string]string `json:"channels"`
}

// Channels returns the manifest's channels: name → version (without "v").
func (c *Client) Channels(ctx context.Context) (map[string]string, error) {
	url := c.ManifestURL
	if url == "" {
		url = ManifestURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("nEVR's manifest: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("nEVR's manifest: %s", resp.Status)
	}
	var m manifest
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&m); err != nil {
		return nil, fmt.Errorf("nEVR's manifest: %w", err)
	}
	out := map[string]string{}
	for name, v := range m.Channels {
		if ChannelOK(name) {
			if _, err := ParseVersion(v); err == nil {
				out[name] = strings.TrimPrefix(v, "v")
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("nEVR's manifest names no channel")
	}
	return out, nil
}

// ChannelNames returns the channels sorted, stable first.
func ChannelNames(chs map[string]string) []string {
	var out []string
	for name := range chs {
		out = append(out, name)
	}
	sort.Slice(out, func(i, j int) bool {
		if (out[i] == DefaultChannel) != (out[j] == DefaultChannel) {
			return out[i] == DefaultChannel
		}
		return out[i] < out[j]
	})
	return out
}

// Channel returns the release nEVR's channel name points at now.
func (c *Client) Channel(ctx context.Context, name string) (Release, error) {
	name = ChannelName(name)
	chs, err := c.Channels(ctx)
	if err != nil {
		return Release{}, err
	}
	v, ok := chs[name]
	if !ok {
		return Release{}, fmt.Errorf("nEVR has no channel %q (it has %s)", name, strings.Join(ChannelNames(chs), ", "))
	}
	rel, err := c.Find(ctx, "v"+v)
	if err != nil {
		return Release{}, fmt.Errorf("nEVR's %s channel: %w", name, err)
	}
	if rel.Blocked != "" {
		return Release{}, fmt.Errorf("nEVR's %s channel names %s, which is not used on servers: %s", name, rel.Tag, rel.Blocked)
	}
	return rel, nil
}

// SelfUpdates reports whether the release tag updates itself (UpdateMin on).
func SelfUpdates(tag string) bool { return atLeast(tag, UpdateMin) }

// UpdateConfigurable reports whether the release tag takes config.yaml's
// update section, and so a channel (UpdateConfigMin on).
func UpdateConfigurable(tag string) bool { return atLeast(tag, UpdateConfigMin) }

func atLeast(tag string, min Version) bool {
	v, err := ParseVersion(tag)
	return err == nil && !v.Less(min)
}

// Pickable returns the versions a host can be kept on, newest first: the
// releases servers can use (List) and the installed ones, without blocked
// releases and ones whose rollout failed. It is the list a version picker
// offers, as the launcher's "nEVR version" does.
func Pickable(rels []Release, installed []Installed) []string {
	skip, seen := map[string]bool{}, map[string]bool{}
	for _, in := range installed {
		if in.Bad != "" {
			skip[in.Tag] = true
		}
	}
	var out []string
	add := func(tag string) {
		if !seen[tag] && !skip[tag] && Blocked[tag] == "" {
			seen[tag] = true
			out = append(out, tag)
		}
	}
	for _, r := range rels {
		if r.Blocked == "" {
			add(r.Tag)
		}
	}
	for _, in := range installed {
		add(in.Tag)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, aerr := ParseVersion(out[i])
		b, berr := ParseVersion(out[j])
		return aerr == nil && (berr != nil || b.Less(a))
	})
	return out
}

// PickLabel is a version picker's label for tag: the nEVR channels that name
// it now and whether it is current ("v6.17.0 (nEVR beta, current)").
func PickLabel(tag string, channels map[string]string, current string) string {
	var notes []string
	for _, n := range ChannelNames(channels) {
		if "v"+strings.TrimPrefix(channels[n], "v") == tag {
			notes = append(notes, "nEVR "+n)
		}
	}
	if tag == current {
		notes = append(notes, "current")
	}
	if len(notes) == 0 {
		return tag
	}
	return tag + " (" + strings.Join(notes, ", ") + ")"
}

// SameRelease reports whether two version names are the same release
// (MAJOR.MINOR.PATCH): the store's tag ("v6.16.5", "v6.16.5-tel2100b") and
// what a running game reports ("6.16.5", "6.17.0-dev.125+1a874753") differ in
// form. Names that aren't versions are compared as they are.
func SameRelease(a, b string) bool {
	va, erra := ParseVersion(a)
	vb, errb := ParseVersion(b)
	if erra != nil || errb != nil {
		return strings.TrimPrefix(a, "v") == strings.TrimPrefix(b, "v")
	}
	return va.Major == vb.Major && va.Minor == vb.Minor && va.Patch == vb.Patch
}
