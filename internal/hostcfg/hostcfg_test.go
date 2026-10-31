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
		// Keys the file doesn't have take the defaults (the tool and mods update themselves).
		if cfg.Updates.Runtime != c.want || cfg.Updates.CheckHours != 3 || cfg.Updates.Self != "auto" || cfg.Updates.Game != "notify" || cfg.Updates.Mods != "auto" {
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

// A mode the file names stays: the new defaults reach only keys it lacks.
func TestNamedUpdateModesStay(t *testing.T) {
	dir := t.TempDir()
	old := "version: 1\nproject: evr\ndata_dir: /srv/echovr\nuid: 999\ninstances: []\nruntime:\n    channel: pre\nupdates:\n    self: notify\n    game: off\n    runtime: notify\n"
	os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(old), 0o644)
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if u := cfg.Updates; u.Self != "notify" || u.Game != "off" || u.Runtime != "notify" || u.Mods != "auto" {
		t.Errorf("%+v", u)
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
