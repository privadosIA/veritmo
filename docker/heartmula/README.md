# HeartMuLa engine (optional)

[HeartMuLa](https://github.com/HeartMuLa/heartlib) writes complete songs (voice and accompaniment) from lyrics and a few
style tags, with far better results than ACE-Step, at about 1.1 times real time (a 2-minute song takes about 2 minutes) and
about 10 GB of GPU memory. It is Python and PyTorch, so it lives here, in a container, and the veritmo talks to it over
HTTP. It is optional, and it is the Docker (Python) alternative to the .NET service that `scripts/get-services.sh` builds: the music project offers HeartMuLa when a local service or an external one (this container is one: the server reaches it by its address) is available.

## What is here

- `Dockerfile`: Python 3.11, PyTorch (CUDA 12.8 wheels), the `heartlib` library fixed to a commit, ffmpeg.
- `heartlib-memory-and-save.patch`: the two changes made to the library:
  1. the attention cache of the language model is sized to the song (it is allocated for 8192 tokens whatever the song,
     about 2 GB with the guidance batch), which is what lets a song fit a 12 GB card;
  2. the audio is written with soundfile instead of torchaudio (which needs torchcodec, not loadable against every ffmpeg).
- `server.py`: the HTTP service (standard library only).
- `test_server.py`: tests of the service without a GPU: `python3 -m unittest docker/heartmula/test_server.py`.

The weights (about 21 GB) are **not** in the image. Download them into `models/music/heartmula/` (the Models screen, or by hand, see below); the container mounts that folder read-only at `/ckpt`.

## With the script

    bash scripts/music-containers.sh build heartmula
    bash scripts/music-containers.sh up heartmula 1      # the card number
    bash scripts/music-containers.sh status
    bash scripts/music-containers.sh down heartmula

`up` prints the one thing to tell the server (`heartmula_host` in Settings, `http://127.0.0.1:8787`). It is an address on this machine, so nothing needs accepting.

## Build and run by hand

    docker build -t veritmo/heartmula:1 docker/heartmula
    docker run --rm --gpus device=1 -p 127.0.0.1:8787:8787 \
        -v "$PWD/models/music/heartmula:/ckpt:ro" veritmo/heartmula:1

Needs Docker with the NVIDIA container toolkit (`docker run --gpus` must work). The weights, by hand:

    hf download --local-dir models/music/heartmula HeartMuLa/HeartMuLaGen
    hf download --local-dir models/music/heartmula/HeartMuLa-oss-3B HeartMuLa/HeartMuLa-oss-3B-happy-new-year
    hf download --local-dir models/music/heartmula/HeartCodec-oss HeartMuLa/HeartCodec-oss-20260123

## Another machine

Run the container there the same way and put its address (and the token, if you set `HEARTMULA_TOKEN`) in the Settings of
the orchestrator. Anything that is not this machine is "external": the lyrics and tags are sent to it, so the orchestrator
asks you to accept that for that address.

## The API

| Call | |
|---|---|
| `GET /health` | `{ready, version, busy, queued, gpu: {name, free_mb, total_mb}, missing}` |
| `POST /generate` | `{lyrics, tags, max_ms, seed, temperature, topk, cfg_scale}` → `202 {id}` |
| `GET /jobs/{id}` | `{id, state: queued|running|done|failed, progress: {done, total}, error, seconds}` |
| `GET /jobs/{id}/audio` | the song as a 48 kHz stereo WAV (`409` until done) |
| `DELETE /jobs/{id}` | forgets the job and deletes its audio |

`lyrics` uses `[Verse]`/`[Chorus]` markers; `tags` are short words (`piano,happy`). `max_ms` is the longest the song may be
(the model stops by itself at the end of the lyrics). The model has no seed option: the service sets the random seed before
each song. With `HEARTMULA_TOKEN` set every call needs `Authorization: Bearer <token>`.

Memory measured with a 2-minute song: about 8.4 GB for the language model and 6.9 GB for the decoder, one stage at a time,
freed after each song.
