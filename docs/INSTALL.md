# Installation

[← Back to the README](../README.md)

This guide takes a Linux machine from nothing to a working `veritmo serve`. The short way is the installer, `scripts/install.sh`: it looks at what the machine has, tells you what is missing and, only if you say so, installs it. The rest of the page explains every piece it deals with, so you can also do it by hand, on a distribution it does not know, or understand what it asks.

## What you need

- **Linux on x86_64.** Windows and macOS are not supported yet (the render and music engines are built with CUDA, and the scripts are Linux-only); see [Other platforms](#other-platforms-windows-and-macos). The development machine runs CachyOS (Arch family); the installer is tested against Arch, Debian 12, Ubuntu 24.04 and Fedora 41 containers.
- **An NVIDIA GPU with recent drivers** for rendering images and video and for the music engine. Without one the pipeline's other parts work, but `sd` can only be built for the CPU, which is very slow (see [NVIDIA driver and CUDA](#nvidia-driver-and-cuda)).
- **Enough memory and disk.** The development machine has 30 GB of RAM, which is comfortable for the large models: a model is copied to RAM as well as to VRAM while it loads, and the large video set needs about 21 GB. `./serve.sh` caps the server and everything it launches at 85% of the RAM (`MEM_MAX`; see [Machines with 32 GB of RAM](#machines-with-32-gb-of-ram)). Count about 3 GB of disk for the tools, plus the models you choose (see [Models](#models)): from 4 GB for a single Stable Diffusion checkpoint up to 50 GB or more for the large video and music sets. The projects themselves (scenes, sources, cache and results) can live on another, bigger disk: `MEDIA_DIR=/big/disk/media ./serve.sh` keeps them there (or write the path once in `~/.config/veritmo/media-dir`), and `MEDIA_DIR=off` keeps them in `projects/` (see [SERVER.md](SERVER.md)). The models can be moved the same way with `MODELS_DIR` and `veritmo models migrate` (see [SERVER.md](SERVER.md)).
- **Network access** to GitHub, Hugging Face and your distribution's package mirrors while installing. Nothing is downloaded at run time except the models you ask for.

## Other platforms (Windows and macOS)

Only Linux x86_64 with an NVIDIA GPU is supported and tested. Nothing below has been tried yet: it is the plan for testing, not a promise.

| Where | What to try | Expected state |
|---|---|---|
| Windows 11 with an NVIDIA GPU | **WSL2** (Ubuntu) with the NVIDIA driver of Windows, then this guide as on Linux | Probably works: WSL2 exposes CUDA to the Linux side. Untested |
| Windows 11 with an NVIDIA GPU | **Docker Desktop** (WSL2 backend) and the [Compose stack](#with-docker-compose), with the NVIDIA Container Toolkit | Probably works. Untested |
| Windows, native | Not planned: the scripts are bash and `sd`/`acestep` would need MSVC builds | Not supported |
| macOS on Apple Silicon | Build `sd` and `acestep` with Metal instead of CUDA, services with `--cpu`, scripts ported to Homebrew | Not supported: the installer and `serve.sh` are Linux-only. The first two steps were tried by hand, see [macOS on Apple Silicon: what was tried](#macos-on-apple-silicon-what-was-tried) |
| macOS on Intel | Same as above, without Metal in practice | Not supported |
| Linux with an AMD or Intel card | `sd` and `acestep` with Vulkan (`sd` also ROCm); HeartMuLa and DiffRhythm2 in the Docker containers with the ROCm wheel of PyTorch | Not supported and not tried; the .NET services have no ROCm build. See [Cards that are not NVIDIA](#cards-that-are-not-nvidia) |
| Any system, only the dashboard | Open the dashboard (or the phone app) in a browser against a server running on Linux | Works today |

Without an NVIDIA GPU everything that does not render still works (the editor, the import, narration and the recognition services on the CPU); see [NVIDIA driver and CUDA](#nvidia-driver-and-cuda).

If you try one of these, please report what worked and what did not (the installer's `--check` output helps).

### macOS on Apple Silicon: what was tried

Tried on 2026-11-20 on a Mac mini M4 with 16 GB of unified memory, building by hand in a scratch folder (nothing installed, no change to this repository):

| Step | Result |
|---|---|
| `go build ./cmd/veritmo` (Go 1.27, darwin/arm64) | Builds unchanged, 4 s |
| `sd` at the pinned commit (`SD_REF`, `7867f6d`) with `cmake -B build -DSD_METAL=ON -DCMAKE_BUILD_TYPE=Release` | Builds first time, 40 s. Needs only `git`, `cmake` and a compiler |
| One 512x512 image with SD-Turbo (`sd-cli -m sd_turbo.safetensors --steps 4 --cfg-scale 1.0`) | Correct picture in 8.7 s on the GPU (Metal). The checkpoint file is 5.2 GB (fp32) |

**Not tried:** `veritmo serve` with that `sd` plugged in, SDXL at normal size, the video models (Wan, MiniMax H3, LTX 2.3), `acestep`, the music services, Blender and Real-ESRGAN/RIFE. The video set needs about 21 GB of RAM (see above) and the Apple GPU shares its memory with the system, so 16 GB is tight for it; a Mac with more memory removes that limit but not the others (a slower GPU than an NVIDIA card, engines measured only with CUDA). A sensible split is the Mac for the server, the dashboard and the image mode, and the heavy video and music work on a Linux machine with an NVIDIA card.

### Cards that are not NVIDIA

There are two families of engines, and they do not share a backend:

- **ggml engines** (`sd`, `acestep`): besides CUDA and Metal, ggml has a Vulkan backend (`-DSD_VULKAN=ON` for `sd`; the `acestep.cpp` README lists Vulkan too), and `sd` also has ROCm and SYCL ones. `scripts/build-sd.sh` builds only with CUDA and needs `nvcc`, so another backend means changing that flag. Not tried here. Real-ESRGAN and RIFE already run on Vulkan (see [Video upscalers](#video-upscalers-real-esrgan-and-rife)).
- **PyTorch engines** (HeartMuLa and DiffRhythm2, as the Docker containers of `docker/` and as the .NET services, which use TorchSharp, that is libtorch): PyTorch has no useful Vulkan backend for these models. The containers install the `cu128` wheels and need the NVIDIA container runtime; with an AMD card they would need the ROCm wheel of PyTorch and `/dev/kfd` and `/dev/dri` passed to the container (not tried). The .NET services have `TorchBackend` `cpu`, `cuda-windows` or `cuda-linux` and no ROCm option: TorchSharp has no ROCm package, and its issue [#1125](https://github.com/dotnet/TorchSharp/issues/1125) was closed as not planned. It would mean building LibTorchSharp against a ROCm libtorch of the same version as TorchSharp 0.107; nobody has done it here.

## The installer

```bash
bash scripts/install.sh --check     # report only: what is ok, old or missing. Exits 1 if a required piece is missing
bash scripts/install.sh --dry-run   # the report and the plan (every command it would run); runs nothing
bash scripts/install.sh             # asks about each missing piece: [y/N/a(ll)/q]
bash scripts/install.sh --yes       # installs everything it can without asking
bash scripts/install.sh --yes --only piper,ytdlp   # only these components
bash scripts/install.sh --yes --no-models          # leave the large model downloads for later
bash scripts/install.sh --yes --cpu-sd             # on a machine without an NVIDIA GPU: build sd for the CPU
```

| Option | Meaning |
|---|---|
| `--check` | Prints the report and exits `0` when every required component is `ok`, `1` otherwise. Changes nothing. |
| `--dry-run` | Prints the report and the plan. Runs nothing. |
| `--yes`, `-y` | Installs every installable missing component, printing each command before it runs. |
| `--only a,b` | Acts only on the named components: `git cmake cc make curl ffmpeg go cuda sd piper voice acestep ytdlp gsap hflibs hfblocks node hfruntime upscalers model veritmo ollama`, or the groups `system` and `models`. |
| `--no-models` | Skips the image model download and lists it as pending. (`piper` brings its default voice, a few MB, with it.) |
| `--cpu-sd` | Builds `sd` without CUDA when there is no NVIDIA GPU. Without it, `--yes` does not build `sd` on such a machine. |

How it behaves:

1. **It reports first.** The distribution and package manager, the architecture, memory and free disk, each NVIDIA GPU with its compute capability, the `nvcc` version, and every component as `[ok]` (with its version), `[old]`, `[missing]` or `[n/a]`, marked required or optional, with the section of this guide that explains it. Nothing is changed before the report.
2. **It asks, unless you tell it not to.** For each missing piece it shows the exact command and waits for `y` (yes), `n` (no, the default), `a` (yes to the rest) or `q` (quit). Without a terminal and without `--yes`, `--check` or `--dry-run` it refuses instead of guessing.
3. **System packages go through your package manager** (`pacman`, `apt` or `dnf`), with `sudo` unless you run it as root. The command is printed first. If `sudo` would need a password and there is no terminal, it stops and prints the command for you to run.
4. **The project's own pieces use the scripts in `scripts/`**, the same ones you would run by hand: `build-sd.sh`, `build-acestep.sh`, `get-piper.sh`, `get-ytdlp.sh` and `get-models.sh`; it also builds `bin/veritmo`.
5. **Go and CUDA are checked before they are installed.** A distribution package whose version is too old for the project is not installed: the installer says what version it would get and where to download a proper one.
6. **It is safe to re-run.** Every check looks at the real thing (the binary runs, the file exists), so after an interruption the next run just sees what is still missing. Commands and their output are appended to `~/.cache/veritmo/install.log`. It writes only inside the project directory, that log directory and what your package manager manages, and never removes anything outside the repository.

When it finishes, create the database with `bin/veritmo data migrate` (the server keeps its state in one, and refuses to start while migrations are pending; see [DATA_STORE.md](DATA_STORE.md#who-migrates-the-deployment-workflow-never-the-server)) and start the server with `./serve.sh` (see [SERVER.md](SERVER.md)).

## Components

| Component | | Needed for | Version | Section |
|---|---|---|---|---|
| git, cmake, a C/C++ compiler, make, curl | required | building `sd` and the music engine, downloading | cmake >= 3.18 | [System tools](#system-tools) |
| ffmpeg + ffprobe | required | timing and assembling the video; the music export | with `libx264` | [ffmpeg](#ffmpeg) |
| Go | required | building `veritmo` | >= 1.22 | [Go](#go) |
| CUDA toolkit (`nvcc`) | required with an NVIDIA GPU | building `sd` and the music engine | >= 12.8 | [NVIDIA driver and CUDA](#nvidia-driver-and-cuda) |
| `bin/sd` | required | images and video | built here | [stable-diffusion.cpp](#stable-diffusioncpp-sd) |
| `bin/piper` + a voice | required (video mode) | narration | latest release | [Piper and a voice](#piper-and-a-voice) |
| an image model | required | rendering | at least one checkpoint | [Models](#models) |
| `bin/veritmo` | required | everything | built here | [The veritmo binary](#the-veritmo-binary) |
| `bin/ace-lm`, `bin/ace-synth` | optional | music mode | pinned commit | [Music engine](#music-engine-acestepcpp) |
| `bin/services/*` (HeartMuLa, DiffRhythm2, LAION-CLAP, video-vision) | optional | the song generators of the music mode, the sound recognition of "Take it from a video or audio" and the person mask and depth of the video-edit tools | built here with the .NET SDK | [Music services](#music-services-net) |
| `bin/yt-dlp` | optional | adding a source from a YouTube address | latest release | [yt-dlp](#yt-dlp) |
| `bin/realesrgan`, `bin/rife` | optional | the better engines of scaling and raising the frame rate of a video (video-edit tools) | fixed releases, SHA-256 checked | [Video upscalers](#video-upscalers-real-esrgan-and-rife) |
| `whisper-cli` + a Whisper model | optional | reading the lyrics of a song (music studio) | the package or built here; large-v3-turbo q5_0 | [Whisper](#whisper-lyrics-of-a-song) |
| `bin/demucs` + the htdemucs weights | optional | separating the voice of a song to time its words | built here from a pinned commit of demucs.cpp; weights checked by SHA-256 | [Voice separation](#voice-separation-for-the-timing-of-the-words) |
| `third_party/hyperframes-runtime/` | optional | the runtime of HyperFrames, built here, so that the components that mount sub-compositions are drawn | built from the pinned commit, SHA-256 checked | [HyperFrames runtime](#hyperframes-runtime) |
| `hyperframes/blocks/` | optional | the 210 HyperFrames blocks and 238 components, whole, with everything they need (the libraries below) | cloned at a pinned commit | [HyperFrames blocks](#hyperframes-blocks) |
| `third_party/hyperframes-libs/` | optional | the libraries (three, gsap, d3...) that the HyperFrames blocks load, kept here so a block is drawn without the network | pinned versions, SHA-256 checked | [HyperFrames libraries](#hyperframes-libraries) |
| `third_party/gsap/gsap.min.js` | optional | parts and scenes that animate with GSAP (timelines and clips) | pinned version, SHA-256 checked | [GSAP](#gsap-timelines-in-parts-and-scenes) |
| Chrome or Chromium | optional | motion-graphic scenes and overlays (animated titles) | any recent version | [Chrome or Chromium](#chrome-or-chromium) |
| Blender | optional | 3D scenes rendered from a `.blend` file (`blender` in `SCENES_YAML.md`) | 4.2 or newer | [Blender](#blender) |
| Ollama | optional | the prompt assistant | any recent | [Ollama](#ollama-prompt-assistant) |

## System tools

`git`, `cmake` (3.18 or newer), a C/C++ compiler with `make`, and `curl`. They build `stable-diffusion.cpp` and `acestep.cpp` and download the rest.

```bash
# Arch / CachyOS / Manjaro
sudo pacman -S --needed git cmake gcc make curl ffmpeg
# Debian / Ubuntu
sudo apt-get update && sudo apt-get install -y git cmake build-essential curl ffmpeg
# Fedora (ffmpeg needs RPM Fusion, see below)
sudo dnf install -y git cmake gcc gcc-c++ make curl
```

The compiler must be one that your `nvcc` accepts as a host compiler: a very new `gcc` can be newer than the CUDA toolkit supports (see [Troubleshooting](#troubleshooting)).

## ffmpeg

`ffmpeg` and `ffprobe` time the narration and assemble the clips, and the clip phase encodes with `libx264`, so a build **with `libx264`** is required: the installer checks it and reports `[old]` (no libx264 encoder) otherwise. Arch, Debian and Ubuntu ship a full ffmpeg. Fedora's own `ffmpeg-free` has no `libx264`: enable RPM Fusion and swap to the full build.

```bash
sudo dnf install https://mirrors.rpmfusion.org/free/fedora/rpmfusion-free-release-$(rpm -E %fedora).noarch.rpm
sudo dnf swap ffmpeg-free ffmpeg --allowerasing
ffmpeg -hide_banner -encoders | grep -E 'libx264|aac|libmp3lame'    # the three must appear
```

## Go

Go 1.22 or newer builds `veritmo`. Arch and recent Ubuntu or Fedora packages are new enough (`go`, `golang-go`, `golang`); Debian 12's is 1.19 and too old. In that case download a current release from <https://go.dev/dl/> and put it on `PATH`:

```bash
sudo rm -rf /usr/local/go && sudo tar -C /usr/local -xzf go1.XX.Y.linux-amd64.tar.gz
export PATH=$PATH:/usr/local/go/bin
```

## NVIDIA driver and CUDA

Rendering (`sd`) and the music engine use CUDA.

- **Driver.** Install your distribution's NVIDIA driver and check that `nvidia-smi` lists the GPU. The installer does not install drivers.
- **CUDA toolkit 12.8 or newer.** `scripts/build-sd.sh` refuses older ones: 12.8 is the first with support for `sm_120` (Blackwell, RTX 50 series). Check with `nvcc --version`. Arch has a current `cuda` package (`sudo pacman -S cuda`; the installer does this itself when the repository version is at least 12.8). Debian, Ubuntu and Fedora repositories usually have older versions: install the toolkit from NVIDIA's page, <https://developer.nvidia.com/cuda-downloads>, choosing your distribution, and make sure `nvcc` is on `PATH` (usually `/usr/local/cuda/bin`).
- **Several GPUs.** Pin the work with `--gpu` (see [CLI.md](CLI.md#using-both-gpus)); the installer lists every GPU with its compute capability.
- **No NVIDIA GPU.** Everything that does not render still works (the import, the editor, narration). `sd` can be built for the CPU with `SD_CUDA=OFF bash scripts/build-sd.sh` (or `install.sh --cpu-sd`), but images then take many minutes each. The installer does not offer a CPU build of the music engine.

## stable-diffusion.cpp (`sd`)

```bash
bash scripts/build-sd.sh
```

Clones `leejet/stable-diffusion.cpp` into `third_party/`, pins it to the commit in `SD_REF` (master-949, `7867f6d`), builds it with CUDA and installs `bin/sd`. It takes several minutes and about 1 GB of disk. Idempotent: re-running fetches, checks out the pinned commit and rebuilds.

**Why it is pinned.** With `sd` master-1010 (`012c7f8`) the **LTX 2.3** engine writes an all-white clip: the run ends without an error, but the raw video that `sd` saves is already 255 in every frame (checked with ffmpeg's `signalstats`). SDXL, Krea 2, IntoRealism, Wan 5B, MiniMax H3 and the music engine are not affected. The same models, flags and compiler on master-949 give a normal clip. It is the `sd` version, not the machine: it was found on a laptop with an RTX 4080 (12 GB), CUDA 13.4 and GCC 15, and the two machines measured before (an RTX 4070 and an RTX 5060 Ti) used master-949 without noticing. To try another version, `SD_REF=<commit> bash scripts/build-sd.sh`, then render an LTX clip and look at it; see [the benchmark](DEVELOPMENT.md#benchmark-comparing-machines).

## Piper and a voice

```bash
bash scripts/get-piper.sh                       # Piper 2023.11.14-2 and the lessac voice, checked against their checksums
VOICE_ID=en_US-amy-medium bash scripts/get-piper.sh
```

Downloads the Piper binary (with its libraries) into `bin/` and a voice into `voices/` (a few tens of MB). More voices are available from the dashboard's Models screen (Voice tab), including Spanish ones (Spain, Mexico and Argentina, men and women). Each scene can use its own voice, and the editor's **Listen** button lets you hear one before generating. The release of Piper, the revision of the voices repository and the lessac voice are pinned and checked against a SHA-256; another `PIPER_VERSION` or `VOICE_ID` needs its checksums in `PIPER_SHA256`, `VOICE_ONNX_SHA256` and `VOICE_JSON_SHA256` (or `ALLOW_UNVERIFIED=1`, at your own risk).

## Music engine (acestep.cpp)

```bash
bash scripts/build-acestep.sh
```

Builds `acestep.cpp` (pinned to the version this project was measured with) into `bin/ace-lm`, `bin/ace-synth` and `bin/ace-server`. It needs the same CUDA setup as `sd`. The models are downloaded separately, from the dashboard's Models screen (Music tab), 4 to 8 GB per set. See [MUSIC_MODE.md](MUSIC_MODE.md).

## Music services (.NET)

```bash
bash scripts/get-services.sh              # the three, with CUDA
bash scripts/get-services.sh --only lionclap
bash scripts/get-services.sh --only triposr,stable-fast-3d    # the 3D generator (TripoSR and Stable Fast 3D), see MODEL_GENERATION.md; not part of the install tiers
```

HeartMuLa, DiffRhythm2 and LAION-CLAP are .NET services.  `VERITMO_SERVICES_SRC` points the script at the checkouts of the four repositories (`heartmula`, `diffrhythm2`, `lion-clap`, `musicgen-core`). `bash scripts/install.sh` offers both pieces (`dotnet`, the SDK 8 or newer, and `services`) and builds the services with `--cpu` when there is no NVIDIA card. It asks which services to build, in four tiers: `none`; `recognition` (LAION-CLAP and video-vision, the only ones that make sense without an NVIDIA card: CLAP takes about a second per clip on the CPU, while a generator is about 17 times slower than real time there); `generator` (those and DiffRhythm2, which also brings the MuQ-MuLan recognition); and `all` (and HeartMuLa too). Enter takes the one that fits the machine: `all` when a card has 15 GB or more (HeartMuLa's model and its codec did not fit one 12 GB card in the service), `generator` with a smaller NVIDIA card and `recognition` without one. `--services TIER` chooses it without asking, `--yes` takes the recommended one, and `get-services.sh --only a,b` builds any list of the four by hand. The 3D services (one picture to a 3D model) have their own tiers, asked in the same step: `none`; `triposr` (TripoSR); and `all` (TripoSR and Stable Fast 3D). Enter takes `all` with an NVIDIA card of 8 GB or more (Stable Fast 3D was measured at 8 GB of VRAM) and `none` otherwise; `--3d TIER` chooses it without asking. The tiers add up: `--services recognition --3d all` builds `lionclap,videovision,triposr,stable-fast-3d`. Building Stable Fast 3D needs no licence, its weights do: two of its files are gated on Hugging Face, so the licence must be accepted there and the Hugging Face key put in the Models screen before they can be downloaded; until then the service is built but its weights are not installed: the install of the engine (Environment screen) refuses with the reason, key missing or licence not accepted, and the engine stays unavailable. TripoSR works without any of it. The script publishes each into `bin/services/<name>` (about 5.8 GB each, nearly all of it the CUDA libraries; `--cpu` makes a build without them) and then keeps each library that is byte-identical in the three big services once, making the other copies hard links to it: the three take about 6.5 GB instead of 17 (measured: 12.3 of 18.8 GB of files of 1 MB or more were identical; a link needs one volume, and where it cannot be made the copies stay as they are). `--no-share` keeps separate copies and `--share-only` shares the services already installed without building anything; running them needs the **ASP.NET Core runtime** (the services are web applications), not only the base .NET runtime: `install.sh` checks for it and installs it with the SDK (on Arch `dotnet-sdk` and `aspnet-runtime`: the SDK package brings `dotnet-runtime` only, and a service started without the web runtime stops with `No frameworks were found` for `Microsoft.AspNetCore.App` in `.orchestrator/services/mo-<name>.log`; by hand, `sudo pacman -S aspnet-runtime`); on Debian, Ubuntu and Fedora the `dotnet-sdk-8.0` package already includes it. The weights are the ones in `models/music/` (DiffRhythm2's must be the converted ones: `dotnet run --project dotnet/diffrhythm2/src/DiffRhythm2.Cli -- install --model models/music/diffrhythm2 --accept-licenses`, or the equivalent in its README). The Environment screen starts and stops them. The script also publishes the `diffrhythm2` executable next to the DiffRhythm2 service: it is what "MuQ-MuLan" in "Take it from a video or audio" runs (`classify`, on the CPU), and it needs the audio weights converted once with `diffrhythm2 convert --model models/music/diffrhythm2 --audio` (1.3 GB, nothing more is downloaded; CC-BY-NC, non-commercial use only). The fourth service, **video-vision** (`bash scripts/get-services.sh --only videovision`, about 250 MB), runs the person mask and the depth estimator of the video-edit tools (`remove-background`, `depth`, `motion-control`) on the CPU with ONNX Runtime, so it needs no CUDA libraries and no Python; its two model files, `depth-anything-v2-small.onnx` (99 MB) and `u2net_human_seg.onnx` (176 MB), go in `models/video/` (the tools say which one is missing); the catalog has them as one entry, **Vision for editing video** (kind `vision`, 275 MB, from Hugging Face and the rembg releases), that the Models screen installs with one button. The Environment screen starts and stops it like the others, and the tools that need it are listed unavailable while it is stopped. Its source is in the `dotnet/` folder of the private edition, which the public edition does not carry: there the tools that need it are listed unavailable, with the reason. What is still open (where the binaries come from, the installer tiers and what one 12 GB card can run) is in `openspec/changes/plan-services-installation`.

## Music generators in Docker (Python), optional

HeartMuLa and DiffRhythm2 can also run as the Python containers of `docker/` instead of the .NET services above. It is an alternative, not a requirement. The server can run them itself: the setting `heartmula_backend` / `diffrhythm2_backend` (`dotnet` or `docker`, see [http-api.md](http-api.md)) says which way each generator runs on this machine, `/api/music/engines/{engine}/start` and `/stop` start and stop the container like the service (loopback only, the weights read-only, one card, it does not restart by itself), `GET /api/music/engines` says per backend what is missing (`backends`), and `POST /api/music/engines/{engine}/install` plans and runs the download of what is missing (the image, about 14 GB each; the weights; or the .NET service). The script below does the same by hand, and a container started with it is reached by its address, as an external service.

```bash
bash scripts/music-containers.sh build diffrhythm2   # Python and PyTorch: several GB of image
bash scripts/music-containers.sh weights diffrhythm2 # ~9.5 GB (HeartMuLa's weights: docker/heartmula/README.md)
bash scripts/music-containers.sh up diffrhythm2 1    # card 1; it prints the address to save in Settings
```

Needs Docker with the NVIDIA container toolkit. Tried on 2026-11-17 with DiffRhythm2 on the RTX 5060 Ti: the image built, `up` gave `ready: true` on `/health`, a 10 s song came out as 48 kHz stereo, the idle container held no video memory, and the server (with `diffrhythm2_host` set to `http://127.0.0.1:8788`) listed the engine as `available`, `mode: external`, not remote. HeartMuLa the same day on the same card: `ready: true`, a song from two lines of lyrics (`max_ms` 15000) came out as 6.2 s of 48 kHz stereo in 70 s (most of it loading the model), the card was left with 223 MiB, and the server listed it as `available`, `mode: external`. The Environment screen cannot start or stop these containers (it does for the .NET services): use `up` and `down`. See `docker/heartmula/README.md` and `docker/diffrhythm2/README.md`.

## yt-dlp

```bash
bash scripts/get-ytdlp.sh
```

Installs the standalone `yt-dlp` into `bin/` (no Python), checking its SHA-256 against the release. It is used only to add the audio of a YouTube address as a source. Run it again to update it when YouTube changes.

## Video upscalers (Real-ESRGAN and RIFE)

```bash
bash scripts/get-upscalers.sh                    # both (about 35 MB; RIFE's download is 430 MB, only what is used is kept)
bash scripts/get-upscalers.sh --only realesrgan  # one of them
```

Installs the authors' own Linux builds of [Real-ESRGAN](https://github.com/xinntao/Real-ESRGAN-ncnn-vulkan) (with the video model `realesr-animevideov3`, which beat the photo and anime models on frames of generated video) and [RIFE](https://github.com/nihui/rife-ncnn-vulkan) (network `rife-v4.6`) into `bin/realesrgan/` and `bin/rife/`, checking each download against the SHA-256 recorded in the script. They run on any graphics card with **Vulkan** (NVIDIA, AMD or Intel; not CUDA) and need no Python. They are used by the video-edit tools `scale` with `engine: quality` and `interpolate`; without them those tools still work, with the lanczos filter and `minterpolate`, and the result says so (the options list says it too, with this command). Measured on an RTX 5060 Ti: enlarging 5.3 s of 480x866 video takes 17 s (lanczos, 0.8 s), and making 2 s of 16 fps into 30 takes 2.8 s (`minterpolate`, 4.0 s). The enlarged frames go through the disk (about 1.5 bytes a pixel): a video that would not fit is made with the filter.

## Whisper (lyrics of a song)

```bash
bash scripts/get-whisper.sh           # the model (574 MB) into models/whisper/; checks for whisper-cli
bash scripts/get-whisper.sh --build   # also builds whisper-cli (CPU) into bin/ when there is none, from a pinned commit of whisper.cpp
```

Optional: only **Take it from a video or audio** (the music studio) needs it, to read the lyrics. It runs on the CPU, so the GPUs stay free; a song of 3 minutes takes about 45 seconds with 8 threads. `whisper-cli` is looked for in `bin/` first and then in the `PATH` (Arch has it in the `whisper.cpp` package). The model, Whisper large-v3-turbo quantised (`ggml-large-v3-turbo-q5_0.bin`), can also be installed from **Models** (Music tab); the script checks its size. Run it again to repair a download that was cut.

## GSAP (timelines in parts and scenes)

```bash
bash scripts/get-gsap.sh   # gsap.min.js 3.13.0 into third_party/gsap/, checked against its SHA-256 (the file is not committed)
```

Optional: only the parts and the scenes that animate with a GSAP timeline (`window.__timelines`, see `docs/MUSIC_MODE_MOTION.md`) need it; everything that moves with `window.__partSeek` works without it. The page writes the file inline, so nothing is loaded from the network. Run it again to repair a file that was changed. The licence (GSAP's "no charge" licence, which is not an open-source one) is read in `THIRD_PARTY.md`. `bash scripts/install.sh` offers it as the optional component `gsap` (`--only gsap`). It is a file, not a service: nothing is started or stopped, the page that needs it carries it. The blocks of HyperFrames are not installed by the installer: they are converted from a folder you bring (`veritmo parts import-hyperframes`, see `docs/MUSIC_MODE_MOTION.md`).

## HyperFrames libraries

```bash
bash scripts/get-hyperframes-libs.sh   # 60 files (~8 MB) into third_party/hyperframes-libs/, each checked against its SHA-256 (not committed)
```

Optional: only the HyperFrames blocks need them. The list, with the hash of every file, is `internal/music/hyperframes/libs.txt`. While a block is drawn the page can only get these files, served from here, and nothing from the network.

## HyperFrames blocks

```bash
bash scripts/get-hyperframes-blocks.sh   # the libraries (above) + the 210 blocks, cloned at a pinned commit and imported whole
HF_CHECK=1 bash scripts/get-hyperframes-blocks.sh   # the same, running the minimum checks on each block (needs Chrome; a few minutes)
```

Optional, and it needs `bin/veritmo` (the installer builds it first). The blocks go to `hyperframes/blocks/` (or `$HYPERFRAMES_DIR`); the clone is kept in `~/.cache/veritmo/hyperframes` and reused. Run the checks later with `bin/veritmo blocks check`. See [HYPERFRAMES.md](HYPERFRAMES.md).

## HyperFrames runtime

```bash
bash scripts/get-hyperframes-runtime.sh   # builds the runtime of HyperFrames from its source at the pinned commit
```

Optional, and it needs Node 20 or newer, npm, git and the network, only for this step (the server does not need them). The installer has a component for Node (`node`: `nodejs` and `npm` from the package manager, refusing a package older than 20) that this one depends on. It clones HyperFrames at the commit the blocks come from, installs esbuild and four npm packages with pinned versions in a temporary folder and bundles `packages/core/src/runtime/entry.ts` into `third_party/hyperframes-runtime/hyperframe.runtime.iife.js` (about 690 kB; the build gives the same file every time and its SHA-256 is checked). With it, blocks and components that mount sub-compositions (`data-composition-src`) are drawn by HyperFrames' own runtime; without it the server draws the blocks with its own small runtime and the components that need mounting are not usable.

## Voice separation for the timing of the words

```bash
bash scripts/get-demucs.sh           # the htdemucs weights (80 MB) into models/demucs/, checked by size and SHA-256
bash scripts/get-demucs.sh --build   # also builds the multi-threaded CLI (CPU) into bin/demucs, from a pinned commit of demucs.cpp
```

Optional: when a song's words are heard (`transcribe`), the voice can be separated from the mix first, which gives Whisper a track without drums and synths and tells the server where the voice is, so that a word Whisper invents over the drums is left out. Without it the words are heard from the whole mix. It runs on the CPU, like Whisper, so the GPUs stay free. The build needs `git`, `cmake` and a C++17 compiler with OpenMP (Arch: `sudo pacman -S base-devel cmake git`; Debian and Ubuntu: `sudo apt-get install build-essential cmake git`) and fetches the sources of its submodules (about 340 MB of sources, a minute of build); with OpenBLAS installed (`openblas`, `libopenblas-openmp-dev`) `DEMUCS_OPENBLAS=1 bash scripts/get-demucs.sh --build` builds against it. A song takes about 0.8 times its length with 4 threads and about 6 GB of memory (measured on a 20-thread machine: 50 s for 60 s of audio); the server separates only when no run is queued or active and that memory is free.

## Blender

Optional: only the scenes made from a `.blend` file (`blender:` in `docs/SCENES_YAML.md`) need it. The installer reports it (`bash scripts/install.sh --only blender` offers your package manager's `blender`); it counts as present when `blender` is in the `PATH` and is version 4.2 or newer. Without it nothing else changes: the Environment screen lists it as unavailable with the reason, and a project that has such a scene is refused before anything runs. The renders use the Cycles engine with OptiX on **one** NVIDIA card (the one the run gives the scene; the others stay free for `sd`), or the CPU when the run lists no card. The Intel integrated GPU is not used (it is not exposed to Cycles). Nothing is downloaded.

## Chrome or Chromium

Optional: only the motion-graphic scenes and the overlays (animated titles and lower thirds) need it, to draw the pages the model writes. The installer offers it (`bash scripts/install.sh --only browser`) with your package manager's `chromium`; it counts as present when any of the names below is in the `PATH`. The orchestrator finds `google-chrome-stable`, `google-chrome`, `chromium` or `chromium-browser` in the `PATH`; install one with your system package manager (`sudo pacman -S chromium`, `sudo apt install chromium`, …) or point to another with the **Browser** card of Settings. The Environment screen shows which one is used. Nothing is downloaded and no Node.js or Playwright is needed at run time.

## Models

An image model is needed to render. The simplest start:

```bash
bash scripts/get-models.sh        # Stable Diffusion 1.5 (4.3 GB) into models/, pinned to a revision and checked against its SHA-256 (another MODEL_URL needs MODEL_SHA256)
```

Everything else is installed from the dashboard's **Models** screen, whose catalog downloads each set in the background (see [SERVER.md](SERVER.md#models-voices-and-loras)):

| Set | Size | Notes |
|---|---|---|
| Stable Diffusion 1.5 | 4.3 GB | the small default; also the base of AnimateDiff |
| SDXL base / Juggernaut XL / Pony | about 7 GB each | 10 GB of VRAM |
| Krea 2 Turbo (Q4) | 10.2 GB | photorealistic; about 14 GB of VRAM |
| Wan 2.2 5B (Q4 / Q8), A14B (Q4) | 8.0 / 12.9 / 21.2 GB | animation; the A14B needs about 21 GB of RAM |
| AnimateDiff v3 | 0.8 GB | light animation on SD 1.5 |
| ACE-Step 1.5 sets | 4.4 to 8.4 GB | music |
| Piper voices | about 0.1 GB each | narration |

Some downloads need an account key (Hugging Face for gated models); the Models screen explains and stores it.

## The veritmo binary

```bash
go build -o bin/veritmo ./cmd/veritmo
```

This repository is the server only: the binary has no front end inside and needs no Node.js to build. A dashboard is a separate project (`dashboards-lab`) that you build there and hand to the server as a folder with `--web-dir` (see [SERVER.md](SERVER.md)). `./serve.sh` builds its own server binary (`bin/veritmo-api`) and starts it.

### Updating from mediaOrquestator (the former name)

The program was called mediaOrquestator before it was called Veritmo. Updating a machine that ran it under the old name moves four things, all on their own except the last two:

- **The directories.** `~/.config/mediaorquestator/` (the token, the settings, the keys, `models-dir`, `media-dir`, `web-dir`, the pid files) becomes `~/.config/veritmo/`, and `~/.cache/mediaorquestator/` becomes `~/.cache/veritmo/`. The first thing that starts (the server, any command, `scripts/serve.sh` or the installer) renames the old directory and says so in one line; nothing is copied and nothing is deleted. If both directories exist the new one is used and a warning says the old one is ignored and may be deleted. A cron or a script of yours that reads `~/.config/mediaorquestator/token` has to be pointed at the new path.
- **The binary.** `bin/orchestrator` and `bin/orchestrator-api` are now `bin/veritmo` and `bin/veritmo-api` (`./serve.sh` builds the new one and still recognises a server started under the old name, so `./serve.sh --stop` stops it). The old binaries can be deleted. `VERITMO_TOKEN` and `VERITMO_WEB_DIR` keep their names.
- **The environment variables.** `MO_DB_*`, `MO_DOCKER`, `MO_BROWSER_GPU` and the rest of the `MO_*` are now `VERITMO_*`. The eight variables that began with `ORCHESTRATOR_` (`TOKEN`, `TOKEN_FILE`, `WEB_DIR`, `MIN_FREE_GB`, `STORE_MB`, `ASSIST_MODEL`, `VISION_MODEL`, `DESCRIBE_CPU`) are `VERITMO_*` too. The server and the commands still read the old names (`MO_*` and `ORCHESTRATOR_*`) for one release and log `MO_X is obsolete, use VERITMO_X` (or `ORCHESTRATOR_X ...`); the compose files read only the new ones, so rename them in your `.env`. The default database of the compose stack is now called `veritmo` (user `veritmo`): a stack whose volume was created with the old defaults keeps working with `VERITMO_DB_NAME=mediaorquestator` and `VERITMO_DB_USER=mo` in its `.env`.
- **Docker.** The images are `veritmo/<component>`, the compose project is `veritmo` and the music and 3D containers are `vt-<engine>` (they were `mo-<engine>`). Stop the old services first (the Environment screen, or `docker rm -f mo-heartmula mo-diffrhythm2 mo-clap mo-videovision mo-model3d`), then rebuild (`docker compose build`) and, once the new ones run, remove what is left of the old name: `docker rmi $(docker images 'mediaorquestator/*' -q)`.

The Prometheus families are now `veritmo_*` (`veritmo_info`, `veritmo_runs`, ...): the history of the old series stays under the old names, and the Grafana dashboards of [`docs/grafana/`](grafana/README.md) are the renamed ones, to import again.

## Ollama (prompt assistant)

Optional. The prompt assistant runs a local model on the CPU through [Ollama](https://ollama.com). Install it from <https://ollama.com/download> (Arch also has an `ollama` package; the installer never pipes a downloaded script into a shell), start it, and pull the default model:

```bash
ollama pull qwen3:8b       # about 5 GB
```

To use **Describe a reference** (upload a picture or a clip and get its prompt) pull a vision model too, for example `ollama pull gemma4:26b` (about 17 GB). Nothing is downloaded by itself, and the dashboard's **Settings** screen shows and tests the Ollama address (it can be another machine) and can register other providers; see [SERVER.md](SERVER.md#settings-where-ollama-is). Other models and the details are in [ASSISTANT.md](ASSISTANT.md).

## With Docker Compose

The other way to install everything is the Compose stack (`compose.yaml`): the server and what it works with start from one file, and the programs the installer builds are built **inside an image** and written into folders of this machine. It needs Docker with Compose v2 and the NVIDIA Container Toolkit (it does not need Go, CMake, CUDA or Node on the host). [COMPOSE.md](COMPOSE.md) has the whole story; the installation itself is:

```bash
bash scripts/compose-init.sh                              # checks Docker and the NVIDIA runtime, makes .env with generated secrets and the folders
docker compose --profile install run --rm installer check # what is installed and what is missing; changes nothing
docker compose --profile install run --rm installer       # installs what is missing (the first time builds sd with CUDA: it takes a while)
docker compose up -d                                      # the server and its pieces
```

What the step installs is the same as the native installer and with the same pins, by the same scripts (`build-sd.sh`, `get-piper.sh`, `get-ytdlp.sh`, `get-gsap.sh`, `get-hyperframes-libs.sh`, `get-hyperframes-runtime.sh`, `build-acestep.sh`; optionally `whisper`, `demucs`, `upscalers`, `hfblocks` and `models` with `INSTALL_COMPONENTS` in `.env`). The difference is **where**: the programs go to `INSTALL_PATH/bin` and `INSTALL_PATH/third_party`, the models to `MODELS_PATH`, the voices to `VOICES_PATH` and the HyperFrames blocks to `HYPERFRAMES_PATH`, all folders of this machine given in `.env`, and the server of the stack mounts them. Running the step again builds nothing that is installed and says so. A native install and the stack can share the folders of the models, the voices and the blocks; do not share `bin/` between them: the programs of each are built against the libraries of the system that runs them.

## Manual installation

The installer does only this, in this order:

1. Install the [system tools](#system-tools) and [ffmpeg](#ffmpeg), [Go](#go) and, with a GPU, the [NVIDIA driver and CUDA 12.8+](#nvidia-driver-and-cuda).
2. `bash scripts/build-sd.sh`, `bash scripts/get-piper.sh`, `bash scripts/get-models.sh`; optionally `bash scripts/build-acestep.sh` and `bash scripts/get-ytdlp.sh`.
3. `go build -o bin/veritmo ./cmd/veritmo`
4. `bin/veritmo data migrate`: creates the database of the server (SQLite in `.orchestrator/orchestrator.db` unless another is saved in Settings), and brings it up to date after an update.
5. `./serve.sh --local`; the server answers on <http://127.0.0.1:4666>.

On a distribution the installer does not know, it lists the tools to install by hand and the rest works unchanged.

## Troubleshooting

- **`nvcc` not found, or too old.** See [NVIDIA driver and CUDA](#nvidia-driver-and-cuda). After installing the toolkit, open a new shell so `PATH` is updated.
- **The `sd` or music build fails with a compiler error.** A host compiler newer than the CUDA toolkit supports is the usual cause. Install an older `gcc` (for example `gcc-13` or `gcc-14`) next to the current one and tell the build to use it: `NVCC_CCBIN=g++-13 bash scripts/build-acestep.sh` for the music engine (that script also tries to pick a compatible one by itself), and `CUDAHOSTCXX=g++-13 bash scripts/build-sd.sh` for `sd` (CMake reads `CUDAHOSTCXX`). Delete `third_party/<project>/build` first if CMake already configured with the other compiler.
- **An LTX 2.3 clip comes out all white (and small, a few KB).** `sd` newer than master-949 breaks LTX; see [stable-diffusion.cpp](#stable-diffusioncpp-sd). Rebuild with `bash scripts/build-sd.sh` (it checks out the pinned commit) and run the clip again: the cache is keyed on the inputs, so use `--force` or change the scene.
- **The music build says GCC is too new for `nvcc`.** On Arch the current `gcc` (16 at the time of writing) is newer than CUDA 13.4 accepts for `acestep.cpp`: `sudo pacman -S gcc15` and `NVCC_CCBIN=g++-15 bash scripts/build-acestep.sh`. `sd` built with the default compiler on that same machine.
- **The model does not fit or the machine runs out of memory.** A model is copied to RAM as well as to VRAM. Close other heavy programs, use a smaller or more quantized set, and keep `MEM_MAX` (85% of the RAM by default in `./serve.sh`) below your RAM; on a 32 GB machine see [the next section](#machines-with-32-gb-of-ram).

### Machines with 32 GB of RAM

A model is copied to RAM as well as to VRAM while it loads, and `sd` makes a pinned copy of it besides: a 13 GB checkpoint with its text model reached about 17 GB resident, and with the page cache and the desktop on top, the 30 GB development machine froze (swap thrashing) and had to be restarted. What keeps it from happening is a **hard cap with no swap**: the process that goes over is killed, not the machine.

- **The server** (`./serve.sh`): `MEM_MAX` is 85% of the installed RAM by default, about 26 GB on 32 GB. With a desktop and a browser open, **`MEM_MAX=24G ./serve.sh`** is the safer value, the one the development machine runs with (the rest is for the system, the cache and the services that run beside it: Ollama, the music and 3D containers). It uses `systemd-run --user --scope -p MemoryMax=… -p MemorySwapMax=0`; without `systemd-run` it starts with no cap and says so.
- **A program by hand** (`sd`, a model conversion, a test of a big model): `CUDA_VISIBLE_DEVICES=<card> timeout <seconds> systemd-run --user --scope -q -p MemoryMax=24G -p MemorySwapMax=0 bin/sd …`. Give `timeout` the time the job needs plus a margin: one that was too short cut a slow but healthy run with no error to say why.
- **Starting the server by hand**: `systemd-run --user --scope -p MemoryMax=24G -p MemorySwapMax=0 bin/veritmo serve …`.
- With 32 GB, the 12.9 GB Wan 2.2 5B (Q8) and the Krea 2 and SDXL checkpoints fit; the A14B video set (about 21 GB of RAM) and a checkpoint of 14 GB or more next to an LLM leave no room: use the lighter set or a smaller quantization. The memory the containers (music, 3D) take is not capped by this: the 3D one was only measured on the card (VRAM: TripoSR 3.4 GB in the container and up to 5.8 GB as a .NET service at `high` detail, Stable Fast 3D 8 GB), not in RAM.
- **Ollama starves the render GPU of VRAM.** See the note under [Requirements (reference)](#requirements-reference).
- **`ffmpeg` has no `libx264`.** See [ffmpeg](#ffmpeg).
- **The root of the server shows a plain-text note instead of a dashboard.** That is expected: the server has no front end of its own. Build a dashboard in `dashboards-lab` and start the server with `--web-dir <folder>` (see [SERVER.md](SERVER.md)).
- **A step fails halfway.** Read `~/.cache/veritmo/install.log`, fix the cause and run the installer again; it continues from what is still missing.

## Requirements (reference)

- **Go >= 1.22** (see `go.mod`).
- **Native binaries on `PATH`** (or resolvable from the project root): `piper`, `sd` (stable-diffusion.cpp), `ffmpeg`, `ffprobe`. Music mode instead needs `ace-synth` (and `ace-lm` for tracks the language model composes), built by `scripts/build-acestep.sh`, plus the GGUF models in `models/music/`. Optionally `nvidia-smi`, used only for the advisory `--gpu` free-VRAM warning.
- **Shell**: developed against CachyOS with `fish` as the interactive shell. The setup scripts under `scripts/` are plain POSIX-ish `bash` (`#!/usr/bin/env bash`), so run them with `bash scripts/<name>.sh` rather than sourcing them from `fish` directly; `fish` cannot source `bash` scripts, but invoking `bash <script>` works from any shell.
- **Two-GPU setup**: this pipeline is designed around a machine with two NVIDIA GPUs — one (e.g. an RTX 4070) driving the display and everything else running locally, and a second (e.g. an RTX 5060 Ti) reserved for `sd`'s CUDA render work. Pass `--gpu <index>` to pin the render phase to the second GPU via `CUDA_VISIBLE_DEVICES`; Piper, ffmpeg, and ffprobe don't need CUDA and are unaffected by this flag. Use `nvidia-smi -L` to confirm which index corresponds to which card.
- **Ollama VRAM retention caveat**: if you also run [Ollama](https://ollama.com) on this machine, note that it keeps a model resident in VRAM for a while after last use (`OLLAMA_KEEP_ALIVE`, 5 minutes by default) before unloading it. If Ollama happens to share the GPU you pass to `--gpu`, that retained model can starve `sd`'s VRAM budget even though Ollama "isn't doing anything" at the moment. Keep Ollama pinned to the *display* GPU (e.g. via `OLLAMA_CUDA_VISIBLE_DEVICES` or by running it when the display GPU is otherwise idle), or lower `OLLAMA_KEEP_ALIVE`, so it never contends with the GPU `veritmo` uses for rendering.


## Setup scripts (reference)

None of the following scripts run automatically — the pipeline never builds or downloads anything for you. Run each manually, once, before first use. All three are idempotent (safe to re-run; they skip work that's already done).

```bash
bash scripts/build-sd.sh    # clones + builds stable-diffusion.cpp with CUDA (-DSD_CUDA=ON) into bin/sd
bash scripts/get-piper.sh   # downloads the Piper binary into bin/piper and a default voice into voices/
bash scripts/get-ytdlp.sh   # optional: yt-dlp into bin/, to add a source from a YouTube address
bash scripts/get-whisper.sh # optional: the Whisper model (and whisper-cli) to read the lyrics of a song
bash scripts/get-models.sh  # downloads a stable-diffusion.cpp-compatible checkpoint into models/
```

- `scripts/build-sd.sh` requires `nvcc >= 12.8` on `PATH` (needed for `sm_120` / Blackwell-class GPUs) and will refuse to build otherwise.
- `scripts/get-piper.sh` accepts `PIPER_VERSION` and `VOICE_ID` environment overrides (defaults: `latest` and `en_US-lessac-medium`) if you want a different Piper release or voice.
- `scripts/get-models.sh` accepts `MODEL_URL` and `MODEL_NAME` overrides if you want a different checkpoint than the small default.

Once these have run, `bin/`, `voices/`, and `models/` hold what `scenes.yaml` (see [SCENES_YAML.md](SCENES_YAML.md)) references.
