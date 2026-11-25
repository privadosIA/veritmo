# `scenes.yaml` reference

[← Back to the README](../README.md)


```yaml
project: demo-video

# Output selection. `mode` is "video" (default) or "images".
#   - video:  the full TTS → render → clip → concat pipeline writes `file`.
#   - images: only render + export run; final PNGs and a manifest.json land
#             in out/<project>/ and `file` is ignored. Requires only `sd`
#             on PATH (plus ffmpeg when contact_sheet is true) — no piper.
output:
  mode: video
  file: out/demo.mp4        # required in video mode; its directory must be creatable
  contact_sheet: false      # images mode only: also tile all PNGs into contact_sheet.png

# DEPRECATED alias for output.file, kept for one release cycle. Setting it
# emits a warning; setting it AND output.file to different values is an error.
# output_file: out/demo.mp4

# Shared defaults applied to every scene unless noted otherwise.
defaults:
  model: models/v1-5-pruned-emaonly.safetensors  # sd.cpp checkpoint (see scripts/get-models.sh)
  voice: voices/en_US-lessac-medium.onnx          # Piper voice (see scripts/get-piper.sh)
  resolution: 1280x720                             # WIDTHxHEIGHT, passed to sd as -W/-H
  sampler: euler                                   # sd sampling method, e.g. euler, dpmpp_2m
  steps: 30                                        # sd sampling steps
  fps: 30                                           # ffmpeg framerate for the looped image
  subject_prompt: ""        # optional: prepended to every scene's prompt (subject coherence)
  negative_prompt: ""       # optional: appended after each scene's own negative_prompt
  # Alternative to `model` (use only one) for diffusion models that need an LLM
  # text encoder and a VAE, such as Krea 2. sd gets
  # --diffusion-model/--llm/--vae/--cfg-scale instead of -m.
  # model_set:
  #   diffusion_model: models/krea2_turbo-Q4_K_M.gguf
  #   llm: models/Qwen3VL-4B-Instruct-Q4_K_M.gguf
  #   vae: models/wan_2.1_vae.safetensors
  #   cfg_scale: 1                  # optional; omitted = sd's default

scenes:
  - id: scene_intro
    prompt: a wide establishing shot of a futuristic city at dawn, cinematic lighting
    negative_prompt: blurry, low quality, text, watermark
    seed: 42                      # omit this field entirely for a random seed each run
    variants: 1                   # images mode: render N images (seed, seed+1, …); ignored in video mode
    tts_text: Welcome to the city of tomorrow.
    speed: 1.0                    # 1.0 = Piper's natural pace; >1.0 speaks faster
    # voice: voices/other.onnx    # optional: narrate this scene with another voice (default: defaults.voice)
    # speaker: 1                  # optional: which speaker of a voice that holds several (es_ES-sharvard: 0 man, 1 woman)
    motion:                       # optional camera movement (video mode only)
      type: zoom_in               # static (default) | zoom_in | zoom_out | pan_left | pan_right | handheld | rotate_cw | rotate_ccw
      intensity: 0.5              # 0.0–1.0, scales the movement amplitude (default 0.5)
    look:                         # optional finishing (video mode only)
      grain: 0.3                  # 0.0–1.0 sensor noise; 0.0 (default) adds no filter
      white_balance: warm         # neutral (default) | warm | cool | fluorescent

  - id: scene_middle
    prompt: a close-up of a street vendor preparing food, warm evening light
    negative_prompt: blurry, low quality, text, watermark
    tts_text: Every corner has its own story to tell.
    speed: 1.0

  - id: scene_breather            # a silent scene: no narration at all
    prompt: fog rolling over the empty street at dawn
    negative_prompt: blurry, low quality, text, watermark
    silence_seconds: 2.0          # scene duration; tts_text must be empty (mutually exclusive)

  - id: scene_outro
    prompt: a drone shot pulling back from the city skyline at night, neon lights
    negative_prompt: blurry, low quality, text, watermark
    seed: 7
    tts_text: Thanks for watching. See you next time.
    speed: 0.9
```

Notes:

- `id` must be unique across scenes (used for cache artifact paths and `--only`).
- `prompt` must be non-empty for every scene. `tts_text` is required in video mode; in images mode it is optional and ignored (with a warning when present).
- `seed` is a scene-level override; omit it for a random seed on every render (recorded in the cache hash either way, so an explicit seed makes that scene's render reproducible and cacheable across runs, while an omitted seed is still hashed as the sentinel `-1` and therefore still cacheable as long as nothing else about that scene changes).
- `defaults.model` and `defaults.voice` must exist on disk when the pipeline starts; `output.file` does not. A Piper voice (`.onnx`) also needs its companion `<voice>.onnx.json` beside it (Piper aborts without it); `get-piper.sh` and the Models screen install both. Scenes with `tts_text` need `defaults.voice` set; scenes with `silence_seconds` do not.
- Narrated scenes synthesized before this was fixed left invalid `.wav` files in `tmp/`; run once with `--force` to regenerate them.
- `motion` and `look` affect only the clip phase (video mode): changing a preset re-encodes that scene's clip but re-runs neither `sd` nor `piper`. Non-static motion drops `-tune stillimage`; `handheld` jitter is deterministic per scene seed, so it stays cacheable. `rotate_cw` / `rotate_ccw` turn the picture slowly about its centre over the clip, up to `5 + 40 × intensity` degrees, zoomed once so no empty corner shows. `white_balance` presets use ffmpeg's `colortemperature` filter (requires ffmpeg ≥ 4.4).
- A scene with `silence_seconds` is silent: `piper` and `ffprobe` never run for it (and it emits no `tts`/`probe` events), its clip lasts exactly that many seconds, and it carries a real silent AAC track so the final `-c copy` concat never sees mismatched streams.
- One-time cache migration: manifests written before camera-motion support lack the per-scene clip hash, so the first run after upgrading re-encodes every clip once (seconds per scene, no `sd`/`piper` re-run) and then converges back to cache hits.

## Paths, names and limits

A `scenes.yaml` is checked before anything runs, and the same rules apply to the command line and to the server.

- **Paths stay inside the project.** Every field that names a file or a folder (`output.file`, `defaults.model`, `defaults.voice`, the model set, the animate and music models, the pictures, clips and references of a scene, the adapter...) is relative to the project folder. An absolute path (`/home/...`) or a `..` segment is refused with the field and the value. To use a model that lives elsewhere, link it into the project (the projects already carry `models/` and `voices/` links) and name it as `models/x.safetensors`.
- **The output goes under `out/`** (`out/final.mp4`) or is a plain file name.
- **`project` and every scene id** use letters, digits, `.`, `_` and `-`, start with a letter or a digit and have at most 64 characters, because they become file names.
- **Limits.** Steps (`steps`, `high_noise_steps`, `inference_steps`, `dr2_steps`) up to 200, `fps` up to 240, `frames` up to 1000, `variants` up to 64, `silence_seconds` and `duration_seconds` up to 3600, `mp3_bitrate` up to 640, and a resolution up to 4096 on a side and 9,437,184 pixels in all. No preset or example is anywhere near them.
- **A misspelt field is an error** (`step:` instead of `steps:`), with the field name and the line, instead of being ignored.

## Animated scenes (Wan 2.2)

A scene with `animate: true` turns its rendered image into a short video with Wan 2.2 TI2V 5B (an `animate` phase between `render` and `clip`) instead of the zoom/pan of a still. Configure the models once and opt in per scene:

```yaml
defaults:
  animate:
    diffusion_model: models/video/Wan2.2-TI2V-5B-Q4_K_S.gguf
    t5xxl: models/video/umt5-xxl-encoder-Q4_K_S.gguf
    vae: models/video/wan2.2_vae.safetensors
    # optional, with these defaults: resolution: 832x480, frames: 33, fps: 16, steps: 20, cfg_scale: 6
scenes:
  - id: cafe
    prompt: photo of a woman reading in a cafe
    tts_text: ...
    animate: true
    motion_prompt: she slowly turns a page   # optional; defaults to the scene prompt
```

How it behaves:
- One `sd` video run per scene (about 84 s for 33 frames on an RTX 5060 Ti, about 205 s for 65), saved as a forward-then-reverse loop (33 frames become 64, 4 s) so repeating it shows no jump. Clips are never chained: chaining degraded contrast and colour.
- The clip phase fits it to the narration: slowed down by at most 2x, then looped, or cut when the narration is shorter. It is scaled and cropped to `defaults.resolution`. `look` (grain, white balance) still applies; `motion` is ignored with a warning.
- `resolution` sides must be multiples of 16 and `frames` must be 4n+1. A failed animation stops the run with an `animate` error; it never falls back to the still.
- The animation is cached by image, motion prompt, seed and the Wan settings. Changing only the motion prompt re-animates without re-rendering.
- `images` output mode ignores `animate`. As with model sets, launch it with a memory cap on a 30 GB machine.

### Higher quality: Wan 2.2 A14B (two experts)

Wan 2.2 A14B animates better than the 5B (steadier faces, movement that follows the prompt) but takes about eight times longer. It works as two experts: set `high_noise_diffusion_model` and `diffusion_model` becomes the low-noise one:

```yaml
defaults:
  animate:
    diffusion_model: models/video/Wan2.2-I2V-A14B-LowNoise-Q4_K_S.gguf
    high_noise_diffusion_model: models/video/Wan2.2-I2V-A14B-HighNoise-Q4_K_S.gguf
    t5xxl: models/video/umt5-xxl-encoder-Q4_K_S.gguf
    vae: models/video/wan_2.1_vae.safetensors   # the A14B uses the Wan 2.1 VAE, not the 5B's
    # optional, with these defaults in this mode: steps: 10, cfg_scale: 3.5, high_noise_steps: 8, high_noise_cfg_scale: 3.5
```

Without `high_noise_diffusion_model` nothing changes: the same `sd` command, the same defaults (20 steps, cfg 6) and the same cache key as before. With it, `sd` also gets `--high-noise-diffusion-model`, `--high-noise-steps`, `--high-noise-cfg-scale`, `--high-noise-sampling-method euler` and `--flow-shift 3.0` (the value `sd.cpp` documents and the one it was measured with). `high_noise_steps` and `high_noise_cfg_scale` are refused without the expert. Measured with Q4_K_S on an RTX 5060 Ti, 33 frames at 832x480: **684 s** (the 5B: 85 s), 10.3 GB of VRAM and a 21.1 GB RAM peak, on a single image, so treat it as indicative. The memory reservation counts both experts, so an A14B animation runs alone on a 30 GB machine. It is in the model catalog as "Wan 2.2 I2V A14B (Q4)", and the editor has a selector for the expert.

### Video with audio: MiniMax-H3

`defaults.animate.engine: minimax-h3` animates the scene's picture with MiniMax-H3 (Ref2VA: the picture is a reference image) and generates video and stereo audio together. The Q4 weights are about 35 GB, but the engine passes `--mmap`, so `sd` maps the files instead of copying them to RAM: measured on a 30 GB machine with a 16 GB GPU (864x480, 56 frames, 258 s per clip, 13.5 GB of VRAM) the available RAM never fell under 28 GB. Without `--mmap` the same run is killed at 24 GB. An SSD is advised, because the disk serves the weights. The Q2 weights give unusable pictures. The five files (the fourth is the turbo LoRA, see below) are one bundle in Models > Video, which shows the RAM warning:

```yaml
defaults:
  animate:
    engine: minimax-h3
    diffusion_model: models/video/minimax_h3_ref2va_pruned-Q4_K_M.gguf
    llm: models/video/qwen3vl_32b_minimax_h3-Q4_K_M.gguf
    vae: models/video/minimax_h3_video_vae_fp16.safetensors
    audio_vae: models/video/minimax_h3_audio_vae_fp32.safetensors
    # lora: models/video/minimax_h3_ref2v_turbo_4step.safetensors # optional turbo LoRA (drafts)
    # defaults: resolution 864x480, frames 56, steps 20 (4 with `lora`), cfg_scale 1
```

- `first_frame: true` makes the scene's picture the **first frame** of the clip (`--init-img`, the FL2VA model) instead of a reference image (`-r`, Ref2VA): the shot starts exactly from the picture and the subjects stay in frame. `diffusion_model` must then be the fl2va file (`models/video/minimax_h3_fl2va_pruned-Q4_K_M.gguf`, catalog entry "MiniMax H3 First frame"), and the turbo `lora` the first-frame one (`minimax_h3_fl2v_turbo_8step.safetensors`), whose default is **8 steps**. Measured with one picture and seed at 864x480 and 56 frames: 290 s at 20 steps, 168 s with the 8-step LoRA, both with clean faces and both subjects in frame (the Ref2VA run of the same shot lost one of them).
- On a 12 GB card use `resolution: 768x416` (the first-frame bundle with the 8-step LoRA peaked at 11.2 GB of VRAM and took 108 s per clip on an RTX 4070; 864x480 needs 13.5 GB). The catalog entry records it as `low_vram`, and Easy mode applies it by itself.
- `lora` (optional) adds the turbo LoRA (Models > Video, fifth file of the MiniMax H3 bundle): `sd` gets `--lora-model-dir` and `<lora:name:1>` at the end of the prompt, and the default steps become 4. Measured with 864x480 and 56 frames: 96 s per clip instead of 258 s, but with people the faces and the background (above all in the last half second) come out clearly worse than with 20 steps and no LoRA, measured on three shots (v0.1, experimental): use it for drafts and leave `lora` out for the final render.

- The frame rate is fixed at 24 fps; the sides must be multiples of 32 and `frames` on the 17k+5 grid (22, 39, 56, ...), also in a scene's `animation` block.
- The audio track sd generates is dropped by the forward-and-back conversion unless `model_audio: true` is set (see below): by default the project's own audio (voice, music) is what the final video uses.
- In the editor, choose it in Animation > Animation engine > MiniMax H3: it fills the four files and the recommended settings (864x480, 20 steps, cfg 1) with an Undo button. The quality levels do not touch this engine's files and settings.

### A last picture and references

An animated scene with its own picture (`image:`) can end on another picture, or be guided by references. Both are files in the project's `sources/` and are validated when the project is loaded:

```yaml
scenes:
  - id: handshake
    animate: true
    image: start.png          # the first frame
    end_image: end.png        # the clip ends on this one
    motion_prompt: the two friends walk towards each other and shake hands
  - id: kitchen
    animate: true
    image: woman-red.png      # reference picture 1 (the scene's own picture)
    references:
      images: [woman-dark.png]            # reference picture 2, 3, ...
      videos: [dance.mp4]
      audios: [song.wav]
    motion_prompt: The woman from <Picture 1> and the woman from <Picture 2> dance in a kitchen
```

- `end_image` needs `image` and an engine that takes a last frame: `minimax-h3` with `first_frame: true`, or `ltx-2.3`. The clip is converted forward only (it does not go back and forth, which would end on the first picture), plays once, and the last frame is held for the rest of the scene.
- `references` needs `minimax-h3` **without** `first_frame` (the Ref2VA bundle) and cannot be combined with `end_image`. The scene's `image` is reference picture 1; the engine reads the pictures as `<Picture N>` in the order of the YAML and the prompt gets a sentence that names them. Reference pictures are scaled down to the canvas, a reference video becomes PNG frames at 24 fps, and a reference audio a stereo 32 kHz WAV (the sound of a reference video is not used).
- Measured limits (768x416, 22 frames): 2 pictures, 1 video of 2 s and 1 audio of 3 s on a 12 GB card (55 s, 11.3 GB of VRAM); 6 pictures, 2 videos of 2 and 3 s and 3 audios of 4 s on a 16 GB card (129 s, 15.2 GB). Nothing above that was measured.
- The content of every one of these files is part of the cache key of the clip: changing, adding or reordering one makes it again; a scene without them keeps the key it had.

### Faster video with audio: LTX 2.3

`defaults.animate.engine: ltx-2.3` animates the scene's picture with the distilled LTX-2.3 (GGUF Q4_K_M) through the same `sd` binary: the picture is the **first frame** of the clip (`--init-img`) and the prompt only says what moves. No ComfyUI and no Python are involved. It is one bundle of five files in Models > Video, all from open repositories (no access request):

```yaml
defaults:
  animate:
    engine: ltx-2.3
    diffusion_model: models/video/ltx-2.3-22b-distilled-1.1-Q4_K_M.gguf
    llm: models/video/gemma-3-12b-it-qat-UD-Q4_K_XL.gguf              # Gemma 3 text encoder
    vae: models/video/ltx-2.3-22b-distilled_video_vae.safetensors
    audio_vae: models/video/ltx-2.3-22b-distilled_audio_vae.safetensors
    connectors: models/video/ltx-2.3-22b-distilled_embeddings_connectors.safetensors
```

- The defaults are 768x416, 49 frames (2 s), 8 steps, cfg 1 and 24 fps (fixed). The sides must be multiples of 32 and `frames` on the 8k+1 grid (9, 17, ..., 49, 57, ...), also in a scene's `animation` block (`sd` rounds other sizes up).
- `connectors` only applies to this engine; `lora` and `first_frame` do not apply to it (the picture is always the first frame).
- Measured with the same picture and seed (two friends on a bench), 768x416, 49 frames, 8 steps: 116 s on a 12 GB RTX 4070 (peak 11.1 GB of VRAM: it fits with little margin) and 145 s on a 16 GB RTX 5060 Ti. The ~26 GB of weights are read with `--mmap`; it was tested with 30 GB of RAM and the catalog asks for 24 GB.
- Like MiniMax-H3, the audio the model writes is dropped by the forward-and-back conversion unless `model_audio: true` is set.
- `model_audio: true` (only with `minimax-h3` and `ltx-2.3`; off by default) keeps that audio as **ambience**: the animate phase writes its forward part (the model's own length, never the reversed half) with a 0.3 s fade out to `tmp/scene_<id>.anim.ambient.wav`, and the clip phase mixes it at a fixed −18 dB under the scene's narration (ducked by it when the scene has voice), from the scene's start and **once**: when it ends, the narration (or silence) goes on alone. The project's `bgm` is mixed over the result as usual. Scenes with their own `clip:`, a trimmed animation or a `motion_graphic` get no ambience. With `ltx-2.3`, `model_audio_speech: true` (needs `model_audio`) says that audio carries a **voice** (a line the movement prompt asks the model to say, e.g. `The person says clearly: "Hola a todos"`) and mixes it at full level instead of −18 dB; the frames must be long enough for the line (about 2.5 words a second, 73 frames ≈ 3 s). Measured with a 3 s clip: Whisper reads the line back word for word in Spanish and almost so in English. Turning it on re-runs `animate` and `clip` of the animated scenes; the other scenes stay cached.
- In the editor, choose it in Animation > Animation engine > LTX 2.3: it fills the five files and the recommended settings with an Undo button, and Easy mode can plan with it.

### Lighter alternative: AnimateDiff (SD 1.5)

`defaults.animate.engine: animatediff` animates the scene's picture with AnimateDiff instead of Wan: lighter and faster, with a **subtle** movement (waves, wind, a portrait that breathes) that keeps the picture and the faces. Use Wan when people or the camera must really move. It needs an SD 1.5 checkpoint and the `mm_sd15_v3` motion module (about 836 MB, Models > Video > "AnimateDiff v3"; the SD 1.5 checkpoint is the one from Models > Imagen, it is not downloaded twice):

```yaml
defaults:
  animate:
    engine: animatediff
    checkpoint: models/v1-5-pruned-emaonly.safetensors   # must be SD 1.5, not SDXL
    motion_module: models/video/mm_sd15_v3.safetensors
    # optional, with these defaults: resolution: 512x512, frames: 16, fps: 8, steps: 25, cfg_scale: 7, strength: 0.75
```

- Without `engine` (or with `engine: wan`) nothing changes: same command, same defaults, same cache keys. The Wan files are not needed with AnimateDiff.
- `resolution` sides must be multiples of 8 and `frames` between 8 and 32 (16 is what the module was trained on; it is not 4n+1 as in Wan). `strength` (0 < s <= 1) is how far the frames may move away from the picture: higher moves more. The scene's own `animation` block (resolution, frames, steps) follows the same rules.
- The picture is scaled to cover the animation resolution and centre-cropped (never stretched) before `sd` sees it, so choose a resolution with the video's aspect (for example `640x352` for `1280x704`). The clip is then looped forward-and-back and cropped to `defaults.resolution` like a Wan clip.
- Measured with `sd` master-948: 16 frames at 512x512 take about 80 s on the RTX 5060 Ti and 57 s on the RTX 4070 (12 GB), with a peak of 5.6 GB of VRAM there. The command carries `--offload-to-cpu --diffusion-fa`: without them the 12 GB card runs out of memory.
- The animation is cached by picture, prompt, seed, checkpoint, module, strength and the other settings; changing only `strength` regenerates just that clip.
- In the editor, the Animation section has an engine selector: choosing AnimateDiff writes the installed module and SD 1.5 checkpoint and applies the catalog preset (with Undo), and the Baja/Media/Alta levels and each scene's own quality use AnimateDiff's values (Low 384x384, 8 f, 12 steps; Medium 512x512, 16 f, 25; High 640x640, 24 f, 38) without changing the engine.

## Scenes from your own picture

A scene can use a picture you already have instead of generating one: put a `.png`, `.jpg`, `.jpeg` or `.webp` in the project's `sources/` folder (the editor uploads it for you: «Mi imagen») and name it with `image`:

```yaml
scenes:
  - id: foto
    image: lighthouse.jpg      # a file name in sources/, not a path
    tts_text: "Esta es mi imagen."
    speed: 1.0
    motion: {type: rotate_cw, intensity: 0.6}
```

The picture is fitted to the project resolution (scaled to cover, centre-cropped) and from there the scene is like any other: narration, camera motion (including the rotation presets), look, grain and, with `animate: true`, the Wan animation (set `motion_prompt`, since there is no prompt to fall back on). `prompt` is not required; `negative_prompt`, `lora` and `variants` are ignored with a warning. The fit is the scene's `render` phase and is cached by the content of the file, so replacing it re-runs the scene; a project where every scene brings its own picture needs neither `sd` nor a model.

## Scenes from a clip that already exists

A scene can use a video you already have instead of generating a picture and animating it, the way it can use its own picture. This is how you mix in a clip made elsewhere: for example, the one shot that came out deformed with the project's Wan 5B, remade with the A14B in another project.

```yaml
scenes:
  - id: s06_stand_up
    silence_seconds: 1.5       # or tts_text: the scene lasts what its narration lasts
    clip: stand-up.mp4         # a file name in sources/ (mp4, mov, webm or mkv), not a path
```

**Using only part of the clip.** `clip_start` and `clip_end` (seconds from the start of the file, both optional) pick the part of the video the scene uses; the start defaults to 0 and the end to the end of the file:

```yaml
    clip: stand-up.mp4
    clip_start: 0.4            # skip the first 0.4 s
    clip_end: 1.9              # and stop at 1.9 s: the scene uses these 1.5 s
```

The part is cut exactly and fitted to the scene's length as a whole clip is (cut when longer; slowed up to 2x and repeated, the part and not the whole file, when shorter). The scene picture is the frame at `clip_start`. `clip_end` must be greater than `clip_start`, a start or an end past the end of the file stops the run with a message that names the scene and the length of the file, and both only exist with `clip`. In the editor the clip has **From** and **To** fields with buttons that take the instant the preview is paused at.

**The same on a generated animation.** A scene with `animate: true` accepts `clip_start` and `clip_end` too, to use only the good part of the Wan animation (for example the first 1.5 s, before the legs bend, or from 0.4 s when the movement starts late). The times count from the start of the animation and only its **forward part** can be chosen (frames ÷ fps; the reverse half of the ping-pong is shown only if the scene is longer than the part); the end defaults to the end of that part, and a start or end past it stops the run with the scene and the length of the part. Changing the trim does **not** make the animation again: only the clip phase runs (an ffmpeg cut). **Speed of the scene's video.** `clip_speed` (0.25 to 4) says how fast the video plays: 1 is its own speed, 0.5 half speed (slower), 2 double. It works on a scene with `clip` or with `animate: true`, after the trim (the part is cut first, then retimed): the part plays at exactly that speed for as long as the scene lasts, **cut** when it lasts longer and **repeated** when it lasts less. Without `clip_speed` nothing changes: a part shorter than the scene is slowed automatically by up to 2x. (`speed`, on its own, is the narration's speed.) Changing it does not make a generated animation again, only the clip phase runs. The editor has a **Speed** field next to From and To; empty means automatic.

The animation section of the editor has the same From and To fields, with the animation's preview when the run kept `tmp/`; the numbers work without it.

The scene's `render` phase writes the first frame of the clip as the scene picture (the editor and the review show it) without loading any model, and the `clip` phase builds the scene from the file: cover-cropped to the project resolution, converted to its frame rate, **cut to the scene's length when the video is longer** and, when it is shorter, slowed (up to 2x, as animated clips are) and then repeated. The scene is cached by the **content** of the file, so replacing it re-runs the scene and renaming it does not. `clip` cannot be combined with `image`, `init_image`, `animate`, `motion_prompt`, `animation` or `render`, only exists in video projects, and `prompt`, `negative_prompt`, `lora` and `variants` are ignored with a warning. A project where every scene brings a picture or a clip needs neither `sd` nor a model.

In the editor, **My clip** (next to "Generate with AI" and "My image") lets you choose a video of `sources/`, upload one, or take **a clip another project kept** (see below). A clip of another project is **copied** into this project's `sources/`: the scene never depends on the other project, so deleting that project, or its runs with their files, does not affect it.

**Kept clips.** After every run, whether or not `--keep-tmp` is set, each scene's clip is kept at `out/clips/<scene>.mp4`; the editor previews a scene with it (or with `tmp/scene_<scene>.mp4`). These are the latest clip of each scene, not a history. A scene that animates also keeps its **complete animation** (the ping-pong file, forward and back, about twice the length of the part a scene shows) at `out/clips/<scene>.anim.mp4`, so a good generated shot can be reused in another project and trimmed again from scratch: the clip picker offers it as «complete animation (forward and back)» and copies it into `sources/` as `<project>-<scene>-anim.mp4`. When a scene stops animating or is removed, the next run prunes its animation like its clip.

**No clip is left loose.** After each run the clips of scenes that are no longer in the project are removed from `out/clips/`. When you save the project, a video of `sources/` that a scene used as its clip and no scene uses any more (the scene was deleted or given another clip) is removed from `sources/`, unless another scene still uses it; a video that was never a scene's clip is never removed this way, and the editor lists the videos no scene uses so you can delete them. Deleting a project, or a run **with its files**, also removes the kept clips. A dataset export cannot carry a clip (it lists it in the notes); the zip package does.

## Motion graphics and overlays

An animated **HTML page**, written by the language model and drawn with Chrome, is a third source of video next to a generated picture and your own clip. It is sharp, costs no GPU and is exactly reproducible, which makes it the right tool for text that diffusion models draw badly: titles, lower thirds, credits, lists, diagrams and charts. Two uses:

**A scene that is a motion graphic** (`motion_graphic`): the whole scene is the page.

```yaml
scenes:
  - id: s01_ideas
    motion_graphic: three bullet points appearing one by one over a dark blue background, a thin gold line under the title "Three ideas"
    tts_text: Three ideas to remember.      # or silence_seconds: the video lasts as long as the narration
```

**An overlay on any scene** (`overlay`): an animated title, lower third or credits drawn over the scene's picture, animation or own clip, with the picture visible around it.

```yaml
scenes:
  - id: s02_lighthouse
    prompt: a lighthouse at dusk
    tts_text: The coast at the end of the day.
    overlay:
      text: Chapter 1 — The coast     # the text as it is, or a description of what to show
      position: top                   # top, center or bottom (default bottom)
      start: 0.5                      # seconds within the scene; absent = from the start
      end: 3                          # absent = until the end of the scene
```

`motion_graphic` cannot be combined with `image`, `init_image`, `clip`, `animate`, `motion_prompt`, `animation` or `render`; `prompt`, `negative_prompt`, `lora` and `variants` are ignored with a warning. Both only exist in video projects. `overlay.end` must be greater than `overlay.start`, and for a silent scene neither may pass `silence_seconds`.

**How it works.**
- The first time a scene needs its page, the model of the `motion_graphics` task (a text model; choose the provider in *Tasks* and the model in *Settings > Model for motion graphics*, since a big one writes better code than the prompt assistant's. With none chosen, an Ollama uses `gemma4:26b`, or else `gemma4:31b`, if installed, and otherwise the assistant's model: on the same briefs `gemma4` kept the words and the hierarchy of the description where a coding model cut the text or copied the example) writes it from the description, the scene's narration, the project's size and its `subject_prompt` as the style. The page is **checked in the browser** before it is kept (a complete document, nothing loaded from outside, no script errors at the first, middle and last frame, and not blank or static; and its text kept inside the frame and apart: no text partly out of the frame on its left or right at the middle or last frame, and no two text boxes overlapping by more than a fifth of the smaller one at the last frame; a cut at the top or the bottom is allowed so credits can leave); if it fails, the model is asked again with the problem, up to 3 attempts, and the scene fails with the last problem if none works. An Ollama on this machine loads the model into the GPUs only while it writes and drops it right after, so the pictures of the same run find their memory free.
- **The timeline.** A page may declare its animation as data instead of CSS, in one `<script type="application/json" id="motion-timeline">` block: `{"version": 1, "tracks": [{"id": "title", "label": "Title", "target": "h1", "start": 0.2, "duration": 0.9, "ease": "out", "stagger": 0.12, "from": {"opacity": 0, "y": 36}, "to": {"opacity": 1, "y": 0}}]}`. `target` is a CSS selector (with `stagger`, each matched element starts that many seconds after the previous one, in document order); `start` and `duration` are seconds; `from` and `to` set the same properties, among `opacity` (0 to 1), `x` and `y` (px), `scale`, `scaleX`, `rotate` (degrees), `blur` (px) and `reveal` (0 to 1: the share of the element shown, from the left); a property in only one of them is left alone. `ease` is `linear`, `in`, `out` (the default, cubic-bezier .22,1,.36,1), `in-out` (.65,0,.35,1), `out-back` (.34,1.56,.64,1) or four numbers of a cubic-bezier. Before its start an element has its `from` values and after its end its `to` values. The script that applies the tracks is part of the clock the renderer and the editor's preview use, not of the page, so the preview and the video show the same; it runs before the page's own `window.__seek`. The model is asked to declare the entrances this way (CSS stays for continuous decoration of other elements) and the check rejects an invalid block (JSON, version, ids, selectors that match nothing, times outside the clip, unknown properties or curves, a CSS animation on a targeted element). Pages without the block run exactly as before.
- **A scene made from a part of the catalog.** Instead of asking the model for a whole page, a motion scene can name a part of the catalog that the music motion pieces use (a terminal that types, a search box with suggestions, a button a cursor presses, a counter that rolls, a table that fills, a sunburst, a toast, a stamp...; `GET /api/music-motion/catalog` lists them with their parameters and limits):

  ```yaml
  defaults:
    motion:
      bpm: 100                      # the tempo of the scenes' clock (default 120)
      design: {dark: "#0b0f14", light: "#e8f1ec", accent: "#2bff88", display: heavy, body: mono, default_mode: dark}
  scenes:
    - id: s01_count
      motion_part: roll-counter     # a part of the catalog
      motion_params: {from: 10, to: 0, beats: 4, label: years}
      motion_text: it is closer than it looks   # a line in the verse layer for the whole scene
      motion_bpm: 90                # optional: this scene's own tempo
      silence_seconds: 6
    - id: s02_chosen
      motion_part: auto             # the model chooses a part and fills its parameters from the description
      motion_graphic: a search box that types a question and lists four suggestions
      silence_seconds: 6
  ```

  The page is built from the part, the design and a clock with a beat every `60 / bpm` seconds, with no call to the model, and stored like any other page. `motion_part: auto` needs `motion_graphic` and asks the model once; it is validated against the limits of the parts and, when the model cannot choose a valid one, the scene falls back to the whole page written by the model and the run says so. `motion_params`, `motion_text` and `motion_bpm` need `motion_part`; `motion_text` is shown as one line (the verse styles that need the time of each word fall back to plain for it); the design has the same fields as the design of a music piece (`verse_style` and `hit_style` too) and, without `defaults.motion.design`, a fixed palette is chosen from the words of `subject_prompt`. A part is refused if it is not in the catalog or a parameter is not its own or outside its limits, naming the scene, the part and the parameter. `motion_part` cannot be combined with `image`, `init_image`, `clip`, `animate`, `motion_prompt`, `animation` or `render`.

  **A song put on the scenes.** `defaults.motion.song: song.mp3` (an `.mp3` or `.wav` of `sources/`) makes the scenes made from parts follow that song instead of a fixed tempo: its beats are followed in the audio (a song made by a model does not keep the tempo it was asked for), every scene's clock starts at the position of the song where the scene starts (the scenes follow one another, so a scene starts where the ones before it end) and the song is the soundtrack at full volume unless the project has a `bgm` of its own. `motion_bpm` is then ignored (with a warning). Use silent scenes (`silence_seconds`) with a song: their place in it is exact; the place of a narrated scene is estimated from the words of its narration, and the run says so. A scene that starts after the end of the song keeps a fixed tempo and the run says so. The map of the song is kept in `motion/song-map.json` keyed by the audio, and the scenes are drawn again only when the song changes. A music motion piece (see `docs/MUSIC_MODE_MOTION.md`) is this: a video of scenes made from parts with a song put on it, and the same scenes work with or without one.
- The page is stored in the project as `motion/<scene>.html` (`motion/<scene>.overlay.html` for the overlay) with its details in `motion/<scene>.meta.json`. **It is reused by every later run without calling the model**; edit the file (or use *Edit the page* in the editor) and the next run draws your version. A new page is written only when you ask (*Write another page* in the editor, or `POST /api/projects/{id}/scenes/{scene}/motion/generate`). To change only how it moves, ask in words (`POST /api/projects/{id}/scenes/{scene}/motion/revise` with `{"instruction": "slower entrances"}`; the dashboard has no control for it yet): the model writes the new content of the timeline block only, the rest of the page stays byte for byte as it was, and it needs a page that declares its animation as a timeline (see **The timeline**); a page that animates with its own script is written again instead. The folder travels with the project package (export and import).
- The page is **played frame by frame**, not recorded: before every frame its clock is set to the exact second (CSS animations, the Web Animations API, GSAP's timeline and `requestAnimationFrame` follow it; a page may also define `window.__seek(t)`), so the same page, length, frame rate and resolution always give the same frames, however busy the machine is. `window.__motionDuration` tells the page how long the scene really is.
- **The beat clock (pages of a music piece).** A page that belongs to a music motion piece also receives the song map as `window.__motionSong` (`{bpm, beats, sections, words, loudness}`, see `specs/music-motion`) and, before every frame and before its own `window.__seek`, `window.__beat = {number, position, section, sectionIndex, bpm, time}`: `number` is the index (from 0) of the last beat at or before the frame (-1 before the first), `position` how far inside that beat the frame is (0 up to, never, 1) and `section` the tag of the lyrics' section that holds it. It is part of the same virtual clock, so the preview and the video agree and the same piece always gives the same frames. A page of an ordinary scene gets neither. A complete example is `docs/examples/beat-page.html`: a dot that swells on every beat and the name of the section.
- The video lasts as long as the scene (its narration, or `silence_seconds`), at the project's resolution and `fps`. A motion-graphic scene's first frame is its picture in the editor and the review. An overlay is drawn on a transparent background (VP9 with alpha, or ProRes 4444 when ffmpeg lacks it) and composited over the scene in the `clip` phase; one that starts after the end of a narrated scene is skipped with a warning.
- The result is cached by the **content** of the page, the length, the frame rate and the size, so a longer narration, an edited page or a moved overlay is drawn again and nothing else is. Transitions, voices and music work on these scenes as on any other.
- **Offline sandbox.** The page runs in a throw-away browser profile with every network request and every file blocked (only the page itself is served), with time limits for loading and for each frame; the browser is stopped whatever happens. System fonts only (`sans-serif`, `serif`, `monospace`): text may look slightly different on another machine.
- Chrome or Chromium is found by itself (`google-chrome-stable`, `google-chrome`, `chromium`, `chromium-browser`) or by the path saved in Settings. If a project has such scenes and no browser is found, the validation names the scenes and says how to fix it; projects without them are not affected. A project whose scenes are all motion graphics needs neither `sd` nor a diffusion model.

## Scenes rendered by Blender

A scene of a video project can come from a `.blend` file (a 3D scene made in Blender, or [built from a description](#scenes-built-from-a-description)) instead of a prompt: it is rendered headless with the Cycles engine, frame by frame, and used as the scene's video. Put the file in the project's `sources/` folder (it can be uploaded like any other source) and name it:

```yaml
scenes:
  - id: titulo
    silence_seconds: 4
    blender:
      file: escena.blend        # a .blend in sources/
      samples: 32               # Cycles samples per pixel (default 64, up to 4096)
      device: auto              # auto (default), gpu or cpu
      frames: [1, 48]           # optional: [first, last] frame of the file's animation; absent = the file's own range
      inject:                   # optional: things to set in the file, by name, before rendering
        Title: "Hola \"Blender\""   # a text object (its text)
        Ball: "#ff2020"         # a material (its colour: #rrggbb, or 3 or 4 numbers)
        Cube.speed: 2           # a custom property of an object ("Object.property")
```

- **Names.** `inject` finds each name first as an object (a text object gets its text), then as a material (its Principled BSDF colour), and last as `Object.property`, a custom property of that object (a number, text, boolean or list of numbers). A name that is none of them stops the scene, naming it. The texts may hold quotes, backslashes and line breaks: they travel as data and are never part of a script.
- **Length.** The video lasts as long as the scene (its narration, or `silence_seconds`) at the project's resolution and `fps`: when the file's animation is shorter, its last frame is held; when it is longer, it is cut.
- **Where it renders.** With `device: auto` the render uses OptiX on **one** NVIDIA card, the one the run gives the scene (`--gpu`, one scene at a time on each card of a list), so the other card stays free; with no card in the run it uses the CPU (about three times slower) and the run says so. `device: cpu` never uses a card; `device: gpu` fails, saying why, when there is none. The Intel integrated GPU is not used. A 48-frame 1280x720 scene at 32 samples took about 19 s on one card (the OptiX denoiser runs on the card too).
- **Cache.** The scene is rendered again only when the content of the `.blend`, the injected values, `samples`, `frames`, the size, the frame rate, its length or Blender's version change (not the card or the CPU).
- **Safe.** Blender opens the file with the auto-run of its scripts disabled, and no Python file is written: the small script that applies `inject` and picks the device is passed inline.
- **Blender is optional** (4.2 or newer, `blender` in the `PATH`). A project with such scenes is refused, with the reason and how to install it, when Blender is missing; every other project is unaffected. A project whose scenes are all Blender scenes needs neither `sd` nor a diffusion model. It cannot be combined with `image`, `init_image`, `clip`, `animate`, `motion_graphic`, `motion_part`, `motion_prompt`, `animation` or `render`, and only works in video projects. EEVEE is not offered (it cannot be kept off the other cards).

### Scenes built from a description

A 3D scene does not have to start in Blender: with `build` instead of `file` the scene is **described** in a small JSON file (a `.scene.json` in `sources/`), built inside Blender, rendered exactly like a `.blend` scene and **kept as a `.blend`** that you can open and touch up.

```yaml
scenes:
  - id: titulo
    silence_seconds: 5
    blender:
      build: titulo.scene.json  # a description in sources/ (a plain file name); not with file, inject or frames
      samples: 16               # optional; device and samples mean what they mean for a .blend scene, but the
                                # default for a built scene is 16 (see "Measured" below), not 64
```

The whole example is in `testdata/blender-build/` (a 5 s clip at 30 fps and 120 bpm, so 10 beats: a studio backdrop with no horizon, the 3D title "Hola" in the design's display font rising with an overshoot by beat 1.2, the camera dollying in over the ten beats, and an accent sphere that hits on every beat from 1 to 9 and decays by the half beat). This is the shape of the description (contract **version 1**, abbreviated; the project of the example sets `defaults.motion.bpm: 120` and a design whose display font is `playfair-display`):

```json
{
  "version": 1,
  "world": { "color": "bg", "strength": 1.0 },
  "camera": { "lens": 50, "keys": [
    { "beat": 0,  "location": [0, -9, 1.2], "target": [0, 0, 0.6] },
    { "beat": 10, "location": [0, -7, 1.0], "target": [0, 0, 0.6], "ease": "inout" } ] },
  "lights": [
    { "type": "area", "location": [3, -4, 5], "target": [0, 0, 0], "power": 600, "size": 3 },
    { "type": "sun", "rotation": [50, 0, 30], "power": 2 } ],
  "materials": {
    "title":  { "color": "fg", "roughness": 0.35 },
    "pulse":  { "color": "accent", "emission": 0 },
    "ground": { "color": "bg", "roughness": 0.9 } },
  "objects": [
    { "id": "stage", "type": "backdrop", "size": 40, "curve": 4, "material": "ground" },
    { "id": "title", "type": "text", "text": "Hola", "font": "display", "size": 1.2, "extrude": 0.12, "bevel": 0.015, "align": "center", "material": "title",
      "keys": [ { "beat": 0,   "location": [0, 0, -2.0], "rotation": [90, 0, 0], "scale": 1 },
                { "beat": 1.2, "location": [0, 0, 0.4], "ease": "back" } ] },
    { "id": "pulse", "type": "sphere", "size": 0.7, "material": "pulse",
      "keys": [ { "beat": 0,   "location": [1.8, 0.5, 0.35], "scale": 1, "emission": 0 },
                { "beat": 1,   "scale": 1.15, "emission": 4, "ease": "step" },
                { "beat": 1.5, "scale": 1, "emission": 0, "ease": "out" } ] } ]
}
```

- **Units.** Blender metres, Z up, rotations in degrees (Euler XYZ). A key says **when** with exactly one of `t` (seconds from the start of the scene) or `beat` (a number from 0, fractions allowed: `2.5` is the half beat after beat 2); a beat is turned into seconds with the project's tempo, `defaults.motion.bpm` (so at 120 bpm beat 4 is second 2, which is frame 61 at 30 fps). A project with no `defaults.motion.bpm` refuses a description that uses `beat`, naming the first key that does. A key gives only the channels it changes (`location`, `rotation`, `scale` as one number or three, `emission`); the others keep what the previous key left. The camera and the area and point lights can aim at a `target` instead of a `rotation` (the camera's target can be keyed too). A key at the length of the scene (`t: 5`, or `beat: 10` at 120 bpm) lands on its last frame, 150 for 5 s at 30 fps.
- **Objects.** `text` (`text` of up to 120 characters, `extrude`, `bevel`, `align`, and an optional `font`, see **Fonts**), `cube`, `sphere`, `cylinder`, `cone`, `torus`, `plane` and `backdrop`; `size` is the edge, the diameter or the text height. A `backdrop` (at most one per scene) is a photo-studio floor that curves up into a back wall, so there is no horizon line behind the objects: a floor `size` deep and wide, joined to a wall `size / 2` high, at +Y, by a quarter circle of radius `curve` (required, from 0.1 to `size / 2`; 16 segments, smooth shading, centred on the origin, no modifier so the `.blend` opens the same anywhere). Give it the colour of the world and it melts into it. An object whose material has `emission` keys gets its own copy of the material, so a flash on one object does not light the others.
- **Easing.** `ease` is how the interval that **ends** at that key is travelled: `inout` (the default, a smooth Bezier), `linear`, `in`, `out`, `back` (overshoots), `bounce`, `elastic` and `step`. The ease of a channel's first key is not used. `step` is for hits: the channels the key gives hold their previous value until the key's frame and jump to the key's value on it, so a flash is dark on the frame before the beat and bright on the beat, with no ramp into it (Blender stores the interpolation on the key that *starts* an interval, so the script sets it to constant on the previous key of those channels). A typical hit is two keys: `{ "beat": 4, "emission": 4, "scale": 1.15, "ease": "step" }` then `{ "beat": 4.5, "emission": 0, "scale": 1, "ease": "out" }`.
- **Fonts.** `font` is `display`, `body`, or the name of a `.ttf` or `.otf` in `sources/`. `display` is the project's design display font (weight 700 when the server's pack has it, else 400) and `body` its body font (400), taken from the pack that comes with the program (Inter, Space Grotesk, JetBrains Mono, Playfair Display, Bebas Neue and Fraunces, and the old names `grotesk`, `mono`, `serif` and `heavy` that map to them), so a 3D title matches the 2D parts of the same piece. A design whose font is a system stack (`rounded`, `condensed`) has no file to give and refuses `display` or `body`, naming it. Blender does not read WOFF2, which is what the pack is, so the server decodes the font once to TrueType with a decoder written in Go ([tdewolff/font](https://github.com/tdewolff/font), MIT; checked against fontTools on all 11 files of the pack: same glyph count and same advance for "Hola") and keeps it in the cache folder (`~/.cache/veritmo/fonts/<sha256 of the WOFF2>.ttf`, `$XDG_CACHE_HOME` honoured). The `.blend` packs the font, so it opens without that cache. Blender's built-in font when no `font` is given. Bebas Neue has only capitals, as on the web.
- **Colours.** `#rrggbb`, or `bg`, `fg` and `accent`, taken from `defaults.motion.design` (its `dark`, `light` and `accent`) or from a fixed palette when the project has no design, so a 3D scene takes the same colours as the rest of the piece. The scene is rendered with the *Standard* view transform so those colours stay those colours.
- **Limits, checked before Blender starts.** Version 1, a file of up to 256 KiB, 64 objects, 8 lights, 32 materials, 400 keys per object or camera, `t` (or the second a `beat` falls on) between 0 and the length of the scene, exactly one of `t` and `beat` in each key, at most one `backdrop`, coordinates within ±1000, `size` in (0, 1000], ids matching `^[a-z][a-z0-9_-]{0,31}$` and not repeated, and every `material` and `font` named must exist. A fault is named by its place in the JSON (`objects[3].keys[2].t: 7 is outside the scene (0 to 5 s)`; for a beat, `objects[0].keys[1].beat: beat 12 falls at 6 s, outside the scene (0 to 5 s)`), not by a Python error. `build` and `font` are plain file names of `sources/`: a path, `..` or a link that leads out of the folder is refused.
- **Safe.** The description is read as data by the same inline script (no Python file is written) and its values are never evaluated, formatted into code or used as a name except through fixed tables: a text like `__import__('os')` is just those characters.
- **The `.blend`.** Before rendering, the built scene is saved, with its fonts packed, as `tmp/scene_<id>.blend`, and it can be downloaded from the project's artifacts (`GET /api/projects/{id}/artifacts/tmp/scene_<id>.blend`). It is saved by Blender 5.3 and opens in **Blender 5.3 or newer** (an older Blender may refuse a file saved by a newer one). It carries no device preference of this machine. A render that fails after the build still leaves it, and the error says so.
- **Cache.** The scene is rendered again when its description **as resolved** changes (every key in seconds with the project's tempo, every colour as `#rrggbb` with the design's, the world's `bg` and a material's `fg` included when they are not given), so a new `bpm` renders a scene that uses beats and a new accent renders one that names `accent`, while a tempo or a colour the scene does not use changes nothing; when a font it uses changes (the file of `sources/`, or the design's display or body font); when `samples`, the size, the frame rate, its length or Blender's version change; or when its kept `.blend` is missing.
- **Measured.** The example at 1280x720, 30 fps (150 frames) took **45.6 s at 16 samples and 64.1 s at 32 samples** on one card (an RTX 5060 Ti, OptiX; building the scene takes a fraction of a second of that). The mean SSIM of 16 against 32 samples over every 15th frame (10 frames) is **0.9988** (the lowest, 0.9985), so the default for built scenes is **16 samples** (`DefaultBuiltSamples`; a `.blend` scene keeps 64 because its materials are the author's). The rule was: 16 if the mean SSIM is 0.98 or more, else 32. The clip, the `.blend`, frames 1, 30, 31, 75 and 150 and the log of the run are left in the shared folder `blender-build-2/`.
- **Checked by the tests.** With `VERITMO_REAL_BLENDER=1` the tests open the kept `.blend` in a clean Blender in another folder (`--factory-startup`), render frames 1, 75 and 150 on the same card and compare them with the pipeline's by SSIM (at least 0.99; measured 0.9975 to 0.9986 at 320x180 and 8 samples); they read the emission of a `step` key from the saved file (0 on frame 60, 4 on frame 61), check that a backdrop has no horizon edge where a plane has one (24 against 0.8 levels of luminance between neighbouring rows), that the design's font is drawn and packed in the file, and that the sphere is brighter on the frame of each beat from 1 to 9 than on the one before.
- **Checked on another machine.** The `.blend` of the example was opened on the owner's Mac with Blender 5.3 or newer and plays the same animation as the clip (the title rises with its overshoot, the camera moves in, the sphere flashes on the half seconds).
- **Blender version.** Built scenes were tried with Blender 5.3 only (the builder also has a path for the older, pre-4.4 animation API, which is untested).
- **Not yet.** There is no asset library, the beats are the project's fixed tempo and not the song's, and nothing looks at a frame to correct the scene. EEVEE stays out for the reason above.

### Models from files

A 3D scene can place a **model** made elsewhere (a character, a prop) instead of building it from primitives: an object of `type: "model"` names a `.glb` of the project's `sources/` (binary glTF, up to 32 MiB and 500,000 triangles; upload it like any source, see `docs/http-api.md`), and may play one of the animations the file carries.

```json
{ "id": "ana", "type": "model", "file": "character-female-a.glb", "size": 1.7,
  "animation": { "clip": "sprint", "offset": 0.2 },
  "keys": [ { "beat": 0, "location": [-5, -0.5, 0], "rotation": [0, 0, 90] },
            { "beat": 10, "location": [5, -0.5, 0], "ease": "linear" } ] }
```

- **Fields.** `file` is a plain `.glb` name of the sources. `size` is the model's **height** in metres whatever the file's own units (its bounding box along Z, in the pose it is saved in; the helper objects the importer adds are not counted), and a `scale` key multiplies it. `fit` (optional) says which dimension `size` is: `"height"` (the default) or `"largest"`, the longest of the three dimensions of the model, which is what a flat or wide thing needs: a plate 3 cm tall and 30 cm wide placed with `"size": 0.3` and `"fit": "largest"` is 30 cm wide, and with no `fit` it comes out ten times too big (its height would be 30 cm). `material` (optional) replaces every material of the model with one of the description, so a colour or an `emission` key applies to it; without it the model keeps its own textures. `text`, `font`, `curve` and the other fields of a text or a backdrop are refused on a model. At most 16 models in a scene. The contract stays at version 1: the new type is additive.
- **Position.** The model's origin (its feet, for a character) is the point `location` places, and at `rotation` `[0, 0, 0]` it faces the negative Y side (toward a camera at `[0, -9, 3]`); `[0, 0, 90]` faces +X and `[0, 0, -90]` faces -X. The `keys` move the model as a whole, in seconds or in beats, with the same eases as any object, while its clip plays.
- **Animation.** `clip` is one of the file's animations (the check lists them when the name is wrong: `"run" is not in character-female-a.glb (clips: idle, sprint, walk)`), `speed` multiplies how fast it plays (0.1 to 4, 1 by default), `loop` repeats it for the whole scene (true by default) and `offset` delays it by that many seconds, so two characters on the same clip are out of step (for a looped clip it is a phase; a clip that does not loop holds its first pose until then and its last pose after its end). A clip keeps its own duration in seconds whatever the project's frame rate (the 12 frames of `sprint` in Kenney's characters are half a second at 30 fps and at 24). A clip is a cycle in place: moving the model across the scene is what its `keys` are for. A model with no `animation` stays in the pose it is saved in (a T pose, for those characters).
- **The pictures a model needs.** A `.glb` may keep its textures in another file: Kenney's 43 characters all name `Textures/colormap.png`. Upload that picture as an ordinary source (`colormap.png`): the model is found with it by the picture's last name, and a scene is refused with `character-female-a.glb needs the picture colormap.png, which is not in the sources` while it is missing. The sources list shows `needs` for each model. Nothing else outside the file is read: an address with a scheme, an absolute path, a backslash or `..` in a picture, or a data buffer in another file, refuses the model.
- **Checked before Blender starts.** The server reads the file as data (a binary glTF is a header and a JSON chunk), so the description names the fault by its place (`objects[2].file`, `objects[2].animation.clip`) and nothing of the model is evaluated; Blender then imports it with its glTF importer, which runs no script, and the project's auto-run of scripts stays disabled.
- **Cache and the `.blend`.** The scene is rendered again when a model file or a picture it needs changes (their content, not their names), and not when another file of the sources does. The kept `.blend` holds the model, its rig, its clips and its textures packed, so it opens on another machine with no `sources/`.
- **Only where there are sources.** A scene with no project (the one-call generation, the writer and the preview with no project, the datasets of `assist/dataset`) has no sources: a `model` there is refused saying so. A piece of a music motion script does have them (its own project's): it keeps its models with `PUT /api/music-motion/pieces/{id}/models/{name}` and its 3D pieces place them like a project's scenes do, with fonts still taken from the design (see `docs/MUSIC_MODE_MOTION.md`). The writer of a project that has models is told which ones (name and clips) and how to place them; a project without models gets the prompt it always had.
- **Where to get models.** The server keeps a **library of packs** (Kenney's CC0 characters and props are in its list) that installs on demand and puts a model, with the pictures it needs, in a project or a piece with one call: see [MODEL_LIBRARY.md](MODEL_LIBRARY.md). Remember that `size` is the model's height unless the object has `"fit": "largest"`, which a flat or wide thing such as a pizza, a rug or a table top needs.
- **Licences.** The server ships no model but the two characters of `testdata/blender-models/` (Kenney's *Mini Characters 2.0*, CC0, with its licence). Kenney and Quaternius publish packs under CC0; read each pack's licence before using it.
- **Fit, measured.** On a plate built for the test (30 x 3 x 30 cm), `size: 0.3` with `fit: "largest"` fills 30 cm of the frame and with no `fit` about 12 times that in the picture (10 in size, a little more because the front of the 3 m plate is nearer the camera); a `scale` key still multiplies it. The thumbnails of the library are drawn with `fit: "largest"`: the bed, the rug, the pizza, the lamp, a table and a character all fit whole in the same frame (`blender-fit/` in the shared folder).
- **Measured.** The example of `testdata/blender-models/` (five seconds at 30 fps and 1280x720, a glossy floor, a title, four characters on two files) took **46 s to render and 47 s in all** with 16 samples on one card (an RTX 4070, OptiX) through the command line, against the 48 s of the primitives scene of the same size: the characters weigh little. A character has 860 to 1,004 triangles and 32 clips; at 355,000 triangles (the same character subdivided) a frame at 1280x720 and 16 samples took 0.6 s, as it did at 924, so the limit of 500,000 triangles a file is about memory and not about time. Five ideas that use the characters of the project were written by the local `gemma4:26b` (nothing paid): **5 of 5 valid, 2 at the first attempt and the others at the second**, every one with models that play a clip; the faults of the first answers were JSON slips (a colon after an array element, a missing quote) and one `animation` given to an object that was not a model, and none invented a file or a clip. The clip and the offset were checked on the real render: a looped clip of half a second repeats on frame 16 at 30 fps, a delayed one differs on every frame, double speed doubles the cycle, a clip that does not loop holds its first and its last pose, and a model of `size` 1 fills 41% of the frame's height from 6 m with a 50 mm lens (2 m, twice as much). Running the example from the command line found a bug that no unit test had: the command line runs a `scenes.yaml` in place, and a check that took its empty project folder for "no project" refused every model (and every font file). The clips, the frames, the `.blend`, the descriptions the model wrote and their previews are in the shared folder `blender-modelos/`. In a piece of a music motion script (`docs/MUSIC_MODE_MOTION.md`) the same models placed in the two choruses of a 20 s piece with the verses as pages took **111 s to render** (599 frames, 219 of them in 3D; the estimate said 114.8 s), and the local model wrote 3 of 3 ideas for those pieces after a fix found there: the prompt told it the end of the scene in beats rounded up (7.077 for 7.0765), so a key at the end fell 0.2 ms outside and was refused; it is now cut, not rounded (the shared folder `blender-modelos-pieza/` has the video, the script and the descriptions).

### Where else a description is used

The same description, checked and rendered the same way, is used in three more places (their routes are in `docs/http-api.md`):

- **Datasets.** In a scene dataset a line `Blender: <name>.scene.json` makes the scene a 3D scene, and `POST /api/assist/dataset` with `picture: "blender"` writes the descriptions with the model (at most 6 per dataset). The import and the export carry them (`docs/IMPORT.md`, `docs/ASSISTANT.md`); with no project yet, the descriptions travel inside the JSON (`descriptions`).
- **One call.** `POST /api/generations` with `option: "blender"` renders a description sent with the request (or written first with `POST /api/blender/descriptions`, which needs no project, and looked at with `POST /api/blender/preview`). It is never the default option, takes no picture and lasts 1 to 30 s.
- **Music motion.** A piece of a music motion script can be a 3D scene (`blender` instead of `part`): it is rendered as a scene of the piece's project with the song's tempo and design, and written, changed and previewed like any description (`docs/MUSIC_MODE_MOTION.md`). The measured cost is about 370 ms per frame at 720p, which the estimate of the render uses.


## Fades between scenes

By default scenes are joined with hard cuts, by stream copy (no re-encoding). A short crossfade hides the jump between two clips:

```yaml
defaults:
    transition_seconds: 0.4    # every join between two scenes is a 0.4 s crossfade (0 to 2; absent or 0 = cuts)
scenes:
    - id: s01
      ...
    - id: s02
      transition_seconds: 0    # a hard cut into this scene, whatever the project says
    - id: s03
      transition_seconds: 1    # a longer fade into this one
```

`transition` picks the **kind** of join (default `fade`, the crossfade): `fade`, `fadeblack`, `fadewhite`, `dissolve`, `wipeleft`, `wiperight`, `wipeup`, `wipedown`, `slideleft`, `slideright`, `slideup`, `slidedown`, `circleopen`, `circleclose`, `radial`, `pixelize` (the names of ffmpeg's `xfade`), or a **shader transition** of HyperFrames, written `shader:<name>`: `shader:domain-warp`, `shader:ridged-burn`, `shader:whip-pan`, `shader:sdf-iris`, `shader:ripple-waves`, `shader:gravitational-lens`, `shader:cinematic-zoom`, `shader:chromatic-split`, `shader:glitch`, `shader:swirl-vortex`, `shader:thermal-distortion`, `shader:flash-through-white`, `shader:cross-warp-morph` or `shader:light-leak`. A shader transition is drawn by Chrome (WebGL on the CPU, the same on every machine) from the last frames of one scene and the first of the next, replaces the picture of the crossfade over the same seconds and leaves the sound crossfaded; it needs Chrome or Chromium and, if it cannot be drawn, the join is a plain crossfade with a warning naming the scene. The shaders glow with HyperFrames' orange by default; `transition_accent` (`#rrggbb` or `#rgb`, in `defaults` and in a scene, the scene's wins) sets the colour of the glow, light leaks and sparks of the shader transition into that scene (for example `transition_accent: "#00c2a8"`), and does nothing for the other kinds. The shaders are Apache-2.0 (see THIRD_PARTY.md). It can be in `defaults` and in a scene (the scene's wins) and only matters where the join has a `transition_seconds` above 0; the sound is always crossfaded.

A scene's `transition_seconds` is how **that scene comes in** from the previous one; it overrides the project's and 0 is a cut. The first scene's is ignored. During a fade the end of one scene and the start of the next **overlap** and their sound is crossfaded over the same time, so the video is shorter by the sum of the fades (three 2 s scenes with two 0.5 s fades last 5 s). A fade cannot be longer than half of the shorter scene it joins: it is shortened and a warning names the scene. With any fade the join is re-encoded once (libx264 and aac, as the clips are); with none it is exactly as before. The join is made on every run, so changing a fade never makes a scene again. Only video projects have it. In the editor: **Fade between scenes** in the project defaults and **Fade in** in each scene (empty = the project's).


A scene can start its generation from a picture of yours instead of from noise: `init_image` names a file in `sources/` and `strength` (0 to 1, default 0.6) how far the result may depart from it. It works in images and video modes, is fitted to the project resolution first, and is cached by the content of the file. See `docs/IMAGE_MODE.md`.

**Reusing how a picture was made.** In the editor, when you upload the starting picture ("Upload image, GIF or video") the server looks for the generation settings the file carries — prompt, negative prompt, seed, steps, cfg and model — and, if it finds any, a dialog lists them and offers to apply them to the scene (prompt, negative prompt and seed are checked; steps and cfg become the scene's own render settings and start unchecked; the prompt starts unchecked when the scene already has one; the model is only shown, with whether you have it installed). Applying leaves a note with "Deshacer". Nothing found, nothing asked. What is read: the PNG text of stable-diffusion.cpp and A1111 (`parameters`) and of ComfyUI (`prompt`), the `comment` tag of the videos `sd` makes, the comment of a GIF and the EXIF/XMP of a WebP. A GIF almost never carries them, and a picture that went through a web page or a chat usually lost them. A GIF or a video uploaded here becomes its **first frame** (stored as a PNG in `sources/`); the video itself is not kept. Only the starting picture does this: the scene's own picture and project import do not. API: `PUT /api/projects/{id}/sources/{name}?as=picture` in `docs/http-api.md`.

## `adult`

A project may carry a top-level `adult`: `true` when it shows pictures, videos or songs for people of 18 or older, `false` when it was looked at and is fine for everyone, and nothing when it was not looked at. It changes nothing in a run: the server uses it to decide whether to show the project (see [SERVER.md](SERVER.md)). The editor sets it (Output → Content) and so does the new-project dialog; duplicating, exporting and importing a project keep it, and a dataset carries it as an `Adult: yes` or `Adult: no` line on its first entry.

## `described_by`

A scene may carry `described_by`, the model that wrote its prompt when it came from "Describe a reference" in the editor: the model alone for the built-in Ollama (`gemma4:26b`), `provider-id:model` for any other provider (`openrouter:gpt-4o`). It is only a record: it changes nothing about how the scene is generated, and the reference file it was written from is not kept (see `docs/ASSISTANT.md`).

## Style and quality in the editor

The editor can fill the model and the generation settings for you from what is installed. A **style** (photorealism, illustration and anime, general) picks the best installed model of that style, from the optional `styles` / `style_rank` fields of the catalog entries (`GET /api/assets/catalog`). A **quality** level (Low, Medium, High) writes resolution, steps (and, in images mode, variants and an optional LoRA) from the model's preset, and in video mode picks the installed Wan (5B Q4, 5B Q8 or A14B, from the catalog's `quality` field) with its resolution, frames and steps; the factors are a guide (×0.75 / ×1 / ×1.25 of resolution and ×0.5 / ×1 / ×1.5 of steps), editable in «Ajustar niveles» and kept in the browser. None of this is saved in `scenes.yaml` as such — only the values it writes — and nothing is applied when a project is opened. The new-project dialog can apply a style and a level straight away.

## Quality per scene

Resolution, steps and cfg scale are the project's (`defaults.resolution`, `defaults.steps`, `defaults.model_set.cfg_scale`), and so are the Wan settings (`defaults.animate`). A scene can set its own, for that scene only, with two optional blocks:

```yaml
scenes:
  - id: key-scene
    prompt: ...
    render: {resolution: 1280x704, steps: 12, cfg_scale: 1}   # how its picture is drawn
    animate: true
    animation: {resolution: 832x480, frames: 49, steps: 12}    # how Wan animates it
```

Anything left out is the project's. The model, the Wan model and the frame rate stay the project's. The video keeps the project's `defaults.resolution`: a picture drawn at another size is scaled to cover it and cropped, never stretched. `render.resolution` is any `WIDTHxHEIGHT`; `animation.resolution` needs both sides multiples of 16 and `animation.frames` the form 4n+1 (the clip lasts (2×frames−2)/fps seconds before it is stretched over the narration). `cfg_scale` is also accepted with a single checkpoint (then `--cfg-scale` is passed to `sd` only for that scene). The cache is per scene: changing one scene's block regenerates only that scene, and a project without these blocks behaves exactly as before. The images-mode manifest records each scene's own resolution and steps. The editor writes these blocks from «Calidad de esta escena».

## Model sets (Krea 2)

The model sets are in the model catalog and install with one request (Models tab, or `POST /api/downloads {"id": "krea2-turbo-q4"}`), each as three files in `models/`: the checkpoint, `Qwen3VL-4B-Instruct-Q4_K_M.gguf` (text encoder) and `wan_2.1_vae.safetensors`. Measured on an RTX 5060 Ti: ~17 s (Krea 2 Turbo) per 1024x576 image, with a VRAM peak of 14.2 GB. Licenses: Krea 2 uses the Krea 2 Community License and its use policy (the GGUF is a third-party conversion; read the terms before commercial use) The text encoder and the VAE are support files: they also show up in the installed image models list but are not checkpoints to pick as `defaults.model`. The entries declare a `preset` (8 steps, cfg 1, 1024x576): the Models tab shows it, and the editor, with that model chosen in a model set, offers "Use recommended settings", which writes steps, resolution and `cfg_scale` in one edit; it never applies on its own.

```yaml
defaults:
  resolution: 1024x576
  sampler: euler
  steps: 8
  model_set:
    diffusion_model: models/krea2_turbo-Q4_K_M.gguf
    llm: models/Qwen3VL-4B-Instruct-Q4_K_M.gguf
    vae: models/wan_2.1_vae.safetensors
    cfg_scale: 1
```

`defaults.model_set` replaces `defaults.model` for models that ship as a bare diffusion model. Tested combination: Krea 2 Turbo Q4 + Qwen3-VL-4B Q4 + `wan_2.1_vae`, with `resolution: 1024x576`, `steps: 8`, `cfg_scale: 1` (~16 s per image on an RTX 5060 Ti). Changing any of the three files regenerates cached images. `sd` copies the whole model into pinned memory as well as VRAM, so on a 30 GB machine launch large sets with a memory cap (`systemd-run --user --scope -p MemoryMax=24G -p MemorySwapMax=0`). int8 files from Civitai are not usable here (about 195 s per step); pick fp8 or GGUF.

**Editing a picture by instruction (`model_set.edit`).** A model set with `edit: true`, `clip_l` and `t5xxl` (and no `llm`) is an editing model such as FLUX.1 Kontext dev: a scene's `init_image` is then the picture to **edit** (given to the model as a reference) and the scene's `prompt` is the **instruction** (`put a red cap on the man`); `strength` has no effect and a scene without `init_image` gets a warning. The picture is edited **whole, at its own shape** (about the pixels of the project's resolution, in multiples of 16): a portrait photo comes out portrait instead of being cropped to the project's landscape size. Measured with Kontext Q4_K_M, T5-XXL fp8 and the Flux VAE at 1024x576, 20 steps, cfg 1: 42 s per edit on an RTX 4070 12 GB, peaking at 7.9 GB of VRAM. The catalog entry (Models) installs the four files.

**A second editing engine: Qwen-Image-Edit (Precise).** The other family of editing models takes `llm` (Qwen2.5-VL-7B) and `llm_vision` (its vision projector) instead of `clip_l` and `t5xxl`; a set never mixes the two families. Retouch then has two engines: **Fast** (FLUX.1 Kontext: about 40 s, non-commercial licence) and **Precise** (Qwen-Image-Edit-2509: about 2 min, Apache 2.0, so commercial use is allowed). Measured with the Q4_K_M model, Qwen2.5-VL-7B Q4_K_M, its `mmproj-F16` and the Qwen-Image VAE, 20 steps, cfg 2.5, euler, flow shift 3, on an RTX 4070 12 GB: 53 s at 512x512 and 113 s at 1024x576 (peak 11.3 GB of VRAM, so it only just fits 12 GB), the whole scene kept. It runs with `--mmap`, `--offload-to-cpu` and `--vae-tiling` (the engine adds them) and needs 24 GB of RAM: launch the server with `scripts/serve.sh`, which caps memory at 24 GB with no swap, because 19 GB of weights run on a machine without that cap froze a 30 GB one. Changing the engine of a project swaps only its model set.

```yaml
defaults:
  resolution: 1024x576
  sampler: euler
  steps: 20
  model_set:             # FLUX family; the Qwen one has llm + llm_vision instead of clip_l + t5xxl
    edit: true
    diffusion_model: models/flux1-kontext-dev-Q4_K_M.gguf
    vae: models/ae.safetensors
    clip_l: models/clip_l.safetensors
    t5xxl: models/t5xxl_fp8_e4m3fn.safetensors
    cfg_scale: 1
scenes:
  - id: gorra
    init_image: foto.png        # in the project's sources folder
    prompt: put a red cap on the man
```

## Images mode

With `output.mode: images` (or `--mode images`), each scene runs **render → export** only; `tts`, `probe`, `clip`, and `concat` are skipped entirely and `piper`/`ffprobe` are not required on PATH. Per scene:

- `variants: N` renders N images sequentially. With an explicit `seed`, variant *i* uses `seed + i` (deterministic and cacheable). Without a seed, each variant gets a random 32-bit seed that is recorded in the manifest for reproducibility.
- Final PNGs land at `out/<project>/<id>.png` (single variant) or `out/<project>/<id>_v<N>.png` (multi-variant, 1-based).
- `out/<project>/manifest.json` (always written, atomically) records per image: scene ID, variant index, effective seed, final composed prompts, model, resolution, sampler, steps, and file path — everything needed to reproduce it.
- `output.contact_sheet: true` additionally tiles all exported PNGs into `out/<project>/contact_sheet.png` via ffmpeg.
- `defaults.subject_prompt` is prepended to every scene's prompt; `defaults.negative_prompt` is appended after each scene's own `negative_prompt`. Both affect the render cache key, in both modes.
- The render cache is shared across modes: switching a project between `images` and `video` reuses already-rendered PNGs when the render inputs are unchanged.

## Music mode

With `output.mode: music` (or `--mode music`) the project declares `tracks:` instead of `scenes:` and each track runs **compose** (`ace-lm`, only when the language model writes the lyrics) → **synth** (`ace-synth`) → **export**; `piper`, `sd`, `ffmpeg` and `ffprobe` are not required. Per track:

- `prompt` is the style description (required); `lyrics` is sung as written (the language model is skipped unless `use_lm: true`); `instrumental: true` makes a track without vocals. `duration_seconds`, `bpm`, `keyscale`, `timesignature`, `vocal_language`, `seed` and `variants` are optional. Without `vocal_language` (on the track or in `defaults.music`) the language is deduced from the lyrics, and is English when there are none; an explicit value always wins. `vocals` (`female`, `male`, `duet`, `child`, `choir`, `deep`; also in `defaults.music`) asks the engine for a voice by adding a phrase to the style: it is a hint, not a guarantee.
- `mixes` (optional) joins songs and audios into one file: a timeline of `clips` (a `track` of the project or a `file` of `sources/`, with `start`, `gain`, `mute`, `fade_in`, `fade_out`, `trim_start` and `trim_end`), rendered by a `mix` phase with ffmpeg to `out/<project>/<mix id>.mp3` after the tracks, cached by the content of its inputs, and edited in the studio in a resizable, movable modal window with a timeline (drag, trim, zoom, snap), per-track playback and a live **«Listen without mixing»** preview of every track together (no render needed; the exported file still comes from the `mix` phase). The mix itself accepts `gain` (master volume, 0 to 2) and `normalize: true` (brings it to −14 LUFS), and a rendered mix records its loudness (`lufs`) in the manifest. See `docs/MUSIC_MODE.md`.
- `defaults.music.generator` (`ace-step`, the default, `heartmula` or `diffrhythm2`) chooses which model makes the songs; `diffrhythm2` (an optional generator that runs as a service: fast, but English lyrics only) takes `tags` like HeartMuLa, accepts `dr2_cfg_strength` (0.5 to 10) and `dr2_steps` (4 to 64) in `defaults.music`, and cannot use `task`, `reference`, `instrumental` or a `vocal_language` other than `en`; with `heartmula` (an optional generator that runs as a service, better quality but slower) a track needs `tags` (words separated by commas, or a list) and cannot use `task`, `reference` or `instrumental` (`bpm`, `keyscale` and `timesignature` are ignored with a warning). See `docs/MUSIC_MODE.md`.
- `favorites` (optional, music) lists the songs marked with a star in the studio, each `{track, seed}`: nothing in a run reads it.
- `defaults.music` sets the models directory (`models/music`), the two models, `output_format` (`mp3` or `wav`) and the generation parameters.
- Audio lands at `out/<project>/<id>.mp3` (or `<id>_v<N>.mp3` with variants) with a `manifest.json` recording the final caption, the lyrics requested and the lyrics actually sung, both seeds, the models and every parameter.
- Changing only synthesis parameters never re-runs the language model.

Install the engine (`ace-lm`, `ace-synth` and `ace-server`, which a project can use instead with `defaults.music.engine: server`) with `bash scripts/build-acestep.sh` and a model bundle from the Models screen (Music tab); see `docs/MUSIC_MODE.md` for the full reference.

## Models 3D mode

With `output.mode: models3d` the project declares `models:` instead of `scenes:` (a project in this mode has no scenes and no tracks, and `models` is refused in any other mode). Each entry:

- `id` (required): a plain lower-case name (letters, digits, `.`, `_`, `-`, not starting with a symbol, up to 64 characters), unique; it names the file `out/<project>/<id>.glb`.
- `prompt` (up to 1000 characters) **or** `image` (the name of a `.png`, `.jpg`, `.jpeg` or `.webp` in `sources/`): the picture is drawn from the prompt with the project's image model
  (`defaults.model` or `defaults.model_set`, `resolution` (768x768 when absent), `sampler`, `steps`), after completing it to show one object centred on a plain background; an `image` is used as it is. Both, or neither, is refused.
- `engine` (`triposr` by default, or `sf3d`) and `detail` (`low`, `normal`, `high`), which `defaults.models3d: {engine, detail}` gives to the entries that say none.
- `variants` (1 to 4, default 1): that many models with different seeds, `<id>.glb`, `<id>-2.glb`, ... An entry with its own `image` makes one (a warning says so).
- `seed`: the picture's seed for the first variant, each next one adds 1; absent, one is taken from the prompt.
- `adult: true`: the models are marked as for people of 18 or older.

`defaults.models3d` is only valid in this mode. The run needs the 3D service (see `MODEL_GENERATION.md`) and `sd` for the entries that have a prompt. What a run makes, what it leaves in `out/<project>/` and what
makes it do a model again are in `MODEL_GENERATION.md`.

## `client`

A project made through the generation API (`api-gen-…`, `api-edit-…`, `api-m3d-…`) carries a top-level `client`: the name of the dashboard that asked for it (one to 32 lowercase letters, digits or hyphens, `video-studio` for example; `unknown` when it gave none). It changes nothing in a run: the server copies it to the run so the statistics (`GET /api/stats?client=`) and the metrics can group by it, and it tells whose a folder is when you look at it on disk. A value that does not fit that form is refused when the file is loaded. You never need to write it by hand.

### Written by a model

A description does not have to be written by hand: a model writes it from an idea (`POST /api/projects/{id}/blender/descriptions`, see `docs/http-api.md`). The file goes to `sources/<name>.scene.json`, and a scene then names it with `blender: {build: <name>}`; nothing about how it renders changes.

```json
{ "name": "hola.scene.json", "idea": "the word Hola in big letters rising from the floor, a red ball that hits on every beat, a slow dolly in", "seconds": 5 }
```

- **What the model is told.** From the project and from the contract, not from a fixed text: the length of the scene and the shape of the frame, the colours behind `bg`, `fg` and `accent` of the design, the fonts `display` and `body` only when the design has them in the pack, the tempo (`defaults.motion.bpm`) and how many beats the scene lasts, or that keys are in seconds when the project has no tempo, every object type, light type and easing the contract accepts (a test fails if one is missing from the prompt) and one worked example (also tested: it passes the checks in any project). The model that answers is the one of the `motion_graphics` task, so its provider, its consent and its cap of calls are the ones already set for the pages of motion graphics; a local model waits while a run is active, as it does for a page.
- **What is stored.** Only a description that passes the same checks as one written by hand, plus two a model must meet and a person need not: at least one object, and something that moves (the camera or an object has two keys that differ). A description that does not is asked for again with the problems, each naming its place in the JSON and the previous answer, up to 3 attempts; then `422` with the last problems and the file untouched. The file keeps what the model wrote (`accent`, `beat`, indented), so a person opens it and sees names and beats, not hex and seconds; the answer also carries the resolved description.
- **The idea is data.** It says what to show. Whatever it asks for, only a description that passes the checks is stored, so it cannot widen what is accepted.
- **Revising.** With `"revise": true` the model is shown the stored description and `idea` says how to change it ("a slower camera", "the title in the accent colour"); the whole new description goes through the same checks and is stored only if it passes.
- **By hand.** `GET` and `PUT /api/projects/{id}/blender/descriptions/{name}` read and save the same file (`PUT` takes `{description, seconds}` and checks it as the writer does, naming each fault by its place in the JSON).
- **A frame to look at.** `POST .../descriptions/{name}/preview` with `{t, seconds, width?}` draws one frame as a PNG: 480 pixels wide by default (up to 960), 8 samples, on the CPU, so it never takes a card from a run: **about 0.6 s** for the example at 480x270 (Blender's start included). It needs Blender; writing and revising do not. The frame is the one the clip would have at that second: the keys are placed against the whole scene, and a frame rendered alone matched the clip's (SSIM 1.0000 in the test).
- **Measured.** Five ideas through the real endpoint with the local `gemma4:26b` (the routing as it is, nothing paid): a title with hits on the beat, a turning cube, a product on a plinth, three bouncing spheres in a project with no tempo, and a revision ("a slower, calmer camera"), in a project with the design of the example (display Playfair Display, 120 bpm). What the rounds taught, in order: with a worked example keyed in seconds, **0 of 4 passed at the first attempt**, because the model copied the unit of the example and wrote `t: 10` for the 10 beats of a 5 s scene (and the revision failed three times with a JSON slip); with the example keyed the way the project keys (beats at 120 bpm, seconds otherwise), **5 of 5 at the first attempt**; after adding the framing rules (what a lens sees at a distance) **4 of 5**, one idea failing three times with the same slip, a missing closing quote in `{"t:2.3,"location"...`; and after the repair request began to show where the JSON breaks (`...{"t:2.3,"<<HERE>>location...`), **5 of 5 at the first attempt again, 8 to 13 s each** (the revision included). So the number to expect from a 26B local model is "valid at the first or second attempt", not "always good": the scenes are valid and plausible, and the preview showed what the checks cannot see: the title was cropped when the camera dollied past it before the framing rules, and in "the torus on a plinth" the plinth still fills the frame and hides the torus. Checking the framing is what the vision review loop (not in this change) would do. Two of the descriptions were also rendered through the real server (a run each, on one card, 16 samples, 1280x720: **48 s for the 5 s scene and 39 s for the 4 s one**); doing it found a bug the tests had not: the server's projects folder is a relative path (`projects`), Blender and ffmpeg run inside the project's folder, and every built scene failed in a real run with `projects/x/tmp/.../scene.json: no such file` (the render) and then with the frames not found (the join); both now take absolute paths and a test of each keeps it so. The descriptions, the previews of three moments of each, a contact sheet, the two clips and the log of every round are in the shared folder `blender-scene-writer-pruebas/`.
- **Not yet.** A model that looks at the preview and corrects the scene, the song's real beats, and a library of models and textures.
