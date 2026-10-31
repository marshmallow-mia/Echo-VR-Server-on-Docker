package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/internal/agent"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/internal/core"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/mods"
)

func modsCmd(g *globals) *cobra.Command {
	var server string
	cmd := &cobra.Command{
		Use: "mods", Aliases: []string{"mod"}, Short: "Mods for the servers, from the launcher's catalogues",
		Long: `The servers run mods from the same catalogues as the Echo VR launcher
(release.echovr.de, checked against its signed index). Mods for players' games
only are left out. Without --server a change is for every server (the
defaults); with it, for one server: it adds a mod, leaves one of the defaults
out, pins a version or sets arguments.

A change reaches each server at its next start, after its match. New versions
of the mods come with "evr-docker update apply mods" (or by themselves with
the mods update setting on auto): one idle server tries them first.`,
	}
	cmd.PersistentFlags().StringVar(&server, "server", "", "change one server instead of the defaults")

	// sync writes the locks after an edit and says what each server will run.
	sync := func(g *globals, cmd *cobra.Command, a *core.App) error {
		plans, changed, err := a.SyncMods(cmd.Context(), func(s string) { g.say("%s", s) })
		if err != nil {
			return fmt.Errorf("saved, but the servers' mods couldn't be updated: %w (try again with: evr-docker mods sync)", err)
		}
		for _, p := range plans {
			if server != "" && p.Server != server {
				continue
			}
			for _, pr := range p.Problems {
				g.say("%s: %s left out: %s", p.Server, pr.ID, pr.Why)
			}
		}
		if len(changed) > 0 {
			g.say("%s: new mods from the next start (after the current match; now: evr-docker server restart NAME)", strings.Join(changed, ", "))
		}
		return nil
	}
	edit := func(use, short string, args cobra.PositionalArgs, do func(a *core.App, args []string) error) *cobra.Command {
		return &cobra.Command{
			Use: use, Short: short, Args: args,
			RunE: func(cmd *cobra.Command, args []string) error {
				a, err := g.app()
				if err != nil {
					return err
				}
				if err := do(a, args); err != nil {
					return err
				}
				return sync(g, cmd, a)
			},
		}
	}

	cmd.AddCommand(&cobra.Command{
		Use: "ls", Aliases: []string{"list"}, Short: "The mods each server runs, and the lists they come from",
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := g.app()
			if err != nil {
				return err
			}
			return modsList(g, cmd, a, server)
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use: "search [TEXT]", Short: "Mods in the catalogues that servers can run", Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := g.app()
			if err != nil {
				return err
			}
			f, err := a.ModHost(nil).Source.Fetch(cmd.Context())
			if err != nil {
				return err
			}
			q := ""
			if len(args) == 1 {
				q = strings.ToLower(args[0])
			}
			type row struct {
				ID, Name, Version, Target, Summary string
			}
			var rows []row
			clientOnly := 0
			for id := range f.Catalog.Versions {
				e, err := f.Catalog.Choose(id, "", false)
				if err != nil {
					continue
				}
				if !e.Target.OnServers() {
					clientOnly++
					continue
				}
				if q != "" && !strings.Contains(strings.ToLower(id+" "+e.Name+" "+e.Summary), q) {
					continue
				}
				rows = append(rows, row{id, e.Name, e.Version, string(e.Target), e.Summary})
			}
			sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
			return g.print(rows, func(w io.Writer) {
				var t [][]string
				for _, r := range rows {
					t = append(t, []string{r.ID, r.Name, r.Version, orDash(r.Target), r.Summary})
				}
				g.table("ID\tNAME\tNEWEST\tFOR\tSUMMARY", t)
				if clientOnly > 0 {
					fmt.Fprintf(w, "\n%d mod(s) for players' games only are not shown.\n", clientOnly)
				}
				for _, s := range f.Skipped {
					fmt.Fprintln(w, "skipped: "+s)
				}
			})
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use: "info ID", Short: "A mod's versions, what it needs and its arguments", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := g.app()
			if err != nil {
				return err
			}
			f, err := a.ModHost(nil).Source.Fetch(cmd.Context())
			if err != nil {
				return err
			}
			vs := f.Catalog.Versions[args[0]]
			if len(vs) == 0 {
				return fmt.Errorf("no mod %q in the catalogues (evr-docker mods search)", args[0])
			}
			return g.print(vs, func(w io.Writer) {
				e := vs[0]
				fmt.Fprintf(w, "%s (%s)\n%s\nFor: %s\n", e.Name, e.ID, e.Summary, orDash(string(e.Target)))
				for _, v := range vs {
					var notes []string
					if v.Prerelease {
						notes = append(notes, "prerelease")
					}
					if v.Requires.Nevr != "" {
						notes = append(notes, "nEVR "+v.Requires.Nevr)
					}
					for _, d := range v.Requires.Mods {
						notes = append(notes, "needs "+strings.TrimSpace(d.ID+" "+d.Version))
					}
					if v.Blocked != nil || f.Catalog.Rollback.BlockFor(v) != "" {
						notes = append(notes, "blocked")
					}
					if v.Source != "" && v.Source != "published" {
						notes = append(notes, v.Source)
					}
					fmt.Fprintf(w, "  %s  %s\n", v.Version, strings.Join(notes, ", "))
				}
				if len(e.Args) > 0 {
					fmt.Fprintln(w, "Arguments (set with --arg KEY=VALUE):")
					keys := make([]string, 0, len(e.Args))
					for k := range e.Args {
						keys = append(keys, k)
					}
					sort.Strings(keys)
					for _, k := range keys {
						fmt.Fprintf(w, "  %s = %v\n", k, e.Args[k])
					}
				}
			})
		},
	})

	var argList []string
	var pre bool
	add := edit("add ID[@VERSION]", "Add a mod (or change its version and arguments)", cobra.ExactArgs(1), func(a *core.App, args []string) error {
		id, ver, _ := strings.Cut(args[0], "@")
		s := mods.Spec{ID: id, Version: ver, Prerelease: pre}
		for _, kv := range argList {
			k, v, ok := strings.Cut(kv, "=")
			if !ok || k == "" {
				return fmt.Errorf("--arg %q: use KEY=VALUE", kv)
			}
			if s.Args == nil {
				s.Args = map[string]string{}
			}
			s.Args[k] = v
		}
		return a.AddMod(server, s)
	})
	add.Flags().StringArrayVar(&argList, "arg", nil, "an argument for the mod, KEY=VALUE (repeatable)")
	add.Flags().BoolVar(&pre, "prerelease", false, "run its newest prerelease too")
	cmd.AddCommand(add)
	cmd.AddCommand(edit("rm ID", "Take a mod off (with --server: only that server)", cobra.ExactArgs(1), func(a *core.App, args []string) error {
		return a.RemoveMod(server, args[0])
	}))
	cmd.AddCommand(edit("pin ID [VERSION]", "Keep a mod at a version or range (without VERSION: follow the newest again)", cobra.RangeArgs(1, 2), func(a *core.App, args []string) error {
		ver := ""
		if len(args) == 2 && args[1] != "latest" {
			ver = args[1]
		}
		return a.PinMod(server, args[0], ver)
	}))
	cmd.AddCommand(edit("local on|off", "Also load each server's own DLLs from instances/NAME/mods/local (unchecked)", cobra.ExactArgs(1), func(a *core.App, args []string) error {
		if args[0] != "on" && args[0] != "off" {
			return fmt.Errorf("local on or local off")
		}
		if args[0] == "on" && server != "" {
			dir := filepath.Join(a.Cfg.InstanceDir(server), "mods", "local")
			if err := os.MkdirAll(dir, 0o755); err == nil {
				g.say("put the DLLs into %s", dir)
			}
		}
		return a.SetLocalPlugins(server, args[0] == "on")
	}))
	cmd.AddCommand(edit("sync", "Write each server's mods again (after a catalogue or dev code change)", cobra.NoArgs, func(*core.App, []string) error { return nil }))

	var check bool
	update := &cobra.Command{
		Use: "update", Short: "Move the servers to the newest mod versions (one idle server tries them first)", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := g.app()
			if err != nil {
				return err
			}
			say := func(s string) { g.say("%s", s) }
			c, err := component(agent.Components(a, say), "mods")
			if err != nil {
				return err
			}
			if check {
				st, err := c.Check(cmd.Context())
				if err != nil {
					return err
				}
				return g.print(st, func(w io.Writer) {
					if st.Available {
						fmt.Fprintln(w, "available: "+st.Latest)
					} else {
						fmt.Fprintln(w, "up to date ("+st.Installed+")")
					}
				})
			}
			return c.Apply(cmd.Context(), "", say)
		},
	}
	update.Flags().BoolVar(&check, "check", false, "only say what would change")
	cmd.AddCommand(update)

	codes := &cobra.Command{Use: "dev-code", Short: "Launcher dev codes whose mods go over the published ones"}
	codes.AddCommand(&cobra.Command{
		Use: "ls", Short: "List the dev codes (shortened)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := g.app()
			if err != nil {
				return err
			}
			var short []string
			for _, c := range a.Secrets.DevCodes() {
				short = append(short, mods.NormalizeCode(c)[:4]+"…")
			}
			return g.print(short, func(w io.Writer) {
				if len(short) == 0 {
					fmt.Fprintln(w, "no dev codes")
				}
				for _, s := range short {
					fmt.Fprintln(w, s)
				}
			})
		},
	})
	codes.AddCommand(edit("add CODE", "Add a dev code (gated codes, behind the EchoVRCE sign-in, work only in the launcher)", cobra.ExactArgs(1), func(a *core.App, args []string) error {
		c := mods.NormalizeCode(args[0])
		if c == "" {
			return fmt.Errorf("%q isn't a dev code", args[0])
		}
		have := a.Secrets.DevCodes()
		if slices.ContainsFunc(have, func(x string) bool { return mods.NormalizeCode(x) == c }) {
			return nil
		}
		return a.SetDevCodes(append(have, c))
	}))
	codes.AddCommand(edit("rm CODE", "Remove a dev code (the first letters are enough)", cobra.ExactArgs(1), func(a *core.App, args []string) error {
		want := strings.ToLower(strings.NewReplacer("-", "", " ", "", "…", "").Replace(args[0]))
		var keep []string
		found := false
		for _, c := range a.Secrets.DevCodes() {
			if want != "" && strings.HasPrefix(mods.NormalizeCode(c), want) {
				found = true
				continue
			}
			keep = append(keep, c)
		}
		if !found {
			return fmt.Errorf("no dev code starting with %q", args[0])
		}
		return a.SetDevCodes(keep)
	}))
	cmd.AddCommand(codes)
	return cmd
}

// modsList prints each server's lock and what nEVR made of it.
func modsList(g *globals, cmd *cobra.Command, a *core.App, only string) error {
	rows, err := a.ModRows(cmd.Context(), only)
	if err != nil {
		return err
	}
	return g.print(rows, func(w io.Writer) {
		if len(rows) == 0 {
			fmt.Fprintln(w, "No server runs mods. Add one with: evr-docker mods search, then evr-docker mods add ID")
		} else {
			var t [][]string
			for _, r := range rows {
				t = append(t, []string{r.Server, r.ID, r.Version, r.State})
			}
			g.table("SERVER\tMOD\tVERSION\tSTATE", t)
		}
		fmt.Fprintln(w, "\nFor every server: "+core.SpecList(a.Cfg.Defaults.Mods))
		for _, in := range a.Cfg.Instances {
			if len(in.Settings.Mods) > 0 && (only == "" || in.Name == only) {
				fmt.Fprintf(w, "%s's own: %s\n", in.Name, core.SpecList(in.Settings.Mods))
			}
		}
	})
}
