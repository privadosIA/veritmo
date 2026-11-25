#!/usr/bin/env python3
"""Downloads the weights of the DiffRhythm2 service into /ckpt (about 9.5 GB). It is the only thing of this
image that touches the network, and it only runs when the user asks for it (the Models tab):

  docker run --rm --user $(id -u):$(id -g) -e HOME=/tmp -v <weights folder>:/ckpt veritmo/diffrhythm2:1 python /opt/fetch.py

It prints one line per step, so the caller can show where it is, and exits 0 only when every file is there.
"""

import os
import sys

CKPT = os.environ.get("DIFFRHYTHM2_CKPT", "/ckpt")
REPO = "ASLP-lab/DiffRhythm2"
FILES = ["config.json", "model.safetensors", "decoder.bin", "decoder.json"]


def main():
    from huggingface_hub import hf_hub_download

    os.makedirs(CKPT, exist_ok=True)
    for name in FILES:
        print(f"step: {REPO}/{name}", flush=True)
        hf_hub_download(repo_id=REPO, filename=name, local_dir=CKPT)
    # The two MuQ models and the text encoder come with this call, into the hub layout the service reads offline.
    print("step: OpenMuQ/MuQ-MuLan-large (with MuQ-large-msd-iter and xlm-roberta-base)", flush=True)
    from muq import MuQMuLan

    MuQMuLan.from_pretrained("OpenMuQ/MuQ-MuLan-large", cache_dir=CKPT)
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
