#!/usr/bin/env python3
"""Downloads the weights of the 3D generator into /ckpt. It is the only thing of this image that touches the network, and it only runs when
the user asks for it:

  docker run --rm --user $(id -u):$(id -g) -e HOME=/tmp -v <weights folder>:/ckpt veritmo/model3d:1 python /opt/fetch.py [triposr] [sf3d]

With no argument it fetches TripoSR (no account: about 1.7 GB, and the cut-out model, 176 MB). `sf3d` needs the Hugging Face key of an account that
has accepted the licence of https://huggingface.co/stabilityai/stable-fast-3d, in HF_TOKEN; without it, or when Hugging Face refuses it, this says
which and stops: it never accepts a licence or looks for another way in. It prints one line per step, so the caller can show where it is, and exits 0
only when every file is there. What is already in /ckpt with the right hash is not downloaded again.
"""

import hashlib
import os
import shutil
import sys
import urllib.request

# the image keeps the service offline (HF_HUB_OFFLINE=1); this is the one program that is allowed to reach the network
os.environ["HF_HUB_OFFLINE"] = "0"
CKPT = os.environ.get("MODEL3D_CKPT", "/ckpt")
U2NET_URL = "https://github.com/danielgatis/rembg/releases/download/v0.0.0/u2net.onnx"
# (the sha256 of each file: what was fetched and tried on 2026-11-24)
SHA256 = {
    "rembg/u2net.onnx": "8d10d2f3bb75ae3b6d527c77944fc5e7dcd94b29809d47a739a7a728a912b491",
    "triposr/config.yaml": "74ca708ce086bf68e97709ea6b3d91f14717921c04691e84043f0eb8fcc68e62",
    "triposr/model.ckpt": "429e2c6b22a0923967459de24d67f05962b235f79cde6b032aa7ed2ffcd970ee",
    "triposr/dino-vitb16-config.json": "b87c0270b97db085fd82cf114a761fd0f62ae7914fbd407c752a2260646b689c",
    "sf3d/dinov2-large-config.json": "12df51c069a2dc1305e34ba71ef58bc2407ea553b75f4722a1715c1bce3bbed0",
}


def sha256(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def have(rel):
    path = os.path.join(CKPT, rel)
    if not os.path.isfile(path):
        return False
    want = SHA256.get(rel)
    return want is None or sha256(path) == want


def check(rel):
    want = SHA256.get(rel)
    if want and sha256(os.path.join(CKPT, rel)) != want:
        raise SystemExit(f"{rel} is not the file that was checked (its sha256 differs): the source changed, nothing was kept")


def hf_file(repo, filename, rel, token=None):
    from huggingface_hub import hf_hub_download

    dst = os.path.join(CKPT, rel)
    os.makedirs(os.path.dirname(dst), exist_ok=True)
    cache = os.path.join(CKPT, ".hf-cache")
    got = hf_hub_download(repo_id=repo, filename=filename, token=token, cache_dir=cache)
    shutil.copyfile(got, dst)
    check(rel)
    # The copy is the file the service reads: what the download left in the cache is the same bytes again (1.6 GB for TripoSR, 4 GB for Stable Fast 3D).
    shutil.rmtree(os.path.join(cache, "models--" + repo.replace("/", "--")), ignore_errors=True)


def fetch_cutout():
    rel = "rembg/u2net.onnx"
    if have(rel):
        return
    print(f"step: {U2NET_URL}", flush=True)
    dst = os.path.join(CKPT, rel)
    os.makedirs(os.path.dirname(dst), exist_ok=True)
    tmp = dst + ".part"
    req = urllib.request.Request(U2NET_URL, headers={"User-Agent": "veritmo"})
    with urllib.request.urlopen(req, timeout=60) as r, open(tmp, "wb") as f:
        shutil.copyfileobj(r, f, 1 << 20)
    os.replace(tmp, dst)
    check(rel)


def fetch_triposr():
    fetch_cutout()
    for repo, name, rel in [("stabilityai/TripoSR", "config.yaml", "triposr/config.yaml"), ("stabilityai/TripoSR", "model.ckpt", "triposr/model.ckpt"),
                            ("facebook/dino-vitb16", "config.json", "triposr/dino-vitb16-config.json")]:
        if have(rel):
            continue
        print(f"step: {repo} {name}", flush=True)
        hf_file(repo, name, rel)


def fetch_sf3d():
    token = os.environ.get("HF_TOKEN", "").strip()
    if not token:
        raise SystemExit("sf3d needs a Hugging Face key (HF_TOKEN) of an account that has accepted the licence of https://huggingface.co/stabilityai/stable-fast-3d")
    fetch_cutout()
    from huggingface_hub.errors import GatedRepoError, HfHubHTTPError

    # (the small config of its DINOv2 model is a public file of another repository: the service reads it from here instead of the network)
    for repo, name, rel in [("stabilityai/stable-fast-3d", "config.yaml", "sf3d/config.yaml"),
                            ("stabilityai/stable-fast-3d", "model.safetensors", "sf3d/model.safetensors"),
                            ("facebook/dinov2-large", "config.json", "sf3d/dinov2-large-config.json")]:
        if have(rel):
            continue
        print(f"step: {repo} {name}", flush=True)
        try:
            hf_file(repo, name, rel, token=token)
        except GatedRepoError:
            raise SystemExit("Hugging Face refuses this key for stabilityai/stable-fast-3d: the account has to accept the licence at https://huggingface.co/stabilityai/stable-fast-3d")
        except HfHubHTTPError as e:
            code = getattr(e.response, "status_code", 0)
            if code == 401:
                raise SystemExit("Hugging Face does not know this key")
            if code == 403:
                raise SystemExit("Hugging Face refuses this key for stabilityai/stable-fast-3d: the account has to accept the licence at https://huggingface.co/stabilityai/stable-fast-3d")
            raise


def main():
    engines = sys.argv[1:] or ["triposr"]
    os.makedirs(CKPT, exist_ok=True)
    for e in engines:
        if e == "triposr":
            fetch_triposr()
        elif e == "sf3d":
            fetch_sf3d()
        else:
            print(f"unknown engine {e!r}: triposr or sf3d", flush=True)
            return 2
    shutil.rmtree(os.path.join(CKPT, ".hf-cache"), ignore_errors=True)  # the locks and what an older run left
    sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
    import server

    missing = [f for e in engines for f in server.REQUIRED[e] if not os.path.exists(os.path.join(CKPT, f))]
    if missing:
        print("missing: " + ", ".join(missing), flush=True)
        return 1
    print("done", flush=True)
    return 0


if __name__ == "__main__":
    sys.exit(main())
