# cli Specification

## Purpose
Defines the `orchestrator` command's flags, config resolution, and how it wires the other capabilities into one end-to-end pipeline run.

## Requirements

### Requirement: The scenes.yaml path may be given positionally or via --config
The CLI SHALL accept the `scenes.yaml` path as a bare positional argument or via `--config`, and SHALL default to `./scenes.yaml` when neither is given. Flags SHALL be given before the positional path argument, since argument parsing stops at the first non-flag argument.

#### Scenario: positional argument
- **WHEN** the CLI is invoked with a bare path argument and no `--config`
- **THEN** that path is used as the `scenes.yaml` path

#### Scenario: neither given
- **WHEN** neither a positional argument nor `--config` is given
- **THEN** the CLI defaults to `./scenes.yaml`

#### Scenario: flags placed after the positional path are not recognized
- **WHEN** a flag such as `--json` is given after the positional `scenes.yaml` path
- **THEN** it is treated as an additional positional argument, not as a flag
- **AND** callers who need flags after the path MUST use `--config` instead of a positional path

### Requirement: --only rejects an unknown scene ID before any subprocess runs
The CLI SHALL validate that `--only`'s value names an existing scene id before invoking any native binary.

#### Scenario: unknown scene id
- **WHEN** `--only` names a scene id absent from `scenes.yaml`
- **THEN** the CLI exits non-zero with an error naming the unknown id, before any phase runs

### Requirement: --gpu pins the render phase's GPU and enables a free-VRAM warning
When `--gpu` is set, the CLI SHALL pass `CUDA_VISIBLE_DEVICES` for that GPU to the render phase only, and SHALL emit a warning (without aborting) when free VRAM on that GPU is below `--vram-warn-mb`.

#### Scenario: low VRAM
- **WHEN** `--gpu` is set and `nvidia-smi` reports free VRAM below `--vram-warn-mb`
- **THEN** the CLI emits a `warn` event
- **AND** the run continues

### Requirement: A fatal error in any phase stops the pipeline with a non-zero exit
The CLI SHALL emit an `error` event and exit non-zero as soon as any phase (tts/probe/render/clip/concat) fails, without processing further scenes.

#### Scenario: subprocess failure
- **WHEN** any phase returns an error
- **THEN** the CLI emits an `error` event naming the scene and failure
- **AND** exits non-zero without processing further scenes

### Requirement: --keep-tmp controls whether tmp/ (and its cache) survives the run
The CLI SHALL leave `tmp/` on disk after a successful run when `--keep-tmp` is set, and SHALL remove it otherwise.

#### Scenario: keep-tmp set
- **WHEN** `--keep-tmp` is passed
- **THEN** `tmp/` is left on disk after a successful run, enabling cache reuse on the next invocation

#### Scenario: keep-tmp not set
- **WHEN** `--keep-tmp` is absent
- **THEN** `tmp/` is removed after a successful run

### Requirement: The pipeline runs phases according to output.mode

The CLI SHALL select the per-scene phase sequence based on the resolved `output.mode` value from `scenes.yaml`:

- When `output.mode` is `video` (or unset, defaulting to `video`), the per-scene phase sequence SHALL be exactly: `tts` → `probe` → `render` → `clip`, followed by the global `concat` phase after all scenes complete. This is the existing behaviour and SHALL NOT change.
- When `output.mode` is `images`, the per-scene phase sequence SHALL be exactly: `render` (invoked once per variant, strictly sequentially) → `export` (manifest write and, when `output.contact_sheet` is `true`, contact-sheet assembly). The CLI SHALL NOT invoke the `tts`, `probe`, `clip`, or `concat` phases.
- When `output.mode` is `music`, the per-track phase sequence SHALL be: `compose` (only for tracks that use the language model) → `synth` (once per variant) → `export` (audio files and manifest). The CLI SHALL NOT invoke `tts`, `probe`, `render`, `animate`, `clip` or `concat`, and SHALL NOT spawn `piper`, `sd`, `ffmpeg` or `ffprobe`.
- In `images` mode the CLI SHALL NOT spawn a `piper` subprocess, SHALL NOT invoke `ffprobe` for narration-duration measurement, and SHALL NOT invoke `ffmpeg` for clip-creation or concat operations. The only `ffmpeg` invocation permitted in `images` mode is the optional contact-sheet `tile` filter step within the `export` phase.
- The `--json` event stream SHALL reflect the actual phases executed: in `images` mode, only `render` and `export` phase events SHALL appear per scene; no `tts`, `probe`, `clip`, or `concat` events SHALL be emitted. In `music` mode only `compose`, `synth` and `export` events SHALL appear.

#### Scenario: Video mode default unchanged

- WHEN `output.mode` is `video` or the `output` block is absent (default)
- AND the project contains two scenes, `intro` and `outro`
- THEN the CLI SHALL execute, for each scene in order: `tts`, `probe`, `render`, `clip`
- AND after both scenes complete the CLI SHALL execute `concat`
- AND the event stream SHALL contain `phase_start`/`phase_end` pairs for `tts`, `probe`, `render`, `clip` per scene and for `concat` globally

#### Scenario: Images mode runs render and export only

- WHEN `output.mode` is `images`
- AND scene `intro` has `variants: 3`
- THEN the CLI SHALL invoke `sd` three times sequentially for scene `intro` (variant 0, variant 1, variant 2)
- AND the CLI SHALL then execute the `export` phase (write `manifest.json` and, if enabled, `contact_sheet.png`)
- AND the event stream SHALL contain `phase_start`/`phase_end` pairs for `render` and `export` for scene `intro`
- AND the event stream SHALL NOT contain any `tts`, `probe`, `clip`, or `concat` events

#### Scenario: Images mode never invokes piper, ffprobe-for-duration, or clip/concat ffmpeg steps

- WHEN `output.mode` is `images`
- AND the project contains one or more scenes
- THEN the CLI SHALL NOT spawn a `piper` process for any scene
- AND the CLI SHALL NOT invoke `ffprobe` to measure audio duration
- AND the CLI SHALL NOT invoke `ffmpeg` with a loop-image-to-clip command
- AND the CLI SHALL NOT invoke `ffmpeg` with a concat-demuxer command
- AND the only permitted `ffmpeg` invocation is the `tile` filter for the optional contact sheet within `export`

#### Scenario: Music mode with own lyrics runs synth and export

- WHEN `output.mode` is `music` and track `song` has lyrics and `variants: 2`
- THEN the CLI SHALL run `synth` twice in sequence for `song` and then `export`
- AND no `compose` event SHALL appear for `song`

#### Scenario: Music mode with a caption only runs compose first

- WHEN `output.mode` is `music` and track `song` has no lyrics and is not instrumental
- THEN the CLI SHALL run `compose`, then `synth`, then `export` for `song`

### Requirement: --mode overrides output.mode from the command line

The CLI SHALL accept a `--mode` flag whose value is `video`, `images` or `music`:

- When `--mode` is passed, its value SHALL replace the resolved `output.mode` **before** `config.Validate` runs, so that mode-dependent validation, phase selection, the pre-flight dependency check, and event emission all operate on the effective (flag-supplied) mode.
- When `--mode` is absent, the mode SHALL be resolved from `scenes.yaml` exactly as specified elsewhere in this delta (`output.mode`, defaulting to `video`).
- When `--mode` is passed with any value other than `video`, `images` or `music`, the CLI SHALL exit with a non-zero status and an error message listing the accepted values, before processing any scene.
- Precedence is **flag > YAML**: `--mode images` on a project whose `scenes.yaml` says `output.mode: video` SHALL run the images pipeline (and vice versa). In particular, `--mode images` on a video project SHALL NOT require `output.file` to be set. `--mode music` on a project without `tracks` SHALL fail validation saying tracks are required.

#### Scenario: --mode images overrides a video-mode project

- WHEN `scenes.yaml` has `output.mode: video` (or no `output` block) and the user invokes `veritmo --mode images`
- THEN the CLI SHALL run the images phase sequence (`render` × variants → `export`) and SHALL NOT require `output.file`
- AND no `tts`, `probe`, `clip`, or `concat` phase SHALL run

#### Scenario: Invalid --mode value is rejected

- WHEN the user invokes `veritmo --mode audio`
- THEN the CLI SHALL exit with a non-zero status and an error stating that `--mode` must be `video`, `images` or `music`
- AND no scene SHALL be processed

#### Scenario: --mode music without tracks

- WHEN the user invokes `veritmo --mode music` on a project that declares only `scenes`
- THEN the CLI SHALL exit with a non-zero status saying `tracks` are required in music mode

### Requirement: Pre-flight dependency check matches the selected mode

The pre-flight dependency check (executed before any scene processing begins) SHALL verify the presence of binaries required by the selected `output.mode`:

- In `video` mode the check SHALL require: `piper`, `sd`, `ffmpeg`, `ffprobe` (existing behaviour, unchanged).
- In `images` mode the check SHALL require: `sd` (always). The check SHALL NOT require `piper`. The check SHALL require `ffmpeg` only when `output.contact_sheet` is `true`. The check SHALL NOT require `ffprobe` in `images` mode.
- In `music` mode the check SHALL require `ace-synth`, and `ace-lm` only when at least one target track uses the language model. It SHALL NOT require `piper`, `sd`, `ffmpeg` or `ffprobe`.
- If a required binary is absent, the CLI SHALL exit with a non-zero status and a descriptive error naming the missing binary before any scene is processed.
- The optional `nvidia-smi` advisory check (gated by `--vram-warn-mb`) SHALL apply in all modes and is not affected by this requirement.

#### Scenario: Images mode with piper absent passes pre-flight

- WHEN `output.mode` is `images`
- AND `output.contact_sheet` is `false` (or unset)
- AND `sd` is present on `PATH`
- AND `piper` is NOT present on `PATH`
- AND `ffmpeg` is NOT present on `PATH`
- AND `ffprobe` is NOT present on `PATH`
- THEN the pre-flight check SHALL succeed
- AND the CLI SHALL proceed to process scenes (render + export)

#### Scenario: Images mode with contact sheet requires ffmpeg

- WHEN `output.mode` is `images`
- AND `output.contact_sheet` is `true`
- AND `sd` is present on `PATH`
- AND `ffmpeg` is NOT present on `PATH`
- THEN the pre-flight check SHALL fail
- AND the CLI SHALL exit with a non-zero status and an error message naming `ffmpeg` as the missing dependency

#### Scenario: Music mode with own lyrics needs only ace-synth

- WHEN `output.mode` is `music`, every target track has lyrics and does not set `use_lm: true`
- AND `ace-synth` is on `PATH` but `ace-lm`, `sd`, `piper` and `ffmpeg` are not
- THEN the pre-flight check SHALL succeed

#### Scenario: Music mode needs ace-lm for composed tracks

- WHEN `output.mode` is `music` and a target track has only a caption
- AND `ace-lm` is NOT on `PATH`
- THEN the pre-flight check SHALL fail naming `ace-lm`

### Requirement: --only and --force keep their semantics in images mode

The existing flags `--only` and `--force` SHALL retain their documented semantics when `output.mode` is `images`:

- `--only <scene_id>` SHALL restrict processing to the single named scene. In `images` mode, images for scenes not named by `--only` SHALL be left untouched on disk (neither deleted nor overwritten). The `export` phase SHALL still produce a complete `manifest.json` reflecting only the processed scene(s).
- `--force` SHALL bypass the render cache (`internal/platform/cache.ImageHash` lookup) for the targeted scene(s), causing `sd` to be re-invoked even if a cached PNG exists. In `images` mode with `variants > 1`, `--force` SHALL re-render all variants of the targeted scene.
- In `video` mode the semantics of `--only` and `--force` are unchanged from the existing specification.

#### Scenario: --only restricts processing to the named scene in images mode

- WHEN `output.mode` is `images`
- AND the project contains scenes `intro`, `middle`, and `outro`
- AND the user invokes `veritmo --only middle`
- THEN the CLI SHALL render and export only scene `middle`
- AND the CLI SHALL NOT render or export scenes `intro` or `outro`
- AND any previously generated `out/<project>/intro.png` and `out/<project>/outro.png` files SHALL remain on disk unmodified
- AND `manifest.json` SHALL contain an entry only for scene `middle`

#### Scenario: --force bypasses the render cache in images mode

- WHEN `output.mode` is `images`
- AND scene `intro` has `variants: 2`
- AND a cached PNG for `intro` variant 0 and variant 1 already exists (matching `ImageHash`)
- AND the user invokes `veritmo --force`
- THEN the CLI SHALL re-invoke `sd` for both variant 0 and variant 1 of scene `intro`
- AND the CLI SHALL overwrite the existing PNG files with the new renders
- AND the `

### Requirement: Silent scenes skip narration phases entirely

In video mode, for a scene that declares `silence_seconds`, the CLI SHALL:

- NOT spawn a `piper` subprocess and NOT invoke `ffprobe` for duration measurement for that scene.
- Use `silence_seconds` as the scene's clip duration.
- Emit no `tts` or `probe` events (neither `phase_start`/`phase_end` nor `skipped`) for that scene, following the established contract that phases which do not run are silent in the event stream; `render` and `clip` events are emitted as usual.
- Run the clip phase with the generated silent audio track (specified in the `assemble` capability).

Narrated scenes in the same project SHALL be processed exactly as today; a project may freely mix narrated and silent scenes.

#### Scenario: silent scene emits only render and clip events

- **WHEN** a video-mode project contains a silent scene (`silence_seconds: 2.0`, empty `tts_text`)
- **THEN** the event stream for that scene contains `phase_start`/`phase_end` pairs (or `skipped`) only for `render` and `clip`
- **AND** contains zero events with `phase` equal to `tts` or `probe` for that scene
- **AND** no `piper` or duration-probing `ffprobe` subprocess is spawned for that scene

#### Scenario: mixed project processes both scene kinds

- **WHEN** a video-mode project contains a narrated scene followed by a silent scene
- **THEN** the narrated scene runs `tts → probe → render → clip` exactly as today
- **AND** the silent scene runs only `render → clip` with duration `silence_seconds`
- **AND** the final concat includes both clips in scene order

### Requirement: Pre-flight still requires the full video toolchain in video mode

The pre-flight dependency check in video mode SHALL continue to require `piper`, `sd`, `ffmpeg`, and `ffprobe` even when every scene in the project is silent: the check is mode-dependent, not content-dependent, keeping its behaviour predictable and its implementation independent of scene inspection.

#### Scenario: all-silent project still requires piper

- **WHEN** a video-mode project contains only silent scenes and `piper` is absent from PATH
- **THEN** the pre-flight check fails naming `piper`

### Requirement: --gpu accepts a comma-separated list of GPUs

`--gpu` SHALL accept either one GPU index or several separated by commas (for example `0,1`). Each entry SHALL be a non-negative integer, entries SHALL be distinct, and anything else SHALL be rejected before any work starts with an error naming the offending value. A single index keeps the current meaning; several indexes enable scene-level parallelism as defined by the `parallel-scenes` capability.

#### Scenario: two GPUs
- **WHEN** the CLI runs with `--gpu 0,1`
- **THEN** the project runs with scenes distributed across GPUs 0 and 1

#### Scenario: invalid list
- **WHEN** the CLI runs with `--gpu 0,x` or `--gpu 1,1`
- **THEN** it exits with an error naming the invalid value and starts no subprocess

### Requirement: --only selects a track in music mode

In `music` mode `--only <id>` SHALL refer to a track ID. An unknown ID SHALL be rejected before any subprocess runs, naming the ID. Only the selected track's variants SHALL be processed; the audio and manifest entries of other tracks SHALL be left untouched. `--force` SHALL regenerate every phase and variant of the target tracks, ignoring the cache.

#### Scenario: Unknown track
- **WHEN** the user passes `--only nope` and no track has that id
- **THEN** the CLI exits non-zero with an error naming `nope` and processes nothing

#### Scenario: Regenerate one track
- **WHEN** the user passes `--only intro --force` in a project with tracks `intro` and `outro`
- **THEN** `intro` is composed and synthesized again and `outro`'s files are not modified

### Requirement: The command line exports a project

`veritmo export <scenes.yaml> -o <file>` SHALL write the project as a dataset (`-format dataset`, the default) or as the full project package with its recipe (`-format zip`, with `-lang es|en` for the language of `RECIPE.md`), never run the pipeline, print what a dataset cannot carry and how many entries were written, and refuse to overwrite an existing file unless `--force-overwrite` is given. Missing arguments or an unknown format SHALL exit non-zero.

#### Scenario: Dataset
- **WHEN** `veritmo export -o demo.json scenes.yaml` is run
- **THEN** the file is a dataset that `veritmo import` reads back, and the notes are printed

#### Scenario: Full project
- **WHEN** `veritmo export -format zip -o demo.zip scenes.yaml` is run
- **THEN** the zip holds `scenes.yaml`, the README, `RECIPE.md`, `recipe.json` and the sources

#### Scenario: Existing output
- **WHEN** the output file exists
- **THEN** the command fails naming it, unless `--force-overwrite` is set

### Requirement: The command line checks the clips of a project

`veritmo check [flags] <scenes.yaml>` SHALL check the animated clips of the project, print one line per scene and a total, and write `out/<project>/quality.json`, without running the pipeline. It SHALL exit 0 when nothing is wrong (or after `--fix` edited the file), 3 when there are problems and no `--fix`, and 1 on an error. `--no-vision`, `--min-severity`, `--min-keep` and `--fps` SHALL tune it.

#### Scenario: Problems and no fix
- **WHEN** a scene is bad and the command runs without `--fix`
- **THEN** it prints the problem, leaves scenes.yaml alone and exits 3

#### Scenario: Fix
- **WHEN** it runs with `--fix`
- **THEN** scenes.yaml is edited, a backup is kept and it exits 0

### Requirement: A run can check and fix itself

A run with `--check` SHALL keep `tmp/` and, when it ends, check the clips. With `--fix` as well it SHALL edit scenes.yaml, run the project again (only the changed scenes are remade) and check again, up to `--check-attempts` rounds (2 by default), stopping as soon as nothing is wrong and exiting 3 if problems remain after the last round.

#### Scenario: Clean after one round
- **WHEN** a run with `--check --fix` finds one bad scene and the second check finds none
- **THEN** the project was made twice and the exit is 0

#### Scenario: Out of rounds
- **WHEN** problems remain after the last round
- **THEN** it says how many and where the report is, and exits 3

### Requirement: The command line imports HyperFrames blocks as parts

`veritmo parts import-hyperframes <dir> [--only a,b] [--dry-run] [--json]` SHALL convert the blocks installed in `<dir>` into parts as the `music-motion-parts` specification says, SHALL exit 0 when at least one block was written (or, with `--dry-run`, would be) and 1 when none, and SHALL print one line per block with `written` or `refused: <reason>` (JSON with `--json`), the warnings under it, and, for a written block that embeds pictures, the files embedded with their size (`pictures: stroke.svg 12 KB, paper.webp 140 KB`; in JSON, `pictures: [{name, kind, bytes}]`). It SHALL need no running server and SHALL use the same parts folder and checks the server uses.

#### Scenario: A folder of blocks
- **WHEN** the command runs over a folder with twelve installed blocks of which eight pass the checks
- **THEN** eight parts are written, twelve lines are printed and the exit code is 0

#### Scenario: Nothing usable
- **WHEN** every block in the folder mounts media
- **THEN** nothing is written, every line says why and the exit code is 1

#### Scenario: A block with pictures
- **WHEN** a written block embedded two files
- **THEN** its line is followed by `pictures:` naming both with their sizes, and with `--json` the entry carries them

### Requirement: The command line uses the database of the server

A command of `veritmo` that reads or writes state kept in the database SHALL open the same database the server uses: the one saved in Settings, or the SQLite file given with `--db` (by default the one `serve` uses). A command SHALL be able to name another MySQL or Postgres database with `--driver`, `--host`, `--port`, `--database` and `--user`, and SHALL read its password from the environment variable `VERITMO_DB_PASSWORD` (or `MO_DB_PASSWORD` as the obsolete fallback) and never from a flag (a command line is visible to the other users of the machine); those flags without `--driver`, `--driver sqlite` and an engine that does not exist SHALL be a usage error. It SHALL work while the server is running on the same SQLite file. A command that cannot open the database SHALL exit with a non-zero code and an error that names the database, before doing any work.

#### Scenario: While the server runs

- **WHEN** a command that reads state runs while the server is up on the same SQLite file
- **THEN** it reads the same state the server answers and neither fails

#### Scenario: No database

- **WHEN** the database cannot be opened
- **THEN** the command exits with a non-zero code and names the database

### Requirement: The command line manages the data

`veritmo data status` SHALL print the engine, the migrations applied and pending, the tables with their rows, the imports and the files left behind, changing nothing, on a database that is not migrated too. `veritmo data migrate` SHALL apply the migrations the database has not applied, say which, and be the only command that changes the schema besides `rollback`; `import`, `export` and `restore` SHALL refuse a database with migrations pending and say to run `migrate`. `veritmo data import [entity]` SHALL run the import of the state kept in files. `veritmo data export <file>` SHALL write the export, `veritmo data restore <file>` SHALL load it into an empty database and `veritmo data rollback <migration>` SHALL undo the last migration applied (see `data-store`). Each SHALL exit with a non-zero code and a reason when it fails.

#### Scenario: Status

- **WHEN** `veritmo data status` runs
- **THEN** it prints the engine, the migrations applied and one line per table with its rows

#### Scenario: Migrate

- **WHEN** `veritmo data migrate` runs on a new database
- **THEN** it applies every migration and prints them, and a second run says there is nothing to apply

#### Scenario: Import before migrating

- **WHEN** `veritmo data import` runs on a database with migrations pending
- **THEN** it exits with a non-zero code saying to run `veritmo data migrate`, and changes nothing

#### Scenario: Restore on a database with rows

- **WHEN** `veritmo data restore` runs on a database that is not empty
- **THEN** it exits with a non-zero code, says why and changes nothing

### Requirement: The command line runs a project of the database

`veritmo --project <id>` (and the commands `check` and `export` given `--project <id>` instead of a `scenes.yaml`) SHALL read the definition and the pages of the project from the database, write them to its directory and run there, and take back what the run changed, as the server does. `veritmo <scenes.yaml>` and `--config` SHALL keep running a loose file with no database, as before. An unknown project SHALL exit with a non-zero code naming it before any subprocess runs.

#### Scenario: A project of the database

- **WHEN** `veritmo --project demo` runs
- **THEN** the run uses the definition saved for `demo`, in its directory

#### Scenario: A loose file

- **WHEN** `veritmo some/scenes.yaml` runs
- **THEN** it runs as before and no database is opened

### Requirement: The definition can be pulled and pushed as a file

`veritmo project pull <id> [file]` SHALL write the saved definition of a project to a file (by default `scenes.yaml` in its directory). `veritmo project push <id> [file]` SHALL save the file as the definition after the validation a save through the API passes, keeping the replaced one as a version; a file that is not valid SHALL exit with a non-zero code and the validation error, changing nothing.

#### Scenario: Edit with an editor

- **WHEN** the definition is pulled, edited and pushed
- **THEN** `GET /api/projects/{id}/config` answers the edited text

#### Scenario: An invalid file

- **WHEN** a file that fails validation is pushed
- **THEN** the command exits with a non-zero code and the definition is unchanged
