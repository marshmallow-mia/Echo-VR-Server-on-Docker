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
# Debian's Wine 10 is the tested build; WineHQ 11.0 crashes at boot with
# nevr-runtime 5.4.2 (docs/nevr-5.4.2-findings.md).
ARG WINE_VERSION=10.0~repack-6
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates tini procps winbind \
      wine=${WINE_VERSION} wine64=${WINE_VERSION} libwine=${WINE_VERSION} \
 && rm -rf /var/lib/apt/lists/*
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
