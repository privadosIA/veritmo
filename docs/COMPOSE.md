# The Docker Compose stack

[← Back to the README](../README.md)

`compose.yaml` starts the server and everything it works with from **one file**: its MySQL database, the migration of the schema, Prometheus and Grafana, and, as profiles of the same file, the two music generators, Ollama, Loki with the shipper of the logs and Redis. Every piece starts already pointing at the others: nothing has to be typed in a screen.

It needs Docker with Compose v2 and the NVIDIA Container Toolkit (the server, Ollama and the generators ask for the graphics card). It does not need Go, CMake, CUDA or Node on the host.

## What the stack leaves out

- **A container for the .NET services** (the recognition of songs and the vision of the video-edit tools). The server runs them itself, as programs of the installation folder (`bin/services`): where that folder has none, the tasks that need them are reported unavailable by the server, with their reason, and everything else works. The installer step of the public edition does not build them.
- **The dashboard** (`dashboards-lab`) is a separate project and is not in the file. It runs outside and connects to the server: see [the dashboard](#the-dashboard-connects-from-outside).

## Start

```bash
bash scripts/compose-init.sh                              # checks Docker and the NVIDIA runtime; makes .env (generated secrets) and the folders
docker compose --profile install run --rm installer      # installs the programs into the host folders (once; the first time it builds sd with CUDA)
docker compose up -d                                      # the server, MySQL, the migration, Prometheus and Grafana
docker compose ps                                         # every service healthy
```

**Which images are downloaded and which are built.** The server and the installation step are published with each release
(`ghcr.io/privadosia/veritmo-server` and `veritmo-tools`, see [IMAGES.md](IMAGES.md)): `docker compose up` and the `installer` pull them, and
build them from this checkout only when they cannot be pulled (a version that is not published). `VERSION` in `.env` pins the release
(`compose-init.sh` writes it when the checkout is exactly one; empty is the latest). The generators (`heartmula`, `diffrhythm2`), the
3D-model service (`model3d`) and the compression service are never published by this project: they carry large stacks or
software with terms of their own, so `docker compose build <service>` makes them on your machine the first time their profile is used.

`docker compose up -d` brings up, in this order: `mysql`, `mysql-users` and `migrate` (one-shot steps that end), then `server`, then `prometheus` and `grafana`. The server starts only after the migration ended well, and **never migrates by itself**: a new version that brings migrations has them applied by `migrate` when the stack is started with the new image. If the migration fails, the server is not started and `docker compose logs migrate` says why.

| What | Where |
|---|---|
| The server's API | `http://127.0.0.1:4666` (`BIND` and `SERVER_PORT` in `.env`) |
| Grafana | `http://127.0.0.1:3000` (`BIND` and `GRAFANA_PORT`), user `admin`, password `GRAFANA_ADMIN_PASSWORD` |

Nothing else is published: MySQL, Prometheus and the rest are reachable only on the private network of the stack.

## Profiles

An optional piece is a profile of the same file. Start one with `--profile name`, or write the names in `COMPOSE_PROFILES` in `.env` (comma separated) to have them on every `up`.

| Profile | What it starts |
|---|---|
| `music` | the two song generators, `heartmula` and `diffrhythm2` (Python services of `docker/`) |
| `music-weights` | the one-shot downloads of their weights (see below); not services that stay up |
| `model3d` | the 3D model generator (TripoSR, optionally Stable Fast 3D), a Python service of `docker/model3d` |
| `model3d-weights` | the one-shot download of its weights; not a service that stays up |
| `local-llm` | Ollama, the language model of the assistant |
| `logs` | Loki and Alloy: the logs of every container, searchable in Grafana |
| `redis` | a Redis for the test of the Monitoring card |
| `install` | the `installer` step (see below); not a service that stays up |

Each piece of a profile is already known to the server by its address (the settings of the server say `source: env` for them): there is nothing to type in Settings.

### `music`: the song generators

```bash
docker compose --profile music-weights run --rm heartmula-weights     # about 21 GB, once
docker compose --profile music-weights run --rm diffrhythm2-weights   # about 9.5 GB, once
docker compose --profile music up -d
```

The weights go to `MODELS_PATH/music/heartmula` and `MODELS_PATH/music/diffrhythm2` (folders of this machine) and are mounted read-only into the services. Each generator takes the card `HEARTMULA_GPU` / `DIFFRHYTHM2_GPU` (0 by default): with two cards, give each its own. They share one token (`MUSIC_TOKEN`) with the server, passed as a file. The songs are made through the server like any other (`GET /api/music/engines` lists both as available once their weights are there).

A music run still asks for the programs of the default music engine (`ace-synth`), which the installation step builds by default (`acestep`); its models come from the Models screen.

### `model3d`: the 3D model generator

```bash
docker compose --profile model3d-weights run --rm model3d-weights    # about 1.9 GB, once (TripoSR: no account)
docker compose --profile model3d up -d
```

The weights go to `MODELS_PATH/model3d` and are mounted read-only; the service takes the card `MODEL3D_GPU` (0 by default) and shares the music token. The server knows it by `MODEL3D_HOST` (`GET /api/models3d/engines` lists the engines): it uses the service as it is and does not start, stop or install it (those routes answer `409` and say where it is managed). Stable Fast 3D is not in the image unless it is built with `SF3D=1` and needs the Hugging Face key of an account that has accepted its licence: `docker compose --profile model3d-weights run --rm -e HF_TOKEN model3d-weights sf3d`. See `docs/MODEL_GENERATION.md`.

### `local-llm`: Ollama

```bash
docker compose --profile local-llm up -d
docker compose --profile local-llm exec ollama ollama pull qwen3:8b
```

The models are in `OLLAMA_PATH` (a folder of this machine). The server points at it with `OLLAMA_HOST` (`http://ollama:11434` by default). To use an Ollama that runs somewhere else, write its address in `OLLAMA_HOST` in `.env` and leave this profile off; the text and the pictures of the requests then travel to that machine, as with any external Ollama.

### `logs`: Loki and Alloy

```bash
docker compose --profile logs up -d
```

Alloy reads the logs of every container of the Docker host and sends them to Loki, which keeps them 30 days. In Grafana, *Explore* with the Loki data source and `{service="server"}` shows the server's. Grafana gets the Loki data source when it starts with Loki on the network: if you start this profile later, run `docker compose up -d --force-recreate grafana`. **Alloy mounts the Docker socket read-only; that does not make the Docker API read-only**: whatever has the socket can read the logs and the environment of every container. Leave this profile off if you do not want that.


### `redis`

A Redis with a password (`REDIS_PASSWORD`), without persistence. The server does not use it for anything: it is only there so the test of Redis of the Monitoring card answers. The Monitoring card already has the addresses of Grafana, Prometheus, Loki and Redis from the environment.

## The files are on this machine

Everything that is a file is a **folder of this machine**, given in `.env` and mounted into the containers; no image holds anything the person installs or creates. Only the database and the data of Prometheus and Loki are Docker volumes (`docker volume ls`).

| `.env` | What it holds | Mounted at |
|---|---|---|
| `PROJECTS_PATH` | the projects | `/app/projects` |
| `MEDIA_PATH` | the media directory; set `MEDIA_DIR=/media` to keep whole projects there | `/media` |
| `MODELS_PATH` | the models | `/app/models` |
| `VOICES_PATH` | the voices | `/app/voices` |
| `HYPERFRAMES_PATH` | the HyperFrames blocks | `/app/hyperframes` |
| `INSTALL_PATH` | `bin/` (the programs) and `third_party/` | `/app/bin`, `/app/third_party` |
| `CONFIG_PATH` | the keys and the library (characters, backgrounds, references, motions) | `/config/veritmo` |

Each can be on its own disk (the defaults are folders under `./compose-data`, which git ignores). Every service that writes to them runs as `UID:GID` of `.env`, so nothing in them is owned by root. Stopping the stack (`docker compose down`) loses nothing; `down -v` also deletes the volumes, that is the database.

## The installation step

```bash
docker compose --profile install run --rm installer check   # what is installed and what is missing; changes nothing (exit 1 when something is missing)
docker compose --profile install run --rm installer         # installs what is missing
```

It runs the pinned scripts of the repository (the ones `scripts/install.sh` uses) in an image with the build chain, and writes what they build into the host folders above, so the programs are built against the libraries of the image that runs them. What is installed is not built again. `INSTALL_COMPONENTS` in `.env` chooses the components: `sd piper ytdlp gsap hflibs hfruntime acestep chrome` by default; `whisper demucs upscalers hfblocks models` are optional. The server starts with whatever is installed and reports each task whose element is missing as unavailable, with the reason.

**Chrome is not in the image.** The motion-graphic scenes and overlays draw their pages with Chrome, which is Google software that may not be redistributed (it carries a proprietary decryption module), so no image of this project holds it. The `chrome` component downloads Chrome for Testing from Google's own server into `bin/` of the host (`scripts/get-chrome.sh`, checked against a pinned SHA-256) and the server finds `bin/chromium` on its `PATH`. The image only carries the system libraries Chrome needs. Without the component, motion-graphic scenes are reported as unavailable and everything else works.

## Secrets and the token

`compose-init.sh` generates every secret into `.env` (mode 600). `.env.example` has them empty on purpose: `docker compose up` stops, naming the variable, when one is empty. The secrets reach the services as files under `/run/secrets`, not as environment variables. To rotate one, change it in `.env` and run `docker compose up -d --force-recreate`.

A variable exported in your shell wins over `.env`: if a change in `.env` seems ignored, check `env | grep PASSWORD`.

## The dashboard connects from outside

The server is published on `BIND:SERVER_PORT`. Give the dashboard that address and the token of the server, which is `VERITMO_TOKEN` in `.env`:

```bash
grep ^VERITMO_TOKEN= .env
```

and write the origin the dashboard is served from in `DASHBOARD_ORIGIN`: it becomes an allowed browser origin when the server starts, with no command to run in the container. With it empty no origin is allowed besides the server's own. With `BIND=127.0.0.1` only a dashboard on this machine can connect; for another machine set `BIND` to an address of this one (the server asks for its token on every call anyway).

## The stack without the server

`compose.services.yaml` is the same stack **minus the server**: MySQL, Prometheus and Grafana and the profiles (`music`, `local-llm`, `logs`, `redis`) as Compose services, with the server running on this machine (`bin/veritmo`, started with `scripts/serve.sh`). It leaves out what only exists to run the server inside an image: the server, the migration and the installer (the programs are installed with `scripts/install.sh`). Use one file or the other, not both at once: they have the same project name, the same volumes and the same subnet. `compose.yaml` is kept as it is.

```bash
docker compose -f compose.services.yaml up -d            # add --profile music, local-llm, logs, redis as in the other file
VERITMO_DB_PASSWORD="$(grep ^VERITMO_DB_PASSWORD= .env | cut -d= -f2-)" bin/veritmo data migrate \
  --driver mysql --host 127.0.0.1 --database veritmo --user mo   # the schema: the server never migrates
scripts/serve.sh                                         # the server, reachable by Prometheus (0.0.0.0:4666)
```

Every piece is published on `BIND` (`127.0.0.1` by default) so the server finds it: MySQL `3306`, HeartMuLa `8787`, DiffRhythm2 `8788`, Ollama `11435` (not `11434`, which an Ollama installed on this machine usually has), Redis `6379`, Loki `3100`, Grafana `3000` and Prometheus inside the stack. The ports are variables of `.env` (`MYSQL_PORT`, `HEARTMULA_PORT`, `DIFFRHYTHM2_PORT`, `MODEL3D_PORT`, `OLLAMA_PORT`, `REDIS_PORT`, `LOKI_PORT`). In the Settings of the server, or by environment, point it at them (`VERITMO_DB_*` with the host `127.0.0.1`, `HEARTMULA_HOST=http://127.0.0.1:8787`, `DIFFRHYTHM2_HOST=http://127.0.0.1:8788`, `OLLAMA_HOST=http://127.0.0.1:11435`, with `MUSIC_TOKEN` as the token of the generators).

Prometheus reaches the server through `host.docker.internal` (its file is `deploy/compose/prometheus.services.yml`), so the server has to listen on an address the containers can reach: `scripts/serve.sh` does, `veritmo serve --addr 127.0.0.1:4666` does not.

## Prometheus and Grafana

Prometheus reads the metrics of the server (`/api/metrics`) with its token. Grafana has its data sources and the two dashboards of `docs/grafana` already provisioned: the machine dashboard on Prometheus and the run history on the MySQL, through a user (`grafana_ro`) that can read every table of the server's schema and cannot write anything. Grafana keeps its own state in the same MySQL, in a schema of its own (`grafana`): there is no SQLite file of Grafana. Both users are created, and their passwords kept up to date from `.env`, by the `mysql-users` step.

## All the variables of `.env`

| Variable | What it is |
|---|---|
| `MYSQL_ROOT_PASSWORD`, `VERITMO_DB_PASSWORD`, `GRAFANA_DB_PASSWORD`, `GRAFANA_RO_PASSWORD`, `GRAFANA_ADMIN_PASSWORD`, `REDIS_PASSWORD` | the passwords of the database, Grafana and Redis |
| `VERITMO_TOKEN` | the token of the server: the dashboard and Prometheus use it |
| `MUSIC_TOKEN` | the token the server gives the music generators and they ask for |
| `UID`, `GID` | who writes the host folders (set by `compose-init.sh`) |
| `PROJECTS_PATH`, `MEDIA_PATH`, `MEDIA_DIR`, `MODELS_PATH`, `VOICES_PATH`, `HYPERFRAMES_PATH`, `INSTALL_PATH`, `CONFIG_PATH` | the host folders (above) |
| `OLLAMA_PATH` | the models of the Ollama of the `local-llm` profile |
| `OLLAMA_HOST` | the address of the Ollama the server uses; empty: the one of the profile |
| `HEARTMULA_GPU`, `DIFFRHYTHM2_GPU`, `MODEL3D_GPU` | the card each music generator and the 3D generator run on |
| `BIND`, `SERVER_PORT`, `GRAFANA_PORT` | where the server and Grafana are published |
| `MYSQL_PORT`, `HEARTMULA_PORT`, `DIFFRHYTHM2_PORT`, `OLLAMA_PORT`, `REDIS_PORT`, `LOKI_PORT` | only `compose.services.yaml`: the ports the pieces are published on for the server on this machine |
| `DASHBOARD_ORIGIN` | the browser origin of the dashboard |
| `SERVER_ARGS` | options added to the command line of the server; empty by default (the server starts as it does by default) |
| `SERVER_MEM_LIMIT` | the most RAM the server and what it launches may use. `compose-init.sh` sets `24g` on a machine with 40 GiB of RAM or less (the example's default is above what a 32 GB machine has) and says so; it never changes an existing `.env`. It is the same idea as `MEM_MAX` of the native `scripts/serve.sh` |
| `EDITION_TAG` | the Go build tag of the edition built into the image; empty for the default edition |
| `INSTALL_COMPONENTS` | what the installation step installs; empty: the default ones |
| `VERITMO_DB_NAME`, `VERITMO_DB_USER` | the name of the database and its user |
| `COMPOSE_PROFILES` | the profiles started on every `up` |
| `VERSION`, `PROMETHEUS_RETENTION` | the tag of the images and how long Prometheus keeps its data (defaults `dev` and `30d`) |

## Checking it

```bash
bash deploy/compose/check.sh
```

It looks at what is up from the outside, with the token of `.env`: the server answers and refuses a call without the token, the content mode is the default one, the Monitoring card has the addresses of Grafana, Prometheus, Loki and Redis from the environment and their tests answer ok, the music generators and Ollama are in use, and nothing was edited by hand in Settings. A piece whose profile is not up is skipped, not failed.

## Backing up and updating

- **The database:** `docker compose exec mysql sh -c 'mysqldump -uroot -p"$(cat /run/secrets/mysql_root_password)" --single-transaction --all-databases' > backup.sql`. The files (projects, models, library) are plain folders of this machine: back them up as any folder.
- **Updating:** `git pull`, then `docker compose pull` (or `docker compose build` for a version that is not published), `docker compose up -d`. A version with migrations has them applied by `migrate` before the new server starts.

## Troubleshooting

- `docker compose up` stops saying a variable is missing a value: the `.env` has an empty secret; run `bash scripts/compose-init.sh` (it never overwrites a `.env` that exists: delete it first to make a new one).
- `docker compose logs migrate` shows the failing migration or the error of the connection; the server is not started until it ends well.
- A host folder that did not exist when the stack started was made by Docker as `root` and its service cannot write to it: run `bash scripts/compose-init.sh` before the first `up`, or give the folder to your user.
- A task reported unavailable: `GET /api/environment` says which element is missing; run the installation step.
