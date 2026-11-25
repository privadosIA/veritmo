# Veritmo

**Veritmo — ver el ritmo.** A self-hosted studio that turns a song into typographic motion graphics, and makes videos, images, music and 3D models from a script or a sentence, on your own machine.

[![Two cats in karate suits, a 30-second clip made entirely with Veritmo](docs/media/cats-karate-shots.jpg)](docs/media/cats-karate.mp4)

*A 30-second clip made entirely with Veritmo: 15 scenes of one script, [how it was made](docs/OVERVIEW.md#an-example).*

> **Live examples:** videos, songs, animation and images made with Veritmo on a local machine, with how they were made: [veritmo.xuxo.net](https://veritmo.xuxo.net/) (es/en).

## What it does

- **Music videos from a song.** Animated typography and characters that follow the words and the beat of a song you have or that it writes. See the music videos on the [examples site](https://veritmo.xuxo.net/).
- **Video from a script.** Each scene is narrated, drawn, animated (Wan, AnimateDiff, MiniMax-H3, LTX-2.3) and joined into one video; or start from a picture or a clip of your own, retouch a picture by an instruction, and edit a video you already have.
- **Music.** Songs from a style and lyrics (ACE-Step, and optionally HeartMuLa and DiffRhythm2), with a studio to repaint, separate, mix and take lyrics, tempo and key from a song you have.
- **3D scenes and models.** Scenes made in Blender from a description, a library of 3D models, and a 3D model from one picture (TripoSR, and Stable Fast 3D under its own licence).

Everything runs on your machine, with your models and your GPU: nothing leaves it unless you choose a remote provider. [More, end to end: docs/OVERVIEW.md](docs/OVERVIEW.md).

**Requirements.** Linux x86_64 and an NVIDIA GPU, with 32 GB of RAM (the server caps its own memory so a render cannot starve the machine). Which engines run with 8, 12, 16 and 24 GB of video memory: [the table](docs/INSTALL.md#what-runs-with-how-much-video-memory). Windows and macOS are not supported yet ([what might work](docs/INSTALL.md#other-platforms-windows-and-macos)).

## Quick start

With **Docker Compose** (the server, its MySQL, the migration, Prometheus and Grafana, with your projects, models and programs in folders of your machine; needs the NVIDIA Container Toolkit):

```bash
git clone https://github.com/privadosIA/veritmo.git && cd veritmo
bash scripts/compose-init.sh                              # checks Docker and the NVIDIA runtime; makes .env with generated secrets
docker compose --profile install run --rm installer      # installs the programs into your folders (once)
docker compose up -d                                      # starts everything
```

The server and the installer are published images (`ghcr.io/privadosia/veritmo-server`, `veritmo-tools`): Compose downloads them and builds them from the checkout only when it cannot. Nothing in them is a model: you download those from the dashboard, and [docs/IMAGES.md](docs/IMAGES.md) lists what they hold, with each licence. The dashboard is a separate project that connects from outside: see [docs/COMPOSE.md](docs/COMPOSE.md#the-dashboard-connects-from-outside).

Or **on the host**, without Docker:

```bash
bash scripts/install.sh --check   # what this machine has, and what is missing
bash scripts/install.sh           # install the missing pieces, asking about each one (or --yes)
./serve.sh --local                # the server on http://127.0.0.1:4666
```

New here? **[docs/TUTORIAL.md](docs/TUTORIAL.md)** explains the concepts, the same things from the terminal and a complete worked example ([also in Spanish](docs/TUTORIAL.es.md)).

## Documentation

| Topic | Document |
|---|---|
| What it is and does, end to end, with a worked example | [docs/OVERVIEW.md](docs/OVERVIEW.md) |
| Install it on a machine (requirements, what runs with how much video memory, the installer, per-distribution commands, troubleshooting) | [docs/INSTALL.md](docs/INSTALL.md) |
| Concepts, the terminal and a complete worked example | [docs/TUTORIAL.md](docs/TUTORIAL.md) ([español](docs/TUTORIAL.es.md)) |
| The command line: usage, flags, using both GPUs | [docs/CLI.md](docs/CLI.md) |
| The published images (what is in them, with each licence), the Compose stack and how a release is made | [docs/IMAGES.md](docs/IMAGES.md), [docs/COMPOSE.md](docs/COMPOSE.md), [docs/RELEASING.md](docs/RELEASING.md) |
| The nine repositories of the family and their licences; what changed in each version | [docs/REPOSITORIES.md](docs/REPOSITORIES.md), [CHANGELOG.md](CHANGELOG.md) |
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

| Repository | What it is | Licence |
|---|---|---|
| [`veritmo`](https://github.com/privadosIA/veritmo) | The server: the HTTP API and its contract, the pipelines (video, images, music, 3D), the run history and the services it starts. | Apache-2.0 |
| [`dashboards-labs`](https://github.com/privadosIA/dashboards-labs) | The dashboards (Angular) that talk to the server through its OpenAPI contract. | Apache-2.0 |
| [`forms-lib`](https://github.com/privadosIA/forms-lib) | `@core/dynamic-forms`, the Angular library that draws forms and tables from JSON. | Apache-2.0 |
| [`heartmula`](https://github.com/privadosIA/heartmula) | C# (.NET 8 + TorchSharp) port of [heartlib](https://github.com/HeartMuLa/heartlib): the HeartMuLa song generator, run by the server as a service. | Apache-2.0 |
| [`DiffRhythm2`](https://github.com/privadosIA/DiffRhythm2) | C# port of [DiffRhythm2](https://github.com/ASLP-lab/DiffRhythm2): a diffusion model that writes a whole song. | Apache-2.0 |
| [`lion-clap`](https://github.com/privadosIA/lion-clap) | C# port of [LAION-CLAP](https://github.com/LAION-AI/CLAP): audio and text embeddings, used to recognise songs. | Apache-2.0 |
| [`musicgen-core`](https://github.com/privadosIA/musicgen-core) | The TorchSharp building blocks shared by the five ports. | Apache-2.0 |
| [`triposr`](https://github.com/privadosIA/triposr) | C# port of [TripoSR](https://github.com/VAST-AI-Research/TripoSR): one picture in, one 3D model (`.glb`) out. | Apache-2.0 |
| [`stable-fast-3d`](https://github.com/privadosIA/stable-fast-3d) | C# port of [Stable Fast 3D](https://github.com/Stability-AI/stable-fast-3d): one picture in, one textured 3D model (`.glb`) out. | **Stability AI Community License — not Apache-2.0**: free for research and non-commercial use and under US$1M of yearly revenue (register with Stability AI), a commercial licence above |

The links are to the public copies of these repositories. The third-party tools, models and libraries they build on, with their licenses, are listed in [THIRD_PARTY.md](THIRD_PARTY.md).

## License

Released under the [Apache License 2.0](LICENSE). Third-party models and voices keep their own licenses (see the model catalog notes in [docs/SERVER.md](docs/SERVER.md#models-voices-and-loras)). The full list of tools and models with their licenses, and which are non-commercial, is in [THIRD_PARTY.md](THIRD_PARTY.md).

## Maintenance

Maintained by one person in their free time: issues and pull requests are welcome, with no promised response time. See [CONTRIBUTING.md](CONTRIBUTING.md) and, for vulnerabilities, [SECURITY.md](SECURITY.md).
