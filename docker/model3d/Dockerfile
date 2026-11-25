# The 3D model generator as a small HTTP service (see README.md).
# The weights are NOT in the image: they are mounted read-only at /ckpt.
ARG BASE=python:3.11-slim-bookworm
FROM ${BASE}

# The commit of https://github.com/VAST-AI-Research/TripoSR the service was made and tested with (MIT licence).
ARG TRIPOSR_COMMIT=107cefdc244c39106fa830359024f6a2f1c78871
# Stable Fast 3D (Stability AI Community License) is NOT in the image unless it is asked for: --build-arg SF3D=1 --build-arg ENGINES=triposr,sf3d, and
# --build-arg SF3D_LICENCE=stability-community to say you read the licence and agree (https://stability.ai/community-license-agreement): without it the build stops.
# It needs the CUDA toolchain to build its extensions: use a BASE image that has nvcc (base-cuda.Dockerfile makes one; see README.md).
ARG SF3D=0
ARG SF3D_LICENCE=
ARG SF3D_COMMIT=ff21fc491b4dc5314bf6734c7c0dabd86b5f5bb2
ARG ENGINES=triposr
ARG MODEL3D_VERSION=1

RUN apt-get update \
    && apt-get install -y --no-install-recommends git ca-certificates gcc libc6-dev libgl1 libglib2.0-0 \
    && rm -rf /var/lib/apt/lists/*

# PyTorch with the CUDA 12.8 wheels: they run on the RTX 4070 and on Blackwell cards such as the RTX 5060 Ti.
RUN pip install --no-cache-dir "torch>=2.4,<2.11" "torchvision>=0.19,<0.26" --index-url https://download.pytorch.org/whl/cu128

RUN pip install --no-cache-dir "omegaconf==2.3.0" "einops>=0.7" "transformers>=4.40,<5" "huggingface_hub" "trimesh" "scikit-image" \
    "numpy" "Pillow" "rembg" "onnxruntime"

# TripoSR at a fixed commit, with the two changes of triposr-local.patch: scikit-image's marching cubes instead of the compiled torchmcubes (in the
# axis order the caller expects), and the config of its DINO model read from the weights folder instead of Hugging Face.
COPY triposr-local.patch /opt/triposr-local.patch
RUN git clone https://github.com/VAST-AI-Research/TripoSR.git /opt/TripoSR \
    && cd /opt/TripoSR && git checkout "${TRIPOSR_COMMIT}" && git apply /opt/triposr-local.patch

COPY install-sf3d.sh /opt/install-sf3d.sh
RUN if [ "${SF3D}" = "1" ]; then SF3D_LICENCE="${SF3D_LICENCE}" SF3D_COMMIT="${SF3D_COMMIT}" sh /opt/install-sf3d.sh; fi
# The two things Stable Fast 3D would download when it loads (the DINOv2 and CLIP models, whose weights are inside its own checkpoint): the service
# stays offline, so its code builds them from the small config in the weights folder / without weights (SF3D_DINO_CONFIG, SF3D_OFFLINE).
COPY sf3d-local.patch /opt/sf3d-local.patch
RUN if [ "${SF3D}" = "1" ]; then cd /opt/stable-fast-3d && git apply /opt/sf3d-local.patch; fi

COPY server.py /opt/server.py
COPY fetch.py /opt/fetch.py

LABEL org.veritmo.engines="${ENGINES}"
ENV MODEL3D_CKPT=/ckpt \
    MODEL3D_VERSION=${MODEL3D_VERSION} \
    MODEL3D_ENGINES=${ENGINES} \
    TRIPOSR_COMMIT=${TRIPOSR_COMMIT} \
    SF3D_COMMIT=${SF3D_COMMIT} \
    HF_HUB_OFFLINE=1 \
    U2NET_HOME=/ckpt/rembg \
    PYTHONUNBUFFERED=1
# Not root: the service only reads /ckpt and writes to /tmp and its own home. The application starts the containers with its own --user, which takes
# precedence. The service never reaches the network (HF_HUB_OFFLINE); fetch.py turns it off for itself.
RUN useradd --create-home --uid 10001 app
USER app
EXPOSE 8790
CMD ["python", "/opt/server.py"]
