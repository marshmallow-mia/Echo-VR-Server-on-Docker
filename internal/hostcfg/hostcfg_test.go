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
