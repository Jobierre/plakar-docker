#!/bin/sh
set -e

# Executes a shell-style command string. Supports commands that start with
# "plakar" as well as raw shell commands.
run_start_command() {
    cmd="$1"
    [ -z "$cmd" ] && return 1

    # shellcheck disable=SC2086 # intentional eval to honor user quoting
    eval "set -- $cmd"
    if [ "$#" -eq 0 ]; then
        return 1
    fi

    if [ "$1" = "plakar" ]; then
        shift
        exec /usr/bin/plakar "$@"
    fi

    exec "$@"
}

# If the user supplied CLI arguments, inspect them.
if [ "$#" -gt 0 ]; then
    case "$1" in
        plakar)
            shift
            exec /usr/bin/plakar "$@"
            ;;
        -*)
            exec /usr/bin/plakar "$@"
            ;;
        *)
            exec "$@"
            ;;
    esac
fi

# PLAKAR_STARTUP_COMMAND (or legacy PLAKAR_COMMAND) can provide a shell-style
# command string (e.g. "plakar scheduler start -tasks ./scheduler.yaml").
start_cmd="${PLAKAR_STARTUP_COMMAND:-${PLAKAR_COMMAND:-}}"
if [ -n "$start_cmd" ]; then
    run_start_command "$start_cmd"
fi

# Keep the container alive but idle so users can exec into it.
exec tail -f /dev/null
