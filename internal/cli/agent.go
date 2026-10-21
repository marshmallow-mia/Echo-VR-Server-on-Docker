package cli

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/internal/agent"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/internal/hostcfg"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/notify"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/update"
)

const unitPath = "/etc/systemd/system/evr-docker-agent.service"

func agentCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{Use: "agent", Short: "Background service: metrics, Discord alerts and automatic updates"}
	cmd.AddCommand(&cobra.Command{
		Use: "run", Short: "Run the agent in the foreground (systemd runs this)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			l := log.New(os.Stderr, "", 0)
			return (&agent.Agent{Dir: g.configDir, Log: func(s string) { l.Println(s) }}).Run(cmd.Context())
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use: "install", Short: "Install and start the agent as a systemd service",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if _, err := hostcfg.Load(g.configDir); err != nil {
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
`, exe, g.configDir)
			if err := os.WriteFile(unitPath, []byte(unit), 0o644); err != nil {
				return err
			}
			for _, args := range [][]string{{"daemon-reload"}, {"enable", "evr-docker-agent.service"}, {"restart", "evr-docker-agent.service"}} {
				if out, err := exec.Command("systemctl", args...).CombinedOutput(); err != nil {
					return fmt.Errorf("systemctl %s: %v: %s", strings.Join(args, " "), err, out)
				}
			}
			g.say("installed %s and started evr-docker-agent", unitPath)
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use: "uninstall", Short: "Stop and remove the agent service",
		RunE: func(cmd *cobra.Command, _ []string) error {
			_ = exec.Command("systemctl", "disable", "--now", "evr-docker-agent.service").Run()
			if err := os.Remove(unitPath); err != nil && !os.IsNotExist(err) {
				return err
			}
			_ = exec.Command("systemctl", "daemon-reload").Run()
			g.say("agent removed")
			return nil
		},
	})
	var events string
	webhook := &cobra.Command{
		Use: "webhook [URL]", Short: "Set (or with no URL, test) the Discord webhook for alerts",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := g.app()
			if err != nil {
				return err
			}
			if len(args) == 1 {
				if args[0] == "off" {
					delete(a.Secrets, hostcfg.SecretWebhook)
				} else if !strings.HasPrefix(args[0], "https://") {
					return errors.New("the webhook URL must start with https://")
				} else {
					a.Secrets[hostcfg.SecretWebhook] = args[0]
				}
			}
			if cmd.Flags().Changed("events") {
				a.Cfg.Notify.Events = splitList(events)
			}
			if err := a.Save(); err != nil {
				return err
			}
			if a.Secrets[hostcfg.SecretWebhook] == "" {
				g.say("Discord alerts are off")
				return nil
			}
			host, _ := os.Hostname()
			n := &notify.Notifier{URL: a.Secrets[hostcfg.SecretWebhook]}
			if err := n.Send(cmd.Context(), notify.Event{Kind: "test", Level: notify.Info, Host: host, Title: "evr-docker alerts are set up",
				Text: "This channel will get alerts about the Echo VR servers on this host."}); err != nil {
				return err
			}
			g.say("test message sent")
			return nil
		},
	}
	webhook.Flags().StringVar(&events, "events", "", "comma-separated event kinds to send (default all): "+strings.Join([]string{
		notify.EventServerFailed, notify.EventCredentialsFailed, notify.EventCrashLoop, notify.EventServerDown, notify.EventServerRecovered,
		notify.EventUpdateAvailable, notify.EventUpdateApplied, notify.EventUpdateRolledBack}, ","))
	cmd.AddCommand(webhook)
	return cmd
}

func updateCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{Use: "update", Short: "Check for and roll out nevr-runtime updates"}
	cmd.AddCommand(&cobra.Command{
		Use: "check", Short: "Show whether a newer runtime is available for the channel",
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := g.app()
			if err != nil {
				return err
			}
			u := agent.Updater(a, nil)
			cur, target, available, err := u.Check(cmd.Context(), update.Policy{Channel: a.Cfg.Runtime.Channel, Pinned: a.Cfg.Runtime.Pinned})
			if err != nil {
				return err
			}
			out := map[string]any{"current": cur, "target": target.Tag, "available": available}
			return g.print(out, func(w io.Writer) {
				if available {
					fmt.Fprintf(w, "nevr-runtime %s is available (current %s). Roll it out with: evr-docker update apply\n", target.Tag, cur)
				} else {
					fmt.Fprintf(w, "up to date (%s, channel %s)\n", cur, a.Cfg.Runtime.Channel)
				}
			})
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use: "apply", Short: "Roll out the channel's runtime: canary on an idle server first, then everyone",
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := g.app()
			if err != nil {
				return err
			}
			u := agent.Updater(a, func(s string) { g.say("%s", s) })
			res, err := u.Run(cmd.Context(), update.Policy{Channel: a.Cfg.Runtime.Channel, Pinned: a.Cfg.Runtime.Pinned})
			if err != nil {
				return err
			}
			return g.print(res, func(w io.Writer) {
				switch res.Action {
				case "up_to_date":
					fmt.Fprintf(w, "up to date (%s)\n", res.From)
				case "switched":
					fmt.Fprintf(w, "now on %s; servers switch at their next match boundary\n", res.To)
				case "rolled_back":
					fmt.Fprintf(w, "%s failed on canary %s: %s; staying on %s\n", res.To, res.Canary, res.Detail, res.From)
				default:
					fmt.Fprintf(w, "%s installed but not rolled out: %s\n", res.To, res.Detail)
				}
			})
		},
	})
	return cmd
}
