# nevr-runtime v5.4.2 on Linux: tested behaviour

Measured on 2026-10-21 on a Debian 12 LXC (Proxmox, NAT) with Docker, game build
34.4.631547.1 and `BugSplat64.dll` sha256 `fbff02cd37612bac64a1bbf78f82f76f57f1183e8ea274e5b23738d2f53a9ff1`.
The logs these notes are based on are in `pkg/supervisor/testdata/` (account data replaced).

| Topic | Result | What the tooling does |
|---|---|---|
| Wine | Debian `wine 10.0~repack-6` boots, logs in and registers. WineHQ `winehq-stable 11.0` crashes at boot (ACCESS_VIOLATION in CRYPT32/advapi32). | Image pins Debian Wine 10. |
| Read-only game tree | Works. The game only writes to `_local`, `_temp`, `bin/win10/logs` and the Wine prefix. Paths are resolved through symlinks without being canonicalised (`config.yaml loaded from: Z:\home\container\root\bin\win10\..\..\_local\config.yaml`). | Shared tree mounted read-only, per-instance symlink farm. |
| `-port N` | **Ignored.** The broadcaster always binds the `port` from `sourcedb/rad15/json/r14/config/netconfig_dedicatedserver.json` (6792 in the stock file), whatever the argument order. | Each instance gets its own `netconfig_dedicatedserver.json` with `port=N`, `retries=0`. |
| `-httpport N` | Honoured (`Bound HTTP listener to 127.0.0.1:N`). | Passed per instance. |
| `config.json` | Optional, but without it the runtime uses `publisher_lock: echotools`, which changes `version_lock` (`0x836b7d23b90d3d6d` instead of `0x134b1272e1c4c0b7`). | A minimal `{"publisher_lock":"echovrce"}` is written per instance (configurable). |
| Registration address | Nakama uses the WebSocket source IP and the bound port, then UDP-pings it. If that port is reachable but belongs to another server, the ping still succeeds and the registration points at the wrong server. | Ports are published 1:1 and the bound port is checked against the configured one. |
| ServerDB unreachable | The server retries forever (`[NEVR.SERVERDB] Connection error … retries=N`) and never exits. | Supervisor registration timeout. |
| Ctrl+C (SIGINT) | Unregisters within about 0.1 s (`CTRL+C teardown complete`), then hangs in `StopWebSocketBridgeListener` until the 20 s watchdog exits with code 1 (issue #723). | Supervisor kills the process group a few seconds after the teardown line. |
| Device-code linking | Device flow works (`device/auth/request` → approve at echovrce.com/device → `device/auth/poll`). With a password still set on the account, a server holding only the refresh token did not start (the `/spr` login answered HTTP 400). After the password was cleared with the EchoVRCE bot, the same setup signed in (`LOGIN SUCCESS`) and registered (re-tested 2026-10-21 on Pterodactyl 2.0). The device token carries the Discord ID (`vrs.did`) but no guild memberships. | Password optional: `evr-ptero account link` (device flow) fills in the Discord ID and puts the token into each server's `_local/.credentials.json`. Clear the account password to use it. |
| HTTP key | nevr-runtime embeds the backend's public HTTP key in every client build ("public by design", `config/public-defaults.env.example`), but servers do not get built-in defaults. | The tools use it by default (`config.DefaultHTTPKey`); nobody has to enter it. |
| Credential check | `account/authenticate/password` returns a JWT whose `vrs.memberships` maps guild IDs to role bits (8 = ServerHost, 32 = suspended). | `account test` lists hostable guilds. |
| Listing your servers | RPC `match` with only a Bearer token (no `http_key`) and query `+label.broadcaster.oper:<user id>` returns your registered servers. Endpoint and region codes are blanked; region shows Nakama's geolocation (`de-saxony`). | "Registered servers" view. Requested `regions` cannot be verified from outside. |
