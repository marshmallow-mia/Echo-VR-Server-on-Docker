package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/match"
)

// SessionAPI reports a server's match stats switch: the setting and, while
// the game runs, what the game has now ("on"/"off", "" when unknown).
func (a *App) SessionAPI(ctx context.Context, name string) (setting bool, running string, err error) {
	s, err := a.Server(ctx, name)
	if err != nil {
		return false, "", err
	}
	setting = match.LoadSettings(a.Cfg.InstanceDir(name)).APIOn()
	if s.Running() && s.Status != nil {
		running = s.Status.SessionAPI
	}
	return setting, running, nil
}

// SetSessionAPI switches a server's match stats (the game's HTTP API). A
// running server gets the command through its control file, so its
// supervisor applies it (idle: restarts the game; in a match: after it); a
// stopped one gets the setting written for its next start.
func (a *App) SetSessionAPI(ctx context.Context, name string, on bool) error {
	return a.setSwitch(ctx, name, "api", on, match.SetAPI)
}

// Telemetry reports a server's telemetry switch (nevr-runtime's stream of
// players' actions to the supervisor): the setting and, while the game runs,
// what it has now ("on"/"off", "" when unknown).
func (a *App) Telemetry(ctx context.Context, name string) (setting bool, running string, err error) {
	s, err := a.Server(ctx, name)
	if err != nil {
		return false, "", err
	}
	setting = match.LoadSettings(a.Cfg.InstanceDir(name)).TelemetryOn()
	if s.Running() && s.Status != nil {
		running = s.Status.Telemetry
	}
	return setting, running, nil
}

// SetTelemetry switches a server's telemetry stream like SetSessionAPI.
func (a *App) SetTelemetry(ctx context.Context, name string, on bool) error {
	return a.setSwitch(ctx, name, "telemetry", on, match.SetTelemetry)
}

// setSwitch sends a running server "<command> on|off" through its control
// file, so its supervisor applies it (idle: restarts the game; in a match:
// after it); a stopped one gets the setting written for its next start.
func (a *App) setSwitch(ctx context.Context, name, command string, on bool, save func(string, bool) error) error {
	s, err := a.Server(ctx, name)
	if err != nil {
		return err
	}
	dir := a.Cfg.InstanceDir(name)
	if s.Running() {
		return a.writeOwned(filepath.Join(dir, "control"), []byte(command+" "+onOff(on)+"\n"))
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := save(dir, on); err != nil {
		return err
	}
	return os.Chown(filepath.Join(dir, match.SettingsFile), a.Cfg.UID, a.Cfg.UID)
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// Match returns a server's current or last match, if it had one.
func (a *App) Match(name string) (match.Match, bool) {
	m, err := match.ReadMatch(a.Cfg.InstanceDir(name))
	return m, err == nil
}

// MatchHistory returns a server's finished matches, newest first.
func (a *App) MatchHistory(name string) ([]match.Summary, error) {
	h, err := match.ReadHistory(a.Cfg.InstanceDir(name))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return h.Matches, err
}

func (a *App) writeOwned(path string, b []byte) error {
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return err
	}
	return os.Chown(path, a.Cfg.UID, a.Cfg.UID)
}
