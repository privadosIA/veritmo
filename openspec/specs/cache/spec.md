# cache Specification

## Purpose
Defines the per-scene, per-phase content-hash cache that lets a re-run skip phases whose inputs are unchanged, and the stable artifact paths that cache relies on.

## Requirements

### Requirement: Each phase's cache hit/miss depends only on that phase's own inputs
`Decide` SHALL determine the tts phase's hit/miss from the audio hash alone and the render phase's hit/miss from the image hash alone, independently of each other.

#### Scenario: unchanged audio inputs, changed image inputs
- **WHEN** a scene's manifest entry's audio hash matches the newly computed audio hash but its image hash does not
- **THEN** `Decide` reports the tts phase as a hit and the render phase as a miss

### Requirement: The clip phase is a hit only when both audio and image are hits

`Decide` SHALL report the clip phase as a hit only when the audio hash, the image hash, **and the clip-parameters hash** all match the manifest entry. A manifest entry without a stored clip hash (written by a pre-change binary) SHALL be treated as a clip miss, re-encoding that clip once and storing the hash; the audio and image phases are unaffected by this migration.

#### Scenario: both hashes match

- **WHEN** the audio hash, image hash, and clip-parameters hash all match the manifest entry
- **THEN** `Decide` reports the clip phase as a hit

#### Scenario: only one hash matches

- **WHEN** only one of the audio and image hashes matches the manifest entry
- **THEN** `Decide` reports the clip phase as a miss

#### Scenario: clip parameters changed

- **WHEN** the audio hash and image hash match but the clip-parameters hash does not (e.g. the scene's `motion.intensity` changed)
- **THEN** `Decide` reports the tts and render phases as hits
- **AND** reports the clip phase as a miss

#### Scenario: legacy manifest entry without clip hash

- **WHEN** a manifest entry written by a pre-change binary (no stored clip hash) is evaluated with matching audio and image hashes
- **THEN** `Decide` reports the clip phase as a miss
- **AND** after the re-run, the manifest entry stores the clip-parameters hash so the next evaluation can hit

### Requirement: --force bypasses the cache entirely
`Decide` SHALL report every phase as a miss when `force` is true, regardless of the manifest's content.

#### Scenario: force is true
- **WHEN** `Decide` is called with `force=true`
- **THEN** every phase is reported as a miss regardless of the manifest's content

### Requirement: --only excludes non-target scenes from reprocessing without excluding them from the final video
`DecideWithFilter` SHALL report every phase as a hit for any scene that is not the active `OnlyFilter`'s target, so that scene's existing clip is reused for the final concatenation rather than silently dropped.

#### Scenario: scene is not the --only target
- **WHEN** `OnlyFilter` is active and a scene's id does not match its target
- **THEN** `DecideWithFilter` reports every phase as a hit for that scene

### Requirement: Artifact paths are stable and content-addressed by scene ID
`ArtifactPaths` SHALL return the same wav/png/mp4 paths for a given scene ID across separate calls (and separate runs).

#### Scenario: same scene ID across runs
- **WHEN** `ArtifactPaths` is called with the same scene ID in two different runs
- **THEN** it returns the same wav/png/mp4 paths both times

### Requirement: The image hash covers every input that changes the rendered image

`ImageParams` (defined in `internal/platform/cache/cache.go`) SHALL contain, in addition to the existing seven fields (`prompt`, `negative_prompt`, `seed`, `model`, `resolution`, `sampler`, `steps`), the following four fields:

| Field | Type | Source | Notes |
|---|---|---|---|
| `subject_prompt` | `string` | `defaults.subject_prompt` | Empty string when unset |
| `global_negative_prompt` | `string` | `defaults.negative_prompt` | Empty string when unset; distinct from the scene-level `negative_prompt` already in `ImageParams` |
| `lora` | `string` | `scene.lora` | Empty string until LoRA support ships (reserved per design D4) |
| `variant_index` | `int` | 0-based loop index in the per-scene render loop | 0 for single-variant scenes |

`ImageHash` (line 83 of `internal/platform/cache/cache.go`) SHALL incorporate all eleven fields (the seven legacy plus the four new) into the deterministic hash it returns. The hashing algorithm SHALL be a stable, order-sensitive composition (e.g. SHA-256 over a canonical concatenation of the field values) so that any change to any single field produces a different digest.

**Backward-compatibility invariant.** When `subject_prompt` is `""`, `global_negative_prompt` is `""`, `lora` is `""`, and `variant_index` is `0`, the digest produced by `ImageHash` SHALL be byte-identical to the digest produced by the current `add-script-to-video-pipeline` implementation for the same seven legacy field values. This guarantees that existing cache manifests remain valid after upgrading and that no cached PNG is invalidated by the schema extension alone.

#### Scenario: Changing the global subject prompt invalidates the cache

WHEN a project's `defaults.subject_prompt` is changed from `"cinematic"` to `"watercolor"` and all other render inputs (scene prompt, seed, model, resolution, sampler, steps, scene negative_prompt, lora, variant_index) remain unchanged,
THEN `ImageHash` SHALL return a different digest for the affected scene,
AND the render phase SHALL re-invoke `sd` for that scene (cache miss).

#### Scenario: Variant indices 0 and 1 of the same scene hash differently

WHEN a scene has `variants: 2` and all other render inputs are identical between variant 0 and variant 1 (same prompt, same model, same resolution, same sampler, same steps, same seed base, same subject_prompt, same global_negative_prompt, same lora),
THEN `ImageHash` for `variant_index = 0` SHALL differ from `ImageHash` for `variant_index = 1`,
AND both variants SHALL be independently cacheable (a hit on variant 0 does not satisfy a request for variant 1).

#### Scenario: A legacy video-mode project retains its current hash

WHEN a project uses `output.mode: video` (or omits `output` entirely), has `variants: 1` (or omits `variants`), and sets neither `defaults.subject_prompt` nor `defaults.negative_prompt` nor `scene.lora`,
THEN `ImageHash` for that scene SHALL produce the same digest as the pre-change implementation computed from the same seven legacy fields,
AND a previously cached PNG (written by the old binary) SHALL be recognised as a cache hit,
AND `sd` SHALL NOT be re-invoked for that scene.

### Requirement: The render cache is shared across output modes

The cache key (the digest returned by `ImageHash`) SHALL NOT incorporate `output.mode` or any other field that distinguishes `images` mode from `video` mode. The render cache is a function of render inputs only.

Consequently, a scene whose render inputs are unchanged SHALL be a cache hit in `video` mode if it was previously rendered in `images` mode (and vice versa), and `sd` SHALL NOT be re-invoked on such a hit regardless of the mode under which the project is currently running.

#### Scenario: Render in images mode, then re-run in video mode reuses the cached PNG

WHEN a project is run with `output.mode: images` and a scene's render phase completes (PNG written to the cache directory),
AND the user then changes `output.mode` to `video` (or removes the `output` block to default to video) without altering any render input (prompt, seed, model, resolution, sampler, steps, subject_prompt, global_negative_prompt, lora, variant_index),
AND the project is re-run,
THEN the render phase for that scene SHALL be reported as a cache hit (skipped),
AND `sd` SHALL NOT be invoked for that scene,
AND the previously rendered PNG SHALL be used for the clip phase.

### Requirement: Cache decisions in images mode consider only the render phase

In `images` mode, `Decide` (and `DecideWithFilter`) in `internal/platform/cache/cache.go` SHALL determine render-phase freshness exclusively from the image-hash digest and the presence of the cached PNG artefact. The decision SHALL NOT consult the audio-hash digest, the TTS artefact (WAV), or the clip artefact (MP4), because those phases are skipped entirely in images mode (per design D7).

A scene whose `tts_text` (or any audio-related input) has changed but whose render inputs are unchanged SHALL still be a render cache hit in images mode.

#### Scenario: Changed narration text does not invalidate the render cache in images mode

WHEN a project is in `output.mode: images`,
AND a scene's `tts_text` field is edited (e.g. the narration script is reworded) while all render inputs (prompt, seed, model, resolution, sampler, steps, subject_prompt, global_negative_prompt, lora, variant_index) remain unchanged,
AND the project is re-run,
THEN `Decide` SHALL report the render phase as a cache hit for that scene,
AND `sd` SHALL NOT be re-invoked,
AND the previously rendered PNG SHALL be reused for the export phase.

### Requirement: The clip hash covers every input that changes clip bytes without changing the PNG or WAV

The cache SHALL compute a deterministic clip-parameters hash (`ClipHash` over a `ClipParams` value) from, at minimum:

| Field | Source | Notes |
|---|---|---|
| motion type | `scene.motion.type` | `static` when absent |
| motion intensity | `scene.motion.intensity` | default when absent |
| grain | `scene.look.grain` | 0.0 when absent |
| white balance | `scene.look.white_balance` | `neutral` when absent |
| fps | `defaults.fps` | already affects clip bytes today |
| silence_seconds | `scene.silence_seconds` | 0 for narrated scenes |
| handheld phase seed | scene's resolved seed | only material for `handheld`, but hashed unconditionally for simplicity |

Like `ImageHash`, the algorithm SHALL be a stable, order-sensitive composition (SHA-256 over a canonical concatenation) so any single-field change produces a different digest. A scene using every default SHALL produce a stable, documented digest so that unchanged legacy projects converge to cache hits after the one-time migration miss.

The clip hash SHALL NOT include render inputs (prompt, model, resolution, sampler, steps, seed-as-render-input) — those already invalidate through the image hash — nor narration inputs, which invalidate through the audio hash.

#### Scenario: changing a motion preset invalidates only the clip

- **WHEN** a scene's `motion.type` changes from `static` to `zoom_in` and nothing else changes
- **THEN** the clip-parameters hash changes
- **AND** the image hash and audio hash are unchanged
- **AND** a re-run re-encodes the clip without re-invoking `sd` or `piper`

#### Scenario: identical defaults produce identical clip hashes across runs

- **WHEN** the same scene with no `motion`, no `look`, and no `silence_seconds` is hashed in two separate runs
- **THEN** both runs produce the same clip-parameters digest

### Requirement: The animate phase has its own cache hash

For animated scenes, the animate phase SHALL be a cache hit only when the scene's image hash is unchanged and an animate hash over the following inputs is unchanged: the motion prompt, the seed, the diffusion model, text encoder and VAE paths, resolution, frame count, frame rate, steps and cfg scale. Changing any of them SHALL make the phase a miss. Non-animated scenes SHALL be unaffected, and their hashes SHALL not change.

#### Scenario: changing the motion prompt re-animates only
- **WHEN** an animated scene's `motion_prompt` changes and nothing else does
- **THEN** `render` is a hit and `animate` is a miss

#### Scenario: changing the image re-animates
- **WHEN** the scene's prompt changes so the image hash changes
- **THEN** `animate` is a miss

#### Scenario: non-animated hashes are unchanged
- **WHEN** a project has no animated scene
- **THEN** its image and clip hashes equal those computed before this change

### Requirement: An animated scene's clip also depends on the animation

For an animated scene, the clip phase SHALL be a hit only when the animate phase is a hit as well as the audio, image and clip hashes. The clip hash SHALL include a marker that the scene is animated and the animate hash, so toggling `animate` re-encodes the clip.

#### Scenario: toggling animate re-encodes the clip
- **WHEN** a scene gains `animate: true` with everything else equal
- **THEN** `clip` is a miss

### Requirement: The compose phase has its own cache hash

In music mode the `compose` hash SHALL cover: the composed caption, the track's lyrics, `instrumental`, bpm, duration, key, time signature, vocal language, the effective seed, the language-model parameters (temperature, cfg scale, top-p, top-k, negative prompt) and the language-model file. A `compose` result SHALL be reused when its hash is unchanged and its artifact exists. Tracks that do not use the language model have no `compose` entry.

#### Scenario: Unchanged track
- **WHEN** a track is run twice with no input change
- **THEN** the second run emits `skipped` for `compose` and runs no `ace-lm` process

#### Scenario: Caption edit
- **WHEN** the caption of a track changes
- **THEN** both `compose` and `synth` run again for it

### Requirement: The synth phase has its own cache hash

The `synth` hash SHALL cover: the content of the request handed to the engine (which includes the composed lyrics and audio codes when the language model ran), the synthesis model file, the VAE file, inference steps, guidance scale, shift, solver, output format and MP3 bitrate. Changing only synthesis parameters SHALL NOT invalidate `compose`.

#### Scenario: Only synthesis parameters change
- **WHEN** `defaults.music.inference_steps` changes and nothing else
- **THEN** `compose` is skipped as a cache hit and `synth` runs again

#### Scenario: Different synthesis model
- **WHEN** `synth_model` points to another file
- **THEN** `synth` runs again and `compose` is a hit

### Requirement: Music cache decisions honor --force and --only

`--force` SHALL bypass the music cache for the target tracks and `--only` SHALL exclude other tracks from reprocessing without deleting their cached or exported files. Music artifact paths SHALL be stable and derived from the track ID and variant index.

#### Scenario: --force
- **WHEN** a fully cached track is run with `--force`
- **THEN** every phase runs again

#### Scenario: --only keeps other tracks
- **WHEN** a project is run with `--only intro`
- **THEN** cached artifacts of other tracks stay on disk and are not modified

### Requirement: The synth hash covers the adapter

The `synth` hash SHALL cover the adapter named by the request, its scale and the size of its files, so that changing the adapter, its scale or its files re-synthesizes the track. The `compose` hash SHALL NOT depend on the adapter.

#### Scenario: Changing the adapter
- **WHEN** a cached track gets another adapter
- **THEN** `synth` runs again and `compose` is a cache hit

#### Scenario: Replacing the adapter file
- **WHEN** the files of the named adapter are replaced by ones of another size
- **THEN** `synth` runs again

### Requirement: The synth hash covers the source

For a track with a task, the `synth` hash SHALL cover the content of the source file (not its name or date) and the task. Hashes of tracks without a task SHALL NOT change.

#### Scenario: Nothing changed
- **WHEN** a cover runs again with the same source content and fields
- **THEN** `synth` is a cache hit

#### Scenario: Source replaced
- **WHEN** the file named by `source` is overwritten with other content
- **THEN** `synth` runs again

#### Scenario: Region changed
- **WHEN** only `repaint_end` changes
- **THEN** `synth` runs again

### Requirement: A generated soundtrack is cached

The soundtrack generated from `bgm.prompt` SHALL be reused while its request (caption, duration, seed, steps…), models and adapter are unchanged, and regenerated when any changes. The duration SHALL be the video length rounded up to a multiple of 10 s, so edits that keep that value reuse it. Without `seed` it SHALL be derived from the prompt.

#### Scenario: Unchanged
- **WHEN** a project runs again with the same prompt and video length
- **THEN** no soundtrack is generated

#### Scenario: Prompt changed
- **WHEN** `bgm.prompt` changes
- **THEN** a new soundtrack is generated

#### Scenario: Slightly shorter video
- **WHEN** the video gets 2 s shorter but rounds to the same multiple of 10
- **THEN** the soundtrack is reused

#### Scenario: Stable default seed
- **WHEN** two runs have the same prompt and no seed
- **THEN** both use the same seed

### Requirement: The synth hash covers the reference

For a track with a reference, the `synth` hash SHALL cover the content of the file. Tracks without one keep their hash.

#### Scenario: Replaced file
- **WHEN** the reference file is overwritten with other content
- **THEN** `synth` runs again

#### Scenario: Unchanged
- **WHEN** nothing changes
- **THEN** both phases are cache hits

### Requirement: The scene picture depends on the given file

The `render` hash of a scene with `image` SHALL cover the content of the file and the project resolution, and SHALL NOT cover the prompt, negative prompt, seed, model or sampler. Hashes of scenes without `image` SHALL NOT change.

#### Scenario: Unchanged
- **WHEN** the same picture runs again
- **THEN** `render` is a cache hit

#### Scenario: Picture replaced
- **WHEN** the file is overwritten with other content
- **THEN** `render` and `clip` run again

#### Scenario: Prompt edited
- **WHEN** only the prompt of a scene with a picture changes
- **THEN** `render` is a cache hit

### Requirement: The image hash covers the starting picture

For a scene with `init_image`, the image hash SHALL cover the content of the file and the resolved strength. Hashes of scenes without it SHALL NOT change.

#### Scenario: Unchanged
- **WHEN** the same scene runs again
- **THEN** its render is a cache hit

#### Scenario: Picture replaced
- **WHEN** the file is overwritten with other content
- **THEN** the scene renders again

#### Scenario: Strength changed
- **WHEN** only the strength changes
- **THEN** the scene renders again

### Requirement: The animate cache key distinguishes AnimateDiff without invalidating Wan caches

When the engine is `animatediff`, the animate cache key SHALL include the engine, the checkpoint, the motion module and the strength besides the prompt, seed, resolution, frames, fps, steps and cfg scale, and SHALL NOT include the Wan model, text encoder and VAE. When the engine is Wan the key SHALL be exactly the one computed before this capability. Changing any included value SHALL regenerate the clip, and the scene's clip hash SHALL follow its animate hash as it does for Wan.

#### Scenario: Wan projects keep their cache
- **WHEN** a Wan project is run after this change
- **THEN** its animate cache keys equal the ones computed before, so its clips are cache hits

#### Scenario: changing the strength regenerates
- **WHEN** an AnimateDiff project changes `strength`, the checkpoint or the motion module
- **THEN** the animation is generated again, and the clip after it

#### Scenario: switching engine regenerates
- **WHEN** a project changes from Wan to AnimateDiff
- **THEN** its animated scenes are regenerated

### Requirement: --only does not rewrite the cache entries of the scenes it leaves out

A run restricted with `--only` SHALL leave the manifest entry of every scene other than its target as it was, including leaving it absent, so that a later run decides for those scenes from what was really produced for them.

#### Scenario: A project never rendered, run one scene at a time
- **WHEN** a fresh project is run with `--only a` and then with `--only b`
- **THEN** the second run produces `b` instead of skipping it as cached

#### Scenario: A scene edited while another is run
- **WHEN** scene `b`'s prompt changes and the project is run with `--only a`, and then without `--only`
- **THEN** `b`'s entry was not changed by the first run, and the second run renders `b` with its new prompt

### Requirement: The animate cache key distinguishes MiniMax-H3

When the engine is `minimax-h3`, the animate cache key SHALL include the engine, the DiT, the video VAE, the audio VAE and the text encoder, and the LoRA only when one is set (so the keys of runs without it do not change), besides the prompt, seed, resolution, frames, fps, steps and cfg scale. Wan and AnimateDiff keys SHALL NOT change. Changing any included value SHALL regenerate the clip.

#### Scenario: changing the encoder regenerates
- **WHEN** a MiniMax-H3 project changes `llm`
- **THEN** its animated scenes are generated again

#### Scenario: Wan keys are unchanged
- **WHEN** a Wan or AnimateDiff project runs after this change
- **THEN** its animate cache keys equal the ones computed before

### Requirement: The animate cache key distinguishes LTX-2.3

When the engine is `ltx-2.3`, the animate cache key SHALL include the engine, the DiT, the video VAE, the audio VAE, the text encoder and the connectors file, besides the prompt, seed, resolution, frames, fps, steps and cfg scale. Wan, AnimateDiff and MiniMax-H3 keys SHALL NOT change. Changing any included value SHALL regenerate the clip.

#### Scenario: changing the connectors regenerates
- **WHEN** an LTX-2.3 project changes `connectors`
- **THEN** its animated scenes are generated again

#### Scenario: Other keys are unchanged
- **WHEN** a Wan, AnimateDiff or MiniMax-H3 project runs after this change
- **THEN** its animate cache keys equal the ones computed before

### Requirement: The animate cache key includes model_audio only when it is set

For `minimax-h3` and `ltx-2.3` the animate hash SHALL include `model_audio=1` only when the setting is on, so the hashes of runs without it do not change. Turning it on or off SHALL re-run the animate phase of animated scenes and, through the clip's dependence on the animation, their clip phase; other scenes SHALL be unaffected.

#### Scenario: Existing caches stay valid
- **WHEN** a project that never set `model_audio` is run after this change
- **THEN** every animate hash equals the one stored before and nothing re-runs

#### Scenario: Turning it on
- **WHEN** `model_audio` changes from false to true
- **THEN** the animated scenes re-run `animate` and `clip`, and the others are cached

### Requirement: An artifact already produced for the same inputs is reused by any scene
When the narration or render phase of a scene has the same hash as an artifact already stored from any scene, the pipeline SHALL put that artifact at the scene's own path instead of running the phase, and SHALL report the phase as reused. The artifact SHALL be stored under its hash when the phase that made it succeeds. A stored artifact whose file is missing or whose size is zero SHALL be ignored and the phase run. `--force` SHALL bypass the store as it bypasses the manifest.

#### Scenario: a renamed scene
- **WHEN** a scene is renamed and its text, voice and speed are unchanged
- **THEN** its narration is reused from the store and the TTS engine is not run

#### Scenario: the same sentence in two scenes
- **WHEN** two scenes have the same narration text, voice and speed
- **THEN** the TTS engine runs once and the second scene reuses the result

#### Scenario: different inputs
- **WHEN** a scene's image inputs differ from every stored image in any hash input
- **THEN** the render phase runs

#### Scenario: a damaged stored file
- **WHEN** the store has the hash but the file is empty or missing
- **THEN** the phase runs and the store entry is replaced

### Requirement: Each input changes only the phases that depend on it
Changing one input of the audio hash SHALL leave the image and clip decisions as they were, and changing one input of the image hash SHALL leave the audio decision as it was, for every input those hashes include.

#### Scenario: voice changed
- **WHEN** only a scene's voice changes
- **THEN** the narration phase runs and the render phase is a hit

#### Scenario: resolution changed
- **WHEN** only a scene's resolution changes
- **THEN** the render phase runs and the narration phase is a hit

### Requirement: A layer, a track item and the stack have their own hashes

The video of a layer scene SHALL be a hit only when the content of its page, its song map, its length, its frame rate and its size are unchanged, whatever the track under it. An item of the video track SHALL be a hit only when the content of its file, its part of the file, its speed, its length, its scrim and the background colour, the size and the frame rate are unchanged, whatever the layers over it. The joined track SHALL be a hit when its items' hashes, their order, their transitions, the colour of the gaps and the length are unchanged. The stack SHALL be a hit when the joined track, the hash and place of each layer, their fades, the soundtrack and the size and frame rate are unchanged. `--force` SHALL bypass all of them and `--only` SHALL leave them as it leaves the other phases.

#### Scenario: Only the scrim changes
- **WHEN** the scrim of one item changes
- **THEN** that item, the joined track and the stack are built again and every layer is a hit

#### Scenario: Only a word changes
- **WHEN** the time of one word changes
- **THEN** that piece's layer and the stack are built again and every item and the joined track are hits

#### Scenario: Nothing changes
- **WHEN** the project runs again unchanged
- **THEN** every layer, item, the track and the stack are hits

### Requirement: The local scrims of an item are part of its hash

An item of the video track SHALL be a hit only when, besides what "A layer, a track item and the stack have their own hashes" lists, its `local_scrims` (the bands and their values) are unchanged. An item with none SHALL have the hash it had before this change.

#### Scenario: Only a local scrim changes
- **WHEN** the local scrim of one item changes
- **THEN** that item, the joined track and the stack are built again, every layer is a hit and the other items are hits

#### Scenario: No local scrims
- **WHEN** a project with a track and no `local_scrims` is built again after this change
- **THEN** every item is a hit
