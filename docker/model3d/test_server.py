#!/usr/bin/env python3
"""Tests of the 3D generator service without a GPU: python3 -m unittest docker/model3d/test_server.py

The HTTP and the jobs are tested with a stub engine and a stub mesh (standard library only). The post-processing of a real mesh needs numpy
and trimesh and is skipped without them (the image has them: docker run --rm --entrypoint python veritmo/model3d:1 -m unittest /opt/test_server.py)."""

import http.client
import importlib.util
import json
import os
import sys
import tempfile
import threading
import time
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import server  # noqa: E402

HAVE_MESH = importlib.util.find_spec("trimesh") is not None and importlib.util.find_spec("numpy") is not None
HAVE_PIL = importlib.util.find_spec("PIL") is not None


class StubMesh:
    """What the service needs of a mesh, without trimesh."""

    def __init__(self, triangles=12, lo=(-1, -1, -1), hi=(1, 1, 1)):
        self.faces = [0] * triangles
        self.vertices = [0] * (triangles + 2)
        self._lo, self._hi = list(lo), list(hi)

    @property
    def bounds(self):
        return [self._lo, self._hi]

    def apply_translation(self, t):
        self._lo = [a + b for a, b in zip(self._lo, t)]
        self._hi = [a + b for a, b in zip(self._hi, t)]

    def export(self, file_type="glb"):
        return b"glTF-stub"


class StubEngine(server.Engine):
    name = "triposr"
    version = "stub@1"

    def __init__(self, mesh=None, fail=None, delay=0.0):
        self.mesh, self.fail, self.delay = mesh or StubMesh(), fail, delay

    def generate(self, rgba, detail):
        time.sleep(self.delay)
        if self.fail:
            raise self.fail
        return self.mesh, detail


class ServiceCase(unittest.TestCase):
    """A running service with a weights folder that has every file, a stub engine and no real cut-out."""

    def setUp(self):
        self.ckpt = tempfile.mkdtemp()
        for rels in server.REQUIRED.values():
            for rel in rels:
                path = os.path.join(self.ckpt, rel)
                os.makedirs(os.path.dirname(path), exist_ok=True)
                open(path, "w").close()
        self._saved = (server.CKPT, server.BUILT, server.ENGINES, server.cutout, server.JOBS_DIR, dict(server.JOBS), list(server.ORDER))
        server.CKPT, server.BUILT, server.JOBS_DIR = self.ckpt, ["triposr"], tempfile.mkdtemp()
        server.JOBS.clear(), server.ORDER.clear()
        self.engine = StubEngine()
        server.ENGINES = {"triposr": lambda: self.engine, "sf3d": lambda: self.engine}
        server._loaded["engine"] = None
        server.cutout = lambda image: image
        self.httpd = server.ThreadingHTTPServer(("127.0.0.1", 0), server.Handler)
        threading.Thread(target=self.httpd.serve_forever, daemon=True).start()
        self.port = self.httpd.server_address[1]
        if not any(t.name == "worker" for t in threading.enumerate()):
            threading.Thread(target=server.worker, daemon=True, name="worker").start()

    def tearDown(self):
        self.httpd.shutdown()
        self.httpd.server_close()
        server.CKPT, server.BUILT, server.ENGINES, server.cutout, server.JOBS_DIR = self._saved[:5]

    def call(self, method, path, body=None):
        c = http.client.HTTPConnection("127.0.0.1", self.port, timeout=10)
        c.request(method, path, body=body)
        r = c.getresponse()
        data = r.read()
        c.close()
        return r.status, data

    def json(self, method, path, body=None):
        status, data = self.call(method, path, body)
        return status, json.loads(data)

    def wait(self, job_id, states=("done", "failed")):
        for _ in range(200):
            _, v = self.json("GET", "/jobs/" + job_id)
            if v["state"] in states:
                return v
            time.sleep(0.02)
        self.fail("the job did not end")


class TestHTTP(ServiceCase):
    def test_health_and_engines(self):
        status, h = self.json("GET", "/health")
        self.assertEqual(status, 200)
        self.assertTrue(h["ready"])
        self.assertEqual(h["missing"], [])
        self.assertTrue(h["engines"]["triposr"]["ready"])
        self.assertFalse(h["engines"]["sf3d"]["built"], "an engine the image was built without is not ready")
        _, e = self.json("GET", "/engines")
        self.assertEqual(e["engines"]["triposr"]["version"], "triposr@unknown", "the version a model of this engine says made it, known before one is made")
        self.assertEqual(e["engines"]["sf3d"]["missing"], [])

    def test_not_ready_says_what_is_missing(self):
        os.remove(os.path.join(self.ckpt, "triposr/model.ckpt"))
        _, h = self.json("GET", "/health")
        self.assertFalse(h["ready"])
        self.assertEqual(h["missing"], ["triposr/model.ckpt"])

    def test_a_job_from_start_to_model(self):
        status, j = self.json("POST", "/generate?engine=triposr&detail=low", b"\x89PNG-bytes")
        self.assertEqual(status, 202)
        v = self.wait(j["id"])
        self.assertEqual(v["state"], "done", v)
        self.assertEqual((v["engine"], v["engine_version"], v["detail_used"], v["triangles"]), ("triposr", "stub@1", "low", 12))
        status, data = self.call("GET", "/jobs/" + j["id"] + "/model")
        self.assertEqual((status, data), (200, b"glTF-stub"))
        self.assertEqual(self.json("DELETE", "/jobs/" + j["id"])[0], 200)
        self.assertEqual(self.call("GET", "/jobs/" + j["id"])[0], 404)

    def test_the_model_is_not_there_until_the_job_is_done(self):
        self.engine.delay = 0.3
        _, j = self.json("POST", "/generate", b"x")
        self.assertEqual(self.call("GET", "/jobs/" + j["id"] + "/model")[0], 409)
        self.wait(j["id"])

    def test_what_the_service_refuses_at_once(self):
        for q, body, text in [
            ("engine=nope", b"x", "engine must be"),
            ("detail=ultra", b"x", "detail must be"),
            ("engine=triposr", b"", "empty"),
            ("engine=sf3d", b"x", "built without it"),
        ]:
            status, e = self.json("POST", "/generate?" + q, body)
            self.assertEqual(status, 400, q)
            self.assertIn(text, e["error"], q)
        os.remove(os.path.join(self.ckpt, "triposr/model.ckpt"))
        status, e = self.json("POST", "/generate", b"x")
        self.assertEqual(status, 400)
        self.assertIn("triposr/model.ckpt", e["error"])

    def test_an_engine_that_fails_leaves_a_sentence(self):
        self.engine.fail = server.Refused("the engine made no surface")
        _, j = self.json("POST", "/generate", b"x")
        v = self.wait(j["id"])
        self.assertEqual((v["state"], v["error"]), ("failed", "the engine made no surface"))
        self.engine.fail = RuntimeError("out of memory")
        _, j = self.json("POST", "/generate", b"x")
        self.assertIn("RuntimeError: out of memory", self.wait(j["id"])["error"])

    def test_the_token_protects_every_call(self):
        server.TOKEN = "secret"
        try:
            self.assertEqual(self.call("GET", "/health")[0], 401)
            c = http.client.HTTPConnection("127.0.0.1", self.port, timeout=10)
            c.request("GET", "/health", headers={"Authorization": "Bearer secret"})
            self.assertEqual(c.getresponse().status, 200)
        finally:
            server.TOKEN = ""


@unittest.skipUnless(HAVE_MESH, "numpy and trimesh are not installed")
class TestMesh(unittest.TestCase):
    def box(self, w, h, d, at=(0, 0, 0)):
        import trimesh

        m = trimesh.creation.box(extents=(w, h, d))
        m.apply_translation(at)
        return m

    def test_grounded_puts_the_origin_at_the_middle_of_the_base(self):
        m = server.grounded(self.box(2, 4, 6, at=(5, 7, -3)))
        lo, hi = m.bounds
        self.assertAlmostEqual(lo[1], 0)
        self.assertAlmostEqual((lo[0] + hi[0]) / 2, 0)
        self.assertAlmostEqual((lo[2] + hi[2]) / 2, 0)
        self.assertAlmostEqual(hi[1], 4)

    def test_upright_turns_what_triposr_gives_and_grounds_it(self):
        # TripoSR's raw output has its up along Z; after the two turns of its demo the height is Y
        m = server.upright_and_grounded(self.box(1, 2, 8))
        self.assertAlmostEqual(m.extents[1], 8, msg="the longest axis of the raw box (its up) is Y")
        self.assertAlmostEqual(m.bounds[0][1], 0)

    def test_no_surface_and_too_many_triangles(self):
        import trimesh

        with self.assertRaises(server.Refused):
            server.check_mesh(trimesh.Trimesh())
        old = server.MAX_TRIANGLES
        server.MAX_TRIANGLES = 5
        try:
            with self.assertRaises(server.TooMany) as c:
                server.check_mesh(self.box(1, 1, 1))
            self.assertEqual(c.exception.triangles, 12)
        finally:
            server.MAX_TRIANGLES = old

    def test_the_glb_keeps_the_colours(self):
        import numpy as np
        import trimesh

        m = self.box(1, 1, 1)
        m.visual.vertex_colors = np.tile(np.array([[200, 30, 30, 255]], dtype=np.uint8), (len(m.vertices), 1))
        scene = trimesh.load(trimesh.util.wrap_as_stream(server.export_glb(m)), file_type="glb")
        g = list(scene.geometry.values())[0]
        self.assertEqual(tuple(g.visual.vertex_colors[0][:3]), (200, 30, 30))


class TestDetails(unittest.TestCase):
    def test_the_details_to_try_go_down_from_the_one_asked(self):
        self.assertEqual(server.details_from("high"), ["high", "normal", "low"])
        self.assertEqual(server.details_from("normal"), ["normal", "low"])
        self.assertEqual(server.details_from("low"), ["low"])
        with self.assertRaises(server.Refused):
            server.details_from("x")

    def test_the_resolutions(self):
        self.assertEqual([server.DETAIL[d] for d in ("low", "normal", "high")], [128, 192, 256])


@unittest.skipUnless(HAVE_PIL, "Pillow is not installed")
class TestCutout(unittest.TestCase):
    def test_a_picture_with_transparency_is_used_as_it_is(self):
        import io

        from PIL import Image

        img = Image.new("RGBA", (8, 8), (255, 0, 0, 0))
        img.putpixel((4, 4), (255, 0, 0, 255))
        buf = io.BytesIO()
        img.save(buf, "PNG")
        out = server.cutout(buf.getvalue())
        self.assertEqual(out.mode, "RGBA")
        self.assertEqual(out.getpixel((0, 0))[3], 0)


if __name__ == "__main__":
    unittest.main()
