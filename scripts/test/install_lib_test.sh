#!/usr/bin/env bash
# Tests of scripts/lib/install-lib.sh: detection and component states.
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

source "$(dirname "${BASH_SOURCE[0]}")/machine.sh"

states() { # id=state for every component
    local id out=""
    for id in "${IL_COMPONENTS[@]}"; do il_check "$id"; out+="$id=$IL_STATE "; done
    echo "$out"
}

run_lib() { # runs the body in a subshell with the library loaded
    ( source "$TEST_REPO/scripts/lib/install-lib.sh"; il_detect_platform; "$@" )
}

# --- a complete machine -------------------------------------------------------
sandbox; complete_machine
out="$(run_lib states)"
for id in git cmake cc make curl ffmpeg go cuda sd piper voice acestep ytdlp gsap hflibs hfblocks node hfruntime upscalers browser blender model veritmo ollama; do
    assert_contains "complete machine: $id" "$out" "$id=ok "
done
rep="$(run_lib il_report)"
assert_contains "report shows go version" "$rep" "1.22.3"
assert_contains "report shows both GPUs" "$rep" "RTX 5060 Ti, compute capability 12.0"
assert_contains "report shows nvcc" "$rep" "nvcc         : 12.8"
cleanup_sandbox

# --- a real ffmpeg prints thousands of lines: the libx264 test must not die of SIGPIPE ---
sandbox; complete_machine
stub ffmpeg 'case "$*" in *-encoders*) echo " V....D libx264 H.264"; for i in $(seq 1 20000); do echo " V....D other$i  filler line to make the output long"; done;; *) echo "ffmpeg version 9.0.1";; esac'
out="$(set -o pipefail; run_lib states)"
assert_contains "long ffmpeg output with libx264 first is ok under pipefail" "$out" "ffmpeg=ok "
cleanup_sandbox

# --- no nvcc ------------------------------------------------------------------
sandbox; complete_machine; rm "$T_BIN/nvcc"
out="$(run_lib states)"
assert_contains "no nvcc: cuda missing" "$out" "cuda=missing "
rep="$(run_lib il_report)"
assert_contains "no nvcc: report points at the guide" "$rep" "docs/INSTALL.md#nvidia-driver-and-cuda"
cleanup_sandbox

# --- old nvcc -----------------------------------------------------------------
sandbox; complete_machine; stub nvcc 'echo "Cuda compilation tools, release 12.4, V12.4.131"'
out="$(run_lib states)"
assert_contains "nvcc 12.4 is old" "$out" "cuda=old "
rep="$(run_lib il_report)"
assert_contains "old nvcc: required version stated" "$rep" "need >= 12.8"
cleanup_sandbox

# --- no NVIDIA GPU ------------------------------------------------------------
sandbox; complete_machine; rm "$T_BIN/nvidia-smi" "$T_BIN/nvcc"
out="$(run_lib states)"
assert_contains "no GPU: cuda is n/a" "$out" "cuda=n/a "
rep="$(run_lib il_report)"
assert_contains "no GPU: said in the report" "$rep" "no NVIDIA GPU found"
cleanup_sandbox

# --- old Go ---------------------------------------------------------------------
sandbox; complete_machine; stub go 'echo "go version go1.19.8 linux/amd64"'
out="$(run_lib states)"
assert_contains "go 1.19 is old" "$out" "go=old "
cleanup_sandbox

# --- the dashboard is not a component of this repository any more ----------------
sandbox; complete_machine
out="$(run_lib states)"
assert_not_contains "no dashboard component" "$out" "dashboard="
assert_contains "node is only the tool that builds the HyperFrames runtime" "$out" "node=ok"
stub node 'echo "v18.19.0"'
out="$(run_lib states)"
assert_contains "an old node is reported" "$out" "node=old"
assert_not_contains "no dashboard component" "$out" "dashboard="
cleanup_sandbox

# --- .NET: the SDK is not enough, the services need the ASP.NET Core runtime ----
sandbox; complete_machine
stub dotnet 'case "$1" in --version) echo "10.0.113";; --list-runtimes) echo "Microsoft.NETCore.App 10.0.13 [/usr/share/dotnet/shared/Microsoft.NETCore.App]";; esac'
out="$(run_lib states)"
assert_contains "the SDK without the ASP.NET runtime is missing" "$out" "dotnet=missing "
stub dotnet 'case "$1" in --version) echo "10.0.113";; --list-runtimes) echo "Microsoft.AspNetCore.App 10.0.13 [/usr/share/dotnet/shared/Microsoft.AspNetCore.App]"; echo "Microsoft.NETCore.App 10.0.13 [/usr/share/dotnet/shared/Microsoft.NETCore.App]";; esac'
out="$(run_lib states)"
assert_contains "the SDK with the ASP.NET runtime is ok" "$out" "dotnet=ok "
stub dotnet 'case "$1" in --version) echo "7.0.400";; --list-runtimes) echo "Microsoft.AspNetCore.App 7.0.10 [/x]";; esac'
out="$(run_lib states)"
assert_contains "an old SDK is old" "$out" "dotnet=old "
cleanup_sandbox

# --- missing project artifacts ------------------------------------------------
sandbox; complete_machine; rm "$IL_ROOT/bin/sd" "$IL_ROOT/voices/v.onnx.json" "$IL_ROOT/models/m.safetensors" "$IL_ROOT/bin/ace-lm"
out="$(run_lib states)"
assert_contains "sd missing" "$out" "sd=missing "
assert_contains "voice needs its json" "$out" "voice=missing "
assert_contains "model missing" "$out" "model=missing "
assert_contains "acestep needs both programs" "$out" "acestep=missing "
cleanup_sandbox

# --- a binary that does not run is not ok --------------------------------------
sandbox; complete_machine; fake_repo_file bin/sd '#!/usr/bin/env bash
exit 1'
out="$(run_lib states)"
assert_contains "a silent failing sd is missing" "$out" "sd=missing "
cleanup_sandbox

# --- optional components and the required summary ------------------------------
sandbox; complete_machine; rm "$T_BIN/ollama" "$IL_ROOT/bin/yt-dlp" "$IL_ROOT/bin/ace-synth" "$IL_ROOT/third_party/gsap/gsap.min.js"
out="$(run_lib states)"
assert_contains "ollama missing" "$out" "ollama=missing "
assert_contains "ytdlp missing" "$out" "ytdlp=missing "
assert_contains "gsap missing" "$out" "gsap=missing "
rm "$IL_ROOT/bin/rife/rife-ncnn-vulkan"
out="$(run_lib states)"
assert_contains "one of the two upscalers is not installed" "$out" "upscalers=missing "
rm -rf "$IL_ROOT/bin/realesrgan" "$IL_ROOT/bin/rife"
out="$(run_lib states)"
assert_contains "no upscaler at all is missing too" "$out" "upscalers=missing "
assert_eq "optional components do not fail the required check" "yes" "$(run_lib bash -c 'true' >/dev/null; ( source "$TEST_REPO/scripts/lib/install-lib.sh"; il_detect_platform; il_required_all_ok && echo yes || echo no ))"
rm "$T_BIN/ffmpeg"
assert_eq "a missing required component fails it" "no" "$( ( source "$TEST_REPO/scripts/lib/install-lib.sh"; il_detect_platform; il_required_all_ok && echo yes || echo no ) )"
cleanup_sandbox

# --- the browser for motion graphics: any of the names the renderer looks for, optional ----
sandbox; complete_machine
rep="$(run_lib il_report)"
assert_contains "report shows the browser and its version" "$rep" "126.0.6478.55"
rm "$T_BIN/google-chrome-stable"
out="$(run_lib states)"
assert_contains "no browser: missing" "$out" "browser=missing "
assert_eq "a missing browser does not fail the required check" "yes" "$( ( source "$TEST_REPO/scripts/lib/install-lib.sh"; il_detect_platform; il_required_all_ok && echo yes || echo no ) )"
stub chromium 'echo "Chromium 127.0.1.2"'
out="$(run_lib states)"
assert_contains "chromium counts" "$out" "browser=ok "
cleanup_sandbox

# --- Blender for the 3D scenes: optional, never required, and too old is reported as old ----
sandbox; complete_machine
rep="$(run_lib il_report)"
assert_contains "report shows Blender and its version" "$rep" "5.2.3"
assert_eq "Blender is optional" "optional" "$(run_lib il_required blender)"
rm "$T_BIN/blender"
out="$(run_lib states)"
assert_contains "no Blender: missing" "$out" "blender=missing "
assert_eq "a missing Blender does not fail the required check" "yes" "$( ( source "$TEST_REPO/scripts/lib/install-lib.sh"; il_detect_platform; il_required_all_ok && echo yes || echo no ) )"
stub blender 'echo "Blender 4.1.1"'
out="$(run_lib states)"
assert_contains "an old Blender is old" "$out" "blender=old "
assert_eq "an old Blender does not fail the required check" "yes" "$( ( source "$TEST_REPO/scripts/lib/install-lib.sh"; il_detect_platform; il_required_all_ok && echo yes || echo no ) )"
cleanup_sandbox

# --- ollama not running vs model not pulled ------------------------------------
sandbox; complete_machine
stub ollama 'case "$1" in --version) echo "ollama version is 0.5.0";; list) exit 1;; esac'
rep="$(run_lib il_report)"
assert_contains "ollama not running" "$rep" "Ollama is not running"
stub ollama 'case "$1" in --version) echo "ollama version is 0.5.0";; list) echo "NAME ID";; esac'
rep="$(run_lib il_report)"
assert_contains "model not pulled shows the pull command" "$rep" "ollama pull qwen3:8b"
cleanup_sandbox

# --- platform ------------------------------------------------------------------
sandbox; complete_machine
printf 'NAME="Debian GNU/Linux"\nID=debian\nPRETTY_NAME="Debian GNU/Linux 12 (bookworm)"\n' >"$T_DIR/os-release"
stub apt-get 'true'
export IL_OS_RELEASE="$T_DIR/os-release"
rep="$(run_lib il_report)"
assert_contains "distribution name" "$rep" "Debian GNU/Linux 12 (bookworm)"
assert_contains "package manager apt" "$rep" "package manager: apt"
stub pacman 'true'
assert_contains "pacman wins when present" "$(run_lib il_report)" "package manager: pacman"
rm "$T_BIN/pacman" "$T_BIN/apt-get"
assert_contains "no supported package manager" "$(run_lib il_report)" "none supported"
printf 'MemTotal:       16384000 kB\n' >"$T_DIR/meminfo"
export IL_MEMINFO="$T_DIR/meminfo"
assert_contains "memory is read" "$(run_lib il_report)" "memory       : 16000 MB"
cleanup_sandbox

# --- the cache directory of the former name moves once to the new one ---
sandbox; complete_machine
unset IL_LOG_DIR
mkdir -p "$HOME/.cache/mediaorquestator"; echo "old log" >"$HOME/.cache/mediaorquestator/install.log"
out="$(run_lib true 2>&1)"
assert_contains "the old cache directory is announced as moved" "$out" "moved $HOME/.cache/mediaorquestator to $HOME/.cache/veritmo"
assert_eq "the log travelled with it" "old log" "$(cat "$HOME/.cache/veritmo/install.log")"
assert_eq "the old directory is gone" "no" "$([ -e "$HOME/.cache/mediaorquestator" ] && echo yes || echo no)"
mkdir -p "$HOME/.cache/mediaorquestator"
out="$(run_lib true 2>&1)"
assert_contains "both directories: the old one is ignored with a warning" "$out" "is ignored"
assert_eq "and untouched" "yes" "$([ -d "$HOME/.cache/mediaorquestator" ] && echo yes || echo no)"
cleanup_sandbox

pass_summary
