package game

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// RuntimeName is the signed runtime of nevr-runtime's two-file layout (v6.0.0
// on). The host BugSplat64.dll beside echovr.exe loads it only from the nEVR
// root and stops the game with exit code 78 without it.
const RuntimeName = "nevr-runtime.dll"

// HasRuntime reports whether the runtime folder dir is a two-file release.
func HasRuntime(dir string) bool {
	fi, err := os.Stat(filepath.Join(dir, RuntimeName))
	return err == nil && fi.Mode().IsRegular()
}

// NevrRoot is the nEVR root (%LOCALAPPDATA%\nevr\echovr) of a Wine prefix's
// user. From v6.0.0 on the runtime reads its config.yaml and its sign-in cache
// there and writes its logs, crash records and caches there; _local/config.yaml
// beside the game is never read.
func NevrRoot(prefix, user string) string {
	return filepath.Join(prefix, "drive_c", "users", user, "AppData", "Local", "nevr", "echovr")
}

// Root is what a two-file runtime needs in the nEVR root before each start.
type Root struct {
	// Dir is the nEVR root (NevrRoot).
	Dir string
	// RuntimeDLL is the store's nevr-runtime.dll; it is copied, not linked, so
	// the host checks the bytes in the root itself.
	RuntimeDLL string
	// ConfigYAML is written to Dir/config.yaml.
	ConfigYAML []byte
	// LegacyCredentials is the instance's _local/.credentials.json (a device
	// link made for 5.x). It is copied into the root only when the root has no
	// sign-in yet: once the runtime has used the root's copy it rotates the
	// refresh token there, and the old one must not overwrite it.
	LegacyCredentials string
	// KeepRuntime leaves a runtime already in the root as it is: one the
	// runtime's own update put there (SelfUpdated) and not older than the
	// store's. Without one in the root the store's is copied as always.
	KeepRuntime bool
	// ClearUpdate removes what the runtime's own update left in the root
	// (UpdateCleared), so a release it staged or swapped in does not come back
	// while the host tool keeps the runtime current.
	ClearUpdate bool
}

// UpdateDir is the runtime's self-update folder in the nEVR root (v6.17.0 on).
const UpdateDir = "update"

// UpdateCleared is what nEVR's installer removes from UpdateDir when it puts a
// release in place, in its order (the journal first): nothing older then
// competes with that release. The quarantine in state.json stays.
var UpdateCleared = []string{"swap.json", "pending.json", "crashed", "staged.json", "staging", "previous", "failed"}

// SelfUpdated returns the version of the runtime in root when nEVR's own
// update put it there: update/state.json's current release has the sha256 of
// the root's nevr-runtime.dll. ok is false for anything else (no state, a
// damaged one, a runtime copied in by the host tool).
func SelfUpdated(root string) (version string, ok bool) {
	f, err := os.Open(filepath.Join(root, UpdateDir, "state.json"))
	if err != nil {
		return "", false
	}
	defer f.Close()
	var st struct {
		Current struct {
			Version       string `json:"version"`
			RuntimeSHA256 string `json:"runtime_sha256"`
		} `json:"current"`
	}
	// The host treats a state over 64 KiB as damaged; so does this.
	if err := json.NewDecoder(io.LimitReader(f, 64<<10)).Decode(&st); err != nil || st.Current.Version == "" {
		return "", false
	}
	have, err := fileSHA256(filepath.Join(root, RuntimeName))
	if err != nil || !strings.EqualFold(have, st.Current.RuntimeSHA256) {
		return "", false
	}
	return st.Current.Version, true
}

// ClearUpdate removes UpdateCleared from root's update folder.
func ClearUpdate(root string) error {
	dir := filepath.Join(root, UpdateDir)
	for _, name := range UpdateCleared {
		if err := os.RemoveAll(filepath.Join(dir, name)); err != nil {
			return fmt.Errorf("nEVR root: clearing %s: %w", name, err)
		}
	}
	return nil
}

// CredentialsName is the runtime's sign-in cache.
const CredentialsName = ".credentials.json"

// Prepare puts the runtime, the config and the sign-in into the root. It is
// idempotent and replaces the runtime only when its bytes differ (and not at
// all with KeepRuntime).
func (r Root) Prepare() error {
	if r.Dir == "" || r.RuntimeDLL == "" {
		return errors.New("nEVR root: Dir and RuntimeDLL are required")
	}
	if err := os.MkdirAll(r.Dir, 0o755); err != nil {
		return err
	}
	if r.ClearUpdate {
		if err := ClearUpdate(r.Dir); err != nil {
			return err
		}
	}
	target := filepath.Join(r.Dir, RuntimeName)
	if _, err := os.Stat(target); !r.KeepRuntime || err != nil {
		if err := copyIfChanged(r.RuntimeDLL, target, 0o644); err != nil {
			return fmt.Errorf("nEVR root: runtime: %w", err)
		}
	}
	if r.ConfigYAML != nil {
		if err := writeFileAtomic(filepath.Join(r.Dir, "config.yaml"), r.ConfigYAML, 0o640); err != nil {
			return err
		}
	}
	creds := filepath.Join(r.Dir, CredentialsName)
	if r.LegacyCredentials != "" {
		if _, err := os.Stat(creds); os.IsNotExist(err) {
			b, err := os.ReadFile(r.LegacyCredentials)
			if err == nil {
				if err := writeFileAtomic(creds, b, 0o600); err != nil {
					return err
				}
			} else if !os.IsNotExist(err) {
				return err
			}
		}
	}
	return nil
}

func copyIfChanged(src, dst string, mode os.FileMode) error {
	want, err := fileSHA256(src)
	if err != nil {
		return err
	}
	if have, err := fileSHA256(dst); err == nil && have == want {
		return nil
	}
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return writeFileAtomic(dst, b, mode)
}
