// Package cli is evr-docker's command line. Every action of the TUI has a
// command here; both call into internal/core.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/internal/core"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/internal/hostcfg"
)

// Version is set at build time.
var Version = "dev"

type globals struct {
	configDir string
	json      bool
	yes       bool
	out       io.Writer
}

// TUI is set by main to avoid an import cycle; it runs the interactive UI.
var TUI func(ctx context.Context, dir string) error

// Execute runs the command line.
func Execute() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	g := &globals{out: os.Stdout}
	root := &cobra.Command{
		Use:   "evr-docker",
		Short: "Install and manage Echo VR dedicated servers (nevr-runtime) on Docker",
		Long: `evr-docker installs and runs Echo VR dedicated servers with nevr-runtime in Docker.

Run it without arguments for the interactive TUI, or use the commands below.
Configuration lives in /etc/evr-docker (config.yaml, secrets.env); game files,
runtimes and per-server data in /srv/echovr.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       Version,
		RunE: func(cmd *cobra.Command, args []string) error {
			if TUI == nil || !term.IsTerminal(int(os.Stdout.Fd())) {
				return cmd.Help()
			}
			return TUI(cmd.Context(), g.configDir)
		},
	}
	root.PersistentFlags().StringVar(&g.configDir, "config-dir", hostcfg.Dir(), "configuration directory")
	root.PersistentFlags().BoolVar(&g.json, "json", false, "print machine-readable JSON")
	root.PersistentFlags().BoolVarP(&g.yes, "yes", "y", false, "do not ask for confirmation")
	root.AddCommand(initCmd(g), doctorCmd(g), serverCmd(g), gameCmd(g), runtimeCmd(g), accountCmd(g),
		configCmd(g), exportCmd(g), agentCmd(g), updateCmd(g), migrateCmd(g))
	if err := root.ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}

func (g *globals) app() (*core.App, error) { return core.Open(g.configDir) }

// print writes v as JSON with --json, otherwise calls text.
func (g *globals) print(v any, text func(w io.Writer)) error {
	if g.json {
		enc := json.NewEncoder(g.out)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	}
	text(g.out)
	return nil
}

func (g *globals) table(header string, rows [][]string) {
	tw := tabwriter.NewWriter(g.out, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, header)
	for _, r := range rows {
		fmt.Fprintln(tw, strings.Join(r, "\t"))
	}
	tw.Flush()
}

func (g *globals) confirm(question string) bool {
	if g.yes {
		return true
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return false
	}
	fmt.Fprintf(g.out, "%s [y/N] ", question)
	var a string
	fmt.Scanln(&a)
	return strings.EqualFold(a, "y") || strings.EqualFold(a, "yes")
}

func (g *globals) say(format string, args ...any) {
	if !g.json {
		fmt.Fprintf(g.out, format+"\n", args...)
	}
}

// readSecret reads a value from stdin (piped) or prompts without echo.
func readSecret(prompt string) (string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		b, err := io.ReadAll(io.LimitReader(os.Stdin, 4096))
		return strings.TrimSpace(string(b)), err
	}
	fmt.Fprint(os.Stderr, prompt)
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	return strings.TrimSpace(string(b)), err
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
