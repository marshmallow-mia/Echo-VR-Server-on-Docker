package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/internal/core"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/internal/hostcfg"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/pkg/game"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/pkg/nevr/auth"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/pkg/nevr/config"
)

func initCmd(g *globals) *cobra.Command {
	var answersFile, discordID, httpKey, gameSource, gamePath, regions, guilds string
	var an core.Answers
	var passwordStdin bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Set up evr-docker: account, game files, runtime and servers",
		Long: `Sets up everything in one go. Without flags in a terminal, the TUI wizard opens.
For unattended installs pass --answers FILE (secrets may be ${ENV} references) or flags.

Example:
  echo "$PASSWORD" | evr-docker init --discord-id 1234 --password-stdin --http-key KEY --servers 2 --start`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if answersFile == "" && cmd.Flags().NFlag() == 0 && TUI != nil && isTerminal() {
				return TUI(cmd.Context(), g.configDir)
			}
			if answersFile != "" {
				var err error
				if an, err = core.LoadAnswers(answersFile); err != nil {
					return err
				}
			}
			fl := cmd.Flags()
			if fl.Changed("discord-id") {
				an.Account.DiscordID = discordID
			}
			if fl.Changed("http-key") {
				an.Account.HTTPKey = httpKey
			}
			if passwordStdin {
				p, err := readSecret("EchoVRCE server password: ")
				if err != nil {
					return err
				}
				an.Account.Password = p
			}
			if fl.Changed("game") {
				an.Game.Source = gameSource
			}
			if fl.Changed("game-path") {
				an.Game.Path = gamePath
			}
			if fl.Changed("regions") {
				an.Regions = splitList(regions)
			}
			if fl.Changed("guilds") {
				an.Guilds = splitList(guilds)
			}
			if fl.Changed("servers") || answersFile == "" {
				an.Servers = initServers
			}
			if fl.Changed("start") || answersFile == "" {
				an.Start = initStart
			}
			_, err := core.Init(cmd.Context(), g.configDir, an, func(step, detail string) { g.say("[%s] %s", step, detail) })
			if err == nil {
				g.say("\nAll set. Check the servers with: evr-docker server ls")
			}
			return err
		},
	}
	f := cmd.Flags()
	f.StringVar(&answersFile, "answers", "", "YAML answers file for an unattended setup")
	f.StringVar(&discordID, "discord-id", "", "your Discord user ID (the EchoVRCE host account)")
	f.BoolVar(&passwordStdin, "password-stdin", false, "read the server password from stdin")
	f.StringVar(&httpKey, "http-key", "", "EchoVRCE backend HTTP key")
	f.StringVar(&an.DataDir, "data-dir", "", "where game files and server data go (default /srv/echovr)")
	f.StringVar(&an.Project, "project", "", "container name prefix (default evr; only for several setups on one host)")
	f.StringVar(&an.Image, "image", "", "server image (default "+hostcfg.DefaultImage+")")
	f.StringVar(&an.BindIP, "bind-ip", "", "host address to publish game ports on (default: all)")
	f.StringVar(&an.PublicIP, "public-ip", "auto", `expected public IP ("auto" detects it)`)
	f.IntVar(&initServers, "servers", 1, "number of servers")
	f.IntVar(&an.FirstPort, "first-port", hostcfg.FirstPort, "first UDP game port")
	f.StringVar(&regions, "regions", "", "comma-separated regions")
	f.StringVar(&guilds, "guilds", "", "comma-separated guild IDs to host for (default: all hostable)")
	f.StringVar(&gameSource, "game", "mirror", "game source: mirror, dir, archive or skip")
	f.StringVar(&gamePath, "game-path", "", "directory or archive for --game dir/archive")
	f.StringVar(&an.Game.Mirror, "mirror", "", "rsync mirror (default "+game.DefaultMirror+")")
	f.StringVar(&an.Runtime.Channel, "channel", "", "runtime channel: pre, stable or pinned")
	f.StringVar(&an.Runtime.Pinned, "pin", "", "runtime tag when --channel pinned")
	f.BoolVar(&initStart, "start", true, "start the servers at the end")
	return cmd
}

var (
	initServers int
	initStart   bool
)

func isTerminal() bool {
	fi, err := os.Stdout.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func doctorCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use: "doctor", Short: "Check the host, account, game files and ports",
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := g.app()
			if err != nil && !errors.Is(err, hostcfg.ErrNotInitialized) {
				return err
			}
			checks := core.Doctor(cmd.Context(), a)
			failed := false
			for _, c := range checks {
				failed = failed || c.Level == core.Fail
			}
			err = g.print(checks, func(w io.Writer) {
				for _, c := range checks {
					mark := map[core.Level]string{core.OK: "✔", core.Warn: "!", core.Fail: "✘"}[c.Level]
					fmt.Fprintf(w, "%s %-14s %s\n", mark, c.Name, c.Detail)
					if c.Fix != "" && c.Level != core.OK {
						fmt.Fprintf(w, "  %-14s → %s\n", "", c.Fix)
					}
				}
			})
			if err == nil && failed {
				return errors.New("some checks failed")
			}
			return err
		},
	}
}

func gameCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{Use: "game", Short: "Install and check the Echo VR game files"}
	var fromDir, archive, mirror string
	install := &cobra.Command{
		Use: "install", Short: "Download the game (rsync mirror) or import it from a directory or archive",
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := g.app()
			if err != nil {
				return err
			}
			last := ""
			in := a.Games(func(p game.Progress) {
				line := p.Step
				if p.Percent >= 0 {
					line += fmt.Sprintf(" %3d%%", p.Percent)
				}
				if p.Detail != "" {
					line += "  " + p.Detail
				}
				if line != last && !g.json {
					fmt.Fprintf(g.out, "\r\033[K%s", line)
					last = line
				}
			})
			var info game.Info
			switch {
			case fromDir != "":
				info, err = in.FromDir(cmd.Context(), fromDir)
			case archive != "":
				info, err = in.FromArchive(cmd.Context(), archive)
			default:
				info, err = in.FromMirror(cmd.Context(), mirror)
			}
			g.say("")
			if err != nil {
				return err
			}
			return g.print(info, func(w io.Writer) { fmt.Fprintf(w, "Installed build %s at %s\n", info.Build.Version, info.Dir) })
		},
	}
	install.Flags().StringVar(&fromDir, "from-dir", "", "import an existing game directory (hard-linked when possible)")
	install.Flags().StringVar(&archive, "archive", "", "import a .zip, .7z or .tar.* archive")
	install.Flags().StringVar(&mirror, "mirror", "", "rsync mirror (default "+game.DefaultMirror+")")
	cmd.AddCommand(install)
	cmd.AddCommand(&cobra.Command{
		Use: "status", Aliases: []string{"verify"}, Short: "Verify the installed game files",
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := g.app()
			if err != nil {
				return err
			}
			info, err := a.Games(nil).Current()
			if err != nil {
				return err
			}
			return g.print(info, func(w io.Writer) {
				fmt.Fprintf(w, "Build %s at %s\necho exe sha256 %s\n", info.Build.Version, info.Dir, info.ExeSHA256)
				if len(info.LegacyFiles) > 0 {
					fmt.Fprintf(w, "Legacy loader files (ignored by the servers): %s\n", strings.Join(info.LegacyFiles, ", "))
				}
			})
		},
	})
	return cmd
}

func runtimeCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{Use: "runtime", Short: "Manage nevr-runtime versions"}
	cmd.AddCommand(&cobra.Command{
		Use: "ls", Aliases: []string{"list"}, Short: "List available and installed versions",
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := g.app()
			if err != nil {
				return err
			}
			rts, lerr := a.Runtimes(cmd.Context())
			if lerr != nil {
				g.say("warning: cannot reach GitHub: %v", lerr)
			}
			return g.print(rts, func(w io.Writer) {
				var rows [][]string
				for _, r := range rts {
					state := ""
					switch {
					case r.Current:
						state = "current"
					case r.Previous:
						state = "previous (rollback)"
					case r.Installed:
						state = "installed"
					}
					note := r.Blocked
					if r.Bad != "" {
						note = "rollout failed: " + r.Bad
					}
					published := ""
					if r.Remote != nil {
						published = r.Remote.Published.Format("2006-01-02")
					}
					rows = append(rows, []string{r.Tag, published, state, note})
				}
				g.table("VERSION\tPUBLISHED\tSTATE\tNOTE", rows)
				fmt.Fprintf(w, "\nChannel: %s", a.Cfg.Runtime.Channel)
				if a.Cfg.Runtime.Channel == "pinned" {
					fmt.Fprintf(w, " (%s)", a.Cfg.Runtime.Pinned)
				}
				fmt.Fprintf(w, ", auto update %t\n", a.Cfg.Runtime.AutoUpdate)
			})
		},
	})
	var use bool
	install := &cobra.Command{
		Use: "install [TAG]", Short: "Download and verify a version (default: what the channel wants)", Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := g.app()
			if err != nil {
				return err
			}
			tag := ""
			if len(args) == 1 {
				tag = args[0]
			}
			inst, err := a.InstallRuntime(cmd.Context(), tag, use)
			if err != nil {
				return err
			}
			g.say("installed nevr-runtime %s (BugSplat64.dll sha256 %s)", inst.Tag, inst.DLLSHA256)
			if use {
				g.say("servers switch to it at their next match boundary")
			}
			return nil
		},
	}
	install.Flags().BoolVar(&use, "use", false, "make it the current version")
	cmd.AddCommand(install)
	cmd.AddCommand(&cobra.Command{
		Use: "use TAG", Short: "Switch servers to an installed version (at their next match boundary)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := g.app()
			if err != nil {
				return err
			}
			if err := a.Store().Use(args[0]); err != nil {
				return err
			}
			g.say("nevr-runtime %s is now current; servers switch after their current match", args[0])
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use: "rollback", Short: "Switch back to the previous version",
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := g.app()
			if err != nil {
				return err
			}
			tag, err := a.Store().Rollback()
			if err == nil {
				g.say("rolled back to nevr-runtime %s", tag)
			}
			return err
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use: "channel pre|stable|pinned [TAG]", Short: "Choose which versions updates follow", Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := g.app()
			if err != nil {
				return err
			}
			a.Cfg.Runtime.Channel, a.Cfg.Runtime.Pinned = args[0], ""
			if len(args) == 2 {
				a.Cfg.Runtime.Pinned = args[1]
			}
			if err := a.Save(); err != nil {
				return err
			}
			g.say("runtime channel set to %s", args[0])
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use: "remove TAG", Short: "Delete an installed version that is not in use", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := g.app()
			if err != nil {
				return err
			}
			return a.Store().Remove(args[0])
		},
	})
	return cmd
}

func accountCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{Use: "account", Short: "EchoVRCE host account: set, test, link and list registered servers"}
	var discordID, httpKey string
	var passwordStdin bool
	set := &cobra.Command{
		Use: "set", Short: "Store the Discord ID, server password and HTTP key",
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := g.app()
			if err != nil {
				return err
			}
			if discordID != "" {
				a.Secrets[config.EnvDiscordID] = discordID
			}
			if httpKey != "" {
				a.Secrets[config.EnvHTTPKey] = httpKey
			}
			if passwordStdin || a.Secrets[config.EnvPassword] == "" {
				p, err := readSecret("EchoVRCE server password: ")
				if err != nil {
					return err
				}
				a.Secrets[config.EnvPassword] = p
			}
			s, err := a.Login(cmd.Context())
			if err != nil {
				return err
			}
			if err := a.Secrets.Save(a.Dir); err != nil {
				return err
			}
			g.say("saved; signed in as %s, host role in %d guild(s). Run `evr-docker server apply` to restart servers with it.", s.Username, len(s.HostGuilds()))
			return nil
		},
	}
	set.Flags().StringVar(&discordID, "discord-id", "", "Discord user ID")
	set.Flags().StringVar(&httpKey, "http-key", "", "backend HTTP key")
	set.Flags().BoolVar(&passwordStdin, "password-stdin", false, "read the password from stdin")
	cmd.AddCommand(set)

	cmd.AddCommand(&cobra.Command{
		Use: "test", Short: "Sign in and show which guilds the account can host for",
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := g.app()
			if err != nil {
				return err
			}
			s, err := a.Login(cmd.Context())
			if err != nil {
				return err
			}
			out := map[string]any{"username": s.Username, "user_id": s.UserID, "host_guilds": s.HostGuilds()}
			return g.print(out, func(w io.Writer) {
				fmt.Fprintf(w, "Signed in as %s (%s)\n", s.Username, s.UserID)
				if len(s.HostGuilds()) == 0 {
					fmt.Fprintln(w, "This account has the server host role in no guild: servers register but get no matches.")
					return
				}
				fmt.Fprintln(w, "Can host for guilds:")
				for _, gid := range s.HostGuilds() {
					fmt.Fprintln(w, "  "+gid)
				}
			})
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use: "servers", Short: "List the servers the backend has registered for this account",
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := g.app()
			if err != nil {
				return err
			}
			s, err := a.Login(cmd.Context())
			if err != nil {
				return err
			}
			list, err := a.Auth().MyServers(cmd.Context(), s)
			if err != nil {
				return err
			}
			return g.print(list, func(w io.Writer) {
				var rows [][]string
				for _, sv := range list {
					rows = append(rows, []string{sv.CreatedAt.Local().Format(time.DateTime), sv.Region, sv.LobbyType, strconv.Itoa(sv.Players), sv.VersionLock})
				}
				g.table("REGISTERED\tREGION\tLOBBY\tPLAYERS\tVERSION LOCK", rows)
				fmt.Fprintf(w, "\n%d server(s) registered (the backend hides their addresses).\n", len(list))
			})
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use: "link", Short: "Link this host at echovrce.com/device (optional; stores a refresh token per server)",
		Long: `Runs the echovrce.com device-code flow and stores the refresh token in every
server's _local/.credentials.json. Servers then sign in to ServerDB with it.
The password is still required: nevr-runtime 5.4.2 needs it for the login connection.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := g.app()
			if err != nil {
				return err
			}
			c := a.Auth()
			code, err := c.RequestDevice(cmd.Context())
			if err != nil {
				return err
			}
			g.say("Open %s and approve the code %s (valid until %s).", code.URL(), code.Code, code.Expires.Local().Format(time.TimeOnly))
			ctx, cancel := context.WithDeadline(cmd.Context(), code.Expires)
			defer cancel()
			linked, err := c.WaitDevice(ctx, code, 0)
			if err != nil {
				return err
			}
			for _, in := range a.Cfg.Instances {
				p := a.Cfg.InstanceDir(in.Name) + "/root/_local/.credentials.json"
				if err := auth.WriteCredentials(p, linked); err != nil {
					return err
				}
				_ = chownR(a.Cfg.InstanceDir(in.Name)+"/root", a.Cfg.UID)
			}
			g.say("Linked as %s; credentials stored for %d server(s).", linked.Session.Username, len(a.Cfg.Instances))
			return nil
		},
	})
	return cmd
}

func chownR(dir string, uid int) error {
	return exec.Command("chown", "-R", fmt.Sprintf("%d:%d", uid, uid), dir).Run()
}

func configCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{Use: "config", Short: "Show or edit the configuration"}
	cmd.AddCommand(&cobra.Command{
		Use: "show", Short: "Print the configuration (secrets are not shown)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := g.app()
			if err != nil {
				return err
			}
			if g.json {
				return g.print(a.Cfg, nil)
			}
			b, err := yaml.Marshal(a.Cfg)
			if err != nil {
				return err
			}
			fmt.Fprintf(g.out, "# %s\n%s", a.Cfg.Path(), b)
			for _, k := range []string{config.EnvDiscordID, config.EnvPassword, config.EnvHTTPKey, hostcfg.SecretWebhook} {
				state := "not set"
				if a.Secrets[k] != "" {
					state = "set"
				}
				fmt.Fprintf(g.out, "# secret %s: %s\n", k, state)
			}
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use: "edit", Short: "Open config.yaml in $EDITOR and validate it",
		RunE: func(cmd *cobra.Command, _ []string) error {
			path := g.configDir + "/config.yaml"
			editor := os.Getenv("EDITOR")
			if editor == "" {
				editor = "nano"
			}
			c := exec.Command(editor, path)
			c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
			if err := c.Run(); err != nil {
				return err
			}
			if _, err := hostcfg.Load(g.configDir); err != nil {
				return fmt.Errorf("the edited config is invalid: %w", err)
			}
			g.say("config ok; run `evr-docker server apply` to apply it")
			return nil
		},
	})
	return cmd
}

func exportCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{Use: "export", Short: "Export the setup for use without evr-docker"}
	var out string
	compose := &cobra.Command{
		Use: "compose", Short: "Write a docker-compose file equivalent to the running setup",
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := g.app()
			if err != nil {
				return err
			}
			b, err := a.Compose()
			if err != nil {
				return err
			}
			if out == "" || out == "-" {
				_, err = g.out.Write(b)
				return err
			}
			if err := os.WriteFile(out, b, 0o644); err != nil {
				return err
			}
			g.say("wrote %s", out)
			return nil
		},
	}
	compose.Flags().StringVarP(&out, "output", "o", "-", "file to write")
	cmd.AddCommand(compose)
	return cmd
}
