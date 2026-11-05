// Package match follows the match a server hosts through the game's HTTP API
// (/session on 127.0.0.1) and keeps it in the instance's home directory for
// host tools and the Pelican panel plugin: match.json (the current or last
// match) and match-history.json (finished matches, newest first).
package match

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// File names inside an instance's home directory.
const (
	SettingsFile = "evr-settings.json"
	FileName     = "match.json"
	HistoryFile  = "match-history.json"
)

// Settings are the instance's own switches, kept across restarts and image
// updates. Host tools write the file while the server is stopped; while it
// runs they send the supervisor a command ("api on|off"), which writes it.
type Settings struct {
	// SessionAPI switches the game's HTTP API (EnableAPIAccess) that match
	// stats come from; nil means on.
	SessionAPI *bool `json:"session_api,omitempty"`
	// Telemetry switches nEVR's telemetry stream to the supervisor's local
	// ingest (match events the session API has no field for); nil means on.
	// It never leaves the container: the ingest listens on 127.0.0.1.
	Telemetry *bool `json:"telemetry,omitempty"`
}

// APIOn reports whether the session API is wanted (the default).
func (s Settings) APIOn() bool { return s.SessionAPI == nil || *s.SessionAPI }

// TelemetryOn reports whether nEVR's telemetry stream is wanted (the default).
func (s Settings) TelemetryOn() bool { return s.Telemetry == nil || *s.Telemetry }

// LoadSettings reads home's settings; a missing or broken file gives defaults.
func LoadSettings(home string) Settings {
	var s Settings
	b, err := os.ReadFile(filepath.Join(home, SettingsFile))
	if err == nil {
		_ = json.Unmarshal(b, &s)
	}
	return s
}

// SaveSettings writes home's settings atomically.
func SaveSettings(home string, s Settings) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return writeJSON(filepath.Join(home, SettingsFile), b)
}

// SetAPI stores the session API switch in home's settings.
func SetAPI(home string, on bool) error {
	s := LoadSettings(home)
	s.SessionAPI = &on
	return SaveSettings(home, s)
}

// SetTelemetry stores the telemetry switch in home's settings.
func SetTelemetry(home string, on bool) error {
	s := LoadSettings(home)
	s.Telemetry = &on
	return SaveSettings(home, s)
}

func writeJSON(path string, b []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	_, err = tmp.Write(append(b, '\n'))
	if err == nil {
		err = tmp.Chmod(0o644)
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		os.Remove(tmp.Name())
	}
	return err
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(b) == 0 {
		return errors.New(path + ": empty")
	}
	return json.Unmarshal(b, v)
}
