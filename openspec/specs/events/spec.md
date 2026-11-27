# events Specification

## Purpose
Defines the structured progress event stream emitted on stdout, so a human or a future front-end can follow pipeline execution without parsing ad hoc log lines.

## Requirements

### Requirement: Every event carries a timestamp and a type
Every event the Emitter produces SHALL carry a non-empty RFC3339Nano UTC timestamp and exactly one of the seven defined event types.

#### Scenario: any event
- **WHEN** any Emitter method is called
- **THEN** the resulting event has a non-empty RFC3339Nano UTC timestamp
- **AND** has one of the seven defined types

### Requirement: --json selects JSON-lines output; its absence selects human-readable text
The Emitter SHALL write exactly one JSON object per line when created in JSON mode, and SHALL write human-readable, non-JSON text lines otherwise.

#### Scenario: json mode
- **WHEN** the Emitter is created with `jsonMode=true`
- **THEN** each event is written as exactly one JSON object per line

#### Scenario: text mode
- **WHEN** the Emitter is created with `jsonMode=false`
- **THEN** each event is written as a human-readable line, never as parseable JSON

### Requirement: phase_end reports the elapsed duration since the matching phase_start
`PhaseEnd` SHALL report, in `duration_ms`, the elapsed time since the matching `PhaseStart` call for the same scene and phase.

#### Scenario: matched start/end
- **WHEN** `PhaseStart` and then `PhaseEnd` are called for the same scene and phase
- **THEN** the emitted `phase_end` event's `duration_ms` reflects the elapsed time between the two calls

### Requirement: Omitted fields are absent from JSON output, not emitted as null or zero
Fields that do not apply to a given event (e.g. `duration_ms` or `artifact` on a `skipped` event) SHALL be omitted from the JSON object entirely.

#### Scenario: skipped event
- **WHEN** `Skipped` is called
- **THEN** the emitted JSON object has no `duration_ms` field and no `artifact` field

### Requirement: pipeline_end reports the final video's path and total elapsed duration
`Done` SHALL emit a `pipeline_end` event whose `artifact` is the final output path and whose `duration_ms` reflects the elapsed time since the Emitter was created.

#### Scenario: pipeline completes
- **WHEN** `Done` is called with the final output path
- **THEN** the emitted `pipeline_end` event's `artifact` is that path
- **AND** its `duration_ms` reflects the elapsed time since the Emitter was created

### Requirement: Skipped phases are silent

In `images` output mode the `Emitter` (package `veritmo/internal/platform/events`) SHALL emit no `phase_start`, `phase_end`, or error events for the phases `tts`, `probe`, `clip`, or `concat`. The resulting JSON-lines stream SHALL remain a valid sequence under the existing contract documented in `docs/events.md`: every emitted `phase_start` event SHALL be paired with a subsequent `phase_end` or error event for the same `scene_id` and `phase`, and no event field, type, or ordering rule SHALL change. A consumer that validates the stream against the current `docs/events.md` schema (field names, types, pairing invariant) SHALL parse an images-mode stream without error, regardless of which phases are absent.

#### Scenario: Images-mode run emits only render and export phase pairs per scene

- **WHEN** the orchestrator processes a scene in `images` mode with `variants == 2`
- **THEN** the event stream for that scene SHALL contain exactly:
  - one `phase_start` with `phase: "render"`, `scene_id: "<scene_id>"`
  - one `phase_end` with `phase: "render"`, `scene_id: "<scene_id>"`
  - one `phase_start` with `phase: "export"`, `scene_id: "<scene_id>"`
  - one `phase_end` with `phase: "export"`, `scene_id: "<scene_id>"`
- **AND** the stream SHALL contain zero events with `phase` equal to `"tts"`, `"probe"`, `"clip"`, or `"concat"` for that scene
- **AND** the stream SHALL contain zero events with `scene_id: "_global"` and `phase: "concat"`

#### Scenario: Consumer written against current docs/events.md parses an images-mode stream without errors

- **WHEN** a JSON-lines consumer validates each line against the existing event shape (`phase` is a non-empty string, `scene_id` is a non-empty string, `status` is one of `"start"`, `"end"`, `"error"`, `ts` is an RFC 3339 timestamp, `detail` is a string or absent) and checks the pairing invariant (every `phase_start` is followed by a `phase_end` or error for the same `scene_id`/`phase` before the next `phase_start` for that scene)
- **AND** the input stream is produced by an images-mode run of the orchestrator
- **THEN** the consumer SHALL report zero validation errors
- **AND** the consumer SHALL NOT require the presence of `tts`, `probe`, `clip`, or `concat` phase events

### Requirement: export is a new phase name using existing event shapes

The `export` phase SHALL be emitted using the identical `phase_start` / `phase_end` event structure already defined for `tts`, `probe`, `render`, `clip`, and `concat`. Specifically:

- `phase_start` SHALL carry `phase: "export"`, `scene_id: "<scene_id>"`, `status: "start"`, and `ts`.
- `phase_end` SHALL carry `phase: "export"`, `scene_id: "<scene_id>"`, `status: "end"`, `ts`, and `detail` set to the resolved output path of the exported artefact for that scene (the PNG file path for single-variant scenes, or the last variant's PNG path for multi-variant scenes).
- If a single project-level export step (e.g. writing `manifest.json` or generating `contact_sheet.png`) is emitted as a global event, it SHALL use `scene_id: "_global"`, `phase: "export"`, and `detail` set to the manifest or contact-sheet path.
- No new fields, types, or event categories SHALL be introduced. The `export` value is an additive extension to the existing closed set of phase names.
- During the apply step, `docs/events.md` SHALL be updated to list `export` in the phase table and to add the note: *"Phases that are skipped for the current `output.mode` produce no events; consumers must not assume a fixed set of phases per scene."* This documentation change is additive (a new row and a note) and SHALL NOT alter any existing row, field definition, or example.

#### Scenario: Export start/end pair per scene with artifact path

- **WHEN** the orchestrator completes the export step for scene `intro` (single variant) in images mode
- **THEN** the event stream SHALL contain, in order:
  - `{"phase":"export","scene_id":"intro","status":"start","ts":"2025-01-15T10:30:01Z"}`
  - `{"phase":"export","scene_id":"intro","status":"end","ts":"2025-01-15T10:30:02Z","detail":"out/my_project/intro.png"}`
- **AND** both events SHALL conform to the existing event shape (no additional or renamed fields)

#### Scenario: Unknown-phase tolerance

- **WHEN** a consumer written before this change encounters a `phase_start` or `phase_end` event with `phase: "export"`
- **AND** the consumer's logic is to process known phases (`tts`, `probe`, `render`, `clip`, `concat`) and silently ignore any other `phase` value
- **THEN** the consumer SHALL skip the `export` events without error
- **AND** the consumer SHALL lose no information required by the existing contract (all `tts`/`probe`/`render`/`clip`/`concat` events, if present, are still correctly parsed)
- **AND** no data corruption or state inconsistency SHALL result from the ignored events

### Requirement: animate is a new phase name using existing event shapes

The `animate` phase SHALL use the same `phase_start` / `phase_end` shapes as the other phases, with `phase: "animate"` and the scene ID; `phase_end` carries the animated clip's path in `detail`. No field, type or ordering rule changes. Scenes that do not animate, and `images` mode, SHALL emit no `animate` events. `docs/events.md` SHALL list `animate` in its phase table.

#### Scenario: animated scene event order
- **WHEN** an animated scene runs in video mode
- **THEN** its events follow `tts`, `probe`, `render`, `animate`, `clip`, each start paired with an end or error

#### Scenario: skipped by cache
- **WHEN** the animate phase is a cache hit
- **THEN** it follows the existing skipped-phase behaviour for cached phases

### Requirement: Events of different scenes may interleave

When a run uses several GPUs, events of different scenes MAY interleave in the stream. Within one scene, events SHALL keep their order and every `phase_start` SHALL still be followed by its `phase_end` or error for the same scene and phase before that scene's next `phase_start`. Each line SHALL be written whole (never interleaved inside a line). Consumers SHALL correlate by `scene_id` and not assume that one scene finishes before the next starts. `pipeline_end` SHALL still be the last event. `docs/events.md` SHALL state this.

#### Scenario: two scenes at once
- **WHEN** a run on GPUs `0,1` processes two scenes
- **THEN** the stream may show the first scene's `render` start before the second scene's `tts` ends
- **AND** for each scene the phases appear in pipeline order with each start paired to its end

#### Scenario: single GPU stream is unchanged
- **WHEN** a run uses one GPU or none
- **THEN** all events of a scene precede all events of the next scene, as today

### Requirement: compose and synth are new phase names using existing event shapes

The `compose` and `synth` phases SHALL be emitted with the same `phase_start` / `phase_end` / `skipped` / `error` shapes as the other phases, with `scene_id` carrying the track ID. `phase_end` SHALL carry the produced artifact in `artifact` (the enriched request file for `compose`, the audio file for `synth`). The `export` phase in music mode SHALL use `_global` for the manifest step as in images mode. No field or event type SHALL be added. Phases not run in a mode SHALL produce no events. `docs/events.md` SHALL list `compose` and `synth` in its phase table.

#### Scenario: Composed track
- **WHEN** a track that uses the language model runs
- **THEN** the stream contains `compose`, `synth` and `export` pairs for its ID, in that order

#### Scenario: Track with own lyrics
- **WHEN** a track runs without the language model
- **THEN** the stream contains no `compose` event for it

#### Scenario: Older consumer
- **WHEN** a consumer that ignores unknown phases reads a music stream
- **THEN** it loses no information about the phases it knows

### Requirement: The soundtrack has its own phases

For a video project with `bgm`, the global step SHALL emit `phase_start`/`phase_end` for `bgm` (only when the soundtrack is generated or found in cache: a cache hit emits `skipped`) and for `mix`, after `concat`. Projects without `bgm` SHALL emit neither.

#### Scenario: Generated
- **WHEN** a run generates the soundtrack
- **THEN** `bgm` and `mix` appear for `_global` after `concat`

#### Scenario: From a file
- **WHEN** `bgm.file` is used
- **THEN** only `mix` appears

### Requirement: phase_end may report the model load time of the phase
When a phase runs `sd` and the time `sd` spent loading its model can be read from its output, `phase_end` SHALL report it in `load_ms`, summed over every `sd` invocation of the phase. When it cannot be read, or the phase does not run `sd`, the field SHALL be omitted. `load_ms` SHALL NOT exceed `duration_ms`.

#### Scenario: a render with a readable load time
- **WHEN** a render phase runs `sd` and its output states how long the model took to load
- **THEN** the `phase_end` event has `load_ms` with that time
- **AND** `duration_ms` is unchanged

#### Scenario: several invocations in one phase
- **WHEN** a phase runs `sd` once per variant
- **THEN** `load_ms` is the sum of the load times of all of them

#### Scenario: the time cannot be read
- **WHEN** the output of `sd` has no load time, for example in a different `sd` version
- **THEN** the `phase_end` event has no `load_ms` field
- **AND** the phase still succeeds

### Requirement: track and stack are new phase names using existing event shapes

The `track` and `stack` phases SHALL be emitted with the same `phase_start` / `phase_end` / `skipped` / `error` shapes as the other phases. `track` SHALL carry `track:<item id>` in `scene_id` for the build of an item and `track:_join` for the join of the track, so an item is never mistaken for a scene of the same name, and `phase_end` SHALL carry the produced video in `artifact`; `stack` SHALL be `_global` and its `phase_end` SHALL carry the video before the soundtrack when the project has one. No field or event type SHALL be added, a project with no video track SHALL produce neither phase, and `docs/events.md` SHALL list both in its phase table. The name `compose` SHALL NOT be used for them: music mode already emits it.

#### Scenario: A project with a track
- **WHEN** a project with two items and three layers runs
- **THEN** the stream has a `track` pair with `scene_id` `track:<id>` for each item, one with `track:_join` for the joined track and a `stack` pair with `_global`, after the layers

#### Scenario: A project without a track
- **WHEN** an ordinary video project runs
- **THEN** the stream has no `track` and no `stack` event
