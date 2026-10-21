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

	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/internal/core"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/internal/hostcfg"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/game"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/instance"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/nevr/config"
	ui "github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/tui"
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
	signIn := "device"
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
			huh.NewSelect[string]().Title("How should the servers sign in?").Options(
				huh.NewOption("Device link at echovrce.com/device (recommended, no password)", "device"),
				huh.NewOption("Discord ID and server password", "password"),
			).Value(&signIn),
			huh.NewNote().Title("Device link").Description(core.ResetPasswordNote+"\nThe code to approve is shown during setup."),
		).Title("EchoVRCE account"),
		huh.NewGroup(
			huh.NewInput().Title("Discord user ID").Description("Discord → Settings → Advanced → Developer mode, then copy your ID.").
				Value(&an.Account.DiscordID).Validate(digits),
			huh.NewInput().Title("Server password").Description("The password you set for hosting with the EchoVRCE bot.").
				EchoMode(huh.EchoModePassword).Value(&an.Account.Password).Validate(notEmpty("the password")),
		).Title("Password sign-in").WithHideFunc(func() bool { return signIn != "password" }),
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
		if signIn == "device" {
			an.Account.DiscordID, an.Account.Password = "", ""
		}
		an.Servers, _ = strconv.Atoi(servers)
		an.FirstPort, _ = strconv.Atoi(firstPort)
		an.Regions = splitList(regions)
		if (an.Game.Source == "dir" || an.Game.Source == "archive") && an.Game.Path == "" {
			an.Game.Source = "skip"
		}
		var app *core.App
		return m.run("Setting up evr-docker", func(ctx context.Context, say func(string)) error {
			a, err := core.Init(ctx, m.dir, *an, func(step, detail string) { say(step + ": " + detail) })
			app = a
			return err
		}, func() tea.Cmd {
			m.app = app
			return m.loadServers()
		})
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
		auto := app.Cfg.Runtime.AutoUpdate
		f := huh.NewForm(huh.NewGroup(
			huh.NewSelect[string]().Title("Update channel").Options(
				huh.NewOption("pre: newest release incl. pre-releases (every 5.x release is one)", "pre"),
				huh.NewOption("stable: full releases only", "stable"),
				huh.NewOption("pinned: stay on one version", "pinned"),
			).Value(&channel),
			huh.NewInput().Title("Pinned version").Description("Only for the pinned channel, e.g. v5.4.2.").Value(&pinned),
			huh.NewConfirm().Title("Update automatically (evr-docker agent)?").Value(&auto),
		).Title("Runtime updates"))
		return m, m.showForm(f, scrRuntime, func() tea.Cmd {
			app.Cfg.Runtime.Channel, app.Cfg.Runtime.Pinned, app.Cfg.Runtime.AutoUpdate = channel, pinned, auto
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
	if cfg.AutoUpdate {
		policy += ", automatic updates on"
	} else {
		policy += ", automatic updates off"
	}
	body := m.rtTable.View()
	if m.runtimes == nil {
		body = m.spin.View() + " checking GitHub …"
	}
	return m.header() + "  " + ui.Bold.Render("nevr-runtime") + "\n\n" + body + "\n\n" + ui.Subtle.Render(policy) + "\n" +
		ui.Subtle.Render("Servers pick up a new current version at their next match boundary, so no match is cut short.") + "\n" +
		m.footer(ui.K("enter", "install & use"), ui.K("b", "rollback"), ui.K("c", "channel"), ui.K("esc", "back"))
}

// Account screen.

func (m *model) loadAccount() tea.Cmd {
	app := m.app
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 20*time.Second)
		defer cancel()
		info := &accountInfo{}
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
	app := m.app
	switch k.String() {
	case "esc", "q":
		m.screen = scrDashboard
	case "r":
		m.account = nil
		return m, m.loadAccount()
	case "p":
		discord, password, key := app.Secrets[config.EnvDiscordID], "", app.Secrets[config.EnvHTTPKey]
		f := huh.NewForm(huh.NewGroup(
			huh.NewInput().Title("Discord user ID").Value(&discord).Validate(digits),
			huh.NewInput().Title("Server password").Description("Leave empty to keep the current one.").EchoMode(huh.EchoModePassword).Value(&password),
		).Title("EchoVRCE account"))
		return m, m.showForm(f, scrAccount, func() tea.Cmd {
			old := map[string]string{}
			for k, v := range app.Secrets {
				old[k] = v
			}
			app.Secrets[config.EnvDiscordID] = discord
			if key != "" {
				app.Secrets[config.EnvHTTPKey] = key
			}
			if password != "" {
				app.Secrets[config.EnvPassword] = password
			}
			return m.run("Checking the account", func(ctx context.Context, say func(string)) error {
				s, err := app.Login(ctx)
				if err != nil {
					for k, v := range old {
						app.Secrets[k] = v
					}
					return err
				}
				say(fmt.Sprintf("signed in as %s, %s", s.Username, s.HostSummary()))
				if err := app.Secrets.Save(app.Dir); err != nil {
					return err
				}
				say("saved; recreating servers with the new account")
				_, err = app.Apply(ctx, false, say)
				return err
			}, func() tea.Cmd { m.screen, m.account = scrAccount, nil; return m.loadAccount() })
		})
	case "l":
		return m, m.run("Linking at echovrce.com/device", func(ctx context.Context, say func(string)) error {
			say(core.ResetPasswordNote)
			s, err := app.LinkDevice(ctx, func(url, code string, expires time.Time) {
				say("Open " + url)
				say("and approve the code " + code + " (valid until " + expires.Local().Format(time.TimeOnly) + ")")
				say("waiting …")
			})
			if err != nil {
				return err
			}
			say(fmt.Sprintf("linked as %s; link given to %d server(s) (apply the configuration to restart them with it)", s.Username, len(app.Cfg.Instances)))
			return nil
		}, func() tea.Cmd { m.screen, m.account = scrAccount, nil; return m.loadAccount() })
	}
	return m, nil
}

func (m *model) renderAccount() string {
	var b strings.Builder
	b.WriteString(m.header() + "  " + ui.Bold.Render("account") + "\n\n")
	a := m.account
	switch {
	case a == nil:
		b.WriteString(m.spin.View() + " signing in …\n")
	case a.err != nil:
		b.WriteString(ui.Error.Render(a.err.Error()) + "\n")
	default:
		fmt.Fprintf(&b, "Signed in as %s  %s\n\n", ui.Bold.Render(a.session.Username), ui.Subtle.Render(a.session.UserID))
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
	b.WriteString("\n" + ui.Subtle.Render("Sign in without a password with l (device link).") + "\n")
	b.WriteString(m.footer(ui.K("p", "change account"), ui.K("l", "link device"), ui.K("r", "refresh"), ui.K("esc", "back")))
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
			cs = append(cs, ui.Check{Name: c.Name, Level: c.Result, Detail: c.Detail, Fix: c.Fix})
		}
		body = ui.Checks(cs)
	}
	return m.header() + "  " + ui.Bold.Render("doctor") + "\n\n" + body + "\n" + m.footer(ui.K("r", "run again"), ui.K("g", "(dashboard) install game"), ui.K("esc", "back"))
}
