package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"charm.land/bubbles/v2/table"
	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"

	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/internal/agent"
	ui "github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/tui"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/updates"
)

type (
	updatesMsg  struct{}
	versionsMsg struct {
		c   updates.Component
		vs  []string
		err error
	}
)

// loadVersions fetches what c can be updated to, then opens the form.
func (m *model) loadVersions(c updates.Component) tea.Cmd {
	m.setFlash("looking up versions of "+c.Title()+" …", false)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, time.Minute)
		defer cancel()
		vs, err := c.Versions(ctx)
		return versionsMsg{c, vs, err}
	}
}

func (m *model) components() []updates.Component { return agent.Components(m.app, nil) }

// openUpdates shows the last results at once and checks again in the background.
func (m *model) openUpdates() tea.Cmd {
	m.screen, m.updChecking = scrUpdates, true
	m.refreshUpdates()
	return m.checkUpdates()
}

func (m *model) checkUpdates() tea.Cmd {
	app := m.app
	m.updChecking = true
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 3*time.Minute)
		defer cancel()
		updates.CheckAll(ctx, app.Dir, agent.Components(app, nil), app.Cfg.Updates.Settings())
		return updatesMsg{}
	}
}

func (m *model) refreshUpdates() {
	rec := updates.LoadRecord(m.app.Dir)
	set := m.app.Cfg.Updates.Settings()
	m.updComps = m.components()
	if m.width > 0 {
		m.updTable.SetWidth(m.width)
	}
	rows := make([]table.Row, 0, len(m.updComps))
	for _, c := range m.updComps {
		mode := set.Mode(c.Name())
		inst, avail, note := "-", "", ""
		if st, ok := rec.Get(c.Name()); ok {
			inst = st.Installed
			switch {
			case st.Error != "":
				avail, note = "?", "check failed: "+st.Error
			case st.Available:
				avail = "● " + st.Latest
			default:
				avail = "up to date"
			}
			if note == "" {
				note = "checked " + st.Checked.Local().Format("Jan 2 15:04")
			}
		} else if mode == updates.Off {
			note = "not checked (off)"
		}
		rows = append(rows, table.Row{c.Title(), inst, avail, string(mode), note})
	}
	m.updTable.SetRows(rows)
}

func (m *model) updateUpdates(msg tea.Msg) (tea.Model, tea.Cmd) {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		var cmd tea.Cmd
		m.updTable, cmd = m.updTable.Update(msg)
		return m, cmd
	}
	switch k.String() {
	case "esc", "q":
		m.screen = scrDashboard
		return m, m.loadServers()
	case "c":
		return m, m.checkUpdates()
	case "s":
		return m, m.updateSettingsForm()
	case "enter":
		if i := m.updTable.Cursor(); i >= 0 && i < len(m.updComps) {
			return m, m.loadVersions(m.updComps[i])
		}
	}
	var cmd tea.Cmd
	m.updTable, cmd = m.updTable.Update(msg)
	return m, cmd
}

// applyForm asks which version to install, then installs it.
func (m *model) applyForm(msg versionsMsg) tea.Cmd {
	c, vs := msg.c, msg.vs
	if msg.err != nil {
		m.setFlash("versions of "+c.Title()+": "+msg.err.Error(), true)
		return nil
	}
	m.flash = ""
	st, _ := updates.LoadRecord(m.app.Dir).Get(c.Name())
	version, ok := "", false
	group := []huh.Field{huh.NewNote().Title(c.Title()).Description(fmt.Sprintf("Installed: %s\nAvailable: %s", st.Installed, orDash(st.Latest)))}
	if len(vs) > 0 {
		opts := []huh.Option[string]{huh.NewOption("the newest ("+orDash(st.Latest)+")", "")}
		for _, v := range vs {
			opts = append(opts, huh.NewOption(v, v))
		}
		group = append(group, huh.NewSelect[string]().Title("Version").Options(opts...).Value(&version).Height(8))
	}
	what := map[string]string{
		"self":    "evr-docker is replaced (checksum-verified, the old one kept as .prev) and restarts; servers move to the new image after their current match.",
		"game":    "Missing or changed files are fetched from the mirror and verified; servers use them from their next restart.",
		"runtime": "The version is tried on one idle server first; only if it registers do the others switch, at their next match boundary.",
	}[c.Name()]
	group = append(group, huh.NewConfirm().Title("Install now?").Description(what).Value(&ok))
	f := huh.NewForm(huh.NewGroup(group...).Title("Update"))
	return m.showForm(f, scrUpdates, func() tea.Cmd {
		if !ok {
			return nil
		}
		app := m.app
		return m.run("Updating "+c.Title(), func(ctx context.Context, say func(string)) error {
			comp := c
			for _, x := range agent.Components(app, say) { // with progress output
				if x.Name() == c.Name() {
					comp = x
				}
			}
			if err := comp.Apply(ctx, version, say); err != nil {
				return err
			}
			updates.CheckOne(ctx, app.Dir, comp)
			return nil
		}, func() tea.Cmd {
			if c.Name() == "self" {
				m.reexec = true
				return tea.Quit
			}
			m.screen = scrUpdates
			m.refreshUpdates()
			return nil
		})
	})
}

func (m *model) updateSettingsForm() tea.Cmd {
	app := m.app
	u := app.Cfg.Updates
	hours := fmt.Sprint(app.Cfg.Updates.Settings().CheckHours)
	if u.CheckHours == 0 {
		hours = "6"
	}
	mode := func(title, desc string, v *string) huh.Field {
		return huh.NewSelect[string]().Title(title).Description(desc).Options(
			huh.NewOption("notify: check, show here and send an alert", "notify"),
			huh.NewOption("auto: install automatically", "auto"),
			huh.NewOption("off: do not check", "off"),
		).Value(v)
	}
	f := huh.NewForm(
		huh.NewGroup(
			mode("evr-docker itself", "", &u.Self),
			mode("Game files", "Repairs missing or changed files from the mirror.", &u.Game),
			mode("nevr-runtime", "Tried on one idle server first, then rolled out at match boundaries.", &u.Runtime),
		).Title("How should each part be updated?"),
		huh.NewGroup(
			huh.NewInput().Title("Check every … hours").Value(&hours).Validate(intRange(1, 168)),
			huh.NewInput().Title("Install automatic updates only between").Description("Local time, e.g. 04:00-06:00. Empty: any time.").Value(&u.Window).Validate(updates.ValidWindow),
			huh.NewNote().Title("Background service").DescriptionFunc(func() string {
				if agent.ServiceInstalled() {
					return "Running: it checks and installs as set here."
				}
				return "Not running: nothing is checked or installed in the background. Turn it on under More → Discord alerts and the background service."
			}, nil),
		),
	)
	return m.showForm(f, scrUpdates, func() tea.Cmd {
		fmt.Sscan(hours, &u.CheckHours)
		app.Cfg.Updates = u
		if err := app.Save(); err != nil {
			m.setFlash(err.Error(), true)
		} else {
			m.setFlash("update settings saved", false)
		}
		m.screen = scrUpdates
		m.refreshUpdates()
		return nil
	})
}

func (m *model) renderUpdates() string {
	body := m.updTable.View()
	status := ""
	if m.updChecking {
		status = m.spin.View() + " checking …"
	} else if rec := updates.LoadRecord(m.app.Dir); !rec.Checked.IsZero() {
		status = ui.Subtle.Render("last check " + rec.Checked.Local().Format("Jan 2 15:04"))
	}
	auto := ui.Subtle.Render("Automatic updates run in the background service.")
	if !agent.ServiceInstalled() {
		auto = ui.Warning.Render("The background service is off: nothing is checked or installed automatically (More → Discord alerts and the background service).")
	}
	return m.header() + "  " + ui.Bold.Render("updates") + "\n\n" + body + "\n\n" + status + "\n" + m.wrap(auto, 0) + "\n" +
		m.footer(ui.K("enter", "update"), ui.K("c", "check now"), ui.K("s", "settings"), ui.K("esc", "back"))
}

// updatesNotice is the dashboard line when updates are waiting.
func (m *model) updatesNotice() string {
	if m.app == nil {
		return ""
	}
	avail := updates.LoadRecord(m.app.Dir).Available()
	if len(avail) == 0 {
		return ""
	}
	return "\n" + ui.Warning.Render(fmt.Sprintf("%d update(s) available: press ", len(avail))) + ui.KeyStyle.Render("U")
}

// reexecIfUpdated starts the new binary after a self-update.
func reexecIfUpdated(m *model) error {
	if !m.reexec {
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return err
	}
	return syscall.Exec(exe, os.Args, os.Environ())
}
