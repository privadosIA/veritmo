# Spec Delta

## ADDED Requirements

### Requirement: The program knows its version

The binary SHALL carry a version set at build time from the release tag (`dev` without one); `veritmo --version` SHALL print `veritmo <version> (<edition> edition)`; the `veritmo_info` metric SHALL carry it as `version`; and the published images SHALL carry it, the source address and `Apache-2.0` as OCI labels.

#### Scenario: A release build
- **WHEN** the binary is built from the tag `v0.1.0`
- **THEN** `--version` prints `veritmo 0.1.0 (public edition)` and the info metric has `version="0.1.0"`

### Requirement: Every release has a changelog entry

`CHANGELOG.md` SHALL follow the Keep a Changelog format with a dated section per version, grouped by what a user can do, and an `[Unreleased]` section; the GitHub release of a tag SHALL carry that version's section as its notes; `docs/RELEASING.md` SHALL list the steps of a release, including adding a line to `[Unreleased]` when a change is archived.

#### Scenario: The first release
- **WHEN** `v0.1.0` is published
- **THEN** `CHANGELOG.md` has a dated `0.1.0` section and the GitHub release's notes are that section

### Requirement: Only the server and tools images are published, and they hold no model

A `v*` tag on the public repository SHALL build and push `ghcr.io/privadosia/veritmo-server` and `ghcr.io/privadosia/veritmo-tools` for `linux/amd64` with the version, its major and minor, and `latest`. No other image of the project SHALL be published by it. Before pushing, each image SHALL be checked and the release SHALL fail, naming the paths, when it holds a file with a model extension (`.gguf`, `.safetensors`, `.ckpt`, `.pt`, `.pth`, `.onnx`, or `.bin` over 10 MB), any file over 200 MB, or the source of Stable Fast 3D. Each published image SHALL carry the project's `LICENSE`, `NOTICE` and `THIRD_PARTY.md`, and `docs/IMAGES.md` SHALL list every component of it with its licence. `compose.yaml` SHALL name the published images and keep their `build:` blocks.

#### Scenario: A clean machine
- **WHEN** after `v0.1.0`, on a machine that never built them, `compose-init.sh` and `docker compose up -d` run
- **THEN** the server and tools images are downloaded, not built, and the server answers `GET /api/health`

#### Scenario: A model slips into the image
- **WHEN** a change to the `Dockerfile` copies a `.safetensors` file into the runtime image
- **THEN** the release workflow fails before pushing and names the file

### Requirement: The browser for motion graphics is not redistributed without permission

The published images SHALL NOT contain Chrome for Testing unless `docs/IMAGES.md` records that its terms plainly allow redistributing it. Otherwise the installer SHALL download it into the host's `bin/`, the server SHALL find it there, and the image check SHALL fail naming the path if the browser is in an image.

#### Scenario: Chrome in an image without permission
- **WHEN** the runtime image holds `chrome-linux64` and `docs/IMAGES.md` records no permission
- **THEN** the image check fails before pushing and names the path

#### Scenario: Motion graphics on a Compose install
- **WHEN** the stack runs from the published images and the installer has put Chrome in `bin/`
- **THEN** a motion-graphic scene renders

### Requirement: Compose limits the server on small machines

`compose-init.sh` SHALL set `SERVER_MEM_LIMIT=24g` in `.env` on a machine with 40 GiB of RAM or less, and leave the default otherwise, and SHALL say which it did.

#### Scenario: A 32 GB machine
- **WHEN** `compose-init.sh` runs on a machine with 31 GiB of RAM
- **THEN** `.env` has `SERVER_MEM_LIMIT=24g` and the script says so

### Requirement: A licence with conditions is shown before installing

The install plan of a catalog model or a 3D engine whose licence is not plainly permissive SHALL carry `licence` with its `name`, `url`, whether it allows commercial use and a one-line `summary`, so that a client can show it before the user confirms; the server SHALL NOT accept a licence on anyone's behalf.

#### Scenario: Stable Fast 3D
- **WHEN** a client asks for the install plan of `sf3d`
- **THEN** the plan carries the Stability AI Community License, its address, "with conditions" and the summary, and nothing is downloaded until `confirm` is sent with a key whose account accepted it

### Requirement: Stable Fast 3D is under its own licence, said the same everywhere, and never installed without acceptance

Every README, table and document of the family that names Stable Fast 3D SHALL say that it, its port and its weights are under the Stability AI Community License (free under US$1M of yearly revenue, registration with Stability AI for commercial use, a commercial licence above), and the port's `LICENSE` SHALL hold that licence. Building its .NET service, building the `model3d` container with it, and installing its weights SHALL each be refused unless the request carries the explicit acceptance `stability-community` (a build of every service skips it instead of failing), and the refusal SHALL name the licence and its address. The server SHALL record each acceptance and SHALL never accept on the user's behalf; the weights SHALL still need the user's own Hugging Face account to have accepted the licence.

#### Scenario: Building the service without accepting
- **WHEN** `scripts/get-services.sh --only stable-fast-3d` runs without `--accept-licence stability-community`
- **THEN** nothing is built and the message names the Stability AI Community License, its address and the flag

#### Scenario: Building the container with it
- **WHEN** `docker build` of `docker/model3d` runs with `--build-arg SF3D=1` and without `--build-arg SF3D_LICENCE=stability-community`
- **THEN** the build fails and the message names the Stability AI Community License, its address and the argument

#### Scenario: Installing from the dashboard
- **WHEN** a client confirms `POST /api/models3d/engines/sf3d/install` without `accept_licence`
- **THEN** the server answers 400 naming the licence and its address, and with `accept_licence: "stability-community"` it proceeds and records the acceptance (the licence, the date and the client)

#### Scenario: A build of every service
- **WHEN** `scripts/get-services.sh` runs without `--only` and without the acceptance
- **THEN** every other service is built, Stable Fast 3D is skipped, and the message names the licence and the flag

#### Scenario: Reading the family table
- **WHEN** someone reads the repositories table in any README of the family
- **THEN** the Stable Fast 3D row says "Stability AI Community License — not Apache-2.0"

### Requirement: The site lives at veritmo.xuxo.net

The project's site SHALL be served at `veritmo.xuxo.net`, the old address SHALL redirect permanently to it, and no file of the family SHALL link the old address except the redirect's configuration.

#### Scenario: An old link
- **WHEN** someone opens `https://orquestator.xuxo.net/` or any path under it
- **THEN** they land on the same path under `https://veritmo.xuxo.net/`

#### Scenario: Nothing in the family names the old address
- **WHEN** `grep -rn orquestator.xuxo.net` runs over the nine repositories
- **THEN** it finds only the redirect's configuration (and the OpenSpec records of what was decided)

### Requirement: The requirements are stated from measures

`docs/INSTALL.md` SHALL carry a table of which engines run with 8, 12, 16 and 24 GB of video memory, from the catalog's figures and the recorded runs, saying "not measured" where there is no measure; the public README SHALL state the requirements in one line and link it.

#### Scenario: Reading the requirements
- **WHEN** someone with a 12 GB card and 32 GB of RAM reads the README
- **THEN** one line tells them the machine is enough to start and the table tells them which engines they can use

### Requirement: The family of repositories is described the same everywhere

Veritmo, dashboards-labs and every port SHALL carry the same table of the nine repositories (what each is, its licence, its address), and no public README of the family SHALL use the former name.

#### Scenario: A reader lands on a port
- **WHEN** someone opens the public README of the TripoSR port
- **THEN** it says it is part of Veritmo, links to it, and lists the other eight repositories
