// Package config is the model of a project: the scenes.yaml file as Go types, its loading (strict about fields it
// does not know) and its validation. Validation runs before any program starts and is the same for the command line
// and for the server: it checks that the files named exist, that paths and names stay inside the project, that
// numbers are within bounds and that each engine's own rules hold (see paths.go and bounds.go). The engines and the
// pipeline read a [Config]; nothing here runs a program or opens a network connection.
package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"veritmo/internal/platform/settings"
	"veritmo/internal/speech/tts"
	"veritmo/internal/video/motion"
	"veritmo/internal/video/videoedit"
)

// OutputMode is what a project makes (output.mode, and the --mode CLI override). It is written in the files and in the API as
// its text.
type OutputMode string

// Output modes accepted by output.mode (and the --mode CLI override).
const (
	ModeVideo  OutputMode = "video"
	ModeImages OutputMode = "images"
	ModeMusic  OutputMode = "music"
	// ModeVideoEdit is a project that edits a video that already exists with one tool (see VideoEdit).
	ModeVideoEdit OutputMode = "video_edit"
)

// Config represents the top-level scenes.yaml schema.
//
// OutputFile is the deprecated alias for Output.File, kept for one release
// cycle; Validate maps it onto Output.File and records a deprecation warning.
type Config struct {
	// SkipBrowserCheck leaves out the check that a browser exists for the motion graphics: the import writes
	// projects that are run elsewhere. It is never read from or written to a file.
	SkipBrowserCheck bool `yaml:"-" json:"-"`
	// SkipBlenderCheck does the same for Blender (scenes rendered from a .blend file).
	SkipBlenderCheck bool `yaml:"-" json:"-"`
	// Checks answers the questions about the machine (a browser, Blender) that validation asks; never read from or
	// written to a file.
	Checks Checks `yaml:"-" json:"-"`
	// Parts is the catalog the parts a scene names are checked against; the caller that validates sets it. Never read
	// from or written to a file.
	Parts      PartCatalog `yaml:"-" json:"-"`
	Project    string      `yaml:"project" json:"project"`
	OutputFile string      `yaml:"output_file" json:"output_file,omitempty"`
	// Adult says whether the project shows what is for people of 18 or older: true (for adults),
	// false (looked at and fine for everyone) or nil (not looked at). Nothing in a run reads it;
	// the server uses it to hide the project (see internal/server/core_contentmode.go).
	Adult *bool `yaml:"adult,omitempty" json:"adult,omitempty"`
	// Client is the dashboard that requested the project through the generation API (see ValidClient). Nothing
	// in a run reads it; the server copies it to the run so the statistics and the metrics can group by it.
	Client   string   `yaml:"client,omitempty" json:"client,omitempty"`
	Output   Output   `yaml:"output" json:"output"`
	Defaults Defaults `yaml:"defaults" json:"defaults"`
	Scenes   []Scene  `yaml:"scenes" json:"scenes"`
	// Tracks lists the songs of a music-mode project; scenes and tracks are
	// mutually exclusive by mode.
	Tracks []Track `yaml:"tracks,omitempty" json:"tracks,omitempty"`
	// Models lists the 3D models of a models3d-mode project; scenes, tracks and models are mutually exclusive by mode.
	Models []ModelEntry `yaml:"models,omitempty" json:"models,omitempty"`
	// Mixes lists the timelines of clips a music project renders into one
	// file each, after its tracks.
	Mixes []Mix `yaml:"mixes,omitempty" json:"mixes,omitempty"`
	// Favorites marks songs of a music project as keepers. They only help the
	// user find them again: nothing in a run reads them.
	Favorites []Favorite `yaml:"favorites,omitempty" json:"favorites,omitempty"`
	// BGM is the optional soundtrack of a video project.
	BGM *BGM `yaml:"bgm,omitempty" json:"bgm,omitempty"`
	// VideoTrack is the video under the scenes of a video project: clips of sources/ in seconds of the output, in order, with gaps allowed. With
	// it the scenes that say `layer` are laid over it (see videotrack.go).
	VideoTrack []VideoTrackItem `yaml:"video_track,omitempty" json:"video_track,omitempty"`
	// VideoEdit is the tool, its settings and its inputs in a video-edit project; it is the project's whole job.
	VideoEdit *VideoEdit `yaml:"video_edit,omitempty" json:"video_edit,omitempty"`

	// Warnings collects non-fatal findings (deprecations, ignored fields)
	// produced by the last Validate call; the CLI prints them to stderr.
	Warnings []string `yaml:"-" json:"-"`
}

// VideoEdit is what a video-edit project does: one tool of internal/video/videoedit over a video of its sources folder
// (or, for motion control, a motion of the library), with the tool's settings.
type VideoEdit struct {
	Tool string `yaml:"tool" json:"tool"`
	// Source is the video in the sources folder; empty only when Motion names the motion of the library.
	Source string `yaml:"source,omitempty" json:"source,omitempty"`
	// Image is the reference picture in the sources folder, for the tools that take one.
	Image string `yaml:"image,omitempty" json:"image,omitempty"`
	// Motion is the identifier of a motion of the library, instead of Source.
	Motion   string         `yaml:"motion,omitempty" json:"motion,omitempty"`
	Settings map[string]any `yaml:"settings,omitempty" json:"settings,omitempty"`
}

// Output selects the pipeline mode and its destination.
type Output struct {
	Mode         OutputMode `yaml:"mode" json:"mode"`
	File         string     `yaml:"file" json:"file"`
	ContactSheet bool       `yaml:"contact_sheet" json:"contact_sheet"`
}

// Defaults holds shared generation parameters.
// LoraModelDir is reserved (design D4): parsed and stored, no behaviour yet.
type Defaults struct {
	Model string `yaml:"model" json:"model"`
	Voice string `yaml:"voice" json:"voice"`
	// VoiceSpeaker is the speaker of defaults.voice when that model holds several (0, the first, if absent).
	VoiceSpeaker   int    `yaml:"voice_speaker,omitempty" json:"voice_speaker,omitempty"`
	Resolution     string `yaml:"resolution" json:"resolution"`
	Sampler        string `yaml:"sampler" json:"sampler"`
	Steps          int    `yaml:"steps" json:"steps"`
	FPS            int    `yaml:"fps" json:"fps"`
	SubjectPrompt  string `yaml:"subject_prompt" json:"subject_prompt"`
	NegativePrompt string `yaml:"negative_prompt" json:"negative_prompt"`
	LoraModelDir   string `yaml:"lora_model_dir" json:"lora_model_dir"`
	// ModelSet is the alternative to Model for diffusion models that need
	// a text-encoder LLM and a VAE alongside them (Krea 2, Wan).
	ModelSet *ModelSet `yaml:"model_set,omitempty" json:"model_set,omitempty"`
	// Animate configures the animate phase (Wan image-to-video).
	Animate *Animate `yaml:"animate,omitempty" json:"animate,omitempty"`
	// Motion is the look and the tempo of the scenes made from parts of the catalog.
	Motion *MotionDefaults `yaml:"motion,omitempty" json:"motion,omitempty"`
	// Music configures the music engine (music mode only).
	Music *Music `yaml:"music,omitempty" json:"music,omitempty"`
	// Models3D gives the engine and the detail of the models that do not say (models3d mode only).
	Models3D *Models3DDefaults `yaml:"models3d,omitempty" json:"models3d,omitempty"`
	// TransitionSeconds is the length of the crossfade between scenes (0 = hard cuts);
	// a scene's own TransitionSeconds overrides it.
	TransitionSeconds float64 `yaml:"transition_seconds,omitempty" json:"transition_seconds,omitempty"`
	// Transition is the kind of the transition between scenes (see Transitions); empty is
	// the crossfade ("fade"). A scene's own Transition overrides it.
	Transition string `yaml:"transition,omitempty" json:"transition,omitempty"`
	// TransitionAccent is the colour a shader transition (`shader:<name>`) glows with, "#rrggbb" or "#rgb"; empty is HyperFrames' orange. A
	// scene's own TransitionAccent overrides it.
	TransitionAccent string `yaml:"transition_accent,omitempty" json:"transition_accent,omitempty"`
}

// Wan animate defaults, applied when the matching Animate field is unset.
const (
	DefaultAnimateResolution = "832x480"
	DefaultAnimateFrames     = 33
	DefaultAnimateFPS        = 16
	DefaultAnimateSteps      = 20
	DefaultAnimateCfgScale   = 6.0

	// DefaultAnimateTwoExpertSteps is the number of steps of the first expert of a two-expert (Wan A14B) clip, applied
	// instead of the defaults above when a high-noise expert is set: the value measured with Wan 2.2 A14B.
	DefaultAnimateTwoExpertSteps = 10
	// DefaultAnimateTwoExpertCfgScale is the guidance scale of the first expert of a two-expert clip.
	DefaultAnimateTwoExpertCfgScale = 3.5
	// DefaultAnimateHighNoiseSteps is the number of steps of the high-noise expert of a two-expert clip.
	DefaultAnimateHighNoiseSteps    = 8
	DefaultAnimateHighNoiseCfgScale = 3.5
	// AnimateTwoExpertFlowShift is the flow shift passed to sd in two-expert
	// mode: the value sd.cpp documents for Wan and the one it was measured with.
	AnimateTwoExpertFlowShift = "3.0"
)

// Animation engines. Wan is the default (also when Engine is empty).
const (
	EngineWan         = "wan"
	EngineAnimateDiff = "animatediff"
	EngineMiniMaxH3   = "minimax-h3"
	EngineLTX         = "ltx-2.3"
)

// LTX-2.3 defaults, applied when the matching Animate field is unset and the engine is ltx-2.3 (the distilled
// model, measured on a 12 GB card: 768x448, 49 frames, 8 steps, 118 s, 11.35 GB of VRAM). The model generates
// video and audio at a fixed 24 fps, sides are multiples of 32 and the frame count is on the 8k+1 grid
// (9, 17, ..., 49, 57, ...).
const (
	DefaultLTXResolution = "768x416"
	DefaultLTXFrames     = 49
	DefaultLTXSteps      = 8
	DefaultLTXCfgScale   = 1.0
	LTXFPS               = 24
	LTXSideMultiple      = 32
)

// MiniMax-H3 defaults, applied when the matching Animate field is unset and
// the engine is minimax-h3 (values from sd.cpp's docs/minimax_h3.md; the
// model fits a 30 GB machine thanks to --mmap). The model generates video
// and audio at a fixed 24 fps, sides are multiples of 32 and the frame count
// is on the 17k+5 grid (22, 39, 56, ...).
const (
	DefaultMiniMaxResolution = "864x480"
	DefaultMiniMaxFrames     = 56
	DefaultMiniMaxSteps      = 20
	DefaultMiniMaxTurboSteps = 4
	// DefaultMiniMaxFirstFrameTurboSteps are the steps of the 8-step first-frame LoRA (v1.0), the turbo one of FirstFrame.
	DefaultMiniMaxFirstFrameTurboSteps = 8
	DefaultMiniMaxCfgScale             = 1.0
	MiniMaxFPS                         = 24
	MiniMaxSideMultiple                = 32
)

// AnimateDiff defaults, applied when the matching Animate field is unset and
// the engine is animatediff: the values measured with mm_sd15_v3 on SD 1.5.
const (
	DefaultAnimateDiffResolution = "512x512"
	DefaultAnimateDiffFrames     = 16
	DefaultAnimateDiffFPS        = 8
	DefaultAnimateDiffSteps      = 25
	DefaultAnimateDiffCfgScale   = 7.0
	DefaultAnimateDiffStrength   = 0.75

	// AnimateDiffMinFrames is the fewest frames of an AnimateDiff clip: the module was trained at 16.
	AnimateDiffMinFrames = 8
	// AnimateDiffMaxFrames is the most frames of an AnimateDiff clip: sd's positional encoding goes up to 32.
	AnimateDiffMaxFrames = 32
)

// Animate holds the models and generation settings for animated scenes.
// Wan takes a umt5 text encoder (--t5xxl), not an LLM, so this is not a
// ModelSet. Unset settings resolve to the DefaultAnimate* values.
type Animate struct {
	// Engine is "wan" (also when empty), "animatediff" or "minimax-h3". With
	// animatediff the Wan files below are neither required nor used, and
	// Checkpoint, MotionModule and Strength are. With minimax-h3 the files are
	// DiffusionModel, VAE (video), AudioVAE and LLM (Qwen3-VL text encoder).
	Engine         string `yaml:"engine,omitempty" json:"engine,omitempty"`
	DiffusionModel string `yaml:"diffusion_model,omitempty" json:"diffusion_model,omitempty"`
	T5XXL          string `yaml:"t5xxl,omitempty" json:"t5xxl,omitempty"`
	VAE            string `yaml:"vae,omitempty" json:"vae,omitempty"`
	// MiniMax-H3: the Qwen3-VL text encoder and the audio VAE.
	LLM      string `yaml:"llm,omitempty" json:"llm,omitempty"`
	AudioVAE string `yaml:"audio_vae,omitempty" json:"audio_vae,omitempty"`
	// Connectors is the LTX-2.3 embeddings-connectors file (ltx-2.3 only; its other files are DiffusionModel, VAE,
	// AudioVAE and LLM, here the Gemma text encoder).
	Connectors string `yaml:"connectors,omitempty" json:"connectors,omitempty"`
	// LoRA is an optional LoRA for minimax-h3 (the turbo one: with it the
	// default steps are DefaultMiniMaxTurboSteps instead of DefaultMiniMaxSteps).
	LoRA string `yaml:"lora,omitempty" json:"lora,omitempty"`
	// FirstFrame makes minimax-h3 use the scene's picture as the first frame of the clip (the first/last-frame
	// model, FL2VA) instead of as a reference image (Ref2VA): the clip starts exactly from the picture and the
	// subjects stay where it puts them. DiffusionModel must then be the fl2va file, and a LoRA the first-frame one
	// (whose default is DefaultMiniMaxFirstFrameTurboSteps steps).
	FirstFrame bool `yaml:"first_frame,omitempty" json:"first_frame,omitempty"`
	// ModelAudio keeps the audio the model writes with the video (minimax-h3 and ltx-2.3 only) as ambient sound
	// under the narration; off by default.
	ModelAudio bool `yaml:"model_audio,omitempty" json:"model_audio,omitempty"`
	// ModelAudioSpeech says the model's audio carries a voice (a line the prompt asks it to say), not ambience: it is
	// mixed at full level instead of under the narration. Needs ModelAudio; off by default.
	ModelAudioSpeech bool `yaml:"model_audio_speech,omitempty" json:"model_audio_speech,omitempty"`
	// AnimateDiff: an SD 1.5 checkpoint, the motion module and how far the
	// frames may move away from the scene's still (0 < strength <= 1).
	Checkpoint   string   `yaml:"checkpoint,omitempty" json:"checkpoint,omitempty"`
	MotionModule string   `yaml:"motion_module,omitempty" json:"motion_module,omitempty"`
	Strength     *float64 `yaml:"strength,omitempty" json:"strength,omitempty"`
	Resolution   string   `yaml:"resolution,omitempty" json:"resolution,omitempty"`
	Frames       int      `yaml:"frames,omitempty" json:"frames,omitempty"`
	FPS          int      `yaml:"fps,omitempty" json:"fps,omitempty"`
	Steps        int      `yaml:"steps,omitempty" json:"steps,omitempty"`
	CfgScale     *float64 `yaml:"cfg_scale,omitempty" json:"cfg_scale,omitempty"`
	// HighNoiseDiffusionModel turns on the two-expert mode (Wan A14B):
	// DiffusionModel is then the low-noise expert and this the high-noise
	// one, with their own steps and cfg. Empty means the single-model mode.
	HighNoiseDiffusionModel string   `yaml:"high_noise_diffusion_model,omitempty" json:"high_noise_diffusion_model,omitempty"`
	HighNoiseSteps          int      `yaml:"high_noise_steps,omitempty" json:"high_noise_steps,omitempty"`
	HighNoiseCfgScale       *float64 `yaml:"high_noise_cfg_scale,omitempty" json:"high_noise_cfg_scale,omitempty"`
}

// IsAnimateDiff reports whether the engine is AnimateDiff.
func (a Animate) IsAnimateDiff() bool { return a.Engine == EngineAnimateDiff }

// IsMiniMaxH3 reports whether the engine is MiniMax-H3.
func (a Animate) IsMiniMaxH3() bool { return a.Engine == EngineMiniMaxH3 }

// IsLTX reports whether the engine is LTX-2.3.
func (a Animate) IsLTX() bool { return a.Engine == EngineLTX }

// TwoExpert reports whether the high-noise expert is set.
func (a Animate) TwoExpert() bool { return a.HighNoiseDiffusionModel != "" }

// Resolved returns a copy with every unset setting filled with its default.
// The defaults depend on the mode: single-model (5B) or two-expert (A14B).
func (a Animate) Resolved() Animate {
	if a.IsAnimateDiff() {
		return a.resolvedAnimateDiff()
	}
	if a.IsMiniMaxH3() {
		return a.resolvedMiniMax()
	}
	if a.IsLTX() {
		return a.resolvedLTX()
	}
	if a.Resolution == "" {
		a.Resolution = DefaultAnimateResolution
	}
	if a.Frames == 0 {
		a.Frames = DefaultAnimateFrames
	}
	if a.FPS == 0 {
		a.FPS = DefaultAnimateFPS
	}
	steps, cfg := DefaultAnimateSteps, DefaultAnimateCfgScale
	if a.TwoExpert() {
		steps, cfg = DefaultAnimateTwoExpertSteps, DefaultAnimateTwoExpertCfgScale
		if a.HighNoiseSteps == 0 {
			a.HighNoiseSteps = DefaultAnimateHighNoiseSteps
		}
		if a.HighNoiseCfgScale == nil {
			v := DefaultAnimateHighNoiseCfgScale
			a.HighNoiseCfgScale = &v
		}
	}
	if a.Steps == 0 {
		a.Steps = steps
	}
	if a.CfgScale == nil {
		a.CfgScale = &cfg
	}
	return a
}

// resolvedLTX fills the LTX-2.3 defaults. The frame rate is fixed.
func (a Animate) resolvedLTX() Animate {
	if a.Resolution == "" {
		a.Resolution = DefaultLTXResolution
	}
	if a.Frames == 0 {
		a.Frames = DefaultLTXFrames
	}
	a.FPS = LTXFPS
	if a.Steps == 0 {
		a.Steps = DefaultLTXSteps
	}
	if a.CfgScale == nil {
		v := DefaultLTXCfgScale
		a.CfgScale = &v
	}
	return a
}

// resolvedMiniMax fills the MiniMax-H3 defaults. The frame rate is fixed.
func (a Animate) resolvedMiniMax() Animate {
	if a.Resolution == "" {
		a.Resolution = DefaultMiniMaxResolution
	}
	if a.Frames == 0 {
		a.Frames = DefaultMiniMaxFrames
	}
	a.FPS = MiniMaxFPS
	if a.Steps == 0 {
		a.Steps = DefaultMiniMaxSteps
		if a.LoRA != "" {
			a.Steps = DefaultMiniMaxTurboSteps
			if a.FirstFrame {
				a.Steps = DefaultMiniMaxFirstFrameTurboSteps
			}
		}
	}
	if a.CfgScale == nil {
		v := DefaultMiniMaxCfgScale
		a.CfgScale = &v
	}
	return a
}

// resolvedAnimateDiff fills the AnimateDiff defaults.
func (a Animate) resolvedAnimateDiff() Animate {
	if a.Resolution == "" {
		a.Resolution = DefaultAnimateDiffResolution
	}
	if a.Frames == 0 {
		a.Frames = DefaultAnimateDiffFrames
	}
	if a.FPS == 0 {
		a.FPS = DefaultAnimateDiffFPS
	}
	if a.Steps == 0 {
		a.Steps = DefaultAnimateDiffSteps
	}
	if a.CfgScale == nil {
		v := DefaultAnimateDiffCfgScale
		a.CfgScale = &v
	}
	if a.Strength == nil {
		v := DefaultAnimateDiffStrength
		a.Strength = &v
	}
	return a
}

// ForScene returns the animate settings of one scene: the project's, with the
// scene's own resolution, frames and steps laid over them, and every unset
// setting filled with its default. The project's model and frame rate are kept.
func (a Animate) ForScene(s Scene) Animate {
	if s.Animation != nil {
		if s.Animation.Resolution != "" {
			a.Resolution = s.Animation.Resolution
		}
		if s.Animation.Frames != 0 {
			a.Frames = s.Animation.Frames
		}
		if s.Animation.Steps != 0 {
			a.Steps = s.Animation.Steps
		}
	}
	return a.Resolved()
}

// ModelSet names the three files such a model needs and the cfg scale it
// was tuned for. CfgScale is a pointer so an explicit 0 differs from absent.
type ModelSet struct {
	DiffusionModel string   `yaml:"diffusion_model" json:"diffusion_model"`
	LLM            string   `yaml:"llm,omitempty" json:"llm,omitempty"`
	VAE            string   `yaml:"vae" json:"vae"`
	CfgScale       *float64 `yaml:"cfg_scale,omitempty" json:"cfg_scale,omitempty"`
	// Edit says the model edits a reference picture by instruction (FLUX.1 Kontext): a scene's init_image is then
	// the picture to edit and the prompt is the instruction. It needs ClipL and T5XXL (its text encoders) instead
	// of an LLM; a set that is not an editing one refuses them. The other family of editing models (Qwen-Image-Edit)
	// takes LLM and LLMVision (its vision projector) instead of ClipL and T5XXL; the two are never mixed.
	Edit      bool   `yaml:"edit,omitempty" json:"edit,omitempty"`
	ClipL     string `yaml:"clip_l,omitempty" json:"clip_l,omitempty"`
	T5XXL     string `yaml:"t5xxl,omitempty" json:"t5xxl,omitempty"`
	LLMVision string `yaml:"llm_vision,omitempty" json:"llm_vision,omitempty"`
}

// QwenEdit reports whether the set is an editing model of the Qwen family (an LLM with its vision projector)
// rather than the FLUX one (CLIP-L and T5-XXL).
func (m ModelSet) QwenEdit() bool {
	return m.Edit && (m.LLM != "" || m.LLMVision != "")
}

// Motion types accepted by scene.motion.type.
const (
	MotionStatic    = "static"
	MotionZoomIn    = "zoom_in"
	MotionZoomOut   = "zoom_out"
	MotionPanLeft   = "pan_left"
	MotionPanRight  = "pan_right"
	MotionHandheld  = "handheld"
	MotionRotateCW  = "rotate_cw"
	MotionRotateCCW = "rotate_ccw"
)

// White-balance presets accepted by scene.look.white_balance.
const (
	WBNeutral     = "neutral"
	WBWarm        = "warm"
	WBCool        = "cool"
	WBFluorescent = "fluorescent"
)

// DefaultMotionIntensity applies when motion is declared without intensity.
const DefaultMotionIntensity = 0.5

// Motion declares a per-scene camera movement preset.
// Intensity is a pointer so "absent" (default 0.5) is distinguishable from
// an explicit 0.0.
type Motion struct {
	Type      string   `yaml:"type" json:"type"`
	Intensity *float64 `yaml:"intensity" json:"intensity,omitempty"`
}

// Look declares a per-scene finishing preset (grain, white balance).
type Look struct {
	Grain        float64 `yaml:"grain" json:"grain"`
	WhiteBalance string  `yaml:"white_balance" json:"white_balance"`
}

// Scene represents a single scene entry.
// Seed is a pointer so that "absent" (nil) is distinguishable from an explicit 0.
// Variants is a pointer for the same reason: absent defaults to 1, while an
// explicit 0 (or negative) is a validation error.
// SilenceSeconds, when set, marks a silent scene (no narration pipeline).
// Lora is reserved (design D4): parsed, stored, and hashed, no behaviour yet.
type Scene struct {
	ID             string  `yaml:"id" json:"id"`
	Prompt         string  `yaml:"prompt" json:"prompt"`
	NegativePrompt string  `yaml:"negative_prompt" json:"negative_prompt"`
	Seed           *int    `yaml:"seed" json:"seed,omitempty"`
	TTSText        string  `yaml:"tts_text" json:"tts_text"`
	Speed          float64 `yaml:"speed" json:"speed"`
	// Voice is the Piper voice that narrates this scene; empty means defaults.voice.
	Voice string `yaml:"voice,omitempty" json:"voice,omitempty"`
	// Speaker is the speaker of the voice that narrates the scene when that model holds several.
	// Absent means the project's speaker for the project's voice, the first one for the scene's own voice.
	Speaker        *int     `yaml:"speaker,omitempty" json:"speaker,omitempty"`
	Variants       *int     `yaml:"variants" json:"variants,omitempty"`
	Lora           string   `yaml:"lora" json:"lora"`
	SilenceSeconds *float64 `yaml:"silence_seconds" json:"silence_seconds,omitempty"`
	Motion         *Motion  `yaml:"motion" json:"motion,omitempty"`
	Look           *Look    `yaml:"look" json:"look,omitempty"`
	// Animate opts the scene in to the animate phase; MotionPrompt, when
	// set, replaces the scene prompt as the video prompt.
	Animate      bool   `yaml:"animate" json:"animate,omitempty"`
	MotionPrompt string `yaml:"motion_prompt" json:"motion_prompt,omitempty"`
	// Image is the scene's own picture: a file in the project's sources
	// folder used instead of generating one from the prompt.
	Image string `yaml:"image,omitempty" json:"image,omitempty"`
	// EndImage is the picture the animated clip ends on (a file in the sources folder; needs Image, and an engine that
	// takes a last frame: minimax-h3 with first_frame, ltx-2.3). References are the pictures, videos and audios that
	// guide a clip made by minimax-h3's references bundle; Image is reference picture 1. The two do not combine.
	EndImage   string           `yaml:"end_image,omitempty" json:"end_image,omitempty"`
	References *SceneReferences `yaml:"references,omitempty" json:"references,omitempty"`
	// Clip is the scene's own video: a file in the project's sources folder
	// used instead of generating a picture and animating it.
	Clip string `yaml:"clip,omitempty" json:"clip,omitempty"`
	// ClipStart and ClipEnd pick the part of the clip the scene uses, in seconds
	// from the start of the file; absent means the start and the end of the file.
	ClipStart *float64 `yaml:"clip_start,omitempty" json:"clip_start,omitempty"`
	ClipEnd   *float64 `yaml:"clip_end,omitempty" json:"clip_end,omitempty"`
	// ClipSpeed is how fast the scene's video (own clip or animation) plays: 1 is its own
	// speed, 0.5 half, 2 double. Absent keeps the automatic slowdown.
	ClipSpeed *float64 `yaml:"clip_speed,omitempty" json:"clip_speed,omitempty"`
	// TransitionSeconds is the crossfade into this scene from the previous one; absent
	// means the project's, 0 a hard cut. The first scene's is ignored.
	TransitionSeconds *float64 `yaml:"transition_seconds,omitempty" json:"transition_seconds,omitempty"`
	// Transition is the kind of the transition into this scene; empty means the project's.
	Transition string `yaml:"transition,omitempty" json:"transition,omitempty"`
	// TransitionAccent is the glow colour of a shader transition into this scene; empty means the project's.
	TransitionAccent string `yaml:"transition_accent,omitempty" json:"transition_accent,omitempty"`
	// InitImage is a picture in the sources folder the generation starts from
	// (img2img); Strength says how far the result may depart from it.
	InitImage string   `yaml:"init_image,omitempty" json:"init_image,omitempty"`
	Strength  *float64 `yaml:"strength,omitempty" json:"strength,omitempty"`
	// Render and Animation override, for this scene only, how its image is
	// drawn and how it is animated. Absent means the project's settings.
	Render    *SceneRender    `yaml:"render,omitempty" json:"render,omitempty"`
	Animation *SceneAnimation `yaml:"animation,omitempty" json:"animation,omitempty"`
	// DescribedBy names the vision model that wrote the prompt from a reference
	// picture or clip, when it did. It is only a record: the reference itself is
	// never used to generate.
	DescribedBy string `yaml:"described_by,omitempty" json:"described_by,omitempty"`
	// MotionGraphic makes the scene an animated HTML page, written by the language model from
	// this description and rendered with Chrome, instead of a picture and an animation.
	MotionGraphic string `yaml:"motion_graphic,omitempty" json:"motion_graphic,omitempty"`
	// MotionPart makes the scene's page from a part of the catalog of animated parts instead of asking the model for
	// the whole page: the name of a part, or "auto" to let the model choose one from MotionGraphic. MotionParams are the
	// parameters of the part, MotionText a line shown in the verse layer for the whole scene and MotionBPM the tempo
	// of the scene's clock (the project's defaults.motion.bpm, or DefaultMotionBPM, when absent).
	MotionPart   string         `yaml:"motion_part,omitempty" json:"motion_part,omitempty"`
	MotionParams map[string]any `yaml:"motion_params,omitempty" json:"motion_params,omitempty"`
	MotionText   string         `yaml:"motion_text,omitempty" json:"motion_text,omitempty"`
	MotionBPM    *float64       `yaml:"motion_bpm,omitempty" json:"motion_bpm,omitempty"`
	// Overlay puts an animated title, lower third or credits over the scene's video.
	Overlay *Overlay `yaml:"overlay,omitempty" json:"overlay,omitempty"`
	// Layer makes a motion scene a transparent page laid over the project's video track (video_track) at its place in the output, instead of a
	// picture of its own.
	Layer bool `yaml:"layer,omitempty" json:"layer,omitempty"`
	// Blender makes the scene from a .blend file rendered by Blender instead of a picture and an animation.
	Blender *SceneBlender `yaml:"blender,omitempty" json:"blender,omitempty"`
}

// The tempo of a scene made from a part, in beats per minute, and the longest verse text.
const (
	DefaultMotionBPM = 120.0
	MinMotionBPM     = 40.0
	MaxMotionBPM     = 240.0
	MaxMotionText    = 120
)

// MotionDefaults is defaults.motion: what the scenes made from parts of the catalog share.
type MotionDefaults struct {
	// BPM is the tempo of the clock of a scene that does not give its own (DefaultMotionBPM when absent).
	BPM *float64 `yaml:"bpm,omitempty" json:"bpm,omitempty"`
	// Design is the look: the colours, fonts, modes and finishes of the text. Without it a fixed palette is
	// chosen from the words of subject_prompt.
	Design *MotionDesign `yaml:"design,omitempty" json:"design,omitempty"`
	// Song is an audio of the project's sources (.mp3 or .wav) that the scenes made from parts follow: their tempo and
	// beats come from the song, followed in its audio, each scene's clock starts where the scene starts in the song, and
	// the song is the soundtrack unless bgm says otherwise.
	Song string `yaml:"song,omitempty" json:"song,omitempty"`
}

// MotionDesign has the fields of the design of a music motion piece (see internal/music/musicmotion.Design).
type MotionDesign struct {
	Dark        string            `yaml:"dark" json:"dark"`
	Light       string            `yaml:"light" json:"light"`
	Accent      string            `yaml:"accent" json:"accent"`
	Display     string            `yaml:"display" json:"display"`
	Body        string            `yaml:"body" json:"body"`
	DefaultMode string            `yaml:"default_mode" json:"default_mode"`
	Modes       map[string]string `yaml:"modes,omitempty" json:"modes,omitempty"`
	Concept     string            `yaml:"concept,omitempty" json:"concept,omitempty"`
	VerseStyle  string            `yaml:"verse_style,omitempty" json:"verse_style,omitempty"`
	HitStyle    string            `yaml:"hit_style,omitempty" json:"hit_style,omitempty"`
}

// PartCatalog is the catalog of parts a motion scene can be made from. It lives in a package that imports this one (the
// music motion engine), so a Config carries it as an interface that the caller puts there (Config.Parts). With none, the
// name and the parameters of a part are not checked here (the run still refuses them).
type PartCatalog interface {
	// Check checks a part of the catalog and its parameters (nil when valid).
	Check(part string, params map[string]any) error
	// Parse reads the parameters of a part given as text (as a dataset gives them) as the types the part declares, and
	// refuses a part that is not in the catalog or a parameter the part does not have.
	Parse(part string, raw map[string]string) (map[string]any, error)
	// CheckDesign checks a motion design (nil when valid).
	CheckDesign(d MotionDesign) error
}

// IsMotionGraphic reports whether the scene is made from an animated page: written by the model from motion_graphic,
// or built from a part of the catalog.
func (s Scene) IsMotionGraphic() bool { return s.MotionGraphic != "" || s.MotionPart != "" }

// Overlay is a transparent animated page drawn over a scene.
type Overlay struct {
	// Text is the literal text to show, or a description of what to show.
	Text string `yaml:"text" json:"text"`
	// Start and End bound the overlay in seconds within the scene; absent means the start
	// and the end of the scene.
	Start *float64 `yaml:"start,omitempty" json:"start,omitempty"`
	End   *float64 `yaml:"end,omitempty" json:"end,omitempty"`
	// Position is where in the frame it goes: top, center or bottom (the default).
	Position string `yaml:"position,omitempty" json:"position,omitempty"`
}

// Overlay positions.
const (
	OverlayTop    = "top"
	OverlayCenter = "center"
	OverlayBottom = "bottom"
)

// ResolvedPosition is the overlay's position, bottom when unset.
func (o Overlay) ResolvedPosition() string {
	if o.Position == "" {
		return OverlayBottom
	}
	return o.Position
}

// ResolvedStart is the second of the scene the overlay starts at.
func (o Overlay) ResolvedStart() float64 {
	if o.Start == nil {
		return 0
	}
	return *o.Start
}

// SceneRender holds a scene's own render settings; each field is optional.
// CfgScale is a pointer so an explicit 0 differs from absent.
type SceneRender struct {
	Resolution string   `yaml:"resolution,omitempty" json:"resolution,omitempty"`
	Steps      int      `yaml:"steps,omitempty" json:"steps,omitempty"`
	CfgScale   *float64 `yaml:"cfg_scale,omitempty" json:"cfg_scale,omitempty"`
}

// SceneAnimation holds a scene's own Wan settings; each field is optional.
// The model, the frame rate and the rest stay the project's.
type SceneAnimation struct {
	Resolution string `yaml:"resolution,omitempty" json:"resolution,omitempty"`
	Frames     int    `yaml:"frames,omitempty" json:"frames,omitempty"`
	Steps      int    `yaml:"steps,omitempty" json:"steps,omitempty"`
}

// EffectiveRender is what a scene renders with once its own settings are laid
// over the project's. CfgScale is nil when nothing sets one (sd uses its own).
type EffectiveRender struct {
	Resolution string
	Steps      int
	CfgScale   *float64
}

// EffectiveRender resolves the scene's render settings against the defaults:
// the scene's value when set, otherwise the project's (the model set's cfg
// scale, when there is one).
func (s Scene) EffectiveRender(d Defaults) EffectiveRender {
	r := EffectiveRender{Resolution: d.Resolution, Steps: d.Steps}
	if d.ModelSet != nil {
		r.CfgScale = d.ModelSet.CfgScale
	}
	if s.Render == nil {
		return r
	}
	if s.Render.Resolution != "" {
		r.Resolution = s.Render.Resolution
	}
	if s.Render.Steps != 0 {
		r.Steps = s.Render.Steps
	}
	if s.Render.CfgScale != nil {
		r.CfgScale = s.Render.CfgScale
	}
	return r
}

// DefaultInitStrength is the img2img strength of a scene that sets none.
const DefaultInitStrength = 0.6

// ResolvedStrength returns the img2img strength.
func (s Scene) ResolvedStrength() float64 {
	if s.Strength == nil {
		return DefaultInitStrength
	}
	return *s.Strength
}

// imageExts are the picture formats a scene or a cover can be.
var imageExts = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".webp": true}

// ValidImageName reports whether name can be a scene picture: a plain
// png/jpg/jpeg/webp file name.
func ValidImageName(name string) bool {
	return sourceNamePattern.MatchString(name) && imageExts[strings.ToLower(filepath.Ext(name))]
}

// clipExts are the video formats a scene can bring.
var clipExts = map[string]bool{".mp4": true, ".mov": true, ".webm": true, ".mkv": true}

// ValidClipName reports whether name can be a scene clip: a plain
// mp4/mov/webm/mkv file name.
func ValidClipName(name string) bool {
	return sourceNamePattern.MatchString(name) && clipExts[strings.ToLower(filepath.Ext(name))]
}

// ValidSourceFile reports whether name can be stored in the sources folder:
// audio (mp3, wav), a picture, a video, a .blend file, a scene description (.scene.json), a font (.ttf, .otf) or a 3D model (.glb).
func ValidSourceFile(name string) bool {
	return ValidSourceName(name) || ValidImageName(name) || ValidClipName(name) || ValidBlendName(name) || ValidSceneSpecName(name) || ValidFontName(name) || ValidModelName(name)
}

// MotionType returns the scene's resolved motion type: static when the
// motion object (or its type) is absent.
func (s Scene) MotionType() string {
	if s.Motion == nil || s.Motion.Type == "" {
		return MotionStatic
	}
	return s.Motion.Type
}

// MotionIntensity returns the scene's resolved motion intensity, defaulting
// to DefaultMotionIntensity when unset.
func (s Scene) MotionIntensity() float64 {
	if s.Motion == nil || s.Motion.Intensity == nil {
		return DefaultMotionIntensity
	}
	return *s.Motion.Intensity
}

// LookGrain returns the scene's resolved grain amount (0.0 = no grain).
func (s Scene) LookGrain() float64 {
	if s.Look == nil {
		return 0.0
	}
	return s.Look.Grain
}

// LookWhiteBalance returns the scene's resolved white balance preset
// (neutral = no adjustment).
func (s Scene) LookWhiteBalance() string {
	if s.Look == nil || s.Look.WhiteBalance == "" {
		return WBNeutral
	}
	return s.Look.WhiteBalance
}

// IsSilent reports whether the scene declares silence_seconds.
func (s Scene) IsSilent() bool {
	return s.SilenceSeconds != nil
}

// SilenceDuration returns silence_seconds, or 0 for a narrated scene.
func (s Scene) SilenceDuration() float64 {
	if s.SilenceSeconds == nil {
		return 0
	}
	return *s.SilenceSeconds
}

// EffectiveVariants returns the scene's variant count, defaulting to 1 when
// the field is absent. Validate rejects explicit values < 1 before this is
// consulted.
func (s Scene) EffectiveVariants() int {
	if s.Variants == nil {
		return 1
	}
	return *s.Variants
}

// ResolvedSeed returns the scene's seed, or UnsetSeed (sd.cpp's "pick a
// random seed" sentinel, -1) when the field is absent from scenes.yaml.
func (s Scene) ResolvedSeed() int64 {
	if s.Seed == nil {
		return -1
	}
	return int64(*s.Seed)
}

// Load reads and parses the YAML file at path into a Config.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: cannot read %s: %w", path, err)
	}
	var cfg Config
	if err := checkAdult(data); err != nil {
		return nil, fmt.Errorf("config: %s: %w", path, err)
	}
	if err := checkClient(data); err != nil {
		return nil, fmt.Errorf("config: %s: %w", path, err)
	}
	if err := decodeStrict(data, &cfg); err != nil {
		return nil, fmt.Errorf("config: cannot parse %s: %w", path, err)
	}
	return &cfg, nil
}

// decodeStrict parses data into cfg and refuses a field the configuration does not define: a misspelt name
// (step: for steps:) would otherwise be dropped in silence and the run would use the defaults. The error names the
// field and its line.
func decodeStrict(data []byte, cfg *Config) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(cfg); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// checkAdult names the field when `adult` is not true or false: the YAML decoder alone only
// says which line could not become a bool.
func checkAdult(data []byte) error {
	var probe struct {
		Adult yaml.Node `yaml:"adult"`
	}
	if yaml.Unmarshal(data, &probe) != nil || probe.Adult.Kind == 0 {
		return nil // not readable (reported by the real parse) or absent
	}
	if probe.Adult.Kind != yaml.ScalarNode || probe.Adult.Tag != "!!bool" {
		return fmt.Errorf("adult must be true or false (or be left out), got %q", probe.Adult.Value)
	}
	return nil
}

// clientPattern is what a client name looks like: one to 32 lowercase letters, digits or hyphens.
var clientPattern = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)

// ClientPatternText is clientPattern as the API documents it.
const ClientPatternText = "^[a-z0-9-]{1,32}$"

// ValidClient says whether s is a client name.
func ValidClient(s string) bool { return clientPattern.MatchString(s) }

// checkClient names the field when `client` is present and is not a client name.
func checkClient(data []byte) error {
	var probe struct {
		Client yaml.Node `yaml:"client"`
	}
	if yaml.Unmarshal(data, &probe) != nil || probe.Client.Kind == 0 {
		return nil // not readable (reported by the real parse) or absent
	}
	if probe.Client.Kind != yaml.ScalarNode || !ValidClient(probe.Client.Value) {
		return fmt.Errorf("client must be one to 32 lowercase letters, digits or hyphens (or be left out), got %q", probe.Client.Value)
	}
	return nil
}

// LoadBytes parses in-memory YAML into a Config, with the same semantics as
// Load. It lets callers that hold candidate content (e.g. the HTTP server
// validating an editor save) run the exact pipeline parser without touching
// disk.
func LoadBytes(data []byte) (*Config, error) {
	var cfg Config
	if err := checkAdult(data); err != nil {
		return nil, fmt.Errorf("config: scenes.yaml: %w", err)
	}
	if err := checkClient(data); err != nil {
		return nil, fmt.Errorf("config: scenes.yaml: %w", err)
	}
	if err := decodeStrict(data, &cfg); err != nil {
		return nil, fmt.Errorf("config: cannot parse scenes.yaml: %w", err)
	}
	return &cfg, nil
}

// Music output formats accepted by defaults.music.output_format.
const (
	MusicFormatMP3 = "mp3"
	MusicFormatWAV = "wav"
)

// DefaultMusicModelsDir is where the music engine models live.
const DefaultMusicModelsDir = "models/music"

// Music holds the engine models and generation parameters shared by every
// track. Pointer fields distinguish "absent" (the engine's default) from an
// explicit zero; an unset value is never sent to the engine.
type Music struct {
	ModelsDir        string   `yaml:"models_dir,omitempty" json:"models_dir,omitempty"`
	LMModel          string   `yaml:"lm_model,omitempty" json:"lm_model,omitempty"`
	SynthModel       string   `yaml:"synth_model,omitempty" json:"synth_model,omitempty"`
	OutputFormat     string   `yaml:"output_format,omitempty" json:"output_format,omitempty"`
	MP3Bitrate       int      `yaml:"mp3_bitrate,omitempty" json:"mp3_bitrate,omitempty"`
	LMTemperature    *float64 `yaml:"lm_temperature,omitempty" json:"lm_temperature,omitempty"`
	LMCfgScale       *float64 `yaml:"lm_cfg_scale,omitempty" json:"lm_cfg_scale,omitempty"`
	LMTopP           *float64 `yaml:"lm_top_p,omitempty" json:"lm_top_p,omitempty"`
	LMTopK           *int     `yaml:"lm_top_k,omitempty" json:"lm_top_k,omitempty"`
	LMNegativePrompt string   `yaml:"lm_negative_prompt,omitempty" json:"lm_negative_prompt,omitempty"`
	InferenceSteps   int      `yaml:"inference_steps,omitempty" json:"inference_steps,omitempty"`
	GuidanceScale    *float64 `yaml:"guidance_scale,omitempty" json:"guidance_scale,omitempty"`
	Shift            *float64 `yaml:"shift,omitempty" json:"shift,omitempty"`
	Solver           string   `yaml:"solver,omitempty" json:"solver,omitempty"`
	VocalLanguage    string   `yaml:"vocal_language,omitempty" json:"vocal_language,omitempty"`
	// Vocals is the default voice of the tracks that set none (see VocalStyles).
	Vocals string `yaml:"vocals,omitempty" json:"vocals,omitempty"`
	// StylePrefix is put in front of every track's prompt to form its caption,
	// so a whole project can share a sound without repeating it.
	StylePrefix string `yaml:"style_prefix,omitempty" json:"style_prefix,omitempty"`
	// Adapter (a LoRA: a .safetensors file or a folder in the adapters
	// directory) and its strength apply to every track that sets none.
	Adapter      string   `yaml:"adapter,omitempty" json:"adapter,omitempty"`
	AdapterScale *float64 `yaml:"adapter_scale,omitempty" json:"adapter_scale,omitempty"`
	// Engine is how the engine programs run: "cli" (default) starts a program
	// per phase; "server" starts one ace-server for the run. KeepLoaded keeps
	// the server's models in GPU memory between jobs (default true).
	Engine     string `yaml:"engine,omitempty" json:"engine,omitempty"`
	KeepLoaded *bool  `yaml:"keep_loaded,omitempty" json:"keep_loaded,omitempty"`
	// Generator is what makes the songs: "ace-step" (default, the programs above), "heartmula" (the optional
	// HeartMuLa service, much better and about ten times slower; see internal/music/heartmula) or "diffrhythm2" (the
	// optional DiffRhythm2 service: English lyrics only, no instrumentals; see internal/music/diffrhythm2).
	Generator string `yaml:"generator,omitempty" json:"generator,omitempty"`
	// DR2CfgStrength and DR2Steps are the guidance and the sampling steps of the diffrhythm2 generator (the
	// model's own 2.0 and 16 when unset); no other generator reads them.
	DR2CfgStrength *float64 `yaml:"dr2_cfg_strength,omitempty" json:"dr2_cfg_strength,omitempty"`
	DR2Steps       *int     `yaml:"dr2_steps,omitempty" json:"dr2_steps,omitempty"`
	// Artist is written in the tags of every mp3 (the project's name when unset).
	Artist string `yaml:"artist,omitempty" json:"artist,omitempty"`
	// Cover is how the songs get their cover picture: "" or "off" none, "auto" one made
	// with the project's image model (defaults.model or model_set) from each song's style.
	Cover string `yaml:"cover,omitempty" json:"cover,omitempty"`
	// LoudnessLUFS evens the loudness of every exported song to this integrated level (for example -14, the
	// level of the streaming platforms); unset leaves each song as the engine made it.
	LoudnessLUFS *float64 `yaml:"loudness_lufs,omitempty" json:"loudness_lufs,omitempty"`
}

// Limits of defaults.music.loudness_lufs.
const (
	MinLoudnessLUFS = -30.0
	MaxLoudnessLUFS = -8.0
)

// Cover modes of defaults.music.cover.
const (
	CoverOff  = "off"
	CoverAuto = "auto"
)

// DefaultCoverResolution and DefaultCoverSteps are what a generated cover uses when the project sets none:
// album art is square, and 768x768 costs the same memory as 1024x576.
const (
	DefaultCoverResolution = "768x768"
	DefaultCoverSteps      = 20
)

// Engines of the music programs.
const (
	EngineCLI    = "cli"
	EngineServer = "server"
)

// Generators of the songs (defaults.music.generator).
const (
	GeneratorACEStep     = "ace-step"
	GeneratorHeartMuLa   = "heartmula"
	GeneratorDiffRhythm2 = "diffrhythm2"
)

// ResolvedGenerator returns the generator, ace-step when unset.
func (m *Music) ResolvedGenerator() string {
	if m == nil || m.Generator == "" {
		return GeneratorACEStep
	}
	return m.Generator
}

// UsesHeartMuLa reports whether the songs are made by the HeartMuLa service.
func (m *Music) UsesHeartMuLa() bool { return m.ResolvedGenerator() == GeneratorHeartMuLa }

// UsesDiffRhythm2 reports whether the songs are made by the DiffRhythm2 service.
func (m *Music) UsesDiffRhythm2() bool { return m.ResolvedGenerator() == GeneratorDiffRhythm2 }

// UsesService reports whether the songs are made by a generator that runs as a service in a container (HeartMuLa or
// DiffRhythm2): none of ACE-Step's programs, models or phases are part of them.
func (m *Music) UsesService() bool { return m.UsesHeartMuLa() || m.UsesDiffRhythm2() }

// Tags are the style words HeartMuLa takes, as one text in the form it wants: lower case, separated by commas,
// no spaces around the commas. YAML and JSON accept a text or a list of words and keep the text.
type Tags string

// NormalizeTags puts words into the form HeartMuLa takes: lower case, trimmed, separated by commas, without
// empty or repeated words.
func NormalizeTags(text string) Tags {
	seen := map[string]bool{}
	var out []string
	for _, w := range strings.Split(text, ",") {
		w = strings.ToLower(strings.Join(strings.Fields(w), " "))
		if w != "" && !seen[w] {
			seen[w] = true
			out = append(out, w)
		}
	}
	return Tags(strings.Join(out, ","))
}

// UnmarshalYAML accepts a text or a list of words.
func (t *Tags) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		*t = NormalizeTags(value.Value)
	case yaml.SequenceNode:
		var words []string
		if err := value.Decode(&words); err != nil {
			return err
		}
		*t = NormalizeTags(strings.Join(words, ","))
	default:
		return fmt.Errorf("tags must be a text or a list of words")
	}
	return nil
}

// UnmarshalJSON accepts a text or a list of words.
func (t *Tags) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		*t = NormalizeTags(text)
		return nil
	}
	var words []string
	if err := json.Unmarshal(data, &words); err != nil {
		return fmt.Errorf("tags must be a text or a list of words")
	}
	*t = NormalizeTags(strings.Join(words, ","))
	return nil
}

// ResolvedEngine returns the engine, cli when unset.
func (m *Music) ResolvedEngine() string {
	if m == nil || m.Engine == "" {
		return EngineCLI
	}
	return m.Engine
}

// ResolvedKeepLoaded reports whether the server keeps its models loaded.
func (m *Music) ResolvedKeepLoaded() bool { return m == nil || m.KeepLoaded == nil || *m.KeepLoaded }

// ResolvedModelsDir returns models_dir, or DefaultMusicModelsDir when unset.
func (m *Music) ResolvedModelsDir() string {
	if m == nil || m.ModelsDir == "" {
		return DefaultMusicModelsDir
	}
	return m.ModelsDir
}

// Default music models: the engine falls back to "the first match" when a
// model name is empty, which is not reproducible, so the orchestrator always
// names both.
const (
	DefaultMusicLM    = "acestep-5Hz-lm-1.7B-Q8_0.gguf"
	DefaultMusicSynth = "acestep-v15-xl-turbo-Q8_0.gguf"

	// MusicTextEncoderGlob is the pattern of the text encoder file, which the engine finds by name inside the models dir.
	MusicTextEncoderGlob = "Qwen3-Embedding*.gguf"
	// MusicVAEGlob is the pattern of the VAE file of the music engine, found by name inside the models dir.
	MusicVAEGlob = "vae*.gguf"
)

// ggufName returns name with the ".gguf" suffix the engine requires.
func ggufName(name string) string {
	if name != "" && !strings.HasSuffix(name, ".gguf") {
		return name + ".gguf"
	}
	return name
}

// ResolvedLM returns the language-model file name (default DefaultMusicLM).
func (m *Music) ResolvedLM() string {
	if m == nil || m.LMModel == "" {
		return DefaultMusicLM
	}
	return ggufName(m.LMModel)
}

// ResolvedSynth returns the synthesis-model file name (default DefaultMusicSynth).
func (m *Music) ResolvedSynth() string {
	if m == nil || m.SynthModel == "" {
		return DefaultMusicSynth
	}
	return ggufName(m.SynthModel)
}

// MP3BitrateOrDefault returns mp3_bitrate, or the engine's 128 kbps default.
func (m *Music) MP3BitrateOrDefault() int {
	if m == nil || m.MP3Bitrate <= 0 {
		return 128
	}
	return m.MP3Bitrate
}

// ResolvedOutputFormat returns output_format, or mp3 when unset.
func (m *Music) ResolvedOutputFormat() string {
	if m == nil || m.OutputFormat == "" {
		return MusicFormatMP3
	}
	return m.OutputFormat
}

// Track is one song request of a music project. Seed, Variants, UseLM and
// the numeric metadata are pointers so "absent" differs from an explicit
// zero/false.
type Track struct {
	ID              string   `yaml:"id" json:"id"`
	Prompt          string   `yaml:"prompt" json:"prompt"`
	Lyrics          string   `yaml:"lyrics,omitempty" json:"lyrics,omitempty"`
	Instrumental    bool     `yaml:"instrumental,omitempty" json:"instrumental,omitempty"`
	DurationSeconds *float64 `yaml:"duration_seconds,omitempty" json:"duration_seconds,omitempty"`
	BPM             *int     `yaml:"bpm,omitempty" json:"bpm,omitempty"`
	KeyScale        string   `yaml:"keyscale,omitempty" json:"keyscale,omitempty"`
	TimeSignature   string   `yaml:"timesignature,omitempty" json:"timesignature,omitempty"`
	VocalLanguage   string   `yaml:"vocal_language,omitempty" json:"vocal_language,omitempty"`
	Vocals          string   `yaml:"vocals,omitempty" json:"vocals,omitempty"`
	Seed            *int     `yaml:"seed,omitempty" json:"seed,omitempty"`
	Variants        *int     `yaml:"variants,omitempty" json:"variants,omitempty"`
	UseLM           *bool    `yaml:"use_lm,omitempty" json:"use_lm,omitempty"`
	// Title is written in the tags of the song (the id, spaced, when unset).
	Title string `yaml:"title,omitempty" json:"title,omitempty"`
	// Cover overrides the project's: "off" for no cover, or a picture in sources/ to use as it is.
	Cover string `yaml:"cover,omitempty" json:"cover,omitempty"`
	// Adapter and AdapterScale override the project's default for this track.
	Adapter      string   `yaml:"adapter,omitempty" json:"adapter,omitempty"`
	AdapterScale *float64 `yaml:"adapter_scale,omitempty" json:"adapter_scale,omitempty"`
	// Task turns the track into a transformation of an existing song, named by
	// Source (a file in the project's sources folder). The other fields belong
	// to one task each.
	Task string `yaml:"task,omitempty" json:"task,omitempty"`
	// Reference is a file in the sources folder whose sound a new song takes
	// as timbre reference; a track with a task does not use it.
	Reference     string   `yaml:"reference,omitempty" json:"reference,omitempty"`
	Source        string   `yaml:"source,omitempty" json:"source,omitempty"`
	CoverStrength *float64 `yaml:"cover_strength,omitempty" json:"cover_strength,omitempty"`
	RepaintStart  *float64 `yaml:"repaint_start,omitempty" json:"repaint_start,omitempty"`
	RepaintEnd    *float64 `yaml:"repaint_end,omitempty" json:"repaint_end,omitempty"`
	Stem          string   `yaml:"stem,omitempty" json:"stem,omitempty"`
	// Tags are the style words of the HeartMuLa generator ("piano,happy"); empty, they can be derived from
	// the prompt with the assistant. ACE-Step does not use them.
	Tags Tags `yaml:"tags,omitempty" json:"tags,omitempty"`
}

// BGM is the background music of a video project: a soundtrack generated from
// Prompt, or the audio file File (a name in the project's sources folder),
// mixed under the narration.
type BGM struct {
	Prompt string `yaml:"prompt,omitempty" json:"prompt,omitempty"`
	File   string `yaml:"file,omitempty" json:"file,omitempty"`
	// Volume of the music before ducking, 0..1 (default 0.25).
	Volume *float64 `yaml:"volume,omitempty" json:"volume,omitempty"`
	// FadeSeconds of the fade in and out (default 2).
	FadeSeconds *float64 `yaml:"fade_seconds,omitempty" json:"fade_seconds,omitempty"`
	// Ducking lowers the music while the narration speaks (default true).
	Ducking *bool `yaml:"ducking,omitempty" json:"ducking,omitempty"`
	// Seed of a generated soundtrack; absent derives it from the prompt.
	Seed *int `yaml:"seed,omitempty" json:"seed,omitempty"`
}

// Defaults of the mix.
const (
	DefaultBGMVolume = 0.25
	DefaultBGMFade   = 2.0
)

// ResolvedVolume returns the music volume.
func (b *BGM) ResolvedVolume() float64 {
	if b.Volume == nil {
		return DefaultBGMVolume
	}
	return *b.Volume
}

// ResolvedFade returns the fade length in seconds.
func (b *BGM) ResolvedFade() float64 {
	if b.FadeSeconds == nil {
		return DefaultBGMFade
	}
	return *b.FadeSeconds
}

// ResolvedDucking reports whether the narration lowers the music.
func (b *BGM) ResolvedDucking() bool { return b.Ducking == nil || *b.Ducking }

// Generated reports whether the soundtrack is made from a prompt.
func (b *BGM) Generated() bool { return b != nil && b.Prompt != "" }

// ResolvedSeed returns the seed of a generated soundtrack: the explicit one,
// else a stable value derived from the prompt, so the same prompt gives the
// same music and the cache holds.
func (b *BGM) ResolvedSeed() int64 {
	if b.Seed != nil {
		return int64(*b.Seed)
	}
	sum := sha256.Sum256([]byte(b.Prompt))
	return int64(binary.BigEndian.Uint32(sum[:4]))
}

// Tasks a track can run on an existing song.
const (
	TaskCover      = "cover"
	TaskCoverNoFSQ = "cover-nofsq"
	TaskRepaint    = "repaint"
	TaskExtract    = "extract"
	SourcesSubdir  = "sources"
	MaxSourceBytes = 256 << 20
)

// MusicStems are the instrument and voice names the engine can extract.
var MusicStems = []string{"vocals", "backing_vocals", "drums", "bass", "guitar", "keyboard", "percussion", "strings", "synth", "fx", "brass", "woodwinds"}

// IsCover reports whether the task is one of the two cover variants.
func (t Track) IsCover() bool { return t.Task == TaskCover || t.Task == TaskCoverNoFSQ }

// ValidSourceName reports whether name can be a source: a plain mp3 or wav
// file name, the only formats the engine reads.
func ValidSourceName(name string) bool {
	if !sourceNamePattern.MatchString(name) {
		return false
	}
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".mp3" || ext == ".wav"
}

// AdaptersSubdir is where the adapters live, inside the models directory.
const AdaptersSubdir = "adapters"

// DefaultAdapterScale is the strength of an adapter that sets none.
const DefaultAdapterScale = 1.0

// AdaptersDir returns the directory of the installed adapters.
func (m *Music) AdaptersDir() string {
	return filepath.Join(m.ResolvedModelsDir(), AdaptersSubdir)
}

// ResolveAdapter returns the adapter a track uses and its strength: the
// track's own values, else the project's defaults. An empty name means none.
func (c *Config) ResolveAdapter(t Track) (name string, scale float64) {
	m := c.Defaults.Music
	name = t.Adapter
	if name == "" && m != nil {
		name = m.Adapter
	}
	scale = DefaultAdapterScale
	switch {
	case t.AdapterScale != nil:
		scale = *t.AdapterScale
	case m != nil && m.AdapterScale != nil:
		scale = *m.AdapterScale
	}
	return name, scale
}

// Caption returns the caption the engine receives for the track: its prompt,
// preceded by the project's style prefix when there is one. Captions are lists
// of descriptors, so the two are joined with a comma.
func (m *Music) Caption(prompt string) string {
	if m == nil || strings.TrimSpace(m.StylePrefix) == "" {
		return prompt
	}
	return strings.TrimSpace(m.StylePrefix) + ", " + prompt
}

// HasLyrics reports whether the track brings lyrics of its own to be sung.
func (t Track) HasLyrics() bool {
	return !t.Instrumental && strings.TrimSpace(t.Lyrics) != ""
}

// EffectiveUseLM resolves use_lm: an explicit value wins; otherwise the
// language model runs unless the track brings lyrics of its own, which are
// then sung verbatim. Instrumental tracks have no lyrics, so they use it.
func (t Track) EffectiveUseLM() bool {
	if t.Task != "" {
		return false // the source gives the structure: nothing to compose
	}
	if t.UseLM != nil {
		return *t.UseLM
	}
	return !t.HasLyrics()
}

// DisplayTitle is the title the tags carry: the track's own, or its id with spaces for '_' and '-'.
func (t Track) DisplayTitle() string {
	if s := strings.TrimSpace(t.Title); s != "" {
		return s
	}
	return strings.Join(strings.Fields(strings.NewReplacer("_", " ", "-", " ").Replace(t.ID)), " ")
}

// CoverMode says how a track gets its cover: CoverOff, CoverAuto, or "file" (a picture of sources/ the track names).
// The track's own value wins over the project's.
func (c *Config) CoverMode(t Track) string {
	switch {
	case t.Cover == CoverOff:
		return CoverOff
	case t.Cover != "":
		return "file"
	case c.Defaults.Music != nil && c.Defaults.Music.Cover == CoverAuto:
		return CoverAuto
	}
	return CoverOff
}

// EffectiveVariants returns the variant count, defaulting to 1.
func (t Track) EffectiveVariants() int {
	if t.Variants == nil {
		return 1
	}
	return *t.Variants
}

// ResolvedSeed returns the explicit seed, or -1 when the track has none.
func (t Track) ResolvedSeed() int64 {
	if t.Seed == nil {
		return -1
	}
	return int64(*t.Seed)
}

// UsesLM reports whether any track runs the language model.
func (c *Config) UsesLM() bool {
	if c.Defaults.Music.UsesService() {
		return false // these generators write the song themselves: no ACE-Step language model
	}
	for _, t := range c.Tracks {
		if t.EffectiveUseLM() {
			return true
		}
	}
	return false
}

// VariantAudioName returns the exported file name for variant i (0-based) of
// a track with `variants` variants: <id>.<ext> for one, <id>_v<N>.<ext>
// (N 1-based) otherwise.
func VariantAudioName(trackID string, variants, i int, ext string) string {
	if variants == 1 {
		return trackID + "." + ext
	}
	return fmt.Sprintf("%s_v%d.%s", trackID, i+1, ext)
}

var sourceNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

var trackIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// ValidTrackID reports whether id can name a track (it becomes a file name).
func ValidTrackID(id string) bool { return trackIDPattern.MatchString(id) }

// ExportDir returns the images- and music-mode output directory, out/<project>.
func (c *Config) ExportDir() string {
	return filepath.Join("out", c.Project)
}

// VariantPNGName returns the exported file name for variant index i (0-based)
// of a scene rendering `variants` images: <id>.png for a single variant,
// <id>_v<N>.png (N 1-based) otherwise.
func VariantPNGName(sceneID string, variants, i int) string {
	if variants == 1 {
		return sceneID + ".png"
	}
	return fmt.Sprintf("%s_v%d.png", sceneID, i+1)
}

func (c *Config) warn(format string, args ...any) {
	c.Warnings = append(c.Warnings, fmt.Sprintf(format, args...))
}

// Validate performs all pre-subprocess checks and resolves the output mode
// and the deprecated output_file alias. The invariants are mode-dependent:
//
// Both modes:
//   - output.mode, when set, is "video" or "images" (absent defaults to video)
//   - output_file and output.file, when both set, must be identical
//   - referenced input files (model, voice) exist on disk
//   - scene IDs are unique, prompts are non-empty, variants (if set) >= 1
//
// Video mode additionally requires a non-empty tts_text per scene and a
// resolved output file path; variants != 1 is accepted with a warning.
//
// Images mode treats tts_text/speed as optional (warning when present),
// ignores output.file, and rejects colliding resolved export paths.
//
// Non-fatal findings (deprecations, ignored fields) are collected into
// c.Warnings for the CLI to print to stderr. It returns a single aggregated
// error (or nil) and never invokes an external binary.
func (c *Config) Validate() error {
	return c.validate("")
}

// ValidateInDir behaves exactly like Validate but resolves the relative
// model/voice file-existence checks against dir instead of the process
// cwd, so a caller managing project directories (the HTTP server) can
// validate a project from anywhere. Absolute paths are checked as-is.
func (c *Config) ValidateInDir(dir string) error {
	return c.validate(dir)
}

func (c *Config) validate(statBase string) error {
	statPath := func(p string) string {
		if statBase == "" || filepath.IsAbs(p) {
			return p
		}
		return filepath.Join(statBase, p)
	}
	c.Warnings = nil
	var errs []error
	errs = append(errs, checkPaths(c)...)
	errs = append(errs, checkBounds(c)...)

	// --- output.mode defaulting & validation ---
	if c.Output.Mode == "" {
		c.Output.Mode = ModeVideo
	}
	modeValid := c.Output.Mode == ModeVideo || c.Output.Mode == ModeImages || c.Output.Mode == ModeMusic || c.Output.Mode == ModeVideoEdit || c.Output.Mode == ModeModels3D
	if !modeValid {
		errs = append(errs, fmt.Errorf("config: output.mode %q is invalid: must be %q, %q, %q, %q or %q", c.Output.Mode, ModeVideo, ModeImages, ModeMusic, ModeVideoEdit, ModeModels3D))
	}
	if c.VideoEdit != nil && c.Output.Mode != ModeVideoEdit {
		errs = append(errs, fmt.Errorf("config: video_edit is only valid with output.mode %q (mode is %q)", ModeVideoEdit, c.Output.Mode))
	}

	// --- deprecated output_file alias resolution ---
	if c.OutputFile != "" {
		if c.Output.File != "" && c.Output.File != c.OutputFile {
			errs = append(errs, fmt.Errorf("config: output_file (%q) and output.file (%q) differ: set only one", c.OutputFile, c.Output.File))
		} else {
			c.Output.File = c.OutputFile
			c.warn("output_file is deprecated: use output.file instead")
		}
	}

	// --- referenced input files must exist ---
	if c.Defaults.Model != "" {
		if _, err := os.Stat(statPath(c.Defaults.Model)); err != nil {
			errs = append(errs, fmt.Errorf("config: defaults.model %q does not exist: %w", c.Defaults.Model, err))
		}
	}
	if ms := c.Defaults.ModelSet; ms != nil {
		if c.Defaults.Model != "" {
			errs = append(errs, fmt.Errorf("config: defaults.model and defaults.model_set are both set: use only one"))
		}
		files := []struct{ name, path string }{
			{"defaults.model_set.diffusion_model", ms.DiffusionModel},
			{"defaults.model_set.vae", ms.VAE},
		}
		switch {
		case ms.QwenEdit():
			files = append(files, struct{ name, path string }{"defaults.model_set.llm", ms.LLM}, struct{ name, path string }{"defaults.model_set.llm_vision", ms.LLMVision})
			if ms.ClipL != "" || ms.T5XXL != "" {
				errs = append(errs, fmt.Errorf("config: defaults.model_set.clip_l and t5xxl belong to the FLUX editing family, not to one with llm and llm_vision: use one family"))
			}
		case ms.Edit:
			files = append(files, struct{ name, path string }{"defaults.model_set.clip_l", ms.ClipL}, struct{ name, path string }{"defaults.model_set.t5xxl", ms.T5XXL})
		default:
			files = append(files, struct{ name, path string }{"defaults.model_set.llm", ms.LLM})
			if ms.ClipL != "" || ms.T5XXL != "" || ms.LLMVision != "" {
				errs = append(errs, fmt.Errorf("config: defaults.model_set.clip_l, t5xxl and llm_vision belong to an editing model: set defaults.model_set.edit or remove them"))
			}
		}
		for _, f := range files {
			if f.path == "" {
				errs = append(errs, fmt.Errorf("config: %s is required", f.name))
			} else if _, err := os.Stat(statPath(f.path)); err != nil {
				errs = append(errs, fmt.Errorf("config: %s %q does not exist: %w", f.name, f.path, err))
			}
		}
		if ms.CfgScale != nil && *ms.CfgScale < 0 {
			errs = append(errs, fmt.Errorf("config: defaults.model_set.cfg_scale %v must not be negative", *ms.CfgScale))
		}
	}
	if a := c.Defaults.Animate; a != nil {
		if a.Engine != "" && a.Engine != EngineWan && a.Engine != EngineAnimateDiff && a.Engine != EngineMiniMaxH3 && a.Engine != EngineLTX {
			errs = append(errs, fmt.Errorf("config: defaults.animate.engine %q must be %q, %q, %q or %q", a.Engine, EngineWan, EngineAnimateDiff, EngineMiniMaxH3, EngineLTX))
		}
		errs = append(errs, validateAnimateSettings(a.Resolved())...)
		if a.Frames < 0 || a.FPS < 0 || a.Steps < 0 {
			errs = append(errs, fmt.Errorf("config: defaults.animate frames, fps and steps must not be negative"))
		}
		if a.FirstFrame && !a.IsMiniMaxH3() {
			errs = append(errs, fmt.Errorf("config: defaults.animate.first_frame only applies to the minimax-h3 engine"))
		}
		if a.ModelAudioSpeech && !a.ModelAudio {
			errs = append(errs, fmt.Errorf("config: defaults.animate.model_audio_speech needs defaults.animate.model_audio"))
		}
		if a.ModelAudio && !a.IsMiniMaxH3() && !a.IsLTX() {
			errs = append(errs, fmt.Errorf("config: defaults.animate.model_audio only applies to the %s and %s engines", EngineMiniMaxH3, EngineLTX))
		}
		if a.Connectors != "" && !a.IsLTX() {
			errs = append(errs, fmt.Errorf("config: defaults.animate.connectors only applies to the ltx-2.3 engine"))
		}
		if a.IsLTX() && a.LoRA != "" {
			errs = append(errs, fmt.Errorf("config: defaults.animate.lora does not apply to the ltx-2.3 engine"))
		}
		if !a.IsAnimateDiff() && !a.IsMiniMaxH3() && !a.IsLTX() && !a.TwoExpert() && (a.HighNoiseSteps != 0 || a.HighNoiseCfgScale != nil) {
			errs = append(errs, fmt.Errorf("config: defaults.animate.high_noise_steps and high_noise_cfg_scale need defaults.animate.high_noise_diffusion_model"))
		}
	}
	if c.Defaults.Voice != "" {
		errs = append(errs, checkVoice("defaults.voice", c.Defaults.Voice, statPath)...)
		errs = append(errs, checkSpeaker("defaults.voice_speaker", c.Defaults.Voice, c.Defaults.VoiceSpeaker, statPath)...)
	}
	for _, sc := range c.Scenes {
		if sc.Voice != "" {
			errs = append(errs, checkVoice(fmt.Sprintf("scene %q voice", sc.ID), sc.Voice, statPath)...)
		}
		if v := c.VoiceFor(sc); v != "" && sc.Speaker != nil {
			errs = append(errs, checkSpeaker(fmt.Sprintf("scene %q speaker", sc.ID), v, c.SpeakerFor(sc), statPath)...)
		}
	}

	// --- scene IDs must be unique ---
	seen := make(map[string]int, len(c.Scenes)) // id -> first index
	for i, s := range c.Scenes {
		if prev, dup := seen[s.ID]; dup {
			errs = append(errs, fmt.Errorf("config: duplicate scene id %q (first at index %d, again at index %d)", s.ID, prev, i))
		} else {
			seen[s.ID] = i
		}
	}

	// The engine decides which rules a scene's own animation settings follow.
	engine := EngineWan
	if a := c.Defaults.Animate; a != nil && a.IsAnimateDiff() {
		engine = EngineAnimateDiff
	} else if a != nil && a.IsMiniMaxH3() {
		engine = EngineMiniMaxH3
	} else if a != nil && a.IsLTX() {
		engine = EngineLTX
	}

	// --- per-scene invariants (common to both modes) ---
	for _, s := range c.Scenes {
		if s.Prompt == "" && s.Image == "" && s.Clip == "" && strings.TrimSpace(s.MotionGraphic) == "" && s.MotionPart == "" && s.Blender == nil {
			errs = append(errs, fmt.Errorf("config: scene %q has an empty prompt", s.ID))
		}
		if s.Variants != nil && *s.Variants < 1 {
			errs = append(errs, fmt.Errorf("config: scene %q: variants must be a positive integer >= 1, got %d", s.ID, *s.Variants))
		}
		if s.Motion != nil {
			switch s.MotionType() {
			case MotionStatic, MotionZoomIn, MotionZoomOut, MotionPanLeft, MotionPanRight, MotionHandheld, MotionRotateCW, MotionRotateCCW:
			default:
				errs = append(errs, fmt.Errorf("config: scene %q: motion.type %q is invalid: must be one of %q, %q, %q, %q, %q, %q, %q, %q",
					s.ID, s.Motion.Type, MotionStatic, MotionZoomIn, MotionZoomOut, MotionPanLeft, MotionPanRight, MotionHandheld, MotionRotateCW, MotionRotateCCW))
			}
			if i := s.MotionIntensity(); i < 0 || i > 1 {
				errs = append(errs, fmt.Errorf("config: scene %q: motion.intensity must be within [0.0, 1.0], got %v", s.ID, i))
			}
		}
		if s.Look != nil {
			if g := s.Look.Grain; g < 0 || g > 1 {
				errs = append(errs, fmt.Errorf("config: scene %q: look.grain must be within [0.0, 1.0], got %v", s.ID, g))
			}
			switch s.LookWhiteBalance() {
			case WBNeutral, WBWarm, WBCool, WBFluorescent:
			default:
				errs = append(errs, fmt.Errorf("config: scene %q: look.white_balance %q is invalid: must be one of %q, %q, %q, %q",
					s.ID, s.Look.WhiteBalance, WBNeutral, WBWarm, WBCool, WBFluorescent))
			}
		}
		if s.TransitionSeconds != nil && (*s.TransitionSeconds < 0 || *s.TransitionSeconds > MaxTransitionSeconds) {
			errs = append(errs, fmt.Errorf("config: scene %q: transition_seconds must be between 0 and %v, got %v", s.ID, MaxTransitionSeconds, *s.TransitionSeconds))
		}
		errs = append(errs, validateSceneQuality(s, engine)...)
		if s.SilenceSeconds != nil {
			if *s.SilenceSeconds <= 0 {
				errs = append(errs, fmt.Errorf("config: scene %q: silence_seconds must be > 0, got %v", s.ID, *s.SilenceSeconds))
			}
			if s.TTSText != "" {
				errs = append(errs, fmt.Errorf("config: scene %q: silence_seconds and a non-empty tts_text are mutually exclusive: a scene is either narrated or silent", s.ID))
			}
		}
	}

	if c.Defaults.Transition != "" && !IsTransition(c.Defaults.Transition) {
		errs = append(errs, fmt.Errorf("config: defaults.transition %q is not one of %s or shader:<%s>", c.Defaults.Transition, strings.Join(Transitions, ", "), strings.Join(ShaderTransitions, "|")))
	}
	if c.Defaults.TransitionAccent != "" && !validAccent(c.Defaults.TransitionAccent) {
		errs = append(errs, fmt.Errorf("config: defaults.transition_accent %q is not a colour of the form #rrggbb or #rgb", c.Defaults.TransitionAccent))
	}
	for _, s := range c.Scenes {
		if s.Transition != "" && !IsTransition(s.Transition) {
			errs = append(errs, fmt.Errorf("config: scene %q: transition %q is not one of %s or shader:<%s>", s.ID, s.Transition, strings.Join(Transitions, ", "), strings.Join(ShaderTransitions, "|")))
		}
		if s.TransitionAccent != "" && !validAccent(s.TransitionAccent) {
			errs = append(errs, fmt.Errorf("config: scene %q: transition_accent %q is not a colour of the form #rrggbb or #rgb", s.ID, s.TransitionAccent))
		}
	}
	if c.Defaults.TransitionSeconds < 0 || c.Defaults.TransitionSeconds > MaxTransitionSeconds {
		errs = append(errs, fmt.Errorf("config: defaults.transition_seconds must be between 0 and %v, got %v", MaxTransitionSeconds, c.Defaults.TransitionSeconds))
	}

	if !modeValid {
		return errors.Join(errs...)
	}

	if c.BGM != nil {
		errs = append(errs, c.validateBGM(statPath)...)
	}

	if c.Output.Mode != ModeVideo {
		if c.Defaults.TransitionSeconds != 0 || c.Defaults.Transition != "" {
			errs = append(errs, fmt.Errorf("config: defaults.transition_seconds only works in video mode (mode is %q)", c.Output.Mode))
		}
		for _, s := range c.Scenes {
			if s.TransitionSeconds != nil || s.Transition != "" {
				errs = append(errs, fmt.Errorf("config: scene %q: transition_seconds only works in video mode (mode is %q)", s.ID, c.Output.Mode))
			}
			if s.Image != "" {
				errs = append(errs, fmt.Errorf("config: scene %q: image only works in video mode (mode is %q)", s.ID, c.Output.Mode))
			}
			if s.Clip != "" {
				errs = append(errs, fmt.Errorf("config: scene %q: clip only works in video mode (mode is %q)", s.ID, c.Output.Mode))
			}
			if s.MotionGraphic != "" {
				errs = append(errs, fmt.Errorf("config: scene %q: motion_graphic only works in video mode (mode is %q)", s.ID, c.Output.Mode))
			}
			if s.MotionPart != "" {
				errs = append(errs, fmt.Errorf("config: scene %q: motion_part only works in video mode (mode is %q)", s.ID, c.Output.Mode))
			}
			if s.Blender != nil {
				errs = append(errs, fmt.Errorf("config: scene %q: blender only works in video mode (mode is %q)", s.ID, c.Output.Mode))
			}
			if s.Overlay != nil {
				errs = append(errs, fmt.Errorf("config: scene %q: overlay only works in video mode (mode is %q)", s.ID, c.Output.Mode))
			}
		}
	}

	if c.Output.Mode != ModeMusic && len(c.Tracks) > 0 {
		errs = append(errs, fmt.Errorf("config: tracks is only valid with output.mode %q (mode is %q)", ModeMusic, c.Output.Mode))
	}
	if c.Output.Mode != ModeModels3D && len(c.Models) > 0 {
		errs = append(errs, fmt.Errorf("config: models is only valid with output.mode %q (mode is %q)", ModeModels3D, c.Output.Mode))
	}
	if c.Output.Mode != ModeModels3D && c.Defaults.Models3D != nil {
		errs = append(errs, fmt.Errorf("config: defaults.models3d is only valid with output.mode %q (mode is %q)", ModeModels3D, c.Output.Mode))
	}
	if c.Output.Mode != ModeMusic && len(c.Mixes) > 0 {
		errs = append(errs, fmt.Errorf("config: mixes is only valid with output.mode %q (mode is %q)", ModeMusic, c.Output.Mode))
	}

	switch c.Output.Mode {
	case ModeModels3D:
		errs = append(errs, c.validateModels3D(statPath)...)
	case ModeVideoEdit:
		errs = append(errs, c.validateVideoEdit(statPath)...)
	case ModeMusic:
		errs = append(errs, c.validateMusic(statPath)...)
		errs = append(errs, c.validateFavorites()...)
	case ModeVideo:
		if c.Output.File == "" {
			errs = append(errs, fmt.Errorf("config: output_file is required: a video project must declare its output path (output.file)"))
		}
		errs = append(errs, c.validateMotionDefaults(statPath)...)
		for _, s := range c.Scenes {
			errs = append(errs, c.validateSceneImage(s, statPath)...)
			errs = append(errs, c.validateSceneInputs(s, statPath)...)
			errs = append(errs, c.validateSceneClip(s, statPath)...)
			errs = append(errs, c.validateMotionGraphics(s)...)
			errs = append(errs, c.validateSceneBlender(s, statPath)...)
			errs = append(errs, c.validateInitImage(s, statPath)...)
			if s.TTSText == "" && !s.IsSilent() {
				errs = append(errs, fmt.Errorf("config: scene %q has an empty tts_text", s.ID))
			}
			if s.Variants != nil && *s.Variants != 1 {
				c.warn("scene %q: variants has no effect in video mode (only one still feeds the clip)", s.ID)
			}
			if s.Animate && s.MotionType() != MotionStatic {
				c.warn("scene %q: motion is ignored because the scene is animated", s.ID)
			}
		}
		errs = append(errs, c.validateVideoTrack(statPath)...)
		errs = append(errs, c.validateAnimateUse(statPath)...)
		errs = append(errs, c.validateBrowserForMotion()...)
		errs = append(errs, c.validateBlenderAvailable()...)
	case ModeImages:
		if len(c.VideoTrack) > 0 {
			errs = append(errs, fmt.Errorf("config: video_track only works in video mode (mode is %q)", c.Output.Mode))
		}
		for _, s := range c.Scenes {
			errs = append(errs, c.validateInitImage(s, statPath)...)
			// Fields of the other modes (tts_text, speed, motion...) are ignored here without a warning: a project
			// that was a video before keeps them and the warning is only noise.
		}
		// Resolved export paths must not collide (e.g. scene "a_v1" with one
		// variant vs scene "a" with two variants both produce a_v1.png).
		paths := make(map[string]string) // resolved path -> scene id
		for _, s := range c.Scenes {
			n := s.EffectiveVariants()
			if n < 1 {
				continue // already reported above
			}
			for i := 0; i < n; i++ {
				p := filepath.Join(c.ExportDir(), VariantPNGName(s.ID, n, i))
				if other, dup := paths[p]; dup && other != s.ID {
					errs = append(errs, fmt.Errorf("config: scenes %q and %q both resolve to output path %q", other, s.ID, p))
				}
				paths[p] = s.ID
			}
		}
	}

	return errors.Join(errs...)
}

// validateVideoEdit checks a video-edit project: a known tool, settings inside its limits, and the inputs the tool
// needs in the sources folder (the video, or a motion of the library for motion control; the picture when the tool
// takes one and none otherwise).
func (c *Config) validateVideoEdit(statPath func(string) string) []error {
	var errs []error
	if len(c.Scenes) > 0 {
		errs = append(errs, fmt.Errorf("config: output.mode %q has no scenes: it edits one video (video_edit)", ModeVideoEdit))
	}
	ve := c.VideoEdit
	if ve == nil {
		return append(errs, fmt.Errorf("config: output.mode %q needs a video_edit block", ModeVideoEdit))
	}
	tool, ok := videoedit.Lookup(ve.Tool)
	if !ok {
		return append(errs, fmt.Errorf("config: video_edit.tool %q is unknown: use one of %s", ve.Tool, strings.Join(videoedit.IDs(), ", ")))
	}
	if _, err := videoedit.ValidateSettings(ve.Tool, ve.Settings); err != nil {
		errs = append(errs, fmt.Errorf("config: video_edit: %w", err))
	}
	source := func(field, name string, valid func(string) bool, what string) {
		if !valid(name) {
			errs = append(errs, fmt.Errorf("config: video_edit.%s %q must be %s", field, name, what))
			return
		}
		if _, err := os.Stat(statPath(filepath.Join(SourcesSubdir, name))); err != nil {
			errs = append(errs, fmt.Errorf("config: video_edit.%s %q not found in %s", field, name, SourcesSubdir))
		}
	}
	switch {
	case ve.Motion != "" && ve.Source != "":
		errs = append(errs, fmt.Errorf("config: video_edit takes a source or a motion, not both"))
	case ve.Motion != "":
		if !tool.AcceptsMotion {
			errs = append(errs, fmt.Errorf("config: video_edit.motion: the tool %s does not take a motion", tool.ID))
		}
	case ve.Source == "":
		errs = append(errs, fmt.Errorf("config: video_edit.source is required: the video to edit"))
	default:
		source("source", ve.Source, ValidClipName, "a .mp4, .mov, .webm or .mkv file")
	}
	switch {
	case ve.Image != "" && !tool.NeedsImage:
		errs = append(errs, fmt.Errorf("config: video_edit.image: the tool %s does not take a reference picture", tool.ID))
	case ve.Image == "" && tool.NeedsImage:
		errs = append(errs, fmt.Errorf("config: video_edit.image is required by the tool %s", tool.ID))
	case ve.Image != "":
		source("image", ve.Image, ValidImageName, "a .png, .jpg, .jpeg or .webp file")
	}
	return errs
}

// validateAnimateSettings checks the resolved animate settings against what
// Wan accepts: sides that are positive multiples of 16 and a frame count of
// the form 4n+1.
func validateAnimateSettings(a Animate) []error {
	if a.IsAnimateDiff() {
		return validateAnimateDiffSettings(a)
	}
	if a.IsMiniMaxH3() {
		return validateMiniMaxSettings(a)
	}
	if a.IsLTX() {
		return validateLTXSettings(a)
	}
	var errs []error
	w, h, err := parseWxH(a.Resolution)
	if err != nil || w <= 0 || h <= 0 || w%16 != 0 || h%16 != 0 {
		errs = append(errs, fmt.Errorf("config: defaults.animate.resolution %q must be WIDTHxHEIGHT with both sides positive multiples of 16", a.Resolution))
	}
	if a.Frames < 1 || (a.Frames-1)%4 != 0 {
		errs = append(errs, fmt.Errorf("config: defaults.animate.frames must be 4n+1 (e.g. 33), got %d", a.Frames))
	}
	if a.FPS < 1 {
		errs = append(errs, fmt.Errorf("config: defaults.animate.fps must be positive, got %d", a.FPS))
	}
	if a.Steps < 1 {
		errs = append(errs, fmt.Errorf("config: defaults.animate.steps must be positive, got %d", a.Steps))
	}
	if *a.CfgScale < 0 {
		errs = append(errs, fmt.Errorf("config: defaults.animate.cfg_scale must not be negative, got %v", *a.CfgScale))
	}
	if a.TwoExpert() {
		if a.HighNoiseSteps < 1 {
			errs = append(errs, fmt.Errorf("config: defaults.animate.high_noise_steps must be positive, got %d", a.HighNoiseSteps))
		}
		if *a.HighNoiseCfgScale < 0 {
			errs = append(errs, fmt.Errorf("config: defaults.animate.high_noise_cfg_scale must not be negative, got %v", *a.HighNoiseCfgScale))
		}
	}
	return errs
}

// validLTXFrames reports whether n is on LTX-2.3's 8k+1 frame grid.
func validLTXFrames(n int) bool { return n >= 9 && (n-1)%8 == 0 }

// validateLTXSettings checks the resolved LTX-2.3 settings: sides that are positive multiples of 32 and a frame
// count on the 8k+1 grid.
func validateLTXSettings(a Animate) []error {
	var errs []error
	w, h, err := parseWxH(a.Resolution)
	if err != nil || w <= 0 || h <= 0 || w%LTXSideMultiple != 0 || h%LTXSideMultiple != 0 {
		errs = append(errs, fmt.Errorf("config: defaults.animate.resolution %q must be WIDTHxHEIGHT with both sides positive multiples of %d", a.Resolution, LTXSideMultiple))
	}
	if !validLTXFrames(a.Frames) {
		errs = append(errs, fmt.Errorf("config: defaults.animate.frames must be 8k+1 for ltx-2.3 (9, 17, ..., 49, 57, ...), got %d", a.Frames))
	}
	if a.Steps < 1 {
		errs = append(errs, fmt.Errorf("config: defaults.animate.steps must be positive, got %d", a.Steps))
	}
	if *a.CfgScale < 0 {
		errs = append(errs, fmt.Errorf("config: defaults.animate.cfg_scale must not be negative, got %v", *a.CfgScale))
	}
	return errs
}

// validMiniMaxFrames reports whether n is on MiniMax-H3's 17k+5 frame grid.
func validMiniMaxFrames(n int) bool { return n >= 5 && (n-5)%17 == 0 }

// validateMiniMaxSettings checks the resolved MiniMax-H3 settings: sides that
// are positive multiples of 32 and a frame count on the 17k+5 grid.
func validateMiniMaxSettings(a Animate) []error {
	var errs []error
	w, h, err := parseWxH(a.Resolution)
	if err != nil || w <= 0 || h <= 0 || w%MiniMaxSideMultiple != 0 || h%MiniMaxSideMultiple != 0 {
		errs = append(errs, fmt.Errorf("config: defaults.animate.resolution %q must be WIDTHxHEIGHT with both sides positive multiples of %d", a.Resolution, MiniMaxSideMultiple))
	}
	if !validMiniMaxFrames(a.Frames) {
		errs = append(errs, fmt.Errorf("config: defaults.animate.frames must be 17k+5 for minimax-h3 (22, 39, 56, ...), got %d", a.Frames))
	}
	if a.Steps < 1 {
		errs = append(errs, fmt.Errorf("config: defaults.animate.steps must be positive, got %d", a.Steps))
	}
	if *a.CfgScale < 0 {
		errs = append(errs, fmt.Errorf("config: defaults.animate.cfg_scale must not be negative, got %v", *a.CfgScale))
	}
	return errs
}

// validateAnimateDiffSettings checks the resolved AnimateDiff settings: sides
// that are positive multiples of 8 (SD 1.5's latent grid), a frame count
// inside the range the module supports (any count, not only 4n+1), and a
// strength in (0, 1].
func validateAnimateDiffSettings(a Animate) []error {
	var errs []error
	w, h, err := parseWxH(a.Resolution)
	if err != nil || w <= 0 || h <= 0 || w%8 != 0 || h%8 != 0 {
		errs = append(errs, fmt.Errorf("config: defaults.animate.resolution %q must be WIDTHxHEIGHT with both sides positive multiples of 8", a.Resolution))
	}
	if a.Frames < AnimateDiffMinFrames || a.Frames > AnimateDiffMaxFrames {
		errs = append(errs, fmt.Errorf("config: defaults.animate.frames must be between %d and %d for animatediff, got %d", AnimateDiffMinFrames, AnimateDiffMaxFrames, a.Frames))
	}
	if a.FPS < 1 {
		errs = append(errs, fmt.Errorf("config: defaults.animate.fps must be positive, got %d", a.FPS))
	}
	if a.Steps < 1 {
		errs = append(errs, fmt.Errorf("config: defaults.animate.steps must be positive, got %d", a.Steps))
	}
	if *a.CfgScale < 0 {
		errs = append(errs, fmt.Errorf("config: defaults.animate.cfg_scale must not be negative, got %v", *a.CfgScale))
	}
	if *a.Strength <= 0 || *a.Strength > 1 {
		errs = append(errs, fmt.Errorf("config: defaults.animate.strength must be greater than 0 and at most 1, got %v", *a.Strength))
	}
	return errs
}

// validateSceneQuality checks a scene's own render and animation settings
// with the rules of the project's: a positive WIDTHxHEIGHT, at least one step,
// no negative cfg; for the animation, sides that are multiples of 16 and a
// frame count of the form 4n+1. An animation block on a scene that does not
// animate is allowed (it is unused).
func validateSceneQuality(s Scene, engine string) []error {
	var errs []error
	if r := s.Render; r != nil {
		if r.Resolution != "" {
			if w, h, err := parseWxH(r.Resolution); err != nil || w <= 0 || h <= 0 {
				errs = append(errs, fmt.Errorf("config: scene %q: render.resolution %q must be WIDTHxHEIGHT with both sides positive", s.ID, r.Resolution))
			}
		}
		if r.Steps < 0 {
			errs = append(errs, fmt.Errorf("config: scene %q: render.steps must be at least 1, got %d", s.ID, r.Steps))
		}
		if r.CfgScale != nil && *r.CfgScale < 0 {
			errs = append(errs, fmt.Errorf("config: scene %q: render.cfg_scale must not be negative, got %v", s.ID, *r.CfgScale))
		}
	}
	if a := s.Animation; a != nil {
		multiple := 16
		switch engine {
		case EngineAnimateDiff:
			multiple = 8
		case EngineMiniMaxH3:
			multiple = MiniMaxSideMultiple
		case EngineLTX:
			multiple = LTXSideMultiple
		}
		if a.Resolution != "" {
			if w, h, err := parseWxH(a.Resolution); err != nil || w <= 0 || h <= 0 || w%multiple != 0 || h%multiple != 0 {
				errs = append(errs, fmt.Errorf("config: scene %q: animation.resolution %q must be WIDTHxHEIGHT with both sides positive multiples of %d", s.ID, a.Resolution, multiple))
			}
		}
		if engine == EngineAnimateDiff {
			if a.Frames != 0 && (a.Frames < AnimateDiffMinFrames || a.Frames > AnimateDiffMaxFrames) {
				errs = append(errs, fmt.Errorf("config: scene %q: animation.frames must be between %d and %d for animatediff, got %d", s.ID, AnimateDiffMinFrames, AnimateDiffMaxFrames, a.Frames))
			}
		} else if engine == EngineMiniMaxH3 {
			if a.Frames != 0 && !validMiniMaxFrames(a.Frames) {
				errs = append(errs, fmt.Errorf("config: scene %q: animation.frames must be 17k+5 for minimax-h3 (22, 39, 56, ...), got %d", s.ID, a.Frames))
			}
		} else if engine == EngineLTX {
			if a.Frames != 0 && !validLTXFrames(a.Frames) {
				errs = append(errs, fmt.Errorf("config: scene %q: animation.frames must be 8k+1 for ltx-2.3 (9, 17, ..., 49, 57, ...), got %d", s.ID, a.Frames))
			}
		} else if a.Frames != 0 && (a.Frames < 1 || (a.Frames-1)%4 != 0) {
			errs = append(errs, fmt.Errorf("config: scene %q: animation.frames must be 4n+1 (e.g. 33), got %d", s.ID, a.Frames))
		}
		if a.Steps < 0 {
			errs = append(errs, fmt.Errorf("config: scene %q: animation.steps must be at least 1, got %d", s.ID, a.Steps))
		}
	}
	return errs
}

func parseWxH(s string) (w, h int, err error) {
	_, err = fmt.Sscanf(s, "%dx%d", &w, &h)
	return w, h, err
}

// validateAnimateUse requires the animate block and its model files when at
// least one scene animates; with no animated scene the block is not checked
// beyond its settings.
func (c *Config) validateAnimateUse(statPath func(string) string) []error {
	var errs []error
	var animated []string
	for _, s := range c.Scenes {
		if s.Animate {
			animated = append(animated, s.ID)
		}
	}
	if len(animated) == 0 {
		return nil
	}
	a := c.Defaults.Animate
	if a == nil {
		return []error{fmt.Errorf("config: scene %q has animate: true but defaults.animate is not set", animated[0])}
	}
	files := []struct{ name, path string }{
		{"defaults.animate.diffusion_model", a.DiffusionModel},
		{"defaults.animate.t5xxl", a.T5XXL},
		{"defaults.animate.vae", a.VAE},
		{"defaults.animate.high_noise_diffusion_model", a.HighNoiseDiffusionModel},
	}
	if a.IsAnimateDiff() {
		// The Wan files are neither required nor used.
		files = []struct{ name, path string }{
			{"defaults.animate.checkpoint", a.Checkpoint},
			{"defaults.animate.motion_module", a.MotionModule},
		}
	}
	if a.IsMiniMaxH3() {
		files = []struct{ name, path string }{
			{"defaults.animate.diffusion_model", a.DiffusionModel},
			{"defaults.animate.vae", a.VAE},
			{"defaults.animate.audio_vae", a.AudioVAE},
			{"defaults.animate.llm", a.LLM},
			{"defaults.animate.lora", a.LoRA},
		}
	}
	if a.IsLTX() {
		files = []struct{ name, path string }{
			{"defaults.animate.diffusion_model", a.DiffusionModel},
			{"defaults.animate.vae", a.VAE},
			{"defaults.animate.audio_vae", a.AudioVAE},
			{"defaults.animate.llm", a.LLM},
			{"defaults.animate.connectors", a.Connectors},
		}
	}
	for _, f := range files {
		if f.path == "" && (f.name == "defaults.animate.high_noise_diffusion_model" || f.name == "defaults.animate.lora") {
			continue // optional: empty means the single-model mode
		}
		if f.path == "" {
			errs = append(errs, fmt.Errorf("config: scene %q has animate: true but %s is not set", animated[0], f.name))
		} else if _, err := os.Stat(statPath(f.path)); err != nil {
			errs = append(errs, fmt.Errorf("config: %s %q does not exist: %w", f.name, f.path, err))
		}
	}
	return errs
}

// validateMusic checks a music-mode project: tracks instead of scenes, the
// engine defaults, and per-track invariants. Image-only defaults are
// ignored with a warning.
func (c *Config) validateMusic(statPath func(string) string) []error {
	var errs []error
	if len(c.Scenes) > 0 {
		errs = append(errs, fmt.Errorf("config: output.mode %q uses tracks, not scenes: move the %d scene(s) to tracks or change the mode", ModeMusic, len(c.Scenes)))
	}
	if len(c.Tracks) == 0 {
		errs = append(errs, fmt.Errorf("config: output.mode %q needs at least one track", ModeMusic))
	}
	if c.Defaults.SubjectPrompt != "" {
		c.warn("defaults.subject_prompt is ignored in music mode")
	}
	if c.Defaults.NegativePrompt != "" {
		c.warn("defaults.negative_prompt is ignored in music mode")
	}
	if m := c.Defaults.Music; m != nil {
		switch m.ResolvedOutputFormat() {
		case MusicFormatMP3, MusicFormatWAV:
		default:
			errs = append(errs, fmt.Errorf("config: defaults.music.output_format %q is invalid: must be %q or %q", m.OutputFormat, MusicFormatMP3, MusicFormatWAV))
		}
		switch m.ResolvedGenerator() {
		case GeneratorACEStep, GeneratorHeartMuLa, GeneratorDiffRhythm2:
		default:
			errs = append(errs, fmt.Errorf("config: defaults.music.generator %q is invalid: must be %q, %q or %q", m.Generator, GeneratorACEStep, GeneratorHeartMuLa, GeneratorDiffRhythm2))
		}
		if m.DR2CfgStrength != nil && (*m.DR2CfgStrength < 0.5 || *m.DR2CfgStrength > 10) {
			errs = append(errs, fmt.Errorf("config: defaults.music.dr2_cfg_strength must be between 0.5 and 10, got %v", *m.DR2CfgStrength))
		}
		if m.DR2Steps != nil && (*m.DR2Steps < 4 || *m.DR2Steps > 64) {
			errs = append(errs, fmt.Errorf("config: defaults.music.dr2_steps must be between 4 and 64, got %d", *m.DR2Steps))
		}
		if (m.DR2CfgStrength != nil || m.DR2Steps != nil) && !m.UsesDiffRhythm2() {
			c.warn("defaults.music.dr2_cfg_strength and dr2_steps only apply to the %s generator", GeneratorDiffRhythm2)
		}
		switch m.ResolvedEngine() {
		case EngineCLI, EngineServer:
		default:
			errs = append(errs, fmt.Errorf("config: defaults.music.engine %q is invalid: must be %q or %q", m.Engine, EngineCLI, EngineServer))
		}
		if m.KeepLoaded != nil && m.ResolvedEngine() != EngineServer {
			errs = append(errs, fmt.Errorf("config: defaults.music.keep_loaded only applies with engine %q", EngineServer))
		}
		if m.Vocals != "" && !ValidVocals(m.Vocals) {
			errs = append(errs, fmt.Errorf("config: defaults.music.vocals %q is invalid: use one of %s", m.Vocals, strings.Join(VocalStyles(), ", ")))
		}
		if m.MP3Bitrate < 0 || m.InferenceSteps < 0 {
			errs = append(errs, fmt.Errorf("config: defaults.music mp3_bitrate and inference_steps must not be negative"))
		}
		if l := m.LoudnessLUFS; l != nil && (*l < MinLoudnessLUFS || *l > MaxLoudnessLUFS) {
			errs = append(errs, fmt.Errorf("config: defaults.music.loudness_lufs %g is out of range: must be between %g and %g", *l, MinLoudnessLUFS, MaxLoudnessLUFS))
		}
		switch m.Cover {
		case "", CoverOff:
		case CoverAuto:
			if c.Defaults.Model == "" && c.Defaults.ModelSet == nil {
				errs = append(errs, fmt.Errorf("config: defaults.music.cover %q needs an image model: set defaults.model or defaults.model_set", CoverAuto))
			}
		default:
			errs = append(errs, fmt.Errorf("config: defaults.music.cover %q is invalid: must be %q or %q", m.Cover, CoverOff, CoverAuto))
		}
		if res := c.Defaults.Resolution; res != "" && m.Cover == CoverAuto {
			if _, _, err := parseWxH(res); err != nil {
				errs = append(errs, fmt.Errorf("config: defaults.resolution %q must be WIDTHxHEIGHT (it is the size of the covers)", res))
			}
		}
	}

	errs = append(errs, c.validateMusicModels(statPath)...)
	errs = append(errs, c.validateAdapters(statPath)...)

	seen := make(map[string]int, len(c.Tracks))
	for i, t := range c.Tracks {
		if t.ID == "" || !trackIDPattern.MatchString(t.ID) {
			errs = append(errs, fmt.Errorf("config: track at index %d has an invalid id %q: use letters, digits, '.', '_' or '-'", i, t.ID))
		}
		if prev, dup := seen[t.ID]; dup {
			errs = append(errs, fmt.Errorf("config: duplicate track id %q (first at index %d, again at index %d)", t.ID, prev, i))
		} else {
			seen[t.ID] = i
		}
		if strings.TrimSpace(t.Prompt) == "" {
			errs = append(errs, fmt.Errorf("config: track %q has an empty prompt", t.ID))
		}
		if t.Variants != nil && *t.Variants < 1 {
			errs = append(errs, fmt.Errorf("config: track %q: variants must be a positive integer >= 1, got %d", t.ID, *t.Variants))
		}
		if t.DurationSeconds != nil && *t.DurationSeconds <= 0 {
			errs = append(errs, fmt.Errorf("config: track %q: duration_seconds must be > 0, got %v", t.ID, *t.DurationSeconds))
		}
		if t.BPM != nil && *t.BPM <= 0 {
			errs = append(errs, fmt.Errorf("config: track %q: bpm must be > 0, got %d", t.ID, *t.BPM))
		}
		if t.Vocals != "" && !ValidVocals(t.Vocals) {
			errs = append(errs, fmt.Errorf("config: track %q: vocals %q is invalid: use one of %s", t.ID, t.Vocals, strings.Join(VocalStyles(), ", ")))
		}
		if t.Vocals != "" && (t.Instrumental || t.Task == TaskExtract) {
			c.warn("track %q: vocals is ignored because the track has no voice to ask for", t.ID)
		}
		if t.Instrumental && strings.TrimSpace(t.Lyrics) != "" {
			c.warn("track %q: lyrics are ignored because the track is instrumental", t.ID)
		}
		if len(t.Title) > 200 {
			errs = append(errs, fmt.Errorf("config: track %q: title is too long (200 characters at most)", t.ID))
		}
		if t.Cover != "" && t.Cover != CoverOff {
			if !imageExts[strings.ToLower(filepath.Ext(t.Cover))] || strings.ContainsAny(t.Cover, `/\`) {
				errs = append(errs, fmt.Errorf("config: track %q: cover %q must be %q or a picture (.png, .jpg, .jpeg, .webp) in sources/", t.ID, t.Cover, CoverOff))
			} else if _, err := os.Stat(statPath(filepath.Join(SourcesSubdir, t.Cover))); err != nil {
				errs = append(errs, fmt.Errorf("config: track %q: cover %q not found in sources: %w", t.ID, t.Cover, err))
			}
		}
		errs = append(errs, c.validateTask(t, statPath)...)
		errs = append(errs, c.validateHeartMuLaTrack(t)...)
		errs = append(errs, c.validateDiffRhythm2Track(t)...)
	}

	// Resolved export paths must not collide (track "a_v1" with one variant
	// vs track "a" with two).
	ext := c.Defaults.Music.ResolvedOutputFormat()
	paths := make(map[string]string)
	for _, t := range c.Tracks {
		n := t.EffectiveVariants()
		if n < 1 {
			continue // already reported above
		}
		for i := 0; i < n; i++ {
			p := filepath.Join(c.ExportDir(), VariantAudioName(t.ID, n, i, ext))
			if other, dup := paths[p]; dup && other != t.ID {
				errs = append(errs, fmt.Errorf("config: tracks %q and %q both resolve to output path %q", other, t.ID, p))
			}
			paths[p] = t.ID
		}
	}
	// Mixes: their own rules, and their files may not take a track's path.
	errs = append(errs, c.validateMixes()...)
	for _, m := range c.Mixes {
		p := c.MixFile(m.ID)
		if other, dup := paths[p]; dup && other != m.ID {
			errs = append(errs, fmt.Errorf("config: mix %q and track %q both resolve to output path %q", m.ID, other, p))
		}
		paths[p] = m.ID
	}
	return errs
}

// validateInitImage checks the starting picture of a scene (any mode that renders).
func (c *Config) validateInitImage(s Scene, statPath func(string) string) []error {
	var errs []error
	bad := func(format string, a ...any) {
		errs = append(errs, fmt.Errorf("config: scene %q: "+format, append([]any{s.ID}, a...)...))
	}
	editing := c.Defaults.ModelSet != nil && c.Defaults.ModelSet.Edit
	if s.InitImage == "" {
		if s.Strength != nil {
			bad("strength needs an init_image")
		}
		if editing && s.Image == "" && s.Clip == "" {
			c.warn("scene %q: an editing model needs the picture to edit: set init_image", s.ID)
		}
		return errs
	}
	if editing && s.Strength != nil {
		c.warn("scene %q: strength has no effect with an editing model", s.ID)
	}
	if s.Image != "" {
		bad("image and init_image cannot both be set: image uses the picture as it is, init_image starts a generation from it")
	}
	switch {
	case !ValidImageName(s.InitImage):
		bad("init_image %q must be the name of a .png, .jpg, .jpeg or .webp file, not a path", s.InitImage)
	default:
		if _, err := os.Stat(statPath(filepath.Join(SourcesSubdir, s.InitImage))); err != nil {
			bad("init_image %q not found in %s", s.InitImage, SourcesSubdir)
		}
	}
	if s.Strength != nil && (*s.Strength < 0 || *s.Strength > 1) {
		bad("strength must be between 0 and 1, got %v", *s.Strength)
	}
	return errs
}

// validateSceneImage checks the picture of a video scene, and warns about the
// fields a picture makes pointless.
func (c *Config) validateSceneImage(s Scene, statPath func(string) string) []error {
	if s.Image == "" {
		return nil
	}
	var errs []error
	switch {
	case !ValidImageName(s.Image):
		errs = append(errs, fmt.Errorf("config: scene %q: image %q must be the name of a .png, .jpg, .jpeg or .webp file, not a path", s.ID, s.Image))
	default:
		if _, err := os.Stat(statPath(filepath.Join(SourcesSubdir, s.Image))); err != nil {
			errs = append(errs, fmt.Errorf("config: scene %q: image %q not found in %s", s.ID, s.Image, SourcesSubdir))
		}
	}
	for name, set := range map[string]bool{"negative_prompt": s.NegativePrompt != "", "lora": s.Lora != "", "variants": s.Variants != nil && *s.Variants != 1} {
		if set {
			c.warn("scene %q: %s is ignored because the scene uses its own image", s.ID, name)
		}
	}
	return errs
}

// ResolvedClipStart is where the part of the clip begins, in seconds (0 when unset).
func (s Scene) ResolvedClipStart() float64 {
	if s.ClipStart == nil {
		return 0
	}
	return *s.ClipStart
}

// MaxTransitionSeconds is the longest crossfade between two scenes.
const MaxTransitionSeconds = 2.0

// Transitions are the kinds of transition between scenes: names of ffmpeg's xfade filter.
var Transitions = []string{
	"fade", "fadeblack", "fadewhite", "dissolve",
	"wipeleft", "wiperight", "wipeup", "wipedown",
	"slideleft", "slideright", "slideup", "slidedown",
	"circleopen", "circleclose", "radial", "pixelize",
}

// ShaderTransitions are the shader transitions of HyperFrames, usable as `shader:<name>` (internal/video/shadertrans draws them; its test keeps
// this list equal to the shaders it carries).
var ShaderTransitions = []string{
	"domain-warp", "ridged-burn", "whip-pan", "sdf-iris", "ripple-waves", "gravitational-lens", "cinematic-zoom",
	"chromatic-split", "glitch", "swirl-vortex", "thermal-distortion", "flash-through-white", "cross-warp-morph", "light-leak",
}

// IsTransition reports whether name is a known kind ("" is not: it means the default): an xfade name or `shader:<name>`.
func IsTransition(name string) bool {
	if n, ok := strings.CutPrefix(name, "shader:"); ok {
		for _, s := range ShaderTransitions {
			if s == n {
				return true
			}
		}
		return false
	}
	for _, t := range Transitions {
		if t == name {
			return true
		}
	}
	return false
}

var reAccent = regexp.MustCompile(`^#([0-9a-fA-F]{3}|[0-9a-fA-F]{6})$`)

func validAccent(s string) bool { return reAccent.MatchString(s) }

// ResolvedTransitionAccent is the glow colour of the shader transition into the scene: its own or the project's, "" for the default of the shader.
func (s Scene) ResolvedTransitionAccent(d Defaults) string {
	if s.TransitionAccent != "" {
		return s.TransitionAccent
	}
	return d.TransitionAccent
}

// ResolvedTransitionKind is the kind of the transition into the scene: its own or the project's, "fade" by default.
func (s Scene) ResolvedTransitionKind(d Defaults) string {
	switch {
	case s.Transition != "":
		return s.Transition
	case d.Transition != "":
		return d.Transition
	}
	return "fade"
}

// ResolvedTransition is the crossfade into the scene, in seconds: its own value or the project's.
func (s Scene) ResolvedTransition(d Defaults) float64 {
	if s.TransitionSeconds != nil {
		return *s.TransitionSeconds
	}
	return d.TransitionSeconds
}

// Allowed range of clip_speed.
const (
	MinClipSpeed = 0.25
	MaxClipSpeed = 4.0
)

// ClipTrimmed reports whether the scene uses only part of its clip.
func (s Scene) ClipTrimmed() bool { return s.ClipStart != nil || s.ClipEnd != nil }

// validateSceneClip checks the own clip of a video scene, and warns about the
// fields a clip makes pointless.
func (c *Config) validateSceneClip(s Scene, statPath func(string) string) []error {
	var errs []error
	if s.ClipSpeed != nil {
		switch {
		case s.Clip == "" && !s.Animate:
			errs = append(errs, fmt.Errorf("config: scene %q: clip_speed only works with clip or animate: true", s.ID))
		case *s.ClipSpeed < MinClipSpeed || *s.ClipSpeed > MaxClipSpeed:
			errs = append(errs, fmt.Errorf("config: scene %q: clip_speed must be between %v and %v, got %v", s.ID, MinClipSpeed, MaxClipSpeed, *s.ClipSpeed))
		}
	}
	if s.ClipTrimmed() {
		if s.Clip == "" && !s.Animate {
			errs = append(errs, fmt.Errorf("config: scene %q: clip_start and clip_end only work with clip or animate: true", s.ID))
		}
		if s.ClipStart != nil && *s.ClipStart < 0 {
			errs = append(errs, fmt.Errorf("config: scene %q: clip_start must be >= 0, got %v", s.ID, *s.ClipStart))
		}
		if s.ClipEnd != nil && *s.ClipEnd <= s.ResolvedClipStart() {
			errs = append(errs, fmt.Errorf("config: scene %q: clip_end must be greater than clip_start (%v), got %v", s.ID, s.ResolvedClipStart(), *s.ClipEnd))
		}
	}
	if s.Clip == "" {
		return errs
	}
	if !ValidClipName(s.Clip) {
		errs = append(errs, fmt.Errorf("config: scene %q: clip %q must be the name of a .mp4, .mov, .webm or .mkv file, not a path", s.ID, s.Clip))
	} else if _, err := os.Stat(statPath(filepath.Join(SourcesSubdir, s.Clip))); err != nil {
		errs = append(errs, fmt.Errorf("config: scene %q: clip %q not found in %s", s.ID, s.Clip, SourcesSubdir))
	}
	for name, set := range map[string]bool{
		"image": s.Image != "", "init_image": s.InitImage != "", "animate": s.Animate,
		"motion_prompt": s.MotionPrompt != "", "animation": s.Animation != nil, "render": s.Render != nil,
	} {
		if set {
			errs = append(errs, fmt.Errorf("config: scene %q: clip cannot be combined with %s: the scene uses a video that already exists", s.ID, name))
		}
	}
	for name, set := range map[string]bool{"prompt": s.Prompt != "", "negative_prompt": s.NegativePrompt != "", "lora": s.Lora != "", "variants": s.Variants != nil && *s.Variants != 1} {
		if set {
			c.warn("scene %q: %s is ignored because the scene uses its own clip", s.ID, name)
		}
	}
	return errs
}

// Checks is what validation asks of the machine the project will run on. A field of a Config so a test or a
// caller answers for its own situation without touching shared state; a nil function asks the real thing.
type Checks struct {
	// Browser says whether a Chrome or Chromium can render motion graphics (nil error: it can). The default looks
	// for a browser the way the renderer does.
	Browser func() error
	// Blender says whether Blender can render (nil error: it can). The default asks the installed Blender, so it
	// needs no more than finding it and its version.
	Blender func() error
}

func (k Checks) browser() error {
	if k.Browser != nil {
		return k.Browser()
	}
	_, err := motion.Find(settings.Browser())
	return err
}

// validateMotionGraphics checks a scene's motion graphic and overlay.
func (c *Config) validateMotionGraphics(s Scene) []error {
	var errs []error
	bad := func(format string, a ...any) {
		errs = append(errs, fmt.Errorf("config: scene %q: "+format, append([]any{s.ID}, a...)...))
	}
	if s.MotionPart == "" {
		for name, set := range map[string]bool{"motion_params": len(s.MotionParams) > 0, "motion_text": s.MotionText != "", "motion_bpm": s.MotionBPM != nil} {
			if set {
				bad("%s needs motion_part: name the part of the catalog the scene is made from", name)
			}
		}
	} else {
		if s.MotionPart == "auto" {
			if strings.TrimSpace(s.MotionGraphic) == "" {
				bad("motion_part: auto needs motion_graphic: describe what the graphic shows, so the model can choose a part")
			}
		} else if c.Parts != nil {
			if err := c.Parts.Check(s.MotionPart, s.MotionParams); err != nil {
				bad("motion_part %q: %v", s.MotionPart, err)
			}
		}
		if s.MotionPart == "auto" && len(s.MotionParams) > 0 {
			bad("motion_params cannot be given with motion_part: auto: the model fills them in")
		}
		if n := len([]rune(s.MotionText)); n > MaxMotionText {
			bad("motion_text has %d characters, the most is %d", n, MaxMotionText)
		}
		if s.MotionBPM != nil && (*s.MotionBPM < MinMotionBPM || *s.MotionBPM > MaxMotionBPM) {
			bad("motion_bpm must be between %v and %v, got %v", MinMotionBPM, MaxMotionBPM, *s.MotionBPM)
		}
	}
	if s.IsMotionGraphic() {
		if s.MotionGraphic != "" && strings.TrimSpace(s.MotionGraphic) == "" {
			bad("motion_graphic is empty: describe what the graphic shows")
		}
		what := "motion_graphic"
		if s.MotionPart != "" {
			what = "motion_part"
		}
		for name, set := range map[string]bool{
			"image": s.Image != "", "init_image": s.InitImage != "", "clip": s.Clip != "", "animate": s.Animate,
			"motion_prompt": s.MotionPrompt != "", "animation": s.Animation != nil, "render": s.Render != nil,
		} {
			if set {
				bad("%s cannot be combined with %s: the scene is made from an animated page", what, name)
			}
		}
		for name, set := range map[string]bool{"prompt": s.Prompt != "", "negative_prompt": s.NegativePrompt != "", "lora": s.Lora != "", "variants": s.Variants != nil && *s.Variants != 1} {
			if set {
				c.warn("scene %q: %s is ignored because the scene is a motion graphic", s.ID, name)
			}
		}
	}
	o := s.Overlay
	if o == nil {
		return errs
	}
	if strings.TrimSpace(o.Text) == "" {
		bad("overlay.text is empty: write the text to show or describe it")
	}
	switch o.Position {
	case "", OverlayTop, OverlayCenter, OverlayBottom:
	default:
		bad("overlay.position %q is invalid: must be %q, %q or %q", o.Position, OverlayTop, OverlayCenter, OverlayBottom)
	}
	if o.Start != nil && *o.Start < 0 {
		bad("overlay.start must be >= 0, got %v", *o.Start)
	}
	if o.End != nil && *o.End <= o.ResolvedStart() {
		bad("overlay.end must be greater than overlay.start (%v), got %v", o.ResolvedStart(), *o.End)
	}
	if s.IsSilent() { // the scene's length is only known beforehand when it is silent
		if o.ResolvedStart() >= s.SilenceDuration() {
			bad("overlay.start (%v) is beyond the end of the scene, which lasts %v s", o.ResolvedStart(), s.SilenceDuration())
		}
		if o.End != nil && *o.End > s.SilenceDuration() {
			bad("overlay.end (%v) is beyond the end of the scene, which lasts %v s", *o.End, s.SilenceDuration())
		}
	}
	return errs
}

// validateMotionDefaults checks defaults.motion: the tempo and the design.
func (c *Config) validateMotionDefaults(statPath func(string) string) []error {
	m := c.Defaults.Motion
	if m == nil {
		return nil
	}
	var errs []error
	if m.Song != "" {
		switch {
		case !ValidSourceName(m.Song):
			errs = append(errs, fmt.Errorf("config: defaults.motion.song %q must be the name of an .mp3 or .wav file in %s, not a path", m.Song, SourcesSubdir))
		default:
			if _, err := os.Stat(statPath(filepath.Join(SourcesSubdir, m.Song))); err != nil {
				errs = append(errs, fmt.Errorf("config: defaults.motion.song: file %q not found in %s", m.Song, SourcesSubdir))
			}
		}
		for _, s := range c.Scenes {
			if s.MotionBPM != nil {
				c.warn("scene %q: motion_bpm is ignored: the beats come from the song (defaults.motion.song)", s.ID)
			}
			if s.MotionPart != "" && !s.IsSilent() {
				c.warn("scene %q: it is narrated, so its place in the song (defaults.motion.song) is estimated from the words of its narration; a silent scene (silence_seconds) follows the song exactly", s.ID)
			}
		}
	}
	if m.BPM != nil && (*m.BPM < MinMotionBPM || *m.BPM > MaxMotionBPM) {
		errs = append(errs, fmt.Errorf("config: defaults.motion.bpm must be between %v and %v, got %v", MinMotionBPM, MaxMotionBPM, *m.BPM))
	}
	if m.Design != nil && c.Parts != nil {
		if err := c.Parts.CheckDesign(*m.Design); err != nil {
			errs = append(errs, fmt.Errorf("config: defaults.motion.%w", err))
		}
	}
	return errs
}

// validateBrowserForMotion fails a project whose scenes need a browser when there is none.
func (c *Config) validateBrowserForMotion() []error {
	var ids []string
	for _, s := range c.Scenes {
		if s.IsMotionGraphic() || s.Overlay != nil {
			ids = append(ids, fmt.Sprintf("%q", s.ID))
		}
	}
	if len(ids) == 0 || c.SkipBrowserCheck {
		return nil
	}
	if err := c.Checks.browser(); err != nil {
		return []error{fmt.Errorf("config: scenes %s need Chrome or Chromium to draw their motion graphics: %w", strings.Join(ids, ", "), err)}
	}
	return nil
}

// validateBGM checks the soundtrack block of a video project.
func (c *Config) validateBGM(statPath func(string) string) []error {
	b := c.BGM
	var errs []error
	bad := func(format string, a ...any) { errs = append(errs, fmt.Errorf("config: bgm: "+format, a...)) }
	if c.Output.Mode != ModeVideo {
		bad("background music only works in video mode (mode is %q)", c.Output.Mode)
		return errs
	}
	switch {
	case b.Prompt != "" && b.File != "":
		bad("set either prompt (generate) or file, not both")
	case strings.TrimSpace(b.Prompt) == "" && b.File == "":
		bad("set prompt (to generate the music) or file (a name in %s)", SourcesSubdir)
	case b.File != "":
		if !ValidSourceName(b.File) {
			bad("file %q must be the name of an .mp3 or .wav file, not a path", b.File)
		} else if _, err := os.Stat(statPath(filepath.Join(SourcesSubdir, b.File))); err != nil {
			bad("file %q not found in %s", b.File, SourcesSubdir)
		}
		if b.Seed != nil {
			bad("seed only applies to a generated soundtrack (prompt)")
		}
	default:
		errs = append(errs, c.checkMusicModels(statPath, false)...)
	}
	if b.Volume != nil && (*b.Volume < 0 || *b.Volume > 1) {
		bad("volume must be between 0 and 1, got %v", *b.Volume)
	}
	if b.FadeSeconds != nil && *b.FadeSeconds < 0 {
		bad("fade_seconds must not be negative, got %v", *b.FadeSeconds)
	}
	return errs
}

// validateTask checks the task fields of one track: a task needs an existing
// source and only its own fields, and a track without task has none of them.
func (c *Config) validateTask(t Track, statPath func(string) string) []error {
	var errs []error
	bad := func(format string, a ...any) {
		errs = append(errs, fmt.Errorf("config: track %q: "+format, append([]any{t.ID}, a...)...))
	}
	if t.Task == "" {
		if t.Reference != "" {
			switch {
			case !ValidSourceName(t.Reference):
				bad("reference %q must be the name of an .mp3 or .wav file, not a path", t.Reference)
			default:
				if _, err := os.Stat(statPath(filepath.Join(SourcesSubdir, t.Reference))); err != nil {
					bad("reference %q not found in %s", t.Reference, SourcesSubdir)
				}
			}
		}
		for name, set := range map[string]bool{"source": t.Source != "", "cover_strength": t.CoverStrength != nil, "repaint_start": t.RepaintStart != nil, "repaint_end": t.RepaintEnd != nil, "stem": t.Stem != ""} {
			if set {
				bad("%s needs a task (cover, cover-nofsq, repaint or extract)", name)
			}
		}
		return errs
	}
	if t.Reference != "" {
		bad("reference is for new songs: a track with a task uses its source")
	}
	switch t.Task {
	case TaskCover, TaskCoverNoFSQ, TaskRepaint, TaskExtract:
	case "lego", "complete":
		bad("task %q is not supported: it needs the base model", t.Task)
		return errs
	default:
		bad("task %q is invalid: must be %q, %q, %q or %q", t.Task, TaskCover, TaskCoverNoFSQ, TaskRepaint, TaskExtract)
		return errs
	}
	switch {
	case t.Source == "":
		bad("task %q needs a source: a file in the project's %s folder", t.Task, SourcesSubdir)
	case !ValidSourceName(t.Source):
		bad("source %q must be the name of an .mp3 or .wav file, not a path", t.Source)
	default:
		if _, err := os.Stat(statPath(filepath.Join(SourcesSubdir, t.Source))); err != nil {
			bad("source %q not found in %s", t.Source, SourcesSubdir)
		}
	}
	if t.DurationSeconds != nil {
		bad("duration_seconds is not allowed with a task: the source decides the length")
	}
	if t.UseLM != nil {
		c.warn("track %q: use_lm is ignored because the track has a task", t.ID)
	}
	if t.CoverStrength != nil {
		if !t.IsCover() {
			bad("cover_strength belongs to cover and cover-nofsq, not %q", t.Task)
		} else if *t.CoverStrength < 0 || *t.CoverStrength > 1 {
			bad("cover_strength must be between 0 and 1, got %v", *t.CoverStrength)
		}
	}
	if t.RepaintStart != nil || t.RepaintEnd != nil {
		if t.Task != TaskRepaint {
			bad("repaint_start and repaint_end belong to repaint, not %q", t.Task)
		} else if t.RepaintStart != nil && t.RepaintEnd != nil && *t.RepaintEnd <= *t.RepaintStart {
			bad("repaint_end (%v) must be greater than repaint_start (%v)", *t.RepaintEnd, *t.RepaintStart)
		}
	}
	switch {
	case t.Task == TaskExtract && t.Stem == "":
		bad("extract needs a stem: one of %s", strings.Join(MusicStems, ", "))
	case t.Task != TaskExtract && t.Stem != "":
		bad("stem belongs to extract, not %q", t.Task)
	case t.Task == TaskExtract && !slices.Contains(MusicStems, t.Stem):
		bad("stem %q is invalid: use one of %s", t.Stem, strings.Join(MusicStems, ", "))
	}
	if t.Task == TaskExtract && strings.Contains(strings.ToLower(c.Defaults.Music.ResolvedSynth()), "turbo") {
		bad("extract does not work with the turbo model %q: install the SFT model (Models screen, Music tab) and set defaults.music.synth_model", c.Defaults.Music.ResolvedSynth())
	}
	return errs
}

// validateMusicModels checks that the model files the run needs exist in the
// models directory: the synthesis model, the language model when any track
// uses it, and the text encoder and VAE the engine finds by name.
func (c *Config) validateMusicModels(statPath func(string) string) []error {
	if len(c.Tracks) == 0 || c.Defaults.Music.UsesService() {
		return nil // these generators have their own weights, found by their service (see internal/platform/musicsvc)
	}
	return c.checkMusicModels(statPath, c.UsesLM())
}

// checkMusicModels checks the model files exist; needLM adds the language model.
func (c *Config) checkMusicModels(statPath func(string) string, needLM bool) []error {
	m := c.Defaults.Music
	rel := m.ResolvedModelsDir()
	dir := statPath(rel)
	hint := fmt.Sprintf("install it from the Models screen (Music tab) or the catalog into %s", rel)
	var errs []error
	need := func(label, name string) {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			errs = append(errs, fmt.Errorf("config: music %s %q not found in %s: %s", label, name, rel, hint))
		}
	}
	need("synthesis model", m.ResolvedSynth())
	if needLM {
		need("language model", m.ResolvedLM())
	}
	for _, g := range []struct{ label, glob string }{
		{"text encoder", MusicTextEncoderGlob},
		{"VAE", MusicVAEGlob},
	} {
		if matches, _ := filepath.Glob(filepath.Join(dir, g.glob)); len(matches) == 0 {
			errs = append(errs, fmt.Errorf("config: music %s (%s) not found in %s: %s", g.label, g.glob, rel, hint))
		}
	}
	return errs
}

// validateAdapters checks the adapters tracks resolve to: they must be
// installed and their strength must not be negative.
func (c *Config) validateAdapters(statPath func(string) string) []error {
	var errs []error
	if m := c.Defaults.Music; m != nil && m.AdapterScale != nil && *m.AdapterScale < 0 {
		errs = append(errs, fmt.Errorf("config: defaults.music.adapter_scale must not be negative, got %v", *m.AdapterScale))
	}
	dir := statPath(c.Defaults.Music.AdaptersDir())
	reported := map[string]bool{}
	for _, t := range c.Tracks {
		if t.AdapterScale != nil && *t.AdapterScale < 0 {
			errs = append(errs, fmt.Errorf("config: track %q: adapter_scale must not be negative, got %v", t.ID, *t.AdapterScale))
		}
		name, _ := c.ResolveAdapter(t)
		if name == "" || reported[name] {
			continue
		}
		if strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
			errs = append(errs, fmt.Errorf("config: track %q: adapter %q must be a file or folder name, not a path", t.ID, name))
			reported[name] = true
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			errs = append(errs, fmt.Errorf("config: music adapter %q not found in %s: install it from the Models screen (Music tab)", name, c.Defaults.Music.AdaptersDir()))
			reported[name] = true
		}
	}
	return errs
}

// VoiceFor is the voice that narrates a scene: its own, else defaults.voice.
func (c *Config) VoiceFor(s Scene) string {
	if s.Voice != "" {
		return s.Voice
	}
	return c.Defaults.Voice
}

// SpeakerFor is the speaker of a multi-speaker voice that narrates a scene (0 for the usual voice).
func (c *Config) SpeakerFor(s Scene) int {
	if s.Speaker != nil {
		return *s.Speaker
	}
	if s.Voice != "" {
		return 0
	}
	return c.Defaults.VoiceSpeaker
}

// checkSpeaker says what is wrong with a speaker id for a voice: negative, or past the speakers the
// model holds (when its config can be read).
func checkSpeaker(what, voice string, speaker int, statPath func(string) string) []error {
	if speaker == 0 {
		return nil
	}
	if speaker < 0 {
		return []error{fmt.Errorf("config: %s must be >= 0, got %d", what, speaker)}
	}
	if n := tts.SpeakerCount(statPath(voice)); speaker >= n {
		return []error{fmt.Errorf("config: %s %d is out of range: voice %q has %d speaker(s) (0 to %d)", what, speaker, voice, n, n-1)}
	}
	return nil
}

// checkVoice says what is wrong with a voice file: missing, or (for a Piper
// .onnx) missing its companion config, which makes Piper abort with an opaque
// core dump. what names the field in the message.
func checkVoice(what, voice string, statPath func(string) string) []error {
	if _, err := os.Stat(statPath(voice)); err != nil {
		return []error{fmt.Errorf("config: %s %q does not exist: %w", what, voice, err)}
	}
	if strings.HasSuffix(voice, ".onnx") {
		companion := voice + ".json"
		if _, err := os.Stat(statPath(companion)); err != nil {
			return []error{fmt.Errorf("config: %s %q needs its companion config %q, which does not exist: %w", what, voice, companion, err)}
		}
	}
	return nil
}

// validateHeartMuLaTrack reports what the HeartMuLa generator cannot do with a track: tasks, a reference
// and a track without lyrics are errors; the fields that only ACE-Step understands are ignored with a warning.
func (c *Config) validateHeartMuLaTrack(t Track) []error {
	if !c.Defaults.Music.UsesHeartMuLa() {
		return nil
	}
	var errs []error
	if t.Task != "" {
		errs = append(errs, fmt.Errorf("config: track %q: tasks (%s) are not supported by the heartmula generator", t.ID, t.Task))
	}
	if t.Reference != "" {
		errs = append(errs, fmt.Errorf("config: track %q: a reference audio is not supported by the heartmula generator", t.ID))
	}
	if t.Task == "" && (t.Instrumental || strings.TrimSpace(t.Lyrics) == "") {
		errs = append(errs, fmt.Errorf("config: track %q: the heartmula generator needs lyrics (instrumental tracks are not supported)", t.ID))
	}
	var ignored []string
	if t.BPM != nil {
		ignored = append(ignored, "bpm")
	}
	if t.KeyScale != "" {
		ignored = append(ignored, "keyscale")
	}
	if t.TimeSignature != "" {
		ignored = append(ignored, "timesignature")
	}
	if len(ignored) > 0 {
		c.warn("track %q: %s %s ignored by the heartmula generator", t.ID, strings.Join(ignored, ", "), map[bool]string{true: "is", false: "are"}[len(ignored) == 1])
	}
	return errs
}

// validateDiffRhythm2Track reports what the DiffRhythm2 generator cannot do with a track: a language other than
// English (it reads English and Chinese phonemes and the program offers only English and Spanish), tasks, a
// reference and a track without lyrics are errors; the fields that only ACE-Step understands are ignored with a warning.
func (c *Config) validateDiffRhythm2Track(t Track) []error {
	if !c.Defaults.Music.UsesDiffRhythm2() {
		return nil
	}
	var errs []error
	if t.Task != "" {
		errs = append(errs, fmt.Errorf("config: track %q: tasks (%s) are not supported by the diffrhythm2 generator", t.ID, t.Task))
	}
	if t.Reference != "" {
		errs = append(errs, fmt.Errorf("config: track %q: a reference audio is not supported by the diffrhythm2 generator", t.ID))
	}
	if t.Task == "" && (t.Instrumental || strings.TrimSpace(t.Lyrics) == "") {
		errs = append(errs, fmt.Errorf("config: track %q: the diffrhythm2 generator needs lyrics (it cannot make instrumentals)", t.ID))
	}
	lang := t.VocalLanguage
	if lang == "" {
		lang = c.Defaults.Music.VocalLanguage
	}
	if lang != "" && lang != "en" {
		errs = append(errs, fmt.Errorf("config: track %q: the diffrhythm2 generator reads English lyrics only, not vocal_language %q", t.ID, lang))
	}
	var ignored []string
	if t.BPM != nil {
		ignored = append(ignored, "bpm")
	}
	if t.KeyScale != "" {
		ignored = append(ignored, "keyscale")
	}
	if t.TimeSignature != "" {
		ignored = append(ignored, "timesignature")
	}
	if t.Vocals != "" {
		ignored = append(ignored, "vocals")
	}
	if len(ignored) > 0 {
		c.warn("track %q: %s %s ignored by the diffrhythm2 generator", t.ID, strings.Join(ignored, ", "), map[bool]string{true: "is", false: "are"}[len(ignored) == 1])
	}
	return errs
}
