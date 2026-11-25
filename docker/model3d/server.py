#!/usr/bin/env python3
"""The 3D model generator as a small HTTP service: one picture in, one binary glTF (.glb) out.

Calls (an optional bearer token protects all of them):

  GET    /health            {ready, version, busy, queued, gpu: {name, free_mb, total_mb}, missing, engines}
  GET    /engines           {engines: {triposr: {built, missing, ready, version}, sf3d: {...}}}
  POST   /generate?engine=triposr&detail=normal   the picture as the body (png, jpg or webp, up to 20 MB) -> 202 {id}
  GET    /jobs/{id}         {id, state, progress, error, seconds, engine, engine_version, detail, resolution, triangles}
  GET    /jobs/{id}/model   the .glb (409 until the job is done)
  DELETE /jobs/{id}         forgets a job and deletes its model

One model is made at a time; the others wait in a queue. The engine is loaded on first use and kept in memory; asking for the other one
frees the first (one engine on the card at a time). Nothing here reaches the network: the weights are in /ckpt and the cut-out model too.
Standard library only for the HTTP part (the engines import torch when the first model is made), so the API can be tested without a GPU.

What every model leaves with, whatever the engine gave: it stands upright in glTF's axes (Y up), its origin is the middle of its base,
it has surface and no more triangles than a scene takes (MODEL3D_MAX_TRIANGLES), and it keeps its colours.
"""

import io
import json
import os
import queue
import subprocess
import tempfile
import threading
import time
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlparse


def _secret(name):
    """The value of the variable NAME, or the trimmed content of the file the variable NAME_FILE names (a secret mounted by the container
    runtime). A file that cannot be read stops the service and the message names the file, never the value."""
    path = os.environ.get(name + "_FILE", "")
    if path:
        try:
            with open(path, encoding="utf-8") as f:
                return f.read().strip()
        except OSError as e:
            raise SystemExit(f"cannot read {name}_FILE ({path}): {e.strerror}")
    return os.environ.get(name, "")


CKPT = os.environ.get("MODEL3D_CKPT", "/ckpt")
TOKEN = _secret("MODEL3D_TOKEN")
VERSION = os.environ.get("MODEL3D_VERSION", "1")
PORT = int(os.environ.get("MODEL3D_PORT", "8790"))
JOBS_DIR = os.environ.get("MODEL3D_JOBS") or tempfile.mkdtemp(prefix="model3d-jobs-")
KEEP_JOBS = 20
MAX_IMAGE_BYTES = 20 << 20
# the limit of the triangles of a model in a scene (the same as the program's: blender.MaxModelTriangles)
MAX_TRIANGLES = int(os.environ.get("MODEL3D_MAX_TRIANGLES", "500000"))

# Which engines this image has (the build says it) and the files of the weights folder each one reads.
BUILT = [e for e in os.environ.get("MODEL3D_ENGINES", "triposr").split(",") if e]
REQUIRED = {
    "triposr": ["triposr/config.yaml", "triposr/model.ckpt", "triposr/dino-vitb16-config.json", "rembg/u2net.onnx"],
    "sf3d": ["sf3d/config.yaml", "sf3d/model.safetensors", "sf3d/dinov2-large-config.json", "rembg/u2net.onnx"],
}
# The resolution of the surface (points a side) of each detail; a model that has too many triangles is made again one detail lower.
DETAIL = {"low": 128, "normal": 192, "high": 256}
DETAIL_ORDER = ["high", "normal", "low"]


class Refused(Exception):
    """A request the service cannot do, with the message the caller sees."""


# ---------------------------------------------------------------------------------------------------------------------------------------
# What every model leaves with


def upright_and_grounded(mesh):
    """TripoSR's meshes are not upright in glTF's Y-up (its own demo turns them -90 degrees about X and 90 about Y) and are centred on their
    middle: turn it as the demo does and put the origin at the middle of the base, so that a model placed at the floor of a scene stands on it."""
    import numpy as np
    import trimesh

    mesh.apply_transform(trimesh.transformations.rotation_matrix(-np.pi / 2, [1, 0, 0]))
    mesh.apply_transform(trimesh.transformations.rotation_matrix(np.pi / 2, [0, 1, 0]))
    return grounded(mesh)


def grounded(mesh):
    """The origin at the middle of the base: the lowest point of the bounding box (Y), centred on the other two axes."""
    lo, hi = mesh.bounds
    mesh.apply_translation([-(lo[0] + hi[0]) / 2, -lo[1], -(lo[2] + hi[2]) / 2])
    return mesh


def check_mesh(mesh):
    """A model with no surface is an error; one with more triangles than a scene takes is TooMany (the caller makes it again with less detail)."""
    n = len(mesh.faces)
    if n == 0 or len(mesh.vertices) == 0:
        raise Refused("the engine made no surface: the picture may not show an object on its own")
    if n > MAX_TRIANGLES:
        raise TooMany(n)
    return n


class TooMany(Exception):
    def __init__(self, triangles):
        super().__init__(f"{triangles} triangles, more than the {MAX_TRIANGLES} of a scene")
        self.triangles = triangles


def export_glb(mesh):
    return mesh.export(file_type="glb")


def details_from(detail):
    """The details to try, from the one asked for down."""
    if detail not in DETAIL:
        raise Refused(f"detail must be low, normal or high, got {detail!r}")
    return DETAIL_ORDER[DETAIL_ORDER.index(detail):]


# ---------------------------------------------------------------------------------------------------------------------------------------
# The cut-out and the engines (torch is imported when the first model is made)

_cutout_session = None


def cutout(image_bytes):
    """The picture as RGBA with the background cut out. A picture that already has transparency is used as it is."""
    from PIL import Image

    img = Image.open(io.BytesIO(image_bytes))
    img.load()
    if img.mode in ("RGBA", "LA") or "transparency" in img.info:
        rgba = img.convert("RGBA")
        if rgba.getchannel("A").getextrema()[0] < 250:
            return rgba
    global _cutout_session
    import rembg

    if _cutout_session is None:
        os.environ.setdefault("U2NET_HOME", os.path.join(CKPT, "rembg"))
        _cutout_session = rembg.new_session("u2net")
    return rembg.remove(img.convert("RGB"), session=_cutout_session)


class Engine:
    name = ""
    version = ""

    def load(self):
        raise NotImplementedError

    def unload(self):
        pass

    def generate(self, rgba, detail):
        """-> (mesh, detail_used). The mesh as the engine gave it; the caller makes it upright, grounded and checked."""
        raise NotImplementedError


class TripoSR(Engine):
    name = "triposr"

    def __init__(self):
        self.version = engine_version("triposr")
        self.model = None

    def load(self):
        if self.model is not None:
            return
        import sys

        sys.path.insert(0, os.environ.get("TRIPOSR_DIR", "/opt/TripoSR"))
        os.environ.setdefault("TRIPOSR_DINO_CONFIG", os.path.join(CKPT, "triposr", "dino-vitb16-config.json"))
        import torch
        from tsr.system import TSR

        model = TSR.from_pretrained(os.path.join(CKPT, "triposr"), config_name="config.yaml", weight_name="model.ckpt")
        model.renderer.set_chunk_size(8192)
        model.to("cuda")
        self.model = model
        self.torch = torch

    def unload(self):
        self.model = None
        try:
            self.torch.cuda.empty_cache()
        except Exception:
            pass

    def generate(self, rgba, detail):
        import numpy as np
        from PIL import Image

        self.load()  # puts TripoSR on the path: it comes before the import of its helpers
        from tsr.utils import resize_foreground

        img = resize_foreground(rgba, 0.85)
        arr = np.array(img).astype(np.float32) / 255.0
        arr = arr[:, :, :3] * arr[:, :, 3:4] + (1 - arr[:, :, 3:4]) * 0.5  # the model sees the object on grey
        img = Image.fromarray((arr * 255).astype(np.uint8))
        with self.torch.no_grad():
            codes = self.model([img], device="cuda")
            last = None
            for d in details_from(detail):
                mesh = self.model.extract_mesh(codes, True, resolution=DETAIL[d])[0]
                mesh = upright_and_grounded(mesh)
                try:
                    check_mesh(mesh)
                    return mesh, d
                except TooMany as e:
                    last = e
            raise Refused(str(last))


class SF3D(Engine):
    """Stable Fast 3D. UNVERIFIED: it was written without an account that has accepted its licence on Hugging Face, so it follows the
    repository's documentation and has never been run. It is in the image only when it was built with SF3D=1."""

    name = "sf3d"

    def __init__(self):
        self.version = engine_version("sf3d")
        self.model = None

    def load(self):
        if self.model is not None:
            return
        import sys

        sys.path.insert(0, os.environ.get("SF3D_DIR", "/opt/stable-fast-3d"))
        os.environ.setdefault("SF3D_DINO_CONFIG", os.path.join(CKPT, "sf3d", "dinov2-large-config.json"))
        os.environ.setdefault("SF3D_OFFLINE", "1")
        import torch
        from sf3d.system import SF3D as Model

        self.torch = torch
        self.model = Model.from_pretrained(os.path.join(CKPT, "sf3d"), config_name="config.yaml", weight_name="model.safetensors")
        self.model.to("cuda")
        self.model.eval()

    def unload(self):
        self.model = None
        try:
            self.torch.cuda.empty_cache()
        except Exception:
            pass

    def generate(self, rgba, detail):
        self.load()
        bake = {"low": 512, "normal": 1024, "high": 2048}[detail]
        with self.torch.no_grad():
            mesh, _ = self.model.run_image(rgba, bake_resolution=bake, remesh="none")
        mesh = grounded(mesh)  # its orientation is the one glTF expects
        check_mesh(mesh)
        return mesh, detail


ENGINES = {"triposr": TripoSR, "sf3d": SF3D}
_loaded = {"engine": None}


def engine_version(name):
    """What a model made by this engine says made it (the commit the image was built with): the same text as the engine's own version."""
    return name + "@" + os.environ.get({"triposr": "TRIPOSR_COMMIT", "sf3d": "SF3D_COMMIT"}[name], "unknown")[:7]


def engine_view():
    out = {}
    for name in ("triposr", "sf3d"):
        missing = [f for f in REQUIRED[name] if not os.path.exists(os.path.join(CKPT, f))]
        built = name in BUILT
        out[name] = {"built": built, "missing": missing, "ready": built and not missing, "version": engine_version(name)}
    return out


def get_engine(name):
    """The engine, loaded; the other one is freed first: one engine on the card at a time."""
    cur = _loaded["engine"]
    if cur is not None and cur.name == name:
        return cur
    if cur is not None:
        cur.unload()
        _loaded["engine"] = None
    eng = ENGINES[name]()
    _loaded["engine"] = eng
    return eng


# ---------------------------------------------------------------------------------------------------------------------------------------
# Jobs


class Job:
    def __init__(self, engine, detail, image):
        self.id = uuid.uuid4().hex[:12]
        self.engine, self.detail, self.image = engine, detail, image
        self.state = "queued"
        self.error = ""
        self.created = time.time()
        self.started = 0.0
        self.finished = 0.0
        self.engine_version = ""
        self.resolution = 0
        self.detail_used = ""
        self.triangles = 0
        self.path = os.path.join(JOBS_DIR, self.id + ".glb")

    def view(self):
        end = self.finished or time.time()
        return {
            "id": self.id, "state": self.state, "progress": {"done": 1 if self.state == "done" else 0, "total": 1}, "error": self.error,
            "seconds": round(end - self.started, 2) if self.started else 0, "engine": self.engine, "engine_version": self.engine_version,
            "detail": self.detail, "detail_used": self.detail_used, "resolution": self.resolution, "triangles": self.triangles,
        }


JOBS = {}
ORDER = []
QUEUE = queue.Queue()
LOCK = threading.Lock()
BUSY = {"job": None}


def run_job(job):
    job.state, job.started = "running", time.time()
    try:
        eng = get_engine(job.engine)
        rgba = cutout(job.image)
        mesh, used = eng.generate(rgba, job.detail)
        glb = export_glb(mesh)
        with open(job.path, "wb") as f:
            f.write(glb)
        job.engine_version, job.detail_used, job.resolution, job.triangles = eng.version, used, DETAIL.get(used, 0), len(mesh.faces)
        job.state = "done"
    except Refused as e:
        job.state, job.error = "failed", str(e)
    except Exception as e:  # the engines raise whatever torch raises; the caller needs a sentence, not a trace
        job.state, job.error = "failed", f"{type(e).__name__}: {e}"
    finally:
        job.image = b""
        job.finished = time.time()


def worker():
    while True:
        job = QUEUE.get()
        BUSY["job"] = job
        try:
            if job.state == "queued":
                run_job(job)
        finally:
            BUSY["job"] = None


def forget(job_id):
    with LOCK:
        job = JOBS.pop(job_id, None)
        if job_id in ORDER:
            ORDER.remove(job_id)
    if job is not None:
        try:
            os.remove(job.path)
        except OSError:
            pass
    return job is not None


def submit(engine, detail, image):
    """Queues a model. Raises Refused for what the service can say at once."""
    if engine not in ENGINES:
        raise Refused(f"engine must be triposr or sf3d, got {engine!r}")
    details_from(detail)
    if not image:
        raise Refused("the picture is empty")
    if len(image) > MAX_IMAGE_BYTES:
        raise Refused(f"the picture is larger than {MAX_IMAGE_BYTES >> 20} MB")
    view = engine_view()[engine]
    if not view["built"]:
        raise Refused(f"the engine {engine} is not in this image: it was built without it")
    if view["missing"]:
        raise Refused(f"the weights of {engine} are not complete: " + ", ".join(view["missing"]))
    job = Job(engine, detail, image)
    with LOCK:
        JOBS[job.id] = job
        ORDER.append(job.id)
        while len(ORDER) > KEEP_JOBS:
            old = JOBS.get(ORDER[0])
            if old is not None and old.state in ("queued", "running"):
                break
            forget(ORDER[0])
    QUEUE.put(job)
    return job


# ---------------------------------------------------------------------------------------------------------------------------------------
# HTTP


def gpu_info():
    try:
        out = subprocess.run(
            ["nvidia-smi", "--query-gpu=name,memory.free,memory.total", "--format=csv,noheader,nounits"],
            capture_output=True, text=True, timeout=5, check=True).stdout.strip().splitlines()
        name, free, total = [x.strip() for x in out[0].split(",")]
        return {"name": name, "free_mb": int(free), "total_mb": int(total)}
    except Exception:
        return None


def health():
    engines = engine_view()
    ready = any(v["ready"] for v in engines.values())
    # what a person has to fetch: the files of the first engine this image has when none is ready
    missing = []
    if not ready:
        for name in BUILT:
            missing = engines[name]["missing"] if name in engines else []
            break
    return {"ready": ready, "version": VERSION, "busy": BUSY["job"] is not None, "queued": QUEUE.qsize(), "gpu": gpu_info(), "missing": missing, "engines": engines}


class Handler(BaseHTTPRequestHandler):
    server_version = "model3d/" + VERSION

    def log_message(self, fmt, *args):
        pass

    def _send(self, code, body, content_type="application/json"):
        data = body if isinstance(body, bytes) else json.dumps(body).encode()
        self.send_response(code)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def _error(self, code, message):
        self._send(code, {"error": message})

    def _authorised(self):
        if not TOKEN:
            return True
        return self.headers.get("Authorization", "") == "Bearer " + TOKEN

    def _route(self, method):
        if not self._authorised():
            return self._error(401, "a token is needed")
        url = urlparse(self.path)
        path = url.path.rstrip("/") or "/"
        if method == "GET" and path == "/health":
            return self._send(200, health())
        if method == "GET" and path == "/engines":
            return self._send(200, {"engines": engine_view()})
        if method == "POST" and path == "/generate":
            q = parse_qs(url.query)
            engine, detail = q.get("engine", ["triposr"])[0], q.get("detail", ["normal"])[0]
            try:
                n = int(self.headers.get("Content-Length", "0"))
            except ValueError:
                return self._error(400, "Content-Length must be a number")
            if n > MAX_IMAGE_BYTES:
                return self._error(413, f"the picture is larger than {MAX_IMAGE_BYTES >> 20} MB")
            image = self.rfile.read(n) if n > 0 else b""
            try:
                job = submit(engine, detail, image)
            except Refused as e:
                return self._error(400, str(e))
            return self._send(202, {"id": job.id})
        parts = path.strip("/").split("/")
        if len(parts) >= 2 and parts[0] == "jobs":
            job = JOBS.get(parts[1])
            if job is None:
                return self._error(404, "no such job")
            if method == "GET" and len(parts) == 2:
                return self._send(200, job.view())
            if method == "GET" and len(parts) == 3 and parts[2] == "model":
                if job.state != "done":
                    return self._error(409, "the model is not ready (state: " + job.state + ")")
                with open(job.path, "rb") as f:
                    return self._send(200, f.read(), "model/gltf-binary")
            if method == "DELETE" and len(parts) == 2:
                forget(job.id)
                return self._send(200, {"deleted": True})
        return self._error(404, "no such call")

    def do_GET(self):
        self._route("GET")

    def do_POST(self):
        self._route("POST")

    def do_DELETE(self):
        self._route("DELETE")


def main():
    os.makedirs(JOBS_DIR, exist_ok=True)
    threading.Thread(target=worker, daemon=True).start()
    srv = ThreadingHTTPServer(("0.0.0.0", PORT), Handler)
    print(f"model3d {VERSION} on :{PORT}, engines {BUILT}", flush=True)
    srv.serve_forever()


if __name__ == "__main__":
    main()
