# Scene import and background music

[← Back to the README](../README.md)

## Importing scene datasets

`veritmo import` converts an external scene dataset into a ready-to-fill `scenes.yaml` without running the pipeline:

```bash
bin/veritmo import dataset.html -o scenes.yaml
bin/veritmo import dataset.json -o scenes.yaml --silence-seconds 1.5
```

Accepted inputs: a JSON array of `[id, title, lora_info, full_prompt]` string tuples, or an HTML page whose `<script>` declares `const data = [...]` in that shape (only `${CONST}` interpolations of string constants from the same script are resolved — no JavaScript is evaluated; entries with unresolved placeholders are rejected individually and named in the report).

Mapping rules per entry:

- `id` → `scene.id` (duplicate ids abort the import).
- A `Dialogue:` / `Dialogue (<who>):` line → `scene.tts_text` (the quoted text); entries without one become silent scenes with `silence_seconds` from `--silence-seconds` (default 2.0).
- A `Negative:` line shared verbatim by **all** entries → `defaults.negative_prompt` once; if they differ, each scene keeps its own and defaults stays empty.
- `Camera/style:` lines are stripped and **not** parsed into `motion`/`look` (the discarded text is listed in the report); non-empty `lora_info` is discarded with a warning (LoRA is reserved, not active).
- Optional one-line settings, in any order, case-insensitive (a bad value rejects that entry, the others import):
  - `Motion: <text>` → `scene.motion_prompt` and `animate: true`. The animation models are the project's, not the dataset's: the generated file carries a commented `defaults.animate` block to fill in.
  - `Seed: <n>` → `scene.seed`; `Variants: <n>` → `scene.variants`.
  - `Adult: yes` / `Adult: no` → the project's `adult` label (any entry may carry it: `yes` wins over `no`).
  - `Speed: <x>` → `scene.speed` (a scene with dialogue); `Silence: <seconds>` → `silence_seconds` for a scene without dialogue, instead of `--silence-seconds`.
- Everything else becomes `scene.prompt`, whitespace-normalized.

The generated file passes the orchestrator's own validation; `defaults.model`, `voice`, `resolution`, `sampler`, `steps`, `fps` and `output.file` are emitted as commented placeholders for you to fill in. An existing `-o` file is never overwritten without `--force-overwrite`, and a per-entry report (mapped fields, discarded items, totals) is printed to stdout.

There are four kinds of dataset, one per kind of content, all in the same tuple shape. `--mode` chooses how the entries are read (flags go **before** the file):

| `--mode` | Each entry becomes | The text before the first marker is |
|---|---|---|
| `video` (default) | a scene (a picture, narrated or silent) | the picture prompt |
| `motion` | an animated graphic (`motion_graphic` scene) | the description of what the graphic shows |
| `music` | a song | the style |
| `models3d` | a 3D model (an entry of `models:`) | the prompt of one object alone |

**3D models** (`--mode models3d`). The result is a project of the mode `models3d` (see [MODEL_GENERATION.md](MODEL_GENERATION.md)): one entry of `models:` per tuple. The lines before the first marker are the prompt of one object alone (joined with spaces, up to 1000 characters); one-line markers, case-insensitive, set the rest: `Engine: triposr|sf3d`, `Detail: low|normal|high`, `Variants: 1..4`, `Seed: <n>`, `Image: <file of the project's sources>` (the picture of the model is that file and the entry has no prompt) and `Adult: true|false`. The tuple's id becomes the entry's id, made plain (lower case, digits, dashes, up to 32 characters) and unique with a number when it is not, and a warning says so; an empty id is made from the title. An entry is rejected, naming the line, for an `Engine:` or `Detail:` that is not valid, a `Variants:` outside 1 to 4, a `Seed:` that is not a number, no prompt and no `Image:`, or both; a line that is none of the markers after the first one is ignored with a warning. A dataset cannot carry the pictures an `Image:` names: the report warns, and the confirmation refuses the project until they are in its sources. The image model that draws the pictures of the prompts (`defaults.model`...) is a commented placeholder, as in the other modes. A server that hides adult content refuses a dataset with `Adult: true` (`422` `content_not_allowed`). `veritmo export` of such a project writes the same format back: the engine and the detail of `defaults.models3d` are folded into each entry and written only when they are not `triposr` and `normal`, and the notes say what is not carried (the image model and its settings, the library and the models themselves).

**Animated graphics** (`--mode motion`). The result is a video project whose scenes are all animated pages. One-line markers, case-insensitive: `Duration: <seconds>` (the length of a silent graphic; with `Dialogue:` the narration sets it and `Duration:` is reported as discarded), `Dialogue:` and `Speed:` as in scenes, `Graphic: <text>` (the description, which wins over the text before the markers) and a title over the graphic with `Overlay: <text>`, `Overlay position: top|center|bottom`, `Overlay start: <s>` and `Overlay end: <s>`. Importing needs neither Chrome nor a language model: the model writes each page, and Chrome draws it, when the project runs.

**Graphics made from parts of the catalog.** In an animated-graphics dataset an entry can name a part of the catalog of animated parts (the ones the music motion pieces use: `GET /api/music-motion/catalog` lists them with their parameters) instead of describing a graphic for the model to draw. Markers: `Part: <name>` (or `Part: auto`, and the model chooses a part from the description in the text), `Param <name>: <value>` (repeatable; the value is read as the type the part declares: a whole number, a number, `true`/`false` or text), `Text: <line>` (shown for the whole scene), `BPM: <40 to 240>` (the tempo of the scene's clock) and `Design: <words of style>` (the first entry that gives it sets the palette of the whole project: the words go to `defaults.subject_prompt`, from which a fixed palette is chosen). An entry with a named part needs no description; one with a part that is not in the catalog, a parameter the part does not have or one outside its limits, parameters with no `Part:` or with `Part: auto`, or a tempo out of range, is rejected with the reason and the rest are imported. The export writes the same markers back. In a scenes dataset these lines stay part of the text. Nothing is drawn at import: the page is built from the part, with no model, when the project runs (see `docs/SCENES_YAML.md`). A complete example is `docs/examples/graphics-parts.json`.

The scene dataset also understands `Overlay:` (and its position, start and end) on any scene, and `Graphic:` to mark one scene as a graphic, so a project that mixes pictures and graphics fits in one dataset. A line `Duration:` in a scene dataset is not a marker: it stays part of the text.

**3D scenes.** In a scene dataset a line `Blender: <name>.scene.json` makes the scene a 3D scene rendered by Blender (`blender: {build: <name>}`): a plain file name of a description in the project's `sources/`, one line per entry. The text of the entry stays as the scene's prompt (Blender ignores it) and the entry may have no other text; `Motion:` is reported as discarded, `Dialogue:`, `Silence:`, `Speed:` and `Overlay:` work as in any scene. In the animated-graphics mode the line makes a 3D graphic too: its length is the entry's `Duration:` and `Part:`, `Graphic:`, `Param`, `Text`, `BPM`, `Design` and the text before the markers are discarded with a report line (an entry with only the line is valid); `descriptions` and `warnings` of the preview work as in the video mode, and `assist/dataset` with `picture: "blender"` and `mode: "motion"` writes the entries (a line that says what is built and a `Duration:`). The dataset carries the line and not the file: the preview of the API takes the descriptions in `descriptions` (`{"<name>.scene.json": <description>}`), the confirmation writes them to `sources/` and refuses the project (`422`, nothing created) when a scene names a file that is not there or one that does not pass the checks of a hand-written description. `POST /api/assist/dataset` with `"picture": "blender"` writes the lines and the descriptions together (at most 6 scenes per call).

**Songs** (`--mode music`): see `docs/MUSIC_MODE.md` for every marker.

## Worked examples

The three sample datasets are in `docs/examples/`. Each one is imported with one command; the report says what each entry became, what was discarded and what was rejected, and the file written is a normal `scenes.yaml` to open in the dashboard.

### Scenes: `docs/examples/scenes.json`

Three scenes of a fishing village: a narrated one, a silent one that moves, and a narrated one with a slower voice and a fixed seed.

```bash
bin/veritmo import --mode video -o farol.yaml docs/examples/scenes.json
```

```text
[farol]
  mapped: tts_text ← dialogue "Cada mañana, el faro despierta antes que el pueblo."
  mapped: prompt ← 64 chars
  mapped: negative ← defaults.negative_prompt (shared by all entries)
[barca]
  mapped: motion_prompt ← "the boat rocks gently and the water ripples" (animate ← true)
  mapped: silence_seconds ← 3
  ...
imported 3, silent 1, rejected 0
```

The `Negative:` line is the same in the three entries, so it went once to `defaults.negative_prompt`; `Motion:` made `barca` an animated scene (the file carries a commented `defaults.animate` block to fill in with the project's animation models); `Silence: 3` is its length, because it has no narration. The scenes in `farol.yaml`:

```yaml
scenes:
  - id: farol
    prompt: A lighthouse on a rocky coast at dawn, soft pink light, calm sea
    tts_text: Cada mañana, el faro despierta antes que el pueblo.
    speed: 1.0
  - id: barca
    prompt: A small wooden fishing boat leaving the harbour, gulls overhead
    silence_seconds: 3
    animate: true
    motion_prompt: the boat rocks gently and the water ripples
  - id: cena
    prompt: A family eating together at a long table, warm lamp light
    seed: 7
    tts_text: Por la noche, todos vuelven a casa.
    speed: 0.9
```

Fill in the commented `model`, `voice` and `output.file`, or create the project from the dashboard (**Projects > Import dataset**), which fills in what this machine has.

### Animated graphics: `docs/examples/graphics.json`

The opening of a documentary: a title card with a credit that appears over it, a bar chart (with its numbers) and the closing credits. Nothing is drawn yet: each entry only says what the graphic shows and how long it lasts.

```bash
bin/veritmo import --mode motion -o graficos.yaml docs/examples/graphics.json
```

```text
[titulo]
  mapped: overlay.text ← "Un documental de Ana Ruiz"
  mapped: overlay.position ← bottom
  mapped: overlay.start ← 1.5
  mapped: overlay.end ← 4
  mapped: motion_graphic ← 90 chars
  mapped: silence_seconds ← 4 (Silence line)
...
imported 3 graphics, silent 3, rejected 0
```

```yaml
scenes:
  - id: titulo
    motion_graphic: 'Opening title card: the words "Un año en el puerto" fade in over slowly moving blue waves'
    silence_seconds: 4
    overlay:
      text: Un documental de Ana Ruiz
      position: bottom
      start: 1.5
      end: 4
  - id: grafico
    motion_graphic: 'A bar chart of fish caught in three months: January 12, February 30 and March 48; ...'
    silence_seconds: 6
  - id: creditos
    motion_graphic: 'Rolling credits on a dark background: Direction, Camera, Music, Thanks'
    silence_seconds: 5
```

`creditos` shows the other spelling: a `Graphic:` line instead of plain text. The result is a video project; the first run asks the language model for a page per graphic (and for the title's page), checks it, stores it in `motion/` and renders it with Chrome, so it needs both; later runs reuse the stored pages (see `docs/SCENES_YAML.md`).

### Songs: `docs/examples/songs.json`

A ballad with its lyrics and metadata and an instrumental ambient piece with two variants.

```bash
bin/veritmo import --mode music -o canciones.yaml docs/examples/songs.json
```

```text
[balada]
  mapped: prompt ← 57 chars
  mapped: vocal_language ← es
  mapped: bpm ← 76
  mapped: duration_seconds ← 90
  mapped: keyscale ← C major
  mapped: seed ← 7
  mapped: lyrics ← 5 lines
[ambiente]
  ...
  mapped: variants ← 2
  mapped: instrumental ← true
imported 2, instrumental 1, rejected 0
```

The lines before `Lyrics:` are the style; the lyrics keep their line breaks up to the next marker; `Instrumental` leaves the song without voice. A `Tags:` line (words separated by commas) gives the tags the optional HeartMuLa generator reads. The project opens in the music studio.

### 3D models: a dataset

```
[["patin", "Patín", "", "a retro roller skate, glossy pink leather with white wheels"],
 ["casco", "Casco", "", "a matte black derby helmet with a chrome star\nDetail: high\nVariants: 2"],
 ["gato",  "Gato",  "", "Image: gato.png"]]
```

`bin/veritmo import --mode models3d -o objetos.yaml objetos.json` writes the project (and a report like the other modes: what each line set, what was changed or ignored). The assistant writes such a dataset from a sentence: `POST /api/assist/dataset` with `mode: "models3d"` (see [ASSISTANT.md](ASSISTANT.md)).

### The same three in the dashboard

**Projects > Import dataset**, choose the type (**Video scenes**, **Animated graphics** or **Songs**), upload the file and read the report before confirming; nothing is created until you give it a name and confirm. A graphics or scenes project opens the scene editor, a songs project the music studio. The reverse trip (**Export > Scenes only**) writes the same format, graphics and titles included; the pages the model wrote are not carried (the full project keeps them).



## Export: take a project to another installation

`veritmo export` writes a project so it can be taken elsewhere, in two ways:

```bash
bin/veritmo export -format zip -lang en -o demo.zip projects/demo/scenes.yaml   # the full project (flags go before the file)
bin/veritmo export -o demo.dataset.json projects/demo/scenes.yaml               # only the scenes, as a dataset
```

The same two downloads are in the dashboard (**Projects**, the download button of each row: "Full project" and "Scenes only") and in the API (`GET /api/projects/{id}/export?format=zip|dataset&lang=es|en`).

- **Full project** (`zip`): `scenes.yaml` (every setting of the project: models, resolution, steps, sampler, voice, animation engine and LoRA, camera, look, mixes...), the files of `sources/`, a README and **the recipe**. The recipe is `RECIPE.md` (to read, in the language of `lang`) and `recipe.json` (for the program): the settings the project generates with, the catalog's **recommended settings** of its models and whether the project follows them (or what it changed, e.g. `steps 8 → 6`), and **every file it uses** (image model, text encoder, VAE, voice, animation files, LoRAs, music models) with its size, the catalog entry and the URL it is downloaded from. The models, voices and what the project generated (`out/`, `tmp/`) are not in the package: they belong to the machine and are tens of GB.
- **Dataset** (default of the API and the CLI). Video and images projects write one tuple per scene with its text, `Dialogue:`, `Negative:`, `Motion:`, `Seed:`, `Speed:`, `Silence:` and `Variants:` (an animated graphic is written as `Graphic: <description>`, a 3D scene as `Blender: <name>.scene.json` (a scene rendered from a `.blend` file is left out, with a note) and a title over any scene as `Overlay:` lines; the pages the model wrote for them and the description files of the 3D scenes are not carried: the zip package has them in `sources/`); music projects write one per song in the music format (`Lyrics:`, `Language:`, `BPM:`, `Duration:`, `Key:`, `Time signature:`, `Seed:`, `Variants:`, `Tags:`). The subject prompt, the style prefix and the voice are folded into each entry. It carries **content, not setup**: what it cannot carry (the models, the voice, the resolution and steps, the animation models, camera and look presets, per-scene render/animation settings, scenes with their own pictures, mixes, favorites...) is listed after exporting (in the terminal, in the dashboard message and in the `X-Export-Notes` header of the API). Use the full project when the other side must generate the same thing.

### Import a full project

In the dashboard, **Projects > Import full project** (the API is `POST /api/import/package/preview` and `/confirm`, the body is the `.zip`). It never creates anything until you confirm, and it tells you, before:

- what settings the project generates with and whether they are the catalog's recommended ones;
- **every file it uses, whether this machine has it**, and the missing ones grouped by catalog bundle with a button that installs them (the same download as in Models, with its progress); the files the catalog does not know are listed by name;
- what the project's own validation says once its `sources/` are in place (a picture or clip the package does not carry, an invalid value): `problems`; `ready` is true only when nothing is missing and nothing has to be fixed.

Confirming writes `scenes.yaml` and `sources/` (nothing else of the zip is read: no other paths, no nested folders) as a new project; an existing id is never overwritten. Missing models do not prevent creating it: install them whenever you want, and when `ready` is true all that is left is to launch the generation. A package from an older version (without `recipe.json`) works too: the report is built from its `scenes.yaml`.

## Background music

A video project can carry a soundtrack under its narration with a `bgm` block: a `prompt` (an instrumental track is generated with the music engine and cached) or a `file` from `sources/`, plus `volume`, `fade_seconds` and `ducking`. It is mixed after the clips are joined (music lowered while someone speaks, faded, normalized with `loudnorm`; the video is copied, not re-encoded). See `docs/MUSIC_MODE.md`.
