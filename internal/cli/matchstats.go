package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/internal/core"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/match"
)

// switchSpec describes an on/off switch of a server's match stats.
type switchSpec struct {
	name, short, long, label string
	get                      func(a *core.App, ctx context.Context, name string) (bool, string, error)
	set                      func(a *core.App, ctx context.Context, name string, on bool) error
}

func apiCmd(g *globals) *cobra.Command {
	return switchCmd(g, switchSpec{
		name:  "api",
		short: "Switch match stats (the game's session API) on or off",
		long: "Match stats come from the game's HTTP API on 127.0.0.1 inside each container (never reachable from outside);\n" +
			"the supervisor writes them to match.json and match-history.json in the server's directory.\n" +
			"It is on by default. An idle server restarts its game to apply a change; one in a match applies it after the match.",
		label: "match stats",
		get:   (*core.App).SessionAPI,
		set:   (*core.App).SetSessionAPI,
	})
}

func telemetryCmd(g *globals) *cobra.Command {
	return switchCmd(g, switchSpec{
		name:  "telemetry",
		short: "Switch nevr-runtime's telemetry stream to the supervisor on or off",
		long: "With telemetry on, nevr-runtime streams the match to the supervisor on 127.0.0.1 inside the container\n" +
			"(never to anyone else). The supervisor reports the connection; the play-by-play comes from the session API,\n" +
			"because nevr-runtime 6.16.4 sends most player stat events wrong (EVR_TELEMETRY_PLAYS=1 uses them anyway).\n" +
			"It is on by default. An idle server restarts its game to apply a change; one in a match applies it after the match.",
		label: "telemetry",
		get:   (*core.App).Telemetry,
		set:   (*core.App).SetTelemetry,
	})
}

func switchCmd(g *globals, sp switchSpec) *cobra.Command {
	var all bool
	c := &cobra.Command{
		Use:   sp.name + " on|off|status [NAME...]",
		Short: sp.short,
		Long:  sp.long,
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := g.app()
			if err != nil {
				return err
			}
			action, names := strings.ToLower(args[0]), args[1:]
			if action != "on" && action != "off" && action != "status" {
				return fmt.Errorf("unknown action %q: use on, off or status", args[0])
			}
			if all || (action == "status" && len(names) == 0) {
				names = nil
				for _, in := range a.Cfg.Instances {
					names = append(names, in.Name)
				}
			}
			if len(names) == 0 {
				return errors.New("name a server or use --all")
			}
			if action == "status" {
				type row struct {
					Name, Setting, Running string
				}
				var rows []row
				for _, n := range names {
					set, run, err := sp.get(a, cmd.Context(), n)
					if err != nil {
						return err
					}
					rows = append(rows, row{n, onOff(set), run})
				}
				return g.print(rows, func(w io.Writer) {
					var t [][]string
					for _, r := range rows {
						note := ""
						if r.Running != "" && r.Running != r.Setting {
							note = "the game picks it up at its next start"
						}
						t = append(t, []string{r.Name, r.Setting, orDash(r.Running), note})
					}
					g.table("NAME\tSETTING\tRUNNING GAME\tNOTE", t)
				})
			}
			var errs []error
			for _, n := range names {
				if err := sp.set(a, cmd.Context(), n, action == "on"); err != nil {
					errs = append(errs, fmt.Errorf("%s: %w", n, err))
					continue
				}
				g.say("%s: %s %s (an idle server restarts its game now, one in a match after it)", n, sp.label, action)
			}
			return errors.Join(errs...)
		},
	}
	c.Flags().BoolVar(&all, "all", false, "all servers")
	return c
}

func matchCmd(g *globals) *cobra.Command {
	var history int
	c := &cobra.Command{
		Use: "match NAME", Short: "Show a server's current or last match (match stats)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := g.app()
			if err != nil {
				return err
			}
			m, ok := a.Match(args[0])
			hist, err := a.MatchHistory(args[0])
			if err != nil {
				return err
			}
			if history >= 0 && len(hist) > history {
				hist = hist[:history]
			}
			out := struct {
				Match   *match.Match    `json:"match"`
				History []match.Summary `json:"history"`
			}{History: hist}
			if ok {
				out.Match = &m
			}
			return g.print(out, func(w io.Writer) {
				if !ok {
					fmt.Fprintln(w, "No match yet (or the server's image is older than match stats).")
				} else {
					match.Fprint(w, m)
				}
				match.FprintHistory(w, hist)
			})
		},
	}
	c.Flags().IntVar(&history, "history", 10, "finished matches to list (-1 for all)")
	return c
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}
