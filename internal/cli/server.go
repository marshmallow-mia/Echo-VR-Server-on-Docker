package cli

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/internal/core"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/internal/hostcfg"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/pkg/instance"
)

// settingsFlags binds instance settings to flags; only changed flags apply.
type settingsFlags struct {
	regions, guilds, runtime, lock string
	tick, ttl                      int
	cmd                            *cobra.Command
}

func addSettingsFlags(cmd *cobra.Command) *settingsFlags {
	f := &settingsFlags{cmd: cmd}
	cmd.Flags().StringVar(&f.regions, "regions", "", "comma-separated regions (empty string clears)")
	cmd.Flags().StringVar(&f.guilds, "guilds", "", "comma-separated guild IDs to host for (empty string clears)")
	cmd.Flags().StringVar(&f.runtime, "runtime", "", `nevr-runtime tag or "current"`)
	cmd.Flags().StringVar(&f.lock, "publisher-lock", "", "publisher lock (default echovrce)")
	cmd.Flags().IntVar(&f.tick, "tick-rate", 0, "server tick rate in Hz (10-1000)")
	cmd.Flags().IntVar(&f.ttl, "empty-ttl", 0, "seconds an empty server waits before returning to the lobby")
	return f
}

func (f *settingsFlags) apply(s *instance.Settings) {
	fl := f.cmd.Flags()
	if fl.Changed("regions") {
		s.Regions = splitList(f.regions)
		if s.Regions == nil {
			s.Regions = []string{}
		}
	}
	if fl.Changed("guilds") {
		s.Guilds = splitList(f.guilds)
		if s.Guilds == nil {
			s.Guilds = []string{}
		}
	}
	if fl.Changed("runtime") {
		s.Runtime = f.runtime
	}
	if fl.Changed("publisher-lock") {
		s.PublisherLock = f.lock
	}
	if fl.Changed("tick-rate") {
		s.TickRate = f.tick
	}
	if fl.Changed("empty-ttl") {
		s.EmptyTTL = f.ttl
	}
}

func serverCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{Use: "server", Aliases: []string{"servers", "s"}, Short: "List, create and control game servers"}
	cmd.AddCommand(&cobra.Command{
		Use: "ls", Aliases: []string{"list"}, Short: "List servers and their state",
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := g.app()
			if err != nil {
				return err
			}
			servers, err := a.Servers(cmd.Context())
			if err != nil {
				return err
			}
			type row struct {
				Name, State, Runtime, PublicIP, Session, Drift string
				Port, Restarts, Sessions                       int
			}
			var rows []row
			for _, s := range servers {
				r := row{Name: s.Instance.Name, Port: s.Instance.Port, State: s.State(), Drift: s.Drift}
				if s.Status != nil {
					r.Runtime, r.Session = s.Status.RuntimeVersion, s.Status.Session
					if s.Status.PublicIP != "" {
						r.PublicIP = fmt.Sprintf("%s:%d", s.Status.PublicIP, s.Status.Port)
					}
					r.Restarts, r.Sessions = s.Status.Restarts, s.Status.SessionsTotal
				}
				rows = append(rows, r)
			}
			return g.print(rows, func(w io.Writer) {
				if len(rows) == 0 {
					fmt.Fprintln(w, "No servers configured. Add some with: evr-docker server create --count 2")
					return
				}
				var t [][]string
				for _, r := range rows {
					note := r.Drift
					if note != "" {
						note = "needs apply: " + note
					}
					t = append(t, []string{r.Name, strconv.Itoa(r.Port), r.State, r.Runtime, r.PublicIP, strconv.Itoa(r.Sessions), strconv.Itoa(r.Restarts), note})
				}
				g.table("NAME\tPORT\tSTATE\tRUNTIME\tREGISTERED AS\tMATCHES\tRESTARTS\tNOTE", t)
			})
		},
	})

	var cName string
	var cPort, cCount int
	var cNoStart bool
	create := &cobra.Command{
		Use: "create", Short: "Add servers (on the next free ports unless --port is given) and start them",
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := g.app()
			if err != nil {
				return err
			}
			var set instance.Settings
			createSettings.apply(&set)
			if cName != "" || cPort != 0 {
				if cCount > 1 {
					return errors.New("--name/--port create one server; drop --count")
				}
				name, port := a.Cfg.NextFree()
				if cName != "" {
					name = cName
				}
				if cPort != 0 {
					port = cPort
				}
				a.Cfg.Instances = append(a.Cfg.Instances, hostcfg.Instance{Name: name, Port: port, Settings: set})
				if err := a.Cfg.Validate(); err != nil {
					return err
				}
				g.say("added %s on port %d", name, port)
			} else {
				added, err := a.AddInstances(cCount, set)
				if err != nil {
					return err
				}
				for _, in := range added {
					g.say("added %s on port %d", in.Name, in.Port)
				}
			}
			if err := a.Save(); err != nil {
				return err
			}
			if cNoStart {
				g.say("run `evr-docker server apply` to start them")
				return nil
			}
			_, err = a.Apply(cmd.Context(), false, func(s string) { g.say("%s", s) })
			return err
		},
	}
	create.Flags().StringVar(&cName, "name", "", "server name (default echo-N)")
	create.Flags().IntVar(&cPort, "port", 0, "UDP game port (default: next free from 6792)")
	create.Flags().IntVarP(&cCount, "count", "n", 1, "number of servers to add")
	create.Flags().BoolVar(&cNoStart, "no-start", false, "only add to the config")
	createSettings = addSettingsFlags(create)
	cmd.AddCommand(create)

	var purge bool
	rm := &cobra.Command{
		Use: "rm NAME...", Aliases: []string{"remove", "delete"}, Short: "Remove servers", Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := g.app()
			if err != nil {
				return err
			}
			if !g.confirm(fmt.Sprintf("Remove %s?", strings.Join(args, ", "))) {
				return errors.New("cancelled (use --yes)")
			}
			for _, n := range args {
				if err := a.RemoveInstance(n, purge); err != nil {
					return err
				}
			}
			if err := a.Save(); err != nil {
				return err
			}
			_, err = a.Apply(cmd.Context(), true, func(s string) { g.say("%s", s) })
			return err
		},
	}
	rm.Flags().BoolVar(&purge, "purge", false, "also delete the server's data (Wine prefix, logs, credentials)")
	cmd.AddCommand(rm)

	var force bool
	apply := &cobra.Command{
		Use: "apply", Short: "Create, update or remove containers to match the configuration",
		Long: "Servers that are hosting a match are updated after the match unless --force is given.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := g.app()
			if err != nil {
				return err
			}
			done, err := a.Apply(cmd.Context(), force, func(s string) { g.say("%s", s) })
			if len(done) == 0 && err == nil {
				g.say("everything is up to date")
			}
			return err
		},
	}
	apply.Flags().BoolVar(&force, "force", false, "also recreate servers that are hosting a match")
	cmd.AddCommand(apply)

	var all, soft bool
	power := func(use, short string, f func(a *core.App, cmd *cobra.Command, name string) error) *cobra.Command {
		c := &cobra.Command{
			Use: use + " [NAME...]", Short: short,
			RunE: func(cmd *cobra.Command, args []string) error {
				a, err := g.app()
				if err != nil {
					return err
				}
				if all {
					args = nil
					for _, in := range a.Cfg.Instances {
						args = append(args, in.Name)
					}
				}
				if len(args) == 0 {
					return errors.New("name a server or use --all")
				}
				var errs []error
				for _, n := range args {
					if err := f(a, cmd, n); err != nil {
						errs = append(errs, fmt.Errorf("%s: %w", n, err))
					} else {
						g.say("%s: %s", n, map[string]string{"start": "started", "stop": "stopped", "restart": "restarting"}[use])
					}
				}
				return errors.Join(errs...)
			},
		}
		c.Flags().BoolVar(&all, "all", false, "all servers")
		return c
	}
	cmd.AddCommand(power("start", "Start servers", func(a *core.App, cmd *cobra.Command, n string) error { return a.Start(cmd.Context(), n) }))
	cmd.AddCommand(power("stop", "Stop servers (players in a match are disconnected)", func(a *core.App, cmd *cobra.Command, n string) error {
		return a.Stop(cmd.Context(), n)
	}))
	restart := power("restart", "Restart servers", func(a *core.App, cmd *cobra.Command, n string) error { return a.Restart(cmd.Context(), n, soft) })
	restart.Flags().BoolVar(&soft, "soft", false, "only restart the game process, keep the container")
	cmd.AddCommand(restart)

	var follow bool
	var tail int
	logs := &cobra.Command{
		Use: "logs NAME", Short: "Show a server's console output", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := g.app()
			if err != nil {
				return err
			}
			return a.Logs(cmd.Context(), args[0], tail, follow, func(l string) { fmt.Fprintln(g.out, l) })
		},
	}
	logs.Flags().BoolVarP(&follow, "follow", "f", false, "keep printing new lines")
	logs.Flags().IntVar(&tail, "tail", 200, "lines from the end to show (-1 for all)")
	cmd.AddCommand(logs)

	cmd.AddCommand(&cobra.Command{
		Use: "status NAME", Short: "Show one server's status", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := g.app()
			if err != nil {
				return err
			}
			s, err := a.Server(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return g.print(s, func(w io.Writer) {
				fmt.Fprintf(w, "Name:      %s\nPort:      %d (UDP+TCP)\nState:     %s\n", s.Instance.Name, s.Instance.Port, s.State())
				if s.Container != nil {
					fmt.Fprintf(w, "Container: %s (%s)\n", s.Container.Name, s.Container.Status)
				}
				if st := s.Status; st != nil {
					fmt.Fprintf(w, "Since:     %s\nRuntime:   %s\nRegistered: %s\nMatches:   %d\nRestarts:  %d\n",
						st.Since.Local().Format(time.DateTime), st.RuntimeVersion, orDash(st.PublicIP), st.SessionsTotal, st.Restarts)
					if st.LastError != "" {
						fmt.Fprintf(w, "Last error: %s (%s)\n", st.LastError, st.Reason)
					}
				}
				set := s.Settings
				fmt.Fprintf(w, "Regions:   %s\nGuilds:    %s\nTick rate: %d Hz\n", orDash(strings.Join(set.Regions, ",")), orDash(strings.Join(set.Guilds, ",")), set.TickRate)
				if s.Drift != "" {
					fmt.Fprintf(w, "Note:      %s; run `evr-docker server apply`\n", s.Drift)
				}
			})
		},
	})

	var enable, disable bool
	edit := &cobra.Command{
		Use: "edit NAME", Short: "Change one server's settings", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := g.app()
			if err != nil {
				return err
			}
			in, ok := a.Cfg.Instance(args[0])
			if !ok {
				return fmt.Errorf("no server named %q", args[0])
			}
			editSettings.apply(&in.Settings)
			if cmd.Flags().Changed("port") {
				in.Port = editPort
			}
			if enable {
				in.Disabled = false
			}
			if disable {
				in.Disabled = true
			}
			if err := a.Save(); err != nil {
				return err
			}
			_, err = a.Apply(cmd.Context(), false, func(s string) { g.say("%s", s) })
			return err
		},
	}
	edit.Flags().IntVar(&editPort, "port", 0, "new UDP game port")
	edit.Flags().BoolVar(&enable, "enable", false, "enable the server")
	edit.Flags().BoolVar(&disable, "disable", false, "disable (stop) the server but keep its settings")
	editSettings = addSettingsFlags(edit)
	cmd.AddCommand(edit)

	cmd.AddCommand(&cobra.Command{
		Use: "scale N", Short: "Keep exactly N servers (removes idle ones from the end)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			n, err := strconv.Atoi(args[0])
			if err != nil {
				return err
			}
			a, err := g.app()
			if err != nil {
				return err
			}
			msgs, err := a.Scale(cmd.Context(), n)
			for _, m := range msgs {
				g.say("%s", m)
			}
			if err != nil {
				return err
			}
			if err := a.Save(); err != nil {
				return err
			}
			_, err = a.Apply(cmd.Context(), false, func(s string) { g.say("%s", s) })
			return err
		},
	})
	return cmd
}

var (
	createSettings, editSettings *settingsFlags
	editPort                     int
)

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
