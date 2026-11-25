# Design: prepare-release-0-1

## Context (as of commit 74d8657d)

- `cmd/veritmo`, `module veritmo`; `--version` prints only the edition.
- `compose.yaml` names `veritmo/<x>:${VERSION:-dev}` for server, tools, heartmula, diffrhythm2, model3d, ctxpress, and builds them; the server's `mem_limit` is `${SERVER_MEM_LIMIT:-48g}`.
- `scripts/serve.sh` caps memory with `MEM_MAX` (85 % of RAM by default).
- `Dockerfile` `runtime`: the binary, Ubuntu's ffmpeg and libraries, the `nvidia/cuda` runtime base, Chrome for Testing downloaded from Google, Liberation and Noto fonts; models, programs and projects are host folders.
- `docker/model3d` can carry Stable Fast 3D's source when built with `SF3D=1`; its weights are fetched with the user's key.
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

A job after each build, on the built image (`docker save` → tar listing, no run): fail if any file matches `*.gguf`, `*.safetensors`, `*.ckpt`, `*.pt`, `*.pth`, `*.onnx`, `*.bin` over 10 MB, any regular file over 200 MB, or a path containing `stable-fast-3d` / `sf3d`; print the offending paths. The same check runs locally as `scripts/check-image.sh <image>`.

### Licence inventory of the published images

`docs/IMAGES.md`: per published image, every component with its licence and source (base image `nvidia/cuda` runtime under NVIDIA's terms for its container images; Ubuntu packages with their own licences, source in Ubuntu's archive; ffmpeg as packaged by Ubuntu; fonts OFL/Apache; the Go binary Apache-2.0 with the third-party Go modules listed by `go-licenses` or equivalent; Node in `tools`, MIT). Chrome for Testing: read its terms; if redistribution is not clearly allowed, the `runtime` image stops carrying it and the installer step (`tools`) downloads it into the host's `bin/` like the other programs; `docs/INSTALL.md` and `COMPOSE.md` follow. The image also carries `/usr/share/doc/veritmo/` with `LICENSE`, `NOTICE` and `THIRD_PARTY.md`.

### Compose memory on small machines

`compose-init.sh` reads `MemTotal`: ≤ 40 GiB → `SERVER_MEM_LIMIT=24g`; above → `SERVER_MEM_LIMIT` left to the default. `docs/COMPOSE.md` explains it next to `MEM_MAX`.

### VRAM table

`docs/INSTALL.md` §"What runs with how much video memory": rows by engine family (image models, Wan 5B Q4/Q8, Wan A14B, MiniMax H3, LTX 2.3, ACE-Step, HeartMuLa, DiffRhythm2, TripoSR, Stable Fast 3D, motion graphics, Blender), columns 8 / 12 / 16 / 24 GB, filled from the catalog's `vram_mb` and the measured runs in the docs; a cell with no measure says "not measured". The public README links it in its one-line requirements.

### Licences in the install plans

The install plan answers (`POST …/install` without `confirm`, for models of the catalog and for the 3D engines) gain `licence: {name, url, commercial, summary}` taken from the catalog entry or the engine table; `summary` is the one line the docs already have (e.g. Stable Fast 3D: free under US$1M of yearly revenue, attribution, its weights need an account that accepted the licence). The dashboard request (`docs/server-ready-…`) asks to show it above the confirm button.

### The family table

One table, kept in `docs/REPOSITORIES.md` of Veritmo and copied verbatim into the READMEs: Veritmo, dashboards-labs, forms-lib, heartmula, DiffRhythm2, lion-clap, musicgen-core, triposr, stable-fast-3d; each row: what it is, licence, GitHub link (the private edition keeps Gitea links through the existing private-line markers). The ports' public READMEs say "Part of [Veritmo](…)" and their `dotnet/REVISIONS.txt` commits are published (each port's own `publish` routine; the task lists which are behind GitHub).

### Public README

In the public worktree: `# Veritmo` and the tagline; a GIF (`docs/media/hero.gif`, ≤ 8 MB, ~10 s, made by the owner); four bullets with links to the site's examples (music videos from a song; video from a script; music; 3D scenes and models); requirements in one line (Linux x86_64, NVIDIA, 32 GB of RAM with the cap, link to the VRAM table); quickstart with Compose first and native second; the documentation table; the family table; licence (Apache-2.0, third-party models keep their own, link to THIRD_PARTY). The long introduction moves to `docs/OVERVIEW.md`.

## Risks

- Runner disk for the CUDA layers: freed first, measured in the first run.
- Gitea running `.github/workflows`: checked before tagging.
- Packages start private in an organisation: the owner makes them public once.
