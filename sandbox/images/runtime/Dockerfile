# syntax=docker/dockerfile:1.7
FROM --platform=$BUILDPLATFORM golang:1.26-bookworm AS agent-build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/echo-sandbox-agent ./cmd/echo-sandbox-agent
COPY internal/sandboxprotocol ./internal/sandboxprotocol
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/echo-sandbox-agent ./cmd/echo-sandbox-agent

FROM node:22-bookworm-slim AS node-runtime

FROM mcr.microsoft.com/playwright:v1.62.1-noble
ARG TARGETARCH
ARG ECHO_SOURCE_REPOSITORY=https://github.com/BrentFarris/echo
ARG FOSSIL_VERSION=2.27
ARG FOSSIL_CHECKIN=99675884a93c09125dbfbef0ca47959626c81545c132e247e67a08bd12ac7256
ENV DEBIAN_FRONTEND=noninteractive \
    DISPLAY=:1 HOME=/home/echo \
    PATH=/usr/local/go/bin:/home/echo/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
RUN test "$TARGETARCH" = "amd64" \
 && apt-get update \
 && apt-get install -y --no-install-recommends \
      bash build-essential ca-certificates clang cmake curl file git jq less make \
      openssh-client pkg-config python3 python3-pip python3-venv ripgrep rsync \
      sudo tcl-dev unzip xz-utils zlib1g-dev libssl-dev \
      dbus-x11 gosu imagemagick mousepad thunar x11-utils xdg-utils \
      tigervnc-standalone-server tigervnc-tools xdotool xfce4 xfce4-terminal \
 && rm -rf /var/lib/apt/lists/* \
 && curl -fsSL "https://fossil-scm.org/home/tarball/${FOSSIL_CHECKIN}/fossil-src-${FOSSIL_VERSION}.tar.gz" -o /tmp/fossil-src.tar.gz \
 && tar -xzf /tmp/fossil-src.tar.gz -C /tmp \
 && cd "/tmp/fossil-src-${FOSSIL_VERSION}" \
 && ./configure --prefix=/usr/local --disable-fusefs \
 && make -j2 fossil \
 && install -m 0755 fossil /usr/local/bin/fossil \
 && fossil version \
 && cd / \
 && rm -rf "/tmp/fossil-src-${FOSSIL_VERSION}" /tmp/fossil-src.tar.gz \
 && existing_user="$(getent passwd 1000 | cut -d: -f1 || true)" \
 && if [ -n "$existing_user" ]; then userdel "$existing_user"; fi \
 && existing_group="$(getent group 1000 | cut -d: -f1 || true)" \
 && if [ -n "$existing_group" ]; then groupdel "$existing_group"; fi \
 && groupadd --gid 1000 echo \
 && useradd --uid 1000 --gid 1000 --create-home --shell /bin/bash echo \
 && printf 'echo ALL=(ALL) NOPASSWD:ALL\n' >/etc/sudoers.d/echo \
 && chmod 0440 /etc/sudoers.d/echo \
 && install -d -o echo -g echo /exchange /workspace
# Echo owns the desktop/browser session and maps its UID to the host at startup.
# The unused Playwright account reserves UID 1001, used by Linux CI runners.
RUN if getent passwd pwuser >/dev/null; then userdel pwuser; fi
COPY --from=agent-build /usr/local/go /usr/local/go
COPY --from=agent-build /out/echo-sandbox-agent /usr/local/bin/echo-sandbox-agent
COPY --from=node-runtime /usr/local/bin/node /usr/local/bin/node
COPY --from=node-runtime /usr/local/lib/node_modules /usr/local/lib/node_modules
RUN ln -s ../lib/node_modules/npm/bin/npm-cli.js /usr/local/bin/npm \
 && ln -s ../lib/node_modules/npm/bin/npx-cli.js /usr/local/bin/npx \
 && printf 'export PATH=/usr/local/go/bin:/home/echo/go/bin:$PATH\n' >/etc/profile.d/echo-toolchain.sh
WORKDIR /opt/echo-browser
COPY sandbox/images/runtime/package.json ./package.json
RUN npm install --omit=dev --ignore-scripts --no-audit --no-fund && npm cache clean --force
COPY sandbox/images/runtime/browser-bridge.mjs ./browser-bridge.mjs
COPY internal/sandboxprotocol/version.txt ./protocol-version.txt
COPY sandbox/images/runtime/browser-launcher.mjs ./browser-launcher.mjs
COPY sandbox/images/runtime/echo-browser.desktop /usr/share/xfce4/helpers/echo-browser.desktop
COPY sandbox/images/runtime/entrypoint.sh /usr/local/bin/echo-runtime-entrypoint
RUN sed -i 's/\r$//' /usr/local/bin/echo-runtime-entrypoint /opt/echo-browser/browser-launcher.mjs \
 && chmod 0755 /usr/local/bin/echo-runtime-entrypoint /usr/local/bin/echo-sandbox-agent /opt/echo-browser/browser-launcher.mjs \
 && ln -s /opt/echo-browser/browser-launcher.mjs /usr/local/bin/echo-browser \
 && update-alternatives --install /usr/bin/x-www-browser x-www-browser /usr/local/bin/echo-browser 200 \
 && chown -R echo:echo /opt/echo-browser
LABEL org.opencontainers.image.title="Echo Sandbox Runtime" \
      org.opencontainers.image.source="$ECHO_SOURCE_REPOSITORY"
EXPOSE 3000 5900 7777
ENTRYPOINT ["/usr/local/bin/echo-runtime-entrypoint"]
CMD ["/usr/local/bin/echo-sandbox-agent", "-role=runtime"]
