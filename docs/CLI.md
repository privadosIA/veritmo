# Command line (`veritmo`)

[← Back to the README](../README.md)


Build the CLI once from the project root:

```bash
go build -o bin/veritmo ./cmd/veritmo
```

Then run it:

```bash
bin/veritmo [flags] [scenes.yaml]                 # run the pipeline
bin/veritmo import <dataset> -o scenes.yaml       # convert a scene dataset (see IMPORT.md)
bin/veritmo import --mode motion <dataset> -o scenes.yaml   # animated graphics; --mode music for songs
bin/veritmo export scenes.yaml -o dataset.json    # write a project as a dataset (or --format zip for a package; see IMPORT.md)
bin/veritmo serve --strict                          # start the server hiding every project not marked adult: false (by default only adult: true ones are hidden)
bin/veritmo --version                               # the edition of this build (private or public)
bin/veritmo describe photo.png                       # read a picture or a clip with a local vision model and print its prompt; --split gives one per shot (see ASSISTANT.md)
bin/veritmo check scenes.yaml                     # look at the animated clips of a project that was run: deformed people, colour drift, blur
bin/veritmo --project <id>                        # run a project of the database by its id (also with check and export); see below
bin/veritmo project pull <id> [file]              # write the saved definition of a project to a file; push <id> [file] saves a file as the definition
```

The `scenes.yaml` path may be given positionally or via `--config`; if neither is given, it defaults to `./scenes.yaml`. **Flags must come before the positional path**, not after: Go's standard `flag` package stops parsing flags at the first non-flag argument, so `veritmo scenes.yaml --json` silently treats `--json` as a second positional argument rather than a flag. Use `veritmo --json scenes.yaml`, or pass the path via `--config` if you want flags in any order.

## Projects of the database

The server keeps each project's definition in its database (see [DATA_STORE.md](DATA_STORE.md#the-projects-in-the-database)). The command line runs one of them with `--project <id>` (flags `--projects-dir`, default `projects`, and `--db`, default `.orchestrator/orchestrator.db`, or the database saved in Settings): it writes the saved definition and pages to the project's folder, runs there (the command moves into that folder, so the relative paths of the definition, such as `models/…`, resolve through the links it has) and takes back what the run changed as a new version. `check --project <id>` and `export --project <id>` do the same; a fix applied by `check --fix` becomes a version and no `scenes.yaml.bak` is left. An unknown project exits non-zero naming it before any program runs. `bin/veritmo [scenes.yaml]` and `--config` still run a loose file and open no database.

```bash
bin/veritmo --project demo --keep-tmp
bin/veritmo project pull demo my.yaml     # edit my.yaml with an editor ...
bin/veritmo project push demo my.yaml     # ... and save it (validated like PUT /api/projects/{id}/config; the replaced definition is a version)
```

**Editing `projects/<id>/scenes.yaml` by hand has no effect:** the next run overwrites it with the saved definition. Use `project pull` and `project push`, the editor, or `PUT /api/projects/{id}/config`. `push` exits non-zero with the validation error, and changes nothing, for a file that is not valid.

## The settings of a command

A command reads and writes the same settings as the server, which are in the database (see [DATA_STORE.md](DATA_STORE.md#the-settings-the-prompts-the-blocks-and-the-motions-in-the-database)): `veritmo cors add` is seen by a running server within 5 seconds, and a model routing saved in Settings is the one `check`, `describe` and `assist` use. The database is opened the first time a command needs a setting (the SQLite file `.orchestrator/orchestrator.db` of the folder it runs in, or the MySQL or Postgres saved in Settings); a command that never reads one opens nothing. Before the first start of the server there is no database file and the settings are still `settings.json`. A database that exists and is not migrated is not used: readers are told once (`run veritmo data migrate`) and get no settings, and a command that saves one fails instead of saving over what it could not read.

## Flags

| Flag | Default | Meaning |
|---|---|---|
| `--config <path>` | (positional arg, or `scenes.yaml`) | Path to `scenes.yaml`. |
| `--keep-tmp` | `false` | Preserve `tmp/` (including the cache manifest and every scene's intermediate `.wav`/`.png`/`.mp4`) after the run instead of deleting it. Needed for the cache to help on the *next* run — without it, every run starts cold. |
| `--gpu <index[,index…]>` | (unset) | GPU index to export as `CUDA_VISIBLE_DEVICES` for the `sd` phases only. A comma-separated list (`--gpu 0,1`) runs one scene at a time on each listed GPU (see *Using both GPUs*). |
| (environment) `VERITMO_STORE_MB` | `2048` | The narration and the picture of a video scene are also kept by the hash of their inputs in `tmp/store/`, and a scene whose narration (text, voice, speed) or picture (prompt, seed, model, size, steps...) matches one already made, by any scene, takes the file instead of running `piper` or `sd`: the second of two scenes with the same sentence, a renamed or moved scene, a project made again. The phase is reported as `skipped`. `--force` does not read the store (but what it makes goes in). It lasts as long as `tmp/` does (with `--keep-tmp` across runs, otherwise within the run); the number is the most megabytes it may hold, removing the oldest files when it goes over, and `0` turns it off. Only the narration and the drawn pictures: animation, clips, a given picture and music are not kept this way. |
| `--overlap <n>` | `0` | With one GPU or none, narrate and probe up to `n` scenes ahead and encode the clip of a drawn scene while the card renders another one (a video project; 0 is one phase after another, the default; 2 is a good value). The video, the manifest and the seeds are the same; the events of different scenes interleave (correlate them by `scene_id`). It is ignored, with a warning, when several GPUs are listed (each already works on its own scene) and in a project with motion graphics or overlays (they draw with a browser that must not run next to a render). A failure in any phase stops the whole run. |
| `--vram-warn-mb <n>` | `2048` | With `--gpu` set, warn (but don't abort) if a listed GPU's free VRAM is below this many MB (the warning names the GPU). |
| `--json` | `false` | Emit progress as JSON-lines on stdout instead of human-readable text. See `docs/events.md` for the full contract. |
| `--force` | `false` | Bypass the cache and re-run every phase for every scene, even if nothing changed. In images mode this re-renders every variant of the targeted scene(s). |
| `--only <scene-id>` | (unset) | Re-process only the named scene (a track ID in music mode); every other scene reuses its existing cached clip for the final video (they are **not** dropped from the output). In images mode, other scenes' exported PNGs are left untouched and `manifest.json` lists only the processed scene. |
| `--check` | `false` | When the run ends, check the animated clips (see *Checking the animated clips* below). Keeps `tmp/`, which the check reads. |
| `--fix` | `false` | With `--check`: edit `scenes.yaml` to cut or remake the bad scenes and run the project again, up to `--check-attempts` rounds. |
| `--check-attempts <n>` | `2` | With `--check --fix`: how many rounds of fixing and making again. |
| `--check-model <name>` | (unset) | With `--check`: the Ollama vision model that judges the frames (or `$VERITMO_VISION_MODEL`). Without one only colour and sharpness are checked. |
| `--check-provider <id>` | (routing) | With `--check`: the provider that judges the frames, instead of the one routed to the quality check in Settings. A remote one must have been accepted in Settings; the check stops and says so otherwise. `assist`, `describe` and `check` also take `--provider <id>`. |
| `--no-vision` | `false` | With `--check`: colour and sharpness only, even if a model is named. |
| `--mode <video\|images>` | (unset) | Override `output.mode` from `scenes.yaml` (flag takes precedence over YAML). `--mode images` runs the images pipeline even on a video project, without requiring `output.file`. |

## Filling the prompt presets

`veritmo prompts fetch [--source id] [--max n] [--seed n] [--media-dir DIR] [--db file] [--sources file] [--out file] [--dry-run]` downloads public prompt datasets whose licence it reads from their cards and writes them into the database of the server (`--db` is the SQLite file when no other database is saved in Settings; it must be migrated), so a running server lists them at once; the downloads and the report stay in `<prompts>/.cache` (or a bundle with `--out`). It is described in [PROMPT_PRESETS.md](PROMPT_PRESETS.md).

## Checking the animated clips

Wan sometimes deforms faces and bodies, or drifts in colour, from some second of a clip on. `veritmo check scenes.yaml` looks at the animated clips of a project that has been run (it reads `tmp/scene_<id>.anim.avi`, so run with `--keep-tmp`) and says, for each animated scene, whether it is fine, can be **cut** at some second, or should be **made again**:

```text
$ veritmo check --model qwen3.8:27b scenes.yaml
[check] s01_bench_wide: ok (2.0s clean)
[check] s04c_two_smile: trim to 1.0s of 2.0s — from 1.25s (vision: Extra arm on left person, fused anatomy)
[check] s13_curve_path: retry (bad from the start) — from 1.00s (color)
checked 15 animated scenes: 10 ok, 5 with problems
```

- **What it looks at**: 4 frames per clip second (`--fps`), only the part the scene uses (a silent scene's `silence_seconds`). Per frame it measures the shift of the mean colour and the loss of sharpness against the first frame (a colour cast above 20, or 30 % less sharpness, flags the frame) and, **if you name a vision model** (`--model` or `$VERITMO_VISION_MODEL`), asks it through Ollama whether the people are deformed (it also asks it to locate the faces, enlarges each one and judges it on its own, so a small deformed face in a wide shot is caught; frames are then read at 1280 px) (a defect of severity 7 or more, `--min-severity`, flags it). A model is never chosen for you: without one the check measures only, and lists the vision models Ollama has. Of the three installed ones we tried (`qwen3.8:27b`, `gemma4:26b`, `gemma4:31b`), only the first found most bad frames without flagging the good ones; the last flagged every frame. The model is unloaded from Ollama when the check ends, so the next run has the memory.
- **The verdict**: from the first counted bad frame the clean part is the time before it (minus one sampling step). With at least 0.8 s clean (`--min-keep`) the scene is **trim**; with less it is **retry**. One doubtful frame from the vision model alone is ignored unless it is severe (9 or more). The report goes to `out/<project>/quality.json`.
- **`--fix`** edits `scenes.yaml` (comments kept, the original saved once as `scenes.yaml.bak`): a silent scene with a trim gets that `silence_seconds`, any other bad scene gets its `seed` + 1000 (a narrated scene cannot be cut: its clip is fitted to the narration). Then run the project: only the changed scenes are made again.
- **Exit codes**: 0 when nothing is wrong (or `--fix` edited the file), 3 when there are problems and no `--fix`, 1 on an error.
- **All in one**: `veritmo --check --fix --check-model qwen3.8:27b --gpu 1 scenes.yaml` runs the project, checks it, fixes, runs the changed scenes again and checks again, up to `--check-attempts` rounds. The model needs the memory of its own (17 GB for the ones above), which is why the check runs when the run is over and unloads it.

## HyperFrames blocks

`veritmo blocks import-hyperframes <registry/blocks dir>` copies the blocks of a HyperFrames clone whole into the catalog (the files in `hyperframes/`, or `$HYPERFRAMES_DIR`; the record of each block in the database, `--db` like the other commands) and checks them; `blocks check [name ...]` checks them again, `blocks list [--json]` lists them and `blocks frame <name> <seconds> <out.png>` draws one frame. The installer's optional component `hfblocks` does the whole thing (`bash scripts/get-hyperframes-blocks.sh`). See [HYPERFRAMES.md](HYPERFRAMES.md).

## Music generators in Docker

`bash scripts/music-containers.sh build|weights|up|down|status` runs HeartMuLa and DiffRhythm2 as the Python containers of `docker/`, an alternative to the .NET services; see [INSTALL.md](INSTALL.md#music-generators-in-docker-python-optional).

## Using both GPUs

`--gpu 0,1` processes two scenes at the same time, one per GPU: the next scene in project order goes to whichever GPU frees up first, and every `sd` run of a scene sees only its GPU. The final video, the export manifest and the cache keep project order whatever finishes first, and random seeds are drawn in project order up front. Events of different scenes interleave in the stream (within a scene they stay in order). If a scene fails the others are stopped and the run ends with that failure. With one GPU or none nothing changes.

Memory, not VRAM, is the limit: `sd` copies the whole model into RAM too. Before launching `sd` a scene reserves (size of its model files × 1.15) from a budget of the memory available at start minus 4 GiB, and waits if it does not fit; a single run bigger than the budget runs alone, and if the memory cannot be read every `sd` run is serialized. On this 30 GB machine that means:

- **Wan animation** (≈9 GiB), SDXL and SD1.5 overlap two at a time. Measured with 4 animated scenes (SDXL render + Wan): 374 s on one GPU, 188 s on `--gpu 0,1`, with the final video in the same order.
- **Krea 2 / IntoRealism renders** (≈18 GiB) do not fit beside any other `sd` and take turns (a render also waits for a running animation), but the two animations still overlap. Measured with 4 animated scenes: 252 s on `--gpu 0,1` against about 410 s sequentially, with the lowest available RAM staying at 11 GB. The 12 GB RTX 4070 renders the Krea 2 set too (21 s instead of 16 s on the 5060 Ti).

Splitting one clip across both GPUs does not speed it up; the gain comes from running different scenes.

## Example run

```bash
bash scripts/build-sd.sh
bash scripts/get-piper.sh
bash scripts/get-models.sh
bin/veritmo --gpu 1 --keep-tmp scenes.yaml
```

Re-running with `--keep-tmp` from the previous run and only `scene_outro`'s prompt changed:

```bash
bin/veritmo --gpu 1 --keep-tmp --only scene_outro scenes.yaml
```
