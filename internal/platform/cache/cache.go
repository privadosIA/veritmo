// Package cache provides deterministic per-scene artifact caching for the
// multi-phase generation pipeline (tts → render → clip).
//
// Design invariants (see design.md / specs/cache.md):
//
//   - Artifact names are stable: tmp/scene_<id>.{wav,png,mp4}
//   - Audio and image hashes are independent; changing one does not
//     invalidate the other.
//   - Decide() reports per-phase hit/miss so the orchestrator can skip
//     work and emit "skipped" for cached phases.
//     --only <id> restricts processing to a single scene; all other scenes
//     are treated as fully cached for the final concat.
package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

// ---------------------------------------------------------------------------
// Phase
// ---------------------------------------------------------------------------

// Phase identifies a generation stage.
type Phase string

// The stages that are cached: the narration, the picture and the clip.
const (
	PhaseTTS    Phase = "tts"
	PhaseRender Phase = "render"
	PhaseClip   Phase = "clip"

	// PhaseAnimate runs only for animated scenes, between render and clip.
	// It is deliberately not in AllPhases: a Decision for a non-animated
	// scene never carries it, so those decisions stay exactly as before.
	PhaseAnimate Phase = "animate"
)

// AllPhases lists every phase in execution order (animate excluded, see
// PhaseAnimate).
var AllPhases = []Phase{PhaseTTS, PhaseRender, PhaseClip}

// ---------------------------------------------------------------------------
// Parameter structs (single source of truth for hash inputs)
// ---------------------------------------------------------------------------

// AudioParams are the exact inputs that determine TTS output.
// The audio hash is computed over these three fields only.
type AudioParams struct {
	Text  string  `json:"tts_text"`
	Speed float64 `json:"speed"`
	Voice string  `json:"voice"`
	// Speaker is the speaker of a multi-speaker voice; 0 (the usual voice) is left out of the hash so
	// existing caches stay valid.
	Speaker int `json:"speaker,omitempty"`
}

// ImageParams are the exact inputs that determine the rendered image.
// They mirror the sd CLI flags (model, prompt, negative_prompt, seed,
// resolution, sampler, steps) centralised in one place per design.md,
// plus the images-mode additions: the global subject prompt, the global
// negative-prompt contribution, the reserved LoRA string (design D4), and
// the 0-based variant index.
type ImageParams struct {
	Prompt               string `json:"prompt"`
	NegativePrompt       string `json:"negative_prompt"`
	Seed                 int64  `json:"seed"`
	Model                string `json:"model"`
	Resolution           string `json:"resolution"`
	Sampler              string `json:"sampler"`
	Steps                int    `json:"steps"`
	SubjectPrompt        string `json:"subject_prompt,omitempty"`
	GlobalNegativePrompt string `json:"global_negative_prompt,omitempty"`
	Lora                 string `json:"lora,omitempty"`
	VariantIndex         int    `json:"variant_index,omitempty"`
	// Model set (diffusion model + LLM + VAE); empty for a single checkpoint.
	DiffusionModel string  `json:"diffusion_model,omitempty"`
	LLM            string  `json:"llm,omitempty"`
	VAE            string  `json:"vae,omitempty"`
	CfgScale       float64 `json:"cfg_scale,omitempty"`
	// An editing model set (FLUX.1 Kontext): its two text encoders, and that the init picture is a reference.
	EditClipL string `json:"edit_clip_l,omitempty"`
	EditT5XXL string `json:"edit_t5xxl,omitempty"`
	// The Qwen family of editing models takes an LLM (above) with its vision projector instead.
	EditLLMVision string `json:"edit_llm_vision,omitempty"`
	Edit          bool   `json:"edit,omitempty"`
	// SourceImage is the SHA-256 of a scene's own picture. When set, the
	// image comes from that file and none of the generation inputs apply.
	SourceImage string `json:"source_image,omitempty"`
	// InitImage is the SHA-256 of the picture an img2img generation starts
	// from, and InitStrength the strength; both only when it is set.
	InitImage    string  `json:"init_image,omitempty"`
	InitStrength float64 `json:"init_strength,omitempty"`
}

// ---------------------------------------------------------------------------
// Hash functions
// ---------------------------------------------------------------------------

// AudioHash returns a deterministic SHA-256 hex digest over the TTS inputs.
func AudioHash(p AudioParams) string {
	h := sha256.New()
	fmt.Fprintf(h, "tts_text=%s\x00", p.Text)
	fmt.Fprintf(h, "speed=%v\x00", p.Speed)
	fmt.Fprintf(h, "voice=%s\x00", p.Voice)
	if p.Speaker > 0 {
		fmt.Fprintf(h, "speaker=%d\x00", p.Speaker)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ImageHash returns a deterministic SHA-256 hex digest over the image
// generation inputs.
//
// The four images-mode fields are folded in only when set, so a params value
// with all of them at their zero value hashes byte-identically to the
// pre-images-mode implementation: existing cache manifests stay valid and no
// cached PNG is invalidated by the schema extension alone.
func ImageHash(p ImageParams) string {
	h := sha256.New()
	fmt.Fprintf(h, "prompt=%s\x00", p.Prompt)
	fmt.Fprintf(h, "negative_prompt=%s\x00", p.NegativePrompt)
	fmt.Fprintf(h, "seed=%d\x00", p.Seed)
	fmt.Fprintf(h, "model=%s\x00", p.Model)
	fmt.Fprintf(h, "resolution=%s\x00", p.Resolution)
	fmt.Fprintf(h, "sampler=%s\x00", p.Sampler)
	fmt.Fprintf(h, "steps=%d\x00", p.Steps)
	if p.SubjectPrompt != "" {
		fmt.Fprintf(h, "subject_prompt=%s\x00", p.SubjectPrompt)
	}
	if p.GlobalNegativePrompt != "" {
		fmt.Fprintf(h, "global_negative_prompt=%s\x00", p.GlobalNegativePrompt)
	}
	if p.Lora != "" {
		fmt.Fprintf(h, "lora=%s\x00", p.Lora)
	}
	if p.VariantIndex != 0 {
		fmt.Fprintf(h, "variant_index=%d\x00", p.VariantIndex)
	}
	if p.DiffusionModel != "" || p.LLM != "" || p.VAE != "" {
		fmt.Fprintf(h, "diffusion_model=%s\x00llm=%s\x00vae=%s\x00cfg_scale=%v\x00", p.DiffusionModel, p.LLM, p.VAE, p.CfgScale)
	}
	if p.Edit {
		fmt.Fprintf(h, "edit=1\x00clip_l=%s\x00t5xxl=%s\x00", p.EditClipL, p.EditT5XXL) // only when set: keeps the hashes of the rest
		if p.EditLLMVision != "" {
			fmt.Fprintf(h, "llm_vision=%s\x00", p.EditLLMVision)
		}
	}
	if p.SourceImage != "" {
		fmt.Fprintf(h, "source_image=%s\x00", p.SourceImage)
	}
	if p.InitImage != "" {
		fmt.Fprintf(h, "init_image=%s\x00init_strength=%v\x00", p.InitImage, p.InitStrength)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ClipParams are the exact inputs that change clip bytes without changing
// the rendered PNG or the synthesized WAV: the per-scene motion and look
// presets, the framerate, the silent-scene duration, and the resolved seed
// (phase source for handheld jitter; hashed unconditionally for simplicity —
// a seed change already invalidates the render anyway).
type ClipParams struct {
	MotionType      string  `json:"motion_type"`
	MotionIntensity float64 `json:"motion_intensity"`
	Grain           float64 `json:"grain"`
	WhiteBalance    string  `json:"white_balance"`
	FPS             int     `json:"fps"`
	SilenceSeconds  float64 `json:"silence_seconds"`
	Seed            int64   `json:"seed"`
	// AnimateHash is the animate hash of an animated scene, empty otherwise;
	// it is folded into the clip hash only when set.
	AnimateHash string `json:"animate_hash,omitempty"`
	// ClipTrim is the part of an own clip a scene uses ("start-end" in seconds),
	// empty for a whole clip; it is folded into the hash only when set.
	ClipTrim string `json:"clip_trim,omitempty"`
	// Assembly is the revision of how a clip made from a video is assembled (0 for a still): 2 brings
	// the colour range of an animation to the normal one, so clips made before are made again (only
	// ffmpeg runs, not the animation). Hashed only when set.
	Assembly int `json:"assembly,omitempty"`
	// ClipSpeed is the speed the scene's video plays at, 0 when automatic; hashed only when set.
	ClipSpeed float64 `json:"clip_speed,omitempty"`
	// Overlay identifies the scene's overlay: the content of its page, its range, position and
	// the size and frame rate it is drawn at; hashed only when set.
	Overlay string `json:"overlay,omitempty"`
}

// AnimateParams are the inputs that change the animated clip given the same
// rendered image: the motion prompt, the seed and the Wan models/settings.
type AnimateParams struct {
	Prompt         string  `json:"prompt"`
	Seed           int64   `json:"seed"`
	DiffusionModel string  `json:"diffusion_model"`
	T5XXL          string  `json:"t5xxl"`
	VAE            string  `json:"vae"`
	Resolution     string  `json:"resolution"`
	Frames         int     `json:"frames"`
	FPS            int     `json:"fps"`
	Steps          int     `json:"steps"`
	CfgScale       float64 `json:"cfg_scale"`

	// Two-expert (Wan A14B) inputs. They are hashed only when HighNoiseModel
	// is set, so a single-model project keeps the key it always had.
	HighNoiseModel    string  `json:"high_noise_model,omitempty"`
	HighNoiseSteps    int     `json:"high_noise_steps,omitempty"`
	HighNoiseCfgScale float64 `json:"high_noise_cfg_scale,omitempty"`
	FlowShift         string  `json:"flow_shift,omitempty"`

	// AnimateDiff inputs. Engine is "animatediff" only for that engine and is
	// empty for Wan, whose key does not change; with AnimateDiff the Wan
	// models are not part of the key.
	Engine       string  `json:"engine,omitempty"`
	Checkpoint   string  `json:"checkpoint,omitempty"`
	MotionModule string  `json:"motion_module,omitempty"`
	Strength     float64 `json:"strength,omitempty"`

	// MiniMax-H3 inputs (Engine "minimax-h3"): the DiffusionModel and VAE
	// above plus the audio VAE and the Qwen3-VL text encoder.
	AudioVAE string `json:"audio_vae,omitempty"`
	LLM      string `json:"llm,omitempty"`
	LoRA     string `json:"lora,omitempty"`
	// Connectors is the LTX-2.3 embeddings-connectors file (Engine "ltx-2.3", which also uses the DiffusionModel,
	// VAE, AudioVAE and LLM above).
	Connectors string `json:"connectors,omitempty"`
	// FirstFrame: the picture is the clip's first frame, not a reference image.
	FirstFrame bool `json:"first_frame,omitempty"`
	// ModelAudio: the model's audio is kept as ambience (minimax-h3 and ltx-2.3).
	ModelAudio bool `json:"model_audio,omitempty"`
	// ModelAudioSpeech: that audio carries a voice and is mixed at full level (ltx-2.3).
	ModelAudioSpeech bool `json:"model_audio_speech,omitempty"`
	// EndImage is the content hash of the clip's last picture and References those of the references (kind and
	// hash, in the order they are given to the engine): changing, adding or reordering one makes the clip again. A
	// scene without them leaves both out of the key, so existing caches stay valid.
	EndImage   string   `json:"end_image,omitempty"`
	References []string `json:"references,omitempty"`
}

// writeInputs adds the last picture and the references to an animate hash, only when there are any.
func (p AnimateParams) writeInputs(h io.Writer) {
	if p.EndImage != "" {
		fmt.Fprintf(h, "end_image=%s\x00", p.EndImage)
	}
	for i, r := range p.References {
		fmt.Fprintf(h, "reference%d=%s\x00", i, r)
	}
}

// AnimateHash returns a deterministic SHA-256 hex digest over the animate
// inputs, in the same label-prefixed style as ImageHash.
func AnimateHash(p AnimateParams) string {
	h := sha256.New()
	fmt.Fprintf(h, "prompt=%s\x00", p.Prompt)
	fmt.Fprintf(h, "seed=%d\x00", p.Seed)
	if p.Engine == "minimax-h3" {
		fmt.Fprintf(h, "engine=%s\x00", p.Engine)
		fmt.Fprintf(h, "diffusion_model=%s\x00", p.DiffusionModel)
		fmt.Fprintf(h, "vae=%s\x00", p.VAE)
		fmt.Fprintf(h, "audio_vae=%s\x00", p.AudioVAE)
		fmt.Fprintf(h, "llm=%s\x00", p.LLM)
		if p.LoRA != "" {
			fmt.Fprintf(h, "lora=%s\x00", p.LoRA) // only when set: keeps the hashes of runs without it
		}
		if p.FirstFrame {
			fmt.Fprintf(h, "first_frame=1\x00") // only when set: keeps the hashes of reference runs
		}
		if p.ModelAudio {
			fmt.Fprintf(h, "model_audio=1\x00") // only when set: keeps the hashes of runs without it
		}
		p.writeInputs(h)
		fmt.Fprintf(h, "resolution=%s\x00", p.Resolution)
		fmt.Fprintf(h, "frames=%d\x00", p.Frames)
		fmt.Fprintf(h, "fps=%d\x00", p.FPS)
		fmt.Fprintf(h, "steps=%d\x00", p.Steps)
		fmt.Fprintf(h, "cfg_scale=%v\x00", p.CfgScale)
		return hex.EncodeToString(h.Sum(nil))
	}
	if p.Engine == "ltx-2.3" {
		fmt.Fprintf(h, "engine=%s\x00", p.Engine)
		fmt.Fprintf(h, "diffusion_model=%s\x00", p.DiffusionModel)
		fmt.Fprintf(h, "vae=%s\x00", p.VAE)
		fmt.Fprintf(h, "audio_vae=%s\x00", p.AudioVAE)
		fmt.Fprintf(h, "llm=%s\x00", p.LLM)
		fmt.Fprintf(h, "connectors=%s\x00", p.Connectors)
		if p.ModelAudio {
			fmt.Fprintf(h, "model_audio=1\x00") // only when set: keeps the hashes of runs without it
		}
		if p.ModelAudioSpeech {
			fmt.Fprintf(h, "model_audio_speech=1\x00") // only when set
		}
		p.writeInputs(h)
		fmt.Fprintf(h, "resolution=%s\x00", p.Resolution)
		fmt.Fprintf(h, "frames=%d\x00", p.Frames)
		fmt.Fprintf(h, "fps=%d\x00", p.FPS)
		fmt.Fprintf(h, "steps=%d\x00", p.Steps)
		fmt.Fprintf(h, "cfg_scale=%v\x00", p.CfgScale)
		return hex.EncodeToString(h.Sum(nil))
	}
	if p.Engine != "" {
		fmt.Fprintf(h, "engine=%s\x00", p.Engine)
		fmt.Fprintf(h, "checkpoint=%s\x00", p.Checkpoint)
		fmt.Fprintf(h, "motion_module=%s\x00", p.MotionModule)
		fmt.Fprintf(h, "strength=%v\x00", p.Strength)
		fmt.Fprintf(h, "resolution=%s\x00", p.Resolution)
		fmt.Fprintf(h, "frames=%d\x00", p.Frames)
		fmt.Fprintf(h, "fps=%d\x00", p.FPS)
		fmt.Fprintf(h, "steps=%d\x00", p.Steps)
		fmt.Fprintf(h, "cfg_scale=%v\x00", p.CfgScale)
		return hex.EncodeToString(h.Sum(nil))
	}
	fmt.Fprintf(h, "diffusion_model=%s\x00", p.DiffusionModel)
	fmt.Fprintf(h, "t5xxl=%s\x00", p.T5XXL)
	fmt.Fprintf(h, "vae=%s\x00", p.VAE)
	fmt.Fprintf(h, "resolution=%s\x00", p.Resolution)
	fmt.Fprintf(h, "frames=%d\x00", p.Frames)
	fmt.Fprintf(h, "fps=%d\x00", p.FPS)
	fmt.Fprintf(h, "steps=%d\x00", p.Steps)
	fmt.Fprintf(h, "cfg_scale=%v\x00", p.CfgScale)
	if p.HighNoiseModel != "" {
		fmt.Fprintf(h, "high_noise_model=%s\x00", p.HighNoiseModel)
		fmt.Fprintf(h, "high_noise_steps=%d\x00", p.HighNoiseSteps)
		fmt.Fprintf(h, "high_noise_cfg_scale=%v\x00", p.HighNoiseCfgScale)
		fmt.Fprintf(h, "flow_shift=%s\x00", p.FlowShift)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ClipHash returns a deterministic SHA-256 hex digest over the clip
// generation inputs, in the same label-prefixed style as ImageHash.
func ClipHash(p ClipParams) string {
	h := sha256.New()
	fmt.Fprintf(h, "motion_type=%s\x00", p.MotionType)
	fmt.Fprintf(h, "motion_intensity=%v\x00", p.MotionIntensity)
	fmt.Fprintf(h, "grain=%v\x00", p.Grain)
	fmt.Fprintf(h, "white_balance=%s\x00", p.WhiteBalance)
	fmt.Fprintf(h, "fps=%d\x00", p.FPS)
	fmt.Fprintf(h, "silence_seconds=%v\x00", p.SilenceSeconds)
	fmt.Fprintf(h, "seed=%d\x00", p.Seed)
	if p.AnimateHash != "" {
		fmt.Fprintf(h, "animate=%s\x00", p.AnimateHash)
	}
	if p.Assembly != 0 {
		fmt.Fprintf(h, "assembly=%d\x00", p.Assembly)
	}
	if p.ClipSpeed != 0 {
		fmt.Fprintf(h, "clip_speed=%v\x00", p.ClipSpeed)
	}
	if p.ClipTrim != "" {
		fmt.Fprintf(h, "clip_trim=%s\x00", p.ClipTrim)
	}
	if p.Overlay != "" {
		fmt.Fprintf(h, "overlay=%s\x00", p.Overlay)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ---------------------------------------------------------------------------
// Artifact paths
// ---------------------------------------------------------------------------

// ArtifactPaths returns the stable per-scene file paths under tmp/.
func ArtifactPaths(sceneID string) (wav, png, mp4 string) {
	base := filepath.Join("tmp", fmt.Sprintf("scene_%s", sceneID))
	wav = base + ".wav"
	png = base + ".png"
	mp4 = base + ".mp4"
	return
}

// AnimatePaths returns the stable per-scene animate artifacts under tmp/:
// raw is sd's .avi output and mp4 the ping-pong clip the clip phase reads.
func AnimatePaths(sceneID string) (raw, mp4 string) {
	base := filepath.Join("tmp", fmt.Sprintf("scene_%s.anim", sceneID))
	return base + ".avi", base + ".mp4"
}

// VariantKey returns the manifest key for variant i (0-based) of a scene
// rendering `variants` images. A single-variant scene keeps the plain scene
// ID so its render cache entry is shared with video mode (the cross-mode
// cache-sharing guarantee).
func VariantKey(sceneID string, variants, i int) string {
	if variants == 1 {
		return sceneID
	}
	return fmt.Sprintf("%s_v%d", sceneID, i+1)
}

// ---------------------------------------------------------------------------
// Manifest
// ---------------------------------------------------------------------------

// SceneEntry stores the cached hashes and resolved artifact paths for one
// scene. ClipHash is empty in manifests written by pre-camera-motion
// binaries; Decide treats that as a one-time clip miss.
type SceneEntry struct {
	AudioHash string `json:"audio_hash"`
	ImageHash string `json:"image_hash"`
	ClipHash  string `json:"clip_hash,omitempty"`
	// AnimateHash and AnimPath are set only for animated scenes.
	AnimateHash string `json:"animate_hash,omitempty"`
	AnimPath    string `json:"anim_path,omitempty"`
	WavPath     string `json:"wav_path"`
	PngPath     string `json:"png_path"`
	Mp4Path     string `json:"mp4_path"`
	// Music mode: the compose and synth hashes and their artifacts (the
	// enriched request and the audio file).
	ComposeHash string `json:"compose_hash,omitempty"`
	SynthHash   string `json:"synth_hash,omitempty"`
	ComposePath string `json:"compose_path,omitempty"`
	AudioPath   string `json:"audio_path,omitempty"`
}

// Manifest is the on-disk cache index (manifest.json).
type Manifest struct {
	Scenes map[string]SceneEntry `json:"scenes"`
}

// LoadManifest reads and parses manifest.json. A missing file yields an
// empty (non-nil) manifest so callers can proceed without a nil check.
func LoadManifest(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Manifest{Scenes: make(map[string]SceneEntry)}, nil
		}
		return nil, fmt.Errorf("cache: read manifest %s: %w", path, err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("cache: parse manifest %s: %w", path, err)
	}
	if m.Scenes == nil {
		m.Scenes = make(map[string]SceneEntry)
	}
	return &m, nil
}

// SaveManifest atomically writes manifest.json (write to temp, rename).
func SaveManifest(path string, m *Manifest) error {
	if m.Scenes == nil {
		m.Scenes = make(map[string]SceneEntry)
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("cache: marshal manifest: %w", err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("cache: mkdir %s: %w", dir, err)
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("cache: write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("cache: rename %s → %s: %w", tmp, path, err)
	}
	return nil
}

// Has reports whether the manifest contains an entry for sceneID.
func (m *Manifest) Has(sceneID string) bool {
	_, ok := m.Scenes[sceneID]
	return ok
}

// UpdateEntry sets or replaces the entry for sceneID.
func (m *Manifest) UpdateEntry(sceneID string, e SceneEntry) {
	if m.Scenes == nil {
		m.Scenes = make(map[string]SceneEntry)
	}
	m.Scenes[sceneID] = e
}

// SortedSceneIDs returns scene IDs in lexicographic order for
// deterministic iteration.
func (m *Manifest) SortedSceneIDs() []string {
	ids := make([]string, 0, len(m.Scenes))
	for id := range m.Scenes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// ---------------------------------------------------------------------------
// Decide
// ---------------------------------------------------------------------------

// Decision reports, for a single scene, which phases are cache hits
// (should be skipped) versus misses (must be executed).
type Decision struct {
	SceneID string
	// Phases maps each Phase to true (hit / skip) or false (miss / run).
	Phases map[Phase]bool
}

// Decide evaluates the manifest for sceneID against the newly computed
// hashes. If force is true, every phase is a miss regardless of cache state.
//
// Dependency model:
//
//	tts    → wav   (depends on audio hash)
//	render → png   (depends on image hash)
//	clip   → mp4   (depends on wav, png, AND the clip parameters)
//
// Therefore:
//
//	tts    hit  ⇔ audioHash matches
//	render hit  ⇔ imageHash matches
//	clip   hit  ⇔ audioHash AND imageHash AND clipHash match
//
// A manifest entry with an empty stored ClipHash (written by a pre-change
// binary) is a clip miss: the clip re-encodes once and the hash is stored.
func Decide(m *Manifest, sceneID, newAudioHash, newImageHash, newClipHash string, force bool) Decision {
	d := Decision{
		SceneID: sceneID,
		Phases:  make(map[Phase]bool, len(AllPhases)),
	}

	// --force: ignore cache entirely.
	if force {
		for _, p := range AllPhases {
			d.Phases[p] = false
		}
		return d
	}

	entry, ok := m.Scenes[sceneID]
	if !ok {
		// No prior entry → full miss.
		for _, p := range AllPhases {
			d.Phases[p] = false
		}
		return d
	}

	audioHit := entry.AudioHash == newAudioHash
	imageHit := entry.ImageHash == newImageHash
	clipHit := entry.ClipHash != "" && entry.ClipHash == newClipHash

	d.Phases[PhaseTTS] = audioHit
	d.Phases[PhaseRender] = imageHit
	d.Phases[PhaseClip] = audioHit && imageHit && clipHit

	return d
}

// DecideImages reports whether the render phase for one images-mode variant
// is a cache hit. The decision consults only the image-hash digest and the
// presence of the cached PNG on disk — never the audio hash, the WAV, or the
// MP4, because tts/probe/clip/concat do not run in images mode. entryKey is
// the manifest key for the variant (see VariantKey).
func DecideImages(m *Manifest, entryKey, newImageHash string, force bool) bool {
	if force {
		return false
	}
	entry, ok := m.Scenes[entryKey]
	if !ok || entry.ImageHash != newImageHash {
		return false
	}
	if entry.PngPath == "" {
		return false
	}
	if _, err := os.Stat(entry.PngPath); err != nil {
		return false
	}
	return true
}

// ---------------------------------------------------------------------------
// --only filter
// ---------------------------------------------------------------------------

// OnlyFilter encapsulates the --only <id> CLI flag. When SceneID is empty
// the filter is inactive and all scenes are processed normally.
type OnlyFilter struct {
	SceneID string
}

// NewOnlyFilter creates a filter from the CLI value (may be "").
func NewOnlyFilter(id string) OnlyFilter {
	return OnlyFilter{SceneID: id}
}

// Active reports whether the filter is set.
func (f OnlyFilter) Active() bool {
	return f.SceneID != ""
}

// ShouldProcess returns true if the scene should go through the full
// Decide / execute pipeline. Under --only, only the matching scene is
// processed; all others are skipped.
func (f OnlyFilter) ShouldProcess(sceneID string) bool {
	if !f.Active() {
		return true
	}
	return sceneID == f.SceneID
}

// IsCachedForConcat reports whether a scene should be treated as fully
// cached (all three phases hit) for the purposes of the final concat step.
// Under --only, every scene that is NOT the target is assumed to already
// have valid artifacts on disk.
func (f OnlyFilter) IsCachedForConcat(sceneID string) bool {
	if !f.Active() {
		return false
	}
	return sceneID != f.SceneID
}

// DecideWithFilter is a convenience wrapper that applies the OnlyFilter
// before calling Decide. Scenes excluded by the filter are reported as
// fully cached (all phases hit).
func DecideWithFilter(m *Manifest, f OnlyFilter, sceneID, newAudioHash, newImageHash, newClipHash string, force bool) Decision {
	if f.IsCachedForConcat(sceneID) {
		d := Decision{SceneID: sceneID, Phases: make(map[Phase]bool, len(AllPhases))}
		for _, p := range AllPhases {
			d.Phases[p] = true
		}
		return d
	}
	return Decide(m, sceneID, newAudioHash, newImageHash, newClipHash, force)
}

// DecideAnimated is DecideWithFilter for an animated scene. The animate
// phase is a hit only when the render phase is (same image) and the stored
// animate hash matches; the clip phase additionally requires the animate
// hit. The caller passes a clip hash that already includes the animate hash.
// Scenes excluded by --only are reported as fully cached.
func DecideAnimated(m *Manifest, f OnlyFilter, sceneID, newAudioHash, newImageHash, newClipHash, newAnimateHash string, force bool) Decision {
	d := DecideWithFilter(m, f, sceneID, newAudioHash, newImageHash, newClipHash, force)
	if f.IsCachedForConcat(sceneID) {
		d.Phases[PhaseAnimate] = true
		return d
	}
	entry, ok := m.Scenes[sceneID]
	hit := !force && ok && d.Phases[PhaseRender] && entry.AnimateHash != "" && entry.AnimateHash == newAnimateHash
	d.Phases[PhaseAnimate] = hit
	d.Phases[PhaseClip] = d.Phases[PhaseClip] && hit
	return d
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// InitImagePath is where a scene's starting picture, fitted to the project
// resolution, is written for the diffusion program to read.
func InitImagePath(sceneID string) string {
	return filepath.Join("tmp", fmt.Sprintf("scene_%s.init.png", sceneID))
}

// ManifestPath returns the conventional location of manifest.json relative
// to the project root.
func ManifestPath() string {
	return filepath.Join("tmp", "manifest.json")
}

// BlenderParams is everything that decides the video Blender renders for a scene: the content of the .blend file (or,
// for a scene built from a description, the description resolved with the project's tempo and design colours, and its fonts), the values injected
// into it, the render settings, the size and frame rate, how long the scene is and Blender's version.
// The device (card or CPU) is deliberately not part of it: the same picture up to noise is not worth a new render.
type BlenderParams struct {
	FileSHA256 string `json:"file_sha256"` // empty for a built scene
	// Build and Font are only for a built scene (omitted otherwise, so the hash of a .blend scene is the one it always
	// was): the SHA-256 of the description resolved (every key in seconds, every colour #rrggbb), and the identity of
	// the font files it uses (the files of the sources and the WOFF2 of the design's fonts).
	Build string `json:"build,omitempty"`
	Font  string `json:"font,omitempty"`
	// Models is the identity of the model files a built scene uses and of the pictures they need (omitted when it has none,
	// so the hash of every scene without a model is the one it always was).
	Models     string `json:"models,omitempty"`
	Inject     string `json:"inject"` // canonical JSON (keys sorted)
	Samples    int    `json:"samples"`
	Frames     []int  `json:"frames,omitempty"`
	Resolution string `json:"resolution"`
	FPS        int    `json:"fps"`
	Length     string `json:"length"` // the narration's hash, or the silence's seconds
	Version    string `json:"version"`
}

// BlenderHash is the hex SHA-256 of the canonical JSON of the parameters.
func BlenderHash(p BlenderParams) string {
	data, _ := json.Marshal(p)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
