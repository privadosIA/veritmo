# Tasks: prepare-release-0-1

Commit messages in Spanish. Every task names how to verify it. **(owner)** marks the owner's tasks.

## 0. Decisions (owner)

- [ ] 0.1 **(owner)** The three decisions of the proposal: Chrome for Testing in or out of the image; the site's address; the `LICENSE` of the Stable Fast 3D port.

## 1. Version

- [ ] 1.1 `Version` (default `dev`) by `-ldflags -X`; `--version`; `veritmo_info{version}`; `Dockerfile` `ARG VERSION` and OCI labels; `serve.sh`/`install.sh` pass `git describe`. Verify: unit test of the string; metrics test; `docker inspect` of a local build shows the labels.

Commit: `Versión del programa fijada al compilar: --version, métrica info y etiquetas de las imágenes`

## 2. Changelog and releasing

- [ ] 2.1 `CHANGELOG.md`: `[Unreleased]`, `0.1.0` from the archive grouped as in the design, "Not in this release". Verify: every archived change maps to a line or is listed as internal in the commit message.
- [ ] 2.2 `docs/RELEASING.md`, and the archive step adds a line to `[Unreleased]` (in `docs/DEVELOPMENT.md`'s OpenSpec section).
- [ ] 2.3 `scripts/publish-github.sh --tag v<x>`: tags `github-main` after pushing and pushes the tag. Verify: `--dry-run --tag v0.0.0-test`.

Commit: `CHANGELOG con la 0.1.0 y guía de release`

## 3. Images

- [ ] 3.1 `.github/workflows/release-images.yml` as in the design; confirm Gitea ignores it (push a branch, look at Gitea Actions) or move it to the public tree only.
- [ ] 3.2 `scripts/check-image.sh <image>` (tar listing; the rules of the spec) and the workflow runs it on both images before pushing. Verify: a test image with a dummy `x.safetensors` fails naming it; the real images pass.
- [ ] 3.3 `/usr/share/doc/veritmo/` with `LICENSE`, `NOTICE`, `THIRD_PARTY.md` in both images; `docs/IMAGES.md` with the inventory (Go modules' licences with `go-licenses` or an equivalent pure-Go tool; Ubuntu packages from `dpkg -l` of the image). Chrome per decision 0.1: if out, the `tools` step downloads it into `bin/` and the server finds it there (`docs/INSTALL.md`, `COMPOSE.md`).
- [ ] 3.4 `compose.yaml`: server and tools from `ghcr.io/privadosia/veritmo-<x>:${VERSION:-latest}`, `build:` kept; the other images unchanged and documented as built locally in `docs/COMPOSE.md`. `.env.example`/`compose-init.sh` write `VERSION`. Verify: `docker compose config`.
- [ ] 3.5 `compose-init.sh` writes `SERVER_MEM_LIMIT=24g` on ≤ 40 GiB. Verify: `scripts/test/` with a fake `/proc/meminfo`.

Commit: `Imágenes server y tools en GHCR con guarda de modelos e inventario de licencias; compose las descarga`

## 4. Requirements and licences before installing

- [ ] 4.1 VRAM table in `docs/INSTALL.md` as in the design.
- [ ] 4.2 `licence {name, url, commercial, summary}` in the install plans of catalog models and 3D engines whose licence has conditions; `openapi.yaml`; a `docs/server-ready-licence-in-install-plan.md` note for dashboards-labs. Verify: handler tests for `sf3d` and one non-commercial catalog model; `contract/openapi.yaml` copied.

Commit: `Requisitos de VRAM medidos y licencia en los planes de instalación`

## 5. The family

- [ ] 5.1 `docs/REPOSITORIES.md` with the nine-row table; the same table in Veritmo's README (private and public), and the note for dashboards-labs and each port (`docs/server-ready-family-table.md` with the text to paste).
- [ ] 5.2 List which ports' GitHub copies are behind `dotnet/REVISIONS.txt` (their public READMEs still say "mediaOrquestator"): **(owner)** publish each with its own routine.

Commit: `Tabla de la familia de repositorios, igual en todos`

## 6. Public README (public worktree)

- [ ] 6.1 Rewrite the top as in the design; move the long introduction to `docs/OVERVIEW.md` (in `main`, so sync carries it); link checking. **(owner)** the GIF in `docs/media/hero.gif`.
- [ ] 6.2 **(owner)** GitHub settings: description (the tagline), topics (`self-hosted`, `local-ai`, `music-video`, `kinetic-typography`, `text-to-video`, `image-to-3d`, `stable-diffusion`, `blender`, `golang`), social preview (1280×640 from the GIF), website.

Commit (public worktree): `README: qué es, un vídeo, qué hace, requisitos y arranque rápido`

## 7. The release

- [ ] 7.1 Tag `v0.1.0` on `main`; `sync-public.sh "0.1.0"`; `publish-github.sh --tag v0.1.0 "Release 0.1.0"`; `gh release create v0.1.0 --notes-file <0.1.0 section>`.
- [ ] 7.2 Watch the workflow; **(owner)** make the two packages public.
- [ ] 7.3 **(owner, or someone else)** On a machine that never built them: follow only the public README; fix what stuck before announcing.
- [ ] 7.4 Site: requirements and a link to the 0.1.0 release; the address per decision 0.1.
- [ ] 7.5 **(owner)** 2FA on the organisation and token scopes/expiry (from `add-contribution-policy`), before announcing.
