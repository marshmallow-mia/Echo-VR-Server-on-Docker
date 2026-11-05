package game

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// SettingsPath is the game's settings file (%LOCALAPPDATA%\rad\loneecho\
// settings_mp_v2.json) of a Wine prefix's user. The game reads it at start.
func SettingsPath(prefix, user string) string {
	return filepath.Join(prefix, "drive_c", "users", user, "AppData", "Local", "rad", "loneecho", "settings_mp_v2.json")
}

// SetAPIAccess switches the game's HTTP API (game.EnableAPIAccess, the
// in-game "API Access" setting) in the settings file at path. Without it the
// API answers /session with err_code -2 ("API setting is not enabled"). Every
// other key is kept as the game wrote it; a missing file gets only this key,
// and the game fills in the rest. It reports whether the file changed.
func SetAPIAccess(path string, on bool) (bool, error) {
	doc := map[string]any{}
	b, err := os.ReadFile(path)
	switch {
	case err == nil:
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.UseNumber() // keeps the game's numbers as written (0.050000001, 4294967295)
		if err := dec.Decode(&doc); err != nil {
			return false, fmt.Errorf("%s: %w", path, err)
		}
	case errors.Is(err, os.ErrNotExist):
	default:
		return false, err
	}
	g, _ := doc["game"].(map[string]any)
	if g == nil {
		g = map[string]any{}
	}
	if cur, ok := g["EnableAPIAccess"].(bool); ok && cur == on && err == nil {
		return false, nil
	}
	g["EnableAPIAccess"] = on
	doc["game"] = g
	out, err := json.MarshalIndent(doc, "", "    ") // the game's own layout: sorted keys, 4 spaces
	if err != nil {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	return true, writeAtomic(path, append(out, '\n'), 0o644)
}

// APIAccess reports the file's game.EnableAPIAccess (false when unset).
func APIAccess(path string) (bool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	var doc struct {
		Game struct {
			EnableAPIAccess bool `json:"EnableAPIAccess"`
		} `json:"game"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return false, fmt.Errorf("%s: %w", path, err)
	}
	return doc.Game.EnableAPIAccess, nil
}

func writeAtomic(path string, b []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}
