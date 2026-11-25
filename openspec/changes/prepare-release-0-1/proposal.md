# Proposal: prepare-release-0-1

## Why

The project is now Veritmo, Apache-2.0, public on GitHub with eight sibling repositories (the dashboards, the forms library and six .NET ports: HeartMuLa, DiffRhythm2, LAION-CLAP, musicgen-core, TripoSR and Stable Fast 3D), and the site shows real results. What a stranger still cannot do is install a known version with confidence:

- There is no version: the only tag is `pwa-movil-v1`, `veritmo --version` prints the edition and no number, there is no changelog.
- The Compose stack builds every image on the user's machine (Go, the CUDA runtime, Chrome) before anything runs.
- The public README opens with what the project was months ago, as a wall of text, with no picture.
- The family is not described as one: the README's "Related repositories" lists six repositories and not TripoSR or Stable Fast 3D, and the public READMEs of the ports still say "Part of mediaOrquestator".
- Third-party weights with their own terms (Stable Fast 3D's, under the Stability AI Community License; the non-commercial models of the catalog) must never travel inside anything this project publishes, and nothing guards that today.

Already done and not repeated here: the memory cap (`MEM_MAX`, 85 % of RAM by default, `24G` advised on 32 GB), the rename, the licence change to Apache-2.0, and Stable Fast 3D's weights being fetched only with the user's own Hugging Face key from an account that accepted its licence.

## What changes

- **Version** stamped from the tag into the binary (`veritmo 0.1.0 (public edition)`), the info metric and the images' OCI labels.
- **`CHANGELOG.md`** (Keep a Changelog) with a `0.1.0` section written from the archived changes, `docs/RELEASING.md`, and `publish-github.sh --tag`.
- **Images on GHCR** (`ghcr.io/privadosia/veritmo-server`, `-tools`), built by a GitHub Actions workflow on a `v*` tag; `compose.yaml` pulls them and keeps `build:`. Only these two are published: the music, `model3d` and `ctxpress` images are never pushed by this project.
- **A guard on what is published**: the workflow fails if an image holds a model file (by extension or size), the source of Stable Fast 3D or Chrome for Testing (unless its terms are recorded as allowing it); a licence inventory of what each published image contains. Chrome leaves the image by default and the installer downloads it into the host's `bin/`.
- **Compose on 32 GB**: `compose-init.sh` writes `SERVER_MEM_LIMIT=24g` on machines with 40 GB or less (today's default, 48g, is above their RAM).
- **Requirements**: a table of what runs with 8, 12, 16 and 24 GB of video memory, from the catalog and the recorded runs.
- **Licences shown before installing**: the install plan of every engine or model whose licence has conditions (Stable Fast 3D, the non-commercial models) names the licence, its link and a one-line summary, so the dashboard shows it before the confirmation.
- **Stable Fast 3D under its own licence, accepted on purpose**: every README, table and document of the family says it, its port and its weights are under the Stability AI Community License (not Apache-2.0), and the port's `LICENSE` holds that licence. Building its .NET service, building the `model3d` container with it and installing its weights are each refused unless the request carries the explicit acceptance `stability-community`; the server records each acceptance and never gives one on the user's behalf.
- **The site at `veritmo.xuxo.net`**: the old address redirects permanently (same path) and no file of the family links it any more except the redirect's own configuration.
- **The family as one**: the same "Related repositories" table (nine rows, GitHub links in the public edition) in Veritmo, dashboards-labs and every port; the ports' public READMEs with the new name.
- **The public README** rewritten at the top (it lives in the public worktree, which sync does not overwrite).
- **The release**: tag, sync, publish, GitHub release with the changelog section, images checked by pulling them on a clean machine.

## What does not change

- No feature work. `plan-public-access` (a plan, no tasks done) stays open and is listed under "Not in this release".

## Capabilities

- `release` (ADDED).

## Decisions taken (2026-11-25)

1. **Chrome for Testing**: out of the image unless its terms plainly allow redistributing it; the installer downloads it into the host's `bin/`. Reading its terms is a task (3.6), and the finding is written in `docs/IMAGES.md`.
2. **The site's address**: `veritmo.xuxo.net`; `orquestator.xuxo.net` redirects to it permanently.
3. **Stable Fast 3D's `LICENSE`**: the Stability AI Community License, the same in the port's `LICENSE`, its README and NOTICE, and every table of the family. Because that is a licence with conditions, nothing builds or installs it without the user's explicit acceptance (`stability-community`).

## Still the owner's

The DNS record and certificate for `veritmo.xuxo.net` and the redirect on the VPS (6.1, 6.4), GitHub settings, making the two packages public, the clean-machine test and 2FA (see the tasks marked **(owner)**).
