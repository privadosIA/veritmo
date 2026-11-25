# syntax=docker/dockerfile:1
#
# The image of the server of the Compose stack (see compose.yaml and docs/COMPOSE.md). Two targets:
#
#   runtime  what the server runs in: veritmo, ffmpeg, the CUDA runtime libraries, and a pinned Chrome for the motion graphics.
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
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
# CGO is off: the SQLite driver is pure Go, so the program is one static file that runs on any base.
RUN CGO_ENABLED=0 go build ${EDITION_TAG:+-tags "$EDITION_TAG"} -trimpath -ldflags "-s -w" -o /out/orchestrator ./cmd/veritmo

# ─── What the server runs in ────────────────────────────────────────────────
FROM nvidia/cuda:${CUDA_VERSION}-runtime-ubuntu24.04 AS runtime
# Chrome for Testing, pinned: Ubuntu's own chromium is a snap, which a container cannot run.
ARG CHROME_VERSION=158.0.8127.36
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates curl unzip tini ffmpeg git \
        fonts-liberation fonts-noto-color-emoji fontconfig \
        libasound2t64 libatk-bridge2.0-0t64 libatk1.0-0t64 libcairo2 libcups2t64 libdbus-1-3 libdrm2 libgbm1 libglib2.0-0t64 \
        libnspr4 libnss3 libpango-1.0-0 libx11-6 libxcb1 libxcomposite1 libxdamage1 libxext6 libxfixes3 libxkbcommon0 libxrandr2 \
    && rm -rf /var/lib/apt/lists/*
RUN curl -fsSL -o /tmp/chrome.zip "https://storage.googleapis.com/chrome-for-testing-public/${CHROME_VERSION}/linux64/chrome-linux64.zip" \
    && unzip -q /tmp/chrome.zip -d /opt && rm /tmp/chrome.zip \
    # The container is the sandbox: Chrome's own needs user namespaces, which Docker's default profile does not give.
    && printf '#!/bin/sh\nexec /opt/chrome-linux64/chrome --no-sandbox "$@"\n' > /usr/local/bin/chromium \
    && chmod 755 /usr/local/bin/chromium


COPY --from=gobuild /out/orchestrator /usr/local/bin/veritmo
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
ENV HOME=/tmp/home
ENTRYPOINT ["/usr/local/bin/install-elements.sh"]
