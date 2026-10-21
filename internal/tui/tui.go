// Package tui is evr-docker's interactive terminal UI. It only calls into
// internal/core, the same functions the CLI uses.
package tui

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/table"
	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"

	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/internal/core"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/internal/hostcfg"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/nevr/auth"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/supervisor"
	ui "github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/tui"
)

// Run starts the TUI. Without a configuration it opens the setup wizard.
func Run(ctx context.Context, dir string) error {
	m := newModel(ctx, dir)
	_, err := tea.NewProgram(m, tea.WithContext(ctx)).Run()
	if errors.Is(err, tea.ErrProgramKilled) || errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

type screen int

const (
	scrDashboard screen = iota
	scrLogs
	scrForm
	scrBusy
	scrRuntime
	scrAccount
	scrDoctor
	scrInfo
)

type model struct {
	ctx           context.Context
	dir           string
	app           *core.App
	width, height int
	screen        screen

	servers []core.Server
	table   table.Model
	loadErr error

	logs      ui.LogView
	logName   string
	logCancel context.CancelFunc

	form     *huh.Form
	onSubmit func() tea.Cmd
	back     screen

	busyTitle string
	busyLines []string
	busyDone  bool
	busyErr   error
	afterBusy func() tea.Cmd
	// busyRetry is offered with l when a device link failed.
	busyRetry func() tea.Cmd
	// busyCancel stops the task on esc when busyCanCancel is set.
	busyCancel    context.CancelFunc
	busyCanCancel bool
	// wizardRetry runs the setup again with the same answers.
	wizardRetry func() tea.Cmd
	spin        spinner.Model

	infoTitle, infoText string

	runtimes []core.RuntimeInfo
	rtTable  table.Model
	account  *accountInfo
	checks   []core.Check

	confirm     string
	confirmFunc func() tea.Cmd

	flash    string
	flashBad bool
}

type accountInfo struct {
	session auth.Session
	servers []auth.Server
	err     error
}

func newModel(ctx context.Context, dir string) *model {
	m := &model{ctx: ctx, dir: dir, spin: spinner.New(spinner.WithSpinner(spinner.Dot))}
	m.table = table.New(table.WithColumns(serverColumns(100)), table.WithFocused(true), table.WithHeight(10))
	m.table.SetStyles(tableStyles())
	m.rtTable = table.New(table.WithColumns([]table.Column{{Title: "VERSION", Width: 12}, {Title: "PUBLISHED", Width: 12}, {Title: "STATE", Width: 20}, {Title: "NOTE", Width: 50}}), table.WithFocused(true), table.WithHeight(8))
	m.rtTable.SetStyles(tableStyles())
	m.logs = ui.NewLogView(func(s string) bool { return !supervisor.DefaultFilter(supervisor.Clean(s)) })
	app, err := core.Open(dir)
	switch {
	case err == nil:
		m.app = app
	case errors.Is(err, hostcfg.ErrNotInitialized):
		// A v1 installation is offered for migration first.
		dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		l, lerr := core.DetectLegacy(dctx, "")
		cancel()
		if lerr == nil && l.Found() {
			m.migrateForm(legacyMsg{l: l})
		} else {
			m.startWizard()
		}
	default:
		m.loadErr = err
	}
	return m
}

func tableStyles() table.Styles {
	s := table.DefaultStyles()
	s.Header = s.Header.Bold(true).Foreground(ui.Accent).BorderStyle(lipgloss.NormalBorder()).BorderBottom(true)
	s.Selected = s.Selected.Foreground(lipgloss.Color("#FFFFFF")).Background(ui.Accent).Bold(false)
	return s
}

func serverColumns(width int) []table.Column {
	note := max(width-90, 12)
	return []table.Column{
		{Title: "NAME", Width: 12}, {Title: "PORT", Width: 6}, {Title: "STATE", Width: 12}, {Title: "RUNTIME", Width: 8},
		{Title: "REGISTERED AS", Width: 21}, {Title: "MATCHES", Width: 7}, {Title: "RESTARTS", Width: 8}, {Title: "NOTE", Width: note},
	}
}

// Messages.
type (
	tickMsg    struct{}
	serversMsg struct {
		list []core.Server
		err  error
	}
	busyLineMsg struct{ line string }
	busyDoneMsg struct{ err error }
	runtimesMsg struct {
		list []core.RuntimeInfo
		err  error
	}
	checksMsg  []core.Check
	accountMsg *accountInfo
	flashMsg   struct {
		text string
		bad  bool
	}
)

func (m *model) Init() tea.Cmd {
	cmds := []tea.Cmd{m.spin.Tick, tea.RequestWindowSize}
	if m.form != nil {
		cmds = append(cmds, m.form.Init())
	}
	if m.app != nil {
		cmds = append(cmds, m.loadServers())
	}
	return tea.Batch(cmds...)
}

func tick() tea.Cmd { return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return tickMsg{} }) }

func (m *model) loadServers() tea.Cmd {
	app := m.app
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 10*time.Second)
		defer cancel()
		list, err := app.Servers(ctx)
		return serversMsg{list, err}
	}
}

func (m *model) setFlash(text string, bad bool) { m.flash, m.flashBad = text, bad }

// run executes a long task on the busy screen, showing its progress lines.
func (m *model) run(title string, task func(ctx context.Context, say func(string)) error, after func() tea.Cmd) tea.Cmd {
	m.screen, m.busyTitle, m.busyLines, m.busyDone, m.busyErr, m.afterBusy = scrBusy, title, nil, false, nil, after
	m.busyRetry, m.busyCanCancel = nil, false
	ctx, cancel := context.WithCancel(m.ctx)
	m.busyCancel = cancel
	ch := make(chan tea.Msg, 64)
	go func() {
		defer cancel()
		err := task(ctx, func(s string) { ch <- busyLineMsg{s} })
		ch <- busyDoneMsg{err}
		close(ch)
	}()
	return waitChan(ch)
}

func waitChan(ch <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		return chanMsg{msg, ch}
	}
}

type chanMsg struct {
	msg tea.Msg
	ch  <-chan tea.Msg
}

// quick runs a short action and reports the result in the flash line.
func (m *model) quick(done string, f func(ctx context.Context) error) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 2*time.Minute)
		defer cancel()
		if err := f(ctx); err != nil {
			return flashMsg{err.Error(), true}
		}
		return flashMsg{done, false}
	}
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.table.SetColumns(serverColumns(msg.Width))
		m.table.SetWidth(msg.Width)
		m.table.SetHeight(max(msg.Height-12, 4))
		m.rtTable.SetWidth(msg.Width)
		m.rtTable.SetHeight(max(msg.Height-14, 4))
		m.logs.SetSize(msg.Width, msg.Height-4)
		if m.form != nil {
			m.form = m.form.WithWidth(min(msg.Width-4, 90))
		}
		return m, nil
	case chanMsg:
		next, cmd := m.Update(msg.msg)
		return next, tea.Batch(cmd, waitChan(msg.ch))
	case tickMsg:
		if m.app != nil && (m.screen == scrDashboard || m.screen == scrLogs) {
			return m, m.loadServers()
		}
		return m, tick()
	case serversMsg:
		m.servers, m.loadErr = msg.list, msg.err
		m.refreshTable()
		return m, tick()
	case busyLineMsg:
		m.busyLines = append(m.busyLines, msg.line)
		if len(m.busyLines) > 500 {
			m.busyLines = m.busyLines[len(m.busyLines)-500:]
		}
		return m, nil
	case busyDoneMsg:
		m.busyDone, m.busyErr = true, msg.err
		if errors.Is(msg.err, core.ErrDeviceLink) {
			if m.app != nil {
				m.busyRetry = m.linkDevice
			} else {
				m.busyRetry = m.wizardRetry
			}
		}
		return m, nil
	case legacyMsg:
		return m, m.migrateForm(msg)
	case editedMsg:
		return m, m.afterEdit(msg)
	case runtimesMsg:
		m.runtimes = msg.list
		if msg.err != nil {
			m.setFlash("cannot reach GitHub: "+msg.err.Error(), true)
		}
		m.refreshRuntimes()
		return m, nil
	case checksMsg:
		m.checks = msg
		return m, nil
	case accountMsg:
		m.account = msg
		return m, nil
	case flashMsg:
		m.setFlash(msg.text, msg.bad)
		return m, m.loadServers()
	case ui.LogLineMsg:
		if msg.Source == m.logName {
			m.logs.Append(msg.Line)
		}
		return m, nil
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" {
			m.stopLogs()
			return m, tea.Quit
		}
		if m.confirm != "" {
			f := m.confirmFunc
			m.confirm, m.confirmFunc = "", nil
			if msg.String() == "y" || msg.String() == "Y" {
				return m, f()
			}
			m.setFlash("cancelled", false)
			return m, nil
		}
	}

	switch m.screen {
	case scrForm:
		return m.updateForm(msg)
	case scrBusy:
		k, ok := msg.(tea.KeyPressMsg)
		if ok && !m.busyDone && k.String() == "esc" && m.busyCanCancel {
			m.busyCancel()
			return m, nil
		}
		if ok && m.busyDone && m.busyErr != nil && m.busyRetry != nil && k.String() == "l" {
			return m, m.busyRetry()
		}
		if ok && m.busyDone && (k.String() == "enter" || k.String() == "esc" || k.String() == "q") {
			m.screen = scrDashboard
			if m.afterBusy != nil && m.busyErr == nil {
				return m, m.afterBusy()
			}
			if m.app == nil {
				// The setup stopped early: open what it saved, or ask again.
				if app, err := core.Open(m.dir); err == nil {
					m.app = app
				} else {
					m.startWizard()
					return m, m.form.Init()
				}
			}
			return m, m.loadServers()
		}
		return m, nil
	case scrLogs:
		if k, ok := msg.(tea.KeyPressMsg); ok && !m.logs.Editing() && (k.String() == "esc" || k.String() == "q") {
			m.stopLogs()
			m.screen = scrDashboard
			return m, nil
		}
		var cmd tea.Cmd
		m.logs, cmd = m.logs.Update(msg)
		return m, cmd
	case scrRuntime:
		return m.updateRuntime(msg)
	case scrAccount:
		return m.updateAccount(msg)
	case scrDoctor:
		if k, ok := msg.(tea.KeyPressMsg); ok {
			switch k.String() {
			case "esc", "q":
				m.screen = scrDashboard
			case "r":
				m.checks = nil
				return m, m.loadChecks()
			case "g":
				return m, m.installGameTask()
			case "u":
				m.screen = scrRuntime
				return m, m.loadRuntimes()
			case "c":
				return m, m.openAccount()
			}
		}
		return m, nil
	case scrInfo:
		if k, ok := msg.(tea.KeyPressMsg); ok && (k.String() == "esc" || k.String() == "q" || k.String() == "enter") {
			m.screen = scrDashboard
			return m, m.loadServers()
		}
		return m, nil
	}
	return m.updateDashboard(msg)
}

func (m *model) refreshTable() {
	rows := make([]table.Row, 0, len(m.servers))
	for _, s := range m.servers {
		reg, runtime, matches, restarts := "-", "-", "0", "0"
		if st := s.Status; st != nil {
			if st.PublicIP != "" {
				reg = fmt.Sprintf("%s:%d", st.PublicIP, st.Port)
			}
			runtime = orDash(st.RuntimeVersion)
			matches, restarts = strconv.Itoa(st.SessionsTotal), strconv.Itoa(st.Restarts)
		}
		note := s.Drift
		if s.Status != nil && s.Status.LastError != "" && (s.State() == "failed" || s.State() == "backoff") {
			note = s.Status.LastError
		}
		rows = append(rows, table.Row{s.Instance.Name, strconv.Itoa(s.Instance.Port), ui.StateLabel(s.State()), runtime, reg, matches, restarts, note})
	}
	m.table.SetRows(rows)
}

func (m *model) selected() (core.Server, bool) {
	i := m.table.Cursor()
	if i < 0 || i >= len(m.servers) {
		return core.Server{}, false
	}
	return m.servers[i], true
}

func (m *model) updateDashboard(msg tea.Msg) (tea.Model, tea.Cmd) {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		var cmd tea.Cmd
		m.table, cmd = m.table.Update(msg)
		return m, cmd
	}
	if m.app == nil {
		if k.String() == "q" {
			return m, tea.Quit
		}
		return m, nil
	}
	sel, hasSel := m.selected()
	name := sel.Instance.Name
	app := m.app
	switch k.String() {
	case "q":
		return m, tea.Quit
	case "enter", "l":
		if hasSel {
			return m, m.openLogs(name)
		}
	case "s":
		if hasSel {
			m.setFlash("starting "+name+" …", false)
			return m, m.quick(name+" started", func(ctx context.Context) error { return app.Start(ctx, name) })
		}
	case "x":
		if hasSel {
			m.ask(fmt.Sprintf("Stop %s? Players in a match are disconnected.", name), func() tea.Cmd {
				m.setFlash("stopping "+name+" …", false)
				return m.quick(name+" stopped", func(ctx context.Context) error { return app.Stop(ctx, name) })
			})
		}
	case "r":
		if hasSel {
			return m, m.quick(name+": game restarting", func(ctx context.Context) error { return app.Restart(ctx, name, true) })
		}
	case "R":
		if hasSel {
			m.ask(fmt.Sprintf("Restart the %s container?", name), func() tea.Cmd {
				return m.quick(name+" restarted", func(ctx context.Context) error { return app.Restart(ctx, name, false) })
			})
		}
	case "a":
		if !app.HasAccount() {
			m.setFlash("Sign in first: l links this host (no password), p uses a password.", true)
			return m, m.openAccount()
		}
		return m, m.addForm()
	case "m":
		return m, m.menu()
	case "e":
		if hasSel {
			if _, ok := app.Cfg.Instance(name); ok {
				return m, m.editForm(name)
			}
		}
	case "d":
		if hasSel {
			m.ask(fmt.Sprintf("Remove %s from the configuration and delete its container?", name), func() tea.Cmd {
				return m.run("Removing "+name, func(ctx context.Context, say func(string)) error {
					if err := app.RemoveInstance(name, false); err != nil && !strings.Contains(err.Error(), "no server named") {
						return err
					}
					if err := app.Save(); err != nil {
						return err
					}
					_, err := app.Apply(ctx, true, say)
					return err
				}, nil)
			})
		}
	case "A":
		return m, m.run("Applying the configuration", func(ctx context.Context, say func(string)) error {
			done, err := app.Apply(ctx, false, say)
			if len(done) == 0 && err == nil {
				say("everything is up to date")
			}
			return err
		}, nil)
	case "u":
		m.screen = scrRuntime
		return m, m.loadRuntimes()
	case "c":
		return m, m.openAccount()
	case "D":
		m.screen, m.checks = scrDoctor, nil
		return m, m.loadChecks()
	case "g":
		return m, m.installGameTask()
	}
	var cmd tea.Cmd
	m.table, cmd = m.table.Update(msg)
	return m, cmd
}

func (m *model) ask(q string, f func() tea.Cmd) { m.confirm, m.confirmFunc = q, f }

func (m *model) openLogs(name string) tea.Cmd {
	m.stopLogs()
	m.screen, m.logName = scrLogs, name
	m.logs.Reset()
	ctx, cancel := context.WithCancel(m.ctx)
	m.logCancel = cancel
	app := m.app
	ch := make(chan tea.Msg, 256)
	go func() {
		err := app.Logs(ctx, name, 500, true, func(l string) { ch <- ui.LogLineMsg{Source: name, Line: l} })
		if err != nil && ctx.Err() == nil {
			ch <- ui.LogLineMsg{Source: name, Line: "[evr] log stream ended: " + err.Error()}
		}
		close(ch)
	}()
	return waitChan(ch)
}

func (m *model) stopLogs() {
	if m.logCancel != nil {
		m.logCancel()
		m.logCancel = nil
	}
}

func (m *model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	return v
}

func (m *model) header() string {
	title := ui.Title.Render("evr-docker")
	if m.app == nil {
		return title + "  " + ui.Subtle.Render("Echo VR servers with nevr-runtime")
	}
	cfg := m.app.Cfg
	parts := []string{fmt.Sprintf("%d server(s)", len(cfg.Instances)), "runtime " + orDash(m.app.Store().Current())}
	if cfg.PublicIP != "" {
		parts = append(parts, cfg.PublicIP)
	}
	return title + "  " + ui.Subtle.Render(strings.Join(parts, " · "))
}

func (m *model) footer(keys ...ui.Key) string {
	var b strings.Builder
	if m.confirm != "" {
		b.WriteString(ui.Warning.Render(m.confirm) + " " + ui.KeyStyle.Render("[y/N]") + "\n")
	} else if m.flash != "" {
		if m.flashBad {
			b.WriteString(ui.Error.Render(m.flash) + "\n")
		} else {
			b.WriteString(ui.Success.Render(m.flash) + "\n")
		}
	} else {
		b.WriteString("\n")
	}
	b.WriteString(ui.HelpBar(keys...))
	return b.String()
}

func (m *model) render() string {
	switch m.screen {
	case scrForm:
		return m.header() + "\n\n" + m.form.View()
	case scrBusy:
		return m.header() + "\n\n" + m.renderBusy()
	case scrLogs:
		return m.header() + "  " + ui.Bold.Render("logs: "+m.logName) + "\n" + m.logs.View() + "\n" +
			ui.HelpBar(ui.K("esc", "back"), ui.K("f", "follow"), ui.K("a", "all lines"), ui.K("/", "filter"), ui.K("↑↓", "scroll"))
	case scrRuntime:
		return m.renderRuntime()
	case scrAccount:
		return m.renderAccount()
	case scrDoctor:
		return m.renderDoctor()
	case scrInfo:
		return m.header() + "  " + ui.Bold.Render(m.infoTitle) + "\n\n" + m.infoText + "\n" + ui.HelpBar(ui.K("esc", "back"))
	}
	if m.app == nil {
		msg := "Not set up."
		if m.loadErr != nil {
			msg = ui.Error.Render(m.loadErr.Error())
		}
		return m.header() + "\n\n" + msg + "\n\n" + ui.HelpBar(ui.K("q", "quit"))
	}
	body := m.table.View()
	if len(m.servers) == 0 {
		body = ui.Subtle.Render("No servers yet. Press ") + ui.KeyStyle.Render("a") + ui.Subtle.Render(" to add some.")
	}
	if m.loadErr != nil {
		body += "\n" + ui.Error.Render(m.loadErr.Error())
	}
	detail := ""
	if s, ok := m.selected(); ok && s.Status != nil && s.Status.LastError != "" {
		detail = "\n" + ui.Subtle.Render(ui.Truncate("last error: "+s.Status.LastError, m.width-2))
	}
	if !m.app.HasAccount() {
		detail += "\n" + ui.Warning.Render("The servers have no EchoVRCE sign-in yet: press ") + ui.KeyStyle.Render("c") + ui.Warning.Render(" to link this host.")
	}
	return m.header() + "\n\n" + body + detail + "\n" + m.footer(
		ui.K("enter", "logs"), ui.K("s", "start"), ui.K("x", "stop"), ui.K("r/R", "restart"),
		ui.K("a", "add"), ui.K("e", "edit"), ui.K("d", "delete"), ui.K("A", "apply"),
		ui.K("u", "runtime"), ui.K("c", "account"), ui.K("m", "more"), ui.K("q", "quit"))
}

func (m *model) renderBusy() string {
	var b strings.Builder
	cancelled := errors.Is(m.busyErr, context.Canceled)
	head := m.spin.View() + " " + ui.Bold.Render(m.busyTitle)
	switch {
	case m.busyDone && cancelled:
		head = ui.Warning.Render("✘ " + m.busyTitle + ": cancelled")
	case m.busyDone && m.busyErr != nil:
		head = ui.Error.Render("✘ " + m.busyTitle + " failed")
	case m.busyDone:
		head = ui.Success.Render("✔ " + m.busyTitle)
	}
	b.WriteString(head + "\n\n")
	var lines []string
	for _, l := range m.busyLines {
		lines = append(lines, strings.Split(m.wrap(l, 4), "\n")...)
	}
	if n := m.height - 9; n > 0 && len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	for _, l := range lines {
		b.WriteString("  " + l + "\n")
	}
	if m.busyErr != nil && !cancelled {
		b.WriteString("\n" + ui.Error.Render(m.wrap(m.busyErr.Error(), 0)) + "\n")
	}
	switch {
	case m.busyDone && m.busyErr != nil && m.busyRetry != nil:
		b.WriteString("\n" + ui.HelpBar(ui.K("l", "link again"), ui.K("enter", "continue")))
	case m.busyDone:
		b.WriteString("\n" + ui.HelpBar(ui.K("enter", "continue")))
	case m.busyCanCancel:
		b.WriteString("\n" + ui.HelpBar(ui.K("esc", "cancel")))
	}
	return b.String()
}

// wrap breaks s to the terminal width minus indent.
func (m *model) wrap(s string, indent int) string {
	w := m.width - indent
	if w < 20 {
		w = 76
	}
	return lipgloss.NewStyle().Width(w).Render(s)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
