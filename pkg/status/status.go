// Package status is the status.json contract between the in-container
// supervisor and everything that watches it (TUIs, agents, metrics).
package status

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// FileName is the status file's name inside an instance's home directory.
const FileName = "status.json"

// State is the lifecycle state of one game server instance.
type State string

const (
	StateStarting   State = "starting"   // process launched, not registered yet
	StateRegistered State = "registered" // waiting for a match
	StateInSession  State = "in_session" // hosting a match
	StateStopping   State = "stopping"   // shutting down
	StateBackoff    State = "backoff"    // waiting before a restart after an error
	StateFailed     State = "failed"     // error that needs a settings change
	StateStopped    State = "stopped"
)

// Idle reports whether an instance can be restarted without dropping players.
func (s State) Idle() bool {
	return s != StateInSession && s != StateStopping
}

// Status is written atomically by the supervisor on every state change.
type Status struct {
	Instance       string    `json:"instance"`
	State          State     `json:"state"`
	Since          time.Time `json:"since"`
	UpdatedAt      time.Time `json:"updated_at"`
	PID            int       `json:"pid,omitempty"`
	Port           int       `json:"port"`
	HTTPPort       int       `json:"http_port,omitempty"`
	PublicIP       string    `json:"public_ip,omitempty"`
	RuntimeVersion string    `json:"runtime_version,omitempty"`
	GameVersion    string    `json:"game_version,omitempty"`
	Session        string    `json:"session,omitempty"`
	SessionsTotal  int       `json:"sessions_total"`
	Restarts       int       `json:"restarts"`
	Registrations  int       `json:"registrations"`
	LastExitCode   *int      `json:"last_exit_code,omitempty"`
	Reason         string    `json:"reason,omitempty"`
	LastError      string    `json:"last_error,omitempty"`
	NextStart      time.Time `json:"next_start,omitzero"`
}

// Write stores s at path atomically.
func Write(path string, s Status) error {
	s.UpdatedAt = time.Now().UTC()
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".status-*.json")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
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

// Read loads a status file.
func Read(path string) (Status, error) {
	var s Status
	b, err := os.ReadFile(path)
	if err != nil {
		return s, err
	}
	err = json.Unmarshal(b, &s)
	return s, err
}

// Stale reports whether the supervisor stopped updating the file, e.g. because
// the container is gone. The supervisor rewrites it at least every heartbeat.
func (s Status) Stale(now time.Time, heartbeat time.Duration) bool {
	return now.Sub(s.UpdatedAt) > 3*heartbeat
}
