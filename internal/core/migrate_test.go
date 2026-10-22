package core

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/game"
)

func touchFile(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func fakeV1(t *testing.T) (Legacy, string) {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "Echo-VR-Server-on-Docker")
	gameDir := filepath.Join(repo, "ready-at-dawn-echo-arena")
	touchFile(t, filepath.Join(repo, "run.sh"), "")
	touchFile(t, filepath.Join(repo, "scripts", "echo_update.sh"), "")
	touchFile(t, filepath.Join(gameDir, "bin", "win10", "echovr.exe"), "")
	touchFile(t, filepath.Join(gameDir, "logs", "0123456789ab", "a.log"), "log")
	cron := filepath.Join(t.TempDir(), "root")
	touchFile(t, cron, "0 * * * * other\n# disabled by evr-docker migrate: 0 4 * * * "+repo+"/scripts/echo_update.sh\n")
	return Legacy{GameDir: gameDir, RepoDir: repo, CronFile: cron}, t.TempDir()
}

func TestCleanupLegacyDefaultsMoveLogsOnly(t *testing.T) {
	l, data := fakeV1(t)
	if err := cleanupLegacy(context.Background(), l, data, MigrateOptions{}, func(string) {}); err != nil {
		t.Fatal(err)
	}
	if !isFile(filepath.Join(data, game.V1LogsDir, "0123456789ab", "a.log")) {
		t.Error("logs not moved")
	}
	if !isFile(filepath.Join(l.RepoDir, "run.sh")) || !isFile(filepath.Join(l.GameDir, "bin", "win10", "echovr.exe")) {
		t.Error("something was deleted without being asked")
	}
}

func TestCleanupLegacyRemovesEverythingAsked(t *testing.T) {
	l, data := fakeV1(t)
	opts := MigrateOptions{Logs: LogsMove, Remove: []string{RemoveGame, RemoveScripts}}
	if err := cleanupLegacy(context.Background(), l, data, opts, func(string) {}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(l.RepoDir); !os.IsNotExist(err) {
		t.Errorf("old checkout left: %v", err)
	}
	if !isFile(filepath.Join(data, game.V1LogsDir, "0123456789ab", "a.log")) {
		t.Error("logs lost")
	}
	b, _ := os.ReadFile(l.CronFile)
	if strings.Contains(string(b), "echo_update.sh") || !strings.Contains(string(b), "other") {
		t.Errorf("cron: %q", b)
	}
}

func TestMigrateOptionsValidate(t *testing.T) {
	if err := (MigrateOptions{Logs: LogsKeep, Remove: []string{RemoveGame}}).Validate(); err == nil {
		t.Error("keeping logs inside a deleted game folder was accepted")
	}
	if err := (MigrateOptions{Remove: []string{"everything"}}).Validate(); err == nil {
		t.Error("unknown removal accepted")
	}
	if err := (MigrateOptions{Logs: LogsDelete, Remove: []string{RemoveGame, RemoveImage, RemoveScripts}}).Validate(); err != nil {
		t.Error(err)
	}
}
