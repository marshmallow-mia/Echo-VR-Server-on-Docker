# Echo VR dedicated server base image: Wine + evr-supervisor. Game files and
# nevr-runtime are not included; mount the shared tree at /opt/echovr.
FROM golang:1.27-trixie AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/evr-supervisor ./cmd/evr-supervisor

FROM debian:trixie-slim
# WineHQ's Wine 11.19, its 64-bit half only. nevr-runtime 6.x's host checks the
# signature of nevr-runtime.dll with crypt32, which Debian's Wine 10 cannot
# decode (exit 78, "does not parse as PKCS#7"); nevr-runtime measured the check
# on wine-11.19. WineHQ 11.0 crashed at boot with 5.4.2, 11.19 boots 5.4.3 and
# 6.6.0 (docs/nevr-6.6.0-findings.md). echovr.exe is 64-bit, so the i386 half
# and its multiarch libraries are left out: wine-devel (bin/wine, share/) is
# unpacked by hand because its package depends on wine-devel-i386.
ARG WINE_VERSION=11.19~trixie-1
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl gnupg tini procps winbind \
      libgnutls30t64 libfreetype6 libfontconfig1 libgssapi-krb5-2 libkrb5-3 libunwind8 libdbus-1-3 \
 && install -d /etc/apt/keyrings \
 && curl -fsSL https://dl.winehq.org/wine-builds/winehq.key | gpg --dearmor -o /etc/apt/keyrings/winehq-archive.key \
 && echo "deb [arch=amd64 signed-by=/etc/apt/keyrings/winehq-archive.key] https://dl.winehq.org/wine-builds/debian trixie main" \
      > /etc/apt/sources.list.d/winehq.list \
 && apt-get update \
 && apt-get install -y --no-install-recommends wine-devel-amd64=${WINE_VERSION} \
 && cd /tmp && apt-get download wine-devel=${WINE_VERSION} && dpkg -x wine-devel_*.deb / && rm -f wine-devel_*.deb \
 && apt-get purge -y curl gnupg && apt-get autoremove -y && rm -rf /var/lib/apt/lists/*
ENV PATH=/opt/wine-devel/bin:$PATH
ENV WINEDEBUG=-all \
    WINEDLLOVERRIDES="mscoree,mshtml=" \
    USER=container \
    HOME=/home/container
RUN useradd -u 999 -m -d /home/container container \
 && install -d -o container /opt/wine-template /opt/echovr
USER container
RUN WINEPREFIX=/opt/wine-template wineboot -i && wineserver -w
USER root
COPY --from=build /out/evr-supervisor /usr/local/bin/evr-supervisor
COPY LICENSE /usr/share/doc/evr/LICENSE
USER container
WORKDIR /home/container
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/evr-supervisor"]
