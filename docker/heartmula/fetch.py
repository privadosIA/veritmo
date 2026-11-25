#!/usr/bin/env python3
"""Downloads the weights of the HeartMuLa service into /ckpt (about 21 GB). It is the only thing of this image that touches the network, and it only
runs when the user asks for it:

  docker run --rm --user $(id -u):$(id -g) -e HOME=/tmp -v <weights folder>:/ckpt veritmo/heartmula:1 python /opt/fetch.py

It prints one line per step, so the caller can show where it is, and exits 0 only when every file is there. What is already in /ckpt is not downloaded again.
"""

import os
import sys

CKPT = os.environ.get("HEARTMULA_CKPT", "/ckpt")
# (repository, folder inside /ckpt; "" is /ckpt itself)
PARTS = [
    ("HeartMuLa/HeartMuLaGen", ""),
    ("HeartMuLa/HeartMuLa-oss-3B-happy-new-year", "HeartMuLa-oss-3B"),
    ("HeartMuLa/HeartCodec-oss-20260123", "HeartCodec-oss"),
]


def main():
    from huggingface_hub import snapshot_download

    os.makedirs(CKPT, exist_ok=True)
    for repo, folder in PARTS:
        print(f"step: {repo}", flush=True)
        snapshot_download(repo_id=repo, local_dir=os.path.join(CKPT, folder) if folder else CKPT)
    sys.path.insert(0, os.path.dirname(__file__))
    import server

    missing = [f for f in server.REQUIRED if not os.path.exists(os.path.join(CKPT, f))]
    if missing:
        print("missing: " + ", ".join(missing), flush=True)
        return 1
    print("done", flush=True)
    return 0


if __name__ == "__main__":
    sys.exit(main())
