> **STATUS (2026-10-08): COMPLETED.** The two changes this plan asked for — `add-scene-importer`
> (the `veritmo import` subcommand, package `internal/project/importer`) and `add-camera-motion` (the
> `motion`/`look` presets, silent scenes with `silence_seconds`) — are implemented and archived in
> `openspec/changes/archive/2026-10-08-*`. Closed decisions: subcommand (not a separate binary),
> closed presets, silent scenes inside camera-motion, LoRA out of v1, concat -c copy with
> stream normalization. Current reference: `README.md` + `openspec/specs/`.

# TASK: Plan with OpenSpec a scene importer and a camera-motion phase for Veritmo

You are a senior Go engineer. Your job in this session is planning ONLY
(OpenSpec `propose` flow): do not implement anything until I run `/opsx:apply`.
Do not write Go code or final `ffmpeg` commands; decisions go in `design.md`
and the work goes in `tasks.md`.

## 0. Mandatory reading before proposing anything
Read and summarise in 10 lines what you understand of:
- `openspec/config.yaml` (context) and the current specs in `openspec/specs/`. If
  `add-script-to-video-pipeline` or `add-image-output-mode` are still unarchived in
  `openspec/changes/`, tell me before creating anything: they must be archived so that these
  changes can declare MODIFIED requirements on existing specs.
- `README.md` — current `scenes.yaml` schema, flags, content-hash cache,
  `--only`, `--keep-tmp`.
- `internal/` — locate exactly: (a) the validation that requires non-empty `prompt` and `tts_text`,
  (b) the function that builds the per-scene clip (`-loop 1 -tune stillimage`),
  (c) where and with which fields each scene's cache hash is computed. All three are
  touched in these changes; cite them with path and function.
- `testdata/demo_prompts.html` — input fixture for the importer (structure below).

## 1. Goal
Two independent features that are designed together because they share schema changes:
1. **Importer**: convert an external scene dataset (a JS `data` array inside an
   HTML file, or its JSON equivalent) into a valid `scenes.yaml` for the orchestrator.
2. **Camera motion**: the clip of each scene stops being a frozen image and supports
   motion (push-in/out, pan, handheld micro-shake) and an optional finish
   (grain, white balance), declared per scene in `scenes.yaml`.

## 2. Constraints (non-negotiable)
- Go >= 1.22, stdlib (`encoding/json`, `regexp`, `os/exec`, `context`). Zero Python.
- Subprocess pattern intact: each binary exits after its phase.
- The `--json` output in `docs/events.md` does not change shape. If a new phase emits events,
  propose them as a compatible extension and document them.
- Everything that affects the result of a clip (camera parameters, finish,
  global `negative_prompt`, `subject_prompt`, silences) GOES INTO the scene's cache hash.
  Otherwise, changing the zoom does not invalidate the cached clip. This is a requirement, not
  an optional task.
- The clip duration remains exactly that of the voice-over measured by `ffprobe`
  (or the declared one for silent scenes). `zoompan` is sized with `d = duration × fps`;
  `-tune stillimage` goes away when there is motion.
- Encode with `libx264` on CPU. No NVENC in v1: the bottleneck is `sd`, not the
  encode, and the render GPU is already pinned with `--gpu`.
- The importer is developed and tested ONLY against `testdata/demo_prompts.html` and an
  equivalent JSON in `testdata/`. Do not reference any other dataset.

## 3. Schema changes in `scenes.yaml` (declare as MODIFIED in `script-ingestion`)
These already exist since `add-image-output-mode` and are NOT redefined here, only used:
`defaults.negative_prompt`, `defaults.subject_prompt`, `defaults.lora_model_dir`,
`scene.lora`, `scene.variants`, `output.mode`. Read them in their specs. If any is
missing (because that change was trimmed), tell me before proposing.

Fields this plan DOES introduce:
- `scene.silence_seconds` (float, optional): silent scene. If present, `tts_text`
  may be empty and the scene duration is this value. If absent, `tts_text`
  remains mandatory.
- `scene.motion` (object, optional): `type` ∈ {`static`, `zoom_in`, `zoom_out`,
  `pan_left`, `pan_right`, `handheld`}, `intensity` 0.0–1.0. Defaults to `static`
  (= current behaviour, so existing projects do not break).
- `scene.look` (object, optional): `grain` 0.0–1.0, `white_balance` ∈
  {`neutral`, `warm`, `cool`, `fluorescent`}. No effect by default.
- (`scene.lora` may or may not exist depending on what was decided in `add-image-output-mode`;
  question 4 only decides whether the importer fills it from `lora_info`.)

## 4. Change A — `add-scene-importer`
New capability `scene-import`. Requirements to cover:
- Input: a JSON file with an array of entries `[id, title, lora_info, full_prompt]`,
  or an HTML file containing a `<script>` with `const data = [...]` in that same format.
  The HTML is treated as a thin extractor: locate the array, resolve only
  `${CONST}` interpolations of string constants defined in the same script
  (e.g. `${NEG}`), and from there on everything is JSON. No evaluating JavaScript.
- Per entry: `id` → `scene.id` (validate uniqueness); dialogue → `tts_text`, extracting the
  quoted text after `Dialogue:` or `Dialogue (<quien>):`; the `prompt` is
  `full_prompt` without the `Dialogue…` line, without `Negative: …` and without `Camera/style: …`
  (that line is NOT parsed into filters: `motion`/`look` are left unfilled or set to
  a default preset configurable by flag; mapping free-form style to filters is fragile and
  is out of scope).
- Entry without dialogue → `silence_seconds` with a configurable default value
  (e.g. 2.0) and empty `tts_text`.
- The `Negative: …` common to all entries → `defaults.negative_prompt` once;
  if it differs between entries, each one's goes to `scene.negative_prompt`.
- `lora_info` → `scene.lora` if question 4 approves it; otherwise it is ignored with a warning.
- Output: a `scenes.yaml` that passes the orchestrator's validation without hand edits,
  plus a report on stdout of what was mapped and what was discarded per entry.
- It does not run the pipeline. It is a conversion tool.

## 5. Change B — `add-camera-motion`
MODIFIED in `video-assembly` (clip phase) and in the capability that owns the cache
hash; MODIFIED in `voice-synthesis` and `media-telemetry` for silent scenes.
Requirements to cover:
- With `motion.type = static` and no `look`, the clip is identical to the current one
  (verifiable backward compatibility).
- Each `motion.type` produces continuous motion over the whole clip duration,
  with no jumps, and `intensity` scales the amplitude. `handheld` uses deterministic jitter
  (`sin`/`cos` expressions with a phase depending on the scene seed, so that it is
  reproducible and cacheable).
- `look.grain` adds proportional sensor noise; `look.white_balance` adjusts the
  temperature with a fixed preset per value.
- Silent scene: `voice-synthesis` does not invoke `piper`; `media-telemetry` takes
  `silence_seconds` as the duration; the clip carries a silent audio track so that the
  final concatenation does not fail due to mismatched streams.
- Document in `design.md` the concrete choice of filters (`zoompan`, `crop`+`scale`,
  `noise`, `eq`/`colorbalance`…), why, and the expected encode-time cost.

## 6. Decisions you must close WITH ME before writing specs
One by one, with your recommendation:
1. Importer as the subcommand `veritmo import <in> -o scenes.yaml` or a separate
   binary in `cmd/import`? My bias: subcommand, it shares validation.
2. `motion`/`look` as closed presets (above) or allow raw ffmpeg
   expressions per scene? My bias: presets in v1.
3. Silent scenes: do they go in change B or do they deserve their own change C
   (`support-silent-scenes`) that A and B depend on? Tell me based on the size you see.
4. Does `scene.lora` go into v1? It implies `--lora-model-dir` in `defaults` and the syntax
   in the prompt. If it does not, the importer only warns.
5. Does the final concatenation stay with `-c copy`? With different filters per scene
   the stream parameters may diverge; consider normalizing when generating each clip.

## 7. Expected OpenSpec process
1. Resolve point 6.
2. `/opsx:propose` for each agreed change, with `proposal.md`, `design.md`,
   `specs/<capability>/spec.md` (explicit ADDED/MODIFIED, SHALL requirements,
   technology-neutral WHEN/THEN scenarios) and `tasks.md` with small,
   verifiable tasks; include test tasks against `testdata/`.
3. `openspec validate <change> --strict --no-interactive` green on each one.
4. Same language as the existing artifacts.

## 8. Output of this session
Summary of the changes created, their location, dependencies between them, genuinely
open questions, and the `/opsx:apply` to start with. Do NOT implement anything.