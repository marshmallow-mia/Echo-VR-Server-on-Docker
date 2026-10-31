package tui

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"

	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/internal/agent"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/internal/core"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/mods"
	ui "github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/tui"
)

// modCatalogMsg carries the catalogues for the add form.
type modCatalogMsg struct {
	f   mods.Fetched
	err error
}

// modsMenu is More → Server mods.
func (m *model) modsMenu() tea.Cmd {
	choice := "show"
	f := huh.NewForm(huh.NewGroup(
		huh.NewNote().Description("Mods from the Echo VR launcher's catalogues. Mods for players' games only are left out.\n"+
			"A change reaches each server at its next start, after its match."),
		huh.NewSelect[string]().Title("Server mods").Options(
			huh.NewOption("Show what the servers run", "show"),
			huh.NewOption("Add a mod (or change its arguments)", "add"),
			huh.NewOption("Take a mod off", "rm"),
			huh.NewOption("Update the mods now (one idle server tries them first)", "update"),
			huh.NewOption("Each server's own DLLs (unchecked)", "local"),
			huh.NewOption("Dev codes", "codes"),
		).Value(&choice),
	).Title("Server mods"))
	return m.showForm(f, scrDashboard, func() tea.Cmd {
		switch choice {
		case "show":
			return m.showMods()
		case "add":
			return m.loadModCatalog()
		case "rm":
			return m.removeModForm()
		case "update":
			app := m.app
			return m.run("Updating the servers' mods", func(ctx context.Context, say func(string)) error {
				for _, c := range agent.Components(app, say) {
					if c.Name() == "mods" {
						return c.Apply(ctx, "", say)
					}
				}
				return errors.New("no mods part")
			}, m.loadServers)
		case "local":
			return m.localModsForm()
		case "codes":
			return m.devCodesForm()
		}
		return nil
	})
}

func (m *model) showMods() tea.Cmd {
	ctx, cancel := context.WithTimeout(m.ctx, 20*time.Second)
	defer cancel()
	rows, err := m.app.ModRows(ctx, "")
	var b strings.Builder
	if err != nil {
		b.WriteString(ui.Error.Render(err.Error()) + "\n")
	}
	if len(rows) == 0 {
		b.WriteString("No server runs mods yet.\n")
	}
	server := ""
	for _, r := range rows {
		if r.Server != server {
			server = r.Server
			b.WriteString("\n" + ui.Bold.Render(server) + "\n")
		}
		state := r.State
		if strings.HasPrefix(state, "failed") || strings.HasPrefix(state, "left out") {
			state = ui.Error.Render(state)
		}
		fmt.Fprintf(&b, "  %-22s %-12s %s\n", r.ID, r.Version, state)
	}
	b.WriteString("\n" + ui.Subtle.Render("For every server: "+core.SpecList(m.app.Cfg.Defaults.Mods)) + "\n")
	for _, in := range m.app.Cfg.Instances {
		if len(in.Settings.Mods) > 0 {
			b.WriteString(ui.Subtle.Render(in.Name+"'s own: "+core.SpecList(in.Settings.Mods)) + "\n")
		}
	}
	m.screen, m.infoTitle, m.infoText = scrInfo, "server mods", b.String()
	return nil
}

func (m *model) loadModCatalog() tea.Cmd {
	app := m.app
	m.setFlash("reading the mod catalogues…", false)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, time.Minute)
		defer cancel()
		f, err := app.ModHost(nil).Source.Fetch(ctx)
		return modCatalogMsg{f, err}
	}
}

// scopeOptions: every server, or one.
func (m *model) scopeOptions() []huh.Option[string] {
	opts := []huh.Option[string]{huh.NewOption("every server", "")}
	for _, in := range m.app.Cfg.Instances {
		opts = append(opts, huh.NewOption("only "+in.Name, in.Name))
	}
	return opts
}

func (m *model) addModForm(msg modCatalogMsg) tea.Cmd {
	if msg.err != nil {
		m.setFlash("mod catalogues: "+msg.err.Error(), true)
		return nil
	}
	m.flash = ""
	c := msg.f.Catalog
	type item struct {
		e    mods.Entry
		args string
	}
	items := map[string]item{}
	var ids []string
	for id := range c.Versions {
		e, err := c.Choose(id, "", false)
		if err != nil || !e.Target.OnServers() {
			continue
		}
		var kv []string
		for k, v := range e.Args {
			kv = append(kv, fmt.Sprintf("%s=%v", k, v))
		}
		sort.Strings(kv)
		items[id] = item{e, strings.Join(kv, ", ")}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		m.setFlash("the catalogues have no mod for servers", true)
		return nil
	}
	sort.Strings(ids)
	var opts []huh.Option[string]
	for _, id := range ids {
		e := items[id].e
		label := e.Name + " " + e.Version
		if e.Summary != "" {
			label += " — " + e.Summary
		}
		opts = append(opts, huh.NewOption(label, id))
	}
	id, scope, args, version := ids[0], "", "", ""
	f := huh.NewForm(
		huh.NewGroup(huh.NewSelect[string]().Title("Mod").Options(opts...).Value(&id).Height(10)).Title("Add a mod"),
		huh.NewGroup(
			huh.NewSelect[string]().Title("For").Options(m.scopeOptions()...).Value(&scope),
			huh.NewInput().Title("Version (optional)").Description("Empty: the newest. A version keeps it there.").Value(&version),
			huh.NewInput().Title("Arguments (optional)").DescriptionFunc(func() string {
				if a := items[id].args; a != "" {
					return "KEY=VALUE, comma-separated. Its defaults: " + a
				}
				return "KEY=VALUE, comma-separated. It has none by default."
			}, &id).Value(&args).Validate(parseArgsErr),
		).Title("Add a mod"),
	)
	app := m.app
	return m.showForm(f, scrDashboard, func() tea.Cmd {
		spec := mods.Spec{ID: id, Version: strings.TrimSpace(version)}
		spec.Args, _ = parseArgs(args)
		return m.run("Adding "+id, func(ctx context.Context, say func(string)) error {
			if err := app.AddMod(scope, spec); err != nil {
				return err
			}
			return syncMods(ctx, app, scope, say)
		}, m.loadServers)
	})
}

func parseArgs(s string) (map[string]string, error) {
	var out map[string]string
	for _, kv := range strings.Split(s, ",") {
		if kv = strings.TrimSpace(kv); kv == "" {
			continue
		}
		k, v, ok := strings.Cut(kv, "=")
		if !ok || strings.TrimSpace(k) == "" {
			return nil, fmt.Errorf("%q: use KEY=VALUE", kv)
		}
		if out == nil {
			out = map[string]string{}
		}
		out[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return out, nil
}

func parseArgsErr(s string) error { _, err := parseArgs(s); return err }

// syncMods writes the locks after an edit and says what the servers will run.
func syncMods(ctx context.Context, app *core.App, scope string, say func(string)) error {
	plans, changed, err := app.SyncMods(ctx, say)
	if err != nil {
		return fmt.Errorf("saved, but the servers' mods couldn't be updated: %w", err)
	}
	for _, p := range plans {
		if scope != "" && p.Server != scope {
			continue
		}
		for _, pr := range p.Problems {
			say(p.Server + ": " + pr.ID + " left out: " + pr.Why)
		}
	}
	if len(changed) > 0 {
		say(strings.Join(changed, ", ") + ": new mods from the next start, after the current match")
	} else {
		say("nothing changes for the servers")
	}
	return nil
}

func (m *model) removeModForm() tea.Cmd {
	type listed struct{ scope, id string }
	var opts []huh.Option[listed]
	for _, s := range m.app.Cfg.Defaults.Mods {
		opts = append(opts, huh.NewOption(s.ID+" (every server)", listed{"", s.ID}))
		for _, in := range m.app.Cfg.Instances {
			opts = append(opts, huh.NewOption(s.ID+" (off for "+in.Name+" only)", listed{in.Name, s.ID}))
		}
	}
	for _, in := range m.app.Cfg.Instances {
		for _, s := range in.Settings.Mods {
			if s.Enabled == nil || *s.Enabled {
				opts = append(opts, huh.NewOption(s.ID+" ("+in.Name+"'s own)", listed{in.Name, s.ID}))
			}
		}
	}
	if len(opts) == 0 {
		m.setFlash("no mod is listed", false)
		return nil
	}
	var pick listed
	f := huh.NewForm(huh.NewGroup(huh.NewSelect[listed]().Title("Take off").Options(opts...).Value(&pick).Height(10)).Title("Take a mod off"))
	app := m.app
	return m.showForm(f, scrDashboard, func() tea.Cmd {
		return m.run("Taking "+pick.id+" off", func(ctx context.Context, say func(string)) error {
			if err := app.RemoveMod(pick.scope, pick.id); err != nil {
				return err
			}
			return syncMods(ctx, app, pick.scope, say)
		}, m.loadServers)
	})
}

func (m *model) localModsForm() tea.Cmd {
	scope, on := "", false
	f := huh.NewForm(huh.NewGroup(
		huh.NewNote().Description("Each server can also load the DLLs in its own folder, "+m.app.Cfg.InstanceDir("NAME")+"/mods/local.\n"+
			"They are not checked: only use DLLs you trust."),
		huh.NewSelect[string]().Title("For").Options(m.scopeOptions()...).Value(&scope),
		huh.NewConfirm().Title("Load them?").Value(&on),
	).Title("Each server's own DLLs"))
	app := m.app
	return m.showForm(f, scrDashboard, func() tea.Cmd {
		return m.run("Saving", func(ctx context.Context, say func(string)) error {
			if err := app.SetLocalPlugins(scope, on); err != nil {
				return err
			}
			return syncMods(ctx, app, scope, say)
		}, m.loadServers)
	})
}

func (m *model) devCodesForm() tea.Cmd {
	codes := strings.Join(m.app.Secrets.DevCodes(), ", ")
	f := huh.NewForm(huh.NewGroup(
		huh.NewNote().Description("A launcher dev code brings its folder's mods (over the published ones).\n"+
			"Gated codes, behind the EchoVRCE sign-in, work only in the launcher."),
		huh.NewInput().Title("Dev codes").Description("Comma-separated; empty removes them.").EchoMode(huh.EchoModePassword).Value(&codes).
			Validate(func(s string) error {
				for _, c := range splitList(s) {
					if mods.NormalizeCode(c) == "" {
						return fmt.Errorf("%q isn't a dev code", c)
					}
				}
				return nil
			}),
	).Title("Dev codes"))
	app := m.app
	return m.showForm(f, scrDashboard, func() tea.Cmd {
		return m.run("Saving the dev codes", func(ctx context.Context, say func(string)) error {
			if err := app.SetDevCodes(splitList(codes)); err != nil {
				return err
			}
			return syncMods(ctx, app, "", say)
		}, m.loadServers)
	})
}
