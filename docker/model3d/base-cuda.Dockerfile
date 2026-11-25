# A base image for building model3d with Stable Fast 3D (SF3D=1): CUDA 12.8 with nvcc and a Python virtual environment, because the two extensions
# of Stable Fast 3D are built with the CUDA toolchain. Nothing of it is needed for TripoSR alone (that one uses the python slim base).
#   docker build -f docker/model3d/base-cuda.Dockerfile -t vt-model3d-base:cuda128 docker/model3d
#   docker build -t veritmo/model3d:1 --build-arg BASE=vt-model3d-base:cuda128 --build-arg SF3D=1 --build-arg ENGINES=triposr,sf3d docker/model3d
# TORCH_CUDA_ARCH_LIST names the cards the CUDA code is built for (8.9: RTX 40, 12.0: RTX 50); add the one you have if it is another (7.5, 8.6...).
FROM nvidia/cuda:12.8.1-devel-ubuntu24.04
RUN apt-get update && apt-get install -y --no-install-recommends python3 python3-venv python3-dev git ca-certificates build-essential \
    && rm -rf /var/lib/apt/lists/* && python3 -m venv /opt/venv
ENV PATH=/opt/venv/bin:$PATH TORCH_CUDA_ARCH_LIST="8.9;12.0" CUDA_HOME=/usr/local/cuda
