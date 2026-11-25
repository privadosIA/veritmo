#!/usr/bin/env bash
# scripts/install.sh
#
# Detects what this machine has, reports what is present, old or missing, and
# installs the missing pieces only when you choose to. Run it from anywhere:
#
#   bash scripts/install.sh              # report, then ask about each missing piece
#   bash scripts/install.sh --check      # report only; exit 0 when every required piece is ok, else 1
#   bash scripts/install.sh --dry-run    # report and the plan; runs nothing
#   bash scripts/install.sh --yes        # install everything installable, without asking
#   bash scripts/install.sh --only sd,piper     # act on the named components only
#   bash scripts/install.sh --no-models         # skip the model and voice downloads
#   bash scripts/install.sh --services TIER     # the music services to build: none, recognition, generator or all
#                                               # (default: asks; --yes takes the one that fits this machine)
#   bash scripts/install.sh --3d TIER           # the 3D services (one picture to a 3D model): none, triposr or all (+ Stable Fast 3D)
#                                               # (default: asks; --yes takes the one that fits the card: triposr from 8 GB, else none)
#   bash scripts/install.sh --accept-licence stability-community   # you read the Stability AI Community License and agree:
#                                               # the only way --yes or --3d all build Stable Fast 3D without being asked
#
# Components: git cmake cc make curl ffmpeg go cuda sd piper voice acestep
# dotnet services ytdlp gsap hflibs upscalers browser blender model veritmo hfblocks node hfruntime ollama (groups: system, models).
# Safe to re-run: every check looks at the real thing. Commands and their output
# are appended to ~/.cache/veritmo/install.log. See docs/INSTALL.md.

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib/install-lib.sh
source "$SCRIPT_DIR/lib/install-lib.sh"
# shellcheck source=lib/licence-stability.sh
source "$SCRIPT_DIR/lib/licence-stability.sh"

MODE=""        # check | dry-run | install
ASSUME_YES=0
NO_MODELS=0
CPU_SD=0
ONLY=""
SERVICES_TIER=""   # none | recognition | generator | all; empty: ask, or the recommended one
SERVICES_3D=""     # none | triposr | all; empty: ask, or the recommended one
ACCEPT_LICENCE=""  # the licence id given with --accept-licence (stability-community)

usage() { sed -n '2,/^$/p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; }

while (( $# )); do
    case "$1" in
        --check) MODE=check ;;
        --dry-run) MODE=dry-run ;;
        --yes|-y) ASSUME_YES=1 ;;
        --no-models) NO_MODELS=1 ;;
        --cpu-sd) CPU_SD=1 ;;
        --services) shift; SERVICES_TIER="${1:-}"
            il_tier_services "$SERVICES_TIER" >/dev/null 2>&1 || { echo "install: --services is none, recognition, generator or all" >&2; exit 2; } ;;
        --services=*) SERVICES_TIER="${1#--services=}"
            il_tier_services "$SERVICES_TIER" >/dev/null 2>&1 || { echo "install: --services is none, recognition, generator or all" >&2; exit 2; } ;;
        --3d) shift; SERVICES_3D="${1:-}"
            il_tier3d_services "$SERVICES_3D" >/dev/null 2>&1 || { echo "install: --3d is none, triposr or all" >&2; exit 2; } ;;
        --3d=*) SERVICES_3D="${1#--3d=}"
            il_tier3d_services "$SERVICES_3D" >/dev/null 2>&1 || { echo "install: --3d is none, triposr or all" >&2; exit 2; } ;;
        --accept-licence) shift; ACCEPT_LICENCE="${1:-}" ;;
        --accept-licence=*) ACCEPT_LICENCE="${1#--accept-licence=}" ;;
        --only) shift; ONLY="${1:-}"; [[ -n "$ONLY" ]] || { echo "install: --only needs a list" >&2; exit 2; } ;;
        --only=*) ONLY="${1#--only=}" ;;
        -h|--help) usage; exit 0 ;;
        *) echo "install: unknown option: $1 (try --help)" >&2; exit 2 ;;
    esac
    shift
done

# The only licence there is to accept is the Stability AI one, and Stable Fast 3D is built only with it accepted on purpose.
if [[ -n "$ACCEPT_LICENCE" && "$ACCEPT_LICENCE" != "$STABILITY_LICENCE_ID" ]]; then
    echo "install: --accept-licence takes $STABILITY_LICENCE_ID" >&2; exit 2
fi
if [[ "$SERVICES_3D" == all && "$ACCEPT_LICENCE" != "$STABILITY_LICENCE_ID" ]]; then
    # asked for by name without the acceptance: only a person at a terminal can give it, by typing "accept" (see il_pick_services_tier)
    if (( ASSUME_YES )) || [[ "$MODE" == dry-run ]] || [[ ! -t 0 && -z "${IL_FORCE_INTERACTIVE:-}" ]]; then
        echo "install: --3d all builds Stable Fast 3D, which is under the Stability AI Community License and is not built unless you accept it." >&2
        stability_licence_message "install:"
        exit 2
    fi
fi

# --only: names to ids (groups expand), validated before anything runs.
declare -A SELECTED=()
if [[ -n "$ONLY" ]]; then
    IFS=',' read -r -a _names <<<"$ONLY"
    for n in "${_names[@]}"; do
        case "$n" in
            system|system-tools|systools) for id in git cmake cc make curl ffmpeg; do SELECTED[$id]=1; done ;;
            models) SELECTED[model]=1 ;;
            *)
                found=0
                for id in "${IL_COMPONENTS[@]}"; do [[ "$id" == "$n" ]] && { SELECTED[$id]=1; found=1; }; done
                (( found )) || { echo "install: unknown component '$n' in --only (components: ${IL_COMPONENTS[*]}; groups: system, models)" >&2; exit 2; }
                ;;
        esac
    done
fi

il_detect_platform
il_report

if [[ "$MODE" == check ]]; then
    echo
    if il_required_all_ok; then echo "check: every required component is ok."; exit 0; fi
    echo "check: some required components are missing or old (see above)."
    exit 1
fi


# ---------------------------------------------------------------------------
# what to do
# ---------------------------------------------------------------------------

INTERACTIVE=0
if [[ -t 0 || -n "${IL_FORCE_INTERACTIVE:-}" ]]; then INTERACTIVE=1; fi

if [[ "$MODE" != dry-run ]] && (( ASSUME_YES == 0 && INTERACTIVE == 0 )); then
    echo >&2
    echo "install: no terminal to ask on. Use --yes to install everything without asking," >&2
    echo "         --check to only report, or --dry-run to see the plan." >&2
    exit 2
fi

il_selected() { # id: false when --only / --no-models leave it out
    [[ -z "$ONLY" || -n "${SELECTED[$1]:-}" ]] || return 1
    [[ "$1" == model && $NO_MODELS == 1 ]] && return 1
    return 0
}

declare -A OKNOW=()     # id -> 1 when ok now (or planned, in a dry run)

il_dep_ok() { # id: every prerequisite is ok now; sets IL_BLOCKED_BY
    local d deps=""
    case "$1" in
        sd) deps="git cmake cc make cuda" ;;
        acestep) deps="git cmake cc make cuda" ;;
        services) deps="dotnet" ;;
        piper|ytdlp|gsap|hflibs|upscalers|model) deps="curl" ;;
        veritmo) deps="go" ;;
        hfblocks) deps="git curl veritmo" ;;
        hfruntime) deps="git curl node" ;;
    esac
    IL_BLOCKED_BY=""
    for d in $deps; do
        # no NVIDIA GPU: only a CPU-only sd build can go ahead without CUDA
        if [[ "$d" == cuda && "$1" == sd && -n "${CPU_BUILD:-}" ]]; then continue; fi
        [[ -n "${OKNOW[$d]:-}" ]] || IL_BLOCKED_BY+="$d "
    done
    [[ -z "$IL_BLOCKED_BY" ]]
}

il_sudo() { # prints the sudo prefix ("" as root)
    if [[ "${EUID:-$(id -u)}" == 0 ]]; then echo ""; else echo "sudo "; fi
}

# il_pkgs ID: the packages that provide the component for the detected package manager.
il_pkgs() {
    case "$IL_PM:$1" in
        pacman:git|apt:git|dnf:git) echo git ;;
        pacman:cmake|apt:cmake|dnf:cmake) echo cmake ;;
        pacman:cc) echo gcc ;;
        apt:cc) echo build-essential ;;
        dnf:cc) echo "gcc gcc-c++" ;;
        pacman:make|apt:make|dnf:make) echo make ;;
        pacman:curl|apt:curl|dnf:curl) echo curl ;;
        pacman:ffmpeg|apt:ffmpeg|dnf:ffmpeg) echo ffmpeg ;;
        pacman:browser|apt:browser|dnf:browser) echo chromium ;;
        pacman:blender|apt:blender|dnf:blender) echo blender ;;
        pacman:node|apt:node|dnf:node) echo "nodejs npm" ;;
        pacman:go) echo go ;;
        apt:go) echo golang-go ;;
        dnf:go) echo golang ;;
        pacman:dotnet) echo "dotnet-sdk aspnet-runtime" ;;   # the services are web apps; Arch's SDK brings dotnet-runtime only
        apt:dotnet) echo dotnet-sdk-8.0 ;;
        dnf:dotnet) echo dotnet-sdk-8.0 ;;
        pacman:cuda) echo cuda ;;
        pacman:ollama) echo ollama ;;
        *) return 1 ;;
    esac
}

il_pm_cmd() { # packages...: the install command for the package manager
    local sudo; sudo="$(il_sudo)"
    case "$IL_PM" in
        pacman) echo "${sudo}pacman -S --needed --noconfirm $*" ;;
        apt) echo "${sudo}apt-get install -y $*" ;;
        dnf) echo "${sudo}dnf install -y $*" ;;
    esac
}

# il_pkg_version PKG: the version the package manager would install ("" when unknown).
il_pkg_version() {
    case "$IL_PM" in
        pacman) pacman -Si "$1" 2>/dev/null | awk '/^Version/{print $3}' | head -n1 | il_first_version ;;
        apt) apt-cache policy "$1" 2>/dev/null | awk '/Candidate:/{print $2}' | head -n1 | sed 's/^[0-9]*://' | il_first_version ;;
        dnf) dnf -q repoquery --latest-limit 1 --qf '%{version}' "$1" 2>/dev/null | head -n1 | il_first_version ;;
    esac
}

IL_APT_UPDATED=0
# il_cmds ID: prints the commands that install the component, one per line.
# Returns 1 with the reason in IL_GUIDE when it can only be explained.
il_cmds() {
    local id="$1" pkgs
    IL_GUIDE=""
    case "$id" in
        git|cmake|cc|make|curl|ffmpeg|go|node|browser|blender|dotnet)
            if [[ "$id" == ffmpeg && "$IL_PM" == dnf ]]; then
                IL_GUIDE="Fedora's own ffmpeg has no libx264, which the pipeline needs. Enable RPM Fusion and install the full build: sudo dnf install https://mirrors.rpmfusion.org/free/fedora/rpmfusion-free-release-\$(rpm -E %fedora).noarch.rpm && sudo dnf swap ffmpeg-free ffmpeg --allowerasing ($(il_anchor ffmpeg))"
                return 1
            fi
            if [[ -z "$IL_PM" ]]; then
                IL_GUIDE="no supported package manager (pacman, apt, dnf): install $(il_title "$id") by hand ($(il_anchor "$id"))"
                return 1
            fi
            pkgs="$(il_pkgs "$id")" || { IL_GUIDE="install $(il_title "$id") by hand ($(il_anchor "$id"))"; return 1; }
            # Go: do not install a package whose version is too old for the project.
            if [[ "$id" == go ]]; then
                local pv; pv="$(il_pkg_version "${pkgs%% *}")"
                if [[ -n "$pv" ]] && ! il_ver_ge "$pv" "$IL_MIN_GO"; then
                    IL_GUIDE="this distribution's Go is $pv (need >= $IL_MIN_GO): install it from https://go.dev/dl/ ($(il_anchor go))"; return 1
                fi
            fi
            # Node: the runtime of HyperFrames builds with Node 20 or newer; a package that is older is not installed.
            if [[ "$id" == node ]]; then
                local nv; nv="$(il_pkg_version nodejs)"
                if [[ -n "$nv" ]] && ! il_ver_ge "$nv" "$IL_MIN_NODE"; then
                    IL_GUIDE="this distribution's Node.js is $nv (need >= $IL_MIN_NODE): install a newer one from https://nodejs.org/ ($(il_anchor node))"; return 1
                fi
            fi
            if [[ "$IL_PM" == apt && $IL_APT_UPDATED == 0 ]]; then echo "$(il_sudo)apt-get update"; IL_APT_UPDATED=1; fi
            il_pm_cmd $pkgs ;;
        cuda)
            if [[ "$IL_PM" == pacman ]]; then
                local v; v="$(pacman -Si cuda 2>/dev/null | awk '/^Version/{print $3}' | il_first_version)"
                if [[ -n "$v" ]] && il_ver_ge "$v" "$IL_MIN_NVCC"; then il_pm_cmd cuda; return 0; fi
            fi
            IL_GUIDE="the CUDA Toolkit $IL_MIN_NVCC or newer is not in this distribution's repositories: get it from https://developer.nvidia.com/cuda-downloads, then put nvcc on PATH (usually /usr/local/cuda/bin) ($(il_anchor cuda))"
            return 1 ;;
        sd)
            if [[ -n "${CPU_BUILD:-}" ]]; then echo "SD_CUDA=OFF bash scripts/build-sd.sh"; else echo "bash scripts/build-sd.sh"; fi ;;
        piper|voice) echo "bash scripts/get-piper.sh" ;;
        acestep) echo "bash scripts/build-acestep.sh" ;;
        services)
            local only; only="$(il_services_list)"
            [[ -n "$only" ]] || return 0
            local lic=""; [[ ",$only," == *,stable-fast-3d,* ]] && lic=" --accept-licence $STABILITY_LICENCE_ID"
            if [[ -n "${CPU_BUILD:-}" ]]; then echo "bash scripts/get-services.sh --cpu --only $only$lic"; else echo "bash scripts/get-services.sh --only $only$lic"; fi ;;
        ytdlp) echo "bash scripts/get-ytdlp.sh" ;;
        gsap) echo "bash scripts/get-gsap.sh" ;;
        hflibs) echo "bash scripts/get-hyperframes-libs.sh" ;;
        hfblocks) echo "bash scripts/get-hyperframes-blocks.sh" ;;
        hfruntime) echo "bash scripts/get-hyperframes-runtime.sh" ;;
        upscalers) echo "bash scripts/get-upscalers.sh" ;;
        model) echo "bash scripts/get-models.sh" ;;
        veritmo)
            # The private edition has its own files; the public tree does not, so the tag follows them.
            if [[ -f "$IL_ROOT/internal/platform/edition/edition_private.go" ]]; then
                echo "mkdir -p bin && go build -tags private -ldflags \"-X veritmo/internal/platform/edition.Version=\$(bash scripts/version.sh)\" -o bin/veritmo ./cmd/veritmo"
            else
                echo "mkdir -p bin && go build -ldflags \"-X veritmo/internal/platform/edition.Version=\$(bash scripts/version.sh)\" -o bin/veritmo ./cmd/veritmo"
            fi ;;
        ollama)
            if [[ "$IL_PM" == pacman ]]; then il_pm_cmd ollama; return 0; fi
            IL_GUIDE="install Ollama from https://ollama.com/download (this installer never pipes a downloaded script into a shell), then: ollama pull $IL_ASSISTANT_MODEL ($(il_anchor ollama))"
            return 1 ;;
    esac
}

il_log() { mkdir -p "$IL_LOG_DIR" 2>/dev/null && printf '%s\n' "$*" >>"$IL_LOG" 2>/dev/null; return 0; }

# il_run CMD: runs a command from the project directory, showing and logging it.
il_run() {
    echo "  \$ $1"
    il_log "[$(date '+%F %T')] \$ $1"
    local st
    ( cd "$IL_ROOT" && bash -c "$1" ) 2>&1 | tee -a "$IL_LOG"
    st=${PIPESTATUS[0]}
    il_log "[exit $st]"
    return "$st"
}

# il_ask TEXT: y / n / a (all) / q (quit).
il_ask() {
    local ans
    while true; do
        printf '  %s [y/N/a(ll)/q] ' "$1"
        if ! read -r ans; then echo; return 1; fi
        case "${ans,,}" in
            y|yes) return 0 ;;
            a|all) ASSUME_YES=1; return 0 ;;
            q|quit) echo "install: stopped by the user."; exit 3 ;;
            n|no|"") return 1 ;;
        esac
    done
}

# il_pick_services_tier: the tier of the music services. Chosen with --services; otherwise asked (Enter takes the one that fits
# this machine) and, with --yes, in a dry run or with no terminal, the recommended one stays in force.
il_pick_services_tier() {
    (( ASSUME_YES )) || [[ "$MODE" == dry-run ]] || (( ! INTERACTIVE )) && return 0
    local rec ans
    if [[ -z "$SERVICES_TIER" ]]; then
        rec="$(il_recommended_tier)"
        echo "  - $(il_title services): which ones? none | recognition (LAION-CLAP, video-vision) | generator (+ DiffRhythm2) | all (+ HeartMuLa). Recommended here: $rec"
        while true; do
            printf '    tier [%s]: ' "$rec"
            read -r ans || ans=""
            ans="${ans:-$rec}"
            if il_tier_services "$ans" >/dev/null 2>&1; then SERVICES_TIER="$ans"; break; fi
            echo "    none, recognition, generator or all"
        done
    fi
    if [[ -n "$SERVICES_3D" ]]; then il_confirm_stable_fast_3d; return 0; fi
    rec="$(il_recommended_3d)"
    echo "  - 3D services (one picture to a 3D model): none | triposr | all (+ Stable Fast 3D: it needs its licence accepted here, and its weights the licence accepted on Hugging Face). Recommended here: $rec"
    while true; do
        printf '    3D [%s]: ' "$rec"
        read -r ans || ans=""
        ans="${ans:-$rec}"
        if il_tier3d_services "$ans" >/dev/null 2>&1; then SERVICES_3D="$ans"; il_confirm_stable_fast_3d; return 0; fi
        echo "    none, triposr or all"
    done
}

# il_confirm_stable_fast_3d: the tier "all" builds Stable Fast 3D, which needs its licence accepted on purpose. Without the flag the
# person reads the same message as get-services.sh and types "accept"; anything else falls back to TripoSR alone.
il_confirm_stable_fast_3d() {
    [[ "$SERVICES_3D" == all && "$ACCEPT_LICENCE" != "$STABILITY_LICENCE_ID" ]] || return 0
    local ans
    stability_licence_message "   "
    printf '    type accept to build Stable Fast 3D (anything else builds TripoSR only): '
    read -r ans || ans=""
    if [[ "$ans" == accept ]]; then ACCEPT_LICENCE="$STABILITY_LICENCE_ID"; else SERVICES_3D=triposr; echo "    TripoSR only."; fi
}

il_sudo_ok() { # a command using sudo can run: root, a cached password, or a terminal to type it in
    [[ "$1" != sudo\ * ]] && return 0
    sudo -n true 2>/dev/null && return 0
    (( INTERACTIVE )) && [[ -t 0 ]] && return 0
    return 1
}

echo
echo "== Plan"
FAILED=0
for id in "${IL_COMPONENTS[@]}"; do
    il_check "$id"
    if [[ "$IL_STATE" == ok ]]; then OKNOW[$id]=1; continue; fi
    if [[ "$id" == model && $NO_MODELS == 1 && ( -z "$ONLY" || -n "${SELECTED[model]:-}" ) ]]; then
        echo "  - $(il_title model): pending (--no-models): run scripts/get-models.sh or use the Models screen of a dashboard later."
        continue
    fi
    il_selected "$id" || continue
    state="$IL_STATE"; note="$IL_NOTE"

    # voice and piper share one script: once piper is installed the voice is too
    if [[ "$id" == voice && -n "${OKNOW[piper]:-}" ]]; then
        [[ "$(il_check voice; echo "$IL_STATE")" == ok ]] && { OKNOW[voice]=1; continue; }
    fi

    if [[ "$state" == "n/a" ]]; then
        if [[ "$id" == cuda ]]; then continue; fi
        continue
    fi

    # a machine without an NVIDIA GPU: sd only as a CPU-only build, and only on request
    if [[ "$id" == sd && "$(il_check cuda; echo "$IL_STATE")" == "n/a" ]]; then
        if (( CPU_SD )); then CPU_BUILD=1
        elif (( ASSUME_YES )) || [[ "$MODE" == dry-run ]]; then
            echo "  - $(il_title sd): no NVIDIA GPU found. A CPU-only build is possible but renders would be very slow: use --cpu-sd to build it."
            continue
        else
            echo "  - $(il_title sd): no NVIDIA GPU found. A CPU-only build is possible, but renders would be VERY slow."
            if il_ask "Build $(il_title sd) for the CPU only?"; then CPU_BUILD=1; else echo "    skipped"; continue; fi
        fi
    fi

    if [[ "$id" == services ]]; then
        il_pick_services_tier
        if [[ -z "$(il_services_list)" ]]; then echo "  - $(il_title services): skipped (tier none)"; continue; fi
        il_check services
        if [[ "$IL_STATE" == ok ]]; then OKNOW[services]=1; echo "  - $(il_title services): every service of the tier $(il_services_tier) is built"; continue; fi
        state="$IL_STATE"; note="$IL_NOTE"
    fi

    if ! il_dep_ok "$id"; then
        echo "  - $(il_title "$id"): waiting for ${IL_BLOCKED_BY% } (not installed); skipped."
        FAILED=1
        continue
    fi

    cmds=()
    if ! mapfile -t cmds < <(il_cmds "$id"); then :; fi
    apt_update_in_plan=0
    [[ "${cmds[*]:-}" == *"apt-get update"* ]] && apt_update_in_plan=1
    if [[ -z "${cmds[*]:-}" ]]; then
        # only guidance
        il_cmds "$id" >/dev/null 2>&1; guide="$IL_GUIDE"
        echo "  - $(il_title "$id"): $state. $guide"
        [[ "$(il_required "$id")" == required ]] && FAILED=1
        continue
    fi

    echo "  - $(il_title "$id"): $state${note:+ ($note)}"
    for c in "${cmds[@]}"; do echo "      would run: $c"; done
    if [[ "$MODE" == dry-run ]]; then OKNOW[$id]=1; (( apt_update_in_plan )) && IL_APT_UPDATED=1; continue; fi

    if (( ! ASSUME_YES )); then
        il_ask "Install $(il_title "$id")?" || { echo "    skipped"; [[ "$(il_required "$id")" == required ]] && FAILED=1; continue; }
    fi
    stop=0
    (( apt_update_in_plan )) && IL_APT_UPDATED=1
    for c in "${cmds[@]}"; do
        if ! il_sudo_ok "$c"; then
            echo "    sudo needs a password and there is no terminal. Run this yourself, then re-run the installer:"
            echo "      $c"
            stop=1; break
        fi
        il_run "$c" || { echo "    FAILED: $c"; stop=1; break; }
    done
    hash -r
    il_detect_platform
    il_check "$id"
    if (( stop == 0 )) && [[ "$IL_STATE" == ok ]]; then OKNOW[$id]=1; echo "    -> ok"
    else echo "    -> still $IL_STATE ($(il_anchor "$id"))"; FAILED=1; fi
done

if [[ "$MODE" == dry-run ]]; then
    echo
    echo "dry run: nothing was run."
    exit 0
fi

echo
il_report
echo
if il_required_all_ok; then echo "install: every required component is ok. Start the server with ./serve.sh"; exit 0; fi
echo "install: some required components are still missing (see above)."
exit 1
