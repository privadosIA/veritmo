#!/bin/sh
# Entry point of the server's image: `serve` starts the server on the address of the stack with the folders of the image's layout (MEDIA_DIR, when set, is read by the server itself); any other
# argument is a command of `veritmo` (for example `data migrate`). The options the person adds to the server go in SERVER_ARGS, which is
# empty by default: nothing here changes what the server does by default.
set -eu
if [ "${1:-}" = "serve" ]; then
    shift
    # shellcheck disable=SC2086  # SERVER_ARGS is a list of options on purpose
    exec veritmo serve \
        --addr "${SERVER_ADDR:-0.0.0.0:4666}" \
        --projects-dir /app/projects \
        ${SERVER_ARGS:-} "$@"
fi
exec veritmo "$@"
