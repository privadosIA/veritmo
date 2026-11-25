# Image mode (`output.mode: images`)

User reference for the orchestrator's "images" output mode. With this
mode, a single `scenes.yaml` produces **stills per scene** instead of a video:
for each scene only the `render` phase runs (one `sd` invocation per
variant), followed by a lightweight `export` phase. The `tts`, `probe`,
`clip` and `concat` phases are skipped entirely: none of their subprocesses is launched
and they emit no events.

Use cases: exploring and pinning down prompts/seeds in seconds before assembling the
video, using the orchestrator as a batch image generator, or running on
a render-only machine that does not have `piper` installed.

## Activation

Two ways, with precedence **flag > YAML**:

```yaml
# scenes.yaml
output:
  mode: images
  contact_sheet: true   # opcional, por defecto false
```

```bash
# o puntualmente desde la CLI, sin tocar el YAML:
bin/veritmo --mode images scenes.yaml
```

`--mode images` on a video project runs the image pipeline
without requiring `output.file`. A value other than `video`/`images` aborts with
an error before anything is processed.

## Dependencies (pre-flight)

| Mode | Binaries required in PATH |
|---|---|
| `video` | `piper`, `sd`, `ffmpeg`, `ffprobe` |
| `images` | `sd` (always); `ffmpeg` only if `contact_sheet: true` |

In image mode `piper` and `ffprobe` are never required.

## Relevant schema

- `scene.variants` (int ≥ 1, default 1): number of images to generate
  for the scene, with **strictly sequential** `sd` invocations.
  In video mode it is ignored with a warning.
- `scene.prompt` remains mandatory. `tts_text` and `speed` are
  optional in image mode and, if present, are ignored with a warning.
- `defaults.subject_prompt`: prepended to each scene's `prompt`
  (subject coherence across scenes).
- `defaults.negative_prompt`: appended after each scene's
  `negative_prompt`.
- `defaults.lora_model_dir` and `scene.lora`: **reserved** — they are parsed and
  enter the cache key, but have no behaviour yet.

## Starting from an image (img2img)

A scene can start from an image of yours instead of noise: `init_image` is the
name of a `.png`, `.jpg`, `.jpeg` or `.webp` in the project's `sources/` folder
(the editor uploads it for you) and `strength` (0 to 1, default 0.6) is how far the result
may move away from it. The prompt says what to do with the image.

```yaml
scenes:
  - id: acuarela
    prompt: a watercolor painting of the same scene
    init_image: faro.jpg
    strength: 0.5          # bajo: se parece al original · alto: el prompt la reinterpreta
    variants: 3
```

- The image is first fitted to the project resolution (scaled to cover and cropped
  from the centre, so a portrait photo in a landscape project loses top and
  bottom) and passed to `sd` with `-i` and `--strength`.
- The variants all start from the same image, each with its own seed.
- The cache includes the image **content** and the strength; the manifest stores
  `init_image` and `strength`. It needs ffmpeg (for the fitting) even without a contact sheet.
- It also works in video mode: the transformed image feeds the clip. It cannot be
  combined with `image` (your own image as is, video only).
- It has been tested with checkpoints (SD 1.5, SDXL): in a 15-step test, `strength: 0.3`
  leaves the image almost unchanged and `0.95` redoes it completely according to the prompt. With a
  set of models (Krea 2 Turbo Q4 and IntoRealism v4.0 fp8, 8 steps, cfg 1) it also works, but the strength
  acts more abruptly: with `0.3` the image barely changes; with `0.6` (the default
  value) it keeps the composition, but a style change («like an oil painting») is barely
  applied; with `0.75` or more the composition is lost, and at `0.9` it also came out duplicated in
  two panels. To change style while keeping the scene, try between `0.5` and
  `0.7` and, if that is not enough, start from a lower strength with a more explicit prompt.

## Seeds and reproducibility

- With an explicit `seed` on the scene, variant *i* (0-based) uses
  `seed + i`: deterministic and cacheable across runs.
- Without `seed`, the orchestrator generates a random 32-bit seed per
  variant and **records it in `manifest.json`**; re-running `sd` with that
  seed and the other parameters from the manifest reproduces the identical image.

## Output artifacts

Everything lands in `out/<project>/`:

| Artifact | Path | Notes |
|---|---|---|
| PNG (1 variant) | `out/<project>/<scene_id>.png` | |
| PNG (N variants) | `out/<project>/<scene_id>_v<N>.png` | `N` is 1-based |
| Manifest (mandatory) | `out/<project>/manifest.json` | atomic write (tmp + rename) |
| Contact sheet (optional) | `out/<project>/contact_sheet.png` | `ffmpeg` `tile` filter, only with `contact_sheet: true` |

Each manifest entry records: `scene_id`, `variant` (0-based),
`effective_seed`, the **final, already composed** `prompt` and `negative_prompt`,
`model`, `resolution`, `sampler`, `steps` and `file` (`resolution` and `steps` are those of
each scene: its own if it declares a `render` block, see [SCENES_YAML.md](SCENES_YAML.md#quality-per-scene), and the project's
if not). It is the source of truth
for any downstream consumer (dashboard, CI, re-prompting). Output path
collisions are rejected at validation, before launching
any subprocess.

If the contact sheet fails, the PNGs already exported and the `manifest.json`
are left intact on disk; the process exits with a non-zero code.

## Cache

- The render cache is **shared between modes**: switching a project from
  `images` to `video` (or the other way round) reuses the already rendered PNGs without
  invoking `sd` again, as long as no render input changes.
- The cache key includes, besides the seven historical fields:
  `subject_prompt`, the global `negative_prompt`, `lora` and the variant
  index. An old project without these fields keeps its exact hash.
- In image mode the cache decision ignores audio: changing `tts_text`
  invalidates no render.
- Remember `--keep-tmp` if you want the cache to help on the next
  run (as in video mode, intermediate renders live in `tmp/`).

## Flags

- `--only <scene-id>`: processes only that scene; the exported PNGs of the
  other scenes are not touched and `manifest.json` lists only the processed scene.
- `--force`: re-renders all variants of the target scenes,
  ignoring the cache.
- `--gpu` / `--vram-warn-mb` work the same as in video mode.

## VRAM and variants

Variants are rendered serially precisely to bound VRAM: only one
`sd` invocation is alive at a time. Even so, high values of
`variants` lengthen the run linearly and keep the model loaded on
the GPU for the whole scene. On a 12 GB card (e.g. RTX 4070)
with SD 1.5 checkpoints at 1280x720, a practical recommendation is **not to exceed
4–6 variants per scene** per batch and to rely on `--only` to iterate
scene by scene; with `--gpu` active, the `--vram-warn-mb` warning is evaluated
before starting.

## Events

In image mode the stream (`--json`) contains per scene only the
`phase_start`/`phase_end` pairs of `render` and `export`, plus one global
`export` step (`scene_id: "_global"`) with the path of `manifest.json`. The shape
of the events does not change; see `docs/events.md`.
