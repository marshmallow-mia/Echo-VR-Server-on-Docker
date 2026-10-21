package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/internal/core"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/internal/hostcfg"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/notify"
)

// UnitPath is the agent's systemd unit.
const UnitPath = "/etc/systemd/system/evr-docker-agent.service"

// ServiceInstalled reports whether the agent's systemd unit exists.
func ServiceInstalled() bool {
	_, err := os.Stat(UnitPath)
	return err == nil
}

// InstallService installs and (re)starts the agent as a systemd service.
func InstallService(dir string) error {
	if _, err := hostcfg.Load(dir); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return err
	}
	unit := fmt.Sprintf(`[Unit]
Description=evr-docker agent (Echo VR server metrics, alerts, updates)
After=docker.service network-online.target
Wants=network-online.target
Requires=docker.service

[Service]
ExecStart=%s --config-dir %s agent run
Restart=always
RestartSec=10

[Install]
WantedBy=multi-user.target
`, exe, dir)
	if err := os.WriteFile(UnitPath, []byte(unit), 0o644); err != nil {
		return err
	}
	for _, args := range [][]string{{"daemon-reload"}, {"enable", "evr-docker-agent.service"}, {"restart", "evr-docker-agent.service"}} {
		if out, err := exec.Command("systemctl", args...).CombinedOutput(); err != nil {
			return fmt.Errorf("systemctl %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}
	return nil
}

// RemoveService stops the agent and removes its systemd unit.
func RemoveService() error {
	_ = exec.Command("systemctl", "disable", "--now", "evr-docker-agent.service").Run()
	if err := os.Remove(UnitPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	_ = exec.Command("systemctl", "daemon-reload").Run()
	return nil
}

// SetWebhook stores the Discord webhook (empty turns alerts off); a new one
// gets a test message first.
func SetWebhook(ctx context.Context, app *core.App, url string) error {
	if url == "" {
		delete(app.Secrets, hostcfg.SecretWebhook)
		return app.Save()
	}
	if !strings.HasPrefix(url, "https://") {
		return errors.New("the webhook URL must start with https://")
	}
	if err := TestWebhook(ctx, url); err != nil {
		return err
	}
	app.Secrets[hostcfg.SecretWebhook] = url
	return app.Save()
}

// TestWebhook sends a test message.
func TestWebhook(ctx context.Context, url string) error {
	host, _ := os.Hostname()
	return (&notify.Notifier{URL: url}).Send(ctx, notify.Event{Kind: "test", Level: notify.Info, Host: host, Title: "evr-docker alerts are set up",
		Text: "This channel will get alerts about the Echo VR servers on this host."})
}
