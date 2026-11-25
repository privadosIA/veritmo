# Veritmo

**Veritmo — ver el ritmo.** A Go server that turns a song into typographic motion graphics, and also makes videos, images and 3D models from a script.

> **Live examples:** videos, songs, animation and images made with this server on a local machine, with how they were made: [orquestator.xuxo.net](https://orquestator.xuxo.net/) (es/en).

A headless Go CLI (`veritmo`) that turns a declarative `scenes.yaml` script into a narrated video — or a batch of still images — by driving native subprocess binaries: [Piper](https://github.com/rhasspy/piper) for text-to-speech, [stable-diffusion.cpp](https://github.com/leejet/stable-diffusion.cpp) (`sd`) for per-scene image generation, and `ffmpeg`/`ffprobe` for timing and assembly.

In **video mode** (the default), each scene runs: **TTS** (Piper) → **probe** (ffprobe measures the narration's duration) → **render** (`sd` renders the scene's prompt to a still image) → **clip** (ffmpeg animates that image with the scene's optional camera `motion`/`look` presets for the probed duration — or `silence_seconds` for silent scenes — and muxes in the narration or a silent track). All scene clips are then concatenated into the final video. In **images mode** (`output.mode: images` or `--mode images`), only **render** (× `variants`) and a lightweight **export** phase run, producing PNGs plus a reproducibility manifest — see `docs/IMAGE_MODE.md`. A third, independent **music mode** (`output.mode: music` or `--mode music`) turns a `tracks:` list of style prompts and optional lyrics into complete songs with [acestep.cpp](https://github.com/ServeurpersoCom/acestep.cpp) (`ace-lm` + `ace-synth`) — see `docs/MUSIC_MODE.md`. Songs can optionally be made with **HeartMuLa**, a higher-quality (and slower) generator that runs as a local .NET service (or on another machine), and is offered only when it is available. The music studio also compares and keeps variants, lengthens a song, marks favorites, joins songs in a mixer (beat and bar magnet, crossfades, master volume and −14 LUFS normalization) and chooses a voice and a quality level.

Each scene's TTS, render, and clip output is cached independently by content hash, so re-running after editing one scene only redoes that scene's affected phases. A companion `veritmo import` subcommand converts external datasets of scenes, animated graphics or songs (JSON, or HTML embedding a `const data` array) into a ready-to-fill `scenes.yaml`, and `veritmo check` finds the deformed or drifting clips of an animated project (with an Ollama vision model if you name one, which also looks at each face enlarged) and can fix them, and `veritmo export` writes a project back as a dataset (or as a zip package) to run it in another installation.

The editor can also **describe a reference**: you upload a picture or a clip, a local vision model (through Ollama) writes the prompt that would draw it, and the new scene is generated from that text alone (the file is never handed to the generator). A clip can be one scene or **one scene per shot**. Every person in the file is age-checked first (a minor, or an age that cannot be confirmed, is refused and the file deleted) and the model's own refusals are reported, never replaced by an invented prompt — see `docs/ASSISTANT.md`. The AI helpers (the prompt assistant, describing, the quality check) can also run on **another provider**: an Ollama on another machine or any OpenAI-compatible server, registered in the dashboard's *Settings* screen (API keys stay on the server and are never shown again) and chosen **per task**. They all start on the local Ollama; a remote provider needs your explicit consent, tied to its address, shows a "☁ external" badge next to every button that uses it, has a cap on calls per job, and nothing ever switches provider by itself — see `docs/SERVER.md`.

**Animation** is chosen per project: Wan, AnimateDiff, MiniMax-H3 and LTX-2.3 (the last two also write an audio track, which can be kept as quiet ambience under the narration with `model_audio`). A scene can also be a **motion graphic** (an animated title, list, chart or credits whose HTML page a local model writes and Chrome draws frame by frame) and any scene can carry an **overlay** (an animated lower third drawn over its picture or clip); both declare their entrances as a *timeline* of tracks that the editor shows as draggable bars (in a window of its own for clips longer than 4 s) and lets you retouch without asking the model again — see `docs/SCENES_YAML.md`. Controls that need a model are hidden when none is available.

**Retouch:** a picture of yours can be edited by an instruction ("put a red cap on him") instead of redrawn. Two engines, both installed from the *Retouch* tab of Models: **Fast** (FLUX.1 Kontext, about 40 s on a 12 GB card, non-commercial licence) and **Precise** (Qwen-Image-Edit, about 2 min, Apache 2.0). It is a look of a project, of the *Create* page and of the scene editor, and it only applies when you pick it.

**Edit a video you already have:** seven tools, each with one request and no project: **scale** it (with a super-resolution network, Real-ESRGAN, when installed), raise its **frame rate** (RIFE), **reframe** it to another aspect ratio (the crop follows the person), **remove the background** (a colour, or a WebM with transparency), make its **depth** map, **extend** it (a new clip that starts at its last frame, made with MiniMax H3 or LTX 2.3) and **move the movement of the people of a video onto another picture** (*motion control*, Wan 2.1 VACE 1.3B, about 5 minutes for 2 seconds, soft and made for people), optionally from a **library of motions** you build from videos you own. The person mask and the depth run on the CPU in a small service of their own, so they take nothing from the cards (its source ships with the private edition only; without it those tools are listed as unavailable, with the reason). Every tool says why it is not available when it is not (a missing model, a stopped service, a card too small) and falls back to ffmpeg, saying so, when its network is not installed. See [docs/VIDEO_EDIT.md](docs/VIDEO_EDIT.md).

**From a phone:** the *Create* page (the first one of the menu, and the one a phone opens at the root address) is made for a thumb: choose what to make (video, image or song, or let the AI decide: choosing saves a call to the assistant), say or write what you want (**Improve with AI** rewrites it with more detail, in your language), check what was understood, pick a resolution level (draft by default on a phone), and see the video, image or song when it is done; a finished picture has an **Edit** button to retouch the result, and **Delete session** clears everything the tab made. On a narrow screen the interface starts in a **simple version** with only Create and Gallery (a switch in the menu goes to the advanced one; the API reference and the settings never open on a phone). The voice is read on your own server (Whisper, on the CPU); the browser only gives the microphone to a secure page, so serve the dashboard over HTTPS — see `docs/SERVER.md` ("HTTPS for the microphone").

The dashboard starts in English; the *English / Español* switch at the bottom of the side menu changes it (the choice is remembered in the browser).

New here? **[docs/TUTORIAL.md](docs/TUTORIAL.md)** explains the concepts, the same things from the terminal and a complete worked example ([also in Spanish](docs/TUTORIAL.es.md)).

## An example

A 30-second clip made entirely with the orchestrator: two cats in karate suits who bow, spar and bow again in a dojo. It is 15 scenes of a `scenes.yaml` (one still picture per scene, animated with MiniMax-H3 using the picture as the first frame and its 8-step LoRA, about 3 minutes per scene on a 16 GB card, 48 minutes in all). The whole file is [docs/examples/cats-karate.scenes.yaml](docs/examples/cats-karate.scenes.yaml); `defaults.subject_prompt` describes the two cats and the dojo once and is prepended to every scene, so they look the same in every cut, and each scene only says what its shot shows and what moves:

```yaml
defaults:
  subject_prompt: >-
    photo of an orange tabby cat in a white karate gi with a black belt and a grey tuxedo cat in a black
    karate gi with a red belt, both standing upright on two legs like martial artists, detailed fur,
    traditional wooden dojo, tatami mats, paper screens, warm sunlight through the windows, cinematic, 35mm,
  animate:
    engine: minimax-h3
    first_frame: true        # the scene's picture is the first frame of the clip
    diffusion_model: models/video/minimax_h3_fl2va_pruned-Q4_K_M.gguf
    llm: models/video/qwen3vl_32b_minimax_h3-Q4_K_M.gguf
    vae: models/video/minimax_h3_video_vae_fp16.safetensors
    audio_vae: models/video/minimax_h3_audio_vae_fp32.safetensors
    lora: models/video/minimax_h3_fl2v_turbo_8step.safetensors   # 8 steps
scenes:
  - id: s07_roundhouse
    seed: 207
    silence_seconds: 2
    animate: true
    prompt: wide shot, the grey cat throwing a high roundhouse kick while the orange cat ducks
    motion_prompt: the grey cat swings a high roundhouse kick, the orange cat ducks under it, static camera
```

<video src="docs/media/cats-karate.mp4" controls muted width="720" poster="docs/media/cats-karate-shots.jpg"></video>

[![The 15 shots of the clip](docs/media/cats-karate-shots.jpg)](docs/media/cats-karate.mp4)

*(If your viewer does not play the video above, click the picture: it is the same clip, [docs/media/cats-karate.mp4](docs/media/cats-karate.mp4), 1024x576, 30 s, silent.)*

This behavior is specified in detail in `openspec/specs/` (one capability per `internal/` package, plus `scene-import`, `export` and the management server and dashboard); the documents listed below are derived from those specs.

> **This repository is the server only:** the API, its access token and its security. The dashboards are a separate project, [`dashboards-labs`](https://github.com/privadosIA/dashboards-labs), and the server serves any built front end from a folder with `veritmo serve --web-dir <folder>` (see [docs/SERVER.md](docs/SERVER.md#a-front-end-from-a-folder---web-dir)). `./serve.sh` starts only the server, and the binary has no front end inside. The screens described below are those of the dashboards.

## Quick start

**Platforms.** Supported and tested: Linux x86_64 with an NVIDIA GPU. Windows and macOS are not supported yet; [docs/INSTALL.md](docs/INSTALL.md#other-platforms-windows-and-macos) says what might work there (WSL2, Docker Desktop, Apple Silicon) and what has not been tried. Cards that are not NVIDIA: the ggml engines (`sd`, `acestep`) have Vulkan, Metal and (`sd`) ROCm backends, but the PyTorch ones (HeartMuLa, DiffRhythm2) need CUDA, or ROCm through the Docker containers, and TorchSharp has no ROCm build; none of it is tried here ([details](docs/INSTALL.md#cards-that-are-not-nvidia)). The dashboard itself only needs a browser, so it can be used from any system against a server running on Linux.

On Linux x86_64 with an NVIDIA GPU (see [Installation](docs/INSTALL.md) for the details and other cases):

```bash
bash scripts/install.sh --check   # what this machine has, and what is missing
bash scripts/install.sh           # install the missing pieces, asking about each one (or --yes)
./serve.sh --local                # the server on http://127.0.0.1:4666
```

Instead of installing on the host, the whole system can run from **one Docker Compose file** (`compose.yaml`): the server, its MySQL database, the migration, Prometheus and Grafana already configured against each other, with the projects, models, voices and installed programs in folders of your machine (see [docs/COMPOSE.md](docs/COMPOSE.md)):

```bash
bash scripts/compose-init.sh                              # checks Docker and the NVIDIA runtime; makes .env with generated secrets
docker compose --profile install run --rm installer      # builds the programs into your folders (once)
docker compose up -d                                      # starts everything
```

The stack leaves out the dashboard (a separate project that connects to the server from outside; see [docs/COMPOSE.md](docs/COMPOSE.md#the-dashboard-connects-from-outside)) and a container for the .NET services (the song recognition and the vision of the video-edit tools): the server runs those itself from the installation folder, and where it has none the tasks that need them are reported unavailable, with their reason. The installer step of the public edition does not build them.

The installer also has optional components for the animation engine of [HyperFrames](docs/HYPERFRAMES.md): `hflibs` (the libraries its blocks load, kept local), `hfblocks` (the 210 blocks and 238 components, fetched once) and `hfruntime` (its runtime, built once; it needs Node, which the installer offers as `node`). Between the scenes of a video the join can be one of its 14 shader transitions (`transition: shader:glitch`, with an optional `transition_accent` colour; see [docs/SCENES_YAML.md](docs/SCENES_YAML.md)). If you would rather not use .NET for the music generators, the Python versions of HeartMuLa and DiffRhythm2 run as Docker containers (`scripts/music-containers.sh`, optional; see [docs/INSTALL.md](docs/INSTALL.md)).

The projects (scenes, sources, cache and results) can live on another, bigger disk: set `MEDIA_DIR` (or write the path in `~/.config/veritmo/media-dir`) and move the existing ones with `veritmo media migrate`; the API reports that directory and its free space in `GET /api/storage`. The model files can be moved to another disk too (`MODELS_DIR`, `veritmo models migrate`); `GET /api/storage` also reports that directory and its free space. See [docs/SERVER.md](docs/SERVER.md).

Which browser pages may call the API from another origin (CORS) is decided by the server, on its own machine: `cors_origins` and `cors_networks` in its settings file, edited with `veritmo cors add|remove|list`, never through the API. See [docs/SERVER.md](docs/SERVER.md).

## Documentation

| Topic | Document |
|---|---|
| Install it on a machine (requirements, the installer, per-distribution commands, troubleshooting) | [docs/INSTALL.md](docs/INSTALL.md) |
| Concepts, the terminal and a complete worked example | [docs/TUTORIAL.md](docs/TUTORIAL.md) ([español](docs/TUTORIAL.es.md)) |
| The command line: usage, flags, using both GPUs | [docs/CLI.md](docs/CLI.md) |
| The `scenes.yaml` reference: scenes, animation, own pictures and clips, motion graphics and overlays, model sets, images and music modes | [docs/SCENES_YAML.md](docs/SCENES_YAML.md) |
| The management server and dashboard: API, models, voices, LoRAs, remote use, Settings (the Ollama address, providers and API keys, which provider runs each task) | [docs/SERVER.md](docs/SERVER.md) |
| The HTTP API reference (including settings, providers, routing and describe) | [docs/http-api.md](docs/http-api.md) |
| The prompt assistant, describing a picture or a clip, and other model providers (local Ollama by default) | [docs/ASSISTANT.md](docs/ASSISTANT.md) |
| Importing datasets (scenes, animated graphics and songs, with worked examples), exporting, and background music | [docs/IMPORT.md](docs/IMPORT.md) |
| Images mode | [docs/IMAGE_MODE.md](docs/IMAGE_MODE.md) |
| Editing a video you already have: scale, frame rate, reframe, remove background, depth, extend, motion control and the library of motions | [docs/VIDEO_EDIT.md](docs/VIDEO_EDIT.md) |
| Music mode, including the optional HeartMuLa generator | [docs/MUSIC_MODE.md](docs/MUSIC_MODE.md) |
| HyperFrames: 210 blocks and 238 components kept whole (local libraries, checks, thumbnails, edit and preview, render to a clip, layers over a verse, the script writer uses them) | [docs/HYPERFRAMES.md](docs/HYPERFRAMES.md) |
| The `--json` event stream | [docs/events.md](docs/events.md) |
| Building, testing, the OpenSpec workflow, and working on a dashboard against the contract | [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md#working-on-a-dashboard-against-the-contract) |

## Related repositories

Depends on `heartmula`, `diffrhythm2` and `lion-clap` (the music services it starts) and, through them, on `musicgen-core`; `triposr` and `stable-fast-3d` are the .NET ports of the 3D model makers (the `model3d` container stays as the fallback). Used by `dashboards-labs` (its API contract).

| Repository | What it is |
|---|---|
| [`Veritmo`](https://github.com/privadosIA/veritmo) (this repository) | The server: the HTTP API and its contract, the pipelines (video, images, music), the run history and the music services it starts. |
| [`dashboards-labs`](https://github.com/privadosIA/dashboards-labs) | The dashboards (Angular) that talk to the server through its OpenAPI contract. |
| [`heartmula`](https://github.com/privadosIA/heartmula) | C# (.NET 8 + TorchSharp) port of [heartlib](https://github.com/HeartMuLa/heartlib): the HeartMuLa song generator, run by the server as a service. |
| [`DiffRhythm2`](https://github.com/privadosIA/DiffRhythm2) | C# port of [DiffRhythm2](https://github.com/ASLP-lab/DiffRhythm2): a diffusion model that writes a whole song. |
| [`lion-clap`](https://github.com/privadosIA/lion-clap) | C# port of [LAION-CLAP](https://github.com/LAION-AI/CLAP): audio and text embeddings, used to recognise songs. |
| [`musicgen-core`](https://github.com/privadosIA/musicgen-core) | The TorchSharp building blocks shared by the five ports. |
| [`triposr`](https://github.com/privadosIA/triposr) | C# port of [TripoSR](https://github.com/VAST-AI-Research/TripoSR): one picture in, one 3D model (`.glb`) out; makes the models of the *models3d* category (MIT original). |
| [`stable-fast-3d`](https://github.com/privadosIA/stable-fast-3d) | C# port of [Stable Fast 3D](https://github.com/Stability-AI/stable-fast-3d): one picture in, one textured 3D model (`.glb`) out. **Under the Stability AI Community License, not Apache-2.0** (free for research and non-commercial use, and for commercial use under US$1M of yearly revenue). |
| [`forms-lib`](https://github.com/privadosIA/forms-lib) | `@core/dynamic-forms`, the Angular library that draws forms and tables from JSON. |

The links are to the public copies of these repositories. The third-party tools, models and libraries they build on, with their licenses, are listed in [THIRD_PARTY.md](THIRD_PARTY.md).

## License

Released under the [Apache License 2.0](LICENSE). Third-party models and voices keep their own licenses (see the model catalog notes in [docs/SERVER.md](docs/SERVER.md#models-voices-and-loras)). The full list of tools and models with their licenses, and which are non-commercial, is in [THIRD_PARTY.md](THIRD_PARTY.md).

## Maintenance

Maintained by one person in their free time: issues and pull requests are welcome, with no promised response time. See [CONTRIBUTING.md](CONTRIBUTING.md) and, for vulnerabilities, [SECURITY.md](SECURITY.md).
