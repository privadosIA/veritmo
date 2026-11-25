#!/usr/bin/env bash
# scripts/lib/install-lib.sh
#
# Detection and component table of scripts/install.sh. Sourced, never run:
# it defines functions and defaults and has no side effects. Every check looks
# at the real thing (the binary runs, the file exists), so a re-run after an
# interruption simply sees what is still missing.
#
# Test seams (environment): IL_ROOT (the project directory), IL_OS_RELEASE,
# IL_MEMINFO, IL_LOG_DIR. Commands are found on PATH, so tests put stubs first.

IL_ROOT="${IL_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}"
IL_OS_RELEASE="${IL_OS_RELEASE:-/etc/os-release}"
IL_MEMINFO="${IL_MEMINFO:-/proc/meminfo}"
# The cache directory of the former name (mediaorquestator) moves once to the new one: a rename, nothing is copied or deleted (the binary does the same).
il_adopt_old_dir() {
    local base="$1"
    [ -d "$base/mediaorquestator" ] || return 0
    if [ ! -e "$base/veritmo" ]; then
        mv "$base/mediaorquestator" "$base/veritmo" && echo "moved $base/mediaorquestator to $base/veritmo"
    else
        echo "warning: $base/mediaorquestator is ignored ($base/veritmo exists); it may be deleted" >&2
    fi
}
[ -n "${IL_LOG_DIR:-}" ] || il_adopt_old_dir "${HOME:-/tmp}/.cache"
IL_LOG_DIR="${IL_LOG_DIR:-${HOME:-/tmp}/.cache/veritmo}"
IL_LOG="${IL_LOG:-$IL_LOG_DIR/install.log}"

# Minimum versions (see docs/INSTALL.md).
IL_MIN_GO="1.22"
IL_MIN_CMAKE="3.18"
IL_MIN_NVCC="12.8"
IL_MIN_DOTNET="8.0"
IL_MIN_NODE="20"        # builds the runtime of HyperFrames (scripts/get-hyperframes-runtime.sh)
IL_MIN_BLENDER="4.2"   # internal/video/blender (MinMajor.MinMinor)
IL_ASSISTANT_MODEL="qwen3:8b"

# Components, in install order.
IL_COMPONENTS=(git cmake cc make curl ffmpeg go cuda sd piper voice acestep dotnet services ytdlp gsap hflibs upscalers browser blender model veritmo hfblocks node hfruntime ollama)

# ---------------------------------------------------------------------------
# helpers
# ---------------------------------------------------------------------------

il_have() { command -v "$1" >/dev/null 2>&1; }

# il_ver_ge A B: true when version A >= version B.
il_ver_ge() {
    [[ "$(printf '%s\n%s\n' "$2" "$1" | sort -V | head -n1)" == "$2" ]]
}

# il_first_version: the first dotted number in the input (stdin).
il_first_version() { grep -oE '[0-9]+(\.[0-9]+)+' | head -n1; }

# ---------------------------------------------------------------------------
# platform
# ---------------------------------------------------------------------------

il_detect_platform() {
    IL_DISTRO_ID="unknown"
    IL_DISTRO_NAME="unknown"
    IL_DISTRO_LIKE=""
    if [[ -r "$IL_OS_RELEASE" ]]; then
        local k v
        while IFS='=' read -r k v; do
            v="${v%\"}"; v="${v#\"}"
            case "$k" in
                ID) IL_DISTRO_ID="$v" ;;
                ID_LIKE) IL_DISTRO_LIKE="$v" ;;
                PRETTY_NAME) IL_DISTRO_NAME="$v" ;;
            esac
        done <"$IL_OS_RELEASE"
    fi
    IL_ARCH="$(uname -m 2>/dev/null || echo unknown)"
    IL_PM=""
    if il_have pacman; then IL_PM="pacman"
    elif il_have apt-get; then IL_PM="apt"
    elif il_have dnf; then IL_PM="dnf"
    fi
    IL_MEM_MB=0
    if [[ -r "$IL_MEMINFO" ]]; then
        IL_MEM_MB=$(awk '/^MemTotal:/{printf "%d", $2/1024}' "$IL_MEMINFO")
    fi
    IL_DISK_MB=$(df -Pk "$IL_ROOT" 2>/dev/null | awk 'NR==2{printf "%d", $4/1024}')
    IL_DISK_MB="${IL_DISK_MB:-0}"
    il_detect_gpus
    IL_NVCC=""
    if il_have nvcc; then
        IL_NVCC="$(nvcc --version 2>/dev/null | grep -oP 'release \K[0-9]+\.[0-9]+' | head -n1)"
    fi
}

# il_detect_gpus fills IL_GPUS with one "index|name|compute_cap|memory_mb" per GPU.
il_detect_gpus() {
    IL_GPUS=()
    il_have nvidia-smi || return 0
    local line
    while IFS= read -r line; do
        [[ -n "$line" ]] || continue
        IL_GPUS+=("$(printf '%s' "$line" | awk -F', *' '{printf "%s|%s|%s|%s", $1, $2, $3, $4}')")
    done < <(nvidia-smi --query-gpu=index,name,compute_cap,memory.total --format=csv,noheader,nounits 2>/dev/null)
}

il_has_gpu() { (( ${#IL_GPUS[@]} > 0 )); }

# Tiers of the music services (openspec plan-services-installation, 1.3): what each builds, and which one fits this machine.
#   none         nothing
#   recognition  LAION-CLAP (the sound classifier, about 600 MB of weights) and video-vision (CPU, 250 MB)
#   generator    recognition and one song generator, DiffRhythm2 (it also brings the MuQ-MuLan recognition)
#   all          recognition and both generators (HeartMuLa too)
il_tier_services() { # tier: the services it builds, comma separated ("" for none)
    case "$1" in
        none) echo "" ;;
        recognition) echo "lionclap,videovision" ;;
        generator) echo "lionclap,videovision,diffrhythm2" ;;
        all) echo "lionclap,videovision,diffrhythm2,heartmula" ;;
        *) return 1 ;;
    esac
}

# il_recommended_tier: without an NVIDIA GPU only the recognition (CLAP runs at about a second per clip on the CPU; a generator
# is about 17 times slower than real time there); with one, "all" when a card has 15 GB or more (HeartMuLa's model and codec
# did not fit one 12 GB card in the service) and "generator" otherwise.
il_recommended_tier() {
    il_has_gpu || { echo recognition; return; }
    local g mem max=0
    for g in "${IL_GPUS[@]}"; do
        mem="${g##*|}"; mem="${mem%%.*}"
        [[ "$mem" =~ ^[0-9]+$ ]] && (( mem > max )) && max=$mem
    done
    if (( max >= 15000 )); then echo all; else echo generator; fi
}

# il_services_tier: the tier in force: the one chosen (SERVICES_TIER) or the recommended one.
il_services_tier() { echo "${SERVICES_TIER:-$(il_recommended_tier)}"; }

# Tiers of the 3D services (one picture to a 3D model), apart from the music ones because they answer to other things: the card.
#   none      nothing
#   triposr   TripoSR
#   all       TripoSR and Stable Fast 3D (its weights are gated: the service builds without the licence, the weights need the
#             licence accepted on Hugging Face and the key in the Models screen; without them it says so when asked for a model)
il_tier3d_services() { # tier: the services it builds, comma separated ("" for none)
    case "$1" in
        none) echo "" ;;
        triposr) echo "triposr" ;;
        all) echo "triposr,stable-fast-3d" ;;
        *) return 1 ;;
    esac
}

# il_recommended_3d: Stable Fast 3D was measured at 8 GB of VRAM, so "all" from a card of 8 GB or more and nothing without one
# (a 3D model on the CPU is not a thing worth building for).
il_recommended_3d() {
    il_has_gpu || { echo none; return; }
    local g mem max=0
    for g in "${IL_GPUS[@]}"; do
        mem="${g##*|}"; mem="${mem%%.*}"
        [[ "$mem" =~ ^[0-9]+$ ]] && (( mem > max )) && max=$mem
    done
    if (( max >= 8000 )); then echo all; else echo none; fi
}

il_services_3d() { echo "${SERVICES_3D:-$(il_recommended_3d)}"; }

# il_services_list: every service the tiers in force build, comma separated.
il_services_list() {
    local a b; a="$(il_tier_services "$(il_services_tier)")"; b="$(il_tier3d_services "$(il_services_3d)")"
    if [[ -n "$a" && -n "$b" ]]; then echo "$a,$b"; else echo "$a$b"; fi
}

# ---------------------------------------------------------------------------
# component metadata
# ---------------------------------------------------------------------------

il_title() {
    case "$1" in
        git) echo "git" ;;
        cmake) echo "cmake" ;;
        cc) echo "C/C++ compiler" ;;
        make) echo "make" ;;
        curl) echo "curl" ;;
        ffmpeg) echo "ffmpeg + ffprobe" ;;
        go) echo "Go" ;;
        node) echo "Node.js and npm (to build the runtime of HyperFrames)" ;;
        cuda) echo "CUDA toolkit (nvcc)" ;;
        sd) echo "bin/sd (stable-diffusion.cpp)" ;;
        piper) echo "bin/piper (text to speech)" ;;
        voice) echo "a Piper voice (voices/)" ;;
        acestep) echo "bin/ace-lm, bin/ace-synth (music)" ;;
        dotnet) echo ".NET SDK and ASP.NET Core runtime (build and run the music services)" ;;
        services) echo "bin/services (LAION-CLAP, video-vision, DiffRhythm2, HeartMuLa: by tier)" ;;
        ytdlp) echo "bin/yt-dlp" ;;
        gsap) echo "third_party/gsap/gsap.min.js (timelines in parts and scenes)" ;;
        hfruntime) echo "third_party/hyperframes-runtime/ (the runtime of HyperFrames, built here: the components need it; Node is needed to build it)" ;;
        hfblocks) echo "hyperframes/blocks/ (the 210 HyperFrames blocks, with their libraries)" ;;
        hflibs) echo "third_party/hyperframes-libs/ (the libraries the HyperFrames blocks load, kept local)" ;;
        upscalers) echo "bin/realesrgan, bin/rife (better scale and frame rate of a video)" ;;
        browser) echo "Chrome or Chromium (motion graphics)" ;;
        blender) echo "Blender (3D scenes)" ;;
        model) echo "an image model (models/)" ;;
        veritmo) echo "bin/veritmo" ;;
        ollama) echo "Ollama (prompt assistant)" ;;
    esac
}

# il_required ID: "required" or "optional". The CUDA toolkit is only required
# where there is an NVIDIA GPU (see il_check).
il_required() {
    case "$1" in
        acestep|dotnet|services|ytdlp|gsap|hflibs|hfblocks|node|hfruntime|upscalers|browser|blender|ollama) echo optional ;;
        *) echo required ;;
    esac
}

# il_anchor ID: the section of docs/INSTALL.md that explains the component.
il_anchor() {
    case "$1" in
        git|cmake|cc|make|curl) echo "docs/INSTALL.md#system-tools" ;;
        ffmpeg) echo "docs/INSTALL.md#ffmpeg" ;;
        go) echo "docs/INSTALL.md#go" ;;
        node) echo "docs/INSTALL.md#hyperframes-runtime" ;;
        cuda) echo "docs/INSTALL.md#nvidia-driver-and-cuda" ;;
        sd) echo "docs/INSTALL.md#stable-diffusioncpp-sd" ;;
        piper|voice) echo "docs/INSTALL.md#piper-and-a-voice" ;;
        acestep) echo "docs/INSTALL.md#music-engine-acestepcpp" ;;
        dotnet|services) echo "docs/INSTALL.md#music-services-net" ;;
        ytdlp) echo "docs/INSTALL.md#yt-dlp" ;;
        gsap) echo "docs/INSTALL.md#gsap-timelines-in-parts-and-scenes" ;;
        hflibs) echo "docs/INSTALL.md#hyperframes-libraries" ;;
        hfblocks) echo "docs/INSTALL.md#hyperframes-blocks" ;;
        hfruntime) echo "docs/INSTALL.md#hyperframes-runtime" ;;
        upscalers) echo "docs/INSTALL.md#video-upscalers-real-esrgan-and-rife" ;;
        browser) echo "docs/INSTALL.md#chrome-or-chromium" ;;
        blender) echo "docs/INSTALL.md#blender" ;;
        model) echo "docs/INSTALL.md#models" ;;
        veritmo) echo "docs/INSTALL.md#the-veritmo-binary" ;;
        ollama) echo "docs/INSTALL.md#ollama-prompt-assistant" ;;
    esac
}

# ---------------------------------------------------------------------------
# checks: il_check ID sets IL_STATE (ok|old|missing|n/a), IL_VER and IL_NOTE
# ---------------------------------------------------------------------------

il_tool_check() { # id cmd min
    local cmd="$2" min="$3"
    IL_VER=""; IL_NOTE=""
    if ! il_have "$cmd"; then IL_STATE=missing; return; fi
    IL_VER="$("$cmd" --version 2>/dev/null | head -n1 | il_first_version)"
    if [[ -n "$min" && -n "$IL_VER" ]] && ! il_ver_ge "$IL_VER" "$min"; then
        IL_STATE=old; IL_NOTE="need >= $min"
    else
        IL_STATE=ok
    fi
}

il_runs() { # path args...: the binary exists, is executable and prints something
    local bin="$1"; shift
    [[ -x "$bin" ]] || return 1
    local out
    out="$(timeout 15 "$bin" "$@" 2>&1 | head -n3)" || true
    [[ -n "$out" ]]
}

il_check() {
    IL_STATE=missing; IL_VER=""; IL_NOTE=""
    local id="$1"
    case "$id" in
        git) il_tool_check git git "" ;;
        cmake) il_tool_check cmake cmake "$IL_MIN_CMAKE" ;;
        make) il_tool_check make make "" ;;
        blender) il_tool_check blender blender "$IL_MIN_BLENDER" ;;
        curl) il_tool_check curl curl "" ;;
        cc)
            if { il_have c++ && il_have cc; } || { il_have g++ && il_have gcc; } || { il_have clang++ && il_have clang; }; then
                IL_STATE=ok
                local c
                for c in g++ c++ clang++; do il_have "$c" && { IL_VER="$("$c" --version 2>/dev/null | head -n1 | il_first_version)"; break; }; done
            fi ;;
        ffmpeg)
            if il_have ffmpeg && il_have ffprobe; then
                IL_VER="$(ffmpeg -version 2>/dev/null | head -n1 | il_first_version)"
                # The clip phase encodes with libx264; some distributions ship an ffmpeg without it.
                # (captured, not piped into `grep -q`: with pipefail the SIGPIPE of an early exit would fail the test)
                local encoders; encoders="$(ffmpeg -hide_banner -encoders 2>/dev/null)"
                if [[ "$encoders" == *" libx264 "* ]]; then IL_STATE=ok
                else IL_STATE=old; IL_NOTE="no libx264 encoder: a full ffmpeg build is needed"; fi
            elif il_have ffmpeg; then IL_NOTE="ffprobe is missing"
            fi ;;
        node)
            if il_have node && il_have npm; then
                IL_VER="$(node --version 2>/dev/null | head -n1 | il_first_version)"
                if [[ -n "$IL_VER" ]] && ! il_ver_ge "$IL_VER" "$IL_MIN_NODE"; then IL_STATE=old; IL_NOTE="need >= $IL_MIN_NODE"; else IL_STATE=ok; fi
            elif il_have node; then IL_NOTE="npm is missing"
            fi ;;
        go)
            if il_have go; then
                IL_VER="$(go version 2>/dev/null | grep -oE 'go[0-9]+(\.[0-9]+)+' | head -n1)"; IL_VER="${IL_VER#go}"
                if [[ -n "$IL_VER" ]] && ! il_ver_ge "$IL_VER" "$IL_MIN_GO"; then IL_STATE=old; IL_NOTE="need >= $IL_MIN_GO"; else IL_STATE=ok; fi
            fi ;;
        cuda)
            if ! il_has_gpu; then
                IL_STATE="n/a"; IL_NOTE="no NVIDIA GPU found (renders need one)"
            elif [[ -z "$IL_NVCC" ]]; then
                IL_STATE=missing; IL_NOTE="need >= $IL_MIN_NVCC"
            elif il_ver_ge "$IL_NVCC" "$IL_MIN_NVCC"; then
                IL_STATE=ok; IL_VER="$IL_NVCC"
            else
                IL_STATE=old; IL_VER="$IL_NVCC"; IL_NOTE="need >= $IL_MIN_NVCC"
            fi ;;
        sd)
            if il_runs "$IL_ROOT/bin/sd" --version; then
                IL_STATE=ok; IL_VER="$("$IL_ROOT/bin/sd" --version 2>&1 | head -n1 | grep -oE 'master-[0-9]+' | head -n1)"
            fi ;;
        piper) il_runs "$IL_ROOT/bin/piper" --help && IL_STATE=ok ;;
        voice)
            local f
            for f in "$IL_ROOT"/voices/*.onnx; do
                [[ -f "$f" && -f "$f.json" ]] && { IL_STATE=ok; IL_VER="$(basename "$f" .onnx)"; break; }
            done ;;
        acestep)
            [[ -x "$IL_ROOT/bin/ace-lm" && -x "$IL_ROOT/bin/ace-synth" ]] && IL_STATE=ok ;;
        dotnet)
            if il_have dotnet; then
                IL_VER="$(dotnet --version 2>/dev/null | head -n1 | il_first_version)"
                if [[ -n "$IL_VER" ]] && il_ver_ge "$IL_VER" "$IL_MIN_DOTNET"; then
                    # the services are web applications: without the ASP.NET Core runtime they stop at start
                    # with "No frameworks were found" (Arch's dotnet-sdk brings only dotnet-runtime)
                    if dotnet --list-runtimes 2>/dev/null | grep -q '^Microsoft.AspNetCore.App '; then IL_STATE=ok
                    else IL_STATE=missing; IL_NOTE="needs the ASP.NET Core runtime to run the services"; fi
                else IL_STATE=old; IL_NOTE="need the SDK >= $IL_MIN_DOTNET"; fi
            fi ;;
        services)
            # ok when every service of the tier in force is built (tier "none": nothing is asked for)
            local n have=0 miss="" want tier
            tier="$(il_services_tier)"; [[ "$(il_services_3d)" == none ]] || tier="$tier + 3D $(il_services_3d)"
            want="$(il_services_list | tr ',' ' ')"
            for n in $want; do
                if [[ -d "$IL_ROOT/bin/services/$n" ]] && ls "$IL_ROOT/bin/services/$n"/*.dll >/dev/null 2>&1; then have=$((have+1)); else miss+="$n "; fi
            done
            if [[ -z "$want" ]]; then IL_STATE=ok; IL_NOTE="tier none: nothing asked for"
            elif [[ -z "$miss" ]]; then IL_STATE=ok; IL_NOTE="tier $tier"
            elif (( have > 0 )); then IL_STATE=missing; IL_NOTE="tier $tier, not built: ${miss% }"
            else IL_NOTE="tier $tier (source in dotnet/; weights from Models > Music)"; fi ;;
        browser)
            # the same names the renderer looks for (internal/video/motion), in the same order
            local b
            for b in google-chrome-stable google-chrome chromium chromium-browser; do
                if il_have "$b"; then
                    IL_STATE=ok; IL_VER="$("$b" --version 2>/dev/null | il_first_version)"; IL_NOTE="$b"; break
                fi
            done ;;
        upscalers)
            # both programs and the model files they are used with; they print their usage and exit non-zero without arguments,
            # so what is looked at is that they are there and can be run at all
            if [[ -x "$IL_ROOT/bin/realesrgan/realesrgan-ncnn-vulkan" && -f "$IL_ROOT/bin/realesrgan/models/realesr-animevideov3-x2.bin" \
                && -x "$IL_ROOT/bin/rife/rife-ncnn-vulkan" && -f "$IL_ROOT/bin/rife/rife-v4.6/flownet.bin" ]]; then
                IL_STATE=ok
            elif [[ -x "$IL_ROOT/bin/realesrgan/realesrgan-ncnn-vulkan" || -x "$IL_ROOT/bin/rife/rife-ncnn-vulkan" ]]; then
                IL_STATE=missing; IL_NOTE="only one of the two is installed"
            fi ;;
        gsap) [[ -s "$IL_ROOT/third_party/gsap/gsap.min.js" ]] && { IL_STATE=ok; IL_VER="$(sed -n 's/^ \* GSAP //p' "$IL_ROOT/third_party/gsap/gsap.min.js" 2>/dev/null | head -n1)"; } ;;
        hfruntime)
            [[ -s "$IL_ROOT/third_party/hyperframes-runtime/hyperframe.runtime.iife.js" ]] && IL_STATE=ok ;;
        hfblocks)
            local bd="${HYPERFRAMES_DIR:-$IL_ROOT/hyperframes}/blocks" n=0 f
            for f in "$bd"/*/block.json; do [[ -s "$f" ]] && n=$((n + 1)); done
            [[ $n -gt 0 ]] && { IL_STATE=ok; IL_VER="$n blocks"; } ;;
        hflibs)
            # every file of the pinned list (internal/music/hyperframes/libs.txt) is there
            local list="$IL_ROOT/internal/music/hyperframes/libs.txt" h u n=0 miss=0
            if [[ -f "$list" ]]; then
                while read -r h u; do
                    [[ -z "$h" || "$h" == \#* ]] && continue
                    n=$((n + 1)); [[ -s "$IL_ROOT/third_party/hyperframes-libs/${u#https://}" ]] || miss=$((miss + 1))
                done <"$list"
                if [[ $n -gt 0 && $miss -eq 0 ]]; then IL_STATE=ok; IL_VER="$n files"
                elif [[ $miss -gt 0 && $miss -lt $n ]]; then IL_STATE=missing; IL_NOTE="$miss of $n files are missing"; fi
            fi ;;
        ytdlp) il_runs "$IL_ROOT/bin/yt-dlp" --version && { IL_STATE=ok; IL_VER="$("$IL_ROOT/bin/yt-dlp" --version 2>/dev/null | head -n1)"; } ;;
        model)
            local m
            for m in "$IL_ROOT"/models/*.safetensors "$IL_ROOT"/models/*.gguf "$IL_ROOT"/models/*.ckpt; do
                [[ -f "$m" ]] && { IL_STATE=ok; IL_VER="$(basename "$m")"; break; }
            done ;;
        veritmo)
            il_runs "$IL_ROOT/bin/veritmo" --help && IL_STATE=ok ;;
        ollama)
            if il_have ollama; then
                IL_STATE=ok; IL_VER="$(ollama --version 2>&1 | il_first_version)"
                local listing
                if ! listing="$(ollama list 2>/dev/null)"; then
                    IL_NOTE="Ollama is not running (ollama serve); then ollama pull $IL_ASSISTANT_MODEL if needed"
                elif ! grep -q "^$IL_ASSISTANT_MODEL" <<<"$listing"; then
                    IL_NOTE="assistant model not pulled: ollama pull $IL_ASSISTANT_MODEL"
                fi
            fi ;;
    esac
}

# ---------------------------------------------------------------------------
# report
# ---------------------------------------------------------------------------

il_report() {
    echo "== Machine"
    echo "  distribution : $IL_DISTRO_NAME (package manager: ${IL_PM:-none supported})"
    echo "  architecture : $IL_ARCH"
    echo "  memory       : $IL_MEM_MB MB    free disk here: $IL_DISK_MB MB"
    if il_has_gpu; then
        local g idx name cc mem
        for g in "${IL_GPUS[@]}"; do
            IFS='|' read -r idx name cc mem <<<"$g"
            echo "  GPU $idx        : $name, compute capability $cc, $mem MB"
        done
    else
        echo "  GPU          : no NVIDIA GPU found (nvidia-smi missing or empty)"
    fi
    echo "  nvcc         : ${IL_NVCC:-not found}"
    echo
    echo "== Components"
    local id req line anchor
    for id in "${IL_COMPONENTS[@]}"; do
        il_check "$id"
        req="$(il_required "$id")"
        [[ "$id" == cuda && "$IL_STATE" == "n/a" ]] && req=optional
        line="$(printf '  %-5s %-34s %-8s' "[$IL_STATE]" "$(il_title "$id")" "$req")"
        [[ -n "$IL_VER" ]] && line+=" $IL_VER"
        [[ -n "$IL_NOTE" ]] && line+="  ($IL_NOTE)"
        echo "$line"
        if [[ "$IL_STATE" == missing || "$IL_STATE" == old ]]; then
            anchor="$(il_anchor "$id")"
            echo "        see $anchor"
        fi
    done
}

# il_required_all_ok: true when every required component is ok (or n/a).
il_required_all_ok() {
    local id bad=0
    for id in "${IL_COMPONENTS[@]}"; do
        [[ "$(il_required "$id")" == required ]] || continue
        il_check "$id"
        [[ "$IL_STATE" == ok || "$IL_STATE" == "n/a" ]] || bad=1
    done
    (( bad == 0 ))
}
