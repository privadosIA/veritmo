# Making 3D models

A 3D model can be **generated** from a picture or from a text and kept in the [library](MODEL_LIBRARY.md), where any scene or piece can use it like a
downloaded one. Two engines, run by an optional service: each as a **.NET service** (`triposr-service` and `stablefast3d-service`, C# ports of the models, no Python) or, as the fallback, both in one container (`docker/model3d`, see its README):

| Engine | Licence | Needs |
|---|---|---|
| `triposr` (default) | MIT | about 1.9 GB of weights, no account |
| `sf3d` (Stable Fast 3D) | Stability AI Community License: free up to US$1M of yearly revenue, with its terms to follow | the Hugging Face key of an account that has accepted its licence |

Nothing here is installed by the program on its own: the service or the container image is built by you, and the weights are fetched when you ask.

## Install and start

Each engine runs by default as its own .NET service, `bin/services/triposr` and `bin/services/stable-fast-3d` (the .NET SDK 8 or newer and the ASP.NET Core runtime are needed, like for the music services):

```bash
bash scripts/get-services.sh --only triposr,stable-fast-3d   # once; --cpu builds them without the CUDA libraries
```

A service reads the same weights folder as the container (`models/model3d`), listens on a free port of loopback, sees the one card it was started on
(`TRIPOSR_DEVICE=cuda:0`, `SF3D_DEVICE=cuda:0`) and loads the model with the first model it makes. Its source is `dotnet/triposr` and `dotnet/stable-fast-3d` (`dotnet/REVISIONS.txt` says which commit);
their contract is `docs/http-service.md` in each. The one difference from the container that shows: Stable Fast 3D as a service also reads `sf3d/160_tets.npz` (15 MB, a file of its repository), which the
container carries inside its image, and its `engine_version` is `sf3d-net@<version>`. **The setting `model3d_backend`** (`dotnet`, the default, or `docker`; `PUT /api/settings`, shown in the
`model3d` block of `GET /api/settings`) says which way the engines run on this machine, the same for both; `GET /api/models3d/engines` carries `backends` for each engine, saying
what each way lacks. Changing it stops what runs on the old one. With `docker` the container is needed:

```bash
docker build -t veritmo/model3d:1 docker/model3d         # about 14 GB: PyTorch
```

Everything can be installed from the server, for the backend in use or for the other one (`{"backend": "docker"}`): `POST /api/models3d/engines/triposr/install`
answers the plan (`backend`, `parts`: `service`, `image` and `weights`, the weight files missing); add `{"confirm": true}` to run it and follow it at
`GET /api/models3d/installs/{id}`. With the .NET backend the weights are downloaded by the server itself (no Docker); with the container the image is built
(`docker build`, TripoSR only: the image with Stable Fast 3D is built by hand) and the weights are fetched inside it. The weights of Stable Fast 3D are the account's to unlock in both ways: the server
sends the saved Hugging Face key only to Hugging Face, for the two files of its licence, and refuses the install with `key_missing`, `key_rejected` or `licence_not_accepted` before downloading anything. Or by hand (see the README of `docker/model3d`). `GET /api/models3d/engines` says, for each engine, whether it can be
used now and why not: `no_service` (the .NET service is not published), `no_docker`, `no_image`, `no_gpu_runtime`, `no_weights`, `no_gpu`, `not_running` (everything is there, the service or container is stopped:
`POST .../engines/{engine}/start`), `unreachable`, `not_built` and, for `sf3d`, `key_missing`, `key_rejected` and `licence_not_accepted`. With Compose, the
profiles `model3d` and `model3d-weights` (`docs/COMPOSE.md`); a service elsewhere is named by `MODEL3D_HOST`, and then the container and the weights are
managed there (`start`, `stop` and `install` answer `409`).

## Make a model

```bash
veritmo models3d generate --prompt "a retro roller skate, glossy pink" --name patin
veritmo models3d generate --image lampara.png --detail high --name lampara
veritmo models3d generate --prompt "a golden trophy" --engine sf3d
```

It prints the id of the model (`generated/patin`) and, on stderr, the triangles, the detail it was made at and the time. With a text, the picture is made first
by this machine's engines (`bin/sd` and the best image model installed that fits the card; the text is completed to show one object centred on a plain
white background, with a negative prompt against text, shadows and clutter; the same text gives the same picture); a machine with no image model refuses a
text and takes a picture. The container is started for the call and stopped afterwards, unless it was already running. The flags: `--prompt` or `--image`
(not both), `--engine triposr|sf3d`, `--detail low|normal|high`, `--name`.

What comes out: a `.glb` standing upright (Y up) with its origin at the middle of its base (so it stands on the floor of a scene), with its colours (per vertex,
no texture, no animation) and no more triangles than a scene takes (500,000: a model over that is made again at the next lower detail). `detail` is the
resolution of the surface: `low`, `normal` and `high` made 21,000 to 45,000, 49,000 to 105,000 and 88,000 to 187,000 triangles on five objects. Size it in a
scene with `fit: "largest"` (see `SCENES_YAML.md`): a generated model has no real-world size.

Where it goes: the pack **`generated`** of the library (`source: "generated"`), created with the first model and removable like any pack (its models go with it;
an import over that id is `409`). Each model keeps the `prompt` it was made from (or the name of the picture), the `engine`, its `engine_version`, its
`licence` (the engine's) and `generated_at`, shown in `GET /api/models3d/models`; its thumbnail, search and copy into a project or a piece (`POST
/api/projects/{id}/sources/model`) work like any other model's. `attribution` is false for `triposr` and true for `sf3d`, whose licence asks that its terms are followed.

## As a project: the mode `models3d`

3D models are a category of media like pictures, songs and videos: a project of **`output.mode: models3d`** has a top-level `models:` list in its `scenes.yaml`
(see `SCENES_YAML.md`) and the usual runs, history, statistics, storage, export and hidden-project API around it.

```yaml
project: objetos
output: {mode: models3d}
defaults:
  # the image model the pictures of the prompts are drawn with, as in the images mode
  model_set: {diffusion_model: models/Krea-2-Turbo-Q4_K_M.gguf, llm: models/Qwen3VL-4B-Instruct-Q4_K_M.gguf, vae: models/wan_2.1_vae.safetensors, cfg_scale: 1}
  steps: 8
  models3d: {engine: triposr, detail: normal}     # the engine and detail of the entries that do not say
models:
  - {id: patin, prompt: "a retro roller skate, glossy pink"}
  - {id: copa, prompt: "a golden trophy", variants: 3, seed: 7}   # copa.glb, copa-2.glb, copa-3.glb, seeds 7, 8, 9
  - {id: gato, image: gato.png, detail: high, adult: false}       # its own picture, from sources/
```

A run goes through the entries one after the other (the service holds its card, so nothing is gained in parallel): for each, the **picture** (the project's image
engine with the completed prompt, as the images mode draws a scene; an entry with its own `image` has none) and then the **model** (the service), with a phase
`render` and a phase `model3d` for each entry in the events (`skipped` when they were not needed). It leaves `out/<project>/<id>.glb` (`<id>-<n>.glb` for the
variants after the first), `out/<project>/manifest.json` (per model: entry, file, prompt or picture, engine and its version, detail made, seed, triangles, seconds,
`adult` and the `key` below) and a copy in the pack `generated` of the library with the `project` and the `entry` recorded.

**What is made again.** Each model has a key: the hash of its picture (the image settings and the prompt, or the file of the entry's own picture), the engine and the
version the service reports for it (`GET /engines` of the container, before anything is made), the detail and the seed. A run that finds the same key in the manifest and the
file in `out/` makes nothing: change one prompt and only that entry is made again (the picture too); change the detail and only the model is; a new engine build makes
them all. A model made again replaces its file, its manifest line and its entry in the library, so the library never holds two versions of one entry. `--force` makes
everything again. A seed the entry does not set comes from its prompt, so the same prompt gives the same picture on every run.

**Needs the service up.** Before anything is made the run asks the service for each engine its entries use, and refuses with the engine's `reason` (`not_running`, `no_weights`...)
if it cannot be used; a run started by the server never starts the container (start it from `POST /api/models3d/engines/{engine}/start` or compose), the command line
(`veritmo scenes.yaml`) does. A run that is cancelled or fails keeps the models it already made, in `out/`, the manifest and the library.

**Adult content.** An entry with `adult: true`, and every entry of a project marked `adult: true`, makes models marked `adult` in the manifest and the library. A server that does
not show adult content (the default) leaves them out of the catalog and answers `404` to their detail and thumbnail; changing the mark of an entry changes the mark without
making the model again.

The **package** of a project (`GET /api/projects/{id}/export?format=zip`, and the import) carries its `scenes.yaml` with the `models:` list and the pictures of its entries (`sources/`),
and its recipe names the 3D engines it uses (`TripoSR · MIT`, `Stable Fast 3D`, whose weights need an account that accepted its licence) and the weights each one reads
(`models/model3d/…`, fetched with `POST /api/models3d/engines/{engine}/install`). The models themselves are outputs, as the songs and pictures of the other modes are: a project makes them again.

## One call: `POST /api/models3d/generations`

For a client that wants a model and no project: `{prompt, engine?, detail?, name?, client?, adult?}` as JSON, or `multipart/form-data` with an `image` part and the same fields as text.

```bash
curl -s localhost:4666/api/models3d/generations -H 'Content-Type: application/json' \
  -d '{"prompt": "a golden trophy cup", "client": "studio"}'
# 202 {"id": "api-m3d-20261124-101500-a1b2c3", "state": "queued", "step": "", "engine": "triposr", "detail": "normal", "name": "a-golden-trophy-cup", ...}
curl -s localhost:4666/api/models3d/generations/api-m3d-20261124-101500-a1b2c3
# {"state": "running", "step": "picture"} ... {"state": "done", "model": "generated/a-golden-trophy-cup", ...}
```

The server makes a **hidden project** (`api-m3d-…`, kind `model_generation`) with one entry and runs it on the usual queue, so it waits its turn behind other heavy processes like any run. For a prompt
it draws the picture with the best image model installed that fits the card (the same as the command); for a picture it needs no image model. `name` is a plain lower-case name (made
from the prompt when absent); the model keeps it in the library (a number is added when another project took it). Refused before anything is queued: `400` naming the field (a prompt and a
picture together or neither, a prompt over 1000 characters, an unknown engine or detail, a name or a client that is not valid), `413` for a picture over 20 MB, `422` `content_not_allowed` for
`adult: true` on a server that does not keep adult content, and `503` with the engine's `reason` when it cannot be used now or `no_image_model` for a prompt on a machine with none.

`GET /api/models3d/generations/{id}` answers `{id, client, state (queued, running, done, failed, cancelled), step (picture or model, while it runs), prompt, engine, detail, name, model
(the <pack>/<name> in the library, when done), error, created_at, finished_at, run}`; `GET /api/models3d/generations` lists them newest first (`limit`, `offset`, `sort`, `client`, with
`X-Total-Count` and `Link` when paged); `POST .../{id}/cancel` stops a queued or running one (`409` otherwise); `DELETE .../{id}` removes the generation and its project and **not** its model.
They carry the `client` label, appear in `GET /api/projects?managed=include` with `kind: "model_generation"`, and their runs are in `GET /api/runs` and `GET /api/stats` with the mode `models3d`.

## The mode in the rest of the program

- `GET /api/runs` and `GET /api/stats` take `mode=models3d` (alone or in the list); a mode that is none is `400` and the message lists them (`video, images, music, models3d or animation`).
- The history says a run's result is available while a `.glb` is in its `out/<project>/`; the storage of a project counts its models with its output (`final`).
- The projects listing shows the mode `models3d`; the metrics label the runs of the generations `kind="models3d"` (the ordinary projects keep `project`).
- The catalog (`GET /api/models3d/models`) filters by `source` (`catalog`, `user`, `generated`), `engine` and `project`, and `q` also searches the prompt; a project's own models are `?project=<id>`.

### For a dashboard of this category

Everything a page of 3D models needs is in the API, with no other server work: the engines and their start (`/api/models3d/engines`), the generations (`/api/models3d/generations`), the catalog and its
thumbnails (`/api/models3d/models`, `.../thumbnail`), the packs (`/api/models3d/packs`), the projects of the mode (`GET /api/projects`, their runs and `GET /api/projects/{id}/artifacts/out/<project>/<id>.glb`),
and adding a model to a project or a piece (`POST /api/projects/{id}/sources/model`, `POST /api/music-motion/pieces/{id}/models`). The datasets, the assistant and the prompt presets of
the category come with `add-models3d-datasets`.

## Limits

- A model that is the picture's front only: the back is invented. Thin parts (a lamp's cord, a cat's whiskers) are lost; transparent and mirror-like objects fail.
- Colour per vertex, no texture and no rig: a model made here is a prop, not a character that moves.
- One model at a time per service or container, and the engine stays on the card (TripoSR: 3.4 GB in the container; the .NET service, measured on an RTX 4070 of 12 GB with the published `bin/services/triposr`, holds 4.4 GB after a `low` model, 4.8 GB after `normal` and 5.8 GB after `high`, and keeps it until it is stopped, because the weights and the blocks cached by the allocator stay on the card) while it runs.
- The cut-out is `u2net` (176 MB): it gave the same shapes as `bria-rmbg-2.0` (the 1 GB model of the first test) in five objects, 50 times faster (0.1 s against 5 to 9 s), and
  its one visible difference was to drop the white base under a trophy that stood on a white plinth. `bria` is under a non-commercial licence, so it is not offered.

## Licences and what each person does for Stable Fast 3D

TripoSR and its weights are MIT. Stable Fast 3D is under the Stability AI Community License: free for individuals and organisations under US$1M of yearly
revenue, with attribution and terms to follow, and above that a commercial licence is needed; read it at <https://stability.ai/license>. Its weights are on a
repository that asks every account to accept that licence, so **each person manages their own**: accept it on <https://huggingface.co/stabilityai/stable-fast-3d>
with your account, save that account's key (`PUT /api/tokens/huggingface`, or `HF_TOKEN`), build the image with `SF3D=1` and fetch the weights. The server
only checks, with a `HEAD` of one file of the repository and the saved key, and says what is missing; it never accepts a licence, creates an account or asks for a key.
Stable Fast 3D was built and run here once (2026-11-24, an account that had accepted the licence, an RTX 5060 Ti): the check answered `200`, the weights were fetched and
five objects were made (below). The answer for a key whose account has **not** accepted the licence (`403` → `licence_not_accepted`) is still checked against a fake service
only, and a key Hugging Face does not know (`401` → `key_rejected`) against the real one.

### Stable Fast 3D, measured

Same five pictures as TripoSR, with the engine already loaded: 2 s at `low` and `normal`, 2 to 3 s at `high`; the first model after the container starts takes about
24 s (it loads). The container holds 8 GB of the card while it lives (TripoSR: 3.4 GB). Unlike TripoSR, `detail` does not change the mesh: it is the size of
the baked texture (512, 1024 and 2048 px), and the triangles are the same at the three (13,000 to 26,000 in the five objects). Its models carry real materials (a metallic
trophy, the wood of a chest) where TripoSR's carry colours per vertex, and look thinner; both are rough, as a one-picture model is.
