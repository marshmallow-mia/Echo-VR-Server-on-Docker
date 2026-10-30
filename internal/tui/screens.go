package tui

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/table"
	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"

	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/internal/agent"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/internal/core"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/internal/hostcfg"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/game"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/instance"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/nevr/config"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/status"
	ui "github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/tui"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/update"
)

// showForm opens a huh form; onSubmit runs when it is completed.
func (m *model) showForm(f *huh.Form, back screen, onSubmit func() tea.Cmd) tea.Cmd {
	m.form = f.WithTheme(huh.ThemeFunc(huh.ThemeCharm)).WithWidth(min(max(m.width-4, 40), 90)).WithShowHelp(true)
	m.screen, m.back, m.onSubmit = scrForm, back, onSubmit
	return m.form.Init()
}

func (m *model) updateForm(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyPressMsg); ok && k.String() == "esc" && m.app != nil {
		m.screen, m.form = m.back, nil
		return m, nil
	}
	next, cmd := m.form.Update(msg)
	if f, ok := next.(*huh.Form); ok {
		m.form = f
	}
	switch m.form.State {
	case huh.StateCompleted:
		submit := m.onSubmit
		m.form, m.screen = nil, m.back
		return m, tea.Batch(cmd, submit())
	case huh.StateAborted:
		if m.app == nil {
			return m, tea.Quit
		}
		m.form, m.screen = nil, m.back
	}
	return m, cmd
}

func notEmpty(what string) func(string) error {
	return func(s string) error {
		if strings.TrimSpace(s) == "" {
			return errors.New(what + " is required")
		}
		return nil
	}
}

func digits(s string) error {
	if s == "" || strings.Trim(s, "0123456789") != "" {
		return errors.New("enter digits only")
	}
	return nil
}

func intRange(lo, hi int) func(string) error {
	return func(s string) error {
		n, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil || n < lo || n > hi {
			return fmt.Errorf("enter a number from %d to %d", lo, hi)
		}
		return nil
	}
}

// startWizard opens the first-run setup.
func (m *model) startWizard() {
	an := &core.Answers{}
	an.Game.Source = "mirror"
	an.PublicIP = "auto"
	servers, firstPort, regions := "2", strconv.Itoa(hostcfg.FirstPort), ""
	signIn := "code"
	signInOptions := []huh.Option[string]{
		huh.NewOption("Sign-in code on each server's console (6.9.0+, no password stored)", "code"),
		huh.NewOption("Discord ID and server password", "password"),
	}
	if core.DeviceLinkEnabled() {
		signInOptions = append(signInOptions, huh.NewOption("Device link for the whole host (old, nevr-runtime 5.x only)", "device"))
	}
	bindChoices := []huh.Option[string]{huh.NewOption("all addresses (0.0.0.0)", "")}
	for _, ip := range core.HostIPs() {
		bindChoices = append(bindChoices, huh.NewOption(ip, ip))
	}
	f := huh.NewForm(
		huh.NewGroup(
			huh.NewNote().Title("Welcome to evr-docker").Description(
				"This sets up Echo VR dedicated servers with nevr-runtime on this host:\n"+
					"your EchoVRCE host account, the game files (about 5 GB), the runtime and the servers.\n\n"+
					"Everything can be changed later, here or with the evr-docker command."),
		),
		huh.NewGroup(
			huh.NewSelect[string]().Title("How should the servers sign in?").Options(signInOptions...).Value(&signIn),
			// Broken by hand to fit an 80-column terminal: huh sizes a note before
			// wrapping it and cuts the rest.
			huh.NewNote().Description("Without a password, each server prints a sign-in code on its console\n"+
				"the first time it starts (nevr-runtime 6.9.0+). Approve it at\n"+
				"echovrce.com/device with the host account; the server keeps the\n"+
				"sign-in. The dashboard shows the link meanwhile."),
		).Title("EchoVRCE account"),
		huh.NewGroup(
			huh.NewNote().Description(passwordNote),
			huh.NewInput().Title("Discord user ID").Description("Discord → Settings → Advanced → Developer mode, then copy your ID.").
				Value(&an.Account.DiscordID).Validate(digits),
			huh.NewInput().Title("Server password").Description("The password you set for hosting with the EchoVRCE bot.").
				EchoMode(huh.EchoModePassword).Value(&an.Account.Password).Validate(notEmpty("the password")),
		).Title("EchoVRCE account").WithHideFunc(func() bool { return signIn != "password" }),
		huh.NewGroup(
			huh.NewInput().Title("How many servers?").Value(&servers).Validate(intRange(0, 64)),
			huh.NewInput().Title("First UDP port").Description("Servers use consecutive ports from here. Forward them 1:1 (same numbers) if the host is behind NAT.").
				Value(&firstPort).Validate(intRange(1024, 65000)),
			huh.NewSelect[string]().Title("Publish ports on").Options(bindChoices...).Value(&an.BindIP),
			huh.NewInput().Title("Regions (optional)").Description("Comma-separated, e.g. euw. Leave empty for the backend's default.").Value(&regions),
		).Title("Servers"),
		huh.NewGroup(
			huh.NewSelect[string]().Title("Game files").Options(
				huh.NewOption("Download from the public mirror (files.echovr.de)", "mirror"),
				huh.NewOption("Use an existing game folder on this host", "dir"),
				huh.NewOption("Unpack an archive (.zip, .7z, .tar.*)", "archive"),
				huh.NewOption("Skip for now", "skip"),
			).Value(&an.Game.Source),
			huh.NewInput().Title("Path (folder or archive)").Description("Only needed for folder or archive; the folder must contain bin/win10/echovr.exe.").Value(&an.Game.Path),
		).Title("Game"),
		huh.NewGroup(
			huh.NewConfirm().Title("Start the servers when setup is done?").Value(&an.Start).Affirmative("Yes").Negative("No"),
		),
	)
	an.Start = true
	m.form = f.WithTheme(huh.ThemeFunc(huh.ThemeCharm)).WithShowHelp(true).WithWidth(90)
	m.screen, m.back = scrForm, scrDashboard
	m.onSubmit = func() tea.Cmd {
		if signIn != "password" {
			an.Account.DiscordID, an.Account.Password = "", ""
		}
		an.Account.DeviceLink = signIn == "device"
		an.Servers, _ = strconv.Atoi(servers)
		an.FirstPort, _ = strconv.Atoi(firstPort)
		an.Regions = splitList(regions)
		if (an.Game.Source == "dir" || an.Game.Source == "archive") && an.Game.Path == "" {
			an.Game.Source = "skip"
		}
		var app *core.App
		start := func() tea.Cmd {
			return m.run("Setting up evr-docker", func(ctx context.Context, say func(string)) error {
				a, err := core.Init(ctx, m.dir, *an, func(step, detail string) { say(step + ": " + detail) })
				app = a
				return err
			}, func() tea.Cmd {
				m.app = app
				return m.loadServers()
			})
		}
		m.wizardRetry = start
		return start()
	}
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (m *model) addForm() tea.Cmd {
	count, regions := "1", strings.Join(m.app.Cfg.Defaults.Regions, ",")
	f := huh.NewForm(huh.NewGroup(
		huh.NewInput().Title("Add how many servers?").Description("They get the next free names and ports.").Value(&count).Validate(intRange(1, 32)),
		huh.NewInput().Title("Regions (optional)").Description("Empty uses the defaults.").Value(&regions),
	).Title("Add servers"))
	app := m.app
	return m.showForm(f, scrDashboard, func() tea.Cmd {
		n, _ := strconv.Atoi(count)
		var set instance.Settings
		if r := splitList(regions); strings.Join(r, ",") != strings.Join(app.Cfg.Defaults.Regions, ",") {
			set.Regions = r
		}
		return m.run(fmt.Sprintf("Adding %d server(s)", n), func(ctx context.Context, say func(string)) error {
			added, err := app.AddInstances(n, set)
			if err != nil {
				return err
			}
			for _, in := range added {
				say(fmt.Sprintf("added %s on port %d", in.Name, in.Port))
			}
			if err := app.Save(); err != nil {
				return err
			}
			_, err = app.Apply(ctx, false, say)
			return err
		}, nil)
	})
}

func (m *model) editForm(name string) tea.Cmd {
	in, _ := m.app.Cfg.Instance(name)
	port := strconv.Itoa(in.Port)
	regions, guilds := strings.Join(in.Settings.Regions, ","), strings.Join(in.Settings.Guilds, ",")
	tick := ""
	if in.Settings.TickRate != 0 {
		tick = strconv.Itoa(in.Settings.TickRate)
	}
	runtime, enabled := in.Settings.Runtime, !in.Disabled
	if runtime == "" {
		runtime = "current"
	}
	rtOpts := []huh.Option[string]{huh.NewOption("follow the host's current runtime", "current")}
	if list, err := m.app.Store().List(); err == nil {
		for _, r := range list {
			rtOpts = append(rtOpts, huh.NewOption("pin to "+r.Tag, r.Tag))
		}
	}
	f := huh.NewForm(huh.NewGroup(
		huh.NewInput().Title("UDP port").Value(&port).Validate(intRange(1024, 65535)),
		huh.NewInput().Title("Regions").Description("Comma-separated; empty uses the defaults ("+orDash(strings.Join(m.app.Cfg.Defaults.Regions, ","))+").").Value(&regions),
		huh.NewInput().Title("Guilds").Description("Guild IDs to host for; empty means all guilds the account may host.").Value(&guilds),
		huh.NewInput().Title("Tick rate (Hz)").Description("Empty uses the default ("+strconv.Itoa(m.app.Cfg.Defaults.TickRate)+").").Value(&tick).Validate(func(s string) error {
			if s == "" {
				return nil
			}
			return intRange(10, 1000)(s)
		}),
		huh.NewSelect[string]().Title("nevr-runtime").Options(rtOpts...).Value(&runtime),
		huh.NewConfirm().Title("Enabled").Value(&enabled),
	).Title("Edit " + name))
	app := m.app
	return m.showForm(f, scrDashboard, func() tea.Cmd {
		in, ok := app.Cfg.Instance(name)
		if !ok {
			return nil
		}
		in.Port, _ = strconv.Atoi(port)
		in.Settings.Regions, in.Settings.Guilds = nilIfEmpty(splitList(regions)), nilIfEmpty(splitList(guilds))
		in.Settings.TickRate, _ = strconv.Atoi(tick)
		in.Settings.Runtime = runtime
		if runtime == "current" {
			in.Settings.Runtime = ""
		}
		in.Disabled = !enabled
		return m.run("Updating "+name, func(ctx context.Context, say func(string)) error {
			if err := app.Save(); err != nil {
				return err
			}
			_, err := app.Apply(ctx, false, say)
			return err
		}, nil)
	})
}

func nilIfEmpty(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	return s
}

func installGame(ctx context.Context, app *core.App, say func(string)) error {
	last := -2
	_, err := app.Games(func(p game.Progress) {
		if p.Percent >= 0 && p.Percent == last {
			return
		}
		last = p.Percent
		line := p.Step
		if p.Percent >= 0 {
			line += fmt.Sprintf(" %d%%", p.Percent)
		}
		say(strings.TrimSpace(line + " " + p.Detail))
	}).FromMirror(ctx, "")
	return err
}

// Runtime screen.

func (m *model) loadRuntimes() tea.Cmd {
	app := m.app
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 20*time.Second)
		defer cancel()
		list, err := app.Runtimes(ctx)
		return runtimesMsg{list, err}
	}
}

func (m *model) refreshRuntimes() {
	rows := make([]table.Row, 0, len(m.runtimes))
	for _, r := range m.runtimes {
		state := "available"
		switch {
		case r.Current:
			state = "● current"
		case r.Previous:
			state = "previous (rollback)"
		case r.Installed:
			state = "installed"
		}
		note := r.Blocked
		if r.Bad != "" {
			note = "rollout failed: " + r.Bad
		}
		pub := ""
		if r.Remote != nil {
			pub = r.Remote.Published.Format("2006-01-02")
		}
		rows = append(rows, table.Row{r.Tag, pub, state, note})
	}
	m.rtTable.SetRows(rows)
}

func (m *model) updateRuntime(msg tea.Msg) (tea.Model, tea.Cmd) {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		var cmd tea.Cmd
		m.rtTable, cmd = m.rtTable.Update(msg)
		return m, cmd
	}
	app := m.app
	var sel *core.RuntimeInfo
	if i := m.rtTable.Cursor(); i >= 0 && i < len(m.runtimes) {
		sel = &m.runtimes[i]
	}
	switch k.String() {
	case "esc", "q":
		m.screen = scrDashboard
		return m, m.loadServers()
	case "enter", "i":
		if sel == nil {
			return m, nil
		}
		if sel.Blocked != "" {
			m.setFlash(sel.Tag+": "+sel.Blocked, true)
			return m, nil
		}
		tag := sel.Tag
		return m, m.run("Installing nevr-runtime "+tag, func(ctx context.Context, say func(string)) error {
			say("downloading and verifying " + tag)
			inst, err := app.InstallRuntime(ctx, tag, true)
			if err != nil {
				return err
			}
			say("BugSplat64.dll sha256 " + inst.DLLSHA256)
			say("now current: servers switch to it at their next match boundary")
			return nil
		}, func() tea.Cmd { m.screen = scrRuntime; return m.loadRuntimes() })
	case "x":
		if sel == nil || !sel.Installed {
			return m, nil
		}
		tag := sel.Tag
		m.ask("Delete "+tag+" from this host?", func() tea.Cmd {
			return func() tea.Msg {
				if err := app.Store().Remove(tag); err != nil {
					return flashMsg{err.Error(), true}
				}
				return tea.Batch(func() tea.Msg { return flashMsg{tag + " deleted", false} }, m.loadRuntimes())()
			}
		})
	case "u":
		pol := update.Policy{Channel: app.Cfg.Runtime.Channel, Pinned: app.Cfg.Runtime.Pinned}
		return m, m.run("Checking for a newer nevr-runtime", func(ctx context.Context, say func(string)) error {
			cur, target, available, err := agent.Updater(app, nil).Check(ctx, pol)
			if err != nil {
				return err
			}
			if !available {
				say("up to date (" + cur + ", channel " + pol.Channel + ")")
				return nil
			}
			say(fmt.Sprintf("%s is available (current %s): trying it on an idle server first", target.Tag, cur))
			res, err := agent.Updater(app, say).Run(ctx, pol)
			if err != nil {
				return err
			}
			say(fmt.Sprintf("%s: %s → %s %s", res.Action, res.From, res.To, res.Detail))
			return nil
		}, func() tea.Cmd { m.screen = scrRuntime; return m.loadRuntimes() })
	case "b":
		return m, func() tea.Msg {
			tag, err := app.Store().Rollback()
			if err != nil {
				return flashMsg{err.Error(), true}
			}
			return tea.Batch(func() tea.Msg { return flashMsg{"rolled back to " + tag, false} }, m.loadRuntimes())()
		}
	case "c":
		channel, pinned := app.Cfg.Runtime.Channel, app.Cfg.Runtime.Pinned
		auto := app.Cfg.Updates.Runtime == "auto"
		f := huh.NewForm(huh.NewGroup(
			huh.NewSelect[string]().Title("Update channel").Options(
				huh.NewOption("pre: newest release incl. pre-releases (every 5.x release is one)", "pre"),
				huh.NewOption("stable: full releases only", "stable"),
				huh.NewOption("pinned: stay on one version", "pinned"),
			).Value(&channel),
			huh.NewInput().Title("Pinned version").Description("Only for the pinned channel, e.g. v5.4.2.").Value(&pinned),
			huh.NewConfirm().Title("Update automatically?").Description("Needs the background service (More → Discord alerts and the background service).").Value(&auto),
		).Title("Runtime updates"))
		return m, m.showForm(f, scrRuntime, func() tea.Cmd {
			app.Cfg.Runtime.Channel, app.Cfg.Runtime.Pinned = channel, pinned
			app.Cfg.Updates.Runtime = "notify"
			if auto {
				app.Cfg.Updates.Runtime = "auto"
			}
			if channel != "pinned" {
				app.Cfg.Runtime.Pinned = ""
			}
			return func() tea.Msg {
				if err := app.Save(); err != nil {
					return flashMsg{err.Error(), true}
				}
				return flashMsg{"runtime channel: " + channel, false}
			}
		})
	}
	var cmd tea.Cmd
	m.rtTable, cmd = m.rtTable.Update(msg)
	return m, cmd
}

func (m *model) renderRuntime() string {
	cfg := m.app.Cfg.Runtime
	policy := "channel " + cfg.Channel
	if cfg.Channel == "pinned" {
		policy += " (" + cfg.Pinned + ")"
	}
	auto := m.app.Cfg.Updates.Runtime == "auto"
	if auto && agent.ServiceInstalled() {
		policy += ", automatic updates on"
	} else if auto {
		policy += ", automatic updates need the background service (More → Discord alerts and the background service)"
	} else {
		policy += ", automatic updates off"
	}
	body := m.rtTable.View()
	if m.runtimes == nil {
		body = m.spin.View() + " checking GitHub …"
	}
	return m.header() + "  " + ui.Bold.Render("nevr-runtime") + "\n\n" + body + "\n\n" + ui.Subtle.Render(policy) + "\n" +
		ui.Subtle.Render("Servers pick up a new current version at their next match boundary, so no match is cut short.") + "\n" +
		m.footer(ui.K("enter", "install & use"), ui.K("b", "rollback"), ui.K("x", "delete"), ui.K("u", "check for updates"), ui.K("c", "update settings"), ui.K("esc", "back"))
}

// Account screen.

func (m *model) openAccount() tea.Cmd {
	m.screen, m.account = scrAccount, nil
	return m.loadAccount()
}

func (m *model) loadAccount() tea.Cmd {
	app := m.app
	return func() tea.Msg {
		info := &accountInfo{}
		if !app.HasAccount() {
			info.err = core.ErrNoAccount
			return accountMsg(info)
		}
		ctx, cancel := context.WithTimeout(m.ctx, 20*time.Second)
		defer cancel()
		info.session, info.err = app.Login(ctx)
		if info.err == nil {
			info.servers, _ = app.Auth().MyServers(ctx, info.session)
		}
		return accountMsg(info)
	}
}

func (m *model) updateAccount(msg tea.Msg) (tea.Model, tea.Cmd) {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	switch k.String() {
	case "esc", "q":
		m.screen = scrDashboard
		return m, m.loadServers()
	case "r":
		return m, m.openAccount()
	case "p":
		return m, m.passwordForm()
	case "c":
		if m.app != nil && m.app.Secrets[config.EnvPassword] != "" {
			return m, m.useSignInCode()
		}
	case "l":
		if core.DeviceLinkEnabled() {
			return m, m.linkDevice()
		}
	}
	return m, nil
}

// passwordNote says where the server password comes from.
const passwordNote = "Optional: the host account's Discord ID and the server password you set for hosting\nwith the EchoVRCE bot. Without one, each server shows its own sign-in code (nevr-runtime 6.9.0+)."

// useSignInCode drops the password and recreates the servers.
func (m *model) useSignInCode() tea.Cmd {
	app := m.app
	return m.run("Switching to the console sign-in code", func(ctx context.Context, say func(string)) error {
		return app.UseSignInCode(ctx, say)
	}, func() tea.Cmd { return m.openAccount() })
}

// linkDevice links this host at echovrce.com/device and recreates the servers with it.
func (m *model) linkDevice() tea.Cmd {
	app := m.app
	cmd := m.run("Linking this host at echovrce.com/device", func(ctx context.Context, say func(string)) error {
		say(core.ResetPasswordNote)
		say("")
		_, err := app.UseDeviceLink(ctx, func(url, code string, expires time.Time) {
			say("Open " + url)
			say("and approve the code " + code + " (valid until " + expires.Local().Format(time.TimeOnly) + ")")
			say("waiting …")
		}, say)
		return err
	}, func() tea.Cmd { return m.openAccount() })
	m.busyCanCancel = true
	return cmd
}

func (m *model) passwordForm() tea.Cmd {
	app := m.app
	discord, password := app.Secrets[config.EnvDiscordID], ""
	f := huh.NewForm(huh.NewGroup(
		huh.NewNote().Title("Password sign-in").Description(passwordNote),
		huh.NewInput().Title("Discord user ID").Description("Discord → Settings → Advanced → Developer mode, then copy your ID.").Value(&discord).Validate(digits),
		huh.NewInput().Title("Server password").Description("The password you set for hosting with the EchoVRCE bot.").EchoMode(huh.EchoModePassword).Value(&password).Validate(notEmpty("the password")),
	).Title("EchoVRCE account"))
	return m.showForm(f, scrAccount, func() tea.Cmd {
		return m.run("Signing in", func(ctx context.Context, say func(string)) error {
			_, err := app.UsePassword(ctx, discord, password, say)
			return err
		}, func() tea.Cmd { return m.openAccount() })
	})
}

func (m *model) renderAccount() string {
	linkOn := core.DeviceLinkEnabled()
	var b strings.Builder
	b.WriteString(m.header() + "  " + ui.Bold.Render("account") + "\n\n")
	a := m.account
	switch {
	case a == nil:
		b.WriteString(m.spin.View() + " signing in …\n")
	case errors.Is(a.err, core.ErrSignInByCode):
		b.WriteString("No password stored: " + m.wrap(core.SignInCodeNote, 0) + ".\n\n")
		waiting := 0
		for _, s := range m.servers {
			if st := s.Status; st != nil && st.State == status.StateAwaitingApproval {
				fmt.Fprintf(&b, "  %s waits: %s\n", s.Instance.Name, st.SignInURL)
				waiting++
			}
		}
		if waiting > 0 {
			b.WriteString("\n")
		}
		b.WriteString("Press " + ui.KeyStyle.Render("p") + " to sign in with your Discord ID and server password instead.\n")
		if linkOn {
			b.WriteString("Or press " + ui.KeyStyle.Render("l") + " to link this host at echovrce.com/device (old, nevr-runtime 5.x only).\n")
		}
	case errors.Is(a.err, core.ErrNoAccount):
		b.WriteString(ui.Warning.Render("Not signed in yet: the servers cannot register with EchoVRCE.") + "\n\n")
		b.WriteString("Press " + ui.KeyStyle.Render("p") + " to sign in with your Discord ID and server password.\n")
		b.WriteString(ui.Subtle.Render(m.wrap(passwordNote, 0)) + "\n")
		if linkOn {
			b.WriteString("\nOr press " + ui.KeyStyle.Render("l") + " to link this host at echovrce.com/device (nevr-runtime 5.x only).\n")
			b.WriteString(ui.Subtle.Render(m.wrap(core.ResetPasswordNote, 0)) + "\n")
		}
	case a.err != nil:
		b.WriteString(ui.Error.Render(m.wrap(a.err.Error(), 0)) + "\n\n")
		if linkOn {
			b.WriteString(ui.Subtle.Render("Press p to use a password, or l to link this host again.") + "\n")
		} else {
			b.WriteString(ui.Subtle.Render("Press p to sign in with your Discord ID and server password.") + "\n")
		}
	default:
		method := "device link"
		if m.app.Secrets[config.EnvPassword] != "" {
			method = "password"
		}
		fmt.Fprintf(&b, "Signed in as %s  %s\n\n", ui.Bold.Render(a.session.Username), ui.Subtle.Render(fmt.Sprintf("Discord ID %s, %s", m.app.Secrets[config.EnvDiscordID], method)))
		guilds := a.session.HostGuilds()
		if !a.session.MembershipsKnown {
			b.WriteString(ui.Subtle.Render("Signed in with the device link; "+a.session.HostSummary()+".") + "\n")
		} else if len(guilds) == 0 {
			b.WriteString(ui.Warning.Render("No guild gives this account the server host role: servers register but get no matches.") + "\n")
		} else {
			fmt.Fprintf(&b, "Can host for %d guild(s):\n", len(guilds))
			for _, g := range guilds {
				b.WriteString("  " + g + "\n")
			}
		}
		fmt.Fprintf(&b, "\nThe backend lists %d registered server(s) for this account:\n", len(a.servers))
		for _, s := range a.servers {
			fmt.Fprintf(&b, "  %s  %-12s %-11s players %d\n", s.CreatedAt.Local().Format(time.DateTime), s.Region, s.LobbyType, s.Players)
		}
	}
	keys := []ui.Key{ui.K("p", "password sign-in")}
	if m.app != nil && m.app.Secrets[config.EnvPassword] != "" {
		keys = append(keys, ui.K("c", "console code instead"))
	}
	if linkOn {
		keys = append(keys, ui.K("l", "link this host"))
	}
	b.WriteString(m.footer(append(keys, ui.K("r", "refresh"), ui.K("esc", "back"))...))
	return b.String()
}

// Doctor screen.

func (m *model) loadChecks() tea.Cmd {
	app := m.app
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 30*time.Second)
		defer cancel()
		return checksMsg(core.Doctor(ctx, app))
	}
}

func (m *model) renderDoctor() string {
	body := m.spin.View() + " checking …"
	if m.checks != nil {
		var cs []ui.Check
		for _, c := range m.checks {
			fix := c.Fix
			if key, ok := doctorKeys[c.Name]; ok && strings.HasPrefix(fix, "evr-docker ") {
				fix = key
			}
			cs = append(cs, ui.Check{Name: c.Name, Level: c.Result, Detail: c.Detail, Fix: fix})
		}
		body = ui.Checks(cs)
	}
	return m.header() + "  " + ui.Bold.Render("doctor") + "\n\n" + body + "\n" + m.footer(ui.K("r", "run again"), ui.K("g", "install game"), ui.K("u", "runtime"), ui.K("c", "account"), ui.K("esc", "back"))
}

// doctorKeys replace the CLI fixes of some checks with what to press here.
var doctorKeys = map[string]string{
	"game":    "press g to install the game files",
	"runtime": "press u and install a version",
	"account": "press c to sign in",
}

func (m *model) installGameTask() tea.Cmd {
	app := m.app
	return m.run("Installing the game files", func(ctx context.Context, say func(string)) error {
		return installGame(ctx, app, say)
	}, nil)
}
