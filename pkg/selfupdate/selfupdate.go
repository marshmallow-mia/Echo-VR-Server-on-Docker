// Package selfupdate replaces the running evr-docker or evr-ptero binary with
// a newer release, verified against the release's checksums.txt.
package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/ghrel"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/nevr"
)

// Updater updates one binary from one repo's releases.
type Updater struct {
	Repo    string // e.g. marshmallow-mia/Echo-VR-Server-on-Docker
	Binary  string // e.g. evr-docker; the asset is <Binary>_linux_<arch>
	Current string // the running version ("dev" for builds without one)
	Exe     string // the file to replace; default: the running executable
	Client  *ghrel.Client
}

func (u *Updater) asset() string { return u.Binary + "_linux_" + runtime.GOARCH }

// Releases lists the releases that have a binary for this machine.
func (u *Updater) Releases(ctx context.Context) ([]ghrel.Release, error) {
	all, err := u.Client.List(ctx, u.Repo)
	if err != nil {
		return nil, err
	}
	var out []ghrel.Release
	for _, r := range all {
		if _, ok := r.Assets[u.asset()]; ok {
			out = append(out, r)
		}
	}
	return out, nil
}

// Pick chooses the release to update to: version when given, otherwise the
// newest one, pre-releases included while the running version is one or no
// full release exists yet.
func Pick(rels []ghrel.Release, current, version string) (ghrel.Release, error) {
	if version != "" {
		for _, r := range rels {
			if r.Tag == version || r.Tag == "v"+version {
				return r, nil
			}
		}
		return ghrel.Release{}, fmt.Errorf("release %s not found", version)
	}
	pre := true
	if cv, err := nevr.ParseVersion(current); err == nil && cv.Pre == "" {
		for _, r := range rels {
			if !r.Prerelease {
				pre = false
				break
			}
		}
	}
	for _, r := range rels {
		if pre || !r.Prerelease {
			return r, nil
		}
	}
	return ghrel.Release{}, errors.New("no release found")
}

// Newer reports whether tag is newer than the running version. Development
// builds count every release as newer.
func Newer(current, tag string) bool {
	cv, err := nevr.ParseVersion(current)
	if err != nil {
		return true
	}
	tv, err := nevr.ParseVersion(tag)
	return err == nil && cv.Less(tv)
}

// Apply downloads rel's binary, checks it against checksums.txt (and GitHub's
// digest) and atomically replaces the executable. The old one is kept as
// <exe>.prev.
func (u *Updater) Apply(ctx context.Context, rel ghrel.Release, say func(string)) error {
	a, ok := rel.Assets[u.asset()]
	if !ok {
		return fmt.Errorf("%s has no %s", rel.Tag, u.asset())
	}
	want, err := u.Client.Checksum(ctx, rel, "checksums.txt", u.asset())
	if err != nil {
		return err
	}
	if want == "" {
		want = a.SHA256
	}
	if want == "" {
		return fmt.Errorf("%s publishes no checksum for %s; not installing it", rel.Tag, u.asset())
	}
	exe := u.Exe
	if exe == "" {
		if exe, err = os.Executable(); err != nil {
			return err
		}
		if exe, err = filepath.EvalSymlinks(exe); err != nil {
			return err
		}
	}
	say(fmt.Sprintf("downloading %s %s", u.Binary, rel.Tag))
	tmp, err := os.CreateTemp(filepath.Dir(exe), "."+filepath.Base(exe)+".new-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	got, err := u.Client.Fetch(ctx, a.URL, tmp)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("checksum mismatch for %s %s: got %s, want %s", u.Binary, rel.Tag, got, want)
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return err
	}
	// Keep the old binary for a manual rollback. A hard link leaves the
	// running file in place until the rename below.
	_ = os.Remove(exe + ".prev")
	if err := os.Link(exe, exe+".prev"); err != nil {
		say("could not keep the old binary: " + err.Error())
	}
	if err := os.Rename(tmp.Name(), exe); err != nil {
		return err
	}
	say(fmt.Sprintf("%s %s installed (checksum ok); the old one is kept as %s.prev", u.Binary, rel.Tag, exe))
	return nil
}
