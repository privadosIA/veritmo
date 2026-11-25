#!/usr/bin/env bash
# scripts/music-containers.sh
#
# The Docker (Python) way of running the music generators, as an alternative to the .NET services of scripts/get-services.sh.
# Optional: HeartMuLa and DiffRhythm2 are Python programs packed in docker/<engine>/; this script builds their images, fetches the
# DiffRhythm2 weights, starts and stops the containers and says what to put in Settings. The server talks to a container the
# way it talks to an external service (the same HTTP contract), so nothing else changes.
#
#   bash scripts/music-containers.sh build  heartmula|diffrhythm2
#   bash scripts/music-containers.sh weights diffrhythm2           # ~9.5 GB; HeartMuLa's: see docker/heartmula/README.md
#   bash scripts/music-containers.sh up     heartmula|diffrhythm2 [GPU]   # GPU: the card number (default 0)
#   bash scripts/music-containers.sh down   heartmula|diffrhythm2
#   bash scripts/music-containers.sh status
#
# Needs Docker with the NVIDIA container toolkit (docker run --gpus must work). VERITMO_DOCKER=podman uses another compatible CLI.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
DOCKER="${VERITMO_DOCKER:-docker}"

declare -A PORT=([heartmula]=8787 [diffrhythm2]=8788)
declare -A IMAGE=([heartmula]=veritmo/heartmula:1 [diffrhythm2]=veritmo/diffrhythm2:1)
declare -A SETTING=([heartmula]=heartmula_host [diffrhythm2]=diffrhythm2_host)

usage() { sed -n '2,19p' "$0" | sed 's/^# \{0,1\}//'; exit 2; }
die() { echo "[music-containers] ✗  $*" >&2; exit 1; }
engine_arg() { [[ -n "${PORT[${1:-}]:-}" ]] || die "the engine must be heartmula or diffrhythm2"; }
need_docker() { command -v "$DOCKER" >/dev/null 2>&1 || die "$DOCKER is not installed"; }

cmd="${1:-}"; engine="${2:-}"
case "$cmd" in
    build)
        engine_arg "$engine"; need_docker
        echo "[music-containers] Building ${IMAGE[$engine]} (Python and PyTorch: several GB) ..."
        "$DOCKER" build -t "${IMAGE[$engine]}" "${ROOT}/docker/${engine}"
        echo "[music-containers] ✓  ${IMAGE[$engine]} built"
        ;;
    weights)
        [[ "$engine" == diffrhythm2 ]] || die "only diffrhythm2 has a fetch step here; HeartMuLa's weights: see docker/heartmula/README.md"
        need_docker
        mkdir -p "${ROOT}/models/music/diffrhythm2"
        "$DOCKER" run --rm --user "$(id -u):$(id -g)" -e HOME=/tmp -v "${ROOT}/models/music/diffrhythm2:/ckpt" "${IMAGE[diffrhythm2]}" python /opt/fetch.py
        ;;
    up)
        engine_arg "$engine"; need_docker
        gpu="${3:-0}"
        [[ "$gpu" =~ ^[0-9]+$ ]] || die "the card must be a number"
        ckpt="${ROOT}/models/music/${engine}"
        [[ -d "$ckpt" ]] || die "no weights in ${ckpt} (see docker/${engine}/README.md)"
        "$DOCKER" image inspect "${IMAGE[$engine]}" >/dev/null 2>&1 || die "no image ${IMAGE[$engine]}: run: bash scripts/music-containers.sh build ${engine}"
        "$DOCKER" rm -f "vt-${engine}" >/dev/null 2>&1 || true
        "$DOCKER" run -d --name "vt-${engine}" --restart no --gpus "device=${gpu}" \
            -p "127.0.0.1:${PORT[$engine]}:${PORT[$engine]}" -v "${ckpt}:/ckpt:ro" "${IMAGE[$engine]}" >/dev/null
        echo "[music-containers] ✓  vt-${engine} is starting on card ${gpu} (it loads its model first: GET http://127.0.0.1:${PORT[$engine]}/health says ready)"
        echo "[music-containers]    tell the server where it is, once:"
        echo "[music-containers]    curl -X PUT <server>/api/settings -H 'Content-Type: application/json' -d '{\"${SETTING[$engine]}\": \"http://127.0.0.1:${PORT[$engine]}\"}'"
        ;;
    down)
        engine_arg "$engine"; need_docker
        "$DOCKER" rm -f "vt-${engine}" >/dev/null 2>&1 && echo "[music-containers] ✓  vt-${engine} stopped and removed" || echo "[music-containers] vt-${engine} was not there"
        ;;
    status)
        need_docker
        for e in heartmula diffrhythm2; do
            state="$("$DOCKER" inspect -f '{{.State.Status}}' "vt-${e}" 2>/dev/null || echo "none")"
            img="no"; "$DOCKER" image inspect "${IMAGE[$e]}" >/dev/null 2>&1 && img="yes"
            printf '%-12s container: %-8s image built: %-3s address: http://127.0.0.1:%s\n' "$e" "$state" "$img" "${PORT[$e]}"
        done
        ;;
    *) usage ;;
esac
