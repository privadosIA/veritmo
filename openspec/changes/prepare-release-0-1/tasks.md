# Tasks: prepare-release-0-1

Commit messages in Spanish. Every task names how to verify it. **(owner)** marks the owner's tasks.

## 0. Decisions (owner)

- [x] 0.1 **(owner)** The three decisions (2026-11-25, in the proposal): Chrome for Testing out of the image unless its terms allow it; the site at `veritmo.xuxo.net`; Stable Fast 3D under the Stability AI Community License everywhere, accepted explicitly.

## 1. Version

- [x] 1.1 `Version` (default `dev`) by `-ldflags -X`; `--version`; `veritmo_info{version}`; `Dockerfile` `ARG VERSION` and OCI labels; `serve.sh`/`install.sh` pass `git describe`. Verify: unit test of the string; metrics test; `docker inspect` of a local build shows the labels.

Commit: `Versión del programa fijada al compilar: --version, métrica info y etiquetas de las imágenes`

## 2. Changelog and releasing

- [x] 2.1 `CHANGELOG.md`: `[Unreleased]`, `0.1.0` from the archive grouped as in the design, "Not in this release". Verify: every archived change maps to a line or is listed as internal in the commit message.
- [x] 2.2 `docs/RELEASING.md`, and the archive step adds a line to `[Unreleased]` (in `docs/DEVELOPMENT.md`'s OpenSpec section).
- [x] 2.3 `scripts/publish-github.sh --tag v<x>`: tags `github-main` after pushing and pushes the tag. Verify: `--dry-run --tag v0.0.0-test`.

Commit: `CHANGELOG con la 0.1.0 y guía de release`

## 3. Images

- [x] 3.1 `.github/workflows/release-images.yml` as in the design, in `main` under `.github/` (which `sync-public.sh` exports; Gitea runs `.gitea/workflows`, and the job also carries `if: github.repository == 'privadosIA/veritmo'`, so it does nothing outside the public repository). Actions pinned by commit like the Gitea workflows. Verify: the YAML parses; a tag on the public repository runs it (9.2).
- [x] 3.2 `scripts/check-image.sh <image>` (tar listing; the rules of the spec) and the workflow runs it on both images before pushing. Verify: a test image with a dummy `x.safetensors` fails naming it; the real images pass.
- [x] 3.3 `/usr/share/doc/veritmo/` with `LICENSE`, `NOTICE`, `THIRD_PARTY.md` in both images; `docs/IMAGES.md` with the inventory (Go modules' licences with `go-licenses` or an equivalent pure-Go tool; Ubuntu packages from `dpkg -l` of the image). Verify: `docker run --rm --entrypoint ls <image> /usr/share/doc/veritmo`.
- [x] 3.4 `compose.yaml`: in the public edition the server and the installer come from `ghcr.io/privadosia/veritmo-<x>:${VERSION:-latest}` with `build:` kept (Compose pulls first and builds only when it cannot: measured), while the private edition keeps building its own: a new `# [public-line]` marker in `sync-public.sh` turns a commented line of `main` into the line of the public tree. The other images are unchanged and documented as built locally in `docs/COMPOSE.md`. `.env.example`/`compose-init.sh` write `VERSION` when the checkout is exactly a release. Verify: `docker compose config --images` on the private file and on the public form; `scripts/test/sync_public_test.sh`; `scripts/test/compose_init_test.sh`.
- [x] 3.5 `compose-init.sh` writes `SERVER_MEM_LIMIT=24g` on ≤ 40 GiB. Verify: `scripts/test/` with a fake `/proc/meminfo`.
- [x] 3.6 Chrome for Testing: read its terms (the download page and the licence files in the zip) and write the finding in `docs/IMAGES.md`. Unless they plainly allow redistributing it in an image: remove it from the `runtime` stage (`Dockerfile` 33-46); the installer (`install.sh` and the `tools` step) downloads the pinned version into the host's `bin/`; the server's browser lookup finds it there; compose mounts `bin/`; `docs/INSTALL.md` and `docs/COMPOSE.md` say so; `check-image.sh` fails on `chrome-linux64`. Verify: a motion-graphic scene renders from the images with Chrome only in `bin/`; the check fails on an image that still holds it.

Commit: `Imágenes server y tools en GHCR con guarda de modelos e inventario de licencias; Chrome fuera de la imagen; compose las descarga`

## 4. Requirements and licences before installing

- [x] 4.1 VRAM table in `docs/INSTALL.md` as in the design.
- [x] 4.2 `licence {id, name, url, commercial, summary, accepted}` in the install plans of catalog models and 3D engines whose licence has conditions (`id` and `accepted` only where an acceptance is required, see 5.3); `openapi.yaml`; the catalog entries of the Models screen carry the same `licence` (the entry's license fields plus the model's page as `url`; the 3D plans' `licence` was a text and is now this object). The note for dashboards-labs (`docs/server-ready-licence-in-install-plan.md`, in their repository: show the text above the confirm button, send `accept_licence` only after the person agrees) is written with 5.3, which defines that field. Verify: handler tests for `sf3d`, a catalog test for the non-commercial FLUX.1 Kontext entry and for `modelcat`; the contract tests of the server.

Commit: `Requisitos de VRAM medidos y licencia en los planes de instalación`

## 5. Stable Fast 3D: its licence, accepted on purpose

- [x] 5.1 `scripts/get-services.sh --accept-licence stability-community`: `--only stable-fast-3d` without it builds nothing, prints the licence, its address and the flag, and exits non-zero; a build of every service skips that one with the same message and builds the rest. Verify: `scripts/test/` cases for both, and with the flag it builds.
- [x] 5.2 `docker/model3d`: `SF3D=1` also needs `--build-arg SF3D_LICENCE=stability-community`; `install-sf3d.sh` fails with the message otherwise; `docker/model3d/README.md` and `docs/MODEL_GENERATION.md` say so. Verify: a build with `SF3D=1` alone fails; with both it succeeds.
- [x] 5.3 Server: `accept_licence` in the body of `POST /api/models3d/engines/sf3d/install`; without it 400 `licence_acceptance_required` (not `licence_not_accepted`, which already means Hugging Face refuses the key) naming the licence and its address; with it the install proceeds and the acceptance is recorded in the settings (`licence_acceptances`: id, date, client label). No default, no setting that accepts for the user. `openapi.yaml`, and the note for dashboards-labs (`docs/server-ready-licence-in-install-plan.md` in their repository: the `licence` object of 4.2 and `accept_licence`). Verify: handler tests for both answers and for the record; the weights still refuse without the Hugging Face key.
- [x] 5.4 The Stable Fast 3D port (`~/Proyectos/stable-fast-3d`): `LICENSE` holds the Stability AI Community License, README and NOTICE say the same, and the same sentence in `THIRD_PARTY.md`, `docs/INSTALL.md`, the family table and `dotnet/stable-fast-3d` of Veritmo (refresh it and `REVISIONS.txt`). Verify: `grep -rni "stable fast 3d\|sf3d"` over the nine repositories: every line that names its licence says Community, none says Apache-2.0 for it. **(owner)** publish the port to GitHub with its own routine (its commit `ee1f1db` is local and on Gitea only): the repository label becomes "Other".

Commit: `Stable Fast 3D con su licencia en todas partes y aceptación explícita para compilarlo e instalarlo`

- [x] 5.5 **The installer must not walk into Stable Fast 3D.** (done 2026-11-25: `il_recommended_3d` gives `triposr` at most; `--accept-licence stability-community`; interactive `all` asks to type `accept`; the message is shared in `scripts/lib/licence-stability.sh`; cases in `install_services_test.sh`.) Found in review (2026-11-25): `install.sh` offers the 3D tier `all` (TripoSR + Stable Fast 3D) and `il_recommended_3d` makes it the default with Enter or `--yes` on any card of 8 GB or more; it then runs `get-services.sh --only triposr,stable-fast-3d` with no `--accept-licence`, which (rightly) refuses and fails the whole step. And `docs/INSTALL.md` (§Music services, the 3D tiers) still says "Building Stable Fast 3D needs no licence, its weights do", which contradicts 5.1. Fix: `il_recommended_3d` recommends `triposr` at most, never `all`; choosing `all` interactively shows the same licence message as `get-services.sh` and asks the person to type `accept` (anything else falls back to `triposr`); `install.sh --accept-licence stability-community` passes it through and is the only way `--yes`/`--3d all` builds it; without it, `--3d all` with `--yes` is a usage error naming the flag. `docs/INSTALL.md` says the same as 5.1 (building it needs the acceptance; its weights also need the Hugging Face acceptance). Verify: `scripts/test/` cases: Enter on a 16 GB card builds TripoSR only; `--yes --3d all` without the flag exits 2 naming it; with the flag it builds both; the interactive `all` without typing `accept` builds TripoSR only; `grep -n "needs no licence" docs/` finds nothing.

## 6. The site at veritmo.xuxo.net

- [x] 6.1 DNS record and certificate for `veritmo.xuxo.net` on the VPS (done 2026-11-25: the A record already existed; certificate by `deploy/vps/issue-certs.sh veritmo.xuxo.net`, https answers 200).
- [x] 6.2 Replace the old address in Veritmo (`README.md`, `docs/SERVER.md`, the six `dotnet/*/README.md`) and in the READMEs of the ports (each repository's own commit; the copies in `dotnet/` follow when they are refreshed). `docs/SERVER.md`: its mentions were the private tailnet address of the server itself behind a Caddy that no longer runs; the section is rewritten (2026-11-25) without that address. Not published until the site answers (6.4): the commits are local. Verify: `grep -rn orquestator.xuxo.net` over Veritmo and the ports finds nothing outside `openspec/`.
- [x] 6.3 `docs/server-ready-site-address.md` for dashboards-labs: rename `deploy/web/orquestator` and what points at it (`README.md`, `deploy/vps/`, `public-page.test.mjs`, the specs `public-landing` and `vps-offline-page`), add the 301 for the old name keeping path and query. Verify: the note lists every file the grep finds there.
- [x] 6.4 Deploy from dashboards-labs and check (done 2026-11-25, `https://orquestator.xuxo.net/some/path?a=1` answers 301 to `https://veritmo.xuxo.net/some/path?a=1`, http too): `curl -sI https://orquestator.xuxo.net/some/path` answers 301 with `location: https://veritmo.xuxo.net/some/path`.

Commit: `El sitio pasa a veritmo.xuxo.net; la nota para dashboards-labs`

## 7. The family

- [x] 7.1 `docs/REPOSITORIES.md` with the nine-row table. The nine rows are already in the "Related repositories" of every README, private and public (2026-11-25); what remains is the licence column and the Stable Fast 3D row saying "Stability AI Community License — not Apache-2.0", the same table copied verbatim from `docs/REPOSITORIES.md` into all of them, and the note for dashboards-labs and each port (`docs/server-ready-family-table.md` with the text to paste). Verify: a script compares the table of each README with the one in `docs/REPOSITORIES.md` (links aside).
- [ ] 7.2 Which ports' GitHub copies are behind (checked 2026-11-25; every README of the family already has the nine-row table in `main`, and the GitHub copies of the ports have the previous table): their public READMEs still say "mediaOrquestator" or the old site address (heartmula 1 time, DiffRhythm2 3, lion-clap 4, musicgen-core 1, triposr 8, stable-fast-3d 8; dashboards-labs 1; forms-lib 0), and none has the licence column; `triposr` and `stable-fast-3d` also lack the commits of the rename; the Stable Fast 3D licence change (`ee1f1db`) is only local and on Gitea. **(owner)** publish each with its own routine **after** the site answers (6.4), so no public README links an address that does not exist yet. Verify: `bash scripts/check-family-table.sh` on the public checkouts.

Commit: `Tabla de la familia de repositorios, igual en todos`

## 8. Public README (public worktree)

- [x] 8.1 Rewrite the top as in the design (done in the public worktree, `public-release`: the hero is the picture of the existing example clip, `docs/media/cats-karate-shots.jpg`, until the dashboard delivers `docs/media/hero.gif` (a request is in `dashboards-lab`: `docs/server-ready-hero-gif.md`) and the line is swapped); the long introduction is in `docs/OVERVIEW.md` (in `main`, with the public example; sync carries it); link checking: `scripts/check-docs.sh` on `main` and on the staged public tree (`sync-public.sh --dry-run`) both pass.
- [ ] 8.2 **(owner)** GitHub settings (2026-11-25: description, topics and website of `privadosIA/veritmo` are set; the social preview image is left, by hand in Settings, once the GIF exists): description (the tagline), topics (`self-hosted`, `local-ai`, `music-video`, `kinetic-typography`, `text-to-video`, `image-to-3d`, `stable-diffusion`, `blender`, `golang`), social preview (1280×640 from the GIF), website.

Commit (public worktree): `README: qué es, un vídeo, qué hace, requisitos y arranque rápido`

## 9. The release

- [ ] 9.1 Tag `v0.1.0` on `main`; `sync-public.sh "0.1.0"`; `publish-github.sh --tag v0.1.0 "Release 0.1.0"`; `gh release create v0.1.0 --notes-file <0.1.0 section>`.
- [ ] 9.2 Watch the workflow; **(owner)** make the two packages public.
- [ ] 9.3 **(owner, or someone else)** On a machine that never built them: follow only the public README; fix what stuck before announcing.
- [ ] 9.4 Site (at `veritmo.xuxo.net`): requirements and a link to the 0.1.0 release; the GitHub website field (8.2) points to it.
- [ ] 9.5 **(owner)** 2FA on the organisation and token scopes/expiry (from `add-contribution-policy`), before announcing.
