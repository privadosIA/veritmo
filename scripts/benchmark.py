#!/usr/bin/env python3
"""scripts/benchmark.py

Runs the same small set of real jobs on this machine and writes how long each took and how much
memory it needed, so two machines (a big desktop and a modest laptop) can be compared on equal terms.

  python3 scripts/benchmark.py                      # run every test whose models are installed
  python3 scripts/benchmark.py --only sdxl,wan5b    # run some of them
  python3 scripts/benchmark.py --keep               # also keep the pictures, clips and songs to look at them
  python3 scripts/benchmark.py --list               # list the tests and whether their models are here
  python3 scripts/benchmark.py --gpu 1              # run on one GPU (index as nvidia-smi shows it)
  python3 scripts/benchmark.py --compare a.json b.json   # print two result files side by side

Every test is a real project run through bin/veritmo with fixed seeds (the same work on every
machine), forced (no cache) and measured from the phase events: seconds per phase (and, for the tests that run sd, `load_seconds`: how much of it sd spent loading models), the peak VRAM of
the GPU in use and the lowest free RAM. A test that fails (out of memory on a modest card, say) is
recorded as failed with the reason: that is a result too. The tests do not touch your projects; they
write out/bench-* and tmp/, and remove them when done. Results go to benchmarks/<label>-<date>.json; the label is what --label says (default "machine"), never the host name.
The two song generators that run as .NET services (heartmula, diffrhythm2) need the server running
(./serve.sh --local): the test starts the service through the server's API, so the model load lands in the
measured VRAM, and stops it afterwards. The seconds it took to be ready are the phase "service_start".
Stop other GPU users (Ollama, a running render) first or the figures are not comparable.
"""
import argparse, datetime, json, os, platform, re, shutil, socket, subprocess, sys, tempfile, threading, time, urllib.request

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
V = "models/video/"
SDXL = {"model": "models/sd_xl_base_1.0.safetensors"}
KREA = {"model_set": {"diffusion_model": "models/krea2_turbo-Q4_K_M.gguf", "llm": "models/Qwen3VL-4B-Instruct-Q4_K_M.gguf",
                      "vae": "models/wan_2.1_vae.safetensors", "cfg_scale": 1}}
INTO = {"model_set": {"diffusion_model": "models/intorealismKrea2_v40.safetensors", "llm": "models/Qwen3VL-4B-Instruct-Q4_K_M.gguf",
                      "vae": "models/wan_2.1_vae.safetensors", "cfg_scale": 1}}
PROMPT = "photo of two friends sitting on a park bench talking and smiling, sunny day, green trees, 35mm"
MOTION = "they chat and laugh, leaves move gently in the breeze, static camera"
WAN5B = {"diffusion_model": V + "Wan2.2-TI2V-5B-Q4_K_S.gguf", "t5xxl": V + "umt5-xxl-encoder-Q4_K_S.gguf", "vae": V + "wan2.2_vae.safetensors"}
H3 = {"engine": "minimax-h3", "first_frame": True, "diffusion_model": V + "minimax_h3_fl2va_pruned-Q4_K_M.gguf",
      "llm": V + "qwen3vl_32b_minimax_h3-Q4_K_M.gguf", "vae": V + "minimax_h3_video_vae_fp16.safetensors",
      "audio_vae": V + "minimax_h3_audio_vae_fp32.safetensors", "lora": V + "minimax_h3_fl2v_turbo_8step.safetensors",
      "resolution": "768x416"}
LTX = {"engine": "ltx-2.3", "diffusion_model": V + "ltx-2.3-22b-distilled-1.1-Q4_K_M.gguf", "llm": V + "gemma-3-12b-it-qat-UD-Q4_K_XL.gguf",
       "vae": V + "ltx-2.3-22b-distilled_video_vae.safetensors", "audio_vae": V + "ltx-2.3-22b-distilled_audio_vae.safetensors",
       "connectors": V + "ltx-2.3-22b-distilled_embeddings_connectors.safetensors"}


def image_project(name, style, res, steps, variants):
    return {"project": name, "output": {"mode": "images"},
            "defaults": {"resolution": res, "sampler": "euler", "steps": steps, **style},
            "scenes": [{"id": "a", "prompt": PROMPT, "seed": 11, "variants": variants},
                       {"id": "b", "prompt": "photo of a city street at night, neon lights, rain, cinematic", "seed": 12, "variants": variants}]}


def video_project(name, animate):
    return {"project": name, "output": {"mode": "video", "file": f"out/{name}.mp4"},
            "defaults": {"voice": "voices/en_US-lessac-medium.onnx", "resolution": "768x416", "sampler": "euler", "steps": 20, "fps": 24,
                         **SDXL, "animate": animate},
            "scenes": [{"id": "clip", "prompt": PROMPT, "seed": 21, "silence_seconds": 2, "animate": True, "motion_prompt": MOTION}]}


def music_project(name, model_args):
    return {"project": name, "output": {"mode": "music"}, "defaults": {"music": {"output_format": "mp3", **model_args}},
            "tracks": [{"id": "song", "prompt": "upbeat pop song, bright synths, steady drums", "instrumental": True,
                        "duration_seconds": 30, "seed": 5}]}


SONG_LYRICS = ("[verse]\nWalking down the street in the morning light\nEverything feels new and the sky is bright\n"
               "[chorus]\nWe are moving forward, never looking back\nDancing on the road and we're staying on track\n"
               "[outro]\nOh oh, the day is ours")


def song_project(name, generator, seconds):
    """A song for the generators that read lyrics and tags (HeartMuLa, DiffRhythm2): English lyrics, no instrumental."""
    return {"project": name, "output": {"mode": "music"}, "defaults": {"music": {"output_format": "mp3", "generator": generator}},
            "tracks": [{"id": "song", "prompt": "upbeat pop song, bright synths, steady drums, female voice", "tags": "pop,upbeat,synth,female",
                        "lyrics": SONG_LYRICS, "duration_seconds": seconds, "seed": 5}]}


HM = "models/music/heartmula/"
DR = "models/music/diffrhythm2/"
# id of the test -> the engine of the server that has to be running for it
SERVICES = {"heartmula": "heartmula", "diffrhythm2": "diffrhythm2"}

# id -> (title, project, files it needs)
TESTS = {
    "sdxl": ("SDXL 1024x576, 20 steps, 4 images", image_project("bench-sdxl", SDXL, "1024x576", 20, 2),
             ["models/sd_xl_base_1.0.safetensors"]),
    "krea2": ("Krea 2 turbo 1024x576, 8 steps, 4 images", image_project("bench-krea2", KREA, "1024x576", 8, 2),
              ["models/krea2_turbo-Q4_K_M.gguf", "models/Qwen3VL-4B-Instruct-Q4_K_M.gguf", "models/wan_2.1_vae.safetensors"]),
    "intorealism": ("IntoRealism 1024x576, 8 steps, 4 images", image_project("bench-into", INTO, "1024x576", 8, 2),
                    ["models/intorealismKrea2_v40.safetensors", "models/Qwen3VL-4B-Instruct-Q4_K_M.gguf", "models/wan_2.1_vae.safetensors"]),
    "wan5b": ("SDXL still + Wan 2.2 5B animation (33 frames, 832x480)", video_project("bench-wan5b", {**WAN5B, "resolution": "832x480"}),
              [WAN5B["diffusion_model"], WAN5B["t5xxl"], WAN5B["vae"], "models/sd_xl_base_1.0.safetensors", "voices/en_US-lessac-medium.onnx"]),
    "h3": ("SDXL still + MiniMax H3 first-frame, 8-step LoRA, 768x416", video_project("bench-h3", H3),
           [v for k, v in H3.items() if isinstance(v, str) and v.startswith(V)] + ["models/sd_xl_base_1.0.safetensors", "voices/en_US-lessac-medium.onnx"]),
    "ltx": ("SDXL still + LTX 2.3 distilled", video_project("bench-ltx", LTX),
            [v for k, v in LTX.items() if isinstance(v, str) and v.startswith(V)] + ["models/sd_xl_base_1.0.safetensors", "voices/en_US-lessac-medium.onnx"]),
    "music": ("ACE-Step XL turbo, 30 s instrumental song", music_project("bench-music", {}),
              ["models/music/acestep-v15-xl-turbo-Q8_0.gguf", "models/music/vae-BF16.gguf"]),
    "heartmula": ("HeartMuLa 3B, 60 s song with English lyrics (.NET service)", song_project("bench-heartmula", "heartmula", 60),
                  [HM + f for f in ("config.json", "gen_config.json", "tokenizer.json", "HeartMuLa-oss-3B/model-00004-of-00004.safetensors",
                                    "HeartCodec-oss/model-00002-of-00002.safetensors")]),
    "diffrhythm2": ("DiffRhythm2, 40 s song with English lyrics (.NET service)", song_project("bench-diffrhythm2", "diffrhythm2", 40),
                    [DR + f for f in ("config.json", "model.safetensors", "decoder.json", "decoder.safetensors", "mulan_text.safetensors",
                                      "mulan_text_tokenizer.json")]),
}


def to_yaml(v, ind=0):
    """JSON is valid YAML, so the project is written as JSON (no YAML library needed)."""
    return json.dumps(v, indent=2)


def sh(cmd, first_line=False):
    """Runs a program (a list: no shell) and returns its output; the first line only when asked."""
    try:
        out = subprocess.run(cmd, capture_output=True, text=True, timeout=20).stdout.strip()
        return out.splitlines()[0] if first_line and out else out
    except Exception:
        return ""


def gpus():
    out = sh(["nvidia-smi", "--query-gpu=index,name,memory.total,driver_version", "--format=csv,noheader,nounits"])
    res = []
    for line in out.splitlines():
        p = [x.strip() for x in line.split(",")]
        if len(p) >= 4:
            res.append({"index": int(p[0]), "name": p[1], "vram_mb": int(p[2]), "driver": p[3]})
    return res


def mem_available_mb():
    for line in open("/proc/meminfo"):
        if line.startswith("MemAvailable:"):
            return int(line.split()[1]) // 1024
    return 0


def machine(label="machine"):
    cpu = ""
    for line in open("/proc/cpuinfo"):
        if line.startswith("model name"):
            cpu = line.split(":", 1)[1].strip()
            break
    total = 0
    for line in open("/proc/meminfo"):
        if line.startswith("MemTotal:"):
            total = int(line.split()[1]) // 1024
    return {"host": label, "os": platform.platform(), "cpu": cpu, "threads": os.cpu_count(), "ram_mb": total,
            "gpus": gpus(), "commit": sh(["git", "-C", str(ROOT), "rev-parse", "--short", "HEAD"]), "sd": sh([f"{ROOT}/bin/sd", "--version"], first_line=True)}


class Probe(threading.Thread):
    """Samples the used VRAM of the chosen GPU and the free RAM while a test runs."""
    def __init__(self, gpu):
        super().__init__(daemon=True)
        self.gpu, self.stop, self.peak_vram, self.min_ram = gpu, False, 0, 1 << 30
        self.base_vram = self.sample_vram()

    def sample_vram(self):
        q = ["nvidia-smi", "--query-gpu=memory.used", "--format=csv,noheader,nounits"]
        if self.gpu is not None:
            q[1:1] = ["-i", str(self.gpu)]
        try:
            return int(sh(q, first_line=True))
        except Exception:
            return 0

    def run(self):
        while not self.stop:
            self.peak_vram = max(self.peak_vram, self.sample_vram())
            self.min_ram = min(self.min_ram, mem_available_mb())
            time.sleep(0.5)


SERVER = os.environ.get("BENCH_SERVER", "http://127.0.0.1:4666")


def api(method, path):
    """A call to the local server with the token that ./serve.sh keeps; returns the decoded JSON or None."""
    try:
        token = open(os.path.expanduser("~/.config/veritmo/token")).read().strip()
        req = urllib.request.Request(SERVER + path, method=method, headers={"Authorization": "Bearer " + token})
        with urllib.request.urlopen(req, timeout=30) as r:
            return json.load(r)
    except Exception:
        return None


def engine_state(engine):
    d = api("GET", "/api/music/engines")
    return (d or {}).get("engines", {}).get(engine)


def start_engine(engine, timeout=300):
    """Starts a music service through the server and waits until it is ready; returns the seconds it took, or None."""
    t = time.time()
    api("POST", f"/api/music/engines/{engine}/start")
    while time.time() - t < timeout:
        e = engine_state(engine)
        if e and e.get("available"):
            return time.time() - t
        if e and e.get("container", {}).get("state") == "stopped":
            return None
        time.sleep(2)
    return None


def stop_engine(engine):
    api("POST", f"/api/music/engines/{engine}/stop")
    time.sleep(3)


def run_test(tid, gpu, timeout, keep=None):
    title, project, _ = TESTS[tid]
    name = project["project"]
    path = os.path.join(tempfile.mkdtemp(prefix="bench-"), "scenes.yaml")
    open(path, "w").write(to_yaml(project))
    cmd = [os.path.join(ROOT, "bin/veritmo"), "--json", "--force"]
    if gpu is not None:
        cmd += ["--gpu", str(gpu)]
    cmd.append(path)
    # a still for the animated tests comes from the SDXL render of the same project, so nothing extra is needed
    engine = SERVICES.get(tid)
    if engine:
        stop_engine(engine)   # so what the service loads into the GPU is counted in this test and in no other
    probe = Probe(gpu)
    probe.start()
    phases, errors, ok = {}, [], False
    load_ms = None   # what sd says it spent loading models (None: no phase reported it)
    t0 = time.time()
    try:
        if engine:
            took = start_engine(engine)
            if took is None:
                raise RuntimeError(f"the {engine} service did not start")
            phases["service_start"] = took
        t0 = time.time()
        p = subprocess.Popen(cmd, cwd=ROOT, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True,
                             env={**os.environ, "PATH": os.path.join(ROOT, "bin") + os.pathsep + os.environ["PATH"]})
        deadline = t0 + timeout
        for line in p.stdout:
            try:
                ev = json.loads(line)
            except ValueError:
                continue
            t = ev.get("type")
            if t == "phase_end" and ev.get("scene_id") != "_global" or (t == "phase_end" and ev.get("phase") in ("concat", "mix", "export")):
                phases[ev["phase"]] = phases.get(ev["phase"], 0) + ev.get("duration_ms", 0) / 1000
                if "load_ms" in ev:
                    load_ms = (load_ms or 0) + ev["load_ms"]
            elif t == "error":
                errors.append(ev.get("message", ""))
            elif t == "pipeline_end":
                ok = True
            if time.time() > deadline:
                p.kill()
                errors.append(f"timed out after {timeout}s")
                break
        p.wait()
        ok = ok and p.returncode == 0 and not errors
    except RuntimeError as e:
        errors.append(str(e))
    finally:
        probe.stop = True
        probe.join()
        if engine:
            stop_engine(engine)
        shutil.rmtree(os.path.dirname(path), ignore_errors=True)
        for d in (f"out/{name}", f"out/{name}.mp4"):
            full = os.path.join(ROOT, d)
            if keep and os.path.exists(full):
                os.makedirs(os.path.join(keep, tid), exist_ok=True)
                shutil.move(full, os.path.join(keep, tid, os.path.basename(full)))
            else:
                shutil.rmtree(full, ignore_errors=True) if os.path.isdir(full) else (os.path.exists(full) and os.remove(full))
    result = {"title": title, "ok": ok, "seconds": round(time.time() - t0, 1), "phases": {k: round(v, 1) for k, v in phases.items()},
              "peak_vram_mb": max(0, probe.peak_vram), "vram_base_mb": probe.base_vram, "min_free_ram_mb": probe.min_ram,
              "error": errors[0][:300] if errors else ""}
    if load_ms is not None:   # absent, not zero, when sd said nothing (another version, or a phase that loads no model)
        result["load_seconds"] = round(load_ms / 1000, 1)
    return result


def missing(tid):
    m = [f for f in TESTS[tid][2] if not os.path.exists(os.path.join(ROOT, f))]
    if not m and tid in SERVICES and engine_state(SERVICES[tid]) is None:
        m.append(f"the server at {SERVER} (./serve.sh --local starts it)")
    return m


def compare(a, b):
    ra, rb = json.load(open(a)), json.load(open(b))
    print(f"{'':44}{ra['machine']['host'][:24]:>26}{rb['machine']['host'][:24]:>26}")
    for k in ("cpu", "ram_mb"):
        print(f"{k:44}{str(ra['machine'][k])[:24]:>26}{str(rb['machine'][k])[:24]:>26}")
    print(f"{'GPU':44}{', '.join(g['name'] for g in ra['machine']['gpus'])[:24]:>26}{', '.join(g['name'] for g in rb['machine']['gpus'])[:24]:>26}")
    for tid in TESTS:
        x, y = ra["results"].get(tid), rb["results"].get(tid)
        if not x and not y:
            continue
        def cell(r):
            if not r:
                return "-"
            return f"{r['seconds']:.0f}s {(r['peak_vram_mb']-r.get('vram_base_mb',0))/1024:.1f}G" if r["ok"] else "FAILED"
        ratio = f"  x{y['seconds']/x['seconds']:.1f}" if x and y and x["ok"] and y["ok"] else ""
        print(f"{tid:44}{cell(x):>26}{cell(y):>26}{ratio}")
        # the model load, where both machines have it (a file from before this field existed has none)
        if x and y and "load_seconds" in x and "load_seconds" in y:
            print(f"{'  of which model load (sd)':44}{x['load_seconds']:>25.1f}s{y['load_seconds']:>25.1f}s")


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--only", help="comma-separated test ids")
    ap.add_argument("--list", action="store_true")
    ap.add_argument("--gpu", type=int, help="GPU index to run on")
    ap.add_argument("--timeout", type=int, default=3600, help="seconds per test (default 3600)")
    ap.add_argument("--label", default="machine", help="name of this machine in the result and in its file name (default: machine); the host name is never recorded")
    ap.add_argument("--out", help="result file (default benchmarks/<label>-<date>.json)")
    ap.add_argument("--keep", action="store_true", help="keep the pictures, clips and songs in out/bench-results/<label>-<date>/<test>/ instead of deleting them")
    ap.add_argument("--compare", nargs=2, metavar=("A", "B"))
    a = ap.parse_args()
    if a.compare:
        return compare(*a.compare)
    if a.list:
        for tid, (title, _, _) in TESTS.items():
            m = missing(tid)
            print(f"{tid:12} {title}  [{'ready' if not m else 'missing: ' + ', '.join(m)}]")
        return
    if not os.access(os.path.join(ROOT, "bin/veritmo"), os.X_OK):
        sys.exit("bin/veritmo is not built: go build -o bin/veritmo ./cmd/veritmo")
    ids = a.only.split(",") if a.only else list(TESTS)
    bad = [i for i in ids if i not in TESTS]
    if bad:
        sys.exit("unknown test: " + ", ".join(bad))
    result = {"date": datetime.datetime.now().isoformat(timespec="seconds"), "machine": machine(a.label), "gpu_used": a.gpu, "results": {}}
    print(f"{result['machine']['host']}: {result['machine']['cpu']}, {result['machine']['ram_mb']//1024} GB RAM, "
          + ", ".join(f"{g['name']} {g['vram_mb']//1024} GB" for g in result["machine"]["gpus"]))
    keep = os.path.join(ROOT, "out", "bench-results", f"{result['machine']['host']}-{datetime.date.today()}") if a.keep else None
    for tid in ids:
        m = missing(tid)
        if m:
            print(f"- {tid}: skipped (missing {', '.join(m)})")
            continue
        print(f"- {tid}: {TESTS[tid][0]} ...", flush=True)
        r = run_test(tid, a.gpu, a.timeout, keep)
        result["results"][tid] = r
        print(f"  {'ok' if r['ok'] else 'FAILED'} {r['seconds']}s  phases {r['phases']}  peak VRAM {r['peak_vram_mb']} MB (+{r['peak_vram_mb']-r['vram_base_mb']} over the {r['vram_base_mb']} MB already in use)  min free RAM {r['min_free_ram_mb']} MB"
              + (f"\n  error: {r['error']}" if r["error"] else ""))
    out = a.out or os.path.join(ROOT, "benchmarks", f"{result['machine']['host']}-{datetime.date.today()}.json")
    os.makedirs(os.path.dirname(out), exist_ok=True)
    json.dump(result, open(out, "w"), indent=2)
    print("written", out)


if __name__ == "__main__":
    main()
