#!/usr/bin/env bash
# scripts/compose-init.sh
#
# Prepares the Compose stack (compose.yaml) on this machine: checks Docker, Docker Compose and the NVIDIA runtime, makes the `.env` from `.env.example`
# with generated secrets (it never overwrites a `.env` that exists), sets UID and GID, makes the host folders, and says what to
# run next and which address and token the dashboard needs.
#
#   bash scripts/compose-init.sh              # checks, then prepares
#   bash scripts/compose-init.sh --check      # only checks Docker, Compose and the NVIDIA runtime
#   bash scripts/compose-init.sh --no-gpu     # skip the check of the NVIDIA runtime (a machine that will not run the generators or the renders)
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
CHECK_ONLY=0
NO_GPU=0
for a in "$@"; do
    case "$a" in
        --check)  CHECK_ONLY=1 ;;
        --no-gpu) NO_GPU=1 ;;
        -h|--help) sed -n 2,14p "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
        *) echo "compose-init: unknown option $a" >&2; exit 2 ;;
    esac
done

fail() { echo "compose-init: $*" >&2; exit 1; }

# ── 1. What the stack needs ──────────────────────────────────────────────────
command -v docker >/dev/null 2>&1 || fail "Docker is not installed (https://docs.docker.com/engine/install/): nothing was changed"
docker compose version >/dev/null 2>&1 || fail "Docker Compose v2 is not installed (the 'docker compose' command): nothing was changed"
docker info >/dev/null 2>&1 || fail "Docker is installed but the daemon does not answer (is it running, and is your user in the docker group?): nothing was changed"
if [[ $NO_GPU -eq 0 ]]; then
    if ! docker info 2>/dev/null | grep -qi 'nvidia'; then
        fail "Docker has no NVIDIA runtime. Install the NVIDIA Container Toolkit (https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/) and restart Docker; with --no-gpu this check is skipped. Nothing was changed"
    fi
    echo "docker: $(docker --version | cut -d, -f1), compose $(docker compose version --short), NVIDIA runtime found"
else
    echo "docker: $(docker --version | cut -d, -f1), compose $(docker compose version --short) (the NVIDIA runtime was not checked)"
fi
[[ $CHECK_ONLY -eq 1 ]] && exit 0

# ── 2. The environment ───────────────────────────────────────────────────────
secret() { openssl rand -hex 24 2>/dev/null || head -c 48 /dev/urandom | od -An -tx1 | tr -d ' \n'; }
if [[ -f .env ]]; then
    echo ".env exists: kept as it is (delete it to make a new one; its secrets are not changed)"
else
    [[ -f .env.example ]] || fail ".env.example is missing"
    cp .env.example .env
    chmod 600 .env
    # every secret that is empty in the example gets a generated value
    while IFS= read -r name; do
        sed -i "s|^${name}=\$|${name}=$(secret)|" .env
    done < <(grep -E '^[A-Z0-9_]*(PASSWORD|TOKEN)=$' .env.example | cut -d= -f1)
    sed -i "s|^UID=.*|UID=$(id -u)|; s|^GID=.*|GID=$(id -g)|" .env
    echo ".env made, with generated secrets (mode 600)"
    # The images are the ones of the release this checkout is, when it is exactly one (v0.1.0, not three commits after it).
    release="$(bash "$ROOT/scripts/version.sh" 2>/dev/null || echo dev)"
    if [[ "$release" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$ && ! "$release" =~ -[0-9]+-g[0-9a-f]+$ ]]; then
        sed -i "s|^#\?VERSION=.*|VERSION=${release#v}|" .env
        grep -q '^VERSION=' .env || echo "VERSION=${release#v}" >> .env
        echo "VERSION=${release#v}: the images of this release"
    else
        echo "VERSION left unset: the images are the latest release (this checkout is not exactly a release)"
    fi
    # The server's memory cap must fit in the machine: the example's default (48g) is above the RAM of a 32 GB machine, where the kernel would
    # kill the server before the cap ever applied. On 40 GiB or less the cap is 24g; above that the default stays.
    mem_kb="$(awk '/^MemTotal:/ {print $2}' "${PROC_MEMINFO:-/proc/meminfo}" 2>/dev/null || true)"
    if [[ "$mem_kb" =~ ^[0-9]+$ ]]; then
        mem_gib="$(awk -v k="$mem_kb" 'BEGIN { printf "%.0f", k / 1048576 }')"
        if (( mem_kb <= 41943040 )); then
            sed -i 's|^SERVER_MEM_LIMIT=.*|SERVER_MEM_LIMIT=24g|' .env
            echo "SERVER_MEM_LIMIT=24g: this machine has ${mem_gib} GiB of RAM (40 or less), so the server is capped at 24g; raise it in .env if you want"
        else
            echo "SERVER_MEM_LIMIT left at its default, $(grep -E '^SERVER_MEM_LIMIT=' .env | cut -d= -f2): this machine has ${mem_gib} GiB of RAM"
        fi
    else
        echo "SERVER_MEM_LIMIT left at its default: the RAM of this machine could not be read"
    fi
fi

# ── 3. The folders of this machine ───────────────────────────────────────────
value() { grep -E "^$1=" .env | tail -1 | cut -d= -f2-; }
for v in PROJECTS_PATH MEDIA_PATH MODELS_PATH VOICES_PATH HYPERFRAMES_PATH OLLAMA_PATH INSTALL_PATH CONFIG_PATH; do
    dir="$(value "$v")"
    [[ -n "$dir" ]] || continue
    case "$dir" in /*) ;; *) dir="$ROOT/${dir#./}" ;; esac
    mkdir -p "$dir"
    [[ "$v" == INSTALL_PATH ]] && mkdir -p "$dir/bin" "$dir/third_party"
done
# The default folders are under this checkout: a go.mod at their root keeps `go build ./...` out of the sources the installer fetches there
# (the same thing third_party/go.mod does for the native install).
if [[ -d "$ROOT/compose-data" && ! -e "$ROOT/compose-data/go.mod" ]]; then
    printf 'module compose-data\n' >"$ROOT/compose-data/go.mod"
fi


port="$(value SERVER_PORT)"; bind="$(value BIND)"
cat <<MSG

Ready. Next:
  docker compose --profile install run --rm installer   # installs the programs into the host folders (once; it can take a long time)
  docker compose up -d                                  # the server, MySQL, the migration, Prometheus and Grafana
Pieces that are optional are profiles: set COMPOSE_PROFILES in .env (music, local-llm, logs, redis), or add --profile to the command.

The dashboard connects from outside to http://${bind:-127.0.0.1}:${port:-4666} with the token of the server, which is VERITMO_TOKEN in .env:
  grep ^VERITMO_TOKEN= .env
and the origin it is served from goes in DASHBOARD_ORIGIN in .env.
MSG
