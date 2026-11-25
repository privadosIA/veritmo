#!/bin/sh
# Adds Stable Fast 3D (https://github.com/Stability-AI/stable-fast-3d) to the image. It follows the repository's own instructions; it needs nvcc to
# build the two extensions of the repository (texture_baker and uv_unwrapper); without it this stops saying so.
set -eu
# Stable Fast 3D is under the Stability AI Community License: it is added to an image only when the person who builds it said they read it and agree.
if [ "${SF3D_LICENCE:-}" != "stability-community" ]; then
    echo "Stable Fast 3D is under the Stability AI Community License (https://stability.ai/community-license-agreement):" >&2
    echo "free for research and non-commercial use, and for commercial use under US\$1M of yearly revenue (register with Stability AI); above that you need a licence from Stability AI." >&2
    echo "Read it, and if you agree: --build-arg SF3D_LICENCE=stability-community" >&2
    exit 1
fi
command -v nvcc >/dev/null 2>&1 || { echo "Stable Fast 3D needs the CUDA toolchain (nvcc) to build: use --build-arg BASE=<a CUDA devel image with Python 3.11 or later>" >&2; exit 1; }
git clone https://github.com/Stability-AI/stable-fast-3d.git /opt/stable-fast-3d
cd /opt/stable-fast-3d
git checkout "${SF3D_COMMIT}"
# rembg is already there (one for both engines: the plain one, not the [gpu] flavour that would bring a second onnxruntime), and torch too.
# Its pins are of Python 3.10: gpytoolbox 0.2.0 has no wheel for 3.12 and cannot be built, and numpy 1.26, transformers 4.42 and huggingface-hub 0.23
# would pull down the ones TripoSR and rembg already run on. Those four go unpinned (the rest stays as it asks).
grep -v '^\./' requirements.txt | grep -v '^rembg' | grep -v -E '^(numpy|transformers|huggingface-hub)==' | sed 's/^gpytoolbox==.*/gpytoolbox/' > /tmp/sf3d-requirements.txt
pip install --no-cache-dir -r /tmp/sf3d-requirements.txt
# The extensions import torch in their setup.py: build them against the torch that is installed, not in an isolated environment without it.
pip install --no-cache-dir setuptools wheel ninja
# Their setup.py builds the CUDA kernels only when it sees a GPU, and a docker build sees none: USE_CUDA=1 asks for them (for the cards in
# TORCH_CUDA_ARCH_LIST), and USE_NATIVE_ARCH=0 keeps the C++ from being tuned to the CPU of the machine that builds the image.
USE_CUDA=1 USE_NATIVE_ARCH=0 pip install --no-cache-dir --no-build-isolation ./texture_baker/ ./uv_unwrapper/
