package workspace

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/platform/runner"
)

func getEnvironment(t *testing.T, s *testServer, query string) EnvironmentReport {
	t.Helper()
	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/environment"+query, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("environment: status %d: %s", rec.Code, rec.Body.String())
	}
	var rep EnvironmentReport
	if err := json.Unmarshal(rec.Body.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	return rep
}

func binByName(t *testing.T, rep EnvironmentReport, name string) BinaryStatus {
	t.Helper()
	for _, b := range rep.Binaries {
		if b.Name == name {
			return b
		}
	}
	t.Fatalf("binary %s missing from report", name)
	return BinaryStatus{}
}

func TestEnvironment_BinariesPresentMissingAndOptional(t *testing.T) {
	s, _ := newProjectsServer(t)
	// Fake PATH: everything except sd and nvidia-smi exists.
	s.look = func(name string) (string, error) {
		switch name {
		case "sd", "nvidia-smi":
			return "", fmt.Errorf("not found")
		default:
			return "/fake/bin/" + name, nil
		}
	}
	fr := runner.NewFakeRunner()
	fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
		if inv.Name == "ffmpeg" || inv.Name == "ffprobe" {
			return runner.ScriptedResult{Stdout: []byte("ffmpeg version 7.1 Copyright\nbuilt with gcc")}
		}
		if inv.Name == "piper" {
			return runner.ScriptedResult{Stdout: []byte("1.2.0\n")}
		}
		return runner.ScriptedResult{Err: fmt.Errorf("unexpected call %s", inv.Name)}
	})
	s.envRunner = fr

	rep := getEnvironment(t, s, "")

	sd := binByName(t, rep, "sd")
	if sd.Present || !sd.Required {
		t.Errorf("sd should be missing+required: %+v", sd)
	}
	if !strings.Contains(sd.Fix, "scripts/build-sd.sh") {
		t.Errorf("sd fix should name build-sd.sh: %q", sd.Fix)
	}
	piper := binByName(t, rep, "piper")
	if !piper.Present || piper.Version != "1.2.0" {
		t.Errorf("piper: %+v", piper)
	}
	ffmpeg := binByName(t, rep, "ffmpeg")
	if !strings.HasPrefix(ffmpeg.Version, "ffmpeg version 7.1") {
		t.Errorf("ffmpeg version = %q", ffmpeg.Version)
	}
	smi := binByName(t, rep, "nvidia-smi")
	if smi.Present || smi.Required {
		t.Errorf("nvidia-smi should be missing+optional: %+v", smi)
	}
	if rep.GPUError == "" || len(rep.GPUs) != 0 {
		t.Errorf("GPU info should be reported unavailable: %+v", rep.GPUs)
	}
}

func TestEnvironment_InventoryAndEmptyDirs(t *testing.T) {
	s, _ := newProjectsServer(t) // creates models/m.safetensors, voices/v.onnx(.json)
	s.look = func(name string) (string, error) { return "", fmt.Errorf("nope") }
	s.envRunner = runner.NewFakeRunner()

	// A LoRA dir with one file.
	if err := os.MkdirAll(filepath.Join("models", "loras"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("models", "loras", "style.safetensors"), []byte("1234"), 0o644); err != nil {
		t.Fatal(err)
	}

	rep := getEnvironment(t, s, "")
	if len(rep.Models) != 1 || rep.Models[0].Name != "m.safetensors" || rep.Models[0].Size != 1 {
		t.Errorf("models inventory: %+v", rep.Models)
	}
	if len(rep.Voices) != 2 || rep.Voices[0].Name != "v.onnx" || rep.Voices[1].Name != "v.onnx.json" {
		t.Errorf("voices inventory: %+v", rep.Voices)
	}
	if len(rep.Loras) != 1 || rep.Loras[0].Name != "style.safetensors" || rep.Loras[0].Size != 4 {
		t.Errorf("loras inventory: %+v", rep.Loras)
	}

	// Explicit lora_dir, empty dir => empty list, not an error.
	if err := os.MkdirAll("empty-loras", 0o755); err != nil {
		t.Fatal(err)
	}
	rep = getEnvironment(t, s, "?lora_dir=empty-loras")
	if len(rep.Loras) != 0 {
		t.Errorf("empty lora dir should list nothing: %+v", rep.Loras)
	}

	// Escaping lora_dir rejected.
	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/environment?lora_dir=../outside", nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("escaping lora_dir: status %d, want 400", rec.Code)
	}
}

func TestEnvironment_GPUsAndOllamaCaveat(t *testing.T) {
	s, _ := newProjectsServer(t)
	s.look = func(name string) (string, error) { return "/fake/" + name, nil }

	canned := map[string]string{
		"--query-gpu=index,name,memory.free,memory.total":        "0, NVIDIA GeForce RTX 4070, 3123, 12282\n1, NVIDIA GeForce RTX 5060 Ti, 14900, 16311\n",
		"--query-gpu=index,uuid":                                 "0, GPU-aaa\n1, GPU-bbb\n",
		"--query-compute-apps=gpu_uuid,process_name,used_memory": "GPU-bbb, /usr/bin/ollama, 4096\n",
	}
	fr := runner.NewFakeRunner()
	fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
		if inv.Name != "nvidia-smi" {
			return runner.ScriptedResult{Stdout: []byte("v1\n")}
		}
		return runner.ScriptedResult{Stdout: []byte(canned[inv.Args[0]])}
	})
	s.envRunner = fr

	rep := getEnvironment(t, s, "")
	if len(rep.GPUs) != 2 {
		t.Fatalf("GPUs = %+v", rep.GPUs)
	}
	if rep.GPUs[0].Index != 0 || rep.GPUs[0].Name != "NVIDIA GeForce RTX 4070" || rep.GPUs[0].FreeVRAMMB != 3123 {
		t.Errorf("gpu0: %+v", rep.GPUs[0])
	}
	if rep.GPUs[1].FreeVRAMMB != 14900 || rep.GPUs[1].TotalVRAMMB != 16311 || rep.GPUs[0].TotalVRAMMB != 12282 {
		t.Errorf("gpu1: %+v (the total memory is reported next to the free one)", rep.GPUs[1])
	}
	if !rep.GPUs[0].LowVRAM || len(rep.GPUs[0].Warnings) != 1 || rep.GPUs[0].OllamaMB != 0 {
		t.Errorf("gpu0 has 3 GB free: only the low-VRAM warning: %+v", rep.GPUs[0])
	}
	if rep.GPUs[1].LowVRAM || rep.GPUs[1].OllamaMB != 4096 {
		t.Errorf("gpu1: %+v", rep.GPUs[1])
	}
	if len(rep.GPUs[1].Warnings) != 1 || !strings.Contains(rep.GPUs[1].Warnings[0], "OLLAMA_KEEP_ALIVE") {
		t.Errorf("gpu1 should carry the Ollama caveat: %+v", rep.GPUs[1].Warnings)
	}

	// Without an Ollama process, no warning appears.
	canned["--query-compute-apps=gpu_uuid,process_name,used_memory"] = ""
	rep = getEnvironment(t, s, "")
	if len(rep.GPUs[1].Warnings) != 0 {
		t.Errorf("no ollama process but warning present: %+v", rep.GPUs[1].Warnings)
	}
}

func TestEnvironment_ReportsTheMusicServerProgramAsOptional(t *testing.T) {
	s, _ := newProjectsServer(t)
	s.look = func(name string) (string, error) {
		if name == "ace-server" {
			return "", fmt.Errorf("not found")
		}
		return "/fake/bin/" + name, nil
	}
	fr := runner.NewFakeRunner()
	fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult { return runner.ScriptedResult{Stdout: []byte("ok")} })
	s.envRunner = fr
	b := binByName(t, getEnvironment(t, s, ""), "ace-server")
	if b.Present || b.Required || !strings.Contains(b.Fix, "build-acestep.sh") {
		t.Fatalf("%+v", b)
	}
}

func TestEnvironment_ReportsYtDlpAsOptionalWithItsInstaller(t *testing.T) {
	s, _ := newProjectsServer(t)
	s.look = func(name string) (string, error) {
		if name == "yt-dlp" {
			return "", fmt.Errorf("not found")
		}
		return "/fake/bin/" + name, nil
	}
	fr := runner.NewFakeRunner()
	fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
		return runner.ScriptedResult{Stdout: []byte("2026.08.19")}
	})
	s.envRunner = fr
	rep := getEnvironment(t, s, "")
	b := binByName(t, rep, "yt-dlp")
	if b.Present || b.Required || !strings.Contains(b.Fix, "get-ytdlp.sh") {
		t.Fatalf("absent: %+v", b)
	}
	s.look = func(name string) (string, error) { return "/fake/bin/" + name, nil }
	b = binByName(t, getEnvironment(t, s, ""), "yt-dlp")
	if !b.Present || b.Version != "2026.08.19" || b.Path != "/fake/bin/yt-dlp" {
		t.Fatalf("present: %+v", b)
	}
}

func TestOllamaUnload_UnloadsWhatIsLoaded(t *testing.T) {
	var unloaded []string
	ol := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/ps":
			w.Write([]byte(`{"models":[{"name":"qwen3.8:27b"}]}`))
		case "/api/generate":
			var b struct {
				Model     string `json:"model"`
				KeepAlive int    `json:"keep_alive"`
			}
			json.NewDecoder(r.Body).Decode(&b)
			if b.KeepAlive == 0 {
				unloaded = append(unloaded, b.Model)
			}
			w.Write([]byte(`{}`))
		}
	}))
	defer ol.Close()
	t.Setenv("OLLAMA_HOST", ol.URL)
	s, _ := newProjectsServer(t)
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/environment/ollama/unload", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "qwen3.8:27b") || len(unloaded) != 1 {
		t.Fatalf("%d %s %v", rec.Code, rec.Body.String(), unloaded)
	}
}

func TestEnvironment_ReportsTheMachineMemory(t *testing.T) {
	s, _ := newProjectsServer(t)
	s.look = func(name string) (string, error) { return "/fake/" + name, nil }
	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/environment", nil)
	var rep struct {
		RAM *struct {
			Total int64 `json:"total_bytes"`
			Avail int64 `json:"available_bytes"`
		} `json:"ram"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat("/proc/meminfo"); err != nil {
		t.Skip("no /proc/meminfo")
	}
	if rep.RAM == nil || rep.RAM.Total <= 0 || rep.RAM.Avail <= 0 || rep.RAM.Avail > rep.RAM.Total {
		t.Fatalf("%+v", rep.RAM)
	}
}

func TestEnvironment_ReportsWhisperAsOptionalWithItsModel(t *testing.T) {
	s, _ := newProjectsServer(t)
	dir := t.TempDir()
	s.whisperDir = dir
	s.look = func(name string) (string, error) {
		if name == "whisper-cli" {
			return "", fmt.Errorf("not found")
		}
		return "/fake/bin/" + name, nil
	}
	s.envRunner = runner.NewFakeRunner()
	rep := getEnvironment(t, s, "")
	b := binByName(t, rep, "whisper-cli")
	if b.Present || b.Required || !strings.Contains(b.Fix, "get-whisper.sh") || len(rep.Transcription) != 0 {
		t.Fatalf("absent: %+v %v", b, rep.Transcription)
	}
	if err := os.WriteFile(filepath.Join(dir, "ggml-large-v3-turbo-q5_0.bin"), []byte("model"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.look = func(name string) (string, error) { return "/usr/bin/" + name, nil }
	rep = getEnvironment(t, s, "")
	if b = binByName(t, rep, "whisper-cli"); !b.Present || b.Path != "/usr/bin/whisper-cli" {
		t.Fatalf("present: %+v", b)
	}
	if len(rep.Transcription) != 1 || rep.Transcription[0].Name != "ggml-large-v3-turbo-q5_0.bin" {
		t.Fatalf("model: %+v", rep.Transcription)
	}
}

func TestEnvironment_ReportsDemucsAsOptionalWithItsWeights(t *testing.T) {
	s, _ := newProjectsServer(t)
	t.Chdir(t.TempDir())
	s.look = func(name string) (string, error) {
		if name == "demucs" {
			return "", fmt.Errorf("not found")
		}
		return "/fake/bin/" + name, nil
	}
	s.envRunner = runner.NewFakeRunner()
	rep := getEnvironment(t, s, "")
	b := binByName(t, rep, "demucs")
	if b.Present || b.Required || !strings.Contains(b.Fix, "get-demucs.sh") || len(rep.Separation) != 0 {
		t.Fatalf("absent: %+v %v", b, rep.Separation)
	}
	if err := os.MkdirAll(filepath.Join("models", "demucs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("models", "demucs", "ggml-model-htdemucs-4s-f16.bin"), []byte("weights"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.look = func(name string) (string, error) { return "/usr/bin/" + name, nil }
	rep = getEnvironment(t, s, "")
	if b = binByName(t, rep, "demucs"); !b.Present || b.Path != "/usr/bin/demucs" || b.Version != "" {
		t.Fatalf("present: %+v (a program that prints a usage and fails has an unknown version)", b)
	}
	if len(rep.Separation) != 1 || rep.Separation[0].Name != "ggml-model-htdemucs-4s-f16.bin" {
		t.Fatalf("weights: %+v", rep.Separation)
	}
}

func TestEnvironment_ReportsGSAPAsOptionalWithItsFixingCommand(t *testing.T) {
	s, _ := newProjectsServer(t)
	t.Chdir(t.TempDir())
	s.look = func(name string) (string, error) { return "/fake/bin/" + name, nil }
	s.envRunner = runner.NewFakeRunner()
	rep := getEnvironment(t, s, "")
	b := binByName(t, rep, "gsap")
	if b.Present || b.Required || b.Fix != "bash scripts/get-gsap.sh" {
		t.Fatalf("absent: %+v", b)
	}
	if err := os.MkdirAll(filepath.Join("third_party", "gsap"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("third_party", "gsap", "gsap.min.js"), []byte("/*gsap*/"), 0o644); err != nil {
		t.Fatal(err)
	}
	rep = getEnvironment(t, s, "")
	if b = binByName(t, rep, "gsap"); !b.Present || b.Required || b.Fix != "" {
		t.Fatalf("present: %+v", b)
	}
}
