#!/usr/bin/env bash
# Starts the management server (the API only) with a bearer token and prints it, so a dashboard on any machine
# can connect to it. It does not build, look for or serve a front end.
#
#   ./serve.sh                  # the server, reachable on the network (0.0.0.0:4666)
#   ./serve.sh --local          # same, but only on 127.0.0.1:4666 (use with an SSH tunnel)
#   ./serve.sh --stop           # stops the server started by this script
#   ./serve.sh --show-token     # also prints the token (otherwise only the file that holds it is named)
#
# The token is created once and reused on every start, so the one you pasted
# in the dashboard keeps working. It is stored outside the repo, in
# ~/.config/veritmo/token (mode 600). Delete that file to rotate it.
# The server starts censored: projects marked for adults (adult: true in their
# scenes.yaml) do not exist for the API. STRICT=1 also hides the projects that
# were not marked as reviewed (adult: false).
# The projects (configuration, sources, cache and results) can be kept in another directory, possibly on another
# disk: MEDIA_DIR, or the path written in ~/.config/veritmo/media-dir when the variable is not set (empty:
# they stay whole inside PROJECTS_DIR; MEDIA_DIR=off ignores the file). The project folder only holds links into it. Projects made
# before it are moved with: bin/veritmo-api media migrate --dry-run (then without it, with the server stopped).
# A built front end (for example dashboards-lab: WEB_DIR=$HOME/Proyectos/dashboards-lab/dist/dashboard-full/browser) is served at the
# root of the same port, so a Tailscale or a tunnel address that points at the server shows it with no CORS: WEB_DIR, or the path
# written in ~/.config/veritmo/web-dir when the variable is not set (WEB_DIR=off ignores the file). Without it the root is a
# plain note and the server is only the API.
# Environment overrides: ADDR (wins over --local), PROJECTS_DIR, MEDIA_DIR, NO_BUILD=1
# (start the binary as it is), STRICT=1,
# MEM_MAX (default: 85% of the machine's RAM, e.g. ~52G on 64 GB: caps the server
# and everything it launches with systemd-run, so a big model cannot take the whole
# machine down; if a run goes over, that process is killed instead of the machine
# hanging). Use MEM_MAX=0 (or off) to run without a cap, or another size such as MEM_MAX=40G.
# --stop sends SIGTERM (a clean shutdown) to the PID saved in
# ~/.config/veritmo/serve-<port>.pid, or to the veritmo listening on the
# port when there is no pid file, and waits for it to exit. A run in progress
# is interrupted.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

LOCAL=0
STOP=0
SHOW_TOKEN=0
for arg in "$@"; do
  case "$arg" in
    --stop) STOP=1 ;;
    --local) LOCAL=1 ;;
    --show-token) SHOW_TOKEN=1 ;;
    -h|--help) sed -n '2,/^$/p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "unknown option: $arg (try --help)" >&2; exit 2 ;;
  esac
done

if [ "$LOCAL" = "1" ]; then DEFAULT_ADDR="127.0.0.1:4666"; else DEFAULT_ADDR="0.0.0.0:4666"; fi
ADDR="${ADDR:-$DEFAULT_ADDR}"
PROJECTS_DIR="${PROJECTS_DIR:-projects}"
# The directory of the former name (mediaorquestator) moves once to the new one, so that the token and the other files are found where they always were:
# a rename, nothing is copied or deleted (the binary does the same when it starts).
CONFIG_BASE="${XDG_CONFIG_HOME:-$HOME/.config}"
if [ -d "$CONFIG_BASE/mediaorquestator" ]; then
  if [ ! -e "$CONFIG_BASE/veritmo" ]; then
    mv "$CONFIG_BASE/mediaorquestator" "$CONFIG_BASE/veritmo" && echo "moved $CONFIG_BASE/mediaorquestator to $CONFIG_BASE/veritmo"
  else
    echo "warning: $CONFIG_BASE/mediaorquestator is ignored ($CONFIG_BASE/veritmo exists); it may be deleted" >&2
  fi
fi
MEDIA_FILE="$CONFIG_BASE/veritmo/media-dir"
if [ -z "${MEDIA_DIR+x}" ] && [ -r "$MEDIA_FILE" ]; then MEDIA_DIR="$(head -n1 "$MEDIA_FILE")"; fi
MEDIA_DIR="${MEDIA_DIR-}"
case "$MEDIA_DIR" in off|none|0) MEDIA_DIR="" ;; esac
WEB_FILE="$CONFIG_BASE/veritmo/web-dir"
if [ -z "${WEB_DIR+x}" ] && [ -r "$WEB_FILE" ]; then WEB_DIR="$(head -n1 "$WEB_FILE")"; fi
WEB_DIR="${WEB_DIR-}"
case "$WEB_DIR" in off|none|0) WEB_DIR="" ;; esac

TOKEN_FILE="$CONFIG_BASE/veritmo/token"

PORT="${ADDR##*:}"
PID_FILE="$(dirname "$TOKEN_FILE")/serve-$PORT.pid"

# True when $1 is a live `veritmo serve` (not a recycled PID).
is_server() {
  [ -r "/proc/$1/cmdline" ] && tr '\0' ' ' < "/proc/$1/cmdline" | grep -Eq "(veritmo|orchestrator)(-api)? serve"
}

if [ "$STOP" = "1" ]; then
  PID="$(cat "$PID_FILE" 2>/dev/null || true)"
  if [ -z "$PID" ] || ! is_server "$PID"; then
    # Started some other way: look for the veritmo listening on the port.
    PID="$(ss -ltnpH "sport = :$PORT" 2>/dev/null | grep -o 'pid=[0-9]*' | head -1 | cut -d= -f2 || true)"
    if [ -n "$PID" ] && ! is_server "$PID"; then PID=""; fi
  fi
  if [ -z "$PID" ]; then
    echo "No Veritmo server found (pid file $PID_FILE, port $PORT)."
    rm -f "$PID_FILE"
    exit 0
  fi
  echo "Stopping the server (PID $PID)..."
  kill -TERM "$PID"
  for _ in $(seq 1 100); do
    kill -0 "$PID" 2>/dev/null || { rm -f "$PID_FILE"; echo "Stopped."; exit 0; }
    sleep 0.1
  done
  echo "The server did not stop after 10 s; kill -KILL $PID if you must." >&2
  exit 1
fi

# One server per port: a second start would only fail on the busy port.
OLD="$(cat "$PID_FILE" 2>/dev/null || true)"
if [ -n "$OLD" ] && is_server "$OLD"; then
  echo "The server is already running (PID $OLD). Stop it with: ./serve.sh --stop" >&2
  exit 1
fi

# piper, sd and the rest live in bin/; the server looks them up on PATH.
export PATH="$ROOT/bin:$PATH"

if [ ! -s "$TOKEN_FILE" ]; then
  mkdir -p "$(dirname "$TOKEN_FILE")"
  umask 077
  head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n' > "$TOKEN_FILE"
fi
VERITMO_TOKEN="$(cat "$TOKEN_FILE")"
export VERITMO_TOKEN

# The private edition is built with the tag `private`; the public tree does not
# contain its files, so the tag is added only when they are there.
EDITION_TAG=""
[ -f internal/platform/edition/edition_private.go ] && EDITION_TAG="private"

# The server is rebuilt on every start, so what you changed in the code (or pulled) is what runs: `go build` is
# incremental and takes a second or two when nothing changed. NO_BUILD=1 starts the binary as it is; without the Go
# toolchain the existing binary is used.
CAN_BUILD=1
{ [ "${NO_BUILD:-0}" = "1" ] || ! command -v go >/dev/null 2>&1; } && CAN_BUILD=0

BIN=bin/veritmo-api
if [ "$CAN_BUILD" = "1" ] || [ ! -x "$BIN" ]; then
  echo "Building $BIN..."
  go build ${EDITION_TAG:+-tags "$EDITION_TAG"} -ldflags "-X veritmo/internal/platform/edition.Version=$(bash scripts/version.sh)" -o "$BIN" ./cmd/veritmo
fi

if [ "$SHOW_TOKEN" = "1" ]; then TOKEN_SHOWN="$VERITMO_TOKEN"; else TOKEN_SHOWN="(hidden: read it with: cat $TOKEN_FILE, or start with --show-token)"; fi

cat <<EOF

==================================================================
  Veritmo server
  Listening on:  $ADDR
  Projects in:   ${MEDIA_DIR:-$PROJECTS_DIR}
  Front end:     ${WEB_DIR:-none (API only)}
  TOKEN:         $TOKEN_SHOWN
  (stored in $TOKEN_FILE)

  Send the token as: Authorization: Bearer <token>

  A dashboard on another machine connects to http://<this-machine-ip>:$PORT (when
  listening on 0.0.0.0) or, safer, start with --local and tunnel:
    ssh -L $PORT:127.0.0.1:$PORT $USER@<this-machine>
  and connect to http://127.0.0.1:$PORT
==================================================================

EOF

# exec keeps this PID, so the pid file names the server itself.
mkdir -p "$(dirname "$PID_FILE")"
echo $$ > "$PID_FILE"

CMD=("$BIN" serve --addr "$ADDR" --projects-dir "$PROJECTS_DIR")
[ -n "$MEDIA_DIR" ] && CMD+=(--media-dir "$MEDIA_DIR")
[ -n "$WEB_DIR" ] && CMD+=(--web-dir "$WEB_DIR")
[ "${STRICT:-0}" = "1" ] && CMD+=(--strict)
# Extra options of the build live in an optional script next to this one (it may add to CMD).
if [ -f "$ROOT/scripts/serve-private.sh" ]; then
  # shellcheck source=/dev/null
  . "$ROOT/scripts/serve-private.sh"
fi
if [ -z "${MEM_MAX+x}" ]; then
  # Default cap: 85% of the installed RAM (falls back to 24G if it cannot be read).
  MEM_KB=$(awk '/^MemTotal:/ {print $2}' /proc/meminfo 2>/dev/null)
  if [ -n "$MEM_KB" ]; then MEM_MAX="$((MEM_KB * 85 / 100 / 1024))M"; else MEM_MAX="24G"; fi
fi
case "$MEM_MAX" in 0|off|none|"") MEM_MAX="" ;; esac
if [ -n "$MEM_MAX" ] && ! command -v systemd-run >/dev/null 2>&1; then
  echo "systemd-run not found: starting without a memory cap." >&2
  MEM_MAX=""
fi
if [ -n "$MEM_MAX" ]; then
  echo "Memory cap: $MEM_MAX (MEM_MAX=0 to remove it)."
  exec systemd-run --user --scope -q -p "MemoryMax=$MEM_MAX" -p MemorySwapMax=0 "${CMD[@]}"
fi
exec "${CMD[@]}"
