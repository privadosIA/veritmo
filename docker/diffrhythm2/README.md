# DiffRhythm2 song service

[DiffRhythm2](https://github.com/ASLP-lab/DiffRhythm2) (ASLP-lab, Apache 2.0, 1 billion parameters; note that it downloads the MuQ-MuLan and MuQ weights, which are CC-BY-NC 4.0, non-commercial) makes a full
song from lyrics with structure tags and a style text. This folder packages it as a small HTTP service in a container (the Docker, Python alternative to the .NET service of `scripts/get-services.sh`): you run it on this machine or on another one and name its address in **Settings** (`diffrhythm2_host`).

## What it does and does not do

- **English (and Chinese) lyrics only.** The lyrics go through a phoneme converter for those two languages: Spanish is
  not read, it comes out as badly pronounced English.
- **No instrumentals** (the authors list them as pending), no covers, remixes or repaints, no extending a song.
- The style is a short text of English words (`Pop, Piano, Bass, Drums, Warm`). The model also accepts a reference
  audio of 10 seconds; this service leaves it out.
- The song is stereo made from a mono signal (the right channel is the left one, quieter and 10 ms late), at 48 kHz.
- The model stops by itself at the end of the lyrics: `max_secs` (10 to 210, default 210) is the longest it may take.
- Measured on an RTX 5060 Ti: a 90 s song in 20 s and a 150 s one in 32 s, with a peak of 7.8 GB of video memory.

## API

JSON; an optional bearer token (`DIFFRHYTHM2_TOKEN`) protects every call.

| Call | Meaning |
|---|---|
| `GET /health` | `{ready, version, busy, queued, gpu: {name, free_mb, total_mb}, missing}` |
| `POST /generate` | `{lyrics, style, max_secs?, seed?, cfg_strength?, steps?}` → `202 {id}`; `400` names the field that is wrong |
| `GET /jobs/{id}` | `{id, state: queued\|running\|done\|failed, progress, error, seconds}` |
| `GET /jobs/{id}/audio` | the song as a 48 kHz stereo WAV (`409` until the job is done) |
| `DELETE /jobs/{id}` | forgets the job and deletes its audio |

The lyrics may carry `[start] [intro] [verse] [chorus] [bridge] [outro] [inst] [solo] [hook] [break] [end]` on lines of
their own; the service writes them in lower case, drops any other tag and adds `[start]` when it is missing. The same
seed gives the same song.

## With the script

```bash
bash scripts/music-containers.sh build diffrhythm2
bash scripts/music-containers.sh weights diffrhythm2   # the one step that uses the network (~9.5 GB)
bash scripts/music-containers.sh up diffrhythm2 1      # the card number; prints what to set in Settings
bash scripts/music-containers.sh down diffrhythm2
```

## Build, weights and run by hand

```bash
docker build -t veritmo/diffrhythm2:1 docker/diffrhythm2
# the only step that uses the network (about 9.5 GB)
docker run --rm --user $(id -u):$(id -g) -e HOME=/tmp -v "$PWD/models/music/diffrhythm2:/ckpt" veritmo/diffrhythm2:1 python /opt/fetch.py
# the service never downloads: the weights are mounted read-only
docker run --rm --gpus device=1 -p 127.0.0.1:8788:8788 -v "$PWD/models/music/diffrhythm2:/ckpt:ro" veritmo/diffrhythm2:1
```

Tests of the API without a GPU: `python3 -m unittest discover -s docker/diffrhythm2 -p test_server.py`.

Between songs the service keeps its models in RAM (about 7.5 GB after the first song) and moves them to the GPU only while it makes one (about 5.6 GB of VRAM), emptying the GPU cache when the song ends, so an idle container holds no video memory.
