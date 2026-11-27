# Pipeline events (JSON-lines contract)

When `veritmo` is run with `--json`, it writes exactly one JSON object per line on stdout for every pipeline event — this is the JSON-lines contract defined by the `events` capability spec (`openspec/changes/add-script-to-video-pipeline/specs/events/spec.md`). Without `--json`, the same events are rendered as human-readable text instead; the two modes carry the same information.

A front-end may rely on the following verbatim:

- Every line is a single, complete JSON object (no multi-line objects, no trailing commas, no comments).
- Every object has a non-empty `timestamp` (RFC3339Nano, UTC) and a `type` that is exactly one of the seven values below.
- Fields that don't apply to a given event are **omitted entirely** — never emitted as `null` or as a zero value. Check for key presence, not for a falsy value.
- `phase_end`'s `duration_ms` is the elapsed time since the matching `phase_start` for the same `scene_id` + `phase` pair.
- `pipeline_end`'s `duration_ms` is the elapsed time since the process started emitting events (i.e. total pipeline wall-clock time).

## Field table

| Field | Type | Present on | Meaning |
|---|---|---|---|
| `timestamp` | string (RFC3339Nano, UTC) | every event | When the event was emitted. |
| `type` | string | every event | One of: `pipeline_start`, `phase_start`, `phase_end`, `skipped`, `warn`, `error`, `pipeline_end`. |
| `scene_id` | string | `phase_start`, `phase_end`, `skipped`, `warn`, `error` | The scene the event concerns, or `_global` for events not scoped to one scene (e.g. the final concat, or a GPU-wide VRAM warning). Omitted on `pipeline_start`/`pipeline_end`. |
| `phase` | string | `phase_start`, `phase_end`, `skipped` | One of: `motion`, `tts`, `probe`, `render`, `animate`, `clip`, `track`, `stack`, `concat`, `bgm`, `mix`, `export`, `compose`, `synth`, `cover`. Omitted on `warn`/`error` (the message carries that context instead). |
| `duration_ms` | integer | `phase_end`, `pipeline_end` | Elapsed milliseconds since the matching start. Omitted everywhere else. |
| `load_ms` | integer | `phase_end` of a phase that runs `sd` (`render`, `animate`) | Milliseconds `sd` says it spent loading model weights, summed over every `sd` invocation of the phase (one per variant in images mode). `sd` writes its log to standard output and loads each component just before using it, printing one `loading tensors completed, taking X s` line per load; the field is the sum of those lines. It is **omitted** when `sd` printed none (another version, another format) and for every phase that does not run `sd`, and it never exceeds `duration_ms`. A render of a scene with its own picture or clip loads no model and carries none. |
| `artifact` | string | `phase_end` (when the phase produced a file), `pipeline_end` | Path to the file the phase/pipeline produced (e.g. a scene's `.wav`/`.png`/`.mp4`, or the final output video). Omitted when not applicable. |
| `message` | string | `warn`, `error` | Human-readable detail. Omitted on all other event types. |

## Event types, with one example object each

### `pipeline_start`

Emitted exactly once, as the first line of the stream.

```json
{"timestamp":"2026-10-06T10:00:00.000000000Z","type":"pipeline_start"}
```

### `phase_start`

Emitted when a phase begins running for a scene (not emitted for a phase that was a cache hit — see `skipped`).

```json
{"timestamp":"2026-10-06T10:00:00.120000000Z","type":"phase_start","scene_id":"scene_intro","phase":"tts"}
```

### `phase_end`

Emitted when a phase finishes successfully for a scene.

```json
{"timestamp":"2026-10-06T10:00:02.340000000Z","type":"phase_end","scene_id":"scene_intro","phase":"tts","duration_ms":2220,"artifact":"tmp/scene_scene_intro.wav"}
```

The final concat's `phase_end` uses `scene_id: "_global"` and `phase: "concat"`:

```json
{"timestamp":"2026-10-06T10:05:10.000000000Z","type":"phase_end","scene_id":"_global","phase":"concat","duration_ms":1840,"artifact":"out/demo.mp4"}
```

### `phase_end` for the `export` phase (images mode)

When `output.mode` is `images`, each scene runs `render` followed by `export` (final PNGs copied to `out/<project>/`). A single global `export` step then writes `out/<project>/manifest.json` (and, when `output.contact_sheet: true`, `contact_sheet.png`); it uses `scene_id: "_global"`. The event shape is identical to every other phase — no new fields or types.

```json
{"timestamp":"2026-10-06T10:00:03.000000000Z","type":"phase_end","scene_id":"scene_intro","phase":"export","duration_ms":12,"artifact":"out/demo/scene_intro.png"}
{"timestamp":"2026-10-06T10:00:03.100000000Z","type":"phase_end","scene_id":"_global","phase":"export","duration_ms":8,"artifact":"out/demo/manifest.json"}
```

> **Note:** Phases that are skipped for the current `output.mode` produce no events; consumers must not assume a fixed set of phases per scene. In `images` mode only `render` and `export` events appear (no `tts`, `probe`, `clip`, or `concat`); in `video` mode the `export` phase never appears. Likewise, a **silent scene** (one declaring `silence_seconds`) runs no narration phases, so it emits no `tts` or `probe` events at all — only `render` and `clip`. The `animate` phase runs only for scenes with `animate: true` in video mode, between `render` and `clip`; its `phase_end` carries the animated clip's path in `artifact`, and scenes that do not animate emit no `animate` events. The `motion` phase appears only when the model had to write the page of a motion-graphic scene or overlay that the project did not have yet (before `tts`; its `phase_end` carries the page's path in `artifact`, `motion/<scene>.html` or `motion/<scene>.overlay.html`); a scene whose pages are already stored emits none. A motion-graphic scene draws its video in its `render` phase (its `artifact` is the first frame) and its overlay inside `clip`.

> **Scenes with their own picture:** a scene with `image` runs no diffusion program: its `render` phase fits the picture to the resolution with ffmpeg (same phase name, `artifact` is `tmp/scene_<id>.png`), so the stream has the same shape.
>
> **Background music:** a video project with a `bgm` block writes the joined video to `tmp/video_nobgm.mp4` (the `concat` `artifact`) and then emits, with `scene_id: "_global"`, `bgm` (the soundtrack generated from `bgm.prompt`; `artifact` is the audio in `tmp/`; a cache hit emits `skipped`; absent when `bgm.file` is used) and `mix` (the final video; `artifact` is `output.file`). Projects without `bgm` emit neither.
>
> **Video track:** a video project with a `video_track` (see `docs/SCENES_YAML.md`) draws its motion scenes with `layer: true` as transparent videos and lays them over the track instead of concatenating clips. The `track` phase has one pair for each item of the track (`scene_id` is `track:<item id>`, never a scene's id, so an item cannot be mistaken for a scene of the same name; `artifact` is the built clip in `tmp/`) and one for the joined track (`scene_id` `track:_join`), and the `stack` phase one `_global` pair (`artifact` is the video before the soundtrack when the project has one, else the final video). A layer has a `render` pair and no `clip`; with a track there is no `concat`. Each of them is `skipped` when what it is made from did not change. A project without a track emits none of them. The event shapes do not change.

> **Music mode:** with `output.mode: music` the `scene_id` of an event is a **track ID** and the phases are `compose` (the language model writes the lyrics and audio codes; its `artifact` is the enriched request in `tmp/`; absent for a track that brings its own lyrics), `synth` (the audio; `artifact` is the file in `tmp/`) `cover` (only with `defaults.music.cover: auto`: the picture of the song, made with the project's image model; its `artifact` is the picture in `tmp/`) and `export` (the audio copied to `out/<project>/`, tagged, with the cover inside), plus one global `export` (`scene_id: "_global"`) whose artifact is `out/<project>/manifest.json`. A track with several variants emits one pair per phase, not one per variant. No `tts`, `probe`, `render`, `animate`, `clip` or `concat` events appear. The event shapes do not change.
>
> **Parallel scenes:** with `--gpu 0,1` (several GPUs) events of different scenes may **interleave** in the stream; each line is still written whole. Within one scene the events keep their order and every `phase_start` is followed by its `phase_end` or `error` for the same scene and phase. Correlate by `scene_id` and do not assume one scene finishes before the next starts. If one scene fails the others are stopped: a scene stopped in the middle of a phase emits an `error` whose message begins with `stopped:`. `pipeline_end` is still the last event. With one GPU or none the stream is strictly sequential, as before.

### `skipped`

Emitted instead of `phase_start`/`phase_end` when a phase was a cache hit and was not re-run.

```json
{"timestamp":"2026-10-06T10:00:00.050000000Z","type":"skipped","scene_id":"scene_intro","phase":"render"}
```

### `warn`

Emitted for a non-fatal condition. The pipeline continues.

```json
{"timestamp":"2026-10-06T10:00:00.080000000Z","type":"warn","scene_id":"_global","message":"free VRAM on GPU 1 is 1024 MB (threshold: 2048 MB)"}
```

### `error`

Emitted for a fatal condition. The process exits non-zero immediately after this event; no further events follow it.

```json
{"timestamp":"2026-10-06T10:01:15.500000000Z","type":"error","scene_id":"scene_middle","message":"render failed: runner: sd -m model.safetensors ...: exit status 1 (stderr: CUDA out of memory)"}
```

### `pipeline_end`

Emitted exactly once, as the last line of the stream, only on success.

```json
{"timestamp":"2026-10-06T10:05:10.010000000Z","type":"pipeline_end","artifact":"out/demo.mp4","duration_ms":310010}
```
