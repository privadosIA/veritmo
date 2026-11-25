# The published images

[← Back to the README](../README.md)

Two images are published with each release, on the GitHub Container Registry, for `linux/amd64`:

| Image | What it is | Dockerfile target |
|---|---|---|
| `ghcr.io/privadosia/veritmo-server` | The server (`veritmo serve`) with `ffmpeg` and the libraries it runs on | `runtime` |
| `ghcr.io/privadosia/veritmo-tools` | The one-shot installation step (`docker compose --profile install run --rm installer`) | `tools` |

They are tagged with the version (`0.1.0`), its major and minor (`0.1`) and `latest`, and carry the version, the source address and
`Apache-2.0` as OCI labels. [RELEASING.md](RELEASING.md) says how they are made.

**What they do not hold.** No model of any kind, however small: weights, voices and LoRAs are downloaded by you, from the Models
screen, into folders of your machine. Nothing of Stable Fast 3D (its licence has conditions; see
[THIRD_PARTY.md](../THIRD_PARTY.md)). No Chrome (see [below](#chrome-for-testing)). The programs the installer builds (stable-diffusion.cpp,
Piper, whisper.cpp, …) are not in the image either: the installer writes them to folders of your machine. The music, 3D-model and
compression images are never published by this project; `docker compose build …` makes them on your machine
([COMPOSE.md](COMPOSE.md)). `scripts/check-image.sh <image>` looks inside an image without running it and refuses all of the above;
the release workflow runs it on both images before it pushes anything.

Each image carries `LICENSE`, `NOTICE` and `THIRD_PARTY.md` in `/usr/share/doc/veritmo/`.

The licence of every component is below; for the full list of an image's packages, `docker run --rm --entrypoint dpkg <image> -l`,
and for the terms of one of them, its `/usr/share/doc/<package>/copyright` inside the image.

## veritmo-server

| Component | Licence | Source |
|---|---|---|
| `veritmo` (the server, `/usr/local/bin/veritmo`) | Apache-2.0 | this repository |
| The Go modules linked into it | see [below](#go-modules-in-the-binary) | each module's repository |
| Base image `nvidia/cuda:12.8.1-runtime-ubuntu24.04`: the CUDA runtime libraries | NVIDIA's terms for CUDA ([EULA](https://docs.nvidia.com/cuda/eula/) and the licence of the container image, shown on its [Docker Hub page](https://hub.docker.com/r/nvidia/cuda)) | NVIDIA, unmodified |
| Ubuntu 24.04 and its packages, in the base image | each package's own licence, in `/usr/share/doc/<package>/copyright` | Ubuntu's archive (`apt-get source <package>`) |
| `ffmpeg` 6.1.1 (Ubuntu's build, with its libraries) | GPL-2+ (built with `--enable-gpl`; its libraries are LGPL, BSD and others) | Ubuntu's archive (`apt-get source ffmpeg`) |
| `tini` 0.19 | MIT | Ubuntu's archive |
| `git`, `curl`, `unzip`, `ca-certificates` | GPL-2, MIT-like, Info-ZIP and MPL-2.0 respectively | Ubuntu's archive |
| `fonts-liberation` | SIL OFL 1.1 (GPL-2+ with font exception for the older files) | Ubuntu's archive |
| `fonts-noto-color-emoji` | SIL OFL 1.1 (Apache-2.0 for the tooling) | Ubuntu's archive |
| `fontconfig` and the libraries a Chrome needs to run in the image (`libnss3`, `libcairo2`, `libpango`, `libgbm1`, …) | LGPL, MPL-2.0, MIT, BSD and others; each in its package | Ubuntu's archive |

`veritmo` starts `ffmpeg` as a separate program and does not link it, so the GPL of `ffmpeg` does not reach the server's own
licence; a person who redistributes the image redistributes the GPL program in it and the source is the one in Ubuntu's archive.

## veritmo-tools

| Component | Licence | Source |
|---|---|---|
| The install scripts of this repository (`/app/scripts`) and `install-elements.sh` | Apache-2.0 | this repository |
| Base image `nvidia/cuda:12.8.1-devel-ubuntu24.04`: the CUDA toolkit | NVIDIA's terms for CUDA, as above | NVIDIA, unmodified |
| Ubuntu packages `git`, `cmake`, `build-essential`, `pkg-config`, `curl`, `ca-certificates`, `unzip`, `xz-utils`, `ffmpeg`, `python3`, `libopenblas-dev` | each package's own licence (`/usr/share/doc/<package>/copyright`) | Ubuntu's archive |
| Node.js 22.11.0 (to build the HyperFrames runtime) | MIT (with the licences of what it bundles, in its `LICENSE`) | nodejs.org, pinned |

What the step builds or downloads into your folders is not part of the image and keeps its own licence:
[THIRD_PARTY.md](../THIRD_PARTY.md).

## Go modules in the binary

The modules `veritmo` links in (`go list -deps ./cmd/veritmo`), with the licence each one's `LICENSE` file states. Refresh the table
when `go.mod` changes.

| Module | Licence |
|---|---|
| `github.com/andybalholm/brotli` | MIT |
| `github.com/coder/websocket` | ISC |
| `github.com/dustin/go-humanize` | MIT |
| `github.com/glebarez/go-sqlite` | BSD-3-Clause |
| `github.com/glebarez/sqlite` | MIT |
| `github.com/go-gormigrate/gormigrate/v2` | MIT |
| `github.com/go-sql-driver/mysql` | MPL-2.0 (file-level copyleft: unmodified, so nothing to publish beyond its source) |
| `github.com/golang/freetype` | FreeType License (one of two choices it offers; the other is GPL-2+) |
| `github.com/google/uuid` | BSD-3-Clause |
| `github.com/jackc/pgpassfile`, `pgservicefile`, `pgx/v5`, `puddle/v2` | MIT |
| `github.com/jinzhu/inflection`, `jinzhu/now` | MIT |
| `github.com/klauspost/compress` | Apache-2.0 (with BSD-3-Clause parts) |
| `github.com/mattn/go-isatty` | MIT |
| `github.com/parquet-go/parquet-go` | Apache-2.0 |
| `github.com/pierrec/lz4/v4` | BSD-3-Clause |
| `github.com/remyoudompheng/bigfft` | BSD-3-Clause |
| `github.com/tdewolff/font`, `tdewolff/parse/v2` | MIT |
| `golang.org/x/image`, `x/sync`, `x/sys`, `x/text` | BSD-3-Clause |
| `gopkg.in/yaml.v3` | Apache-2.0 and MIT |
| `gorm.io/gorm`, `gorm.io/driver/mysql`, `gorm.io/driver/postgres` | MIT |
| `modernc.org/libc`, `mathutil`, `memory`, `sqlite` | BSD-3-Clause |

## Chrome for Testing

The motion-graphic scenes and overlays draw their pages with Chrome. The first images carried Chrome for Testing, downloaded from
Google during the build. Its terms were read for this release and **it is not in any published image**:

- The package says "Copyright Google LLC. All rights reserved" and sends to the Chrome Terms of Service (`chrome://terms`).
- It bundles the Widevine content decryption module, whose own licence reads "You may not use, modify, sell, or otherwise distribute
  the Software without a separate license agreement with Google".

Nothing plainly allows redistributing it, so the image does not. The installer downloads it from Google's server onto your machine
(`scripts/get-chrome.sh`, the `chrome` component of the Compose installer; pinned version and SHA-256) and the server finds it as
`bin/chromium`. The image only carries the system libraries that Chrome needs. The release check fails the build if a Chrome ever
gets into an image, unless this page is changed to record, after reading terms that allow it, that redistribution is permitted
(`scripts/check-image.sh` says how).
