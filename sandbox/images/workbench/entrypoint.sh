#!/bin/bash
set -euo pipefail

if [[ "${1:-}" != "/usr/local/bin/echo-sandbox-agent" ]]; then
  exec "$@"
fi

target_uid="${ECHO_SANDBOX_UID:-1000}"
if [[ "$target_uid" =~ ^[0-9]+$ ]] && (( target_uid > 0 )) && [[ "$(id -u echo)" != "$target_uid" ]]; then
  if existing="$(getent passwd "$target_uid" | cut -d: -f1)" && [[ -n "$existing" && "$existing" != "echo" ]]; then
    echo "sandbox UID $target_uid is already assigned to $existing" >&2
    exit 1
  fi
  usermod --uid "$target_uid" echo
fi

install -d -o echo -g echo /home/echo /home/echo/go /exchange /exchange/downloads
chown -R echo:echo /home/echo
chown echo:echo /exchange /exchange/downloads

while [[ ! -s /run/echo/agent.token ]]; do
  sleep 0.1
done

exec "$@"
