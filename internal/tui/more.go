package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"gopkg.in/yaml.v3"

	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/internal/agent"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/internal/core"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/internal/hostcfg"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/game"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/nevr/config"
	ui "github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/tui"
)

type legacyMsg struct {
	l   core.Legacy
	err error
}

// menu lists everything that has no key of its own on the dashboard.
func (m *model) menu() tea.Cmd {
	choice := "updates"
	f := huh.NewForm(huh.NewGroup(huh.NewSelect[string]().Title("What do you want to do?").Options(
		huh.NewOption("Updates (evr-docker, game files, nevr-runtime)", "updates"),
		huh.NewOption("Runtime versions", "runtime"),
		huh.NewOption("EchoVRCE account", "account"),
		huh.NewOption("Discord alerts and the background service", "alerts"),
		huh.NewOption("Number of servers", "scale"),
		huh.NewOption("Install or check the game files", "game"),
		huh.NewOption("Check the host (doctor)", "doctor"),
		huh.NewOption("Show the configuration", "show"),
		huh.NewOption("Edit the configuration file", "edit"),
		huh.NewOption("Write a docker-compose file (run without evr-docker)", "compose"),
		huh.NewOption("Move a v1 (run.sh) installation", "migrate"),
	).Value(&choice)).Title("More"))
	return m.showForm(f, scrDashboard, func() tea.Cmd {
		switch choice {
		case "updates":
			return m.openUpdates()
		case "runtime":
			m.screen = scrRuntime
			return m.loadRuntimes()
		case "account":
			return m.openAccount()
		case "alerts":
			return m.alertsForm()
		case "scale":
			return m.scaleForm()
		case "game":
			return m.installGameTask()
		case "doctor":
			m.screen, m.checks = scrDoctor, nil
			return m.loadChecks()
		case "show":
			m.showConfig()
			return nil
		case "edit":
			return m.editConfig()
		case "compose":
			return m.writeCompose()
		case "migrate":
			return m.detectLegacy()
		}
		return nil
	})
}

func (m *model) alertsForm() tea.Cmd {
	app := m.app
	webhook, service := app.Secrets[hostcfg.SecretWebhook], agent.ServiceInstalled()
	oldHook, oldService := webhook, service
	f := huh.NewForm(huh.NewGroup(
		huh.NewNote().Title("Background service").Description("Runs on this host: Prometheus metrics on "+orDash(app.Cfg.Metrics.Listen)+
			", Discord alerts when a server fails, keeps restarting or recovers, and automatic runtime updates."),
		huh.NewConfirm().Title("Run the background service?").Value(&service),
		huh.NewInput().Title("Discord webhook URL").Description("Server Settings → Integrations → Webhooks. Empty turns alerts off. A test message is sent.").Value(&webhook).
			Validate(func(s string) error {
				if s = strings.TrimSpace(s); s == "" || strings.HasPrefix(s, "https://") {
					return nil
				}
				return errors.New("a webhook URL starts with https://")
			}),
	).Title("Alerts and background service"))
	return m.showForm(f, scrDashboard, func() tea.Cmd {
		webhook = strings.TrimSpace(webhook)
		return m.run("Saving alerts and background service", func(ctx context.Context, say func(string)) error {
			if webhook != oldHook {
				if err := agent.SetWebhook(ctx, app, webhook); err != nil {
					return err
				}
				if webhook == "" {
					say("Discord alerts are off")
				} else {
					say("webhook saved; test message sent")
				}
			}
			switch {
			case service && (!oldService || webhook != oldHook):
				if err := agent.InstallService(m.dir); err != nil {
					return err
				}
				say("background service evr-docker-agent installed and running")
			case !service && oldService:
				if err := agent.RemoveService(); err != nil {
					return err
				}
				say("background service removed")
			}
			return nil
		}, nil)
	})
}

func (m *model) scaleForm() tea.Cmd {
	app := m.app
	enabled := 0
	for _, in := range app.Cfg.Instances {
		if !in.Disabled {
			enabled++
		}
	}
	count := strconv.Itoa(enabled)
	f := huh.NewForm(huh.NewGroup(
		huh.NewInput().Title("How many servers should run?").
			Description("New ones get the next free ports; fewer removes idle servers from the end.").Value(&count).Validate(intRange(0, 64)),
	).Title("Number of servers"))
	return m.showForm(f, scrDashboard, func() tea.Cmd {
		n, _ := strconv.Atoi(count)
		return m.run(fmt.Sprintf("Scaling to %d server(s)", n), func(ctx context.Context, say func(string)) error {
			msgs, err := app.Scale(ctx, n)
			for _, s := range msgs {
				say(s)
			}
			if err != nil {
				return err
			}
			if err := app.Save(); err != nil {
				return err
			}
			_, err = app.Apply(ctx, false, say)
			return err
		}, nil)
	})
}

func (m *model) showConfig() {
	var b strings.Builder
	if out, err := yaml.Marshal(m.app.Cfg); err != nil {
		b.WriteString(ui.Error.Render(err.Error()) + "\n")
	} else {
		b.WriteString(ui.Subtle.Render("# "+m.app.Cfg.Path()) + "\n" + string(out))
	}
	for _, k := range []string{config.EnvDiscordID, config.EnvPassword, config.EnvHTTPKey, hostcfg.SecretWebhook} {
		state := "not set"
		if m.app.Secrets[k] != "" {
			state = "set"
		}
		b.WriteString(ui.Subtle.Render(fmt.Sprintf("# secret %s: %s", k, state)) + "\n")
	}
	if m.app.DeviceLinked() {
		b.WriteString(ui.Subtle.Render("# device link: "+filepath.Join(m.dir, core.DeviceFile)) + "\n")
	}
	lines := strings.Split(b.String(), "\n")
	if n := m.height - 6; n > 0 && len(lines) > n {
		lines = append(lines[:n-1], ui.Subtle.Render(fmt.Sprintf("… %d more lines (Edit the configuration file to see all)", len(lines)-n+1)))
	}
	m.screen, m.infoTitle, m.infoText = scrInfo, "configuration", strings.Join(lines, "\n")
}

type editedMsg struct{ err error }

// editConfig opens config.yaml in $EDITOR, then validates and applies it.
func (m *model) editConfig() tea.Cmd {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "nano"
	}
	if _, err := exec.LookPath(editor); err != nil {
		m.setFlash("no editor found: set EDITOR, or install nano", true)
		return nil
	}
	return tea.ExecProcess(exec.Command(editor, filepath.Join(m.dir, "config.yaml")), func(err error) tea.Msg { return editedMsg{err} })
}

func (m *model) afterEdit(msg editedMsg) tea.Cmd {
	if msg.err != nil {
		m.setFlash("editor: "+msg.err.Error(), true)
		return nil
	}
	app, err := core.Open(m.dir)
	if err != nil {
		m.setFlash("the edited config is invalid: "+err.Error(), true)
		return nil
	}
	m.app = app
	return m.run("Applying the edited configuration", func(ctx context.Context, say func(string)) error {
		done, err := app.Apply(ctx, false, say)
		if len(done) == 0 && err == nil {
			say("everything is up to date")
		}
		return err
	}, nil)
}

func (m *model) writeCompose() tea.Cmd {
	app := m.app
	return m.run("Writing a docker-compose file", func(ctx context.Context, say func(string)) error {
		b, err := app.Compose()
		if err != nil {
			return err
		}
		wd, _ := os.Getwd()
		path := filepath.Join(wd, "compose.yml")
		if err := os.WriteFile(path, b, 0o644); err != nil {
			return err
		}
		say("wrote " + path)
		say("It runs the same servers without evr-docker: docker compose -f " + path + " up -d")
		return nil
	}, nil)
}

// Migration from v1.

func (m *model) detectLegacy() tea.Cmd {
	m.setFlash("looking for a v1 installation …", false)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 30*time.Second)
		defer cancel()
		l, err := core.DetectLegacy(ctx, "")
		return legacyMsg{l, err}
	}
}

func legacySummary(l core.Legacy) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Old containers: %d\n", len(l.Containers))
	for _, c := range l.Containers {
		fmt.Fprintf(&b, "  %-20s port %-6d %s\n", c.Name, c.Port, c.State)
	}
	state := "ok"
	if !l.GameOK {
		state = l.GameErr
	}
	fmt.Fprintf(&b, "Game files: %s (%s)\n", orDash(l.GameDir), state)
	acct := "not found (sign in afterwards with c)"
	if l.DiscordID != "" {
		acct = "Discord ID " + l.DiscordID
		if l.Password != "" {
			acct += ", password found"
		}
	}
	fmt.Fprintf(&b, "Account: %s\n", acct)
	if l.CronFile != "" {
		fmt.Fprintf(&b, "The old update cron job in %s is turned off.\n", l.CronFile)
	}
	if l.RepoDir != "" {
		fmt.Fprintf(&b, "Old scripts: %s\n", l.RepoDir)
	}
	if l.GameDir != "" {
		fmt.Fprintf(&b, "Old logs: %s", game.Size(l.LogsSize))
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func (m *model) migrateForm(msg legacyMsg) tea.Cmd {
	switch {
	case msg.err != nil:
		m.setFlash(msg.err.Error(), true)
		return nil
	case !msg.l.Found():
		m.setFlash("No v1 installation found on this host.", false)
		return nil
	}
	m.flash = ""
	gameDir := msg.l.GameDir
	confirm := false
	logs := core.LogsMove
	var remove []string
	var ropts []huh.Option[string]
	what := map[string]string{}
	for _, r := range msg.l.Removable() {
		ropts = append(ropts, huh.NewOption(r.What, r.Key))
		what[r.Key] = r.What
	}
	fields := []huh.Field{
		huh.NewNote().Title("v1 installation found").Description(legacySummary(msg.l)),
		huh.NewInput().Title("Old game directory").Description("The folder with bin/win10/echovr.exe; its files are reused, not copied.").Value(&gameDir),
		huh.NewSelect[string]().Title("Old logs").Options(
			huh.NewOption("Move them to "+filepath.Join(hostcfg.DefaultDataDir, game.V1LogsDir), core.LogsMove),
			huh.NewOption("Keep them in the old game folder", core.LogsKeep),
			huh.NewOption("Delete them", core.LogsDelete),
		).Value(&logs),
	}
	if len(ropts) > 0 {
		fields = append(fields, huh.NewMultiSelect[string]().Title("Also delete, once the new servers run").
			Description("Nothing is deleted unless you pick it here.").Options(ropts...).Height(len(ropts)+5).Value(&remove).
			Validate(func(v []string) error {
				return core.MigrateOptions{Logs: logs, Remove: v}.Validate()
			}))
	}
	f := huh.NewForm(
		huh.NewGroup(fields...).Title("Move a v1 installation"),
		huh.NewGroup(
			huh.NewConfirm().Title("Move it to evr-docker now?").
				Description("The old containers are stopped and removed, and a new server is created for each on the same port.").Value(&confirm),
		),
	)
	back := scrDashboard
	if m.app == nil {
		back = scrForm
	}
	return m.showForm(f, back, func() tea.Cmd {
		if !confirm {
			if m.app == nil {
				m.startWizard()
				return m.form.Init()
			}
			m.setFlash("cancelled", false)
			return nil
		}
		start := func(sure bool) tea.Cmd {
			var app *core.App
			return m.run("Moving the v1 installation", func(ctx context.Context, say func(string)) error {
				l := msg.l
				if gameDir != l.GameDir {
					var err error
					if l, err = core.DetectLegacy(ctx, gameDir); err != nil {
						return err
					}
				}
				opts := core.MigrateOptions{Logs: logs}
				if sure {
					opts.Remove = remove
				}
				a, err := core.Migrate(ctx, m.dir, l, "", "", "", "", opts, func(step, detail string) { say(step + ": " + detail) })
				app = a
				if err == nil {
					say("Done.")
				}
				return err
			}, func() tea.Cmd {
				m.app = app
				return m.loadServers()
			})
		}
		if len(remove) == 0 {
			return start(false)
		}
		var items []string
		for _, k := range remove {
			items = append(items, what[k])
		}
		return m.confirmDelete(items, back, start)
	})
}

// confirmDelete asks once more before parts of the old installation are
// deleted; then runs with whether they may be.
func (m *model) confirmDelete(items []string, back screen, then func(sure bool) tea.Cmd) tea.Cmd {
	sure := false
	f := huh.NewForm(huh.NewGroup(
		huh.NewConfirm().Title("Delete these for sure, once the new servers run?").
			Description("• " + strings.Join(items, "\n• ") + "\nThis cannot be undone.").
			Affirmative("Yes, delete them").Negative("No, keep them").Value(&sure),
	))
	return m.showForm(f, back, func() tea.Cmd { return then(sure) })
}
