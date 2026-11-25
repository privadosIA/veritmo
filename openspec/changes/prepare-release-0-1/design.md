# Design: prepare-release-0-1

## Context (as of commit 74d8657d)

- `cmd/veritmo`, `module veritmo`; `--version` prints only the edition.
- `compose.yaml` names `veritmo/<x>:${VERSION:-dev}` for server, tools, heartmula, diffrhythm2, model3d, ctxpress, and builds them; the server's `mem_limit` is `${SERVER_MEM_LIMIT:-48g}`.
- `scripts/serve.sh` caps memory with `MEM_MAX` (85 % of RAM by default).
- `Dockerfile` `runtime`: the binary, Ubuntu's ffmpeg and libraries, the `nvidia/cuda` runtime base, Chrome for Testing downloaded from Google (lines 33-46, with a `chromium` wrapper at `/usr/local/bin`), Liberation and Noto fonts; models, programs and projects are host folders.
- `docker/model3d` can carry Stable Fast 3D's source when built with `SF3D=1` (`install-sf3d.sh`); its weights are fetched with the user's key. `internal/assets3d/generate/engines.go` already marks the engine `Gated` with a free-text `Licence`; `POST /api/models3d/engines/{engine}/install` installs engines and weights. `scripts/get-services.sh` builds `stable-fast-3d` with the rest by default.
- The old site address is in the READMEs of Veritmo (`README.md`, `docs/SERVER.md`, the six `dotnet/*/README.md`) and of the ports, in dashboards-labs (`README.md`, `deploy/web/orquestator/`, `deploy/vps/`, `public-page.test.mjs`, two specs) and in the GitHub settings.
- `scripts/sync-public.sh` keeps the public `README.md` and does not export `dotnet/`, `.gitea/`, `openspec/changes/archive/`; `.github/` is exported.

## Decisions

### Version

`var Version = "dev"` set by `-ldflags -X`; `serve.sh`, `install.sh` and the `Dockerfile` (`ARG VERSION`) pass `git describe --tags --always`. `--version` → `veritmo 0.1.0 (public edition)`; `veritmo_info{version}`; OCI labels `version`, `source`, `licenses=Apache-2.0`.

### Changelog

Written once from `openspec/changes/archive/*/proposal.md` ("What changes"), grouped by what a user does: Music videos and motion graphics; Video; Images and retouch; Music; 3D (Blender scenes, model library, model generation); Editing a video you have; Assistant and providers; Dashboard, phone and API; Installation and data. One line per capability. "Not in this release": `plan-public-access`. From then on, archiving a change adds its line to `[Unreleased]` (step in `docs/RELEASING.md`).

### GHCR, two images only

`.github/workflows/release-images.yml` on `push: tags: ['v*']`: free runner disk, buildx, login with `GITHUB_TOKEN` (`packages: write`), metadata tags (`{{version}}`, `{{major}}.{{minor}}`, `latest`), build-push of `runtime` → `ghcr.io/privadosia/veritmo-server` and `tools` → `-tools`, `linux/amd64`, GHA cache. Confirm Gitea ignores `.github/workflows` while `.gitea/workflows` exists; if not, `sync-public.sh` adds the workflow only to the public tree.

No workflow publishes `heartmula`, `diffrhythm2`, `model3d` or `ctxpress`: the first three carry PyTorch stacks and the model3d one can carry Stable Fast 3D's source; they stay built by the user (`docker compose build …`), and `docs/COMPOSE.md` says so.

`compose.yaml`: `ghcr.io/privadosia/veritmo-server:${VERSION:-latest}` and `-tools`, `build:` kept. `.env.example` and `compose-init.sh` write `VERSION`.

### The guard

A job after each build, on the built image (`docker save` → tar listing, no run): fail if any file matches `*.gguf`, `*.safetensors`, `*.ckpt`, `*.pt`, `*.pth`, `*.onnx`, `*.bin` over 10 MB, any regular file over 200 MB, or a path containing `stable-fast-3d` / `sf3d`, or (unless `docs/IMAGES.md` records permission) a path containing `chrome-linux64`; print the offending paths. The same check runs locally as `scripts/check-image.sh <image>`.

### Licence inventory of the published images

`docs/IMAGES.md`: per published image, every component with its licence and source (base image `nvidia/cuda` runtime under NVIDIA's terms for its container images; Ubuntu packages with their own licences, source in Ubuntu's archive; ffmpeg as packaged by Ubuntu; fonts OFL/Apache; the Go binary Apache-2.0 with the third-party Go modules listed by `go-licenses` or equivalent; Node in `tools`, MIT). Chrome for Testing: decided out of the image unless its terms plainly allow redistributing it (task 3.6 reads them and writes the finding here). Out means the `runtime` image stops carrying it and the installer step (`tools`) downloads it into the host's `bin/` like the other programs; the server's browser lookup finds it there; `docs/INSTALL.md` and `COMPOSE.md` follow. The image also carries `/usr/share/doc/veritmo/` with `LICENSE`, `NOTICE` and `THIRD_PARTY.md`.

### Licence acceptance (Stable Fast 3D)

One id, `stability-community`, for the Stability AI Community License (https://stability.ai/community-license-agreement). Three gates, each refusing with the same message (the licence, its address, how to accept):

- `scripts/get-services.sh`: `--accept-licence stability-community`. With `--only stable-fast-3d` and no flag it builds nothing and exits non-zero; a build of every service skips that one with the message and builds the rest.
- `docker/model3d`: `SF3D=1` also needs `--build-arg SF3D_LICENCE=stability-community`; `install-sf3d.sh` fails without it.
- The server: `POST /api/models3d/engines/sf3d/install` takes `accept_licence`; without it, 400 with the licence's name and address (error code `licence_acceptance_required`, in `openapi.yaml`; `licence_not_accepted` already exists and means Hugging Face refuses the key); with it, the install proceeds and the acceptance is recorded in the server settings (`licence_acceptances`: licence id, date, client label). The server never fills it in: no default, no setting that turns it on. The weights still need the user's own Hugging Face key from an account that accepted the licence (`Gated`).

The install plan (`licence` object, see "Licences in the install plans") says whether it was already accepted, so a dashboard shows the text first and sends `accept_licence` only after the person agrees.

The port's `LICENSE` holds the Stability AI Community License (the text it shipped as `STABILITY-AI-COMMUNITY-LICENSE.md`, which is retired); the whole repository is under it, with no Apache-2.0 claim left in the port. README, NOTICE, `THIRD_PARTY.md`, `docs/MODEL_GENERATION.md`, `docs/INSTALL.md` and the family table say the same sentence: "Stability AI Community License (free under US$1M of yearly revenue, register with Stability AI for commercial use, a commercial licence above) — not Apache-2.0".

### The site address

`veritmo.xuxo.net`. The redirect (301, path and query kept) lives in dashboards-labs' `deploy/vps/nginx/`, next to the other sites; the DNS record and certificate are the owner's. Dashboards-labs renames `deploy/web/orquestator` and its tests; this change writes it the note (`docs/server-ready-site-address.md`) and fixes the Veritmo and ports READMEs. The check is a `grep -rn orquestator.xuxo.net` over the nine repositories that finds only the redirect's configuration.

### Compose memory on small machines

`compose-init.sh` reads `MemTotal`: ≤ 40 GiB → `SERVER_MEM_LIMIT=24g`; above → `SERVER_MEM_LIMIT` left to the default. `docs/COMPOSE.md` explains it next to `MEM_MAX`.

### VRAM table

`docs/INSTALL.md` §"What runs with how much video memory": rows by engine family (image models, Wan 5B Q4/Q8, Wan A14B, MiniMax H3, LTX 2.3, ACE-Step, HeartMuLa, DiffRhythm2, TripoSR, Stable Fast 3D, motion graphics, Blender), columns 8 / 12 / 16 / 24 GB, filled from the catalog's `vram_mb` and the measured runs in the docs; a cell with no measure says "not measured". The public README links it in its one-line requirements.

### Licences in the install plans

The install plan answers (`POST …/install` without `confirm`, for models of the catalog and for the 3D engines) gain `licence: {id, name, url, commercial, summary, accepted}` taken from the catalog entry or the engine table (`id` and `accepted` only where an acceptance is required, today `stability-community`); the catalog entries carry it too (the license fields of the entry, with the model's repository page as `url`), because a catalog download has no plan of its own; `summary` is the one line the docs already have (e.g. Stable Fast 3D: free under US$1M of yearly revenue, attribution, its weights need an account that accepted the licence). The dashboard request (`docs/server-ready-…`) asks to show it above the confirm button.

### The family table

One table, kept in `docs/REPOSITORIES.md` of Veritmo and copied verbatim into the READMEs: Veritmo, dashboards-labs, forms-lib, heartmula, DiffRhythm2, lion-clap, musicgen-core, triposr, stable-fast-3d; each row: what it is, licence, GitHub link (the private edition keeps Gitea links through the existing private-line markers). The ports' public READMEs say "Part of [Veritmo](…)" and their `dotnet/REVISIONS.txt` commits are published (each port's own `publish` routine; the task lists which are behind GitHub).

### Public README

In the public worktree: `# Veritmo` and the tagline; a GIF (`docs/media/hero.gif`, ≤ 8 MB, ~10 s, made by the dashboard: it is a recording of the dashboard making something; delivered into `docs/media/` of `main`, which `sync-public.sh` exports); four bullets with links to the site's examples (music videos from a song; video from a script; music; 3D scenes and models); requirements in one line (Linux x86_64, NVIDIA, 32 GB of RAM with the cap, link to the VRAM table); quickstart with Compose first and native second; the documentation table; the family table; licence (Apache-2.0, third-party models keep their own, link to THIRD_PARTY). The long introduction moves to `docs/OVERVIEW.md`.

## Risks

- Runner disk for the CUDA layers: freed first, measured in the first run.
- Gitea running `.github/workflows`: checked before tagging.
- Packages start private in an organisation: the owner makes them public once.
- The old address is in published pages and in other repositories' READMEs: the grep check lists them all before the release, and the ports' public READMEs only change when each port is published (task 7.2).
- GitHub labels a repository by its `LICENSE` file: with the Community License in the Stable Fast 3D port it will read "Other", which is the intended message.
