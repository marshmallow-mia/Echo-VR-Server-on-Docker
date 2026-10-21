package cli

import (
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/internal/agent"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/internal/hostcfg"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/notify"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/updates"
)

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
			if err := agent.InstallService(g.configDir); err != nil {
				return err
			}
			g.say("installed %s and started evr-docker-agent", agent.UnitPath)
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use: "uninstall", Short: "Stop and remove the agent service",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := agent.RemoveService(); err != nil {
				return err
			}
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
			if cmd.Flags().Changed("events") {
				a.Cfg.Notify.Events = splitList(events)
				if err := a.Save(); err != nil {
					return err
				}
			}
			if len(args) == 1 {
				url := args[0]
				if url == "off" {
					url = ""
				}
				if err := agent.SetWebhook(cmd.Context(), a, url); err != nil {
					return err
				}
				if url != "" {
					g.say("saved; test message sent")
					return nil
				}
			}
			if a.Secrets[hostcfg.SecretWebhook] == "" {
				g.say("Discord alerts are off")
				return nil
			}
			if err := agent.TestWebhook(cmd.Context(), a.Secrets[hostcfg.SecretWebhook]); err != nil {
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

// component finds an updatable part by name.
func component(comps []updates.Component, name string) (updates.Component, error) {
	alias := map[string]string{"evr-docker": "self", "nevr-runtime": "runtime", "nevr": "runtime"}
	if a, ok := alias[name]; ok {
		name = a
	}
	var names []string
	for _, c := range comps {
		if c.Name() == name {
			return c, nil
		}
		names = append(names, c.Name())
	}
	return nil, fmt.Errorf("unknown part %q (one of: %s)", name, strings.Join(names, ", "))
}

func printStatus(g *globals, list []updates.Status, comps []updates.Component, set updates.Settings) error {
	return g.print(list, func(w io.Writer) {
		title := map[string]string{}
		for _, c := range comps {
			title[c.Name()] = c.Title()
		}
		var rows [][]string
		for _, s := range list {
			avail := "up to date"
			switch {
			case s.Error != "":
				avail = "check failed: " + s.Error
			case s.Available:
				avail = s.Latest
			}
			rows = append(rows, []string{s.Component, title[s.Component], s.Installed, avail, string(set.Mode(s.Component))})
		}
		g.table("PART\tNAME\tINSTALLED\tAVAILABLE\tMODE", rows)
	})
}

func updateCmd(g *globals) *cobra.Command {
	check := func(cmd *cobra.Command, args []string) error {
		a, err := g.app()
		if err != nil {
			return err
		}
		comps, set := agent.Components(a, nil), a.Cfg.Updates.Settings()
		if len(args) == 1 {
			c, err := component(comps, args[0])
			if err != nil {
				return err
			}
			return printStatus(g, []updates.Status{updates.CheckOne(cmd.Context(), a.Dir, c)}, comps, set)
		}
		return printStatus(g, updates.CheckAll(cmd.Context(), a.Dir, comps, set), comps, set)
	}
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Updates of evr-docker itself, the game files and nevr-runtime",
		Long: `Without a subcommand: check every part. Parts: self (evr-docker), game
(the shared game files; repairs them from the mirror), runtime (nevr-runtime,
rolled out on one idle server first). "update settings" chooses per part:
off, notify (check and alert) or auto (install; needs the agent).`,
		Args: cobra.NoArgs,
		RunE: check,
	}
	cmd.AddCommand(&cobra.Command{Use: "check [PART]", Short: "Check for updates (all parts, or one)", Args: cobra.MaximumNArgs(1), RunE: check})
	cmd.AddCommand(&cobra.Command{
		Use: "ls PART", Short: "List the versions a part can be updated to", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := g.app()
			if err != nil {
				return err
			}
			c, err := component(agent.Components(a, nil), args[0])
			if err != nil {
				return err
			}
			vs, err := c.Versions(cmd.Context())
			if err != nil {
				return err
			}
			return g.print(vs, func(w io.Writer) {
				if len(vs) == 0 {
					fmt.Fprintln(w, c.Title()+" has no versions to choose from; apply repairs it from the mirror.")
				}
				for _, v := range vs {
					fmt.Fprintln(w, v)
				}
			})
		},
	})
	var version string
	apply := &cobra.Command{
		Use: "apply [PART]", Short: "Install an update now (default part: runtime)", Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := g.app()
			if err != nil {
				return err
			}
			name := "runtime"
			if len(args) == 1 {
				name = args[0]
			}
			say := func(s string) { g.say("%s", s) }
			c, err := component(agent.Components(a, say), name)
			if err != nil {
				return err
			}
			if err := c.Apply(cmd.Context(), version, say); err != nil {
				return err
			}
			updates.CheckOne(cmd.Context(), a.Dir, c)
			return nil
		},
	}
	apply.Flags().StringVar(&version, "version", "", "version to install (default: the newest; see update ls)")
	cmd.AddCommand(apply)
	var self, game, rt, window string
	var hours int
	settings := &cobra.Command{
		Use: "settings", Short: "Show or change how each part is updated",
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := g.app()
			if err != nil {
				return err
			}
			u := &a.Cfg.Updates
			fl := cmd.Flags()
			for _, f := range []struct {
				flag string
				dst  *string
				val  string
			}{{"self", &u.Self, self}, {"game", &u.Game, game}, {"runtime", &u.Runtime, rt}, {"window", &u.Window, window}} {
				if fl.Changed(f.flag) {
					*f.dst = f.val
				}
			}
			if fl.Changed("check-hours") {
				u.CheckHours = hours
			}
			if fl.NFlag() > 0 {
				if err := a.Save(); err != nil {
					return err
				}
			}
			return g.print(u, func(w io.Writer) {
				fmt.Fprintf(w, "self:        %s\ngame:        %s\nruntime:     %s\nwindow:      %s\ncheck every: %s\n", u.Self, u.Game, u.Runtime, orDash(u.Window), a.Cfg.Updates.Settings().Interval())
				if !agent.ServiceInstalled() {
					fmt.Fprintln(w, "\nThe agent is not installed: nothing is checked or installed in the background (evr-docker agent install).")
				}
			})
		},
	}
	sf := settings.Flags()
	sf.StringVar(&self, "self", "", "evr-docker itself: off, notify or auto")
	sf.StringVar(&game, "game", "", "game files: off, notify or auto")
	sf.StringVar(&rt, "runtime", "", "nevr-runtime: off, notify or auto")
	sf.StringVar(&window, "window", "", `automatic installs only in this local time window, e.g. 04:00-06:00 ("" = any time)`)
	sf.IntVar(&hours, "check-hours", 0, "hours between checks")
	cmd.AddCommand(settings)
	return cmd
}

func postUpdateCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use: "post-update", Hidden: true, Short: "Run after a self-update by the new binary: move the servers to its image",
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := g.app()
			if err != nil {
				return err
			}
			if a.Cfg.Image != hostcfg.DefaultImage {
				g.say("servers keep the configured image %s", a.Cfg.Image)
				return nil
			}
			g.say("moving the servers to %s (each after its current match)", a.Cfg.Image)
			_, err = a.Apply(cmd.Context(), false, func(s string) { g.say("%s", s) })
			return err
		},
	}
}
