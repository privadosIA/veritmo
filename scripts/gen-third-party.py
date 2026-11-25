#!/usr/bin/env python3
"""Writes THIRD_PARTY.md: the models table comes from internal/server/catalog.json (the license each entry declares),
the tools section is written by hand below. Run: python3 scripts/gen-third-party.py"""
import json, pathlib

root = pathlib.Path(__file__).resolve().parent.parent
entries = json.load(open(root / "internal/server/catalog.json"))
LABEL = {"yes": "Yes", "conditions": "With conditions", "no": "**No**", "unknown": "Unconfirmed"}

head = """# Third-party software and models

Veritmo is released under the [Apache License 2.0](LICENSE). That covers this repository's own code only.
It does not bundle any model: models are downloaded on demand, from the Models tab or the `scripts/get-*.sh`
scripts, **from their authors, under their authors' licenses**. Check each license before using the output
commercially. This file is a guide, not legal advice, and the license text of each project is the one that counts.

## Tools the orchestrator runs

They are downloaded or built by the `scripts/` installers (none is copied into this repository).
The licenses below are what each project declares in its repository (checked 2026-10-24 for stable-diffusion.cpp, acestep.cpp, whisper.cpp, 2026-11-14 for demucs.cpp, Piper, espeak-ng, ONNX Runtime and yt-dlp, and 2026-11-07 for Real-ESRGAN and RIFE; FFmpeg, Ollama, Chrome and SkiaSharp are listed from memory); confirm them in the project itself.

| Tool | Used for | License |
|---|---|---|
| [stable-diffusion.cpp](https://github.com/leejet/stable-diffusion.cpp) (`sd`) | images, video, retouch | MIT |
| [acestep.cpp](https://github.com/ServeurpersoCom/acestep.cpp) | songs with ACE-Step | MIT |
| [whisper.cpp](https://github.com/ggml-org/whisper.cpp) | transcription | MIT |
| [demucs.cpp](https://github.com/sevagh/demucs.cpp) (Sevag H; built from the commit `4edae49`) | separating the voice of a song to time its words (optional) | MIT |
| [Piper](https://github.com/rhasspy/piper) + espeak-ng + ONNX Runtime | narration voices | Piper MIT (the repository is archived); ONNX Runtime MIT; **espeak-ng GPL-3.0** |
| [yt-dlp](https://github.com/yt-dlp/yt-dlp) | sources from YouTube links | Unlicense |
| [FFmpeg](https://ffmpeg.org) | audio and video handling (system install) | LGPL-2.1+ or GPL, depending on the build |
| [Real-ESRGAN ncnn-vulkan](https://github.com/xinntao/Real-ESRGAN-ncnn-vulkan) with the model `realesr-animevideov3` | enlarging a video (`scale`, quality engine) | the program MIT; the Real-ESRGAN models BSD-3-Clause |
| [RIFE ncnn-vulkan](https://github.com/nihui/rife-ncnn-vulkan) with the network `rife-v4.6` | the frames in between (`interpolate`) | MIT (the program and the RIFE networks, by hzwer) |
| The video-vision service (a .NET service of ours, in its own repository) with ONNX Runtime and SkiaSharp | person mask and depth of the video-edit tools | ONNX Runtime MIT; SkiaSharp MIT |
| [Ollama](https://ollama.com) | local assistant and vision (optional) | MIT |
| Chrome / Chromium | motion graphics, rendered headless (your system's, or Chrome for Testing that `scripts/get-chrome.sh` downloads from Google onto your machine) | Chromium: BSD-3-Clause and the licences of what it bundles. **Chrome for Testing is Google software (Chrome Terms of Service, `chrome://terms`) with the proprietary Widevine CDM: no image or archive of this project carries it** |
| [HyperFrames](https://github.com/heygen-com/hyperframes) (HeyGen; blocks imported with `veritmo parts import-hyperframes`) | the parts `hf-<block>` converted from its blocks, each with an `origin.json` naming the block (the small pictures of a block's `assets/` are part of its Apache-2.0 files and travel inside the part); and the craft rules of the planner, script, scene and review prompts, of `docs/MUSIC_MODE_MOTION.md` § Craft and of `.claude/skills/music-video/`, distilled (not copied) from its skill references `story.md`, `motion.md`, `captions.md`, `review.md`, `film.md` and `types/music.md` at commit `3a4f57252377051f6602bd0149e84f8ead0845b2` | Apache-2.0 |
| [GSAP](https://gsap.com) core 3.13.0 (GreenSock, now Webflow; `scripts/get-gsap.sh`, optional) | the timelines of the parts and scenes that animate with GSAP, drawn inline in the page the server builds | GSAP Standard "No Charge" License (not an open-source licence; see below) |
| The libraries the HyperFrames blocks load from a CDN (`scripts/get-hyperframes-libs.sh`, optional): [three](https://threejs.org) and its loaders, [d3](https://d3js.org) and d3-delaunay, topojson-client, [lottie-web](https://github.com/airbnb/lottie-web), [clipper-lib](https://sourceforge.net/projects/jsclipper/), the Draco decoder, map data (us-atlas, world-atlas, es-atlas), [Inter](https://rsms.me/inter/) and GSAP 3.14.2 | drawn locally inside the pages of the blocks, so that no block asks the network for anything | MIT (three, lottie-web), ISC (d3, topojson-client, the atlases), Boost (clipper-lib), Apache-2.0 (Draco), SIL OFL 1.1 (Inter), GSAP as below; read from each package's `license` field on 2026-11-17, not legal advice |
| The runtime of [HyperFrames](https://github.com/heygen-com/hyperframes) (`scripts/get-hyperframes-runtime.sh`, optional), built here from its source at a pinned commit with esbuild, postcss, acorn, acorn-walk and @chenglou/pretext | drawing the components and the blocks that mount sub-compositions | Apache-2.0 (HyperFrames); MIT (esbuild, postcss, acorn, acorn-walk, pretext; their code is bundled into the runtime; read from each package's `license` field on 2026-11-17, not legal advice) |
| The shader transitions of [HyperFrames](https://github.com/heygen-com/hyperframes) (`@hyperframes/shader-transitions`, the fragment shaders kept in `internal/video/shadertrans/shaders.json`, extracted by `scripts/extract-shader-transitions.cjs`) | `transition: shader:<name>` between scenes, drawn by Chrome | Apache-2.0 |

**GSAP.** `scripts/get-gsap.sh` installs `gsap.min.js` (version 3.13.0, SHA-256 checked, `third_party/`, never committed) and the server writes it inline into the pages of the parts and scenes that use a timeline. Read in the licence of that version (<https://gsap.com/standard-license>, checked 2026-11-15; the file's own header says "Subject to the terms at https://gsap.com/standard-license"): the grant is "a non-exclusive, worldwide license to use, reproduce, display, and implement GSAP Products solely for Permitted Uses", and a Permitted Use is "the implementation and/or use of GSAP Products on any website, web application, or digital interface by any person or entity"; so drawing it inside the pages the server renders is covered. The summary on that page adds that it is free "as long as end users are not charged a fee of any kind to use your product". What it forbids: "tools that allow users to build visual animations without code that encourages, induces, or materially assists in creating a solution that competes with Webflow's visual animation building capabilities", reverse engineering it to make one, and removing its notices (the pages keep the header of the file). Our models and our users write the timelines as code and nothing here is a visual animation builder; if a screen ever lets people build GSAP animations without code, or the product is sold, read that licence again first. This is a reading, not legal advice.

## 3D model engines (optional)

They make a 3D model from one picture (the *models3d* mode). Their code and weights are downloaded by the installer or the Models 3D screen onto your machine, never part of an image or of this repository.

| Engine | Where | License | Commercial use |
|---|---|---|---|
| TripoSR (`triposr`, as a .NET service or in the `model3d` container) | [VAST-AI-Research/TripoSR](https://github.com/VAST-AI-Research/TripoSR); weights `stabilityai/TripoSR` | MIT (Tripo AI and Stability AI) | Yes |
| **Stable Fast 3D** (`sf3d`) | [Stability-AI/stable-fast-3d](https://github.com/Stability-AI/stable-fast-3d); weights `stabilityai/stable-fast-3d` (gated on Hugging Face) | **Stability AI Community License**, for the code, its port to .NET (`stable-fast-3d`) and the weights: [text](https://stability.ai/community-license-agreement). **Not Apache-2.0** | With conditions: free under US$1M of yearly revenue, registering with Stability AI; above that, a licence from Stability AI. Attribution ("Powered by Stability AI") is asked |

Stable Fast 3D is never built or installed on the strength of a click on "install" alone: `scripts/get-services.sh`, the `model3d` container build and the server's install each ask for the explicit acceptance `stability-community`, and the server records it. Its weights also need your own Hugging Face account to have accepted the licence there.

## Models

Installable from the catalog (`internal/server/catalog.json`; this table is generated from it by
`scripts/gen-third-party.py`). **Commercial use** says what the model's license allows:
*Yes* = allowed, *With conditions* = allowed under terms you must read, **No** = non-commercial only,
*Unconfirmed* = the license could not be verified, so treat it as not allowed until you check.

| Model | Kind | License | Commercial use | Note |
|---|---|---|---|---|
"""
ES = {"condición de": "condition from", "modificada": "modified", "sin confirmar": "unconfirmed", "Sin identificar": "Not identified", "Dominio público": "Public domain", "pesos": "weights", "movimiento": "motion"}
def en(t):
    for a, b in ES.items():
        t = t.replace(a, b)
    return t

rows = []
for e in entries:
    note = (e.get("commercial_note_en") or "").replace("\n", " ").replace("|", "/")
    rows.append(f"| {e.get('name_en') or e['name']} | {e['kind']} | {en(e['license'])} | {LABEL.get(e['commercial'], e['commercial'])} | {note} |")

tail = """
## Models you add yourself

Anything you install from another source (for example a checkpoint from Civitai or Hugging Face) keeps its
own license; the orchestrator does not check it. Some checkpoints forbid selling the images or running them as a
paid service, and some impose content rules.

## Prompt datasets (only when you run `veritmo prompts fetch`)

The command reads the licence from each dataset's card on every run and uses a source only when it is `cc0-1.0`,
`cc-by-4.0`, `apache-2.0` or `mit`; each preset keeps its source and licence. Built in: **DiffusionDB**
(`poloclub/diffusiondb`, CC0-1.0, text only) and **Rapidata/awesome-text2video-prompts** (Apache-2.0). The music source
`Kukedlc/suno-ai-music-dataset` (card: CC-BY-4.0) is **off**, not because of its licence but because of where the prompts come from (they were written by Suno's users, and nothing can check that the uploader could license them): use it only for
strictly local work and never in a library that is shared. See `docs/PROMPT_PRESETS.md`.

## Fonts embedded in the pages

The pages of a music-motion piece embed six free fonts so that the render and the browser preview draw the same letters:
**Inter**, **Space Grotesk**, **JetBrains Mono**, **Playfair Display**, **Bebas Neue** and **Fraunces**, all under the
SIL Open Font License 1.1 (latin subsets taken from the [Fontsource](https://fontsource.org) packages; the licence file of each
one is in `internal/music/musicmotion/fonts/`).

## Weights that come with another model

- **DiffRhythm2** downloads the MuQ-MuLan and MuQ weights (CC-BY-NC 4.0, **non-commercial**). Songs made with
  it must not be used commercially. The same MuQ family is used by the optional song-recognition study in
  `docs/SONG_RECOGNITION_STUDY.md`.
- **FLUX.1 Kontext** (the *Fast* retouch engine) is non-commercial. The *Precise* engine (Qwen-Image-Edit) is Apache-2.0.
"""
(root / "THIRD_PARTY.md").write_text(head + "\n".join(rows) + "\n" + tail)
