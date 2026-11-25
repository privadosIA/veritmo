> **STATUS: HISTORICAL.** This was the planning prompt of the music mode, which is implemented (see [MUSIC_MODE.md](MUSIC_MODE.md) and `openspec/specs/music/`). It is kept for reference.

# TASK: Plan a "music" output mode for Veritmo (acestep.cpp) with OpenSpec

You are a senior Go + Angular engineer. Your job in this session is ONLY planning
(OpenSpec `propose` flow): do not implement anything until I run `/opsx:apply`.

## 0. Mandatory reading before proposing anything
Read and summarise in 10 lines what you understand of:
- `openspec/config.yaml` and `openspec/specs/` (source of truth; as of today there are no active
  changes). The capabilities you will touch are `config`, `cli`, `cache`,
  `events`, `export`, `runner`, `parallel-scenes`, `environment-status`,
  `model-management`, `scene-editor`, `image-gallery`, `model-manager-view`,
  `run-monitor`. If you find an active change in `openspec/changes/`, tell me before
  continuing.
- `README.md` — especially "Images mode", "Using both GPUs" (memory reservation in
  `internal/platform/sched`), "Models, voices and LoRAs" (catalogue and tabs) and the
  `scenes.yaml` reference.
- `docs/IMAGE_MODE.md` — it is the exact precedent for what you are going to do: a mode that
  runs a subset of phases, relaxes validation and exports with `manifest.json`.
- `docs/events.md` — the JSON-lines contract. The `phase` enum will have to grow:
  declare it as MODIFIED in `events`, do not change it silently.
- `internal/project/pipeline/pipeline.go` (`PhasePlan`, `RequiredBinaries`, `runImages`),
  `internal/video/render/render.go` (how `sd` is wrapped: centralised `Flags`, `BuildArgs`,
  `runner.CommandRunner`), `internal/project/export/export.go` (manifest) and
  `internal/server/catalog.json` (shape of a catalogue entry).
- `scripts/build-sd.sh` — template for the new engine's build script.

## 1. Goal
That a `scenes.yaml` with `output.mode: music` produces **complete music tracks**
(vocals + instrumental, or instrumental) locally, without Python, with
[acestep.cpp](https://github.com/ServeurpersoCom/acestep.cpp): a C++17/GGML
implementation of ACE-Step 1.5 (MIT), native `ace-lm` and `ace-synth` binaries, GGUF models.
Same pattern as `sd` and `piper`: one subprocess per phase, the process dies and frees the VRAM.
It is the local alternative to the project's Mureka/Suno. Videos are not part of this plan.

## 2. Architecture constraints (non-negotiable)
- **Engine**: acestep.cpp invoked as a subprocess through `runner.CommandRunner`,
  with the flags centralised in a `var Flags` as `internal/video/render` does. NOT the HTTP
  `ace-server`: it keeps models resident in VRAM and would have to be managed
  as a daemon; it breaks the "process dies, VRAM free" principle. No cgo, no bindings,
  no Python at runtime.
- **Two new phases per scene**, in their own `internal/music/music` package:
  - `compose` — `ace-lm --models <dir> --request <json>`: from caption,
    lyrics and metadata (bpm, duration, key, time signature, vocal language) it generates the
    `request0.json … requestN-1.json` files enriched with final lyrics and `audio_codes`.
  - `synth` — `ace-synth --models <dir> --request <requestK.json>`: DiT + VAE,
    returns stereo 48 kHz MP3 or WAV.
  - Then the existing `export` phase (reused, not duplicated) copies to
    `out/<project>/<id>.mp3|wav` and writes `manifest.json`.
- **acestep.cpp JSON contract** (`docs/ARCHITECTURE.md` of the upstream repo, field →
  default): `caption ""` (the only required one), `lyrics ""`, `bpm 0`, `duration 0`,
  `keyscale ""`, `timesignature ""`, `vocal_language ""`, `seed -1`,
  `lm_batch_size 1`, `synth_batch_size 1`, `lm_temperature 0.85`, `lm_cfg_scale 2.0`,
  `lm_top_p 0.9`, `lm_top_k 0`, `lm_negative_prompt ""`, `use_cot_caption true`,
  `inference_steps 0` (auto), `guidance_scale 0.0`, `shift 0.0`, `solver "euler"`,
  `task_type "text2music"`, `output_format "mp3"`, `mp3_bitrate 128`,
  `synth_model ""`, `lm_model ""`, `adapter ""`, `adapter_scale 1.0`. The orchestrator
  writes that JSON; do not invent fields.
- **Two unknowns that are closed with a spike, not with assumptions.** The first
  task of `tasks.md` must be a manual spike documented in `design.md`: (a) name
  and exact location of the files that `ace-synth` writes (upstream shows
  `request00.mp3` for `request0.json`; we must confirm whether it writes them next to the JSON
  or in cwd, in order to rename them deterministically to the cache path);
  (b) whether or not `synth_model`/`lm_model` carry the `.gguf` suffix (the docs are
  inconsistent); (c) whether the DiT-only route (`ace-synth` without `ace-lm`, caption and lyrics
  verbatim) works and with which fields. Until the spike is closed, the implementation
  tasks for the wrapper stay blocked.
- **GPU**: same `--gpu`; `CUDA_VISIBLE_DEVICES` is honoured by the CUDA runtime even though
  acestep.cpp documents no variable of its own. Both phases count as
  GPU work for `parallel-scenes`: memory reservation in `internal/platform/sched`
  just like `sd` (size of the set's GGUFs × 1.15), with the LM and the DiT reserved
  separately because they are different processes.
- **Cache** (MODIFIED in `cache`): `compose` is cached by hash of
  (composed caption, lyrics, metadata, effective seed, `lm_*` parameters, LM file);
  `synth` by hash of (contents of the resulting `requestK.json`, DiT file, VAE
  file, `inference_steps`, `guidance_scale`, `shift`, `solver`, `output_format`,
  `mp3_bitrate`). Changing only synthesis parameters does NOT re-run the LM. `--force` and
  `--only` work the same as in the other modes.
- **Per-mode validation** (MODIFIED in `config`): in `music`, `prompt` is still
  required and acts as the caption; `tts_text`, `speed`, `silence_seconds`, `motion`,
  `look`, `animate`, `resolution`, `sampler`, `steps`, `fps`, `model`/`model_set` and
  `voice` are optional and ignored with a warning. `variants` is reused (seed+i, same
  rule as in `images`). `output.file` is not required.
- **Pre-flight** (`RequiredBinaries`): `music` requires `ace-lm` and `ace-synth` (only
  `ace-synth` if the DiT-only route is agreed as an option); it does not require `piper`, `sd`,
  `ffmpeg` or `ffprobe`.
- **Events**: new phases `compose` and `synth` added to the `docs/events.md` enum;
  same event shape, no new fields. `export` reuses `_global` as in
  images.
- **Installation**: `scripts/build-acestep.sh` (`--recurse-submodules` clone, CUDA
  build; on CachyOS/Arch `NVCC_CCBIN=g++-13` is needed as upstream documents;
  installs `bin/ace-lm` and `bin/ace-synth`) and download of the four GGUFs to
  `models/music/`: LM (`acestep-5Hz-lm-{0.6B,1.7B,4B}-Q8_0.gguf`), text encoder
  (`Qwen3-Embedding-0.6B-Q8_0.gguf`), DiT (`acestep-v15-{turbo,sft}-Q8_0.gguf`) and
  VAE (`vae-BF16.gguf`), from `huggingface.co/Serveurperso/ACE-Step-1.5-GGUF`.
  The scripts do not run themselves, just like the existing ones. Note: upstream only
  publishes the 2B DiTs; the XL (4B) ones from the official ACE-Step do NOT exist as GGUF, do not
  promise them.
- **Catalogue** (MODIFIED in `model-management`): a new `kind` for music with
  bundles (LM + encoder + DiT + VAE) that share files, as Krea 2 and
  IntoRealism already do; a **Music** tab in the Models screen; the environment shows
  `ace-lm`/`ace-synth` like `sd`/`piper`.
- Respect the repo conventions (Apache-2.0 "Copyright (c) 2026 Jesús Suero",
  `go test ./...`, `openspec validate --strict`, Angular conventions from
  `docs/PLAN_FRONTEND_PROMPT.md`: one component per folder with separate `.ts/.html/.scss`,
  `shared/`, `mo-table` with paginator `[4,10,20,50,100]`, theme tokens,
  Angular Material).
- The current CLI, server and UI do not change their behaviour in the `video` and `images`
  modes.

## 3. Candidate functional scope (validate it, cut whatever adds nothing in v1)
- **`scenes.yaml` schema**:
  - `output.mode: music` and `--mode music`.
  - `defaults.music:` with `models_dir` (default `models/music`), `lm_model`,
    `synth_model`, `use_lm` (true), `output_format` (`mp3`|`wav`), `mp3_bitrate`,
    `lm_temperature`, `lm_cfg_scale`, `lm_top_p`, `lm_top_k`, `inference_steps`,
    `guidance_scale`, `shift`, `solver`, `lm_negative_prompt`, `vocal_language`.
  - Per scene (= track): `prompt` (caption), `lyrics` (multiline with `[Verse]`/`[Chorus]`
    tags following the ACE-Step guide), `duration_seconds`, `bpm`,
    `keyscale`, `timesignature`, `instrumental: true|false` (decide in the spike whether
    it translates to empty lyrics, to a tag or to `lm_negative_prompt`), `seed`,
    `variants`. `defaults.subject_prompt` / `defaults.negative_prompt` do NOT apply
    here (they are for images); if you think a "global style" deserves to exist, propose it
    as `defaults.music.style_prefix` and justify it.
- **Export**: `out/<project>/<id>.mp3` (or `<id>_v<N>.mp3` with variants) and an extended
  `manifest.json` (MODIFIED in `export`) with the final caption, the final lyrics returned by
  the LM, effective seed, resulting bpm/duration/key, models and
  parameters: everything needed to reproduce the track.
- **UI** (same criterion as images mode):
  - Mode selector with a third option `music`; the editor hides/dims what does not
    apply and shows the track fields (lyrics in a large textarea, musical
    metadata, `variants`).
  - The results view in music mode is a **list of tracks** with an audio player,
    returned lyrics and metadata, reading `manifest.json` (reuse the
    gallery structure; decide whether it is the same feature or `tracks/`), with the
    actions "fijar esta seed" (pin this seed) and "regenerar esta pista" (regenerate this track) (= `--only`).
  - Run monitor: the `compose` and `synth` phases appear in `phase-strip`.
  - Models: a Music tab with the catalogue bundles; Environment: `ace-lm`,
    `ace-synth`.
  - Prompt assistant: a third task `assist music` (caption + lyrics from
    a description in any language) only if it fits without touching the architecture of
    `internal/assistant/assist`; otherwise, out of v1.
- **Out of v1** (name it as future work, do not plan it): a project-level `bgm:` in `video` mode
  that generates a track and mixes it under the narration
  (`amix`+`loudnorm`); covers/repaint/extract (`task_type` ≠ `text2music`); music
  LoRAs (`adapter`); `ace-server` as an alternative backend.

## 4. Decisions you must close WITH ME before writing specs
Ask me one at a time, with your recommendation and why:
1. One change or two? My preference: `add-music-output-mode` (Go core: config, cli,
   music, cache, events, export, sched, scripts, backend catalogue) and
   `add-music-mode-ui` (Angular + `environment-status`/`model-manager-view`),
   dependent on the first, just as the API/frontend was split.
2. Reuse `scenes` as the list of tracks (my recommendation: yes, with per-mode validation
   as `images` already does, so as not to break `--only`, cache, importer or the
   editor) or introduce a new `tracks:` key?
3. Own lyrics as the main case. The real use is: the user writes the lyrics,
   chooses the style and the song generates itself. My proposal: `use_lm` per track, with
   default `false` when the track brings `lyrics` (DiT-only route: caption and lyrics
   verbatim, the lyrics are sung as written, faster and less VRAM) and `true` when
   it only brings a caption (let the LM compose the lyrics). Confirm it after the spike: if the
   DiT-only route does not work well, the alternative is LM + DiT with the parameters that
   prevent rewriting the lyrics, and `manifest.json` must always store the lyrics
   that were finally sung so they can be compared with the user's.
   `vocal_language` supports `es` and `en` at a minimum (both must be tested in the spike).
4. Default format: `mp3` (what upstream returns by default, small) or
   `wav` (lossless, better if it is later mixed into video). My bias: `wav` in
   `tmp/` and the chosen format in `out/`, if acestep.cpp allows it without a second
   pass; otherwise, the chosen one directly.
5. Name of the phases in the events enum: `compose`/`synth` (my proposal) or
   `lm`/`synth` mirroring the binaries.

## 5. Expected OpenSpec process
1. Resolve the questions in item 4.
2. `/opsx:propose` for each agreed change. In each one: `proposal.md`, `design.md`
   (decisions + risks: `ace-synth` output naming, `.gguf` suffix, LM+DiT VRAM
   on the 16 GB 5060 Ti versus the 12 GB 4070, expected timings, what
   happens if the lyrics returned by the LM differ from the user's, concurrency with
   `sd` in `--gpu 0,1`), `specs/<capability>/spec.md` with SHALL requirements and
   technology-neutral WHEN/THEN scenarios (ADDED for `music`, MODIFIED for
   `config`, `cli`, `cache`, `events`, `export`, `parallel-scenes`,
   `model-management` and the UI ones), and `tasks.md` with small, verifiable tasks
   (each one says how it is checked). The first task of the core change is the spike
   from item 2.
3. Tests: the `internal/music/music` wrapper is tested with a fake `CommandRunner` like
   `render` and `tts`; an optional smoke test with the real binaries behind a
   build tag, like `piper_smoke_test.go`.
4. `openspec validate <change> --strict --no-interactive` green in each change.
5. For the UI, first read the `frontend-design` skill if available and the
   conventions of `docs/PLAN_FRONTEND_PROMPT.md`.

## 6. Output of this session
Summary of the changes created, their location, questions that genuinely remain
open (not decisions you skipped) and the `/opsx:apply` command to start with.
Do NOT implement anything.
