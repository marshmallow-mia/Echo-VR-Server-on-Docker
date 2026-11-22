package supervisor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/game"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/nevr"
)

// selfUpdateRun starts a two-file runtime of tag once with a wine that
// records its arguments, and returns them with the root's config.yaml.
func selfUpdateRun(t *testing.T, tag string, prep func(o *Options, root string)) (args, cfg, root string) {
	t.Helper()
	o, _ := testOptions(t, "v6.6.0-no-password.log", 6792)
	twoFileAt(t, &o, tag, "v6.6.0-no-password.log", 1)
	argsFile := filepath.Join(filepath.Dir(o.Wine), "args")
	os.WriteFile(o.Wine, []byte(fmt.Sprintf("#!/bin/sh\necho \"$@\" > '%s'\nexit 1\n", argsFile)), 0o755)
	root = game.NevrRoot(filepath.Join(o.Home, ".wine"), "container")
	if prep != nil {
		prep(&o, root)
	}
	newSup(t, o).runOnce(context.Background())
	a, _ := os.ReadFile(argsFile)
	c, _ := os.ReadFile(filepath.Join(root, "config.yaml"))
	return string(a), string(c), root
}

func TestSelfUpdateOldRuntimeUntouched(t *testing.T) {
	for _, updater := range []string{nevr.UpdaterHost, nevr.UpdaterNevr} {
		args, cfg, _ := selfUpdateRun(t, "v6.16.5", func(o *Options, _ string) { o.Updater = updater })
		if strings.Contains(args, "-no-update") || strings.Contains(cfg, "update:") {
			t.Errorf("%s: a runtime before %s gets neither: args %q config %q", updater, nevr.UpdateMin, args, cfg)
		}
	}
}

func TestSelfUpdateHostManaged(t *testing.T) {
	args, cfg, root := selfUpdateRun(t, "v6.17.0", func(o *Options, root string) {
		// What nEVR's own update left: a staged 6.18.1 and its swapped runtime.
		os.MkdirAll(filepath.Join(root, "update", "staging", "6.18.1"), 0o755)
		os.WriteFile(filepath.Join(root, "update", "staged.json"), []byte("{}"), 0o644)
		os.WriteFile(filepath.Join(root, "update", "pending.json"), []byte("{}"), 0o644)
		os.WriteFile(filepath.Join(root, game.RuntimeName), []byte("6.18.1"), 0o644)
	})
	if !strings.Contains(args, "-no-update") {
		t.Errorf("args %q", args)
	}
	if !strings.Contains(cfg, "update:\n  enabled: false\n") {
		t.Errorf("config %q", cfg)
	}
	for _, name := range []string{"staged.json", "pending.json", "staging"} {
		if _, err := os.Stat(filepath.Join(root, "update", name)); !os.IsNotExist(err) {
			t.Errorf("update/%s left", name)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(root, game.RuntimeName)); string(b) != "runtime" {
		t.Errorf("the store's runtime must run: %q", b)
	}
}

// nevrState makes the root's runtime one nEVR's own update put there.
func nevrState(t *testing.T, root, version string) {
	t.Helper()
	body := []byte("nevr's " + version)
	os.MkdirAll(filepath.Join(root, "update"), 0o755)
	os.WriteFile(filepath.Join(root, game.RuntimeName), body, 0o644)
	sum := sha256.Sum256(body)
	st := fmt.Sprintf(`{"channel":"beta","current":{"version":%q,"runtime_sha256":%q}}`, version, hex.EncodeToString(sum[:]))
	os.WriteFile(filepath.Join(root, "update", "state.json"), []byte(st), 0o644)
	os.WriteFile(filepath.Join(root, "update", "staged.json"), []byte("{}"), 0o644)
}

func TestSelfUpdateNevr(t *testing.T) {
	args, cfg, root := selfUpdateRun(t, "v6.17.0", func(o *Options, root string) {
		o.Updater, o.UpdateChannel = nevr.UpdaterNevr, "beta"
		nevrState(t, root, "6.18.1")
	})
	if strings.Contains(args, "-no-update") {
		t.Errorf("args %q", args)
	}
	if !strings.Contains(cfg, "update:\n  enabled: true\n  channel: \"beta\"\n") {
		t.Errorf("config %q", cfg)
	}
	if b, _ := os.ReadFile(filepath.Join(root, game.RuntimeName)); string(b) != "nevr's 6.18.1" {
		t.Errorf("nEVR's newer runtime replaced: %q", b)
	}
	if _, err := os.Stat(filepath.Join(root, "update", "staged.json")); err != nil {
		t.Error("nEVR's staged release removed")
	}
}

func TestSelfUpdateNevrNeverBackwards(t *testing.T) {
	_, _, root := selfUpdateRun(t, "v6.18.1", func(o *Options, root string) {
		o.Updater = nevr.UpdaterNevr
		nevrState(t, root, "6.17.0")
	})
	if b, _ := os.ReadFile(filepath.Join(root, game.RuntimeName)); string(b) != "runtime" {
		t.Errorf("an older self-updated runtime must give way to the store's: %q", b)
	}
}

// A pinned server (the update canary is one) runs its release, not nEVR's.
func TestSelfUpdateNevrPinned(t *testing.T) {
	args, cfg, root := selfUpdateRun(t, "v6.17.0", func(o *Options, root string) {
		o.Updater, o.Runtime = nevr.UpdaterNevr, "v6.17.0"
		nevrState(t, root, "6.18.1")
	})
	if !strings.Contains(args, "-no-update") || !strings.Contains(cfg, "enabled: false") {
		t.Errorf("args %q config %q", args, cfg)
	}
	if b, _ := os.ReadFile(filepath.Join(root, game.RuntimeName)); string(b) != "runtime" {
		t.Errorf("pinned release not in place: %q", b)
	}
}

func TestWithUpdateKeepsOperatorSection(t *testing.T) {
	in := []byte("version: 1\nupdate:\n  channel: beta\n")
	if out, ok := withUpdate(in, selfUpdate{applies: true}); ok || string(out) != string(in) {
		t.Errorf("%v %q", ok, out)
	}
}
