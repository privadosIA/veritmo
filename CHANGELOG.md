# Changelog

All notable changes to Veritmo are written here, grouped by what a person can do with it. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the versions follow [Semantic Versioning](https://semver.org/).
How a release is made, and the line a finished change adds to `[Unreleased]`, is in [docs/RELEASING.md](docs/RELEASING.md).

## [Unreleased]

## [0.1.0] - 2026-11-25

The first numbered version: the server, its API and the dashboard's contract, the pipelines (video, images, music, 3D) and the
installation as it is after the rename to Veritmo. It runs on Linux x86_64 with an NVIDIA card; see
[docs/INSTALL.md](docs/INSTALL.md) for what runs with how much memory. Third-party models keep their own licences
([THIRD_PARTY.md](THIRD_PARTY.md)).

### Music videos and motion graphics

- **Motion graphics scenes**: a model writes an animated HTML page from a scene's description and it is rendered to a clip
  (with alpha, as an overlay over a verse); the animation can be declared as data (a motion timeline), revised by describing the
  change in words, imported and exported as a dataset, and joined with the other scenes by shader transitions.
- **HyperFrames**: its 210 blocks and 238 components are kept whole (local libraries, checks, thumbnails, edit and preview, render
  to a clip, layers over a verse), with GSAP embedded; the script writer knows them and uses them.
- **Music motion**: animated typography and characters that follow a song. A song map (tempo, beats, sections of the lyrics and
  the time each word is sung), a catalogue of parts (including the era pack and the interface parts), a design and a script per
  piece with keyframes and layers, a house style with fonts, presets, and a free-text prompt.
- **References and drafts**: a library of references (an address or an uploaded video) analysed into shots, colours and a style
  the piece can learn from; parts written by the model from a description or a key frame, reviewed and repaired before they are
  kept; a part can be edited, replaced or deleted.
- **3D inside a piece**: a piece of the script can be a 3D scene or carry models of its own.
- **A music video from a song**: a planner that splits a song into shots by its sections, aligns the cuts to the music and
  animates the chorus.

### Video

- **From a script to a video**: `scenes.yaml` (or the dashboard) describes the scenes; each one is spoken (Piper), drawn
  (stable-diffusion.cpp), animated and joined into one video, with the finished file re-encoded once.
- **Animation engines**: Wan 2.2 (5B and A14B), AnimateDiff, MiniMax H3 (first frame, memory-mapped weights, LoRA) and LTX 2.3
  (with its own speech and sound).
- **Scenes of your own**: use a video you have (`clip`, trimmed and with its speed), start from a picture, camera motion,
  transitions of several kinds and crossfades, background music generated or from a file.
- **Checks**: sharpness and colour drift of the animated clips, a face check that crops and judges each face.
- **Easy mode and planning**: give a waiting time and the plan picks the settings the machine can hold; levels of quality and
  style; time estimates from the history of your own runs; memory-failure retries; several GPUs at once; the default GPU is the
  one with the most memory.
- **Review and keep**: a Review page per project, kept animations, the full project as one package to export and import.

### Images and retouch

- **Pictures**: an image output mode, image-to-image, model sets (Krea 2, Juggernaut, Pony and the others of the catalogue with
  their measured presets), metadata read from a starting picture, models downloaded from Civitai with your own key, and int8
  models converted for the machine.
- **Retouch**: editing models (FLUX.1 Kontext and Qwen-Image-Edit) with their own tab, the request rewritten as an English
  instruction, and code words for the usual retouches.
- **Describe a reference**: an image or a video becomes a prompt, in one scene or in several.

### Music

- **Songs**: the music output mode (ACE-Step) with its studio and gallery: styles and templates, vocals, language detection,
  favourites, extending a song, covers and ID3 tags, quality levels and an easy mode.
- **Edit what you made**: a waveform per song, repaint a part or separate the instruments in one step, a mixer with beat
  snapping, crossfades, loudness and a master.
- **From a song you have**: lyrics (Whisper), tempo, key and style from an mp3 or a video; word timing snapped to the attacks of
  the voice; recognise a song from a recording of it playing.
- **More generators**: HeartMuLa and DiffRhythm2, as optional containers or as .NET services that the Environment screen starts
  and stops, and a song library kept in the database.

### 3D

- **Blender scenes**: a scene of a video can be a Blender file or a description (a JSON of objects, lights, cameras and
  keyframes) that a writer model can produce and revise; models of your own can be placed in it, fitted by height or width.
- **A library of 3D models** in packs, with thumbnails, a `.glb` download, a 3D project mode and its datasets, and generation of
  a model from a picture with TripoSR (MIT) or Stable Fast 3D. Stable Fast 3D is under the Stability AI Community License, not
  Apache-2.0 (see [THIRD_PARTY.md](THIRD_PARTY.md)): the installer builds only TripoSR unless you accept that licence on purpose
  (`--accept-licence stability-community`, or typing `accept` when asked), and its weights also need your Hugging Face acceptance.

### Editing a video you have

- **Tools** applied to a finished video: scale, frame rate, reframe, remove the background, depth, extend, motion control, and
  the library of motions.

### Assistant and providers

- **The assistant** improves prompts, suggests motion, plans a request from a sentence (what to make, with which engine), writes
  songs, SVGs and datasets, and takes dictation from the microphone.
- **Providers**: Ollama (local or remote) and any OpenAI-compatible service, with presets for the known ones, routing per task, a
  model per request and per call, and the optional compression service for long prompts.

### Dashboard, phone and API

- **An HTTP API** with a contract (`/api/openapi.json`, shown by the dashboard's API page with Swagger UI) that covers everything above, events by SSE, a
  bearer token, configurable CORS, and Prometheus metrics; one-call endpoints to make a video or retouch a picture, with a client
  label so several dashboards share a server.
- **The dashboard** (a separate repository) in English or Spanish, with sortable and searchable tables, a Create page for the
  phone that works as an installable app, statistics by kind, run history management and monitoring settings.

### Installation and data

- **Installation**: an installer for the programs, models and services you choose, a Compose stack, and the plan of what each
  engine or model needs before it is installed.
- **Where things are kept**: the history, projects, settings, catalogues and the song library live in a database of your choice
  (SQLite, MySQL or Postgres) with versioned migrations; the media and model folders are settings; artefacts are reused by hash;
  leftovers are pruned and the free disk space is watched.
- **The project is Veritmo**: renamed from mediaOrquestator; the environment variables are `VERITMO_*` (the old names still work
  with a warning).
- **Editions**: a public edition (this repository) and a private one that differ only through a build tag; paths in a project are
  confined to its folder and the HTTP server has timeouts and size limits.

### Not in this release

- [Public access](openspec/changes/plan-public-access) (serving the dashboard to people outside your network) is planned, not
  built.
