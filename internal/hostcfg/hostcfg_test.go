package hostcfg

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOldRuntimeAutoUpdateMoves(t *testing.T) {
	for _, c := range []struct{ auto, want string }{{"true", "auto"}, {"false", "notify"}} {
		dir := t.TempDir()
		old := "version: 1\nproject: evr\ndata_dir: /srv/echovr\nuid: 999\ninstances: []\nruntime:\n    channel: pre\n    auto_update: " + c.auto + "\n    check_hours: 3\n"
		os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(old), 0o644)
		cfg, err := Load(dir)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Updates.Runtime != c.want || cfg.Updates.CheckHours != 3 || cfg.Updates.Self != "notify" || cfg.Updates.Game != "notify" {
			t.Errorf("auto_update %s → %+v", c.auto, cfg.Updates)
		}
		if err := cfg.Save(dir); err != nil {
			t.Fatal(err)
		}
		b, _ := os.ReadFile(filepath.Join(dir, "config.yaml"))
		if strings.Contains(string(b), "auto_update") || !strings.Contains(string(b), "runtime: "+c.want) {
			t.Errorf("saved:\n%s", b)
		}
	}
}

func TestBadUpdateSettingsRejected(t *testing.T) {
	cfg := New()
	cfg.Updates.Game, cfg.Updates.Window = "sometimes", "4-6"
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "updates.game") || !strings.Contains(err.Error(), "updates.window") {
		t.Errorf("got %v", err)
	}
}

// A config from before runtime.updater keeps evr-docker updating, on nEVR's
// stable channel (no nEVR channel follows GitHub's releases); a pin stays.
func TestOldRuntimeChannelMigrates(t *testing.T) {
	for _, c := range []struct{ in, channel, pinned string }{
		{"channel: pre", "stable", ""},
		{"channel: stable", "stable", ""},
		{"channel: pinned\n    pinned: v6.17.0", "pinned", "v6.17.0"},
	} {
		dir := t.TempDir()
		old := "version: 1\nproject: evr\ndata_dir: /srv/echovr\nuid: 999\ninstances: []\nruntime:\n    " + c.in + "\n"
		os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(old), 0o644)
		cfg, err := Load(dir)
		if err != nil {
			t.Fatal(err)
		}
		if r := cfg.Runtime; r.Updater != UpdaterHost || r.Channel != c.channel || r.Pinned != c.pinned {
			t.Errorf("%q → %+v", c.in, r)
		}
		if env := strings.Join(cfg.Runtime.SelfUpdateEnv(), " "); env != "EVR_RUNTIME_UPDATER=host NEVR_UPDATE_CHANNEL=stable" {
			t.Errorf("%q: env %s", c.in, env)
		}
		cfg.Save(dir)
		if b, _ := os.ReadFile(filepath.Join(dir, "config.yaml")); !strings.Contains(string(b), "updater: evr-docker") {
			t.Errorf("saved:\n%s", b)
		}
	}
}

func TestNewConfigLetsNevrUpdate(t *testing.T) {
	dir := t.TempDir()
	cfg := New()
	if err := cfg.Save(dir); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(dir)
	if err != nil || cfg.Runtime.Updater != UpdaterNevr || cfg.Runtime.Channel != "stable" {
		t.Fatalf("%+v %v", cfg.Runtime, err)
	}
	if env := strings.Join(cfg.Runtime.SelfUpdateEnv(), " "); env != "EVR_RUNTIME_UPDATER=nevr NEVR_UPDATE_CHANNEL=stable" {
		t.Errorf("env %s", env)
	}
	cfg.Runtime.Channel, cfg.Runtime.Pinned = "pinned", "v6.17.0"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "needs runtime.updater evr-docker") {
		t.Errorf("pinned with nevr: %v", err)
	}
	cfg.Runtime.Updater = UpdaterHost
	if env := strings.Join(cfg.Runtime.SelfUpdateEnv(), " "); !strings.Contains(env, "EVR_RUNTIME_UPDATER=host") {
		t.Errorf("env %s", env)
	}
}
