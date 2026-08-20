#!/bin/sh

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd) || exit 1
sh "$script_dir/scripts/install-and-run-unix.sh" macos "$@"
echo_exit_code=$?

if [ "$echo_exit_code" -ne 0 ] && [ -t 0 ]; then
    printf '\nEcho could not be installed or started. Review the error above.\n'
    printf 'Press Enter to close this window...'
    read -r _echo_unused
fi

exit "$echo_exit_code"
