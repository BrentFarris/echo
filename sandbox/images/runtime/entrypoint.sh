#!/bin/bash
set -euo pipefail

if [[ "${1:-}" != "/usr/local/bin/echo-sandbox-agent" ]]; then
  exec "$@"
fi

original_uid="$(id -u echo)"
target_uid="${ECHO_SANDBOX_UID:-1000}"
if [[ "$target_uid" =~ ^[0-9]+$ ]] && (( target_uid > 0 )) && [[ "$(id -u echo)" != "$target_uid" ]]; then
  if existing="$(getent passwd "$target_uid" | cut -d: -f1)" && [[ -n "$existing" && "$existing" != "echo" ]]; then
    echo "sandbox UID $target_uid is already assigned to $existing" >&2
    exit 1
  fi
  usermod --uid "$target_uid" echo
fi

# install assigns ownership only to the named directories, not implicit parents.
# Chromium's crash reporter and desktop apps also write beside the managed profile.
install -d -o echo -g echo /home/echo /home/echo/go /home/echo/.config /home/echo/.config/chromium /home/echo/.config/gtk-3.0 \
  /home/echo/.config/xfce4 /exchange /exchange/downloads
install -d -m 0700 -o echo -g echo /run/echo/browser
install -d -m 0700 -o echo -g echo /run/echo/accessibility
if [[ "$original_uid" != "$(id -u echo)" ]]; then
  find /home/echo -uid "$original_uid" -exec chown -h echo {} +
fi
chown echo:echo /exchange /exchange/downloads
if [[ ! -e /home/echo/.config/gtk-3.0/bookmarks ]]; then
  printf 'file:///workspace Workspace\nfile:///exchange Exchange\n' >/home/echo/.config/gtk-3.0/bookmarks
fi
chown echo:echo /home/echo/.config/gtk-3.0/bookmarks

helpers_file=/home/echo/.config/xfce4/helpers.rc
touch "$helpers_file"
if grep -q '^WebBrowser=' "$helpers_file"; then
  sed -i 's/^WebBrowser=.*/WebBrowser=echo-browser/' "$helpers_file"
else
  printf 'WebBrowser=echo-browser\n' >>"$helpers_file"
fi
chown echo:echo "$helpers_file"

while [[ ! -s /run/echo/agent.token || ! -s /run/echo/vnc.password || ! -s /run/echo/lease.token ]]; do
  sleep 0.1
done

vncpasswd -f </run/echo/vnc.password >/run/echo/vnc.passwd
chown echo:echo /run/echo/vnc.passwd
chmod 0600 /run/echo/vnc.passwd

export DISPLAY=:1 HOME=/home/echo
export XDG_RUNTIME_DIR="/run/user/$(id -u echo)"
export DBUS_SESSION_BUS_ADDRESS="unix:path=$XDG_RUNTIME_DIR/bus"
export GTK_MODULES=gail:atk-bridge NO_AT_BRIDGE=0
install -d -m 0700 -o echo -g echo "$XDG_RUNTIME_DIR"
service_pids=()
shutdown() {
  trap - TERM INT EXIT
  for service_pid in "${service_pids[@]}"; do kill -TERM "$service_pid" 2>/dev/null || true; done
  wait || true
}
trap shutdown TERM INT EXIT

# UI adapters process untrusted application state and third-party accessibility
# trees. A native library or browser failure must fail the current UI call, not
# terminate PID 1 and take the shell, desktop, and agent down with it. Keep each
# adapter behind a stable supervisor; if the adapter exits, its socket/port is
# briefly unavailable and callers can retry after the clean restart.
supervise_ui_service() {
  local name="$1"
  shift
  local child_pid=""
  local status=0
  stop_ui_service() {
    trap - TERM INT
    if [[ -n "$child_pid" ]]; then
      kill -TERM "$child_pid" 2>/dev/null || true
      wait "$child_pid" 2>/dev/null || true
    fi
    exit 0
  }
  trap stop_ui_service TERM INT
  while true; do
    "$@" &
    child_pid="$!"
    if wait "$child_pid"; then
      status=0
    else
      status="$?"
    fi
    child_pid=""
    echo "$name exited with status $status; restarting" >&2
    sleep 1 &
    child_pid="$!"
    wait "$child_pid" || true
    child_pid=""
  done
}

start_accessibility() {
  exec gosu echo python3 /opt/echo-browser/desktop-accessibility.py
}

start_browser_bridge() {
  exec gosu echo node /opt/echo-browser/browser-bridge.mjs 3</run/echo/lease.token
}

gosu echo Xvnc :1 -geometry 1440x900 -depth 24 -rfbport 5900 -localhost no \
  -SecurityTypes VncAuth -PasswordFile /run/echo/vnc.passwd -AlwaysShared=1 -DisconnectClients=0 -ac &
service_pids+=("$!")

for _ in $(seq 1 100); do
  [[ -S /tmp/.X11-unix/X1 ]] && break
  sleep 0.1
done

[[ -S /tmp/.X11-unix/X1 ]] || { echo "X server did not start" >&2; exit 1; }
gosu echo dbus-daemon --session --nofork --address="$DBUS_SESSION_BUS_ADDRESS" &
service_pids+=("$!")
for _ in $(seq 1 100); do [[ -S "$XDG_RUNTIME_DIR/bus" ]] && break; sleep 0.1; done
[[ -S "$XDG_RUNTIME_DIR/bus" ]] || { echo "Session bus did not start" >&2; exit 1; }
# Start accessibility before applications so GTK controls join the same bus.
gosu echo dbus-send --session --dest=org.a11y.Bus --type=method_call --print-reply \
  /org/a11y/bus org.a11y.Bus.GetAddress >/dev/null
supervise_ui_service "desktop accessibility service" start_accessibility &
service_pids+=("$!")
gosu echo startxfce4 &
service_pids+=("$!")
# The source token stays root-only. The unprivileged bridge inherits a single
# already-open descriptor and retains the value only in process memory.
supervise_ui_service "browser bridge" start_browser_bridge &
service_pids+=("$!")

"$@" &
service_pids+=("$!")
# A dead essential service invalidates the shared environment. Exit so Echo
# reports it unhealthy instead of accepting commands into a partial desktop.
wait -n "${service_pids[@]}" || true
exit 1
