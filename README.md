# Echo VR Server on Docker

Run Echo VR dedicated servers with [nevr-runtime](https://github.com/EchoTools/nevr-runtime) on any Linux host with Docker.
`evr-docker` installs and manages them: a guided setup, a live dashboard in the terminal, and a command
for everything the dashboard does.

> Looking for Pterodactyl or Pelican? See [pterodactyl_Echo_Server](https://github.com/marshmallow-mia/pterodactyl_Echo_Server).
> It uses the same image and runtime handling.

## Quick start

On a Debian/Ubuntu host with Docker (`curl -fsSL https://get.docker.com | sh`) and `rsync`:

```sh
curl -fsSL https://github.com/marshmallow-mia/Echo-VR-Server-on-Docker/releases/latest/download/install.sh | sudo sh
```

The installer downloads a checksum-verified `evr-docker` binary to `/usr/local/bin` and opens the setup wizard. The wizard asks for:

- your **EchoVRCE host account**: your Discord user ID and the server password you set with the EchoVRCE bot;
- how many servers to run and their first UDP port (default 6792);
- where the game files come from: the public mirror (about 5 GB), a folder that is already on the host, or an archive.

It then checks the account, downloads and verifies the game and the runtime, and starts the servers.
Run `evr-docker` again at any time to open the dashboard.

Unattended:

```sh
echo "$PASSWORD" | sudo evr-docker init --discord-id 123456789012345678 --password-stdin \
    --servers 2 --regions euw
# or: sudo evr-docker init --answers answers.yaml   (secrets may be ${ENV} references)
```

## How it works

```
/srv/echovr/
  game/34.4.631547.1/      the game, verified, shared read-only by all servers
  game/current → …
  nevr/v5.4.2/             nevr-runtime (BugSplat64.dll), checksum-verified
  nevr/current → v5.4.2    switched by updates; servers follow at their next match
  instances/<name>/        each server's own files: Wine prefix, config, logs, status.json
/etc/evr-docker/config.yaml  settings (edit by hand or with evr-docker)
/etc/evr-docker/secrets.env  account and webhook (mode 0600)
```

- **One game copy for every server.** Each container mounts it read-only and gets a small writable view of its own (a
  "symlink farm"): its own `_local/config.yaml`, credentials, logs and Wine prefix.
- **`evr-supervisor`** runs inside each container and handles the runtime's quirks:
  - It writes the runtime config and the port. nevr-runtime 5.4.2 ignores `-port`, so the port goes into a per-server
    `netconfig_dedicatedserver.json`.
  - It starts the game under Wine and follows its log. It restarts the game after every match, because the runtime
    exits when a match ends.
  - It detects rejected logins, unreachable ports and stuck registrations.
  - It handles the runtime's shutdown hang (nevr-runtime #723): a stop takes about 5 seconds and the server unregisters cleanly.
- **Ports are published 1:1**, and the game port must also be forwarded 1:1 (same number outside and inside). The
  backend registers a server at the address it connects from and the port the game bound; a forwarded port with a
  different number would point players at the wrong place.

The measured runtime behaviour this is built on is documented in [docs/nevr-5.4.2-findings.md](docs/nevr-5.4.2-findings.md).

## Dashboard

`sudo evr-docker` opens the dashboard: every server with its state, the address it registered as, its runtime version,
and how many matches and restarts it has had.

| Key | Action |
|---|---|
| `enter` | live console (`f` follow, `a` show runtime noise, `/` filter) |
| `s` / `x` | start / stop |
| `r` / `R` | restart only the game (fast) / restart the container |
| `a` / `e` / `d` | add servers / edit one (port, regions, guilds, tick rate, runtime pin, on/off) / delete |
| `A` | apply the configuration (after editing `config.yaml` by hand) |
| `u` | runtime versions: install, switch, roll back, update channel |
| `c` | account: host guilds, servers the backend has registered, change account, link device |
| `D` | doctor: checks of Docker, disk, game, runtime, account, ports, public IP |

## Commands

Everything in the dashboard is also a command. Add `--json` for scripts and `-y` to skip confirmations.

```
evr-docker init                       guided setup (or --answers / flags for unattended)
evr-docker doctor                     check everything and print fixes
evr-docker server ls|status NAME      state of the servers
evr-docker server create -n 2 [--regions euw]
evr-docker server edit NAME --tick-rate 90 --regions euw,default [--disable|--enable] [--port N]
evr-docker server start|stop|restart [--soft] NAME|--all
evr-docker server logs NAME [-f]
evr-docker server scale N             keep exactly N servers (removes idle ones from the end)
evr-docker server apply [--force]     make containers match config.yaml
evr-docker server rm NAME [--purge]
evr-docker game install [--from-dir DIR | --archive FILE] / game status
evr-docker runtime ls|install [TAG] [--use]|use TAG|rollback|channel pre|stable|pinned [TAG]
evr-docker account set|test|servers|link
evr-docker agent install|run|webhook URL   metrics, Discord alerts, automatic updates
evr-docker update check|apply
evr-docker migrate detect|run         take over a v1 (bash run.sh) installation
evr-docker export compose             a docker-compose.yml of the same setup
evr-docker config show|edit
```

Changes to a server that is hosting a match are applied after the match (or now with `--force`).

## Updates, alerts and metrics

`sudo evr-docker agent install` runs a small background service:

- **Runtime updates.** It checks the runtime channel (`pre` is the default: every nevr-runtime 5.x release is a GitHub
  pre-release). A new version is installed and tried on one idle server first. Only if that server registers does
  `current` switch; the other servers switch at their next match boundary. A version that fails is marked bad and skipped.
  Turn this off with `runtime channel` / the `u` screen, or pin a version.
- **Discord alerts.** `evr-docker agent webhook https://discord.com/api/webhooks/…` sends a test message. You then get
  alerts when a server stops on an error (e.g. a rejected password), keeps restarting, stays down or recovers, and when
  an update is rolled out or rolled back.
- **Prometheus metrics** on `127.0.0.1:9469/metrics`: `evr_server_up`, `evr_server_state`, `evr_server_sessions_total`,
  `evr_server_restarts_total`, `evr_server_registrations_total`, `evr_server_runtime_info`.

## Doing it yourself

Nothing here requires `evr-docker` at run time.

- `evr-docker export compose` writes a plain `docker-compose.yml`. The image `ghcr.io/marshmallow-mia/echovr-server` is
  configured only through environment variables (`evr-supervisor -h` lists them). Mount the game at
  `/opt/echovr/game/current`, nevr-runtime at `/opt/echovr/nevr/<tag>/BugSplat64.dll`, and a writable directory at
  `/home/container`.
- `/etc/evr-docker/config.yaml` can be edited by hand. `evr-docker config edit` validates it, and `server apply` applies it.

## Moving from v1

The old setup (`run.sh`, `install.sh`, cron `echo_update.sh`) is still on the `main` branch until v2 is released.

```sh
sudo evr-docker migrate detect
sudo evr-docker migrate run
```

This takes over the account from the old `config.json`/`config.yaml`, imports the game folder (hard-linked, so no
second copy), and creates one server per old container on the same port. It then removes the old containers and
disables the old update cron job. The old game folder is left alone; delete it once everything works.

## Troubleshooting

- `evr-docker doctor` checks Docker, LXC settings, disk, the game build, the runtime, the account (and whether it has the
  host role in any guild), port conflicts and the public IP.
- A server in **failed** state shows the reason in the dashboard and in `server status`. Common ones:
  - `bad_credentials`: the backend rejected the Discord ID or password.
  - `udp_unreachable`: the backend could not reach the game port; check the 1:1 port forward.
  - `registration_rejected`: the server did not register in time.
- Under Proxmox LXC, Docker needs `nesting=1,keyctl=1`.

## Building

```sh
go test ./...
go build ./cmd/evr-docker ./cmd/evr-supervisor
docker build -f image/Dockerfile -t echovr-server:dev .
```

`pkg/` holds the shared packages, which the Pterodactyl product also uses: game files, runtime releases, `config.yaml`,
the EchoVRCE account API, the supervisor, status, metrics, alerts, updates and TUI widgets.

## License

GNU General Public License v3.0. Echo VR is property of Meta / Ready At Dawn and is not included; nevr-runtime is
Apache-2.0 by EchoTools.
