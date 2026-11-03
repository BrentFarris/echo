#!/bin/sh
# Explicit, per-user background startup. Uses the existing unattended profile vault.
set -eu
if [ "${1:-}" != '--install' ]; then
  printf '%s\n' 'Usage: sh scripts/startup.sh --install /absolute/path/to/echo /absolute/profile/path [port]' 'Installs a user systemd service (Linux) or a LaunchAgent (macOS). No password is stored in the service definition.'
  exit 0
fi
binary=${2:?absolute Echo executable required}; profile=${3:?absolute profile directory required}; port=${4:-3740}
case "$binary:$profile" in /*:/*) ;; *) printf '%s\n' 'Use absolute paths.' >&2; exit 2;; esac
case "$port" in *[!0-9]*|'') printf '%s\n' 'Port must be a number.' >&2; exit 2;; esac
[ -x "$binary" ] || { printf '%s\n' 'Echo executable is not executable.' >&2; exit 2; }
# Reject control characters and service-language metacharacters in path arguments.
case "$binary$profile" in *'"'*|*'%'*|*'\'*|*'&'*|*'<'*|*'>'*|*'
'*) printf '%s\n' 'Service paths cannot contain quotes, %, backslashes, XML markup or line breaks.' >&2; exit 2;; esac
umask 077
mkdir -p "$profile"
case "$(uname -s)" in
 Linux)
  directory="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user"; mkdir -p "$directory"
  cat > "$directory/echo.service" <<EOF
[Unit]
Description=Echo personal assistant
After=network-online.target
[Service]
ExecStart="$binary" -no-open -data "$profile" -port $port
Restart=on-failure
TimeoutStopSec=30
KillMode=mixed
UMask=0077
[Install]
WantedBy=default.target
EOF
  systemctl --user daemon-reload
  systemctl --user enable --now echo.service
  printf '%s\n' 'Installed echo.service. Manage it with systemctl --user. To run before login, configure user lingering with your system administrator.'
  ;;
 Darwin)
  directory="$HOME/Library/LaunchAgents"; mkdir -p "$directory"
  cat > "$directory/com.echo.assistant.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict><key>Label</key><string>com.echo.assistant</string><key>ProgramArguments</key><array><string>$binary</string><string>-no-open</string><string>-data</string><string>$profile</string><string>-port</string><string>$port</string></array><key>RunAtLoad</key><true/><key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict><key>StandardOutPath</key><string>$profile/service.log</string><key>StandardErrorPath</key><string>$profile/service.log</string></dict></plist>
EOF
  launchctl bootstrap "gui/$(id -u)" "$directory/com.echo.assistant.plist"
  printf '%s\n' 'Installed com.echo.assistant LaunchAgent for this user.'
  ;;
 *) printf '%s\n' 'Supported platforms: Linux and macOS.' >&2; exit 2;;
esac
