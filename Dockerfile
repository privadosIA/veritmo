# syntax=docker/dockerfile:1
#
# The image of the server of the Compose stack (see compose.yaml and docs/COMPOSE.md). Two targets:
#
#   runtime  what the server runs in: veritmo, ffmpeg, the CUDA runtime libraries and the libraries a Chrome needs (the browser itself is installed into the host's bin/, see scripts/get-chrome.sh).
#            It holds nothing the person installs or creates: the programs the installer builds (bin/, third_party/), the models, the
#            voices, the HyperFrames blocks and the projects are folders of the host, mounted under /app.
#   tools    the same base with the CUDA toolkit and the build chain. It runs the pinned install scripts of the repository (the ones
#            scripts/install.sh uses) and writes what they build into those host folders, so the programs are built against the
#            libraries of the image that runs them.
#
#   docker build --target runtime -t veritmo/server .
#   docker build --target tools   -t veritmo/tools  .
#
# EDITION_TAG is the Go build tag of the edition (empty: the default edition).

ARG GO_IMAGE=golang:1.27.2-bookworm
ARG CUDA_VERSION=12.8.1

# ─── The program ────────────────────────────────────────────────────────────
FROM ${GO_IMAGE} AS gobuild
ARG EDITION_TAG=
# The version stamped into the program and the labels of the images (the release tag; `dev` for a local build).
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
# CGO is off: the SQLite driver is pure Go, so the program is one static file that runs on any base.
RUN CGO_ENABLED=0 go build ${EDITION_TAG:+-tags "$EDITION_TAG"} -trimpath -ldflags "-s -w -X veritmo/internal/platform/edition.Version=${VERSION}" -o /out/orchestrator ./cmd/veritmo

# ─── What the server runs in ────────────────────────────────────────────────
FROM nvidia/cuda:${CUDA_VERSION}-runtime-ubuntu24.04 AS runtime
ARG VERSION=dev
ARG SOURCE=https://github.com/privadosIA/veritmo
LABEL org.opencontainers.image.title="veritmo-server" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.source="${SOURCE}" \
      org.opencontainers.image.licenses="Apache-2.0"
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates curl unzip tini ffmpeg git \
        fonts-liberation fonts-noto-color-emoji fontconfig \
        libasound2t64 libatk-bridge2.0-0t64 libatk1.0-0t64 libcairo2 libcups2t64 libdbus-1-3 libdrm2 libgbm1 libglib2.0-0t64 \
        libnspr4 libnss3 libpango-1.0-0 libx11-6 libxcb1 libxcomposite1 libxdamage1 libxext6 libxfixes3 libxkbcommon0 libxrandr2 \
    && rm -rf /var/lib/apt/lists/*
# No browser in the image: Chrome for Testing is Google software that may not be redistributed, so the installer (scripts/get-chrome.sh, the
# `chrome` component) downloads it into the host's bin/ and the server finds bin/chromium on its PATH. The libraries and fonts above are what
# that Chrome needs to run in here.


COPY --from=gobuild /out/orchestrator /usr/local/bin/veritmo
# The licence of the program and of what the image holds, where a person who pulls the image finds it (docs/IMAGES.md).
COPY LICENSE NOTICE THIRD_PARTY.md /usr/share/doc/veritmo/
RUN ln -s veritmo /usr/local/bin/veritmo-api
COPY deploy/compose/entrypoint.sh /usr/local/bin/entrypoint.sh

# The layout the server expects, under /app: the host folders are mounted over bin/, models/, voices/, third_party/, hyperframes/, projects/.
WORKDIR /app
ENV PATH=/app/bin:/usr/local/bin:/usr/bin:/bin \
    XDG_CONFIG_HOME=/config \
    HOME=/config
EXPOSE 4666
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/entrypoint.sh"]
CMD ["serve"]

# ─── What installs the programs ─────────────────────────────────────────────
FROM nvidia/cuda:${CUDA_VERSION}-devel-ubuntu24.04 AS tools
ARG VERSION=dev
ARG SOURCE=https://github.com/privadosIA/veritmo
LABEL org.opencontainers.image.title="veritmo-tools" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.source="${SOURCE}" \
      org.opencontainers.image.licenses="Apache-2.0"
# Node is only for building the HyperFrames runtime (get-hyperframes-runtime.sh asks for 20 or newer), pinned.
ARG NODE_VERSION=22.11.0
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        git cmake build-essential pkg-config curl ca-certificates unzip xz-utils ffmpeg python3 libopenblas-dev \
    && rm -rf /var/lib/apt/lists/*
RUN curl -fsSL "https://nodejs.org/dist/v${NODE_VERSION}/node-v${NODE_VERSION}-linux-x64.tar.xz" | tar -xJ -C /usr/local --strip-components=1
WORKDIR /app
COPY scripts ./scripts
COPY internal/music/hyperframes/libs.txt ./internal/music/hyperframes/libs.txt
COPY deploy/compose/install.sh /usr/local/bin/install-elements.sh
COPY LICENSE NOTICE THIRD_PARTY.md /usr/share/doc/veritmo/
ENV HOME=/tmp/home
ENTRYPOINT ["/usr/local/bin/install-elements.sh"]
