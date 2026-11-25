# Music mode (`output.mode: music`)

User reference for the "music" output mode. A `scenes.yaml` with
`output.mode: music` produces **complete songs** (vocals and instrumental, or
instrumental only) from a style and, if you want, your own lyrics. It is a
standalone feature: it is not mixed with video or images. It runs
locally, with no cloud and no quotas, using [acestep.cpp](https://github.com/ServeurpersoCom/acestep.cpp)
(ACE-Step 1.5 in C++/GGML, MIT licence).

Each track goes through two phases, each in its own process so that the model
leaves the GPU when it finishes: `compose` (`ace-lm`: the language model writes
the lyrics and the audio codes from the style) and `synth` (`ace-synth`:
DiT + VAE, 48 kHz stereo audio), followed by a lightweight `export` phase.

Optionally, songs can be made with **HeartMuLa**, another generator that is higher quality but slower and runs as a local service (or on another machine): see [HeartMuLa generator (optional)](#heartmula-generator-optional). There is a third optional generator, **DiffRhythm2**, which makes complete songs in half a minute but only with English lyrics: see [DiffRhythm2 generator (optional)](#diffrhythm2-generator-optional).

## Installation

```bash
bash scripts/build-acestep.sh          # compiles acestep.cpp with CUDA and installs bin/ace-lm and bin/ace-synth
```

On CachyOS/Arch (GCC 16) nvcc needs an older host compiler: the
script tries `g++-13`, `g++-14` and `g++-15`, or you can set it with
`NVCC_CCBIN=g++-13`.

The script always builds a **pinned** version of the engine (the variable
`ACESTEP_REF` in `scripts/build-acestep.sh`, currently `d881ad2`), the same one
everything was measured with, instead of whatever the author last published. To bump it, change
that commit, run the script again and run `go test ./internal/music/music`. The CUDA architectures are read from `nvidia-smi`
(`CUDA_ARCHS="89;120"` to force them). The script does not download models.

The models go in `models/music/` (four GGUF files: language model,
text encoder, synthesis model and VAE). Install them from the
**Music** tab of the Models screen or with the API:

```bash
curl -X POST http://127.0.0.1:4666/api/downloads -d '{"id":"music-acestep-xl-turbo"}'
```

| Catalogue entry | What changes | Size |
|---|---|---|
| `music-acestep-xl-turbo` | The default one: LM 1,7B + DiT XL turbo (better sound quality with the same 8 steps) | ~8,4 GB |
| `music-acestep-1-7b-turbo` | DiT turbo (2B), lighter. It must be requested with `synth_model: acestep-v15-turbo-Q8_0.gguf` | ~5,6 GB |
| `music-acestep-0-6b-turbo` | LM 0,6B: lighter and faster to compose | ~4,4 GB |
| `music-acestep-4b-turbo` | LM 4B (Q6_K): better lyrics and metadata when you only give a style | ~7,1 GB |
| `music-acestep-sft` | DiT SFT: maximum quality, with `inference_steps: 50` and `shift: 1.0` | ~5,6 GB |

The packages share pieces (the text encoder and the VAE always, and the
language model and the synthesis model when they coincide), so installing one
after another only downloads what is missing. The LM 0,6B and 4B packages bring
the turbo DiT: with them, or with the SFT, set `synth_model` in `defaults.music`,
because the default model is the XL. Measured on a 2-minute track with the same
lyrics and seed: turbo synthesises in 6 s, the XL in 7-9 s and the SFT in 10-11 s;
the VRAM peak is the same (8,4 GB on the 5060 Ti) and the XL fits on the 12 GB
4070. The 4B language model composes in 11 s with a 6,0 GB peak. If you
use the SFT, write `inference_steps: 50` and `shift: 1.0` in `defaults.music`.

In the studio, the engine panel has a **Quality** selector that writes the
synthesis model and its steps in one go: **Fast** (turbo), **Normal** (the default XL turbo: it
writes nothing) and **Maximum** (SFT with `inference_steps: 50` and `shift: 1`, and the one
required for `extract`). A level whose model is not installed is flagged; if you adjust
the model, the steps or the shift by hand, the selector switches to **Custom**.

The `base` variants (covers and repaint, outside this version),
`shift1`/`shift3`/`continuous` (fine adjustments) and the `BF16` ones (twice the
size with no audible improvement) are not offered, although they exist in the
repository. The GGUFs are a third-party conversion (Serveurperso) under the MIT licence.

## Activation

```yaml
# scenes.yaml
project: canciones
output:
  mode: music
```

```bash
bin/veritmo --mode music scenes.yaml     # or one-off from the CLI
```

A music project uses `tracks:` instead of `scenes:`; the two are
mutually exclusive depending on the mode (declaring scenes in music, or tracks in video or
images, is a validation error). `output.file` is not required.

## Dependencies (pre-flight)

| Mode | Binaries required in PATH |
|---|---|
| `music` | `ace-synth`, and `ace-lm` only if some track to be processed uses the language model |

`piper`, `sd`, `ffmpeg` and `ffprobe` are never required. Before launching any
process it is also checked that the models exist in `models/music/`, and the
error message says which file is missing and where to install it.

## Tracks (`tracks`)

```yaml
tracks:
  - id: balada                 # unique; letters, digits, '.', '_' and '-'
    prompt: Spanish pop ballad with piano and warm male vocals   # the style (required)
    lyrics: |                  # optional; [Verse] / [Chorus] tags
      [Verse]
      Camino por la calle bajo la lluvia
      [Chorus]
      Vuelvo a casa, vuelvo a ti
    vocal_language: es
    duration_seconds: 120
    bpm: 90
    keyscale: C minor
    timesignature: "4"
    seed: 7
    variants: 3
  - id: base
    prompt: Relaxed lo-fi hip hop with mellow piano and soft drums
    instrumental: true
```

| Field | Notes |
|---|---|
| `prompt` | Description of the style (caption). Required. |
| `lyrics` | Lyrics with section tags. They are sung as written. |
| `instrumental` | `true` = no vocals (the engine expresses it with `[Instrumental]`); if there are `lyrics` they are ignored with a warning. |
| `duration_seconds`, `bpm`, `keyscale`, `timesignature` | Optional; whatever you leave out is decided by the language model. |
| `vocal_language` | Language of the vocals (`es`, `en`, `fr`, `de`, `it`, `pt`, `ja`, `ko`, `zh`, `ru`). If you do not set it (neither on the track nor in `defaults.music`), it is **deduced from the lyrics** and, if there are no lyrics or it is unclear, it is `en`: it is never left to the model, which used to pick languages at random. An explicit value always wins. Instrumental tracks and task tracks (cover, repaint, extract) are left alone. The language is part of the cache: tracks that did not declare it are regenerated once on upgrade. |
| `vocals` | The voice requested: `female`, `male`, `duet`, `child`, `choir` or `deep`. Without it (neither on the track nor in `defaults.music`) the model chooses. The engine has no voice parameter: an English phrase is appended to the end of the style (e.g. `female vocals`), so it is a hint that the model follows with more or less fidelity. Only the voices that came out well in the listening tests are offered (`chipmunk`, `yoda`/old man and `robot` did not work). It is ignored on instrumental tracks and in `extract`. It is part of the cache. You can also write it yourself in the `prompt`. |
| `seed`, `variants` | Same as for images (see "Seeds"). |
| `tags` | Only with the HeartMuLa and DiffRhythm2 generators: comma-separated English words (or a list) that describe the style: `piano,happy,wedding`. They are normalised (lowercase, no spaces, no duplicates). Required with that generator. |
| `use_lm` | `true`/`false`; `false` by default if the track has its own lyrics and `true` if not. |
| `title` | The title written into the mp3 tags (by default the `id` with spaces). A version of a set comes out as «title (v2)». |
| `cover` | `off` = no cover art, or an image from `sources/` (`.png`, `.jpg`, `.jpeg`, `.webp`) to use as is; without it, the project's one. |

### Who writes the lyrics?

- **With your own lyrics** (`lyrics` present) `ace-lm` is not run: the style and the
  lyrics reach the synthesis as they are. It is faster, uses less VRAM and the lyrics
  are sung as you wrote them. It is the recommended route, especially in Spanish.
- **With a style only**, the language model composes the lyrics, fills in bpm,
  key and time signature, and enriches the style. In the tests, with the 1,7B model
  and a Spanish style it mixed in English lines in one test; in another, all three
  models (0,6B, 1,7B and 4B) composed in Spanish, so it is not systematic. The
  4B one was the most coherent. Even so, for Spanish the most reliable thing is to write the lyrics yourself.
- With `use_lm: true` and your own lyrics, the language model keeps the lyrics
  as they are but does fill in whatever metadata is missing.

## `defaults.music`

```yaml
defaults:
  music:
    models_dir: models/music          # default
    lm_model: acestep-5Hz-lm-1.7B-Q8_0.gguf       # default; only if some track uses the language model
    synth_model: acestep-v15-xl-turbo-Q8_0.gguf   # default
    output_format: mp3                # mp3 (default) or wav
    mp3_bitrate: 128
    inference_steps: 8
    guidance_scale: 1.0
    shift: 3.0
    solver: euler
    lm_temperature: 0.85
    lm_cfg_scale: 2.0
    lm_top_p: 0.9
    lm_top_k: 0
    lm_negative_prompt: ""
    vocal_language: en                # default for tracks that do not declare it
    vocals: female                    # default voice for tracks that do not choose their own
    style_prefix: producción analógica cálida   # prepended to the style of every track
    adapter: chinese-new-year         # one adapter (LoRA) for all tracks that do not set their own
    adapter_scale: 1.0                # its strength (0 = no effect; less than 1 = softer)
    artist: Mi nombre                 # goes into the tags of each mp3 (by default, the project name)
    cover: auto                       # one cover per song, made with the project's image model (by default, none)
    generator: heartmula              # ace-step (default), heartmula (higher quality) or diffrhythm2 (fast, English lyrics only)
```

`style_prefix` is placed in front of the `prompt` of **all** tracks, separated by
a comma («producción analógica cálida, balada pop con piano»), to share a
sound without repeating it; it is not added to the lyrics. The manifest stores the already
composed style, and changing the prefix regenerates the tracks. In the studio it is edited in the
"Engine and run" panel.

Everything is optional: an absent value is not sent to the engine, which uses its own. Model
names accept the `.gguf` suffix or not; it is always sent with it
(the engine requires it). `defaults.subject_prompt` and `defaults.negative_prompt` are for
images: in music they are ignored with a warning. `wav` produces 16-bit PCM.

## Adapters (LoRA)

An adapter changes the **sound** of all songs (a genre, a voice
timbre, a festive style) without changing the model: the engine merges it into the
synthesis model at start (under a second) and then runs at full speed. They are installed
from **Models → Music** (today the catalogue has the official "Chinese New Year" one from
ACE-Step, 88 MB) or by URL/Hugging Face, and are stored in `models/music/adapters/`.
An adapter is **a `.safetensors` file or a folder** with
`adapter_model.safetensors` and `adapter_config.json` (the PEFT format of the
official ones); in the lists it appears only once, by the name of the file or of the
folder, and it is deleted in one go.

They are chosen in the studio (in "Engine and run" for the whole project, and on each
track to change it just there) or in `scenes.yaml`:

```yaml
defaults:
  music: {adapter: chinese-new-year, adapter_scale: 0.8}
tracks:
  - id: festiva
    prompt: Festive pop with erhu and drums
    adapter_scale: 1.0          # this track, stronger
  - id: normal
    prompt: Piano ballad
    adapter_scale: 0            # this track, no adapter effect
```

The track overrides the project, and whatever it does not set it inherits (a track that names
its adapter but not the strength uses the project's). The adapter only affects
synthesis: composing the lyrics does not use it, and changing it regenerates the audio without composing
again. An adapter that is not installed is rejected before launching anything,
naming it. Beware: not all community adapters work: the engine requires
that the keys of their tensors match the model, and one that does not match is rejected when
synthesising with "no tensors merged (model mismatch)"; the base model must also be respected
(the 2B ones are not valid for the XL, which is the default model: with the catalogue adapter, set `synth_model: acestep-v15-turbo-Q8_0.gguf`). That is why the catalogue only offers
verified adapters.

## Style templates

Under the style box of each track (and under the project's "Style shared by all tracks") there is
a collapsed list of **templates** by family (Rock and pop, Electronic, Acoustic and folk, Urban,
Jazz y blues, Cine y orquesta, Del mundo: Rock and pop, Electronic, Acoustic and folk, Urban, Jazz and
blues, Film and orchestra, World). Each one is a short text in English, the language the model
understands best, tied to a genre ("Soundtrack" adds *cinematic film score with sweeping strings, a slowly
building emotional arc and a spacious mix*; hover over it to see it).

- Choosing a template **adds its text** to the end of the style, after a comma; choosing it
  again removes it. You can combine several and edit the text by hand.
- A template appears as chosen as long as its text is verbatim in the style:
  if you edit it, it releases itself. There is no new field in the file: the style
  remains a single text.

## Assistant: write the song with AI

In the studio, each track has **Write with AI**: you describe the song in
any language ("una balada triste sobre volver a casa una noche de lluvia, con
piano" — a sad ballad about coming home on a rainy night, with piano) and the assistant proposes a **style in English** and **lyrics with tags**
(`[Verse]`, `[Chorus]`…) in the language of your description. You see the proposal and
decide: **Use** writes the style into the track's prompt and the lyrics into its lyrics
(on an instrumental track only the style is proposed and the lyrics are not touched);
**Descartar** (Discard) leaves it as it was. Nothing is ever applied on its own.

It is the same local assistant as the image one (Ollama, `qwen3:8b` by default,
**CPU only**: the GPUs are not used), so it takes a few tens of seconds, can be
cancelled and waits if a run is in progress so as not to compete for memory.
From the terminal: `veritmo assist music "<descripción>"` (with
`--instrumental` it writes only the style) and via the API `POST /api/assist` with
`"task": "music"` (see `docs/http-api.md`).

**With HeartMuLa or DiffRhythm2** the proposal also includes the **tags** (the
only style words those generators read) and the lyrics carry the structure tags
they read, in lowercase (`[intro]`, `[verse]`, `[chorus]`, `[bridge]`,
`[outro]`, `[inst]`, `[solo]`, `[hook]`, `[break]`, `[end]`; one they do not know is
removed). When you press **Usar** the tags are written into the track's field, so
it can be created without another step (the button that proposes them from the style stays, and if the
proposal brings no tags the field says so). DiffRhythm2 receives the lyrics in English
(it only reads English) and HeartMuLa in the language of your description.

## Extracting from a video or audio

Each new track (without a task) has the **Take it from a video or audio** button: you choose a
song (an `.mp3` or `.wav`: one you upload, one of the project's sources or the one from a
YouTube video) or a **video** (`.mp4`, `.mov`, `.mkv` or `.webm`, which is saved as a
video in `sources/`), and **its lyrics, its language, its tempo and its key** are read and a
**style** and the **tags** are proposed. It is a **proposal you review**: nothing is
written into the track until you press **Apply**, and everything can be edited beforehand.

- **The lyrics** are read by Whisper (whisper.cpp, `whisper-cli`) on your machine and **on the CPU**:
  the audio does not leave it. It must be installed once (`bash scripts/get-whisper.sh`,
  see `docs/INSTALL.md`); without it the button is disabled and says what is missing. Measured: about
  10 s for 30 s of song and about 45 s for 3 minutes. It is a **transcription**: a
  sung voice is read less well than a spoken one. On a 3-minute song with known lyrics
  (`cleric-ref`) the word error was **23 %**. The assistant arranges it into sections
  by repeated lines (a block that comes back is the chorus) **without changing a
  word**: the server checks that there are no new words and, if the model changes them,
  it leaves the transcription as it was heard under a single tag and says so. The sections are
  a suggestion: the model is sometimes wrong (for example, it calls the first verse `[intro]`).
- **How to recognise the song** (in the same window, and next to the ear button on the Crear
  (Create) page): **Whisper** (the lyrics and their language, which are always read), **LAION-CLAP** (it also *hears
  the sound*: it scores the song against the genre, mood and instrument tags of the
  music vocabulary and gives the best ones —3 genres, 2 moods and 4 instruments— to the assistant
  as a hint; the proposal shows what it heard) and **MuQ-MuLan** (trained on music only, it understands the style in English; it scores the first minute against the same tags with the `diffrhythm2 classify` executable, on CPU, about 6 s per song; it needs `bash scripts/get-services.sh --only diffrhythm2` and the audio weights converted with `diffrhythm2 convert --audio`, otherwise it is disabled saying what is missing; its weights are CC-BY-NC, non-commercial use only). CLAP is
  an **experiment**: in the recognition study it separated genres poorly, so it is a
  hint and not a verdict. It needs its service to be started in **Environment**; if it is
  stopped, the option is disabled with a link. One song at a time: 1 s on the GPU.
- **The language** is detected once at the start (from the first seconds: if it starts with a long
  voiceless intro it may be wrong) and is kept for the whole file; you can set it
  beforehand (`language`) or change it in the proposal.
- **Separate the voice first** (ACE-Step and audio only, not videos) adds and creates an
  `extract` track of the voice, like "Separate into instruments", and reads the lyrics from it. It is a
  GPU generation with the SFT model (about 8 s per minute of song). **It is almost never
  needed**: in the `cleric-ref` test (3 minutes, with instruments) the error was 23 %
  reading the song as is and 22 % from the separated voice. It is off by default.
- **The tempo and the key** are computed by the server itself (no models or GPU) from the
  whole song. They are **estimates**: the tempo is given along with its half and its double in case
  the hit is an octave off, and the key may come out as its relative. Measured with 32 songs
  generated here with a requested tempo and key: the tempo was right (or came out at half or
  double) in 28 and exact in 23; the key, exact in 23 and relative in 3 ("requested" is what
  was told to the generator, which does not always comply). Without a clear pulse or key
  they come out empty. With ACE-Step they are written into `bpm` and `keyscale` if you leave the checkbox ticked;
  HeartMuLa and DiffRhythm2 do not use them, so they are only shown. The time signature is not measured.
- **Describe the image** (videos only): a vision model (the same as "Describir una
  reference", with all its safeguards, including the one that a person who looks like a minor
  deletes the file) describes a few frames. It is shown and can be copied, but it is **not
  written into the track**.
- **What you know about the song** (genre, instruments, mood) is sent to the assistant
  together with the lyrics and the measurements. The style is deduced from that: the server removes from the style and
  from the tags any instrument that neither you nor the image have mentioned.
- Without the assistant the proposal is reduced to the transcription, the language, the tempo and the
  key (the style and the tags need the assistant). A source without words is
  reported and the option to **mark the track as instrumental** is offered (only if you tick it).

Use only material you hold the rights to. Via the API: `POST /api/projects/{id}/transcribe`
and `POST /api/projects/{id}/measure` (see `docs/http-api.md`).

## Engine: programs or server

By default each phase starts its own program (`ace-lm`, `ace-synth`) and the models are
loaded and released each time. With `engine: server` the run starts **one
`ace-server`** (only on `127.0.0.1`, free port, one per GPU in use), sends it
the same jobs and stops it when it finishes, fails or is cancelled:

```yaml
defaults:
  music:
    engine: server        # cli (default) | server
    keep_loaded: true     # server only (default true): models stay resident between jobs
```

The requests, the cache, the seeds, the variants, the adapters, the
covers (the source audio travels with the job), the manifest and the events are
the same: changing engine does not invalidate the cache. With `server` only `ace-server`
is needed (which `scripts/build-acestep.sh` installs alongside the other two
programs), not `ace-lm` or `ace-synth`.

What is gained, measured on an RTX 5060 Ti with the turbo DiT and a 30 s song:

| Engine | Composition (LM) | Synthesis |
|---|---|---|
| Programs | ~2,7 s | ~3,2 s |
| Server without `keep_loaded` | ~2,8 s | ~3,2 s |
| Server with `keep_loaded` | ~2,5 s | ~1,2 s |

That is, about 2 s per song, in exchange for **having the language model and the
synthesis model on the GPU at the same time throughout the run**: with the 1,7B LM the measured
peaks add up to about 12,8 GB, more than a 12 GB card has. Without
`keep_loaded` the server swaps models like the programs and gains nothing.
It is worth it with many tracks or variants; for a single song, no. With
the resident server, no RAM is reserved per phase.

**Covers, repaint and extract with the resident server** also need about 3 GB
to encode the source audio (measured with a 20 s source on a 16 GB
card: with no room, the job fails with `out of memory`). If that happens, the error says so
and suggests `keep_loaded: false` (or `engine: cli`); the whole run worked with
`keep_loaded: false`. If the server fails, the message includes its last lines.

## HeartMuLa generator (optional)

[HeartMuLa](https://github.com/HeartMuLa/heartlib) (Apache 2.0 licence) is a second song generator: in exchange for being about ten times slower than ACE-Step, the songs sound much better (vocals, instruments and structure). It is a **.NET service** (`bin/services/heartmula`) that the veritmo talks to over HTTP, just as it talks to Ollama: if you have it on this machine it uses it, if not you can point it to the one on another machine, and if there is none **the option does not even appear** in the music project.

```yaml
defaults:
  music:
    generator: heartmula        # default: ace-step
tracks:
  - id: boda
    prompt: Warm acoustic wedding song with piano and a soft female voice   # still the style, for the title and the cover art
    tags: piano,acoustic,wedding,romantic,female                            # what HeartMuLa reads
    duration_seconds: 60
    seed: 7
    lyrics: |-
      [Verse]
      ...
```

**What it does differently.** HeartMuLa does not read the long style, only the lyrics and a few short **tags** (`tags`, required on each track; the studio proposes them from the style with the assistant and they can be edited). When the assistant proposes them, it picks them from a **vocabulary** of 109 tags in seven categories (genre, mood, instrument, tempo, voice, era and production, spelled as in the model's dictionary: `hip-hop`, `lo-fi`, `synth`…) and the server discards those that are not in it; by hand you can write whichever you want, because HeartMuLa reads them as free text. See [docs/ASSISTANT.md](ASSISTANT.md#songs-style-tags-and-lyrics-for-each-generator). It does not run the `compose` phase: all the work is one synthesis per variant, with its seed. The song comes back as WAV and goes through the same steps as any other (mp3, limiter and volume, file tags and cover art, mixer, music video). It lasts as long as the lyrics last: `duration_seconds` is only the cap (plus a margin; 240 s if you do not set it).

**What it cannot do** (the studio hides it; a hand-written `scenes.yaml` fails or warns): the tasks (`cover`, `repaint`, `extract`, extend, repaint a section and split into instruments), the reference (`reference`) and tracks without lyrics or instrumental ones are **errors**; `bpm`, `keyscale` and `timesignature` are ignored with a warning.

**What is needed.** Two things (the installer, `bash scripts/install.sh`, builds the service from the first point):

1. The service, built with `bash scripts/get-services.sh` (the .NET SDK is required).
2. The weights (about 21 GB): they are installed from the panel (**Models**, **Music** tab, "HeartMuLa" catalogue entry) into `models/music/heartmula/`.

And a GPU with about 10,5 GB free: the orchestrator picks the one with the most free memory (or the one you indicate with `--gpu`) and, if none reaches it, the run fails saying how much each one has. **You start the service yourself** from the **Environment** screen, "Music services" panel (`vt-heartmula`, it listens only on `127.0.0.1`), with the **Start** button and the card you prefer, and it stays up between songs until you stop it with **Stop**: neither a run nor shutting down the server stops it. If you launch a run from the application with the service stopped, it is rejected immediately (422 `engine_not_running`) and the studio warns, with the link to Environment, and disables the create button. With the command line without a server (`veritmo escenas.yaml`) the behaviour is as always: it starts the service at the beginning of the run and stops it at the end, whether it fails or is cancelled, unless it was already up. Inside, it frees the GPU after each song.

**On another machine.** If the computer does not have enough GPU, run the service on another one (build it there with `scripts/get-services.sh`) and enter its address (and the key, if you protected it with `HEARTMULA_TOKEN`) in **Settings › HeartMuLa music generator**, with "Test" to check it. An address that is not this machine is "external": the lyrics and tags of your songs **are sent there**, and to save it you have to tick that you agree to send them to that address (you are asked once per address). With a saved address that one is always used and the local service is not started; if that address does not respond, the option disappears from the project (the reason is shown in Settings).

**In the studio.** The "Engine and run" panel shows the **Generator** selector (ACE-Step or HeartMuLa) only when one is available; if something local is missing (the service or the weights) a warning appears with where to install it, and if the problem is that the external address does not respond nothing is shown. When HeartMuLa is chosen, the controls for the language model, quality, engine and steps disappear, and each track swaps the reference, key, time signature and bpm for the **Tags** field (with a button that proposes them from the style). A track that already had a reference keeps showing that selector, with a warning, so that you can remove it.

**Cache and seeds.** The key of a song includes the generator, the lyrics, the tags, the seed, the maximum duration and the sampling; changing any of them redoes it, and ACE-Step projects keep theirs. As the model has no seed of its own, the service fixes the one it is sent: the same seed and the same lyrics start the same way.

**Measured performance** (song of about 2 minutes): on the 16 GB RTX 5060 Ti, 109 s of song in 123 s; on the 12 GB RTX 4070, 116 s in 100 s with a 10 GB peak. ACE-Step does 110 s in about 22 s. A 60 s song took 74 s on the 4070 and 78 s on the 5060 Ti.

## DiffRhythm2 generator (optional)

[DiffRhythm2](https://github.com/ASLP-lab/DiffRhythm2) (ASLP-lab, Apache 2.0 licence, 1.000 million parameters; **note: it downloads the MuQ-MuLan and MuQ weights, which are CC-BY-NC 4.0, non-commercial**) is a third song generator, with the same pattern as HeartMuLa: it is a **.NET service** (`bin/services/diffrhythm2`) that the veritmo talks to over HTTP and it is chosen with `defaults.music.generator`. It makes a complete song with aligned lyrics from the lyrics with structure tags and a few style words, and it is much faster than HeartMuLa.

```yaml
defaults:
  music:
    generator: diffrhythm2      # default: ace-step
    # dr2_cfg_strength: 2.0     # optional: the model's guidance (0,5 to 10; default 2,0)
    # dr2_steps: 16             # optional: the sampling steps (4 to 64; default 16)
tracks:
  - id: noche
    prompt: Warm pop song with piano     # still the style, for the title and the cover art
    tags: pop,piano,bass,drums,warm      # what DiffRhythm2 reads (it is passed as «pop, piano, bass, drums, warm»)
    vocal_language: en
    lyrics: |-
      [verse]
      Walking down the empty street tonight
      [chorus]
      Take me home, take me home
```

**What it does.** A song of about 2 and a half minutes takes about 30 seconds and the memory peak is about 7,8 GB of GPU (it fits on a 12 GB card). `duration_seconds` is only the **cap** (plus 15 s of margin, never more than 210 s): the model finishes when the lyrics end. The structure tags it knows are `[start]`, `[intro]`, `[verse]`, `[chorus]`, `[bridge]`, `[outro]`, `[inst]`, `[solo]`, `[hook]`, `[break]` and `[end]`; the service lowercases them, discards the others and adds `[start]` if it is missing. The song is stereo at 48 kHz, but the stereo is fake: the right channel is the left one, quieter and 10 ms late. The model has no seed of its own: the service fixes the one it is sent, and the same seed gives the same song.

**What it cannot do** (the studio warns about it and hides those controls; a hand-written `scenes.yaml` fails or warns):

- **English lyrics only.** The lyrics-to-phonemes converter covers only English and Chinese: Spanish lyrics come out badly pronounced. A track with another `vocal_language` (or with the lyrics detected as another language) is an **error**. With this generator the studio only offers "English" as the voice language.
- **No instrumentals**: a track without lyrics or `instrumental` is an error.
- **It does not transform songs or use references**: the tasks (`cover`, `repaint`, `extract`, extend, repaint a section and split into instruments) and `reference` are errors. (The model accepts a 10 s reference audio as style; this version does not use it.)
- `bpm`, `keyscale`, `timesignature` and `vocals` are ignored with a warning; ACE-Step's language model does not run.

**What is needed.** The same as HeartMuLa, with another service and other weights:

1. The service, built with `bash scripts/get-services.sh` (the .NET SDK is required).
2. The weights (about 9,5 GB: the model, its decoder, two MuQ models and a text encoder) in `models/music/diffrhythm2/`, from the panel (**Models**, **Music** tab).
3. A GPU with about 8 GB free.

**On another machine.** As with HeartMuLa: run the service on another computer (build it there with `scripts/get-services.sh`) and enter its address (and the key, if you protected it with `DIFFRHYTHM2_TOKEN`) in **Settings › DiffRhythm2 music generator**, with "Test". An address that is not this machine is "external": the lyrics and style of your songs **are sent there**, and to save it you have to agree to that. With a saved address the local service is not started.

**In the studio.** The **Generator** selector offers DiffRhythm2 when it is available (if something local is missing, a warning says where to install it). When it is chosen a warning appears with the above, and the non-English language, the instrumental switch, the voice, the tasks, the reference, the bpm, the key, the time signature, "Extend", "Repaint this part" and "Separate into instruments" disappear (a line in the results explains that ACE-Step does that); what it can use remains: each song's waveform, the variants, the favourites, the mixes with their normalisation, the music video and the library. Tracks you already had that this generator cannot do (in Spanish, instrumental, with tasks) **are not touched**: the studio lists them so that you can decide.

**Cache.** The key of a song includes the generator, the lyrics, the tags, the seed, the maximum duration, the guidance and the steps; ACE-Step and HeartMuLa projects keep theirs.

## Seeds and variants

- With an explicit `seed`, variant *i* (0-based) uses `seed + i`.
- Without `seed`, the orchestrator draws a random 32-bit seed per variant and
  **records it in `manifest.json`**. The same seed feeds the language
  model and the synthesis.
- The variants of a track are generated one after another.
- **Compare and keep one.** When a track has several variants already created, the
  studio shows a **Compare variants** player with a button per variant: when
  switching from one to another it continues from the same second (and keeps playing if it was playing). Each
  variant has **Keep this one**: it asks for confirmation, leaves the track with the seed of
  that variant and a single variant (`seed: <la suya>`, without `variants`), moves to the
  first variant the mix tracks that were playing that one, **removes those that used another**, saves and
  creates the track again. It is immediate because nothing is generated: a run reuses from the
  cache any song made with exactly the same inputs, even if it was
  saved under another name, so the same song you chose comes out (the engine does not
  always give the same one twice). When it finishes, the run **deletes the files of the other
  variants** (`<id>_v1.mp3`, `<id>_v2.mp3`…): in general, a run deletes the files that the
  previous manifest attributed to a track it has processed again and no longer produces
  (never those of other tracks nor those of a track that is not processed with `--only`).

## Favourites

Each created song has a star to mark it as a favourite. The mark is stored in
the project (`favorites` in `scenes.yaml`, a list of `{ track, seed }`), so you
have to save as with any change. It is identified by the track and the **seed** it
was made with, not by the variant number: it stays marked if the track changes how many
variants it makes (for example when you keep one). If you rename a track, its
favourites follow it; if you remove it, they go with it. A favourite of a track that no longer
exists only warns on validation. No run reads the favourites.

A track with any favourite carries a star next to its name; when there are favourites
**Only with favorites** appears, which leaves in the list only those tracks; and the mixer's "Add
pista" (Add track) menu puts the favourite songs first, with a star.

## Tags and cover art

Every **mp3** comes out **tagged** (ID3v2.3, written by the program, without touching the audio): title (`title` of
the track, or its `id`), artist (`defaults.music.artist`, or the project name), album (the name of the
project), track number, the final style as a comment and the **sung lyrics** (with accents and symbols).
A `wav` is not tagged.

With `defaults.music.cover: auto` each song also has a **cover**: a square image
(`defaults.resolution`, 768x768 if you set nothing) made with the project's image model
(`defaults.model` or `defaults.model_set`, with its `steps` and `sampler`) from the style of the track
(plus the `style_prefix`). The seed comes from the track's (or from its id), so the same track always gives
the same cover and the cache reuses it (phase `cover`, about 20 s per song with IntoRealism on a 5060 Ti;
one per track even if it has several versions). The cover goes **inside the mp3** and is copied to
`out/<project>/<id>.png` next to the song. A track can bring its own (`cover: mi.png`, an image from
`sources/`, used as is) or disable it (`cover: off`). If the image cannot be made (lack of memory,
for example) the song is created anyway, without a cover, and a warning (`warn`) says so.

## Song library

The **Library** screen (`/library`) gathers the songs of **all music projects** (those in each one's `manifest.json` that are still on disk, most recent first). They can be **searched** (title, project, track, style, key and lyrics; ignoring case and accents, and all the words at once), filtered by project and by minimum stars and sorted by date, stars or title. Each song has a player, **1 to 5 stars** (pressing the same one again removes them), a heart if it is a favourite in its project and a menu to add it to **playlists**. A playlist keeps its order (moved up and down song by song) and ignores songs that no longer exist. **Play all** goes through what the list shows (with its filters) song after song, with previous, next and stop.

The songs, their stars and the playlists are rows of the server's database (see [DATA_STORE.md](DATA_STORE.md#the-song-library-in-the-database)): a song is written when a run of its project ends, so the library opens no folder. The stars and playlists that were kept in `projects/.library.json` are imported when the server starts. A song is identified like the favourites: `proyecto/pista/semilla`. API: `GET /api/library` (songs, stars and playlists), `PUT /api/library/meta` (replaces stars and playlists; up to 200 playlists of 2000 songs), `GET /api/library/songs` (in pages, with search and filters), `PUT /api/library/songs/{key}/rating`, the playlists one by one under `/api/library/playlists` and `POST /api/library/rescan` for audio put or removed by hand.

## Several songs at once (2 or more cards)

With 2 or more graphics cards, a music run that does not pin a GPU uses **all those that fit a song** (9 GB or more) and makes one song on each at the same time; the server decides this at launch. A run of a single song (or restricted to one with `only`) uses the card with the most memory, as always; if you set the GPU by hand, what you set is respected. Measured with 4 instrumental songs of 60 s (RTX 4070 12 GB + RTX 5060 Ti 16 GB): **43 s on one card, 20 s with both**. The unit is the song, not the version: a single song with 4 versions still makes its versions one after another. **I'm lazy** takes this into account in its estimate.

## Equalising the volume of the songs

`defaults.music.loudness_lufs` (between −30 and −8; −14 is the level of music platforms) equalises the integrated loudness of **each song when exporting it**: it is measured, the missing gain is applied (at most ±12 dB) and it goes through the same peak limiter as the rest; the mp3 is encoded once more with the same `mp3_bitrate`. A song that is already within 0,3 dB of the target is not touched. The cached synthesis does not change, so changing the target regenerates nothing: it only redoes the export. The manifest stores per song `loudness_from` (the LUFS it had) and `loudness_gain` (the dB applied). Without adjustment, each song comes out as the engine makes it. The studio offers it in "Engine and run" and **I'm lazy** sets it to −14.

## Automatic review of each song

When a song is exported it is measured with ffmpeg (under a second) and the result goes into the `manifest.json` entry as `check`: `peak_db`, `lufs` and `issues` (empty if all is well). Each problem is a `{code, detail}` and also comes out as a run warning:

| `code` | When |
|---|---|
| `clipping` | the peak exceeds −0,3 dBFS |
| `silence` | there is a silence (< −50 dB) of 4 s or more, at any point |
| `quiet` / `loud` | the integrated loudness is below −24 LUFS or above −8 LUFS |
| `duration` | the real duration deviates more than 15 % from the requested one |

The studio shows the loudness next to each song and the problems with a warning. A song that has not changed since the previous run (same file and same `sha256`) keeps its review without being measured again. Without ffmpeg there is no review and the song is created anyway.

## Music video: the video of a song

Each exported song can be turned into a **video project** ("Create music video" button in the results, or the API
`POST /api/projects/{id}/videoclip/plan` and `/videoclip`):

- The song is split into **shots**: the lyrics are separated by their tags (`[Verse]`, `[Chorus]`...), each section receives
  the time that corresponds to it by the text it sings (a section without lyrics, such as an intro, weighs a couple of lines; an
  instrumental song is cut into equal parts) and is cut into shots of about `shot_seconds` (6 by default, never more than 60 shots).
  Each shot carries the lyric lines it covers and a camera movement (`zoom_in`, `pan_left`, `zoom_out`, `pan_right`;
  stronger in the chorus).
- **The cuts follow the song.** The text gives a first estimate of where each section starts; then the
  audio is read (ffmpeg, the volume envelope every 0,25 s) and each cut between sections is moved to the clearest volume change
  near that estimate (up to 8 s or a third of the shorter of the two sections; only if the change reaches 25 % of the
  average volume around it, so as not to chase noise). That way the chorus enters the shot right where the song rises.
  The plan's response says `sections` and `aligned_cuts` (how many cuts were moved); without ffmpeg, or without clear changes,
  the cuts follow the text. Measured on two songs of 7 sections (160 and 180 s): 5 of the 6 cuts of each were moved,
  and in those the volume changes between 3 and 9 dB on both sides of the new cut.
- The image of each shot is written by the **assistant** (Ollama on CPU) in a single batch per 8 shots, with a common look
  (`subject_prompt`) so that the character does not change; if it cannot, each prompt comes from the song's style and its lines
  and the response carries a warning.
- The video project carries one silent scene per shot (`silence_seconds` = its duration plus the fade that the
  next one eats, so the video ends where the song does: measured with ffprobe, 60,08 s of video for 60,05 s of song),
  `transition_seconds` between scenes and the song copied to `sources/song.mp3` as `bgm` at volume 1 and without *ducking*.
  The models and the resolution are chosen by **I'm lazy** for the time you are willing to wait.
- **Animate the shots**: none (still images with camera movement), **only those of the chorus** (the sections whose tag contains Chorus, Hook, Estribillo or Coro; the option is disabled if the song has none) or all. It is written as `animate: true` in the chosen scenes, and the time that **I'm lazy** estimates counts only the animations that exist.

## Output artefacts

Everything lands in `out/<project>/`:

| Artefact | Path |
|---|---|
| Audio (1 variant) | `out/<project>/<id>.mp3` (or `.wav`) |
| Audio (N variants) | `out/<project>/<id>_v<N>.mp3` (`N` 1-based) |
| Cover (if any) | `out/<project>/<id>.png` (or the extension of your image) |
| Manifest | `out/<project>/manifest.json` (`mode: "music"`, atomic write) |

Each manifest entry records: `track_id`, `variant`, `effective_seed`,
`lm_seed`, `use_lm`, the final `caption`, `lyrics_requested` and `lyrics_sung` (the one
actually sung, in case the language model changed it), bpm, key,
time signature, language, `duration_seconds` (the requested one), `audio_seconds` (the file's),
the models, all the parameters sent to the engine (`params`), `title`, `cover` (the exported image), `file` and `sha256` (the
file hash, so that a mix knows whether the song has been created again). Each
mix also records its `inputs`: the files it read and their hash.

> The real duration of the audio follows the codes generated by the language model,
> not always the `duration_seconds` field: a track requested at 30 s once came out at
> 50 s. That is why the manifest stores both.

## Starting from a song: cover, repaint and extract

A track can transform a song that already exists instead of creating a
new one. It is indicated with `task` and a `source`: an `.mp3` or `.wav` from the project's
`sources/` folder (the file name, not a path).

```yaml
tracks:
  - id: balada_jazz
    prompt: Jazz piano cover with brushed drums and walking bass
    task: cover               # cover | cover-nofsq | repaint | extract
    source: balada.mp3
    cover_strength: 0.6       # cover only: 0 = free, 1 = follows the source
  - id: estribillo_nuevo
    prompt: Same song, bigger chorus with strings
    task: repaint
    source: balada.mp3
    repaint_start: 40         # seconds; the end defaults to the end of the audio
    repaint_end: 65
  - id: solo_voz
    prompt: Isolated vocals
    task: extract
    source: balada.mp3
    stem: vocals              # vocals, backing_vocals, drums, bass, guitar, keyboard,
                              # percussion, strings, synth, fx, brass, woodwinds
```

| Task | What it does |
|---|---|
| `cover` | The same song in another style; the model reinterprets it freely. |
| `cover-nofsq` | A cover that stays close to the original melody and structure (the source is also passed as a timbre reference). |
| `repaint` | Regenerates a section (`repaint_start`–`repaint_end`) and keeps the rest. A negative start or an end past the audio extends it. |
| `extract` | Isolates the voice or an instrument (`stem`). Needs a non-turbo synthesis model (the catalogue's SFT). |

Rules:

- Tracks with a task **do not use the language model** (`use_lm` is ignored with a warning): the source provides the structure and the duration, so `duration_seconds` is not accepted.
- A field belonging to another task is rejected, not ignored (`cover_strength` only with covers, `repaint_*` only with repaint, `stem` only with extract).
- `lego` and `complete` are not supported (they need the base model).
- Variants, seed, adapter and format work as in a new song.
- The `synth` cache includes the **content** of the source: replacing the file with another of the same name synthesises again.
- The manifest records `task` and `source` in each entry.

**Extending a song.** A `repaint` with `repaint_end` past the end of the audio
extends it: that is what the **Extend** button of each created song does. It asks for the seconds to
add (from 5 to 180, 30 by default), copies the song to `sources/` and adds a track
`<id>_larga` with `task: repaint`, `repaint_start` 4 s before the end (so that the
join is not noticeable) and `repaint_end` = duration + the requested seconds. The new track
starts from the same style and the lyrics that were **sung** (or yours if there are none), without
saving: add the verses that continue, and create the track. Measured: a 30 s song with
`repaint_start: 26` and `repaint_end: 60` came out at 60 s. What gets repainted is decided by the
model; it is not guaranteed that the continuation keeps the chorus or the melody, and
it is worth trying more than one seed.

**The waveform of each song.** In the results each song is drawn as a waveform,
with the peaks given by `GET /api/projects/{id}/peaks` (600 bars per row; 1200 in the
section selector), a line that follows the player and the part already heard lighter; a click
jumps to that second. The peaks are requested when the row enters the screen, four at
a time, and are remembered by file and hash (a regenerated song is requested again; a
failure is not repeated). A song whose peaks cannot be read keeps the player and shows
no waveform. Projects hidden by the content mode answer 404 to the peaks
like any other route.

**The section of a repaint, on the waveform.** For a track with `task: repaint` the source is
drawn with its player and a section is dragged that fills in `repaint_start` and
`repaint_end` (tenths of a second). The ends are keyboard-operable controls (arrows 0,1 s,
Shift 1 s, Home and End), a button plays only the section, and with a `bpm` on the track the
section snaps to the beat (Alt: to the bar; the beat comes from `beat_offset`). The numeric
fields are still there and move the section; an end past the audio is kept and drawn
up to the end.

**Repaint this part** (button on each created song) opens the waveform,
copies the song to `sources/`, adds a track `<id>_repintado` with that section, the style and
the lyrics that were sung, saves it and **creates it right away**, on its own (a run with `only`
of that track; the other songs are not touched). **Separar en instrumentos** (Split into
instruments) (on each created song and in the chosen source of a track) adds an `extract` track
for each ticked part (`<canción>_<parte>`, all with the same source), saves the project and creates
them **one after another**, each on its own (a run accepts a single track with `only`; the
studio chains the runs and shows "Pista N de M" (Track N of M); if one fails or is cancelled,
the ones that remain stay added without being created). If the song is one of several variants
of its track, the id carries the variant: `ballad_v3_vocals`, `ballad_v3_repintado`; with a
single variant, `ballad_vocals`; an occupied id is numbered. The button says how many tracks it
creates ("Crear N pistas" (Create N tracks)) and with more than 3 a line says that they are N
generations of about 10 s. Extracting needs a non-turbo synthesis model: the dialog says so and,
if the project's model (`defaults.music.synth_model`) is turbo and another is installed, it
offers (ticked) to move the project to the **Maximum** level (`synth_model` SFT,
`inference_steps: 50`, `shift: 1`): it affects the whole project, so the other songs will use
that level (slower) the next time they are created; without ticking it, the server rejects the
`extract` tracks. If there is no non-turbo model, nothing is added or created. When the tracks
already have their song, **Remove these tracks** (with confirmation)
removes them from the project with their songs and with the copied source if no other track uses
it, and saves; the original song is not touched, and the button disappears as soon as the track
list is edited in any other way or the project is reloaded. Each song that transforms another
shows in the results the task, the instrument of an `extract` and the source. With HeartMuLa or DiffRhythm2 neither is offered and a line explains why.

In the panel, each track has a "What to do" selector. For a
task you choose the source among those of the project, upload an audio
from your computer or use an already created song (it is copied to
`sources/`, so the cover keeps working if you regenerate or delete the
original). `.mp3` and `.wav` of up to 256 MB are accepted. Duplicating a
project also copies its sources; a source that a track uses cannot be deleted.

## Background music in a video

A **video** project (not an images or music one) can carry a soundtrack
under the narration, with a `bgm` block:

```yaml
bgm:
  prompt: Piano suave y atmosférico, tempo lento   # it is generated (instrumental)
  # file: tema.mp3                                 # or an audio from sources/
  volume: 0.25          # 0 to 1, before lowering it under the voice (default 0.25)
  fade_seconds: 2       # fade in and fade out (default 2)
  ducking: true         # lowers the music while the voice speaks (default true)
  seed: 7               # only with prompt; without it, derived from the prompt
```

Exactly one of `prompt` and `file`. `file` is the name of an `.mp3` or
`.wav` from the project's `sources/` folder (the same one the covers use;
the editor allows uploading it). With `prompt`, `ace-synth` and the music
models (synthesis, text encoder and VAE) are needed, but not the language model.

How it works:

- When the concat finishes, the video is measured and an
  **instrumental** track is generated of that duration rounded up to
  multiples of 10 s (between 10 and 240 s; a longer video repeats the
  track). It uses `defaults.music` (synthesis model, steps, adapter, style prefix).
- It is cached like any synthesis: same prompt, same models and same
  duration bracket, it is not generated again. Without `seed`, the seed
  comes from the prompt, so the same prompt gives the same music on every run.
- A single ffmpeg call mixes: the video is copied without re-encoding; the music
  is lowered to `volume`, fades in and out, is compressed against the voice
  (`sidechaincompress`), is added to the narration with `amix` and the result is
  normalised with `loudnorm` (−16 LUFS). The audio comes out as AAC 192k, 44100 Hz, stereo.
- Events: `bgm` (if generated) and `mix`, with `scene_id: "_global"`, after
  `concat`. The intermediate video without music stays in `tmp/video_nobgm.mp4`.

In the project editor, the **Background music** section lets you choose
none, generate it or a file, and adjust the volume, the fade and the lowering under the voice.

## Reference: a new song with the sound of another

A track **without a task** can carry a `reference`: an `.mp3` or
`.wav` from `sources/` whose sound (timbre) guides the new song. It is
not a remix: the structure, the lyrics and the duration still come
from the style and the language model; the reference only steers how it sounds.

```yaml
tracks:
  - id: balada_con_referencia
    prompt: Slow piano ballad
    reference: grabacion.mp3
```

- It is handed to the engine as reference audio (`--ref-audio`; with the server, the
  `ref_audio` part) and **not** as a source: there is no `--src-audio`. The language model
  does not receive it, so changing only the reference re-synthesises without composing again.
- The `synth` cache includes the content of the file; the manifest stores `reference`.
- A track with `task` does not accept `reference` (a faithful remix already uses its source as a reference).
- It is a tendency, not a copy: do not expect it to reproduce the recording.

**Uploading a video.** For a reference, for the source of a remix, or for the
background music of a video, you can upload a video (`.mp4`, `.mov`, `.mkv`,
`.webm`, `.avi`): the server extracts its audio with ffmpeg to
`sources/<nombre>.mp3` and does not keep the video. A video without audio or
an unreadable one is rejected with the reason. The 256 MB limit counts the uploaded file.

**From YouTube.** Next to "Upload audio or video", in the
reference, in the source of a remix and in the background music of a video, you can
paste the address of a YouTube video and press "Add": the server takes
**only the audio** with `yt-dlp` and leaves it in `sources/<título>.mp3`, ready to be
chosen. `yt-dlp` must be installed once (`bash scripts/get-ytdlp.sh`, which downloads
the official binary to `bin/`; run it again to update it when YouTube changes and it
fails). Only single YouTube videos are accepted, up to 20 minutes; no account or
cookies are used, so a private video, members-only, age-restricted or one that
YouTube blocks as automated is rejected with the reason. It can be cancelled while it
downloads. Use only material you hold the rights to: downloading from YouTube may go against its terms.

In the panel, under "What to do" of each track the optional "Reference (song or video)" appears, with the same ways of choosing
the audio (one from the project, uploading one or using an already created song). The
covers are called **Remix** in the menu; in the file they are still `cover` and `cover-nofsq`.

## Mixes: joining songs and audios into one

A music project can declare `mixes`: timelines of **clips** that are
mixed into a single file, `out/<proyecto>/<mezcla>.mp3`. It is useful
for putting a voice over a backing track, layering the instruments that
`extract` produces, or joining sections (verse, chorus) into a long song.

```yaml
tracks:
  - { id: base, prompt: ..., bpm: 90, keyscale: A minor }
  - { id: estribillo, prompt: ..., bpm: 90, keyscale: A minor }
mixes:
  - id: cancion
    gain: 0.9                    # overall volume of the mix (optional)
    normalize: true              # bring it to -14 LUFS (optional)
    clips:
      - track: base
        trim_start: 1.7          # removes the track's initial silence
        trim_end: 24.6
      - track: estribillo
        start: 22                # comes in at second 22 of the mix, slightly on top
        fade_in: 1.5
        trim_start: 1.3
        trim_end: 25.9
      - file: efecto.mp3         # an audio from sources/
        start: 8
        gain: 0.5
```

| Clip field | Notes |
|---|---|
| `track` (+ `variant`) or `file` | **One of the two.** `track` is a track of the project (the variant starts at 1) and its already exported file is taken, so it must be created first. `file` is an `.mp3` or `.wav` from `sources/`. |
| `start` | Second of the mix at which the used part comes in (default 0). |
| `gain` | Volume from 0 to 2 (default 1). |
| `mute` | Mutes the clip: it is not read and does not count towards the duration. |
| `fade_in`, `fade_out` | Fades in seconds, within the used part. If they are longer than it, it only warns and they are shortened. |
| `trim_start`, `trim_end` | Part of the original that is used, in seconds (by default, all of it). |

- The mix lasts until the last clip ends. The clips are summed
  **without normalising** (they respect the volumes you set) and
  a limiter prevents two loud overlapping clips from saturating.
- **Overall volume and normalisation.** The mix itself accepts `gain` (from 0 to 2,
  default 1: it scales the sum of the clips) and `normalize: true`, which after applying
  it brings the mix to −14 LUFS (the level of music platforms, with ffmpeg's `loudnorm`
  and a true-peak ceiling of −1,5 dB), whatever the sum of the tracks' volumes. With
  normalisation on, the overall volume no longer changes how loud it sounds, only the
  headroom it starts from. In "Listen without mixing" you hear the
  overall volume and, with `normalize`, an **approximation** of the normalisation: it is
  computed with the loudness of each track (measured by the server, `GET /loudness`) by
  summing the powers where they overlap; on two test mixes it came out within 0,2–0,3 dB
  of the real one, but it may be off by a couple of dB. The file that "Mix"
  generates is the exact one, and the mixer shows its loudness in LUFS (the manifest
  stores it as `lufs` in the mix). Changing them redoes the mix; the manifest stores the `gain` and `normalize` it was made with.
- The `id` of a mix cannot be that of a track (they share `--only` and file names).
- **Silences.** Each generated song starts with ~1,7 s of silence and ends with a 4–5 s
  tail. In a sequence they must be trimmed, or a gap of several seconds is left. The studio
  does it with **Trim silences**, which writes `trim_start` and
  `trim_end` from the sounding section that the server computes; the render trims nothing on its own.
- **Tempo and key.** Tracks are not corrected against each other: give the same
  `bpm` and the same `keyscale` to those you are going to mix. Otherwise it shows.
- **`mix` phase.** It runs after all the tracks, once per mix
  (`phase_start`/`phase_end` event with the mix id), and is
  sequential. A mix whose audio is missing (an uncreated track, a
  deleted source) fails with a message that names the clip, and the others are made anyway.
- **Cache.** The mix is redone only if the clips or the **content** of
  any input file change (regenerating a track redoes the mixes that use
  it); `--force` ignores it. The manifest carries `mixes` (id, file,
  duration, hash and the clips as they were mixed) only if the project has mixes.
- `--only <id>` accepts a mix or a track: with a mix no track
  is generated (it uses the already exported ones) and only
  ffmpeg and ffprobe are needed; with a track nothing is mixed.
- `GET /api/projects/{id}/peaks` gives the waveform peaks and the
  sounding section of a project audio (see `docs/http-api.md`).

In the studio, the **Mixes** card (collapsible; it opens by itself
if there are mixes) lists the project's mixes, and each one opens in a
**modal window** (**Open mixer**; "Add mix"
opens it at once). The window moves by dragging its top bar, is resized from
any edge or corner (it can reach both edges of the screen), is maximised with
its button (or by double-clicking the bar) and is closed with the X or with Esc.

Without opening the mixer, each mix's row lets you **listen to it** (if
it is already mixed), **rename it**, **duplicate it** (the copy is
called `<id>_copia`) and **remove it** (it asks for confirmation; the
change is applied on saving and the file it already exported is not deleted).

**Fade overlaps:** the timeline button goes through the tracks in order of
start and, where one starts before the previous one ends, puts a fade-out
on the first and a fade-in on the second with the duration of the overlap
(at most that of the shorter track; muted ones do not count). It writes
the fades into the file, so they show in each track's panel, can be
tweaked and are undone in one go. If they are already set, the button is disabled.

**Outdated mixes:** if you create again a song that a mix uses (for example with
"Create this track"), that mix's row shows **Desactualizada:
una pista cambió** (Outdated: a track changed) and a **Volver a mezclar** (Mix
again) button; the mixer warns about it too. It is only detected for the
project's songs (not for the audios in `sources/`) and in mixes made since this change.

**Mixing several tracks at once:** tick the checkbox of each track in
the list and press **Mix these**: a mix is created
with them one after another, in the order of the list (each one starts
where the previous song ends, if it already exists), and the mixer opens.

**Undo and redo:** the mixer has undo and redo buttons (Ctrl+Z and
Ctrl+Shift+Z, or Ctrl+Y; they do not act while you type in a field,
where the browser undoes the text). They apply to any change to the
project since it was opened (up to 100 steps), and dragging a clip
counts as a single step. When the project is reloaded the history starts from zero.

Inside, the mix is a timeline: drag the tracks horizontally, trim them with the handles
at the ends, change the zoom and the snap (besides seconds, it offers **beat** and
**bar** according to the bpm and time signature of the first song that has one, and then
it draws those lines behind the tracks, and when you drop a track with the snap on beat
or bar, what lands on the line is its **first hit**, not the start of the file: the
server looks in the first 30 s of the song for where the beat falls (the offset that
best fits its bpm, trying tempos up to 3 % above and below) and the track is anchored on
the first hit that still sounds after the trim. That way a song that starts with silence
or a soft entrance fits anyway. If there is no clear beat, it is anchored on its **first
sound** (the first non-silent section). The beat does not distinguish beat 1 from the
others: with the snap on bar, the detected hit lands on a bar line even if the song
starts on another beat), and adjust volume, fades, start, trim and mute in the panel of the selected track.

- **Listen to a track alone**: the ▶ to the left of each track plays it alone
  (the part it uses, at its volume and without mix fades except its own); while
  it plays, a small marker shows where it is and **changes to volume, fades and
  trim are heard immediately**. A muted track still sounds if you listen to it alone.
- **Listen without mixing**: plays **all the tracks together** in the browser, each at
  its `start`, with its trims, volume and fades, summed without normalising and
  limited as the render does, **without saving or rendering anything**. Whatever you
  change (moving a track, raising the volume, muting it…) is heard instantly. It
  pauses, stops, and yields to any other player. It is an **approximation** of the exported file.
- **The position bar**: a vertical bar shows where the mix is (the
  preview or the already rendered one) and stays where you pause. It can
  be **dragged** (and so can the marker of a track played alone), and a
  click on the time ruler jumps to that point; with the keyboard, the
  arrows move one second (ten with Shift). It works even if you have never mixed.
- **Mix** saves and renders only that mix;
  the file's player appears when it matches what you see.

## Cache

- `compose` is cached by (style, lyrics, metadata,
  effective seed, `lm_*` parameters and language model file).
- `synth` is cached by the content of the resulting request, the DiT file
  and the VAE. **Changing only synthesis parameters** (steps, guidance,
  shift, solver, format, bitrate) **does not run the language model again**.
- A track without `seed` draws a new seed on every run,
  so it is always regenerated; fix the seed of the
  variant you like so that you can reproduce and cache it.
- With HeartMuLa there is no `compose`: the synthesis key includes the generator, the lyrics, the tags, the seed, the maximum duration and the sampling (see [HeartMuLa generator](#heartmula-generator-optional)).
- `--keep-tmp` keeps the cache between runs, as in the other modes.

## Flags

- `--only <track-id>`: processes only that track; the audio of the others is not
  touched and `manifest.json` keeps their entries (those of the previous manifest
  whose track is still in the project and whose file still exists), alongside the
  new ones of the processed track and in project order. A run without `--only`
  rewrites the whole manifest. An unknown id is rejected before launching anything.
- `--force`: regenerates all phases and variants of the target tracks.
- `--gpu 0,1`: distributes the tracks across the GPUs (one
  engine process per GPU at a time). The engine processes
  reserve RAM in the same budget as `sd` (size of their
  models × 1,15), the language model and the synthesis separately.

## Measured performance

With the 8-step turbo DiT, the 1,7B language model and a
3-minute track, on an RTX 5060 Ti and on an RTX 4070 (12 GB):

| Phase | Time | VRAM peak |
|---|---|---|
| `compose` (`ace-lm`) | ~11 s | ~4,0–4,3 GB |
| `synth` (`ace-synth`) | ~8–9 s | ~8,4–8,5 GB |

A 30 s track with its own lyrics (`synth` only)
takes 3–4 s. Both processes fit on the 12 GB GPU.

## Events

In music mode the `--json` stream contains, per track, the
`phase_start`/`phase_end` pairs of `compose` (only if the track uses the language
model), `synth`, `cover` (only with automatic cover art) and `export`, plus a
global `export` step (`scene_id: "_global"`) with the path of `manifest.json`.
The `scene_id` of each event is the track id. The shape of the events does not change; see `docs/events.md`.

## Importing songs

A song dataset is imported with the same format as the scene one (tuples
`[id, título, lora, prompt]` in JSON, or HTML with `const data = [...]`):

```bash
bin/veritmo import canciones.json --mode music -o scenes.yaml
```

In the panel, the import wizard has an "Video scenes /
Songs" selector, and on creating the
project it opens the music studio. The `prompt` of each entry is read like this:

- The lines before the first marker are the **style** (`prompt`), joined with a space.
- `Lyrics:` starts the **lyrics**, which keep their line breaks
  until the next marker or the end. There can be only one lyrics block.
- An `Instrumental` line (or `Lyrics: [Instrumental]`) marks the track as
  instrumental; if there were lyrics, they are discarded and the report says so.
- `Language:`, `BPM:`, `Duration:` (seconds, accepts `45s`), `Key:`,
  `Time signature:`, `Seed:`, `Variants:` and `Tags:` (comma-separated tags,
  for the HeartMuLa generator) fill in their fields. The markers are case-insensitive.
- `lora_info` and the title are discarded: adapters are chosen in the studio.

An entry with an unreadable number, an id that is not usable as a file
name or without a style is rejected on its own and the rest is imported;
two equal ids abort the import. The validation does not require the music
models to be installed. The summary counts imported, instrumental and rejected.

## Out of scope for this version

- `lego` and `complete` (they need the base model, which is not in the catalogue).
- An `ace-server` that survives the run and is shared between runs: it would save more (the models would stay in memory), but it needs its own lifecycle and memory policy.

### Music services (.NET): start, stop and idle memory

**Since 2026-10-22 HeartMuLa, DiffRhythm2 and LAION-CLAP run as .NET services** (C# ports of the original models, Apache-2.0 licensed; `VERITMO_SERVICES_SRC` points to other checkouts of `heartmula`, `diffrhythm2`, `lion-clap` and `musicgen-core`; `scripts/install.sh` offers the .NET SDK and builds the services). **Environment** starts and stops them with the usual buttons; what is launched is a process on this machine. `bash scripts/get-services.sh` builds them and leaves them in `bin/services/<nombre>` (the .NET SDK is required; `--only lionclap` for one, `--cpu` without CUDA); a missing service comes out with the reason `no_service`. Each process runs in its own session and its pid, port and card are kept in `.orchestrator/services/`: restarting the application server does not stop them and the new one finds them. Stopping them ends the whole process group and returns the VRAM. Starting lasts until the service says "listo" (ready) (it loads its model): meanwhile the state is "arrancando" (starting) and a run is rejected as if it were stopped. HeartMuLa puts the model on the chosen card and the codec on the other one with the most free memory (together they do not fit in 12 GB; with a single 12 GB card it is not yet measured). The DiffRhythm2 weights for the service are the converted ones (`decoder.safetensors`, `mulan_text.safetensors`, `mulan_text_tokenizer.json`), in the same folder as the downloaded checkpoint.

### Music services: start, stop and idle memory

The HeartMuLa (`vt-heartmula`) and DiffRhythm2 (`vt-diffrhythm2`) services can be **up at the same time**: each one listens on its own free port of `127.0.0.1`, so they do not collide. They are started and stopped from **Environment** (or with `POST /api/music/engines/{heartmula|diffrhythm2|clap}/start` and `/stop`, see `docs/http-api.md`); starting waits in the background for the service to say "listo" (ready) and, if it does not, the service is stopped and the panel shows why. They do not stop by themselves when the application server is closed and do not carry `--restart`: after rebooting the machine they are stopped.

In the same panel there is a third row, **LAION-CLAP** (`vt-clap`), which is not a generator: a model that compares audio with text, stopped until you start it. With 5 songs of known style it did not distinguish the genres well (all scores between 0,5 and 0,67); even so it is one of the options for recognising a song (see "Take it from a video or audio"). The service is built with `scripts/get-services.sh --only lionclap` and the weights (about 600 MB) go in `models/music/clap`; it offers `GET /health` and `POST /classify`.

If you want both at once, put HeartMuLa (it needs about 8 GB at full work) on the 16 GB card and DiffRhythm2 (about 5,6 GB) on the 12 GB one.

**Idle memory** (measured on this machine, 12 GB RTX 4070 + 16 GB RTX 5060 Ti, 30 GB of RAM; the VRAM includes about 450 MiB of the desktop on the 4070):

| | Just started | After a song (idle) | During a song | Songs |
|---|---|---|---|---|
| DiffRhythm2 | 0,3 GB of RAM, 0 of own VRAM | 7,5 GB of RAM (the model waits in RAM), VRAM returns to baseline | 5,6 GB of VRAM | 1st 20 s, 2nd 8 s (40 s of music) |
| HeartMuLa | 0,35 GB of RAM, ~150 MiB of VRAM (CUDA context) | 4,5 GB of RAM, ~220 MiB of VRAM | 8 GB of VRAM | 1st 59 s, 2nd 17 s (20 s of music) |

DiffRhythm2 leaves the model **in RAM between songs** and moves it to the GPU only while it makes one; when it finishes it returns it and empties the GPU cache, so two services that are up do not take idle VRAM. HeartMuLa already freed the GPU between songs and rereads the weights from disk, which the system usually has in its RAM cache. It is memory the system can reclaim: with both up, loading HeartMuLa pushed DiffRhythm2's idle model to swap, and its next song took 13 s instead of 8.
