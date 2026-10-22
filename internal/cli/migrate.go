package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/internal/core"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/game"
)

func migrateCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{Use: "migrate", Short: "Move a v1 (bash run.sh) installation to evr-docker"}
	var gameDir, httpKey, image, dataDir, project, logs string
	var remove []string
	detect := &cobra.Command{
		Use: "detect", Short: "Show what a migration would take over",
		RunE: func(cmd *cobra.Command, _ []string) error {
			l, err := core.DetectLegacy(cmd.Context(), gameDir)
			if err != nil {
				return err
			}
			return g.print(l, func(w io.Writer) { printLegacy(w, l) })
		},
	}
	detect.Flags().StringVar(&gameDir, "game-dir", "", "old ready-at-dawn-echo-arena directory (found from the containers if omitted)")
	cmd.AddCommand(detect)
	run := &cobra.Command{
		Use: "run", Short: "Migrate: same account, game files and ports; old containers are removed",
		RunE: func(cmd *cobra.Command, _ []string) error {
			l, err := core.DetectLegacy(cmd.Context(), gameDir)
			if err != nil {
				return err
			}
			if !l.Found() {
				return errors.New("no v1 installation found (try --game-dir)")
			}
			printLegacy(g.out, l)
			opts := core.MigrateOptions{Logs: logs, Remove: remove}
			// Deleting is never implied by --yes: without --remove, ask for each part.
			if !cmd.Flags().Changed("remove") && !g.yes && term.IsTerminal(int(os.Stdin.Fd())) {
				for _, c := range l.Removable() {
					if g.confirm(fmt.Sprintf("After the move, delete %s?", c.What)) {
						opts.Remove = append(opts.Remove, c.Key)
					}
				}
			}
			if err := opts.Validate(); err != nil {
				return err
			}
			if !g.confirm("Replace the old containers with new evr-docker servers on the same ports?") {
				return errors.New("cancelled (use --yes)")
			}
			_, err = core.Migrate(cmd.Context(), g.configDir, l, httpKey, image, dataDir, project, opts, func(step, detail string) { g.say("[%s] %s", step, detail) })
			if err == nil {
				g.say("\nMigrated.")
			}
			return err
		},
	}
	run.Flags().StringVar(&gameDir, "game-dir", "", "old ready-at-dawn-echo-arena directory")
	run.Flags().StringVar(&httpKey, "http-key", "", "backend HTTP key (default: the public key)")
	run.Flags().StringVar(&image, "image", "", "server image to use")
	run.Flags().StringVar(&project, "project", "", "container name prefix (default evr)")
	run.Flags().StringVar(&dataDir, "data-dir", "", "where evr-docker keeps game files and server data (default /srv/echovr)")
	run.Flags().StringVar(&logs, "logs", core.LogsMove, "old logs: move (to <data-dir>/"+game.V1LogsDir+"), keep or delete")
	run.Flags().StringSliceVar(&remove, "remove", nil, "also delete, after the move: scripts, image, game (asked for each when omitted)")
	cmd.AddCommand(run)
	return cmd
}

func printLegacy(w io.Writer, l core.Legacy) {
	if !l.Found() {
		fmt.Fprintln(w, "No v1 installation found.")
		return
	}
	fmt.Fprintf(w, "Old containers: %d\n", len(l.Containers))
	for _, c := range l.Containers {
		fmt.Fprintf(w, "  %-20s port %-6d %s\n", c.Name, c.Port, c.State)
	}
	state := "ok"
	if !l.GameOK {
		state = l.GameErr
	}
	fmt.Fprintf(w, "Game files:     %s (%s)\n", l.GameDir, state)
	acct := "not found"
	if l.DiscordID != "" {
		acct = "Discord ID " + l.DiscordID
		if l.Password != "" {
			acct += ", password found"
		}
	}
	fmt.Fprintf(w, "Account:        %s\n", acct)
	if l.CronFile != "" {
		fmt.Fprintf(w, "Update cron:    %d line(s) in %s will be commented out\n", len(l.CronLines), l.CronFile)
	}
	if l.GameDir != "" {
		fmt.Fprintf(w, "Old logs:       %s (%s)\n", filepath.Join(l.GameDir, "logs"), game.Size(l.LogsSize))
	}
	if l.RepoDir != "" {
		fmt.Fprintf(w, "Old scripts:    %s\n", l.RepoDir)
	}
	for _, img := range l.Images {
		fmt.Fprintf(w, "Old image:      %s (%s)\n", strings.Join(img.Tags, ", "), game.Size(img.Size))
	}
}
