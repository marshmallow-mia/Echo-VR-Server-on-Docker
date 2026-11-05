package game

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetAPIAccessKeepsOtherKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings_mp_v2.json")
	orig := "{\n    \"game\": {\n        \"EnableAPIAccess\": false,\n        \"grabdeadzone\": 17.5\n    },\n    \"graphics\": {\n        \"adaptiveresheadroom\": 0.050000001,\n        \"display\": 4294967295\n    }\n}\n"
	if err := os.WriteFile(path, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, err := SetAPIAccess(path, true)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	b, _ := os.ReadFile(path)
	want := strings.Replace(orig, `"EnableAPIAccess": false`, `"EnableAPIAccess": true`, 1)
	if string(b) != want {
		t.Errorf("got\n%s\nwant\n%s", b, want)
	}
	if changed, _ := SetAPIAccess(path, true); changed {
		t.Error("second call rewrote the file")
	}
	if on, err := APIAccess(path); err != nil || !on {
		t.Errorf("APIAccess = %v, %v", on, err)
	}
	// A prefix without the file yet.
	fresh := filepath.Join(dir, "new", "settings_mp_v2.json")
	if _, err := SetAPIAccess(fresh, true); err != nil {
		t.Fatal(err)
	}
	if on, _ := APIAccess(fresh); !on {
		t.Error("fresh file without the key")
	}
}
