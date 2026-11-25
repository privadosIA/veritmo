#!/usr/bin/env bash
# Tests of scripts/install.sh with stubbed tools: modes, questions, steps, sudo,
# package managers, the log and idempotence. Nothing real is installed.
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
source "$(dirname "${BASH_SOURCE[0]}")/machine.sh"

INSTALL="$TEST_REPO/scripts/install.sh"

# recording: every stubbed installer step appends to $CALLS and creates its artifact
recording_repo() {
    CALLS="$T_DIR/calls"; : >"$CALLS"; export CALLS
    export T_BIN
    fake_repo_file scripts/build-sd.sh '#!/usr/bin/env bash
echo "build-sd SD_CUDA=${SD_CUDA:-}" >>"$CALLS"
printf "#!/usr/bin/env bash\necho \"stable-diffusion.cpp version master-948\"\n" >bin/sd; chmod +x bin/sd
echo "building sd..."'
    fake_repo_file scripts/get-piper.sh '#!/usr/bin/env bash
echo "get-piper" >>"$CALLS"
printf "#!/usr/bin/env bash\necho \"usage: piper\"\n" >bin/piper; chmod +x bin/piper
mkdir -p voices; echo x >voices/v.onnx; echo "{}" >voices/v.onnx.json'
    fake_repo_file scripts/build-acestep.sh '#!/usr/bin/env bash
echo "build-acestep" >>"$CALLS"
for p in ace-lm ace-synth; do printf "#!/usr/bin/env bash\necho x\n" >bin/$p; chmod +x bin/$p; done'
    fake_repo_file scripts/get-ytdlp.sh '#!/usr/bin/env bash
echo "get-ytdlp" >>"$CALLS"
printf "#!/usr/bin/env bash\necho 2026.1.1\n" >bin/yt-dlp; chmod +x bin/yt-dlp'
    fake_repo_file scripts/get-gsap.sh '#!/usr/bin/env bash
echo "get-gsap" >>"$CALLS"
mkdir -p third_party/gsap; printf "/*!\n * GSAP 3.13.0\n */" >third_party/gsap/gsap.min.js'
    fake_repo_file scripts/get-hyperframes-libs.sh '#!/usr/bin/env bash
echo "get-hflibs" >>"$CALLS"
mkdir -p third_party/hyperframes-libs/h; echo x >third_party/hyperframes-libs/h/a.js'
    fake_repo_file scripts/get-hyperframes-blocks.sh '#!/usr/bin/env bash
echo "get-hfblocks" >>"$CALLS"
mkdir -p hyperframes/blocks/x; echo "{}" >hyperframes/blocks/x/block.json'
    fake_repo_file scripts/get-hyperframes-runtime.sh '#!/usr/bin/env bash
echo "get-hfruntime" >>"$CALLS"
mkdir -p third_party/hyperframes-runtime; echo x >third_party/hyperframes-runtime/hyperframe.runtime.iife.js'
    fake_repo_file scripts/get-upscalers.sh '#!/usr/bin/env bash
echo "get-upscalers" >>"$CALLS"
mkdir -p bin/realesrgan/models bin/rife/rife-v4.6
printf "#!/usr/bin/env bash\necho usage\n" >bin/realesrgan/realesrgan-ncnn-vulkan; chmod +x bin/realesrgan/realesrgan-ncnn-vulkan
printf "#!/usr/bin/env bash\necho usage\n" >bin/rife/rife-ncnn-vulkan; chmod +x bin/rife/rife-ncnn-vulkan
echo x >bin/realesrgan/models/realesr-animevideov3-x2.bin; echo x >bin/rife/rife-v4.6/flownet.bin'
    fake_repo_file scripts/get-models.sh '#!/usr/bin/env bash
echo "get-models" >>"$CALLS"
mkdir -p models; echo x >models/m.safetensors'
    # go build creates what the checks look for
    stub go 'case "$1" in
  version) echo "go version go1.22.3 linux/amd64" ;;
  build) echo "go $*" >>"$CALLS"; printf "#!/usr/bin/env bash\necho \"Usage of veritmo\"\n" >bin/veritmo; chmod +x bin/veritmo ;;
esac'
    stub sudo 'echo "sudo $*" >>"$CALLS"; if [ "$1" = "-n" ]; then exit 0; fi; exec "$@"'
}

# package managers that record their calls and "install" the tools they are asked for
pm_stub() { # name
    stub "$1" 'echo "'"$1"' $*" >>"$CALLS"
for a in "$@"; do case "$a" in
  ffmpeg) printf "#!/usr/bin/env bash\ncase \"\$*\" in *-encoders*) echo \" V....D libx264 H.264\";; *) echo \"ffmpeg version 6.1\";; esac\n" >"$T_BIN/ffmpeg"; printf "#!/usr/bin/env bash\necho ffprobe\n" >"$T_BIN/ffprobe"; chmod +x "$T_BIN/ffmpeg" "$T_BIN/ffprobe" ;;
  git|cmake|make|curl) printf "#!/usr/bin/env bash\necho \"%s 9.9.9\"\n" $a >"$T_BIN/$a"; chmod +x "$T_BIN/$a" ;;
esac; done'
}

run_install() { # args...  (stdin closed)
    out="$(bash "$INSTALL" "$@" </dev/null 2>&1)"; st=$?
}
run_install_in() { # answers args...
    local answers="$1"; shift
    out="$(printf '%b' "$answers" | IL_FORCE_INTERACTIVE=1 bash "$INSTALL" "$@" 2>&1)"; st=$?
}
calls() { cat "$CALLS" 2>/dev/null; }

# --- --check --------------------------------------------------------------------
sandbox; complete_machine; recording_repo
run_install --check
assert_status "check on a complete machine" 0 "$st"
assert_contains "check says ok" "$out" "every required component is ok"
assert_eq "check runs nothing" "" "$(calls)"
rm "$T_BIN/ffmpeg"
run_install --check
assert_status "check with ffmpeg missing" 1 "$st"
assert_contains "check names it" "$out" "[missing] ffmpeg"
assert_eq "check still runs nothing" "" "$(calls)"
cleanup_sandbox

# --- no terminal and no mode: refuse, change nothing ----------------------------
sandbox; complete_machine; recording_repo; rm "$T_BIN/ffmpeg"; pm_stub pacman
run_install
assert_status "no terminal" 2 "$st"
assert_contains "refusal names --yes and --check" "$out" "--yes"
assert_contains "refusal names --check" "$out" "--check"
assert_eq "nothing was run" "" "$(calls)"
cleanup_sandbox

# --- --dry-run ------------------------------------------------------------------
sandbox; complete_machine; recording_repo; rm "$IL_ROOT/bin/sd"
run_install --dry-run
assert_status "dry run" 0 "$st"
assert_contains "plan shows the sd step" "$out" "would run: bash scripts/build-sd.sh"
assert_contains "dry run says so" "$out" "nothing was run"
assert_eq "nothing was built" "" "$(calls)"
assert_eq "sd was not created" "no" "$([ -e "$IL_ROOT/bin/sd" ] && echo yes || echo no)"
cleanup_sandbox

# --- asking: no, then yes -------------------------------------------------------
sandbox; complete_machine; recording_repo; rm "$T_BIN/ffmpeg" "$T_BIN/ffprobe"; pm_stub pacman
run_install_in 'n\n' 
assert_contains "the command is shown before asking" "$out" "would run: sudo pacman -S --needed --noconfirm ffmpeg"
assert_contains "no is respected" "$out" "skipped"
assert_not_contains "no package call after no" "$(calls)" "pacman"
assert_status "declining a required piece fails the run" 1 "$st"
run_install_in 'y\n'
assert_contains "yes installs" "$(calls)" "pacman -S --needed --noconfirm ffmpeg"
assert_contains "and rechecks" "$out" "-> ok"
assert_status "everything ok afterwards" 0 "$st"
cleanup_sandbox

# --- "all" answers yes to the rest ---------------------------------------------
sandbox; complete_machine; recording_repo; rm "$IL_ROOT/bin/sd" "$IL_ROOT/bin/yt-dlp"
run_install_in 'a\n'
assert_contains "all: sd built" "$(calls)" "build-sd"
assert_contains "all: yt-dlp fetched" "$(calls)" "get-ytdlp"
cleanup_sandbox

# --- quit -----------------------------------------------------------------------
sandbox; complete_machine; recording_repo; rm "$IL_ROOT/bin/sd" "$IL_ROOT/bin/yt-dlp"
run_install_in 'q\n'
assert_status "quit" 3 "$st"
assert_eq "quit runs nothing" "" "$(calls)"
cleanup_sandbox

# --- --yes installs everything installable, in order ----------------------------
sandbox; complete_machine; recording_repo
rm "$IL_ROOT/bin/sd" "$IL_ROOT/bin/piper" "$IL_ROOT/voices/v.onnx" "$IL_ROOT/bin/ace-lm" "$IL_ROOT/bin/yt-dlp" "$IL_ROOT/models/m.safetensors" "$IL_ROOT/bin/veritmo" "$IL_ROOT/third_party/gsap/gsap.min.js" "$IL_ROOT/third_party/hyperframes-libs/h/a.js" "$IL_ROOT/hyperframes/blocks/x/block.json" "$IL_ROOT/third_party/hyperframes-runtime/hyperframe.runtime.iife.js"
rm -rf "$IL_ROOT/bin/realesrgan" "$IL_ROOT/bin/rife"
run_install --yes
assert_status "yes run" 0 "$st"
order="$(calls | tr '\n' ' ')"
assert_contains "sd, piper, acestep, ytdlp, gsap, hflibs, upscalers, models" "$order" "build-sd SD_CUDA= get-piper build-acestep get-ytdlp get-gsap get-hflibs get-upscalers get-models"
assert_contains "veritmo, built without a UI tag" "$order" "-o bin/veritmo ./cmd/veritmo"
assert_contains "veritmo, stamped with its version" "$order" "go build -ldflags -X veritmo/internal/platform/edition.Version="
assert_contains "hfblocks after the veritmo" "$order" "-o bin/veritmo ./cmd/veritmo get-hfblocks"
assert_not_contains "no Node steps" "$order" "npm"
assert_contains "final report says ok" "$out" "every required component is ok"
cleanup_sandbox

# --- the browser is offered through the package manager, and only when it is missing ---
sandbox; complete_machine; recording_repo; pm_stub pacman; rm "$T_BIN/google-chrome-stable"
run_install --dry-run --only browser
assert_contains "the plan names the browser" "$out" "Chrome or Chromium (motion graphics)"
assert_contains "and the package" "$out" "would run: sudo pacman -S --needed --noconfirm chromium"
assert_eq "a dry run runs nothing" "" "$(calls)"
run_install_in 'n\n' --only browser
assert_status "declining an optional piece is fine" 0 "$st"
assert_not_contains "nothing installed after no" "$(calls)" "chromium"
cleanup_sandbox
sandbox; complete_machine; recording_repo
run_install --dry-run --only browser
assert_not_contains "present: nothing to plan" "$out" "would run"
cleanup_sandbox

# --- Blender is optional: offered through the package manager when missing, never installed on its own ---
sandbox; complete_machine; recording_repo; pm_stub pacman; rm "$T_BIN/blender"
run_install --dry-run --only blender
assert_contains "the plan names Blender" "$out" "Blender (3D scenes)"
assert_contains "and the package" "$out" "would run: sudo pacman -S --needed --noconfirm blender"
run_install_in 'n\n' --only blender
assert_status "declining Blender is fine" 0 "$st"
assert_not_contains "nothing installed after no" "$(calls)" "blender"
cleanup_sandbox
sandbox; complete_machine; recording_repo; pm_stub pacman; rm "$T_BIN/blender"
run_install --dry-run
assert_status "a default install without Blender succeeds" 0 "$st"
cleanup_sandbox

# --- .NET is offered with the ASP.NET Core runtime, and the runtime alone completes an SDK that lacks it ---
sandbox; complete_machine; recording_repo; pm_stub pacman
run_install --dry-run --only dotnet
assert_contains "the plan names .NET" "$out" "ASP.NET Core runtime"
assert_contains "and both packages" "$out" "would run: sudo pacman -S --needed --noconfirm dotnet-sdk aspnet-runtime"
cleanup_sandbox
sandbox; complete_machine; recording_repo; pm_stub pacman
stub dotnet 'case "$1" in --version) echo "10.0.113";; --list-runtimes) echo "Microsoft.NETCore.App 10.0.13 [/x]";; esac'
run_install --dry-run --only dotnet
assert_contains "an SDK without the web runtime is completed" "$out" "would run: sudo pacman -S --needed --noconfirm dotnet-sdk aspnet-runtime"
cleanup_sandbox
sandbox; complete_machine; recording_repo; pm_stub pacman
stub dotnet 'case "$1" in --version) echo "10.0.113";; --list-runtimes) echo "Microsoft.AspNetCore.App 10.0.13 [/x]";; esac'
run_install --dry-run --only dotnet
assert_not_contains "complete: nothing to plan" "$out" "would run"
cleanup_sandbox

# --- --only ---------------------------------------------------------------------
sandbox; complete_machine; recording_repo; rm "$IL_ROOT/bin/sd" "$IL_ROOT/bin/piper" "$IL_ROOT/voices/v.onnx" "$IL_ROOT/bin/yt-dlp"
run_install --yes --only piper,ytdlp
assert_eq "only piper and yt-dlp" "get-piper get-ytdlp" "$(calls | tr '\n' ' ' | sed 's/ $//')"
run_install --yes --only nonsense
assert_status "unknown component" 2 "$st"
assert_contains "names the component" "$out" "unknown component 'nonsense'"
cleanup_sandbox

# --- --no-models ----------------------------------------------------------------
sandbox; complete_machine; recording_repo; rm "$IL_ROOT/models/m.safetensors"
run_install --yes --no-models
assert_not_contains "models not downloaded" "$(calls)" "get-models"
assert_contains "listed as pending" "$out" "pending (--no-models)"
cleanup_sandbox

# --- a prerequisite that is missing blocks what needs it -------------------------
sandbox; complete_machine; recording_repo; rm "$IL_ROOT/bin/sd" "$T_BIN/cmake"
run_install --yes --only sd
assert_contains "waiting for cmake" "$out" "waiting for cmake"
assert_not_contains "sd not built" "$(calls)" "build-sd"
cleanup_sandbox

# --- sudo that needs a password without a terminal -------------------------------
sandbox; complete_machine; recording_repo; rm "$T_BIN/ffmpeg" "$T_BIN/ffprobe"; pm_stub pacman
stub sudo 'echo "sudo $*" >>"$CALLS"; exit 1'
run_install --yes
assert_contains "asks the user to run it" "$out" "sudo needs a password and there is no terminal"
assert_contains "prints the command" "$out" "sudo pacman -S --needed --noconfirm ffmpeg"
assert_not_contains "the package manager did not run" "$(calls)" "pacman -S"
assert_status "run fails" 1 "$st"
cleanup_sandbox

# --- an ffmpeg without libx264 is not enough ----------------------------------------
sandbox; complete_machine; recording_repo
stub ffmpeg 'case "$*" in *-encoders*) echo " V....D libvpx VP8";; *) echo "ffmpeg version 7.1.2";; esac'
run_install --check
assert_status "ffmpeg without libx264 fails the check" 1 "$st"
assert_contains "says why" "$out" "no libx264 encoder"
cleanup_sandbox

# --- apt: update once, then install; dnf; no package manager ---------------------
sandbox; complete_machine; recording_repo; rm "$T_BIN/ffmpeg" "$T_BIN/ffprobe" "$T_BIN/curl" "$T_BIN/git"; pm_stub apt-get
run_install --yes --only system
assert_eq "apt-get update once, then the installs" "1" "$(calls | grep -c '^apt-get update')"
assert_contains "ffmpeg through apt" "$(calls)" "apt-get install -y ffmpeg"
cleanup_sandbox
sandbox; complete_machine; recording_repo; rm "$T_BIN/ffmpeg" "$T_BIN/ffprobe"; pm_stub dnf
run_install --yes --only ffmpeg
assert_not_contains "dnf: the distribution ffmpeg is not installed" "$(calls)" "dnf install -y ffmpeg"
assert_contains "dnf: RPM Fusion explained" "$out" "RPM Fusion"
cleanup_sandbox
sandbox; complete_machine; recording_repo; rm "$T_BIN/ffmpeg" "$T_BIN/ffprobe"
run_install --yes --only ffmpeg
assert_contains "no package manager: by hand" "$out" "no supported package manager"
assert_status "and the run fails" 1 "$st"
cleanup_sandbox

# --- Go: a distribution package that is too old is explained, not installed ---
sandbox; complete_machine; recording_repo; rm "$T_BIN/go"; pm_stub apt-get
stub apt-cache 'echo "$2:"; echo "  Installed: (none)"; echo "  Candidate: ${CANDIDATE:-2:1.19~1}"'
export CANDIDATE="2:1.19~1"
run_install --yes --only go
assert_not_contains "old Go not installed" "$(calls)" "install -y golang-go"
assert_contains "old Go explained" "$out" "this distribution's Go is 1.19"
export CANDIDATE="2:1.22.2-2"
run_install --yes --only go
assert_contains "recent Go installed" "$(calls)" "install -y golang-go"
unset CANDIDATE
cleanup_sandbox

# --- CUDA: installed only where the package is recent enough ----------------------
sandbox; complete_machine; recording_repo; rm "$T_BIN/nvcc"; pm_stub pacman
stub pacman 'echo "pacman $*" >>"$CALLS"; if [ "$1" = "-Si" ]; then echo "Version         : 12.9.1-1"; fi'
run_install --yes --only cuda
assert_contains "cuda through pacman when 12.9" "$(calls)" "pacman -S --needed --noconfirm cuda"
stub pacman 'echo "pacman $*" >>"$CALLS"; if [ "$1" = "-Si" ]; then echo "Version         : 12.4.0-1"; fi'
: >"$CALLS"
run_install --yes --only cuda
assert_not_contains "no install when the package is too old" "$(calls)" "pacman -S --needed"
assert_contains "points at NVIDIA" "$out" "developer.nvidia.com/cuda-downloads"
cleanup_sandbox
sandbox; complete_machine; recording_repo; rm "$T_BIN/nvcc"; pm_stub apt-get
run_install --yes --only cuda
assert_not_contains "apt: cuda only explained" "$(calls)" "apt-get install"
assert_contains "apt: guidance" "$out" "developer.nvidia.com/cuda-downloads"
cleanup_sandbox

# --- no GPU: the CPU-only build is offered, never forced --------------------------
sandbox; complete_machine; recording_repo; rm "$T_BIN/nvidia-smi" "$T_BIN/nvcc" "$IL_ROOT/bin/sd"
run_install --yes --only sd
assert_contains "explained" "$out" "no NVIDIA GPU found"
assert_contains "needs --cpu-sd" "$out" "--cpu-sd"
assert_not_contains "not built by --yes alone" "$(calls)" "build-sd"
run_install --yes --cpu-sd --only sd
assert_contains "CPU-only build on request" "$(calls)" "build-sd SD_CUDA=OFF"
cleanup_sandbox
sandbox; complete_machine; recording_repo; rm "$T_BIN/nvidia-smi" "$T_BIN/nvcc" "$IL_ROOT/bin/sd"
run_install_in 'n\n' --only sd
assert_contains "asked with a warning" "$out" "VERY slow"
assert_eq "declined: nothing built" "" "$(calls)"
cleanup_sandbox

# --- Ollama: guided, installed by the package only where it exists -----------------
sandbox; complete_machine; recording_repo; rm "$T_BIN/ollama"; pm_stub apt-get
run_install --yes --only ollama
assert_contains "guidance for Ollama" "$out" "https://ollama.com/download"
assert_contains "never piped into a shell" "$out" "never pipes a downloaded script"
assert_not_contains "nothing downloaded" "$(calls)" "curl"
cleanup_sandbox
sandbox; complete_machine; recording_repo; rm "$T_BIN/ollama"; pm_stub pacman
run_install --yes --only ollama
assert_contains "Ollama through pacman" "$(calls)" "pacman -S --needed --noconfirm ollama"
cleanup_sandbox

# --- a failing step -----------------------------------------------------------------
sandbox; complete_machine; recording_repo; rm "$IL_ROOT/bin/sd"
fake_repo_file scripts/build-sd.sh '#!/usr/bin/env bash
echo "compiler exploded" >&2; exit 1'
run_install --yes --only sd
assert_contains "failure shown" "$out" "FAILED: bash scripts/build-sd.sh"
assert_contains "output kept" "$out" "compiler exploded"
assert_status "failing step fails the run" 1 "$st"
cleanup_sandbox

# --- idempotence and the log ---------------------------------------------------------
sandbox; complete_machine; recording_repo; rm "$IL_ROOT/bin/sd"
run_install --yes
first_calls="$(calls | wc -l)"
assert_contains "first run built sd" "$(calls)" "build-sd"
: >"$CALLS"
run_install --yes
assert_eq "second run runs nothing" "" "$(calls)"
assert_status "second run ok" 0 "$st"
log="$(cat "$IL_LOG_DIR/install.log")"
assert_contains "the log holds the command" "$log" "\$ bash scripts/build-sd.sh"
assert_contains "the log holds its output" "$log" "building sd..."
assert_contains "the log holds the exit status" "$log" "[exit 0]"
cleanup_sandbox

# --- confinement: only the project, the log and the package manager were written ----
sandbox; complete_machine; recording_repo; rm "$IL_ROOT/bin/sd"
before="$(find "$T_DIR" -newer "$T_DIR/bin" -type f 2>/dev/null | wc -l)"
run_install --yes
outside="$(find "$T_DIR" -type f -newer "$CALLS" ! -path "$IL_ROOT/*" ! -path "$IL_LOG_DIR/*" ! -path "$T_BIN/*" ! -path "$CALLS" 2>/dev/null | head -3)"
assert_eq "nothing written outside the project, the log and the stubs" "" "$outside"
cleanup_sandbox

pass_summary
