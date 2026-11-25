# 3D model generator (optional)

Makes a 3D model (a binary glTF, `.glb`) from **a picture**. The orchestrator makes the picture from a text first when it is asked a text
(`orchestrator models3d generate --prompt "..."`), so the whole path is text → picture → model. It is Python and PyTorch, so it lives here, in a
container, and the veritmo talks to it over HTTP, as it does with HeartMuLa. It is optional. Both engines also have a C# port that runs without Python
(`dotnet/triposr` and `dotnet/stable-fast-3d`, `bash scripts/get-services.sh --only triposr,stable-fast-3d`), the default way to run them; this container is then the fallback
(`model3d_backend` = `docker`).

| Engine | Licence | In the image | Needs |
|---|---|---|---|
| `triposr` ([TripoSR](https://github.com/VAST-AI-Research/TripoSR)) | MIT | always | nothing: about 1.9 GB of weights, no account |
| `sf3d` ([Stable Fast 3D](https://github.com/Stability-AI/stable-fast-3d)) | Stability AI Community License (free up to US$1M of yearly revenue; read its terms) | only when built with `SF3D=1` | a Hugging Face account that has accepted the licence of <https://huggingface.co/stabilityai/stable-fast-3d> |

**Stable Fast 3D is supported but not provided**: its code is installed and its weights fetched only if you ask, with your own account and licence. It
was built and run once here (2026-11-24, with an account that had accepted the licence, on an RTX 5060 Ti). The program never accepts a licence, creates an account or asks for a key on your behalf; it only says what is missing
(`key_missing`, `key_rejected`, `licence_not_accepted` in `GET /api/models3d/engines`). The key is the Hugging Face one that the server already keeps
(`PUT /api/tokens/huggingface`).

## What is here

- `Dockerfile`: Python 3.11, PyTorch (CUDA 12.8 wheels, they run on an RTX 4070 and on Blackwell cards), `transformers` below 5 (TripoSR's DINO model is
  written for its layer names), `rembg` with `onnxruntime`, `trimesh`, TripoSR at a pinned commit. The weights are **not** in the image.
- `triposr-local.patch`: the two changes made to TripoSR: scikit-image's marching cubes instead of the compiled `torchmcubes` (in the axis order the caller
  expects), and the config of its DINO model read from the weights folder instead of Hugging Face (the service never reaches the network).
- `install-sf3d.sh`: what `SF3D=1` adds: its code at a pinned commit, its requirements (without the pins of Python 3.10 that clash with the rest) and its two
  CUDA extensions, built for the cards of `TORCH_CUDA_ARCH_LIST`.
- `sf3d-local.patch`: the two things Stable Fast 3D downloads when it loads, which the offline service cannot: its DINOv2 model (built from the small config
  in the weights folder) and its CLIP model (built without weights; the checkpoint holds both). Without it the first model fails with a Hugging Face error.
- `base-cuda.Dockerfile`: the base image with `nvcc` that `SF3D=1` needs.
- `server.py`: the HTTP service (standard library for the HTTP part; the engines import torch when the first model is made).
- `fetch.py`: downloads the weights into `/ckpt`, checks their hashes, prints one line per step.
- `test_server.py`: tests of the service without a GPU: `python3 -m unittest docker/model3d/test_server.py`.

## Build and fetch

    docker build -t veritmo/model3d:1 docker/model3d                          # TripoSR only (about 14 GB with torch)
    docker build -f docker/model3d/base-cuda.Dockerfile -t vt-model3d-base:cuda128 docker/model3d                      # for Stable Fast 3D: a base with nvcc
    docker build -t veritmo/model3d:1 --build-arg BASE=vt-model3d-base:cuda128 --build-arg SF3D=1 --build-arg ENGINES=triposr,sf3d docker/model3d   # (about 29 GB)
    docker run --rm --user $(id -u):$(id -g) -e HOME=/tmp -v "$PWD/models/model3d:/ckpt" veritmo/model3d:1 python /opt/fetch.py            # TripoSR
    docker run --rm --user $(id -u):$(id -g) -e HOME=/tmp -e HF_TOKEN -v "$PWD/models/model3d:/ckpt" veritmo/model3d:1 python /opt/fetch.py sf3d

The server can do the fetch for you: `POST /api/models3d/engines/triposr/install` answers the plan, and the same with `{"confirm": true}` runs it as a job.

## Run

The server starts and stops the container from `POST /api/models3d/engines/{engine}/start|stop` (the two engines are the one container), and
`orchestrator models3d generate` starts it for the call and stops it afterwards unless it was already running. By hand:

    docker run --rm --gpus device=1 -p 127.0.0.1:8790:8790 -v "$PWD/models/model3d:/ckpt:ro" veritmo/model3d:1

Needs Docker with the NVIDIA container toolkit. With Compose: the `model3d` and `model3d-weights` profiles (`docs/COMPOSE.md`). On another machine, run
the container there and give the server its address in `MODEL3D_HOST` (and `MODEL3D_TOKEN` or `MODEL3D_TOKEN_FILE`): the pictures are sent to it, and
setting the address is that decision.

## The API

| Call | |
|---|---|
| `GET /health` | `{ready, version, busy, queued, gpu: {name, free_mb, total_mb}, missing, engines}` |
| `GET /engines` | `{engines: {triposr: {built, missing, ready}, sf3d: {...}}}` |
| `POST /generate?engine=triposr&detail=normal` | the picture (png, jpg or webp, up to 20 MB) as the body → `202 {id}` |
| `GET /jobs/{id}` | `{id, state: queued|running|done|failed, error, seconds, engine, engine_version, detail, detail_used, resolution, triangles}` |
| `GET /jobs/{id}/model` | the `.glb` (`409` until done) |
| `DELETE /jobs/{id}` | forgets the job and deletes its model |

`detail` is the resolution of the surface: `low` 128, `normal` 192, `high` 256 points a side. A model with more triangles than a scene takes (500,000)
is made again at the next lower detail. What every model leaves with, whatever the engine gave: upright in glTF's axes (Y up), the origin at the middle
of its base (it stands on the floor of a scene), surface, no more triangles than allowed, and its colours (per vertex). With `MODEL3D_TOKEN` set every
call needs `Authorization: Bearer <token>`. A picture that already has an alpha channel is not cut out again.

## Measured (RTX 5060 Ti, 16 GB)

TripoSR: the model in 0.3 to 0.4 s and about 2.5 GB of VRAM (the service keeps the engine loaded: 3.4 GB in use). Surface at `low`, `normal`, `high`: 21,000
to 45,000, 49,000 to 105,000 and 88,000 to 187,000 triangles on five objects; about 1.2 s per model at `low` and `normal` and 1.7 s at `high` with the
service warm. The cut-out is `u2net` (176 MB, 0.1 s on the CPU) because it gave the same shapes as the 1 GB model (5 to 9 s); see `docs/MODEL_GENERATION.md`.
