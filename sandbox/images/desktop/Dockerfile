# syntax=docker/dockerfile:1.7
FROM --platform=$BUILDPLATFORM golang:1.26-bookworm AS agent-build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/echo-sandbox-agent ./cmd/echo-sandbox-agent
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/echo-sandbox-agent ./cmd/echo-sandbox-agent

FROM mcr.microsoft.com/playwright:v1.62.1-noble
ARG TARGETARCH
ARG ECHO_SOURCE_REPOSITORY=https://github.com/BrentFarris/echo
ENV DEBIAN_FRONTEND=noninteractive \
    DISPLAY=:1 \
    HOME=/home/echo
RUN test "$TARGETARCH" = "amd64" \
 && apt-get update \
 && apt-get install -y --no-install-recommends \
      ca-certificates dbus-x11 gosu imagemagick mousepad thunar \
      tigervnc-standalone-server tigervnc-tools xdotool xfce4 xfce4-terminal \
 && rm -rf /var/lib/apt/lists/* \
	&& existing_user="$(getent passwd 1000 | cut -d: -f1 || true)" \
	&& if [ -n "$existing_user" ]; then userdel "$existing_user"; fi \
	&& existing_group="$(getent group 1000 | cut -d: -f1 || true)" \
	&& if [ -n "$existing_group" ]; then groupdel "$existing_group"; fi \
 && groupadd --gid 1000 echo \
 && useradd --uid 1000 --gid 1000 --create-home --shell /bin/bash echo \
 && install -d -o echo -g echo /opt/echo-browser /exchange /exchange/downloads /workspace
WORKDIR /opt/echo-browser
COPY sandbox/images/desktop/package.json ./package.json
RUN npm install --omit=dev --ignore-scripts --no-audit --no-fund \
 && npm cache clean --force
COPY sandbox/images/desktop/browser-bridge.mjs ./browser-bridge.mjs
COPY sandbox/images/desktop/entrypoint.sh /usr/local/bin/echo-desktop-entrypoint
COPY --from=agent-build /out/echo-sandbox-agent /usr/local/bin/echo-sandbox-agent
RUN chmod 0755 /usr/local/bin/echo-desktop-entrypoint /usr/local/bin/echo-sandbox-agent \
 && chown -R echo:echo /opt/echo-browser
LABEL org.opencontainers.image.title="Echo Sandbox Desktop" \
      org.opencontainers.image.source="$ECHO_SOURCE_REPOSITORY"
EXPOSE 3000 5900 7777
ENTRYPOINT ["/usr/local/bin/echo-desktop-entrypoint"]
CMD ["/usr/local/bin/echo-sandbox-agent", "-role=desktop"]
