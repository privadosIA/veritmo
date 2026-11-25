# installer Specification

## Purpose
Takes a Linux machine from a clean checkout to a working orchestrator by detecting what is installed, reporting what is missing, and installing it only when the user chooses to. It never installs without consent and can be re-run at any time.

## Requirements

### Requirement: The installer reports the state of the machine before doing anything

`scripts/install.sh` SHALL start by printing a report, before any change is made, with: the distribution and package manager found, the architecture, the memory and the free disk space where it writes, each NVIDIA GPU with its compute capability when `nvidia-smi` exists, the `nvcc` version, and the state of every component as `ok` (with the version found), `old` (with the version found and the one required), `missing` or `n/a`. The components SHALL include the system tools (`git`, `cmake`, a C/C++ compiler, `make`, `curl`, `ffmpeg`, `ffprobe`), Go, the CUDA toolkit, `bin/sd`, `bin/piper` and a voice, `bin/ace-lm` and `bin/ace-synth`, `bin/yt-dlp`, an image model, `bin/orchestrator`, and Ollama with the assistant's default model. Node, npm and a dashboard build SHALL NOT be components: the server needs none of them. Each component SHALL be marked required or optional, and a missing or old one SHALL point at the part of `docs/INSTALL.md` that explains it.

#### Scenario: report on a machine that has some things
- **WHEN** the installer runs on a machine with Go and ffmpeg but without `nvcc` or `bin/sd`
- **THEN** the report shows Go and ffmpeg as `ok` with their versions, the CUDA toolkit and `sd` as `missing`, and changes nothing

#### Scenario: an old tool
- **WHEN** `nvcc` is 12.4
- **THEN** it is reported `old`, with the required 12.8, as required for building `sd`

#### Scenario: optional components
- **WHEN** Ollama and `yt-dlp` are absent
- **THEN** they are reported as `missing` and optional, and do not make the check fail

#### Scenario: no Node on the machine
- **WHEN** neither `node` nor `npm` is installed
- **THEN** the report does not mention them and the check does not fail because of them

### Requirement: Nothing is installed without the user's choice

By default the installer SHALL ask, for each installable component, whether to install it, showing the exact command it would run and the download size when known, and SHALL accept yes, no, yes to all and quit. With `--yes` it SHALL install every installable component without asking. With `--only <list>` it SHALL act only on the named components, and with `--no-models` it SHALL skip the model and voice downloads and list them as pending. Without a terminal and without `--yes`, `--check` or `--dry-run`, it SHALL refuse and say so, instead of guessing. System packages SHALL be installed through the detected package manager (`pacman`, `apt` or `dnf`) with `sudo` when not run as root; the command SHALL be printed before it runs, and when `sudo` cannot run without a password in a non-interactive session the installer SHALL stop and print the command for the user to run. For any other package manager it SHALL list the tools to install by hand.

#### Scenario: asking
- **WHEN** the installer runs interactively and `ffmpeg` is missing
- **THEN** it shows the package command and waits for an answer, and installs nothing when the answer is no

#### Scenario: automatic
- **WHEN** it runs with `--yes`
- **THEN** every installable missing component is installed in order, each command printed before it runs

#### Scenario: only some components
- **WHEN** it runs with `--only piper,ytdlp`
- **THEN** nothing else is touched

#### Scenario: no terminal
- **WHEN** it runs with its input closed and no mode flag
- **THEN** it exits with an error that names `--yes` and `--check`, having changed nothing

#### Scenario: an unsupported package manager
- **WHEN** none of `pacman`, `apt` or `dnf` exists
- **THEN** the report lists the system tools to install by hand and the installer does not try

### Requirement: Check-only and dry-run modes

`--check` SHALL print the report and exit with status `0` when every required component is `ok` and `1` otherwise, changing nothing. `--dry-run` SHALL print the report and the plan (each step it would run, in order) and run none of it.

#### Scenario: a complete machine
- **WHEN** every required component is `ok`
- **THEN** `--check` exits `0`

#### Scenario: a missing required component
- **WHEN** `ffmpeg` is missing
- **THEN** `--check` exits `1` and names it

#### Scenario: dry run
- **WHEN** `--dry-run` runs on a machine missing `bin/sd`
- **THEN** the plan shows the `build-sd.sh` step and nothing is cloned or built

### Requirement: The project's own components are built with the existing scripts

The installer SHALL install `bin/sd` with `scripts/build-sd.sh`, `bin/ace-lm` and `bin/ace-synth` with `scripts/build-acestep.sh`, `bin/piper` and a voice with `scripts/get-piper.sh`, `bin/yt-dlp` with `scripts/get-ytdlp.sh` and an image model with `scripts/get-models.sh`, passing the environment overrides those scripts support, and SHALL build `bin/orchestrator` with `go build` and no front-end build tag. When the CUDA toolkit is missing or older than 12.8 it SHALL say so and how to get it for the distribution, and SHALL install it automatically only where the package manager provides a sufficient version. When no NVIDIA GPU is present it SHALL offer the CPU-only build of `sd` (`SD_CUDA=OFF`) with a warning that renders will be very slow, and SHALL NOT build it automatically with `--yes` unless `--cpu-sd` is also given.

#### Scenario: building sd
- **WHEN** `bin/sd` is missing and `nvcc` is 12.8 or newer
- **THEN** the installer runs `scripts/build-sd.sh` and, afterwards, the report shows `bin/sd` as `ok`

#### Scenario: no CUDA toolkit
- **WHEN** `nvcc` is missing on a distribution whose repository has no sufficient version
- **THEN** the installer does not install anything for it and prints where to get CUDA 12.8 or newer

#### Scenario: no GPU
- **WHEN** `nvidia-smi` is absent
- **THEN** the report says renders need a GPU, and the CPU-only build is offered with the warning

#### Scenario: no GPU and --yes
- **WHEN** `nvidia-smi` is absent and the installer runs with `--yes` but without `--cpu-sd`
- **THEN** `sd` is not built and the output says `--cpu-sd` builds it for the CPU

#### Scenario: the server binary
- **WHEN** `bin/orchestrator` is missing
- **THEN** the installer builds it without running `npm` or `ng` and without `-tags webui`

### Requirement: Ollama is guided and never piped into a shell

Ollama SHALL be optional. The installer SHALL show how to install it for the detected distribution, SHALL NOT download a script and run it in a shell, SHALL run the distribution's package only when the user asks, and SHALL report whether the assistant's default model is pulled, printing `ollama pull <model>` when it is not.

#### Scenario: Ollama absent
- **WHEN** Ollama is not installed
- **THEN** the report marks it optional and shows the install instructions, and the check does not fail

#### Scenario: model not pulled
- **WHEN** Ollama runs but `qwen3:8b` is not in its list
- **THEN** the report shows `ollama pull qwen3:8b`

### Requirement: Runs are idempotent, logged and confined

Every check SHALL look at the real thing (the binary runs, the file exists), so re-running after an interruption continues from what is still missing and installs nothing twice. Every run SHALL append the commands it ran and their output to `~/.cache/veritmo/install.log`. The installer SHALL NOT write outside the project directory, that log directory and the package manager's own locations, and SHALL NOT remove anything outside the repository.

#### Scenario: re-run
- **WHEN** the installer is run again after a complete install
- **THEN** it reports everything `ok` and runs no installation step

#### Scenario: interrupted build
- **WHEN** a previous run stopped before `bin/sd` was built
- **THEN** the next run reports it `missing` and offers it again

#### Scenario: log
- **WHEN** a step runs
- **THEN** its command and output are appended to the log file

### Requirement: sd is built from a pinned version

The `sd` build script SHALL build stable-diffusion.cpp from a fixed commit and not from the latest upstream, SHALL let the person choose another commit with the `SD_REF` environment variable, and SHALL print the commit it uses. The pinned commit SHALL be one with which every animation engine of the project produces a correct clip.

#### Scenario: a fresh build
- **WHEN** `scripts/build-sd.sh` runs with no repository present
- **THEN** it clones stable-diffusion.cpp, checks out the pinned commit and its submodules, builds it and installs `bin/sd`, and `bin/sd --version` reports that commit

#### Scenario: re-running over a newer checkout
- **WHEN** `third_party/stable-diffusion.cpp` is at a newer commit than the pinned one and the script runs again
- **THEN** it fetches, checks out the pinned commit and rebuilds, so `bin/sd` ends on the pinned version

#### Scenario: choosing another version
- **WHEN** the script runs with `SD_REF` set to another commit
- **THEN** it builds that commit instead of the pinned one

#### Scenario: a version that breaks an engine
- **WHEN** an `sd` version makes an animation engine write an unusable clip (the LTX 2.3 clip of master-1010 is all white)
- **THEN** that version is not the pinned one, and the documentation names the failure

### Requirement: The .NET component includes the ASP.NET Core runtime

The installer SHALL treat the .NET component as present only when the SDK is new enough and the ASP.NET Core runtime is installed, because the music services are web applications that cannot start without it, and SHALL install the runtime together with the SDK through the package manager where the SDK package does not include it.

#### Scenario: an SDK without the web runtime
- **WHEN** `dotnet` is installed but `dotnet --list-runtimes` has no `Microsoft.AspNetCore.App`
- **THEN** the report shows the component as missing, saying it needs the ASP.NET Core runtime, and the plan installs it (on Arch `dotnet-sdk` and `aspnet-runtime`)

#### Scenario: a complete .NET
- **WHEN** the SDK is new enough and the ASP.NET Core runtime is listed
- **THEN** the component is `ok` and nothing is planned

### Requirement: Blender is an optional component

The installer SHALL detect Blender (the `blender` command, version 4.2 or newer) and report it with its version, or as missing with the way to install it. A missing or old Blender SHALL never make the installation fail or stop it, and the installer SHALL NOT install it unless asked.

#### Scenario: Missing
- **WHEN** the installer runs on a machine without Blender
- **THEN** it reports Blender as optional and missing, and finishes successfully

#### Scenario: Present
- **WHEN** Blender 5.2 is installed
- **THEN** it is reported with its version

### Requirement: Piper, the models and whisper are downloaded pinned and verified

The scripts that fetch Piper, its voices, the image models and `whisper.cpp` SHALL name a fixed version, tag or commit and SHALL verify a SHA-256 of every archive or file they download where the source publishes one, refusing a file that does not match, as the yt-dlp and upscaler scripts do.

#### Scenario: a changed archive
- **WHEN** a downloaded archive's hash differs from the pinned one
- **THEN** the script removes it, exits non-zero and names the file

#### Scenario: whisper.cpp
- **WHEN** `get-whisper.sh` clones the repository
- **THEN** it checks out the pinned commit, never the branch head

### Requirement: demucs.cpp and its weights are downloaded pinned and verified

`scripts/get-demucs.sh` SHALL install what the voice separator needs, the way `get-whisper.sh` installs Whisper: the Demucs 4-stem ggml weights into `models/demucs/`, downloaded from a fixed URL and refused unless their size and SHA-256 match the ones written in the script, and, with `--build`, demucs.cpp cloned with its submodules at a pinned commit (`DEMUCS_CPP_REV` overrides it), never the head of a branch, and built on the CPU into `bin/demucs`. It SHALL be safe to run again (a file already there with the right hash is not downloaded again), SHALL say which packages the build needs when one is missing, and SHALL print how to check the install. `docs/INSTALL.md` SHALL list the step as optional, with the packages the build needs, and `THIRD_PARTY.md` SHALL credit demucs.cpp (MIT) and the Demucs weights with their licence.

#### Scenario: A changed file
- **WHEN** the downloaded weights' hash differs from the pinned one
- **THEN** the script removes the file, exits non-zero and names it

#### Scenario: The clone
- **WHEN** `get-demucs.sh --build` clones the repository
- **THEN** it checks out the pinned commit with its submodules and builds from it

#### Scenario: Run twice
- **WHEN** the script runs on a machine where the weights are already installed and match
- **THEN** nothing is downloaded again and it exits 0

### Requirement: GSAP is fetched pinned and verified

`scripts/get-gsap.sh` SHALL download the GSAP core of a fixed version from the npm registry tarball, verify its SHA-256 against the one written in the script, and place `gsap.min.js` in `third_party/gsap/`; it SHALL be safe to run again and SHALL print how to check the install. The file SHALL NOT be committed to the repository. `THIRD_PARTY.md` SHALL state GSAP's version and licence and whether the server may embed it in the pages it draws, as read from the licence of that version; the environment report SHALL list it as optional with the command that installs it. `docs/INSTALL.md` SHALL list the step as optional ("parts and scenes that animate with GSAP").

#### Scenario: A changed file
- **WHEN** the downloaded file's hash differs from the pinned one
- **THEN** the script removes it, exits non-zero and names the file

#### Scenario: Not installed
- **WHEN** the environment is read on a machine without `third_party/gsap/gsap.min.js`
- **THEN** the report lists it absent with `bash scripts/get-gsap.sh`, and only parts and scenes that use GSAP are unavailable
