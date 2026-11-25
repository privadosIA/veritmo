package workspace

// environment.go implements the environment-status capability: binary
// presence/versions (with the fixing setup script named, never executed),
// the models/voices/LoRA inventory for the editor dropdowns, and the GPU
// list with free VRAM plus the Ollama VRAM-retention caveat (see
// specs/environment-status).

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"veritmo/internal/server/modelcat"

	"veritmo/internal/music/heartmula"
	"veritmo/internal/music/hyperframes"
	"veritmo/internal/platform/edition"
	"veritmo/internal/platform/sched"
	"veritmo/internal/platform/settings"
	"veritmo/internal/project/quality"
	"veritmo/internal/server/api"
	"veritmo/internal/video/motion"

	"veritmo/internal/speech/tts"
)

// BinaryStatus is one program the pipeline needs or may use: whether it is installed, where, which version and how to fix it when it is not.
type BinaryStatus struct {
	Name     string `json:"name"`
	Required bool   `json:"required"`
	Present  bool   `json:"present"`
	Path     string `json:"path,omitempty"`
	Version  string `json:"version,omitempty"`
	Fix      string `json:"fix,omitempty"`
	// Devices is what the program renders on, when it has several (Blender: the Cycles devices).
	Devices []string `json:"devices,omitempty"`
}

// AssetEntry is one model, voice or adapter file of the models folder, with its size.
type AssetEntry struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	// Speakers lists the speakers of a Piper voice that holds several (omitted for the usual single one).
	Speakers []tts.Speaker `json:"speakers,omitempty"`
}

// GPUStatus is one graphics card with its memory, as the environment report shows it.
type GPUStatus struct {
	Index      int    `json:"index"`
	Name       string `json:"name"`
	FreeVRAMMB int    `json:"free_vram_mb"`
	// TotalVRAMMB is the card's memory; what a model has to fit in, whatever the desktop holds right now.
	TotalVRAMMB int `json:"total_vram_mb,omitempty"`
	// OllamaMB is the memory an Ollama process holds on this GPU (0 when none).
	OllamaMB int `json:"ollama_mb,omitempty"`
	// SpeedFactor is how many times slower than the reference machine (an RTX 5060 Ti) this card has been in the
	// renders and animations of its runs (the median of the latest ones; below 1 is faster), once there are enough.
	SpeedFactor  float64 `json:"speed_factor,omitempty"`
	SpeedSamples int     `json:"speed_samples,omitempty"`
	// LowVRAM is set when less than LowVRAMMB is free, too little for a render.
	LowVRAM  bool     `json:"low_vram,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

// dictationStatus tells an interface whether to offer the microphone, and why not when it cannot.
type dictationStatus struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

func (h *Handler) dictationStatus() dictationStatus {
	if !h.host.WhisperInstalled() {
		return dictationStatus{Reason: "the Whisper model is not installed: run `bash scripts/get-whisper.sh`"}
	}
	if _, err := h.host.LookPath("whisper-cli"); err != nil {
		return dictationStatus{Reason: "whisper-cli is not installed: run `bash scripts/get-whisper.sh`"}
	}
	if _, err := h.host.LookPath("ffmpeg"); err != nil {
		return dictationStatus{Reason: "ffmpeg is not installed: it is needed to read the recording"}
	}
	return dictationStatus{Available: true}
}

// EnvironmentReport is the answer of GET /api/environment: the edition, the content mode, the programs, the cards and the models, voices and
// adapters installed on this machine.
type EnvironmentReport struct {
	// Edition is "private" or "public": fixed when the program is built (see internal/platform/edition).
	Edition string `json:"edition"`
	// ContentMode is which projects this server shows: "censored" or "strict" (see core_contentmode.go).
	ContentMode string         `json:"content_mode"`
	Binaries    []BinaryStatus `json:"binaries"`
	Models      []AssetEntry   `json:"models"`
	// Retouch lists the installed files that only retouch models use (FLUX.1 Kontext, Qwen-Image-Edit and their
	// text encoders and VAE): they are not image models, so they are not in Models.
	Retouch []AssetEntry `json:"retouch"`
	Voices  []AssetEntry `json:"voices"`
	Loras   []AssetEntry `json:"loras"`
	// Music lists the GGUF files of models/music (language models, text
	// encoder, synthesis models and VAE of the music engine).
	Music []AssetEntry `json:"music"`
	// MusicAdapters lists the installed adapters (LoRAs), one entry per file
	// or folder, with the total size of its files.
	MusicAdapters []AssetEntry `json:"music_adapters"`
	// Transcription lists the Whisper models of models/whisper (the lyrics reader).
	Transcription []AssetEntry `json:"transcription"`
	// Separation lists the weights of the voice separator of models/demucs (demucs.cpp, which gives the words of a song
	// their times): empty when it is not installed, which only makes the timing less exact.
	Separation []AssetEntry `json:"separation"`
	// Dictation says whether a spoken request can be transcribed here (Whisper, its model and ffmpeg).
	Dictation dictationStatus `json:"dictation"`
	GPUs      []GPUStatus     `json:"gpus"`
	// Ollama is the address of Ollama in use and where it comes from; no
	// request is made to it.
	Ollama   settings.Resolved `json:"ollama"`
	GPUError string            `json:"gpu_error,omitempty"`
	// Tokens says whether a download key is configured per service. Only
	// the fact: never the value, where it came from or a file path.
	Tokens TokenStatus `json:"tokens"`
	// TokenSources says where each configured key comes from: "env" (an
	// environment variable, which wins) or "file"; absent when none.
	TokenSources TokenSources `json:"token_sources"`
	// Disk is the space of the disk that holds the projects; absent when it cannot be read.
	Disk *diskReport `json:"disk,omitempty"`
	// ModelsDir is the models directory with its state and the space of its disk; absent without one.
	ModelsDir map[string]any `json:"models_dir,omitempty"`
	// RAM is the machine's memory; absent when it cannot be read.
	RAM *ramReport `json:"ram,omitempty"`
	// HeartMula says whether the optional HeartMuLa music engine can be used (a container here, or a service on
	// another machine) and, when not, why and how to fix it. Not being installed never makes the environment not ready.
	HeartMula *heartmula.Availability `json:"heartmula,omitempty"`
	// DiffRhythm2 says the same for the optional DiffRhythm2 music engine.
	DiffRhythm2 *heartmula.Availability `json:"diffrhythm2,omitempty"`
}

// ramReport is the total and available memory, in bytes (used = total - available).
type ramReport struct {
	TotalBytes     int64 `json:"total_bytes"`
	AvailableBytes int64 `json:"available_bytes"`
}

// TokenStatus reports, per service, whether the server has a non-empty key.
type TokenStatus struct {
	HuggingFace bool `json:"huggingface"`
	Civitai     bool `json:"civitai"`
}

// TokenSources reports, per service, where the key in use comes from.
type TokenSources struct {
	HuggingFace string `json:"huggingface,omitempty"`
	Civitai     string `json:"civitai,omitempty"`
}

// binarySpec describes one external binary the report covers.
type binarySpec struct {
	name        string
	required    bool
	versionArgs []string
	fix         string
}

var binarySpecs = []binarySpec{
	{"piper", true, []string{"--version"}, "bash scripts/get-piper.sh"},
	{"sd", true, []string{"--version"}, "bash scripts/build-sd.sh"},
	{"ffmpeg", true, []string{"-version"}, "install ffmpeg with your system package manager"},
	{"ffprobe", true, []string{"-version"}, "install ffmpeg (ffprobe ships with it) with your system package manager"},
	// The music engine is optional: video and images projects do not need it.
	{"ace-lm", false, []string{"--help"}, "bash scripts/build-acestep.sh"},
	{"ace-synth", false, []string{"--help"}, "bash scripts/build-acestep.sh"},
	{"ace-server", false, []string{"--help"}, "bash scripts/build-acestep.sh"},
	// Only needed to add a source from a YouTube URL.
	{"yt-dlp", false, []string{"--version"}, "bash scripts/get-ytdlp.sh"},
	// Only needed to read the lyrics of a song (its model goes to models/whisper).
	{"whisper-cli", false, []string{"--help"}, "bash scripts/get-whisper.sh"},
	// Only needed to time the words of a song from its separated voice (its weights go to models/demucs). With no
	// arguments it prints its usage and exits with an error, so its version is reported as unknown.
	{"demucs", false, nil, "bash scripts/get-demucs.sh --build"},
	{"nvidia-smi", false, []string{"--version"}, "install the NVIDIA driver (optional: only the --gpu VRAM check uses it)"},
}

// GET /api/environment?lora_dir=<relative path>
func (h *Handler) handleEnvironment(w http.ResponseWriter, r *http.Request) {
	var dsk *diskReport
	if d, ok := disk(h.cfg.ProjectsDir); ok {
		dsk = &d
	}
	var ram *ramReport
	if total, avail, err := sched.MemInfo(); err == nil {
		ram = &ramReport{TotalBytes: total, AvailableBytes: avail}
	}
	tokens := h.host.Tokens()
	models, retouch := splitRetouch(h.host.Catalog(), ListAssets("models"))
	rep := EnvironmentReport{
		Edition:       edition.Name,
		ContentMode:   h.host.ContentMode(),
		Ollama:        settings.Ollama(),
		Disk:          dsk,
		ModelsDir:     h.modelsReport(),
		RAM:           ram,
		Binaries:      h.binaryStatuses(),
		Models:        models,
		Retouch:       retouch,
		Voices:        ListAssets("voices"),
		Loras:         []AssetEntry{},
		Music:         ListAssets(filepath.Join("models", "music")),
		MusicAdapters: ListAdapters(filepath.Join("models", "music", "adapters")),
		Transcription: ListAssets(h.host.WhisperDir()),
		Separation:    ListAssets(filepath.Join("models", "demucs")),
		Dictation:     h.dictationStatus(),
		// The same lookups a download uses, read now: a Civitai key added
		// without restarting shows up on the next report.
		Tokens: TokenStatus{
			HuggingFace: tokens.HuggingFace,
			Civitai:     tokens.Civitai,
		},
		TokenSources: TokenSources{
			HuggingFace: tokens.HuggingFaceSource,
			Civitai:     tokens.CivitaiSource,
		},
	}

	engines := h.host.Engines(r.Context())
	if a, ok := engines["diffrhythm2"]; ok {
		rep.DiffRhythm2 = &a
	}
	if a, ok := engines["heartmula"]; ok {
		rep.HeartMula = &a
	}

	loraDir := r.URL.Query().Get("lora_dir")
	if loraDir == "" {
		if api.DirExists(filepath.Join("models", "loras")) {
			loraDir = filepath.Join("models", "loras")
		}
	}
	if loraDir != "" {
		dir := filepath.FromSlash(loraDir)
		if !filepath.IsLocal(dir) {
			api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "lora_dir must be a relative path inside the server root")
			return
		}
		rep.Loras = ListAssets(dir)
	}

	gpus, err := h.QueryGPUs()
	if err != nil {
		rep.GPUError = err.Error()
		rep.GPUs = []GPUStatus{}
	} else {
		rep.GPUs = gpus
		if !rep.Ollama.Remote { // a process on another machine holds none of this machine's memory
			h.attachOllamaCaveat(rep.GPUs)
		}
		flagLowVRAM(rep.GPUs)
		for i := range rep.GPUs {
			if f, n, ok := h.rm.hist.speedOf(rep.GPUs[i].Name); ok {
				rep.GPUs[i].SpeedFactor, rep.GPUs[i].SpeedSamples = f, n
			}
		}
	}

	api.WriteJSON(w, http.StatusOK, rep)
}

func (h *Handler) binaryStatuses() []BinaryStatus {
	out := make([]BinaryStatus, 0, len(binarySpecs))
	for _, spec := range binarySpecs {
		st := BinaryStatus{Name: spec.name, Required: spec.required}
		if path, err := h.host.LookPath(spec.name); err == nil {
			st.Present = true
			st.Path = path
			st.Version = h.binaryVersion(spec)
		} else {
			st.Fix = spec.fix
		}
		out = append(out, st)
	}
	// The browser that renders motion graphics is found by its own rules (a saved path, then several names).
	br := BinaryStatus{Name: "chrome", Required: false}
	if b, err := motion.Find(settings.Browser()); err == nil {
		br.Present, br.Path, br.Version = true, b.Path, b.Version
	} else {
		br.Fix = motion.InstallHint
	}
	return append(out, br, h.host.BlenderStatus(), gsapStatus(), hyperframesLibsStatus())
}

// gsapStatus is the GSAP core as an optional item of the report: the parts and scenes that animate with a timeline
// embed the file in their page (musicmotion.GSAPFile is the same path), and only they need it.
func gsapStatus() BinaryStatus {
	path := filepath.Join("third_party", "gsap", "gsap.min.js")
	st := BinaryStatus{Name: "gsap", Required: false}
	if fi, err := os.Stat(path); err == nil && fi.Mode().IsRegular() {
		st.Present, st.Path = true, path
	} else {
		st.Fix = "bash scripts/get-gsap.sh"
	}
	return st
}

// hyperframesLibsStatus is the set of libraries the HyperFrames blocks load from a CDN, kept locally so that a block is
// drawn without the network (optional: only the blocks need them).
func hyperframesLibsStatus() BinaryStatus {
	st := BinaryStatus{Name: "hyperframes-libs", Required: false}
	if missing := hyperframes.Missing(hyperframes.LibsDir); len(missing) == 0 {
		st.Present, st.Path = true, hyperframes.LibsDir
	} else {
		st.Fix = "bash scripts/get-hyperframes-libs.sh"
	}
	return st
}

// binaryVersion asks the binary for its version, best effort: a binary
// that refuses the flag simply reports no version.
func (h *Handler) binaryVersion(spec binarySpec) string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := h.host.EnvRunner().Run(ctx, spec.name, spec.versionArgs)
	if err != nil || len(out) == 0 {
		return ""
	}
	line := strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)[0]
	const max = 120
	if len(line) > max {
		line = line[:max]
	}
	return line
}

// ListAssets returns the regular files of a directory with name and size;
// a missing or empty directory is an empty list, never an error.
func ListAssets(dir string) []AssetEntry {
	out := []AssetEntry{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		entry := AssetEntry{Name: e.Name(), Size: info.Size()}
		if strings.HasSuffix(e.Name(), ".onnx") {
			entry.Speakers = tts.Speakers(filepath.Join(dir, e.Name()))
		}
		out = append(out, entry)
	}
	return out
}

// QueryGPUs reads index, name and free VRAM per GPU through nvidia-smi,
// freshly on every call.
func (h *Handler) QueryGPUs() ([]GPUStatus, error) {
	if _, err := h.host.LookPath("nvidia-smi"); err != nil {
		return nil, fmt.Errorf("nvidia-smi not available: GPU information cannot be read")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := h.host.EnvRunner().Run(ctx, "nvidia-smi",
		[]string{"--query-gpu=index,name,memory.free,memory.total", "--format=csv,noheader,nounits"})
	if err != nil {
		return nil, fmt.Errorf("nvidia-smi failed: %w", err)
	}
	gpus := []GPUStatus{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.SplitN(line, ",", 4)
		if len(parts) < 3 {
			continue
		}
		idx, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
		free, err2 := strconv.Atoi(strings.TrimSpace(parts[2]))
		if err1 != nil || err2 != nil {
			continue
		}
		total := 0
		if len(parts) == 4 {
			total, _ = strconv.Atoi(strings.TrimSpace(parts[3]))
		}
		gpus = append(gpus, GPUStatus{Index: idx, Name: strings.TrimSpace(parts[1]), FreeVRAMMB: free, TotalVRAMMB: total})
	}
	return gpus, nil
}

// ollamaCaveat is the warning attached to a GPU where an Ollama process
// holds memory (see docs/INSTALL.md: OLLAMA_KEEP_ALIVE retention).
const ollamaCaveat = "Ollama keeps a model in this GPU's VRAM after its last use (OLLAMA_KEEP_ALIVE, 5 min by default) and can leave the sd render without memory. Free it (POST /api/environment/ollama/unload), pin Ollama to the desktop GPU or lower OLLAMA_KEEP_ALIVE."

// lowVRAMWarning is attached to a GPU with too little free memory for a render.
const lowVRAMWarning = "Little free VRAM on this GPU: a render may run out of memory."

// LowVRAMMB is the free VRAM under which a GPU is flagged.
const LowVRAMMB = 6144

// attachOllamaCaveat marks the GPUs where an Ollama compute process is
// currently holding memory. Best effort: any query failure just leaves
// the warnings absent.
func (h *Handler) attachOllamaCaveat(gpus []GPUStatus) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	uuidOut, err := h.host.EnvRunner().Run(ctx, "nvidia-smi",
		[]string{"--query-gpu=index,uuid", "--format=csv,noheader,nounits"})
	if err != nil {
		return
	}
	uuidToIndex := map[string]int{}
	for _, line := range strings.Split(strings.TrimSpace(string(uuidOut)), "\n") {
		parts := strings.SplitN(line, ",", 2)
		if len(parts) != 2 {
			continue
		}
		if idx, err := strconv.Atoi(strings.TrimSpace(parts[0])); err == nil {
			uuidToIndex[strings.TrimSpace(parts[1])] = idx
		}
	}

	appsOut, err := h.host.EnvRunner().Run(ctx, "nvidia-smi",
		[]string{"--query-compute-apps=gpu_uuid,process_name,used_memory", "--format=csv,noheader,nounits"})
	if err != nil {
		return
	}
	for _, line := range strings.Split(strings.TrimSpace(string(appsOut)), "\n") {
		parts := strings.SplitN(line, ",", 3)
		if len(parts) < 2 {
			continue
		}
		proc := strings.ToLower(strings.TrimSpace(parts[1]))
		if !strings.Contains(proc, "ollama") {
			continue
		}
		idx, ok := uuidToIndex[strings.TrimSpace(parts[0])]
		if !ok {
			continue
		}
		used := 0
		if len(parts) == 3 {
			used, _ = strconv.Atoi(strings.TrimSpace(parts[2]))
		}
		for i := range gpus {
			if gpus[i].Index == idx {
				if gpus[i].OllamaMB == 0 {
					gpus[i].Warnings = append(gpus[i].Warnings, ollamaCaveat)
				}
				gpus[i].OllamaMB += max(used, 1)
			}
		}
	}
}

// flagLowVRAM marks the GPUs with little free memory.
func flagLowVRAM(gpus []GPUStatus) {
	for i := range gpus {
		if gpus[i].FreeVRAMMB < LowVRAMMB {
			gpus[i].LowVRAM = true
			gpus[i].Warnings = append(gpus[i].Warnings, lowVRAMWarning)
		}
	}
}

// POST /api/environment/ollama/unload asks Ollama to drop every model it holds,
// freeing the VRAM it retains after its last use.
func (h *Handler) handleOllamaUnload(w http.ResponseWriter, r *http.Request) {
	names, err := quality.UnloadAll(r.Context(), settings.OllamaURL())
	if err != nil {
		api.WriteError(w, http.StatusBadGateway, api.CodeUnavailable, err.Error())
		return
	}
	if names == nil {
		names = []string{}
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"unloaded": names})
}

// ListAdapters returns the installed music adapters of dir, one per unit: a
// .safetensors file, or a folder holding adapter_model.safetensors (its size
// is the total of its files). Anything else, and leftovers of downloads, are
// not adapters. A missing directory is an empty list.
func ListAdapters(dir string) []AssetEntry {
	out := []AssetEntry{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".partial") {
			continue
		}
		path := filepath.Join(dir, name)
		if e.IsDir() {
			if fi, err := os.Stat(filepath.Join(path, "adapter_model.safetensors")); err != nil || !fi.Mode().IsRegular() {
				continue
			}
			var total int64
			files, _ := os.ReadDir(path)
			for _, f := range files {
				if info, err := f.Info(); err == nil && info.Mode().IsRegular() && !strings.HasSuffix(f.Name(), ".partial") {
					total += info.Size()
				}
			}
			out = append(out, AssetEntry{Name: name, Size: total})
			continue
		}
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() || !strings.HasSuffix(strings.ToLower(name), ".safetensors") {
			continue
		}
		out = append(out, AssetEntry{Name: name, Size: info.Size()})
	}
	return out
}

// retouchOnlyFiles returns the names of the catalog files that belong to retouch models and to no other entry:
// a file an image model shares (a VAE, a text encoder) stays among the image models.
func retouchOnlyFiles(cat []modelcat.Entry) map[string]bool {
	mine := map[string]bool{}
	others := map[string]bool{}
	for _, e := range cat {
		for _, f := range e.Files {
			if e.Edit {
				mine[f.Name] = true
			} else {
				others[f.Name] = true
			}
		}
	}
	for n := range others {
		delete(mine, n)
	}
	return mine
}

// splitRetouch separates the files of the retouch models from the image models' ones, in the order they came.
func splitRetouch(cat []modelcat.Entry, all []AssetEntry) (models, retouch []AssetEntry) {
	edit := retouchOnlyFiles(cat)
	models, retouch = []AssetEntry{}, []AssetEntry{}
	for _, a := range all {
		if edit[a.Name] {
			retouch = append(retouch, a)
		} else {
			models = append(models, a)
		}
	}
	return models, retouch
}
