# Echo VR Server on Docker (v2, work in progress)

This branch is a full rewrite for [nevr-runtime](https://github.com/EchoTools/nevr-runtime) 5.4.2+.
The bash-based v1 setup is still on `main`.

What v2 provides:

- `evr-docker`: a TUI and CLI that installs and manages Echo VR dedicated servers on a Docker host.
- `evr-supervisor` and the base image `ghcr.io/marshmallow-mia/echovr-server`: run one server under Wine,
  restart it after every match and handle the runtime's shutdown hang.
- `pkg/`: shared Go packages, also used by [pterodactyl_Echo_Server](https://github.com/marshmallow-mia/pterodactyl_Echo_Server).

Measured runtime behaviour on Linux is documented in [docs/nevr-5.4.2-findings.md](docs/nevr-5.4.2-findings.md).

Licensed under the GNU General Public License v3.0.
