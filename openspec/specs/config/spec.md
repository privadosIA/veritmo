# config Specification

## Purpose
Defines the `scenes.yaml` schema, how it is loaded into Go types, and the pre-flight validation that must pass before any subprocess runs.

## Requirements

### Requirement: Load scenes.yaml into a typed Config
`Load` SHALL parse a YAML file at a given path into a `Config`, and SHALL return a descriptive error without parsing further when the file cannot be read.

#### Scenario: Valid YAML parses into Config
- **WHEN** `scenes.yaml` is well-formed YAML matching the schema
- **THEN** `Load` returns a `Config` whose `Project`, `OutputFile`, `Defaults`, and `Scenes` reflect the file's content

#### Scenario: Missing file returns a read error
- **WHEN** the path passed to `Load` does not exist
- **THEN** `Load` returns an error whose message mentions the read failure

### Requirement: Seed distinguishes "explicit zero" from "absent"
A scene's `seed` field SHALL be represented so that an explicit `0` is distinguishable from an omitted field, and an omitted field SHALL resolve to the render phase's "pick a random seed" sentinel (`-1`).

#### Scenario: Explicit seed 0 is preserved
- **WHEN** a scene's YAML sets `seed: 0`
- **THEN** the loaded scene's seed is non-nil and equal to 0, and `ResolvedSeed()` returns `0`

#### Scenario: Absent seed resolves to the unset sentinel
- **WHEN** a scene's YAML omits the `seed` field entirely
- **THEN** the loaded scene's seed is nil, and `ResolvedSeed()` returns `-1`

### Requirement: Validate rejects an incomplete or inconsistent config before any subprocess runs

`config.Validate` SHALL reject any configuration that is incomplete or internally inconsistent before the orchestrator launches a single subprocess. The set of invariants enforced is **mode-dependent**:

**Video mode** (`output.mode` absent or `"video"`):

- Every `Scene` SHALL have a non-empty `tts_text` **unless the scene declares `silence_seconds`** (a silent scene; see the ADDED requirement below). A scene with neither a non-empty `tts_text` nor `silence_seconds` is a validation error naming the scene ID.
- Every `Scene` SHALL have a non-empty `prompt` (existing behaviour, unchanged).
- A resolved output file path SHALL be present: either `output.file` or the deprecated top-level `output_file` (or both, if identical). If neither is set, `Validate` SHALL return an error.
- If both `output_file` and `output.file` are set to **different** values, `Validate` SHALL return an error identifying both values.
- `Scene.variants`, if present, SHALL be ≥ 1 (a value of 0 or negative is a validation error regardless of mode).

**Images mode** (`output.mode` == `"images"`):

- Every `Scene` SHALL have a non-empty `prompt`.
- `Scene.tts_text`, `Scene.speed`, and any audio/montage fields, if present, are **optional**; when present they are ignored and a deprecation-style warning SHALL be emitted on stderr naming the field and scene ID. `Scene.silence_seconds`, `Scene.motion`, and `Scene.look` count as audio/montage fields for this rule: they only affect the clip phase, which images mode never runs.
- `Scene.variants`, if present, SHALL be ≥ 1.
- `output.file` is **not required** and, if set, is ignored (the export directory is derived from the project path).
- If both `output_file` and `output.file` are set to **different** values, `Validate` SHALL return an error (same rule as video mode).
- `output.contact_sheet`, if present, SHALL be a boolean; any other type is a validation error.

**Both modes** — structural checks on the new fields (enforced regardless of mode, so a config is either valid everywhere or rejected everywhere):

- `motion.type`, if present, SHALL be one of `static`, `zoom_in`, `zoom_out`, `pan_left`, `pan_right`, `handheld`; any other value is an error listing the accepted values.
- `motion.intensity`, if present, SHALL be within [0.0, 1.0].
- `look.grain`, if present, SHALL be within [0.0, 1.0].
- `look.white_balance`, if present, SHALL be one of `neutral`, `warm`, `cool`, `fluorescent`.
- `silence_seconds`, if present, SHALL be > 0.

In both modes, `Validate` SHALL return a single aggregated error (or `nil`) rather than panicking, and SHALL NOT invoke any external binary.

#### Scenario: Missing output_file is rejected

- **WHEN** the effective mode is `video` and neither `output.file` nor the deprecated `output_file` is set
- **THEN** `Validate` returns an error mentioning the output path
- **AND** the output path is not required to already exist on disk
- **AND** in `images` mode the absence of both fields is NOT an error

#### Scenario: Missing input file is rejected

- **WHEN** `defaults.model` or `defaults.voice` names a path that does not exist on disk
- **THEN** `Validate` returns an error naming the missing field and path (in either mode)

#### Scenario: Duplicate scene IDs are rejected

- **WHEN** two scenes share the same `id`
- **THEN** `Validate` returns an error naming the duplicate id and the index of each occurrence (in either mode)

#### Scenario: Empty prompt or tts_text is rejected

- **WHEN** the effective mode is `video` and any scene has an empty `prompt`, or an empty `tts_text` without `silence_seconds`
- **THEN** `Validate` returns an error naming the offending scene's id
- **AND** in `images` mode only an empty `prompt` is an error; an empty `tts_text` is accepted

#### Scenario: Video mode with missing tts_text is rejected

- **WHEN** `output.mode` is absent (defaults to `"video"`) and a scene has an empty `tts_text` and no `silence_seconds`
- **THEN** `Validate` SHALL return an error identifying the scene ID and the missing `tts_text` field

#### Scenario: Video mode with no output file is rejected

- **WHEN** `output.mode` is `"video"`, neither `output.file` nor top-level `output_file` is set
- **THEN** `Validate` SHALL return an error stating that a video project must declare an output path

#### Scenario: Images mode with empty tts_text is accepted

- **WHEN** `output.mode` is `"images"` and a scene has an empty `tts_text`
- **THEN** `Validate` SHALL NOT reject the config on the basis of `tts_text`; if the field is explicitly present and non-empty, a warning SHALL be emitted on stderr

#### Scenario: Images mode with variants zero is rejected

- **WHEN** `output.mode` is `"images"` and a scene has `variants: 0`
- **THEN** `Validate` SHALL return an error stating that `variants` must be ≥ 1

#### Scenario: Alias conflict is rejected in either mode

- **WHEN** `output_file` is `"a.mp4"` and `output.file` is `"b.mp4"` (regardless of `output.mode`)
- **THEN** `Validate` SHALL return an error naming both values and instructing the user to set only one

#### Scenario: Silent scene passes video-mode validation without tts_text

- **WHEN** `output.mode` is `"video"` and a scene has an empty `tts_text` but `silence_seconds: 2.0`
- **THEN** `Validate` SHALL accept the scene

#### Scenario: Invalid motion type is rejected in either mode

- **WHEN** a scene declares `motion: {type: dolly}` (in video or images mode)
- **THEN** `Validate` SHALL return an error listing the accepted motion types

#### Scenario: Motion and look in images mode warn and are ignored

- **WHEN** `output.mode` is `"images"` and a scene declares `motion` or `look`
- **THEN** `Validate` SHALL accept the config and emit a warning on stderr naming the field and scene ID

### Requirement: output object selects the pipeline mode

`config.Config` SHALL accept an `output` object with the following fields:

| Field | Type | Required | Notes |
|---|---|---|---|
| `mode` | string | No | `"video"` (default), `"images"` or `"music"` |
| `file` | string | Conditional | Required when `mode == "video"`; ignored when `mode == "images"` or `"music"` |
| `contact_sheet` | bool | No | `images` mode only; default `false` |

The top-level `output_file` string field SHALL be retained as a **deprecated alias** for `output.file`. Resolution rules:

- If `output.mode` is absent, it SHALL default to `"video"`.
- If `output.mode` is any value other than `"video"`, `"images"` or `"music"`, `Validate` SHALL return an error listing the accepted values.
- If only `output_file` is set (and `output.file` is absent), the value SHALL be mapped to `output.file` and a deprecation warning SHALL be emitted on stderr.
- If both are set and identical, the config SHALL be accepted and a deprecation warning SHALL be emitted on stderr.
- If both are set and different, `Validate` SHALL reject (see MODIFIED requirement above).

#### Scenario: Absent output.mode defaults to video

- **WHEN** the `output` object is present with `file: "out/final.mp4"` but no `mode` key
- **THEN** the resolved `Config.Output.Mode` SHALL be `"video"` and validation SHALL proceed as in video mode

#### Scenario: Invalid mode value is rejected

- **WHEN** `output.mode` is `"audio"`
- **THEN** `Validate` SHALL return an error stating that `output.mode` must be `"video"`, `"images"` or `"music"`

#### Scenario: Music mode needs no output file

- **WHEN** `output.mode` is `"music"` and `output.file` is absent
- **THEN** `Validate` SHALL NOT report a missing output file

#### Scenario: Alias-only configuration is accepted with warning

- **WHEN** top-level `output_file` is `"out/final.mp4"`, `output.file` is absent, and `output.mode` is `"video"`
- **THEN** the resolved `Config.Output.File` SHALL be `"out/final.mp4"` and a deprecation warning referencing `output_file` SHALL be written to stderr

#### Scenario: Alias and output.file set to the same value

- **WHEN** `output_file` is `"out/final.mp4"` and `output.file` is `"out/final.mp4"`
- **THEN** the config SHALL be accepted and a deprecation warning SHALL be written to stderr

### Requirement: variants controls images-per-scene in images mode

`config.Scene` SHALL accept an optional `variants` field of type `int` with the following constraints:

- The value SHALL be ≥ 1. A value of 0 or any negative integer SHALL cause `Validate` to return an error.
- When absent, the effective value SHALL be 1 (single render per scene).
- In **images mode**, `variants` determines the number of sequential `sd` invocations for that scene (variant index 0 through `variants-1`).
- In **video mode**, a non-default `variants` value (i.e. any value ≠ 1) SHALL be ignored for pipeline purposes and a warning SHALL be emitted on stderr stating that `variants` has no effect in video mode.

#### Scenario: Default variants is one

- **WHEN** a scene in images mode has no `variants` field
- **THEN** the effective variant count SHALL be 1 and the orchestrator SHALL invoke `sd` exactly once for that scene

#### Scenario: Variants of zero is rejected

- **WHEN** a scene has `variants: 0` (in either mode)
- **THEN** `Validate` SHALL return an error stating that `variants` must be a positive integer ≥ 1

#### Scenario: Variants in video mode triggers a warning

- **WHEN** `output.mode` is `"video"` and a scene has `variants: 3`
- **THEN** the config SHALL be accepted, the effective variant count SHALL remain 1 for pipeline purposes, and a warning SHALL be emitted on stderr naming the scene ID

### Requirement: defaults gain subject_prompt and negative_prompt

`config.Defaults` SHALL accept two new optional string fields:

| Field | Type | Default | Effect |
|---|---|---|---|
| `subject_prompt` | string | `""` | Prepended to every scene's `prompt` at render composition (i.e. the positive prompt passed to `sd` becomes `defaults.subject_prompt + " " + scene.prompt` when non-empty) |
| `negative_prompt` | string | `""` | Concatenated after the scene's own `negative_prompt` (i.e. the final negative prompt becomes `scene.negative_prompt + " " + defaults.negative_prompt` when both are non-empty) |

Both fields SHALL default to the empty string. When both are empty, the composed positive and negative prompts passed to `sd` SHALL be byte-identical to the values produced by the current `add-script-to-video-pipeline` implementation (i.e. `scene.prompt` and `scene.negative_prompt` respectively).

These fields are included in the `ImageHash` cache key (see the `cache` capability delta) so that changing a global default invalidates cached renders.

#### Scenario: Both defaults absent preserves current behaviour

- **WHEN** `defaults.subject_prompt` and `defaults.negative_prompt` are both absent (or empty strings) and a scene has `prompt: "A cat"` and `negative_prompt: "blurry"`
- **THEN** the composed positive prompt passed to `sd` SHALL be `"A cat"` and the composed negative prompt SHALL be `"blurry"`, identical to the current pipeline output

#### Scenario: subject_prompt prepends to scene prompt

- **WHEN** `defaults.subject_prompt` is `"A cinematic wide shot of"` and a scene has `prompt: "a red fox in snow"`
- **THEN** the composed positive prompt passed to `sd` SHALL be `"A cinematic wide shot of a red fox in snow"`

#### Scenario: negative_prompt concatenates after scene negative

- **WHEN** `defaults.negative_prompt` is `"low quality, watermark"` and a scene has `negative_prompt: "blurry"`
- **THEN** the composed negative prompt passed to `sd` SHALL be `"blurry low quality, watermark"`

### Requirement: motion declares a per-scene camera movement preset

`config.Scene` SHALL accept an optional `motion` object:

| Field | Type | Default | Constraint |
|---|---|---|---|
| `type` | string | `static` | One of `static`, `zoom_in`, `zoom_out`, `pan_left`, `pan_right`, `handheld` |
| `intensity` | float | 0.5 | Within [0.0, 1.0]; scales the movement's amplitude |

An absent `motion` object SHALL be equivalent to `{type: static}`, which preserves today's frozen-still clip behaviour exactly. The schema SHALL NOT accept raw ffmpeg filter expressions.

#### Scenario: Absent motion defaults to static

- **WHEN** a scene declares no `motion` object
- **THEN** the resolved motion type SHALL be `static` and the clip phase SHALL behave exactly as it does today

#### Scenario: Intensity out of range is rejected

- **WHEN** a scene declares `motion: {type: zoom_in, intensity: 1.5}`
- **THEN** `Validate` SHALL return an error stating that `intensity` must be within [0.0, 1.0]

### Requirement: look declares a per-scene finishing preset

`config.Scene` SHALL accept an optional `look` object:

| Field | Type | Default | Constraint |
|---|---|---|---|
| `grain` | float | 0.0 | Within [0.0, 1.0]; 0.0 means no grain filter at all |
| `white_balance` | string | `neutral` | One of `neutral`, `warm`, `cool`, `fluorescent`; `neutral` means no adjustment filter at all |

An absent `look` object (or one with only default values) SHALL add no filter to the clip invocation.

#### Scenario: Absent look adds nothing

- **WHEN** a scene declares no `look` object
- **THEN** the clip's ffmpeg invocation SHALL contain no grain or white-balance filter

#### Scenario: Invalid white_balance is rejected

- **WHEN** a scene declares `look: {white_balance: sepia}`
- **THEN** `Validate` SHALL return an error listing the accepted white-balance values

### Requirement: silence_seconds declares a silent scene

`config.Scene` SHALL accept an optional `silence_seconds` field of type float:

- When present, its value SHALL be > 0 (zero or negative is a validation error naming the scene ID).
- When present, the scene is a **silent scene**: `tts_text` may be empty, the scene's duration is exactly `silence_seconds`, and the narration pipeline (`piper`, `ffprobe` duration probing) SHALL NOT run for it (orchestration specified in the `cli` capability).
- When present together with a non-empty `tts_text`, `Validate` SHALL return an error: a scene is either narrated or silent, never both.
- When absent, the scene is narrated and `tts_text` remains mandatory in video mode (unchanged).

#### Scenario: Zero silence_seconds is rejected

- **WHEN** a scene declares `silence_seconds: 0`
- **THEN** `Validate` SHALL return an error stating that `silence_seconds` must be > 0

#### Scenario: Silent scene with narration text is rejected

- **WHEN** a scene declares both `silence_seconds: 2.0` and `tts_text: "hello"`
- **THEN** `Validate` SHALL return an error naming the scene and stating the fields are mutually exclusive

### Requirement: An .onnx voice needs its companion config
When `defaults.voice` names a file ending in `.onnx`, `Validate` SHALL also require the file `<voice>.json` to exist and SHALL return an error naming the missing companion path otherwise, without invoking any binary. Voices with other extensions SHALL NOT be subject to this check.

#### Scenario: companion missing
- **WHEN** `defaults.voice` is `voices/a.onnx` and `voices/a.onnx.json` does not exist
- **THEN** `Validate` returns an error naming `voices/a.onnx.json`

#### Scenario: companion present
- **WHEN** both `voices/a.onnx` and `voices/a.onnx.json` exist
- **THEN** no voice error is raised

### Requirement: Defaults may declare a model set instead of a single checkpoint

`defaults.model_set` SHALL be an optional object with the fields `diffusion_model`, `llm`, `vae` (file paths) and an optional `cfg_scale` (number). It describes diffusion models that need a text-encoder LLM and a VAE alongside them, and it is an alternative to `defaults.model`.

#### Scenario: model_set parses into the config
- **WHEN** `scenes.yaml` sets `defaults.model_set` with `diffusion_model`, `llm`, `vae` and `cfg_scale: 1`
- **THEN** `Load` returns a `Config` whose `Defaults.ModelSet` holds those four values

#### Scenario: absent model_set leaves behaviour unchanged
- **WHEN** `defaults.model_set` is omitted
- **THEN** the loaded config has no model set and validation of `defaults.model` is unchanged

### Requirement: Validate checks the model set

`Validate` SHALL reject a configuration where both `defaults.model` and `defaults.model_set` are set, naming both fields. When `model_set` is present it SHALL require non-empty `diffusion_model`, `llm` and `vae`, SHALL reject a `cfg_scale` that is negative, and SHALL require each referenced file to exist (resolved like `defaults.model`). A missing field or file error SHALL name the field.

#### Scenario: model and model_set together are rejected
- **WHEN** both `defaults.model` and `defaults.model_set` are set
- **THEN** `Validate` returns an error naming `defaults.model` and `defaults.model_set`

#### Scenario: incomplete model_set is rejected
- **WHEN** `defaults.model_set` omits `vae`
- **THEN** `Validate` returns an error naming `defaults.model_set.vae`

#### Scenario: missing model_set file is rejected
- **WHEN** `defaults.model_set.llm` points to a file that does not exist
- **THEN** `Validate` returns an error naming `defaults.model_set.llm` and the path

#### Scenario: valid model_set passes
- **WHEN** all three files exist and `cfg_scale` is 1
- **THEN** `Validate` reports no error for the model set

### Requirement: Defaults may configure the animate phase

`defaults.animate` SHALL be an optional object with `diffusion_model`, `t5xxl` and `vae` (file paths) and the optional settings `resolution` (`WIDTHxHEIGHT`, default `832x480`), `frames` (default 33), `fps` (default 16), `steps` (default 20) and `cfg_scale` (default 6). Omitted settings SHALL resolve to those defaults.

#### Scenario: defaults are applied
- **WHEN** `defaults.animate` sets only the three model files
- **THEN** the resolved settings are 832x480, 33 frames, 16 fps, 20 steps, cfg scale 6

### Requirement: Scenes opt in to animation

A scene MAY set `animate: true` and an optional `motion_prompt`. `animate` absent or `false` means no animation. Validation SHALL reject `animate: true` when `defaults.animate` is absent or lacks any of its three model files, naming the scene ID and the missing field. It SHALL require each referenced model file to exist, resolved like `defaults.model`, only when at least one scene animates. It SHALL reject a `resolution` that is not `WIDTHxHEIGHT` with both sides positive multiples of 16 (Wan's requirement), `frames` that is not of the form 4n+1, and non-positive `fps` or `steps`.

#### Scenario: animate without defaults.animate
- **WHEN** a scene sets `animate: true` and `defaults.animate` is absent
- **THEN** `Validate` returns an error naming the scene and `defaults.animate`

#### Scenario: model files are checked only when used
- **WHEN** no scene sets `animate`
- **THEN** a `defaults.animate` pointing to missing files does not make `Validate` fail

#### Scenario: invalid frame count
- **WHEN** `defaults.animate.frames` is 30
- **THEN** `Validate` returns an error saying frames must be 4n+1

#### Scenario: invalid resolution
- **WHEN** `defaults.animate.resolution` is `830x480`
- **THEN** `Validate` returns an error saying both sides must be multiples of 16

### Requirement: Animation conflicts with camera motion and images mode are warnings

When a scene sets both `animate: true` and a `motion` other than `static`, `Validate` SHALL emit a warning naming the scene that `motion` is ignored. In `images` output mode, a scene with `animate: true` SHALL produce a warning that it is ignored, as other clip-phase fields are.

#### Scenario: motion and animate together
- **WHEN** a scene sets `animate: true` and `motion.type: zoom_in`
- **THEN** validation succeeds with a warning naming the scene

### Requirement: tracks declares the songs of a music project

`config.Config` SHALL accept a top-level `tracks` list whose items have the fields defined by the `music` capability. In `music` mode `tracks` SHALL be non-empty and `scenes` SHALL be empty or absent; in `video` and `images` modes a non-empty `tracks` list SHALL be rejected. Track IDs SHALL be unique and match the same filename-safe pattern as scene IDs.

#### Scenario: Music project with scenes
- **WHEN** `output.mode` is `music` and `scenes` is non-empty
- **THEN** `Validate` returns an error saying music projects use `tracks`

#### Scenario: Video project with tracks
- **WHEN** `output.mode` is `video` and `tracks` is non-empty
- **THEN** `Validate` returns an error naming `tracks` and the mode

#### Scenario: Duplicate track id
- **WHEN** two tracks share an `id`
- **THEN** `Validate` returns an error naming the id

### Requirement: defaults.music configures the engine

`defaults` SHALL accept a `music` object with: `models_dir`, `lm_model`, `synth_model`, `output_format` (`mp3` or `wav`), `mp3_bitrate`, `lm_temperature`, `lm_cfg_scale`, `lm_top_p`, `lm_top_k`, `lm_negative_prompt`, `inference_steps`, `guidance_scale`, `shift`, `solver` and `vocal_language`. Unset values SHALL use the engine defaults. An `output_format` outside `mp3`/`wav` SHALL be rejected. `defaults.subject_prompt` and `defaults.negative_prompt` SHALL NOT apply to music; if set in a music project they SHALL be ignored with a warning.

#### Scenario: Invalid output format
- **WHEN** `defaults.music.output_format` is `ogg`
- **THEN** `Validate` returns an error listing `mp3` and `wav`

#### Scenario: Image defaults in a music project
- **WHEN** a music project sets `defaults.subject_prompt`
- **THEN** `Validate` accepts the config and emits a warning naming the field

### Requirement: defaults.music.style_prefix is text shared by every track

`defaults.music` SHALL accept a `style_prefix` string. When it is non-blank it SHALL be put in front of each track's prompt to form the caption (see the `music` capability); when it is absent or blank, captions SHALL be the prompts as written.

#### Scenario: Prefix is accepted
- **WHEN** `defaults.music.style_prefix` is `"warm analog production"`
- **THEN** `Validate` accepts the config

#### Scenario: Blank prefix changes nothing
- **WHEN** `style_prefix` is empty or only whitespace
- **THEN** the captions sent to the engine are the track prompts, exactly as without the field

### Requirement: Tracks and defaults may name a music adapter

`defaults.music` and each track SHALL accept `adapter` (the name of an installed adapter: a `.safetensors` file or a folder in the adapters directory) and `adapter_scale` (a non-negative number, default 1.0). A track's own value SHALL win over the default; an unset track field SHALL inherit it. Validation SHALL fail, before any process runs, when a named adapter is not installed, naming it and pointing at the Models screen, and when the scale is negative.

#### Scenario: Default adapter applies to every track
- **WHEN** `defaults.music.adapter` is `style` and a track sets none
- **THEN** that track uses `style` with the default scale

#### Scenario: Track overrides the default
- **WHEN** a track sets `adapter_scale: 0.5` and the default scale is 1.0
- **THEN** that track uses 0.5

#### Scenario: Missing adapter
- **WHEN** a track names an adapter that is not installed
- **THEN** validation fails naming the adapter and the Models screen

#### Scenario: Negative scale
- **WHEN** `adapter_scale` is -1
- **THEN** validation fails

### Requirement: A track may transform an existing song

A track MAY set `task` to `cover`, `cover-nofsq`, `repaint` or `extract`, and then SHALL set `source` to the name of an `.mp3` or `.wav` file in the project's `sources/` folder, which SHALL exist. `source` SHALL be a file name, not a path. Any other `task` value SHALL be rejected, and `lego` and `complete` SHALL be rejected with a message saying they are not supported. Without `task`, a track is a new song and SHALL NOT set `source` or the task fields.

#### Scenario: Valid cover
- **WHEN** a track has `task: cover` and a `source` that exists
- **THEN** the project is valid

#### Scenario: Missing source
- **WHEN** a task track names a source that is not in `sources/`
- **THEN** validation fails naming the track and the file

#### Scenario: Source given as a path
- **WHEN** `source` is `../x.mp3` or `a/b.mp3`
- **THEN** validation fails saying it must be a file name

#### Scenario: Source without task
- **WHEN** a track has `source` but no `task`
- **THEN** validation fails

### Requirement: Task fields belong to their task

`cover_strength` (between 0 and 1) SHALL be accepted only with `cover` or `cover-nofsq`. `repaint_start` and `repaint_end` (seconds) SHALL be accepted only with `repaint`, and when both are set the end SHALL be greater than the start. `stem` SHALL be required with `extract` and SHALL be one of vocals, backing_vocals, drums, bass, guitar, keyboard, percussion, strings, synth, fx, brass or woodwinds, and SHALL be rejected with any other task. `duration_seconds` SHALL be rejected on a task track, since the source decides the length. `use_lm` on a task track SHALL produce a warning and be ignored.

#### Scenario: Field of another task
- **WHEN** a `cover` track sets `repaint_start`
- **THEN** validation fails naming the field and the task

#### Scenario: Extract without stem
- **WHEN** an `extract` track has no `stem`
- **THEN** validation fails

#### Scenario: Inverted region
- **WHEN** a `repaint` track has `repaint_start: 20` and `repaint_end: 10`
- **THEN** validation fails

### Requirement: Extract needs a model that is not turbo

A project with an `extract` track SHALL be rejected when its synthesis model name contains `turbo`, with a message pointing at the SFT model.

#### Scenario: Extract with the turbo model
- **WHEN** an `extract` track runs with a turbo synthesis model
- **THEN** validation fails suggesting the SFT model

### Requirement: A video project may have background music

A video project MAY have a `bgm` block with exactly one of `prompt` (a soundtrack to generate) and `file` (the name of an `.mp3` or `.wav` in the project's `sources/` folder, which SHALL exist), and optionally `volume` (between 0 and 1, default 0.25), `fade_seconds` (0 or more, default 2), `ducking` (default true) and, with `prompt`, `seed`. `bgm` SHALL be rejected in images and music modes. A generated soundtrack SHALL require the music models (synthesis model, text encoder and VAE) to exist.

#### Scenario: Prompt
- **WHEN** a video project has `bgm: {prompt: calm piano}` and the music models are installed
- **THEN** the project is valid

#### Scenario: Both or neither
- **WHEN** `bgm` has both `prompt` and `file`, or neither
- **THEN** validation fails

#### Scenario: Missing file
- **WHEN** `bgm.file` is not in `sources/`
- **THEN** validation fails naming the file

#### Scenario: Wrong mode
- **WHEN** an images or music project has `bgm`
- **THEN** validation fails

#### Scenario: Out of range
- **WHEN** `volume` is 1.5 or `fade_seconds` is negative
- **THEN** validation fails

#### Scenario: Missing models
- **WHEN** `bgm.prompt` is set and the synthesis model is not installed
- **THEN** validation fails saying where to install it

### Requirement: The music engine can be a server

`defaults.music.engine` MAY be `cli` (default) or `server`; any other value SHALL be rejected. `defaults.music.keep_loaded` (default true) SHALL be accepted only with `engine: server`.

#### Scenario: Server
- **WHEN** `defaults.music.engine` is `server`
- **THEN** the project is valid

#### Scenario: Unknown engine
- **WHEN** `engine` is `remote`
- **THEN** validation fails naming the valid values

#### Scenario: keep_loaded without server
- **WHEN** `keep_loaded` is set and the engine is `cli`
- **THEN** validation fails

### Requirement: A new song may have a reference

A track without a `task` MAY set `reference` to the name of an `.mp3` or `.wav` file in the project's `sources/` folder, which SHALL exist and SHALL be a file name, not a path. A track with a `task` SHALL NOT set `reference`.

#### Scenario: Valid reference
- **WHEN** a track has `reference: sound.mp3` and the file exists
- **THEN** the project is valid

#### Scenario: Missing file
- **WHEN** the file is not in `sources/`
- **THEN** validation fails naming the track and the file

#### Scenario: With a task
- **WHEN** a track has `task: cover` and `reference`
- **THEN** validation fails

### Requirement: A scene may use its own picture

In video mode a scene MAY set `image` to the name of a `.png`, `.jpg`, `.jpeg` or `.webp` file in the project's `sources/` folder, which SHALL exist and SHALL be a file name, not a path. A scene with `image` SHALL NOT need a `prompt`; `negative_prompt`, `lora` and `variants` on it SHALL produce a warning that they are ignored. `image` SHALL be rejected outside video mode.

#### Scenario: Valid picture
- **WHEN** a scene has `image: photo.jpg`, no prompt, and the file exists
- **THEN** the project is valid

#### Scenario: Missing file
- **WHEN** the file is not in `sources/`
- **THEN** validation fails naming the scene and the file

#### Scenario: Audio as picture
- **WHEN** `image` names an `.mp3`
- **THEN** validation fails saying it must be a picture

#### Scenario: Ignored fields
- **WHEN** a scene with `image` also sets a negative prompt
- **THEN** the project is valid with a warning

#### Scenario: Images mode
- **WHEN** an images project has a scene with `image`
- **THEN** validation fails

### Requirement: Rotation presets

`motion.type` MAY be `rotate_cw` or `rotate_ccw`, and `intensity` SHALL keep its meaning (0 to 1).

#### Scenario: Valid
- **WHEN** a scene has `motion: {type: rotate_cw, intensity: 0.5}`
- **THEN** the project is valid

### Requirement: A scene may start from a picture

A scene MAY set `init_image` to the name of a `.png`, `.jpg`, `.jpeg` or `.webp` file in the project's `sources/` folder, which SHALL exist and SHALL be a file name, not a path, and `strength` between 0 and 1 (default 0.6), which SHALL be accepted only with `init_image`. A scene SHALL NOT set both `init_image` and `image`. A scene with `init_image` SHALL still have a prompt. `init_image` SHALL be rejected in music mode.

#### Scenario: Valid
- **WHEN** an images scene has `init_image: photo.jpg`, `strength: 0.4` and a prompt
- **THEN** the project is valid

#### Scenario: Missing file
- **WHEN** the file is not in `sources/`
- **THEN** validation fails naming the scene and the file

#### Scenario: Strength out of range
- **WHEN** `strength` is 1.5
- **THEN** validation fails

#### Scenario: Strength alone
- **WHEN** a scene sets `strength` without `init_image`
- **THEN** validation fails

#### Scenario: Both pictures
- **WHEN** a video scene sets `image` and `init_image`
- **THEN** validation fails

### Requirement: A scene may set its own render settings

A scene MAY declare a `render` object with `resolution` (`WIDTHxHEIGHT`), `steps` and `cfg_scale`, each optional. An absent object or field SHALL resolve to the project's value (`defaults.resolution`, `defaults.steps`, and the cfg of `defaults.model_set` when there is one). `cfg_scale` SHALL be able to be 0 (it is distinct from absent). Validation SHALL reject a malformed or non-positive `render.resolution`, `render.steps` below 1 and a negative `render.cfg_scale`, naming the scene and the field. A project without any scene `render` SHALL behave exactly as before.

#### Scenario: a scene overrides the resolution and the steps
- **WHEN** a scene sets `render: {resolution: 1280x704, steps: 12}`
- **THEN** its effective resolution is 1280x704 and its steps 12, and the other scenes keep the project's

#### Scenario: an absent block
- **WHEN** a scene has no `render`
- **THEN** its effective settings are the project's

#### Scenario: invalid values
- **WHEN** a scene sets `render.steps: 0` or `render.resolution: "big"`
- **THEN** validation fails naming the scene and the field

### Requirement: A scene may set its own animation settings

A scene MAY declare an `animation` object with `resolution`, `frames` and `steps`, each optional, which override the matching setting of `defaults.animate` for that scene only (the model, the frame rate and the rest remain the project's, resolved with the mode's defaults). Validation SHALL apply the rules of `defaults.animate` to each given value: both sides of `resolution` positive multiples of 16, `frames` of the form 4n+1, `steps` at least 1, naming the scene. The block SHALL NOT be an error on a scene that does not animate; it is then unused.

#### Scenario: more frames for one scene
- **WHEN** an animated scene sets `animation: {frames: 49}` and the project's frames are 33
- **THEN** that scene animates 49 frames and the others 33

#### Scenario: invalid frames
- **WHEN** a scene sets `animation.frames: 30`
- **THEN** validation fails saying the frames must be 4n+1 and naming the scene

#### Scenario: not animated
- **WHEN** a scene has `animation` but no `animate: true`
- **THEN** validation passes and the block is not used

### Requirement: defaults.animate chooses the animation engine

`defaults.animate` SHALL accept an optional `engine`, either `wan`, `animatediff`, `minimax-h3` or `ltx-2.3`; absent means `wan`. Any other value SHALL be refused naming the allowed ones. With `animatediff` it SHALL accept `checkpoint` (an SD 1.5 model file), `motion_module` (a file) and `strength`, and the Wan fields `diffusion_model`, `t5xxl`, `vae` and the high-noise settings SHALL be neither required nor used; with `wan` the AnimateDiff fields SHALL be ignored. A project without `engine` SHALL load, resolve and serialize as before this option existed.

#### Scenario: engine absent
- **WHEN** `defaults.animate` has the three Wan files and no `engine`
- **THEN** the engine is Wan and every resolved setting is what it was before

#### Scenario: unknown engine
- **WHEN** `defaults.animate.engine` is `svd`
- **THEN** validation fails saying the engine must be `wan`, `animatediff`, `minimax-h3` or `ltx-2.3`

#### Scenario: AnimateDiff without Wan files
- **WHEN** `engine` is `animatediff`, `checkpoint` and `motion_module` are set and the Wan fields are empty
- **THEN** the configuration is valid

### Requirement: AnimateDiff has its own defaults

When the engine is `animatediff`, unset settings SHALL resolve to `resolution` 512x512, `frames` 16, `fps` 8, `steps` 25, `cfg_scale` 7 and `strength` 0.75. A value the user wrote SHALL always win, including an explicit `cfg_scale` of 0.

#### Scenario: defaults
- **WHEN** `engine: animatediff` sets only `checkpoint` and `motion_module`
- **THEN** the resolved settings are 512x512, 16 frames, 8 fps, 25 steps, cfg 7 and strength 0.75

#### Scenario: the user wins
- **WHEN** the block also sets `frames: 24` and `strength: 0.6`
- **THEN** the resolved frames are 24 and the strength 0.6

### Requirement: AnimateDiff settings are validated with AnimateDiff's rules

With `engine: animatediff`, the resolution SHALL be WIDTHxHEIGHT with both sides positive multiples of 8, `frames` SHALL be between 8 and 32 inclusive (and not required to be of the form 4n+1), `fps` and `steps` SHALL be positive, `cfg_scale` SHALL NOT be negative and `strength` SHALL be greater than 0 and at most 1. A scene's own `animation` resolution, frames and steps SHALL be validated with the rules of the effective engine. When at least one scene animates, `checkpoint` and `motion_module` SHALL be set and the files SHALL exist, with the same kind of error as for the Wan files.

#### Scenario: frames out of range
- **WHEN** the engine is `animatediff` and `frames` is 40
- **THEN** validation fails saying frames must be between 8 and 32

#### Scenario: frames not 4n+1
- **WHEN** the engine is `animatediff` and `frames` is 16
- **THEN** the value is accepted (it would be refused for Wan)

#### Scenario: missing module
- **WHEN** a scene animates and the motion module file does not exist
- **THEN** validation fails naming `defaults.animate.motion_module` and the path

#### Scenario: a scene's own frames
- **WHEN** a scene sets `animation.frames: 49` under an AnimateDiff project
- **THEN** validation fails saying frames must be between 8 and 32

### Requirement: Vocals are a validated choice

A track and `defaults.music` MAY set `vocals` to one of `female`, `male`, `duet`, `child`, `choir` or `deep`. Any other value SHALL be refused naming the accepted ones. A track's value SHALL win over the project's default. Setting `vocals` on an instrumental track or an `extract` track SHALL produce a warning.

#### Scenario: Unknown voice
- **WHEN** a track sets `vocals: yoda`
- **THEN** validation fails and lists the accepted values

#### Scenario: Ignored on an instrumental
- **WHEN** an instrumental track sets `vocals: female`
- **THEN** validation passes with a warning that the voice is ignored

### Requirement: A mix has a master volume and normalisation

A mix MAY set `gain`, between 0 and 2 (default 1), and `normalize: true`. A `gain` outside the range SHALL be refused naming the mix.

#### Scenario: Valid master settings
- **WHEN** a mix sets `gain: 0.7` and `normalize: true`
- **THEN** validation passes

#### Scenario: Out of range
- **WHEN** a mix sets `gain: 2.5`
- **THEN** validation fails with `mix "<id>": gain must be between 0 and 2`

### Requirement: Favorites mark songs of a music project

A music project MAY list `favorites`, each with a `track` and a `seed`. An entry without a track SHALL be refused; a duplicate or an entry whose track is not in the project SHALL only warn. Runs SHALL NOT read the list.

#### Scenario: Valid list
- **WHEN** the project lists `{track: a, seed: 99}` for an existing track
- **THEN** validation passes

#### Scenario: Missing track
- **WHEN** a favorite names a track that is not in the project
- **THEN** validation passes with a warning

#### Scenario: Empty track
- **WHEN** a favorite has no track
- **THEN** validation fails

### Requirement: The minimax-h3 engine has its own files, defaults and rules

With `defaults.animate.engine: minimax-h3`, `diffusion_model`, `vae`, `audio_vae` and `llm` SHALL be the model files (required, and they SHALL exist, when at least one scene animates), and the Wan `t5xxl` and high-noise settings SHALL be neither required nor used. Unset settings SHALL resolve to `resolution` 864x480, `frames` 56, `steps` 20 and `cfg_scale` 1, and `fps` SHALL always resolve to 24. The resolution SHALL have both sides positive multiples of 32 and `frames` SHALL be of the form 17k+5 (22, 39, 56, ...), also in a scene's own `animation` block.

#### Scenario: defaults
- **WHEN** `engine: minimax-h3` sets only the four files
- **THEN** the resolved settings are 864x480, 56 frames, 24 fps, 20 steps and cfg 1

#### Scenario: frames off the grid
- **WHEN** the engine is `minimax-h3` and `frames` is 33
- **THEN** validation fails saying frames must be 17k+5

#### Scenario: a missing file
- **WHEN** a scene animates and `llm` is not set
- **THEN** validation fails saying `defaults.animate.llm` is not set

### Requirement: The minimax-h3 engine accepts an optional LoRA

With `defaults.animate.engine: minimax-h3`, `defaults.animate.lora` MAY name a LoRA file under the project (empty means none). When set it SHALL exist on disk, and when `steps` is unset the default steps SHALL be 4 instead of 20. The field SHALL be ignored by the other engines.

#### Scenario: LoRA sets the default steps
- **WHEN** `engine: minimax-h3` has `lora` and no `steps`
- **THEN** the effective steps are 4

#### Scenario: missing LoRA file
- **WHEN** `lora` names a file that is not on disk
- **THEN** validation fails naming `defaults.animate.lora`

### Requirement: The ltx-2.3 engine has its own files, defaults and rules

With `defaults.animate.engine: ltx-2.3`, `diffusion_model`, `vae`, `audio_vae`, `llm` and the new `connectors` SHALL be the model files (required, and they SHALL exist, when at least one scene animates). Unset settings SHALL resolve to 768x416, 49 frames, 24 fps (fixed), 8 steps and cfg scale 1. Both sides of the resolution SHALL be positive multiples of 32 and the frame count SHALL be of the form 8k+1 (9, 17, 25, 33, 41, 49, ...), in the defaults and in each scene's `animation` override. `connectors` SHALL be refused with any other engine, and `first_frame` and `lora` SHALL be refused with `ltx-2.3`.

#### Scenario: engine defaults
- **WHEN** `engine: ltx-2.3` sets only the five files
- **THEN** it resolves to 768x416, 49 frames, 24 fps, 8 steps and cfg 1

#### Scenario: frame grid
- **WHEN** the engine is `ltx-2.3` and `frames` is 56
- **THEN** validation fails saying frames must be 8k+1 (…, 41, 49, 57, …)

#### Scenario: side grid
- **WHEN** the engine is `ltx-2.3` and `resolution` is `768x432`
- **THEN** validation fails saying both sides must be multiples of 32

#### Scenario: connectors with another engine
- **WHEN** `connectors` is set and the engine is `wan`
- **THEN** validation fails saying it only applies to `ltx-2.3`

### Requirement: defaults.animate.model_audio keeps the model's audio as ambience

`defaults.animate.model_audio` SHALL be an optional boolean, false when unset. It SHALL be accepted only when the engine is `minimax-h3` or `ltx-2.3`; with any other engine (or none) validation SHALL fail naming the setting. It SHALL round-trip through the config API without being dropped.

#### Scenario: Accepted with LTX
- **WHEN** `engine: ltx-2.3` and `model_audio: true`
- **THEN** the config is valid

#### Scenario: Rejected with Wan
- **WHEN** the engine is Wan and `model_audio: true`
- **THEN** validation fails with a message naming `model_audio` and the two engines that support it

#### Scenario: Unset
- **WHEN** `model_audio` is absent
- **THEN** it behaves as false and is not written back by a save

### Requirement: The music settings choose the engine and a track can carry tags

`defaults.music.generator` SHALL be an optional text, `ace-step` or `heartmula`, and any other value SHALL fail validation naming the setting; absent means `ace-step`. A track SHALL accept `tags`, a list of short words or a text of words separated by commas, normalised to the form the engine takes (lower case, separated by commas, no spaces around them). Both SHALL round-trip through the config API and the YAML without being dropped.

#### Scenario: Valid engine
- **WHEN** `generator: heartmula`
- **THEN** the config is valid

#### Scenario: Unknown engine
- **WHEN** `generator: other`
- **THEN** validation fails naming `defaults.music.generator`

#### Scenario: Tags
- **WHEN** a track has `tags: "Piano, Happy"`
- **THEN** it is kept as `piano,happy`

### Requirement: A project may carry the client that asked for it

`scenes.yaml` MAY carry a top-level `client`, the name of the dashboard that requested the project through the generation API: one to 32 characters among lowercase letters, digits and hyphens. It SHALL change nothing in a run: the server copies it to the run so the statistics and the metrics can group by it. A `client` that does not fit that form SHALL be refused when the file is loaded, naming the field. A project without it has no client.

#### Scenario: a client

- **WHEN** `scenes.yaml` has `client: video-studio`
- **THEN** it loads and the value is kept

#### Scenario: a client that does not fit

- **WHEN** `scenes.yaml` has `client: Video Studio`
- **THEN** loading fails with an error that names `client`

#### Scenario: no client

- **WHEN** `scenes.yaml` has no `client`
- **THEN** it loads as before

### Requirement: Every path in a project configuration stays inside the project

Every field of `scenes.yaml` that names a file or a folder (`output.file`, `project`, `defaults.model`, `defaults.voice`, the models directory of music, adapters, scene `image`, `clip`, `video`, `bgm.file`, references and any other path field) SHALL be rejected by validation when it is an absolute path, when any of its segments is `..`, or when it is empty where a value is required. A path SHALL be interpreted relative to the project folder (or to the models directory, for model and voice names that resolve there). The error SHALL name the field, the value and the rule. The rule SHALL be the same whether the configuration is read by the command line or by the server.

#### Scenario: an absolute output path
- **WHEN** `output.file` is `/tmp/x.mp4`
- **THEN** validation fails naming `output.file` and saying paths are relative to the project

#### Scenario: a path that climbs out
- **WHEN** `defaults.model` is `../../models/x.safetensors`
- **THEN** validation fails naming `defaults.model` and the `..` segment

#### Scenario: a relative path inside the project
- **WHEN** `clip` is `sources/intro.mp4`
- **THEN** validation accepts it

#### Scenario: a new field that looks like a path
- **WHEN** a string field is added to the configuration types whose name says it holds a file or a folder, or whose name looks like a place without being recognised as a path
- **THEN** the field-classification test fails naming the field, so its author decides whether it must stay inside the project

### Requirement: Output file, project and scene ids follow strict patterns

`output.file` SHALL be a plain file name or a relative path under `out/`, with a video extension the assembler accepts. `project` and every scene id SHALL match `^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`. A duplicate scene id remains an error.

#### Scenario: an output file in another folder
- **WHEN** `output.file` is `../final.mp4` or `videos/final.mp4`
- **THEN** validation fails saying the output goes under `out/`

#### Scenario: a scene id with a slash
- **WHEN** a scene has id `../../tmp/x`
- **THEN** validation fails naming the scene and the pattern

#### Scenario: a usual id
- **WHEN** a scene has id `s06_stand-up.v2`
- **THEN** validation accepts it

### Requirement: Numbers that drive a job have upper bounds

Validation SHALL reject values above a named limit for: sampling steps, frames per clip, frames per second, each side of a resolution and its total pixels, variants per scene, `silence_seconds`, `duration_seconds`, transition seconds and inference steps of the music engines. The limits SHALL be constants with a name, one per engine where the engine sets it, and the error SHALL name the field, the value and the limit. The limits SHALL be at least as large as every value the documentation and the examples use.

#### Scenario: too many steps
- **WHEN** a scene asks for `steps: 100000`
- **THEN** validation fails naming `steps`, the value and the limit

#### Scenario: a huge resolution
- **WHEN** `resolution: 16384x16384`
- **THEN** validation fails naming the resolution limit

#### Scenario: the examples still pass
- **WHEN** every `scenes.yaml` of `docs/examples/` and `testdata/` is validated
- **THEN** all pass

### Requirement: The configuration is parsed strictly and with a size limit

Parsing SHALL reject a field the configuration does not define, naming the field and its line. The server SHALL reject a configuration larger than a fixed limit (at least 1 MiB) with the code `invalid` and a message that names the limit; the command line keeps reading a file of any size.

#### Scenario: a typo in a field
- **WHEN** a scene has `step: 20` instead of `steps: 20`
- **THEN** parsing fails naming `step` and the line

#### Scenario: an oversized configuration through the API
- **WHEN** `PUT /api/projects/{id}/config` carries a YAML larger than the limit
- **THEN** the answer is `413` with the standard error body and nothing is written

### Requirement: A project can have a video track and layer scenes

`scenes.yaml` SHALL accept, in a video project, `video_track`: a list of `{id, clip, start, end, clip_start?, clip_end?, clip_speed?, scrim?, transition_seconds?}` with the meaning and the limits of the video track of a script (a file name of `sources/`, seconds of the output video, items in order with no overlap, at most 64), and `scrim` and the gaps coloured by the design's `--bg` (`defaults.motion.design.dark` or `light` as the mode says), black (`#000000`) when the project has no motion design. It SHALL accept on a scene `layer: true` together with `motion_graphic` or `motion_part`: the scene is a transparent page, laid over the track at its place in the output instead of being a picture of its own. The validation SHALL name the item or scene and the field for an unknown or missing clip, a trim past the file, an item outside the project's length, an overlap, a `layer` that is not on a motion scene, a `layer` scene with `clip`, `image`, `animate` or `overlay`, and a `video_track` in a project with no scenes. A project with neither SHALL load, validate and run exactly as before.

#### Scenario: A project with a track
- **WHEN** `scenes.yaml` has `video_track` with two items and three `layer: true` motion scenes
- **THEN** it loads and validates, and each item and scene keeps its fields

#### Scenario: A layer that is not a page
- **WHEN** a scene has `layer: true` and `image`
- **THEN** the validation names the scene and says a layer is a page

#### Scenario: No motion design
- **WHEN** a project with `video_track` has no `defaults.motion.design`
- **THEN** the scrim and the gaps are black

#### Scenario: Unchanged projects
- **WHEN** a project has no `video_track` and no `layer`
- **THEN** it behaves exactly as before

### Requirement: A video track item accepts local scrims

A `video_track` item of `scenes.yaml` SHALL accept `local_scrims`, a list of `{band, scrim}` with `band` one of `top`, `center` and `bottom` (each at most once) and `scrim` a number from 0 to 0.8, with the meaning of "A track item can have a local scrim over the band of the verse". An unknown band, a repeated band or a `scrim` outside 0 to 0.8 SHALL be refused naming the item and the field. A project with no `local_scrims` SHALL load and be written exactly as before.

#### Scenario: A local scrim
- **WHEN** an item has `local_scrims: [{band: bottom, scrim: 0.5}]`
- **THEN** the project loads and the item keeps it

#### Scenario: A band that does not exist
- **WHEN** an item has `local_scrims: [{band: left, scrim: 0.5}]`
- **THEN** the project is refused naming the item and `local_scrims`
