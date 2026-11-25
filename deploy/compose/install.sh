#!/usr/bin/env bash
# The installation step of the Compose stack: runs, in the `tools` image, the pinned install scripts of the repository and writes what they build or
# download into the folders of the host mounted under /app (bin/, third_party/, models/, voices/, hyperframes/). It is repeatable: what is already
# installed is not built again, and it says what is missing before it changes anything.
#
#   docker compose --profile install run --rm installer            # the components below
#   docker compose --profile install run --rm installer check      # only says what is installed and what is missing
#   INSTALL_COMPONENTS="sd piper" docker compose --profile install run --rm installer
#
# Components (INSTALL_COMPONENTS, space separated; default: the required ones):
#   required   sd piper ytdlp gsap hflibs hfruntime acestep chrome
#   optional   whisper demucs upscalers hfblocks models
set -euo pipefail
cd /app
mkdir -p "${HOME:-/tmp/home}"
COMPONENTS=${INSTALL_COMPONENTS:-"sd piper ytdlp gsap hflibs hfruntime acestep chrome"}
MODE=${1:-install}

# present NAME: whether the component is already installed (the file or folder its script leaves)
present() {
    case "$1" in
        sd)        [[ -x bin/sd ]] ;;
        piper)     [[ -x bin/piper ]] ;;
        ytdlp)     [[ -x bin/yt-dlp ]] ;;
        gsap)      [[ -s third_party/gsap/gsap.min.js ]] ;;
        hflibs)    [[ -d third_party/hyperframes-libs ]] && [[ -n "$(ls -A third_party/hyperframes-libs 2>/dev/null)" ]] ;;
        hfruntime) [[ -s third_party/hyperframes-runtime/hyperframe.runtime.iife.js ]] ;;
        acestep)   [[ -x bin/ace-synth && -x bin/ace-lm ]] ;;
        chrome)    [[ -x bin/chromium && -x bin/chrome-for-testing/chrome-linux64/chrome ]] ;;
        hfblocks)  [[ -d hyperframes/blocks ]] && [[ -n "$(ls -A hyperframes/blocks 2>/dev/null)" ]] ;;
        whisper)   [[ -x bin/whisper-cli ]] ;;
        demucs)    [[ -x bin/demucs ]] ;;
        upscalers) [[ -x bin/realesrgan ]] && [[ -x bin/rife ]] ;;
        models)    [[ -n "$(ls -A models 2>/dev/null)" ]] ;;
        *)         echo "install: unknown component '$1'" >&2; exit 2 ;;
    esac
}

# script NAME: the command that installs it
script() {
    case "$1" in
        sd)        echo "bash scripts/build-sd.sh" ;;
        piper)     echo "bash scripts/get-piper.sh" ;;
        ytdlp)     echo "bash scripts/get-ytdlp.sh" ;;
        gsap)      echo "bash scripts/get-gsap.sh" ;;
        hflibs)    echo "bash scripts/get-hyperframes-libs.sh" ;;
        hfruntime) echo "bash scripts/get-hyperframes-runtime.sh" ;;
        acestep)   echo "bash scripts/build-acestep.sh" ;;
        chrome)    echo "bash scripts/get-chrome.sh" ;;
        hfblocks)  echo "bash scripts/get-hyperframes-blocks.sh" ;;
        whisper)   echo "bash scripts/get-whisper.sh" ;;
        demucs)    echo "bash scripts/get-demucs.sh" ;;
        upscalers) echo "bash scripts/get-upscalers.sh" ;;
        models)    echo "bash scripts/get-models.sh" ;;
    esac
}

missing=()
echo "installation elements (host folders under /app):"
for c in $COMPONENTS; do
    if present "$c"; then
        printf '  %-10s installed\n' "$c"
    else
        printf '  %-10s MISSING\n' "$c"
        missing+=("$c")
    fi
done
if [[ ${#missing[@]} -eq 0 ]]; then
    echo "nothing to install: everything asked for is installed, nothing was built."
    exit 0
fi
if [[ "$MODE" == "check" ]]; then
    echo "missing: ${missing[*]}"
    exit 1
fi
mkdir -p bin third_party models voices hyperframes
for c in "${missing[@]}"; do
    echo
    echo "== installing $c: $(script "$c")"
    $(script "$c")
done
echo
echo "done: ${missing[*]} installed."
