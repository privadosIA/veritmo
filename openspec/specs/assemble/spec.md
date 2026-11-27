# assemble Specification

## Purpose
Defines how a scene's rendered image and synthesized audio become a timed video clip via ffmpeg, and how all scene clips are concatenated into the final video.

## Requirements

### Requirement: A scene's clip is timed by its probed audio duration, not by -shortest

`GenerateClip` SHALL loop the scene's image at the configured framerate and SHALL set the clip's duration (`-t`) to the scene's duration: the probed audio duration for a narrated scene, or `silence_seconds` for a silent scene. The duration source never changes the encode parameters.

#### Scenario: clip generation

- **WHEN** `GenerateClip` runs for a narrated scene
- **THEN** the ffmpeg invocation loops the scene's image at the configured framerate
- **AND** sets `-t` to the scene's probed duration

#### Scenario: silent scene clip duration

- **WHEN** `GenerateClip` runs for a silent scene with `silence_seconds: 2.5`
- **THEN** the ffmpeg invocation sets `-t` to 2.500000
- **AND** no probed audio duration is involved

### Requirement: Every clip uses identical encode parameters so concatenation never re-encodes

`GenerateClip` SHALL encode every clip — static or moving, narrated or silent — with the same stream parameters: `libx264` video at the declared framerate with `yuv420p` pixel format, and AAC audio at 192k, 44100 Hz, 2 channels. `ConcatClips` SHALL join them with the concat demuxer and encode the result once: `libx264` video (`yuv420p`, constant quality `-crf 18`) and continuous AAC 192k, 44100 Hz, 2 channels. The clips are not copied into the result, because a stream made of independently encoded clips is not one the browsers agree on: the AAC clips each start with their own encoder delay, which leaves overlapping and duplicated audio timestamps at every join and stalls Safari and QuickTime, and Firefox does not decode the copied video at all (the clock runs and the picture stays on the first frame). The joined file SHALL carry its index at the start (`-movflags +faststart`) so a browser can play it before it has arrived. The picture of an animated clip SHALL be brought to the normal (limited) colour range, never left as the full-range `yuvj420p` of the animation file, because many players refuse that as H.264. Motion and look filters change pixel content only, never stream parameters; a silent scene's generated audio track SHALL use the identical AAC parameters.

#### Scenario: concat uses stream copy

- **WHEN** `ConcatClips` runs
- **THEN** the ffmpeg invocation uses the concat demuxer and encodes `libx264`/`yuv420p` video and AAC audio at 192k/44100/stereo, with `-movflags +faststart`

#### Scenario: mixed project concatenates without re-encode

- **WHEN** a project mixes static scenes, moving scenes, and silent scenes
- **THEN** every generated clip carries one `libx264`/`yuv420p` video stream at the declared framerate and one AAC 192k/44100/stereo audio stream
- **AND** the final concat succeeds with no stream-mismatch error

#### Scenario: the joined video decodes in every browser

- **WHEN** the clips of a project are joined
- **THEN** the result is one `libx264` stream whose frames a decoder that starts from the beginning can read through to the end, as Firefox does

#### Scenario: the joined audio is continuous

- **WHEN** the clips of a project are joined
- **THEN** the audio packets of the result have regular timestamps, with no duplicated or overlapping ones at the joins

#### Scenario: an animation is in the normal colour range

- **WHEN** a clip is built from an animation file in full range (`yuvj420p`)
- **THEN** the clip is `yuv420p` with the normal (limited) range

### Requirement: The concat list escapes paths safely
`BuildConcatListContent` SHALL escape single quotes in each clip path so the concat demuxer parses the generated list file correctly.

#### Scenario: a clip path contains a single quote
- **WHEN** `BuildConcatListContent` is given a path containing a single quote
- **THEN** the generated list file escapes that quote so the concat demuxer parses the path correctly

### Requirement: Cleanup preserves manifest-referenced artifacts unless --keep-tmp is set
`CleanupTmp` SHALL leave the tmp directory untouched when `keepTmp` is true, and SHALL otherwise remove only the files not referenced by the given cache manifest.

#### Scenario: keep-tmp set
- **WHEN** `CleanupTmp` is called with `keepTmp=true`
- **THEN** the tmp directory and its contents are left untouched

#### Scenario: keep-tmp not set
- **WHEN** `CleanupTmp` is called with `keepTmp=false` and a manifest listing some artifacts
- **THEN** files not referenced by the manifest are removed
- **AND** files referenced by the manifest are preserved

### Requirement: Motion presets animate the still over the clip's full duration

When a scene's motion type is not `static`, `BuildClipArgs` SHALL add a video filter that produces **continuous, jump-free** movement spanning exactly the clip's duration, and SHALL NOT pass `-tune stillimage`. When the motion type is `static` (or `motion` is absent), the invocation SHALL be identical to the current implementation, including `-tune stillimage`.

Preset semantics (exact filter construction is a design decision, recorded in design.md):

- `zoom_in` / `zoom_out`: a monotonic zoom whose total travel scales with `intensity`, dimensioned over `d = duration × fps` frames so the movement covers the whole clip.
- `pan_left` / `pan_right`: a horizontal drift across the frame whose travel scales with `intensity`.
- `handheld`: small deterministic jitter built from `sin`/`cos` expressions whose phase derives from the scene's resolved seed, so two runs of the same scene produce bit-identical filter arguments. `intensity` scales the jitter amplitude.

All motion filters SHALL keep the output resolution equal to the configured scene resolution.

#### Scenario: static motion is byte-identical to today

- **WHEN** a scene has `motion` absent or `motion.type: static` and no `look`
- **THEN** `BuildClipArgs` SHALL return exactly the argument list the current implementation produces, including `-tune stillimage`

#### Scenario: zoom_in produces a full-duration zoom

- **WHEN** a scene has `motion: {type: zoom_in, intensity: 0.5}`, a probed duration D, and framerate F
- **THEN** the ffmpeg invocation SHALL contain a zoom filter dimensioned over D×F frames
- **AND** SHALL NOT contain `-tune stillimage`
- **AND** the output resolution SHALL equal the configured resolution

#### Scenario: handheld jitter is deterministic per seed

- **WHEN** `BuildClipArgs` is called twice for the same scene with `motion.type: handheld` and the same resolved seed
- **THEN** both invocations SHALL contain byte-identical filter arguments
- **AND** a different seed SHALL produce a different jitter phase

### Requirement: Look presets add grain and white balance as fixed mappings

When a scene declares `look`, `BuildClipArgs` SHALL append the corresponding filters after any motion filter:

- `grain` g > 0 adds a noise filter whose strength is proportional to g; g = 0 adds nothing.
- `white_balance` ≠ `neutral` adds a fixed color-temperature adjustment per preset value (`warm`, `cool`, `fluorescent`); `neutral` adds nothing.

The mapping from preset value to filter parameters SHALL be a fixed table (no per-scene free-form parameters), so identical config always yields identical filter arguments.

#### Scenario: grain scales with its value

- **WHEN** two scenes declare `look.grain: 0.2` and `look.grain: 0.8`
- **THEN** both invocations SHALL contain a noise filter
- **AND** the 0.8 scene's noise strength parameter SHALL be greater than the 0.2 scene's

#### Scenario: neutral look adds no filter

- **WHEN** a scene declares `look: {grain: 0.0, white_balance: neutral}`
- **THEN** the ffmpeg invocation SHALL contain no noise or color-temperature filter

### Requirement: Silent scenes carry a generated silent audio track

For a silent scene, `BuildClipArgs` SHALL replace the WAV input with a generated silent audio source (`anullsrc` at 44100 Hz stereo) encoded with the same AAC parameters as narrated clips, and the clip's duration SHALL be `silence_seconds`. The resulting clip SHALL have the same stream layout (one video + one audio stream) as a narrated clip.

#### Scenario: silent clip has a real audio stream

- **WHEN** `GenerateClip` runs for a silent scene
- **THEN** the ffmpeg invocation uses a silent audio source instead of a WAV file input
- **AND** the output clip contains an AAC 192k/44100/stereo audio stream of the declared duration
- **AND** concatenating it with narrated clips via `-c copy` succeeds

### Requirement: An animated scene's clip is built from its animated video

When a scene has an animated video, `GenerateClip` SHALL use it as the video input instead of the looped still image. The video SHALL be slowed down by the factor `min(2, D / S)` when the scene duration `D` exceeds the animated clip's length `S`, then looped until `D` is covered, and cut at `D`. When `D` is shorter than `S` it SHALL be cut at `D` without speed change. The video SHALL be scaled to cover the project resolution and cropped to it, and converted to the declared framerate. The audio, the encode parameters (`libx264`, `yuv420p`, AAC 192k/44100/stereo) and the `-t` duration rule SHALL be identical to those of still-image clips so concatenation with `-c copy` keeps working. Look filters (grain, white balance) SHALL apply; motion presets SHALL not.

#### Scenario: narration longer than the animation
- **WHEN** the animated clip lasts 4 s and the narration lasts 5 s
- **THEN** the ffmpeg invocation slows the video by 1.25, loops it and cuts at 5 s
- **AND** the output has the same stream parameters as a still-image clip

#### Scenario: narration much longer than the animation
- **WHEN** the animated clip lasts 4 s and the narration lasts 20 s
- **THEN** the slowdown is capped at 2 and the video is looped to fill 20 s

#### Scenario: narration shorter than the animation
- **WHEN** the animated clip lasts 4 s and the narration lasts 3 s
- **THEN** the clip is cut at 3 s with no speed change

#### Scenario: mixed project concatenates without re-encode
- **WHEN** a project mixes animated, static and silent scenes
- **THEN** the final concat succeeds

### Requirement: Background music is mixed under the narration

When a project has `bgm`, after the concat the pipeline SHALL produce `output.file` with one ffmpeg call over the joined video and the soundtrack: video copied without re-encoding; the soundtrack looped to the video length, lowered to `volume`, faded in and out over `fade_seconds`, compressed by the narration when `ducking` is on, mixed with the narration with `amix`, normalized with `loudnorm`, and encoded as AAC 192k, 44100 Hz, stereo. Without `bgm` the concat SHALL write `output.file` directly, as before.

#### Scenario: Mix arguments
- **WHEN** a project has `bgm` with volume 0.3
- **THEN** the ffmpeg call has the video with `-c:v copy`, `volume=0.3`, `sidechaincompress`, `amix` and `loudnorm`

#### Scenario: No ducking
- **WHEN** `ducking` is false
- **THEN** the call has no `sidechaincompress`

#### Scenario: No bgm
- **WHEN** a project has no `bgm`
- **THEN** its ffmpeg calls are exactly as before

### Requirement: A given picture is fitted to the resolution

For a scene with its own picture the pipeline SHALL produce the scene image with one ffmpeg call that scales the picture to cover the project resolution, crops it to exactly that size and writes one frame. The scene's clip, motion, look and animation SHALL use that image as for a generated one.

#### Scenario: Fit arguments
- **WHEN** a 4000x3000 photo is fitted for 1280x720
- **THEN** the call scales with `force_original_aspect_ratio=increase`, crops to 1280:720 and writes one frame

### Requirement: Rotation presets turn the picture without empty corners

`rotate_cw` and `rotate_ccw` SHALL rotate the frame about its center by an angle growing linearly from 0 to `5 + 40*intensity` degrees over the clip, clockwise or counter-clockwise, after a single zoom large enough for the largest angle to leave no empty corner, and SHALL keep the output at the configured resolution and without `-tune stillimage`. The same inputs SHALL give identical arguments.

#### Scenario: Clockwise
- **WHEN** a 1280x720 scene lasts 6 s with `rotate_cw` at intensity 0.5
- **THEN** the filter rotates by an angle expression reaching 25 degrees at 6 s, crops to 1280x720 and the output keeps that resolution

#### Scenario: Counter-clockwise
- **WHEN** the preset is `rotate_ccw`
- **THEN** the angle expression is negative

#### Scenario: Other presets untouched
- **WHEN** a scene uses zoom, pan, handheld or static
- **THEN** its arguments are exactly as before

### Requirement: A scene image of another size is fitted to the project's resolution

When a still scene's image has a size different from the project's resolution (because the scene rendered at its own resolution), the clip phase SHALL scale it to cover the project's resolution and crop it to exactly that size before any motion or finishing filter, so the clip has the project's size and the picture is never stretched. When the sizes are equal the filter chain SHALL be unchanged. An animated scene SHALL keep being fitted by cover and crop to the project's resolution.

#### Scenario: a larger image
- **WHEN** a scene rendered at 1280x704 is assembled in a 1024x576 project
- **THEN** the clip is 1024x576 and the picture is cropped, not stretched

#### Scenario: same size
- **WHEN** the scene image has the project's size
- **THEN** the ffmpeg arguments are the same as before this feature

### Requirement: An animated clip may carry the model's ambience under its narration

When a scene has an ambience WAV (from the animate phase with `model_audio`), `GenerateClip` SHALL add it as an extra last input and mix it into the clip's single audio stream with `amix` (`normalize=0`, `duration=first`) at a fixed gain of -18 dB, starting at the scene's start and played once. When the scene has narration (it is not silent), the ambience SHALL be compressed by the narration with `sidechaincompress`; a silent scene gets the ambience without ducking. The audio SHALL still be AAC 192k/44100/stereo, last exactly the scene's `-t` duration and map as a single stream, so concatenation with `-c copy` keeps working; this SHALL also hold when an overlay is drawn. The ambience SHALL NOT be reversed, looped or time-stretched. A scene without an ambience WAV SHALL produce exactly the arguments it produced before. A scene that uses its own clip, a trimmed animation or a motion graphic SHALL NOT get ambience.

#### Scenario: Narrated scene
- **WHEN** an animated scene with narration has an ambience WAV
- **THEN** the ffmpeg call has the WAV as its last input, `volume=-18dB`, `sidechaincompress`, `amix` and one audio output

#### Scenario: Silent scene
- **WHEN** a silent animated scene has an ambience WAV
- **THEN** the call has `volume=-18dB` and `amix` but no `sidechaincompress`

#### Scenario: No ambience
- **WHEN** a scene has no ambience WAV
- **THEN** its ffmpeg arguments are identical to those before this requirement

#### Scenario: Ambience shorter than the scene
- **WHEN** the scene lasts 10 s and the ambience 2 s
- **THEN** the ambience plays once and the rest of the clip carries only the narration (or silence)

### Requirement: The video track is built from its items

The `track` phase SHALL build each item of the project's video track from its file as a clip of the item's length at the project's size and frame rate with no sound: the part of the file (`clip_start` to `clip_end`) cut exactly, retimed by `clip_speed` (slowed by at most 2x and then repeated when shorter than the item, cut when longer), scaled and cropped to cover the frame, and darkened or lightened by `scrim` of the design's background colour. It SHALL then join the items in order into one video as long as the project, with a crossfade where an item has `transition_seconds` and touches the one before, a cut where it touches without one, and a solid frame of the design's background colour in the gaps and before the first and after the last item. The result SHALL have one video stream and no audio stream.

#### Scenario: Two touching items with a fade
- **WHEN** items from 0 to 6 and from 6 to 12 have `transition_seconds: 0.5` on the second
- **THEN** the track is 12 seconds long and the picture dissolves for 0.5 s around second 6

#### Scenario: A clip shorter than its item
- **WHEN** an item of 6 seconds uses a clip of 2 seconds
- **THEN** the clip is slowed by at most 2x and repeated until the item ends

#### Scenario: A gap
- **WHEN** there is no item from 20 to 30
- **THEN** those seconds are the colour of the design's background

### Requirement: The layers are stacked over the track with the song

The `stack` phase SHALL lay the transparent video of each layer scene over the track at the second of the output where the scene starts, keeping the alpha of the layers (the VP9 decoder that keeps it), with a dissolve of the alpha where two layers overlap by a crossfade, and SHALL write one video as long as the project with the soundtrack of the project (`bgm`) as its only sound. The scenes that are not layers SHALL take their place in the output as they do today. The stack SHALL be one graph over all the layers of the project, and SHALL encode the result once, with the parameters every other final video has.

#### Scenario: Two layers dissolving
- **WHEN** two layer scenes overlap by 0.4 s
- **THEN** over those 0.4 s the first fades out and the second fades in over the same track

#### Scenario: The song is the only sound
- **WHEN** the clips of the track have sound
- **THEN** the final video has the song's sound only

#### Scenario: A long song
- **WHEN** the project has 40 layers
- **THEN** the stack finishes in one pass with the same kind of picture as with a few

### Requirement: A track item can have a local scrim over the band of the verse

An item of the video track with `local_scrims` SHALL be built with, over the item's own scrim, a strip of the design's background colour for each entry `{band, scrim}`: `band` is `top`, `center` or `bottom`; the strip covers the full width and, in the rows of that band as the verse takes them (`top` from 6 % to 22 % of the height, `center` from 42 % to 58 %, `bottom` from 78 % to 94 %), has the opacity `scrim`, and fades out linearly to nothing over 6 % of the height above and below, never outside the frame. The strip SHALL be drawn with the same function by the build, by `GET /api/music-motion/pieces/{id}/frame` and by the page a dashboard draws from the preview, so the three show the same picture. An item with no `local_scrims` SHALL be built exactly as before, byte for byte in its command.

#### Scenario: The band is tinted and the rest is not
- **WHEN** an item of a black clip has `local_scrims: [{band: bottom, scrim: 0.8}]` over a white design colour
- **THEN** the frames of the built item are close to white in the rows from 78 % to 94 % and black above 70 % of the height

#### Scenario: Soft edges
- **WHEN** the same item is looked at from the top of its strip to the middle of it
- **THEN** the opacity grows without a step, from nothing to 0.8

#### Scenario: Without local scrims
- **WHEN** an item has none
- **THEN** its build command is the one it had before this change
