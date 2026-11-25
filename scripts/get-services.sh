#!/usr/bin/env bash
# scripts/get-services.sh
#
# Builds the .NET services the music screens use and puts them in bin/services/<name>, where the server starts
# them from the Environment screen:
#
#   heartmula   HeartMuLa.Net.Server    (song generator)
#   diffrhythm2 diffrhythm2-service     (song generator) and the diffrhythm2 executable (MuQ-MuLan: "muq" in "Take it from a video or audio")
#   lionclap    lionclap-service        (LAION-CLAP, the sound classifier of "Take it from a video or audio")
#   videovision videovision-service     (the person mask and the depth of the video-edit tools; runs on the CPU, no CUDA)
#   triposr     triposr-service         (3D model from one picture, TripoSR; the same HTTP contract as the docker/model3d container, which stays as the other backend)
#   stable-fast-3d  stablefast3d-service (3D model from one picture, Stable Fast 3D; the same contract, the other engine of that container)
#
#   bash scripts/get-services.sh                       # all of them, for an NVIDIA GPU
#   bash scripts/get-services.sh --only lionclap       # one of them (or several: --only lionclap,videovision)
#   bash scripts/get-services.sh --cpu                 # CPU build (no CUDA libraries)
#   bash scripts/get-services.sh --no-share            # keep a separate copy of every library (see below)
#   bash scripts/get-services.sh --share-only          # build nothing: share the libraries of the services already in bin/services
#   bash scripts/get-services.sh --only stable-fast-3d --accept-licence stability-community   # Stable Fast 3D (see below)
#   VERITMO_SERVICES_SRC=~/src bash scripts/get-services.sh # where the repositories are (default: the folder above this one)
#
# Stable Fast 3D is under the Stability AI Community License (https://stability.ai/community-license-agreement): free for research and non-commercial use and
# for commercial use under US$1M of yearly revenue (registering with Stability AI), a licence from Stability AI above that. It is built only when you say you
# read it and agree: --accept-licence stability-community. Asked for with --only and without that, nothing is built and the exit is 1; in a build of every
# service it is skipped (the others are built) and the script says so.
#
# The source of the ports (heartmula, diffrhythm2, lion-clap, video-vision, triposr, stable-fast-3d and the musicgen-core they share) is in dotnet/ of this
# repository (revisions in dotnet/REVISIONS.txt); VERITMO_SERVICES_SRC uses other checkouts instead. The CUDA libraries are the same
# files in the three big services, so after building the script keeps each of them once and makes the other copies hard links to it
# (measured: 12.3 of 18.8 GB; see share_libraries). The weights are not touched: they live in models/music/ (and models/video/ for video-vision). Needs the .NET SDK 8 or newer. Safe to re-run.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
# shellcheck source=lib/licence-stability.sh
source "${SCRIPT_DIR}/lib/licence-stability.sh"
if [[ -n "${VERITMO_SERVICES_SRC:-}" ]]; then SRC="${VERITMO_SERVICES_SRC}"; else SRC="${ROOT}/dotnet"; fi
OUT="${ROOT}/bin/services"
BACKEND="cuda-linux"
ONLY=""
ACCEPTED=""
SHARE=1
SHARE_ONLY=0

while [[ $# -gt 0 ]]; do
    case "$1" in
        --cpu) BACKEND="cpu"; shift ;;
        --only) ONLY="${2:?--only needs a name}"; shift 2 ;;
        --accept-licence) ACCEPTED="${2:?--accept-licence needs the licence id}"; shift 2 ;;
        --accept-licence=*) ACCEPTED="${1#--accept-licence=}"; shift ;;
        --no-share) SHARE=0; shift ;;
        --share-only) SHARE_ONLY=1; shift ;;
        -h|--help) sed -n '2,29p' "$0"; exit 0 ;;
        *) echo "unknown option '$1' (try --help)" >&2; exit 2 ;;
    esac
done

# share_libraries: the native libraries (the CUDA build of TorchSharp: libtorch, cuBLAS, cuDNN...) are the same files in heartmula,
# diffrhythm2 and lionclap, so a file of 1 MiB or more that is byte-identical (cmp) to the first copy found, in the order of the
# paths, becomes a hard link to it. Measured on 2026-11-15 on this machine: 18.8 GB of such files, 12.3 GB of them identical. Safe to
# repeat and to run by itself: every build goes into <name>.new and then replaces the folder, so a link is never written through
# (a service that is rebuilt gets new files and is linked again); a file that differs is left alone; a volume that cannot link
# (links need one volume) leaves the copies as they are.
share_libraries() {
    [[ -d "${OUT}" ]] || return 0
    local -A first=()
    local path size key canon saved=0 linked=0
    while IFS= read -r -d '' path; do
        size="$(stat -c %s "${path}")"
        key="${path##*/}:${size}"
        if [[ -z "${first[${key}]:-}" ]]; then first[${key}]="${path}"; continue; fi
        canon="${first[${key}]}"
        [[ "$(stat -c %i "${canon}")" == "$(stat -c %i "${path}")" ]] && continue
        if cmp -s "${canon}" "${path}"; then
            if ! ln -f "${canon}" "${path}" 2>/dev/null; then
                echo "[get-services] the libraries could not be linked (a hard link needs one volume): the copies stay as they are" >&2
                return 0
            fi
            saved=$((saved + size)); linked=$((linked + 1))
        fi
    done < <(find "${OUT}" -mindepth 2 -type f -size +1048575c -print0 | sort -z)
    if (( linked > 0 )); then
        echo "[get-services] ✓  ${linked} identical libraries are kept once: $(( saved / 1000000 )) MB saved"
    else
        echo "[get-services] the libraries are already shared (or there is nothing to share)"
    fi
}

if [[ "${SHARE_ONLY}" == 1 ]]; then
    share_libraries
    exit 0
fi

# The licences that need an explicit yes: service name -> licence id. What the person is told is the same wherever the refusal comes from.
declare -A LICENCE_OF=( [stable-fast-3d]="stability-community" )
licence_message() { stability_licence_message "[get-services]"; }
accepted() { [[ "${ACCEPTED}" == "${LICENCE_OF[$1]}" ]]; }
# asked for by name, without the acceptance: nothing is built, not even the others named with it
for asked in ${ONLY//,/ }; do
    if [[ -n "${LICENCE_OF[${asked}]:-}" ]] && ! accepted "${asked}"; then
        echo "[get-services] ✗  ${asked} is not built: its licence has not been accepted." >&2
        licence_message
        exit 1
    fi
done

command -v dotnet >/dev/null || { echo "[get-services] dotnet is not installed (the .NET SDK 8 or newer is needed)" >&2; exit 1; }

# name | repository | project | extra project published into the same folder (optional)
SERVICES=(
    "heartmula|heartmula|src/HeartMuLa.Net.Server/HeartMuLa.Net.Server.csproj"
    "diffrhythm2|diffrhythm2|src/DiffRhythm2.Service/DiffRhythm2.Service.csproj|src/DiffRhythm2.Cli/DiffRhythm2.Cli.csproj"
    "lionclap|lion-clap|src/LionClap.Service/LionClap.Service.csproj"
    "videovision|video-vision|src/VideoVision.Service/VideoVision.Service.csproj"
    "triposr|triposr|src/TripoSR.Service/TripoSR.Service.csproj"
    "stable-fast-3d|stable-fast-3d|src/StableFast3D.Service/StableFast3D.Service.csproj"
)

done_any=0
for entry in "${SERVICES[@]}"; do
    IFS='|' read -r name repo project extra <<<"${entry}"
    [[ -n "${ONLY}" && ",${ONLY}," != *",${name},"* ]] && continue
    if [[ -n "${LICENCE_OF[${name}]:-}" ]] && ! accepted "${name}"; then
        echo "[get-services] -  ${name} is skipped: its licence has not been accepted." >&2
        licence_message
        continue
    fi
    csproj="${SRC}/${repo}/${project}"
    if [[ ! -f "${csproj}" ]]; then
        echo "[get-services] ✗  ${name}: ${csproj} is not there (dotnet/ is incomplete; or set VERITMO_SERVICES_SRC)" >&2
        exit 1
    fi
    echo "[get-services] Building ${name} (${BACKEND}) ..."
    dotnet publish "${csproj}" -c Release -p:TorchBackend="${BACKEND}" -o "${OUT}/${name}.new" --nologo -v q
    if [[ -n "${extra:-}" ]]; then
        dotnet publish "${SRC}/${repo}/${extra}" -c Release -p:TorchBackend="${BACKEND}" -o "${OUT}/${name}.new" --nologo -v q
    fi
    rm -rf "${OUT}/${name}"
    mv "${OUT}/${name}.new" "${OUT}/${name}"
    echo "[get-services] ✓  ${name} -> ${OUT}/${name}"
    done_any=1
done

[[ "${done_any}" == 1 ]] || { echo "[get-services] nothing to build: '${ONLY}' names none of heartmula, diffrhythm2, lionclap, videovision, triposr, stable-fast-3d" >&2; exit 2; }
[[ "${SHARE}" == 1 ]] && share_libraries
echo "[get-services] Done. Start them from the Environment screen."
