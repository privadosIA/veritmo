# Veritmo guide: concepts, terminal and a worked example

[← Back to the README](../README.md) · [Versión en español](TUTORIAL.es.md)

The screen-by-screen guide to the **dashboard** (environment, models, the editor, runs, history, statistics, music and your own images) lives with the dashboard, in the `dashboards-lab` project (`docs/TUTORIAL.md`), because it describes screens that change with it. This document keeps what does not depend on the front end: the concepts, what to do when something fails, the same things from the terminal, the glossary, the API screen of the server and a complete worked example with its `scenes.yaml`. The section numbers are the ones of the full guide, so the ones that are missing here (3 to 10) are in the dashboard's.

## Contents

- [1. What this does, in one sentence](#1-what-this-does-in-one-sentence)
- [2. Minimum concepts](#2-minimum-concepts)
- [11. When something fails](#11-when-something-fails)
- [12. The same thing from the terminal](#12-the-same-thing-from-the-terminal)
- [13. Glossary](#13-glossary)
- [The "API" screen](#the-api-screen)
- [A worked example: two friends on a bench who skate away](#a-worked-example-two-friends-on-a-bench-who-skate-away)

---

## 1. What this does, in one sentence

You write **scenes** (a text to generate an image and, if you want, a text for someone to narrate) and Veritmo produces a **narrated video**, or just the **images**, or — separately, unrelated to the video — **songs** from a style and lyrics (see Music). If you already have the image, you can use it as-is in a scene, rotate it, or start from it to generate another, and give the video background music. Everything runs on your machine, on your GPUs; nothing is sent to any service.

Internally, each scene goes through these phases, and the dashboard shows them one by one as they run:

| Phase | What happens | Program that does it |
|---|---|---|
| `tts` | Turns the narration text into audio | Piper |
| `probe` | Measures how long that audio lasts | ffprobe |
| `render` | Generates the scene's image from the prompt | `sd` (stable-diffusion.cpp) |
| `animate` | *(optional)* Turns the image into a clip with motion | `sd` with Wan 2.2 |
| `clip` | Joins image (or animation) and audio into a piece of video | ffmpeg |
| `concat` | Glues all the pieces into the final video | ffmpeg |
| `bgm`, `mix` | *(optional)* Generate the background music and mix it under the voice | music engine and ffmpeg |

Two things that save you time and are worth knowing from the start:

- **Cache**: each phase of each scene is stored according to its content. If you change only one scene's text, only what depends on that change is redone. This is what the "Cache" table on the Runs screen shows.
- **Nothing is deleted on its own**: neither models nor videos. Deleting the run history does not delete the videos.

## 2. Minimum concepts

- **Project**: a folder with its `scenes.yaml` (the script), its cache (`tmp/`) and its results (`out/`). The dashboard creates and edits that file for you.
- **Scene**: an idea-frame: image prompt + narration + (optional) motion.
- **Model**: the "brain" that draws. There are two ways to set it up:
  - **Single checkpoint**: one file (for example Stable Diffusion 1.5 or XL). The simplest.
  - **Model set**: three files that work together (diffusion model + text encoder + VAE). Krea 2 and IntoRealism use it, and they give far more realistic images.
- **Prompt**: the description of the image. English works better with almost all models.
- **Run**: one execution of the pipeline on a project. It stays in the history.
- **GPU**: the graphics card that will do `sd`'s work. If you have two, you can pick one or both.

## 11. When something fails

| Symptom | What to check |
|---|---|
| A binary shows as "falta" (missing) in Environment | Run the command shown below it (`bash scripts/get-piper.sh`, etc.) and press **Refresh**. If you installed it but it still does not show, the server cannot find it on its `PATH`: start it with `PATH=$PWD/bin:$PATH`. |
| A scene with its own quality comes out at a different size from the rest | That is expected: the final video keeps the project's resolution and that scene is cropped down to it (not stretched). To change the size of the whole video, change the resolution in Defaults. |
| In the mixer, "Listen without mixing" does not sound the same as the exported mix | It is an approximation in the browser (volume up to 2 only if the audio comes from the same origin as the page). Press **Mix** to generate the file and hear the real result. |
| It fails in `render` and runs out of memory | Look at the run's "Resources": if VRAM reaches 100 %, lower the resolution or use another model. |
| The run hangs or the machine runs out of RAM | Large models (sets, Wan) copy the model into RAM. `./serve.sh` already limits memory to 85% of the RAM by default (about 52G on 64 GB, no swap): if a run goes over, that process is cut instead of hanging the machine. Change it with `MEM_MAX=40G ./serve.sh`. If you start `bin/veritmo serve` by hand: `systemd-run --user --scope -p MemoryMax=24G -p MemorySwapMax=0 bin/veritmo serve …`. |
| The image comes out wrong with Krea 2 / IntoRealism | Check that it is 8 steps and cfg 1: use **Use recommended settings**. |
| A scene is entirely redone although you did not touch it | Check that **keep-tmp** is on; without it there is no cache between runs. |

## 12. The same thing from the terminal

Everything the dashboard does can be done without it, with the same `scenes.yaml`:

```bash
bin/veritmo --gpu 1 --keep-tmp projects/my-project/scenes.yaml
```

Useful flags (they go **before** the file): `--gpu 0,1`, `--keep-tmp`, `--force`, `--only <scene-id>`, `--mode images`, `--vram-warn-mb 2048`. To convert a dataset of songs: `bin/veritmo import --mode music -o scenes.yaml songs.json` (flags, here too, before the file).

To use the dashboard from another machine, protect access with `ORCHESTRATOR_TOKEN` and open an SSH tunnel (`ssh -L 4666:127.0.0.1:4666 user@server`). The details and every `scenes.yaml` field are in [SCENES_YAML.md](SCENES_YAML.md); the endpoints, in [`docs/http-api.md`](http-api.md).

To have the AI help write prompts from the terminal:

```bash
bin/veritmo assist image "a café in the morning"
bin/veritmo assist motion "she turns a page"
```

(It needs Ollama running; it works on CPU so as not to take VRAM from the render.)

To get the prompt of a picture or a clip you already have, and to use another provider for a run:

```bash
bin/veritmo describe photo.png                # prints the prompt that would draw it
bin/veritmo describe --split clip.mp4         # one line per shot: [start-end] prompt, then its MOTION:
bin/veritmo describe --provider cloud photo.png
bin/veritmo assist --provider lan image "a café in the morning"
```

`describe` needs a vision model in Ollama (`ollama pull gemma4:26b`). `--provider` takes the id of a provider registered in **Settings**; a remote one must have been accepted there first (the terminal cannot accept it), and the commands otherwise follow the routing chosen in the **Tasks** card.

## 13. Glossary

- **VRAM**: the graphics card's memory. If the model does not fit, it fails.
- **RAM**: the computer's memory. Some models pass through it before going to the GPU.
- **Checkpoint / model**: a file with what the AI has learned (`.safetensors` or `.gguf`).
- **Quantized (Q4, fp8, int8)**: a compressed version of the model. It takes less space and loses a little quality; `Q4` and `fp8` work well here.
- **LoRA**: a small adjustment that changes a model's style or subject.
- **VAE / text encoder**: helper pieces of the model sets.
- **Steps**: how many times the AI retouches the image. More steps, slower.
- **cfg_scale**: how closely it obeys the prompt. Each model has its own value (Turbo ones, 1).
- **Seed**: a number that fixes the randomness. Same seed + same prompt = same image. Empty = random on every run.
- **Sampler**: the drawing algorithm (`euler` is fine almost always).
- **Wan**: a model that animates an image to turn it into a clip.
- **TTS**: text to speech (Piper).
- **Cache**: what has already been done and stored so as not to repeat it.
- **Run**: one execution of the pipeline.
- **img2img**: generating an image starting from one of yours instead of from noise; the **strength** says how far it may move away.
- **Remix / reference**: a remix reinterprets a song that already exists; a reference only steers the sound of a new song.
- **Style / quality (level)**: the style (photorealism, anime, general) picks the most suitable installed model; the quality (Low, Medium, High) picks resolution, steps and, for video, the Wan. They apply to the whole project or, from "Quality of this scene", to a single scene. They are not saved as such: only the values they write.
- **Mixer / "Listen without mixing"**: the window where several songs and audio files are placed on a timeline; "Listen without mixing" plays them together in the browser, with changes instantly, without generating the file (that is what "Mix" does).
- **Favorite**: a song marked with a star. It is saved with the project, identified by track and seed.
- **Quality level (music)**: Fast, Normal or Maximum: a synthesis model and its steps chosen in one go.
- **LUFS**: the loudness of a recording as it is heard; −14 LUFS is the usual target of music platforms. *Normalize* in the mixer aims at it.
- **Background music**: an instrumental soundtrack mixed under a video's narration, lowering itself when the voice speaks.
- **Reference / describe**: a picture or a clip you upload only so that a vision model writes its prompt; the new scene is generated from that text, never from the file.
- **Vision model**: a model that can read pictures (in Ollama, for example `gemma4:26b`); it describes the references and judges the frames of the quality check.
- **Provider**: where a model runs: the built-in local Ollama, another Ollama, or any OpenAI-compatible server (OpenAI, OpenRouter, Together, Groq, vLLM, LM Studio…). You register them in Settings.
- **API key**: the secret a cloud provider asks for. It is saved on the server, write-only (it is never shown again) and never sent over plain http to another machine.
- **Task (routing)**: one of the five things that use a model — the prompt assistant, describing a picture, describing a clip, the clip check, and writing the page of a motion graphic. Each runs on the provider you choose in the **Tasks** card; all start on the local Ollama.
- **Consent**: your explicit acceptance that a task will send pictures, clips or texts to a provider that is not this computer. It is tied to the provider's address, shows as a "☁ external" badge and nothing switches provider by itself.
- **Shot**: one continuous take of a clip; describing a clip "one scene per shot" makes a scene for each.

## The "API" screen

If you want to know what the server offers (or try a request by hand), open **API** in the menu, just below Environment. It is the reference of every endpoint, with its parameters and responses:

![API](tutorial/en/17-api-swagger.png)

- Click an endpoint to expand it, and **Try it out** to try it. The request goes to the server you are connected to, with the token from "Connection": there is no need to paste it again.
- Careful: it **acts on the real server**. What you create or delete from there stays done.
- If a warning appears instead of the list, it is usually because the token is missing in "Connection".
- The reference uses Swagger's standard look, different from the rest of the dashboard.

## A worked example: two friends on a bench who skate away

A real case, with its `scenes.yaml` (it is the one in `projects/chicas-patines`): a clip of about 30 seconds in which two girls in bikinis sit on a park bench talking, then stand up and skate away. It shows how to think a **video** whose subject moves, and what we measured.

### What does not work, and why

Asking for it as one scene ("they sit talking and then stand up and skate") fails for four reasons that come from how the pipeline works:

1. **A scene is one picture animated for 2 to 4 seconds**, not a continuous clip. A 30-second video needs about **15 scenes** with cuts between them.
2. **The animation goes forward and then back.** It is saved forward and reversed so it loops without a jump; if the scene lasts longer than the forward part, the reversed half starts and the action **undoes itself** (they stand up and sit down again). So the scene lasts as long as the forward part: frames ÷ fps = 33 ÷ 16 = 2.06 s, hence `silence_seconds: 2`.
3. **One action per scene.** Wan does a gesture well (talking, laughing, turning the head) and a sequence badly.
4. **Clips are not chained**: each scene starts from a new picture, so the two girls change faces between shots unless you describe them the same every time. That is what `defaults.subject_prompt` is for: it is put in front of **every** shot (the characters and the place), and each `prompt` only describes the shot.

### The project

`silence_seconds` makes the scene silent and with exactly that duration (no narration and no voice needed; put music with `bgm` if you want it). The pictures use the model set tested in this project (IntoRealism + Qwen + `wan_2.1_vae`, 1024x576, 8 steps) and Wan 2.2 5B animates 33 frames per shot:

```yaml
# A ~30 s clip: two girls in bikinis sit on a park bench, talk, stand up and skate away.
# 15 shots of 2 s (the stand-up one, 1.5 s): about 29.5 s in all.
# Each shot is a picture that Wan animates forward (33 frames at 16 fps = 2.06 s): the scene lasts
# 2 s so it never reaches the reversed half of the loop. Measured with Wan 5B: with 49 frames the
# movement deformed after ~2 s; with 33 it stays coherent, and the stand-up shot (1.5 s) cuts to
# another shot where they are already standing.
# subject_prompt describes the two girls and the place and is put in front of every shot, so they are
# the same in every cut. Each `prompt` only describes the shot; `motion_prompt`, only what moves.
project: chicas-patines
output:
  mode: video
  file: out/chicas-patines.mp4
defaults:
  voice: voices/en_US-lessac-medium.onnx   # not used (every scene is silent), but required
  resolution: 1024x576
  sampler: euler
  steps: 8
  fps: 24
  model_set:
    diffusion_model: models/intorealismKrea2_v40.safetensors
    llm: models/Qwen3VL-4B-Instruct-Q4_K_M.gguf
    vae: models/wan_2.1_vae.safetensors
    cfg_scale: 1
  subject_prompt: >-
    photo of a brunette young woman with long wavy hair in a red bikini and a blonde young woman
    with a short bob in a blue bikini, both wearing white roller skates, sunny city park, wooden bench,
    green trees, soft golden light, 35mm, natural skin,
  negative_prompt: blurry, low quality, deformed hands, extra fingers, extra limbs, bad anatomy, text, watermark, bent over, rear view close-up, from behind, buttocks
  animate:
    diffusion_model: models/video/Wan2.2-TI2V-5B-Q8_0.gguf
    t5xxl: models/video/umt5-xxl-encoder-Q8_0.gguf
    vae: models/video/wan2.2_vae.safetensors
    frames: 33          # 2.06 s forward (49 frames, 3 s, deformed in the tests)
    steps: 20
scenes:
  - id: s01_bench_wide
    seed: 101
    silence_seconds: 2
    animate: true
    prompt: both sitting on the park bench talking and smiling, wide shot
    motion_prompt: the two women chat, the brunette gestures with one hand while talking, the blonde nods and laughs, leaves move gently in the breeze, static camera

  - id: s02_brunette_talking
    seed: 102
    silence_seconds: 2
    animate: true
    prompt: medium close-up of the brunette sitting on the park bench, looking at her friend
    motion_prompt: she talks, lips moving, smiles and tilts her head, hair moves slightly in the wind, static camera

  - id: s03_blonde_laughing
    seed: 103
    silence_seconds: 2
    animate: true
    prompt: medium close-up of the blonde sitting on the park bench
    motion_prompt: she laughs, tucks her hair behind her ear, glances to the side, static camera

  - id: s04_look_off
    seed: 104
    silence_seconds: 2
    animate: true
    prompt: both sitting on the park bench, two-shot
    motion_prompt: both turn their heads towards the path, then look at each other with excitement, static camera

  - id: s04b_hands_talking
    seed: 111
    silence_seconds: 2
    animate: true
    prompt: close-up of the brunette's hands gesturing while she talks, both sitting on the park bench, shallow depth of field
    motion_prompt: the hands move expressively while she talks, the blonde listens and smiles in the background, static camera

  - id: s04c_two_smile
    seed: 112
    silence_seconds: 2
    animate: true
    prompt: both sitting close together on the park bench smiling, medium shot
    motion_prompt: they smile and glance at each other, one nudges the other playfully, static camera

  - id: s05_skates_detail
    seed: 105
    silence_seconds: 2
    animate: true
    prompt: close-up of two pairs of legs on the park bench, white roller skates with colourful wheels resting on the ground
    motion_prompt: their feet tap the ground impatiently, wheels roll slightly, static camera

  - id: s06_stand_up
    seed: 106
    silence_seconds: 1.5
    animate: true
    prompt: medium shot from the front, both sitting on the edge of the park bench about to stand up, hands on the bench seat, smiling
    motion_prompt: both push off the bench with their hands and stand up smiling, balancing on their roller skates, static camera

  - id: s07_roll_away_back
    seed: 107
    silence_seconds: 2
    animate: true
    prompt: wide landscape shot, the two women are small in the frame, full body, seen from the side in profile facing right, standing on roller skates on a tree-lined park path, bench in the background
    motion_prompt: the two women start skating along the path side by side, gliding away from the bench, hair flowing, static camera

  - id: s08_skating_to_camera
    seed: 108
    silence_seconds: 2
    animate: true
    prompt: medium shot skating towards the camera on a park path, smiling
    motion_prompt: they skate towards the camera smoothly, the brunette waves, hair flowing, camera slowly moves back

  - id: s09_side_tracking
    seed: 109
    silence_seconds: 2
    animate: true
    prompt: side view skating along a tree-lined path, laughing
    motion_prompt: they glide along the path laughing, trees pass in the background, camera tracks with them

  - id: s11_laughing_closeup
    seed: 113
    silence_seconds: 2
    animate: true
    prompt: close-up of both laughing while skating along the park path, front view, trees blurred in the background
    motion_prompt: they laugh and glide forward, hair flowing in the wind, camera moves backward keeping them in frame

  - id: s12_wave
    seed: 114
    silence_seconds: 2
    animate: true
    prompt: medium shot from the front, both skating on a sunny park path, one waving at the camera
    motion_prompt: the brunette waves at the camera while skating, the blonde laughs, camera tracks backward

  - id: s13_curve_path
    seed: 115
    silence_seconds: 2
    animate: true
    prompt: wide shot, both skating side by side along a curved tree-lined path, side view
    motion_prompt: they skate along the curve side by side, gliding smoothly, static camera

  - id: s10_off_into_distance
    seed: 110
    silence_seconds: 2
    animate: true
    prompt: wide shot skating down a long park path towards the sun
    motion_prompt: they skate away into the distance, sunlight glows through the trees, static camera
```

### How it was tested (so you can do the same)

Do not launch the 15 shots to find out. Test the difficult ones first, **picture first and movement later**:

1. **The picture**, in images mode, about 17 s: `veritmo --mode images --only s06_stand_up scenes.yaml`. Look at it before animating: the first try at the stand-up shot (*"low angle, about to stand up"*) came out as a bent-over rear view, so the prompt became a frontal shot sitting on the edge of the bench and `negative_prompt` got *bent over, from behind, rear view close-up*. The skating shot also went from a rear view to **a wide shot in profile**.
2. **The movement**, about 85 s: `veritmo --keep-tmp --gpu 1 --only s06_stand_up scenes.yaml`. Then look at frames of the clip. The first test used 49 frames (3 s) and the clip was looked at frame by frame (seconds 0 to 2.8).

   The first second and a half was right (she leans forward and pushes off with her hands); after that the legs flail and she tumbles over the bench. The same happened to the skating shot: it rolls away well and then the bodies bend. So the shots were cut to **33 frames (2 s)**, and the stand-up one to 1.5 s.

   With 33 frames both stay coherent: they stand up (a bit abruptly: the movement is compressed into 1.5 s) and they glide away while the camera follows them. The cut to the next shot hides the rest.
3. **Fix what does not work** by changing the `prompt` (picture) or only the `motion_prompt` (movement: the picture is not drawn again). The cache also tells you what it will redo.
4. **Then everything**: `veritmo --keep-tmp --gpu 1 scenes.yaml` (or *Create* in the editor). With the 5B each shot takes about 100 s (17 s the picture, 85 s the movement): the 15 shots, about 25 minutes. On a 30 GB machine launch it with a memory cap, as the Animation section says.

> **`--only` and the cache**: until a bug was fixed, `--only` also wrote the cache entries of the scenes it left out, so a later run could believe they were already made and fail when joining the video (*Error opening input … scene_….mp4*). It is fixed now; if you ever see that error in a project in which you ran scenes one at a time, delete the project's `tmp/manifest.json` and run it again.

### How to write the prompts

- **`prompt` describes the picture, with the shot first**: *"medium close-up of the brunette sitting on the park bench"*, *"wide landscape shot … seen from the side in profile"*. The characters and the place are already in `subject_prompt`.
- **`motion_prompt` describes only what moves**, in the present, with one main action and the camera: *"she talks, lips moving, smiles and tilts her head, hair moves slightly in the wind, static camera"*. For "talking": the mouth moves in a generic way (there is no lip sync), so cover the dialogue with music or a voice-over.
- **Say the camera**: `static camera`, `camera slowly moves back`, `camera tracks with them`. If you do not, it can drift.
- **Cut on the action**: finish a shot just as the movement starts (the stand-up shot) and start the next with the action already happening (they are skating). The cut hides what the model cannot do.
- **Choose framings that survive the movement**: rear or very low views deform easily (and the first picture of this example came out badly composed in that way); a frontal, profile or wide shot hides more of a body that bends.

### Review the clips from the dashboard

When the video is done, open **Projects → Review the clips** (or *Review the clips* in the finished run). *Analyze* measures colour and sharpness of every clip and, if you pick an Ollama vision model, looks for deformed faces and bodies (each face is located, enlarged and judged apart, so small faces in wide shots count). Each **finding** shows the frame that was flagged, what was seen and **how it will be fixed**: cut the clip, remake it with another seed, change the motion prompt (the assistant can propose a calmer one) or the picture prompt. You can change the fix, ignore a finding and run one, the marked ones or all as tasks through the run queue; each finding follows its run. If you saw something the analysis did not, write it under *Add what you have seen* with the scene and the second. Old reviews are deleted like runs. The same is available in the terminal with `veritmo check`.

![The Review page](tutorial/en/38-revision.png)

1. **Vision model**: the Ollama models that can read images (the last one you used is remembered); without one only colour and sharpness are measured.
2. **Analyze**: checks every animated clip of the project, with progress.
3. **The frame** that was flagged, as evidence. (In this picture the clip of `s09_side_tracking` was blurred on purpose so that the analysis finds something.)
4. **How it will be fixed**: the kind of fix and its value; you can change both (cut the clip, another seed, another motion prompt, another picture prompt) or ignore the finding.
5. **Fix all pending** (or only the checked ones, or one with its own *Fix* button): each fix is applied to `scenes.yaml` and the scene is made again through the run queue; the finding follows its run.
6. **Add what you saw**: a finding of your own, with the scene, the second and what you saw (the second finding of the picture, `s12_wave`, is one of those).
7. **Earlier reviews**, which can be deleted one by one or all together, like runs.

### Using a clip you already have

If a shot comes out badly with the project's model, make it somewhere else (another project with a better model, another tool) and bring it back: in the scene, choose **My clip** next to *Generate with AI* and *My image*, then pick a video, upload one, or take **a clip another project kept** (every run keeps each scene's clip, and, for generated scenes, its complete animation, so you can reuse the good one and trim it again). The scene lasts what its narration or `silence_seconds` says: a longer video is cut, a shorter one is slowed a little and repeated. The clip has **From** and **To** fields (an animated scene has them too, to use only the good part of the animation) and a **Speed** field: 0.5 plays it at half speed, 2 at double, empty leaves it automatic (in seconds) with buttons that take the instant the preview is paused at, to use only the good part of it. A clip taken from another project is **copied**, so deleting that project does not break this one. Nothing is left loose: the clips of scenes that no longer exist are removed after a run, a video that no scene uses any more is removed when you save, and deleting a project or a run's files removes the kept clips. In the file it is `clip: stand-up.mp4` (see `SCENES_YAML.md`).

![A scene with its own clip](tutorial/en/39-editor-mi-clip.png)

1. **My clip** next to *Generate with AI* and *My image*.
2. **The video**: one of the project's `sources/`, one a **different project kept** (the group *Clips from other projects*, copied when chosen) or *Upload video*.
3. **The preview** of the chosen clip (here, the complete animation made with the A14B for the stand-up shot).
4. **From** and 5. **To**, in seconds, with the button inside each field that takes the instant the preview is paused at; here the part is 0 to 1.5 s.
6. **Speed**: empty is automatic, 0.5 plays it at half speed and 2 at double.

### Titles, credits and motion graphics

Text is what diffusion models draw worst, and what a browser draws best. So there are two ways to put text on screen, both written by the model and drawn with **Chrome or Chromium** (nothing to download: the Environment screen shows which one is used, and Settings > Browser lets you pick another):

1. **An overlay on a scene.** In any scene, near the bottom of the form, *Add an overlay*. Write the text as you want it, or describe it («big title with the name and a thin line under it»), choose **Top, Center or Bottom**, and, if you want, **From** and **To** in seconds (empty is the whole scene). The overlay is animated over the scene's picture, animation or clip, and the picture stays visible around it.
2. **A scene that is a motion graphic.** In the scene, next to *Generate with AI*, *My image* and *My clip*, choose **Motion graphic** and say what it shows («three bullet points appearing one by one over dark blue»). The whole scene is an animated page as long as its narration: lists, diagrams, charts, title cards, credits. A project made only of these needs no GPU at all. To start one, on the Projects screen choose **Motion graphics project**: it creates a video with three silent pages (a title, a list and the credits) joined with a short fade, ready to edit, with *Write the page* or a run.

The page is written **once**, the first time a run needs it (or when you press *Write the page*), and stored in the project: after that the same page is reused, so the result does not change between runs and the model is not called again. The **preview** plays it in the editor with a time slider, without drawing any video; *Edit the page* lets you change its source by hand, and *Write another page* asks for a new one. Where it says how the page was made («written by qwen3:8b · Chrome 126», «then edited») is also what the clip's details show in the clip picker. The model that writes it is chosen in Settings > *Model for motion graphics* (empty uses `gemma4:26b`, or `gemma4:31b`, when installed, and otherwise the prompt assistant's; `gemma4` writes much better pages than a small model) and its provider in Settings > Tasks (*Motion graphics and overlays*). Like the other AI helpers, the controls that ask the model for a page (the *Motion graphic* option, adding an overlay, the *Motion graphics project* button and *Write the page*) are **not shown** when no model can write it, and a note says what to install (`ollama pull gemma4:26b`); pages already stored keep playing, can be edited by hand and are rendered in a run without any model. If the browser is missing, the buttons stay disabled and say why.

**The timeline.** Pages written now declare their entrances as a list of *tracks* (what moves, when it starts, how long it lasts, with which curve) instead of CSS. When a page has them, the editor shows a **Timeline** under the preview: one row per track with a bar over the time axis of the scene. Drag a bar to move it or its right edge to change its duration (in steps of 0.05 s); pick a row to change its start, duration, stagger, curve and values by number. The preview changes at once, **Undo** and **Redo** walk through your edits, and *Save the changes* stores the page so the video uses it. It does not need any model, so it keeps working when the AI controls are hidden. A page written before this animates with CSS and has no tracks: the editor says so, and *Write another page* makes one that has them.

The video is drawn frame by frame, so it is exactly the same every time, takes seconds, and follows the narration's length: a longer narration draws it again, nothing else does. Transitions, voices and background music work on these scenes like on any other. In the file: `motion_graphic: ...` and `overlay: {text, position, start, end}` (see `SCENES_YAML.md`).

### Fades between scenes

By default shots are joined with cuts. In the project defaults, **Fade between scenes (s)** (for example 0.4) makes every join a short crossfade, with the sound crossfaded too, which hides the jump between two clips; in each scene, **Fade in** overrides it (0 is a cut, empty is the project's). The video gets a little shorter, by the sum of the fades.

![Fade between scenes in the project defaults](tutorial/en/40-editor-fundido-proyecto.png)

1. **Fade between scenes (s)** in the project defaults: 0 or empty is a hard cut.
2. **Transition type**: how the clips join (crossfade by default).

![Fade in of one scene](tutorial/en/41-editor-fundido-escena.png)

1. **Fade in** of a scene: empty uses the project's, 0 is a cut and any other number is the length of the transition into this scene from the previous one; **Type** picks its kind (*The project's* by default).

Next to the fade, **Transition type** (in the project and in each scene, where *The project's* is the default) changes how the clips join: crossfade, fade through black or white, dissolve, wipes, slides, circle open/close, radial or pixelate. It only matters when the fade is longer than 0.

### When a picture does not fit the card

If the graphics card runs out of memory making a picture (a resolution too high for the model), the run **does not fail**: it makes the picture again about a quarter smaller, up to twice, and puts a warning in the run ("the card ran out of memory at 1408x768: the picture was made again at 1216x640"). The picture is used at the size it was made at (the video scales it) and the images manifest records that size. The warning tells you to lower the resolution of the project so the first attempt is not wasted. A picture made from one of yours (image to image) is not retried, and neither is any other kind of failure.

### If you want more quality

Wan 2.2 A14B (two experts) keeps faces and bodies steadier, but takes about 680 s per shot of 33 frames and ~21 GiB of RAM. The Wan model is the project's, so you cannot mix the two in one project: copy the project, put the A14B in the copy's `defaults.animate` and keep in it only the shots that are hard (the stand-up and the first skating one), 11 minutes each. Then bring each good clip back into the main project with **My clip**: choose the clip the copy kept (every run keeps each scene's clip in `out/clips/`), which is copied into the project, as the section above explains.
