#!/bin/sh
# Installs evr-docker from GitHub releases and starts the setup.
#   curl -fsSL https://raw.githubusercontent.com/marshmallow-mia/Echo-VR-Server-on-Docker/main/install.sh | sudo sh
# Installs the newest release (a pre-release while there is no stable one).
# Set EVR_VERSION=v2.0.0 to pick a version, EVR_NO_INIT=1 to only install.
set -eu
repo="marshmallow-mia/Echo-VR-Server-on-Docker"
version="${EVR_VERSION:-}"
case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) echo "unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac
[ "$(id -u)" = 0 ] || { echo "run as root (sudo)" >&2; exit 1; }
if [ -z "$version" ]; then
  api="https://api.github.com/repos/$repo/releases"
  tags() { sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p'; }
  # GitHub does not list releases in version order (rc.10 comes after rc.6),
  # so sort them: numbers padded, a full release after its pre-releases.
  newest() {
    tags | awk '{
      v = $0; sub(/^v/, "", v); pre = ""
      if (i = index(v, "-")) { pre = substr(v, i + 1); v = substr(v, 1, i - 1) }
      split(v, a, "."); key = sprintf("%06d.%06d.%06d", a[1], a[2], a[3])
      if (pre == "") key = key "~"
      else {
        n = split(pre, b, "."); p = ""
        for (j = 1; j <= n; j++) { if (b[j] ~ /^[0-9]+$/) b[j] = sprintf("%06d", b[j]); p = p (j > 1 ? "." : "") b[j] }
        key = key "-" p
      }
      print key " " $0
    }' | sort | tail -n 1 | cut -d" " -f2
  }
  version="$(curl -fsSL "$api/latest" 2>/dev/null | tags | head -n 1)"
  [ -n "$version" ] || version="$(curl -fsSL "$api?per_page=100" | newest)"
  [ -n "$version" ] || { echo "no release found for $repo" >&2; exit 1; }
fi
base="https://github.com/$repo/releases/download/$version"
tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
echo "Downloading evr-docker ($version, linux/$arch)…"
curl -fsSL -o "$tmp/evr-docker_linux_$arch" "$base/evr-docker_linux_$arch"
curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt"
(cd "$tmp" && grep " evr-docker_linux_$arch\$" checksums.txt | sha256sum -c -) >/dev/null || { echo "checksum mismatch" >&2; exit 1; }
install -m 0755 "$tmp/evr-docker_linux_$arch" /usr/local/bin/evr-docker
echo "Installed /usr/local/bin/evr-docker ($(/usr/local/bin/evr-docker --version))"
if ! command -v docker >/dev/null 2>&1; then
  answer=n
  if [ -r /dev/tty ]; then
    printf 'Docker is not installed. Install it now with the official script (get.docker.com)? [Y/n] ' >/dev/tty
    read -r answer </dev/tty || answer=n
    [ -n "$answer" ] || answer=y
  fi
  case "$answer" in
    y|Y|yes) curl -fsSL https://get.docker.com | sh ;;
    *) echo "Install Docker before the setup: curl -fsSL https://get.docker.com | sh" ;;
  esac
fi
if ! command -v rsync >/dev/null 2>&1; then
  # The game files are downloaded with rsync.
  if command -v apt-get >/dev/null 2>&1; then
    echo "Installing rsync…"
    apt-get install -y -qq rsync >/dev/null 2>&1 || { apt-get update -qq && apt-get install -y -qq rsync >/dev/null; }
  else
    echo "Install rsync to download the game files."
  fi
fi
if [ -z "${EVR_NO_INIT:-}" ] && [ -t 1 ] && [ -r /dev/tty ]; then
  exec /usr/local/bin/evr-docker init < /dev/tty
fi
echo "Next: evr-docker init"
