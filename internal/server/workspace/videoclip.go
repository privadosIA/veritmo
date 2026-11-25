package workspace

// videoclip.go turns a song of a music project into a video project: a plan (shots with their lyrics, camera
// movements and, when the assistant is available, the pictures to make) and the creation of the project that
// carries it, with the song as its soundtrack (see specs/music-studio).

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"veritmo/internal/assistant/assist"
	"veritmo/internal/assistant/llm"
	"veritmo/internal/music/videoclip"
	"veritmo/internal/platform/settings"
	"veritmo/internal/project/config"
	"veritmo/internal/project/export"
	"veritmo/internal/server/api"
)

// PeaksRate is the rate (Hz, mono) a song is decoded to for the drawing of its waveform and PeaksMaxBytes bounds the
// decoded audio held in memory (about 33 minutes). The music family draws the same waveform.
const (
	PeaksRate     = 8000
	PeaksMaxBytes = 64 << 20
)

// defaultTransition is the crossfade between the scenes of a videoclip.
const defaultTransition = 0.8

type videoclipPlanRequest struct {
	TrackID           string  `json:"track_id"`
	Variant           int     `json:"variant"`
	ShotSeconds       float64 `json:"shot_seconds"`
	TransitionSeconds float64 `json:"transition_seconds"`
	// AI asks the assistant (Ollama) for the pictures; without it, or when it cannot answer, each shot's prompt
	// is built from the style of the song and the lines it covers.
	AI    bool   `json:"ai"`
	Model string `json:"model"`
	// Provider replaces the routing of the assistant for this call only.
	Provider string `json:"provider"`
}

type videoclipShot struct {
	ID        string   `json:"id"`
	Section   string   `json:"section"`
	Start     float64  `json:"start"`
	Seconds   float64  `json:"seconds"`
	Duration  float64  `json:"duration"`
	Lines     []string `json:"lines,omitempty"`
	Motion    string   `json:"motion"`
	Intensity float64  `json:"intensity"`
	Prompt    string   `json:"prompt"`
}

type videoclipPlan struct {
	Title             string   `json:"title"`
	Caption           string   `json:"caption"`
	AudioFile         string   `json:"audio_file"`
	AudioSeconds      float64  `json:"audio_seconds"`
	TransitionSeconds float64  `json:"transition_seconds"`
	SubjectPrompt     string   `json:"subject_prompt"`
	UsedAI            bool     `json:"used_ai"`
	Warnings          []string `json:"warnings,omitempty"`
	// AlignedCuts is how many of the cuts between the Sections sections were moved to a change of the song.
	AlignedCuts int             `json:"aligned_cuts,omitempty"`
	Sections    int             `json:"sections,omitempty"`
	Shots       []videoclipShot `json:"shots"`
}

// SongOf finds the exported song of a track in the project's manifest.
func (h *Handler) SongOf(projectID, trackID string, variant int) (export.MusicEntry, *config.Config, error) {
	cfg, err := h.ProjectConfig(projectID)
	if err != nil {
		return export.MusicEntry{}, nil, err
	}
	if cfg.Output.Mode != config.ModeMusic {
		return export.MusicEntry{}, nil, fmt.Errorf("project %q is not a music project", projectID)
	}
	raw, err := os.ReadFile(filepath.Join(h.ProjectDir(projectID), cfg.ExportDir(), "manifest.json"))
	if err != nil {
		return export.MusicEntry{}, nil, fmt.Errorf("the project has no songs yet: create them first")
	}
	var m export.MusicManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return export.MusicEntry{}, nil, fmt.Errorf("the manifest of the songs cannot be read: %w", err)
	}
	for _, e := range m.Tracks {
		if e.TrackID == trackID && e.Variant == variant {
			if _, err := os.Stat(filepath.Join(h.ProjectDir(projectID), e.File)); err != nil {
				return export.MusicEntry{}, nil, fmt.Errorf("the song %s is no longer in the project", e.File)
			}
			return e, cfg, nil
		}
	}
	return export.MusicEntry{}, nil, fmt.Errorf("track %q has no version %d among the songs of the project", trackID, variant+1)
}

// POST /api/projects/{id}/videoclip/plan
func (h *Handler) handleVideoclipPlan(w http.ResponseWriter, r *http.Request) {
	id, ok := h.RequireProject(w, r)
	if !ok {
		return
	}
	var req videoclipPlanRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
		return
	}
	song, cfg, err := h.SongOf(id, req.TrackID, req.Variant)
	if err != nil {
		api.WriteError(w, http.StatusUnprocessableEntity, api.CodeValidation, err.Error())
		return
	}
	lyrics := ""
	if !song.Instrumental {
		lyrics = song.LyricsSung
		if strings.TrimSpace(lyrics) == "" {
			lyrics = song.LyricsRequested
		}
	}
	trans := req.TransitionSeconds
	if trans == 0 {
		trans = defaultTransition
	}
	env := h.songEnvelope(r.Context(), id, song.File)
	shots, aligned, err := videoclip.BuildAligned(videoclip.Options{Lyrics: lyrics, AudioSeconds: song.AudioSeconds, ShotSeconds: req.ShotSeconds, TransitionSeconds: trans, Envelope: env})
	if err != nil {
		api.WriteError(w, http.StatusUnprocessableEntity, api.CodeValidation, err.Error())
		return
	}
	title := song.Title
	if title == "" {
		for _, t := range cfg.Tracks {
			if t.ID == song.TrackID {
				title = t.DisplayTitle()
			}
		}
	}
	sections := len(videoclip.ParseLyrics(lyrics))
	plan := videoclipPlan{AlignedCuts: aligned, Sections: sections, Title: title, Caption: song.Caption, AudioFile: song.File, AudioSeconds: song.AudioSeconds, TransitionSeconds: trans}
	prompts := make([]string, len(shots))
	if req.AI {
		// No consent is an answer of its own, not a plan that quietly goes without the assistant.
		if err := llm.Check(settings.TaskAssistant, req.Provider); err != nil && api.WriteRoutingError(w, err) {
			return
		}
		sb, werr := h.storyboard(llm.WithProvider(r.Context(), req.Provider), song.Caption, title, shots, req.Model)
		if werr != nil {
			plan.Warnings = append(plan.Warnings, "the assistant could not write the pictures ("+werr.Error()+"): they come from the style and the lyrics")
		} else {
			plan.UsedAI, plan.SubjectPrompt, prompts = true, sb.Subject, sb.Prompts
		}
	}
	for i, sh := range shots {
		p := prompts[i]
		if p == "" {
			p = videoclip.FallbackPrompt(song.Caption, sh)
		}
		plan.Shots = append(plan.Shots, videoclipShot{
			ID: sh.ID, Section: sh.Section, Start: sh.Start, Seconds: sh.Seconds, Duration: sh.Duration,
			Lines: sh.Lines, Motion: sh.Motion, Intensity: sh.Intensity, Prompt: p,
		})
	}
	if plan.SubjectPrompt == "" {
		plan.SubjectPrompt = "cinematic music video, " + strings.TrimSpace(song.Caption)
	}
	api.WriteJSON(w, http.StatusOK, plan)
}

// songEnvelope reads the loudness of an exported song over time, so the cuts of the video can follow the song;
// nil when ffmpeg is missing or cannot read it (the cuts then follow the text alone).
func (h *Handler) songEnvelope(ctx context.Context, projectID, rel string) []float64 {
	if _, err := h.host.LookPath("ffmpeg"); err != nil {
		return nil
	}
	full, ok := h.ConfinedArtifactPath(projectID, filepath.ToSlash(filepath.Clean(filepath.FromSlash(rel))))
	if !ok {
		return nil
	}
	raw, err := h.host.Runner().Run(ctx, "ffmpeg", []string{"-v", "error", "-i", full, "-vn", "-ac", "1", "-ar", strconv.Itoa(PeaksRate), "-f", "f32le", "-"})
	if err != nil || len(raw) > PeaksMaxBytes {
		return nil
	}
	samples := make([]float32, len(raw)/4)
	for i := range samples {
		samples[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[i*4:]))
	}
	return videoclip.Envelope(samples, PeaksRate)
}

// storyboard asks the assistant, unless a run is in progress (it needs several GB of memory).
func (h *Handler) storyboard(ctx context.Context, style, title string, shots []videoclip.Shot, model string) (assist.Storyboard, error) {
	if id, busy := h.rm.anyBusy(); busy && api.NeedsLocalMemory(ctx, settings.TaskAssistant) {
		return assist.Storyboard{}, fmt.Errorf("run %s is in progress and the assistant waits for it", id)
	}
	briefs := make([]assist.ShotBrief, len(shots))
	for i, sh := range shots {
		briefs[i] = assist.ShotBrief{Section: sh.Section, Lines: sh.Lines}
	}
	run := h.Overrides.StoryboardRun
	if run == nil {
		run = defaultAssistStoryboard
	}
	return run(ctx, style, title, briefs, model)
}

func defaultAssistStoryboard(ctx context.Context, style, title string, shots []assist.ShotBrief, model string) (assist.Storyboard, error) {
	a, err := assist.ForContext(ctx, model)
	if err != nil {
		return assist.Storyboard{}, err
	}
	return a.Storyboard(ctx, style, title, shots)
}

type videoclipCreateRequest struct {
	TrackID string `json:"track_id"`
	Variant int    `json:"variant"`
	// ID is the new project; YAML its scenes.yaml, which names the song as its soundtrack (bgm.file).
	ID   string `json:"id"`
	YAML string `json:"yaml"`
}

// POST /api/projects/{id}/videoclip — creates the video project of a song.
func (h *Handler) handleVideoclipCreate(w http.ResponseWriter, r *http.Request) {
	id, ok := h.RequireProject(w, r)
	if !ok {
		return
	}
	var req videoclipCreateRequest
	api.RaiseBodyLimit(w, r, 4<<20)
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&req); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
		return
	}
	if !api.ProjectIDPattern.MatchString(req.ID) {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid project id %q", req.ID))
		return
	}
	if h.ProjectExists(req.ID) || api.DirExists(h.ProjectDir(req.ID)) {
		api.WriteError(w, http.StatusConflict, api.CodeConflict, fmt.Sprintf("project %q already exists", req.ID))
		return
	}
	song, _, err := h.SongOf(id, req.TrackID, req.Variant)
	if err != nil {
		api.WriteError(w, http.StatusUnprocessableEntity, api.CodeValidation, err.Error())
		return
	}
	cfg, err := config.LoadBytes([]byte(req.YAML))
	if err != nil {
		api.WriteError(w, http.StatusUnprocessableEntity, api.CodeValidation, err.Error())
		return
	}
	if cfg.Output.Mode != config.ModeVideo || cfg.BGM == nil || cfg.BGM.File == "" {
		api.WriteError(w, http.StatusUnprocessableEntity, api.CodeValidation, "the videoclip must be a video project whose soundtrack (bgm.file) is the song")
		return
	}
	if ext := strings.ToLower(filepath.Ext(cfg.BGM.File)); ext != strings.ToLower(filepath.Ext(song.File)) {
		api.WriteError(w, http.StatusUnprocessableEntity, api.CodeValidation, fmt.Sprintf("bgm.file %q must have the song's extension %s", cfg.BGM.File, filepath.Ext(song.File)))
		return
	}
	if err := h.materializeProject(req.ID, []byte(req.YAML)); err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	fail := func(status int, code, msg string) {
		_ = h.removeProjectFiles(req.ID)
		api.WriteError(w, status, code, msg)
	}
	src := filepath.Join(h.ProjectDir(id), song.File)
	dstDir := filepath.Join(h.ProjectDir(req.ID), config.SourcesSubdir)
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		fail(http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	if err := export.CopyFile(src, filepath.Join(dstDir, cfg.BGM.File)); err != nil {
		fail(http.StatusInternalServerError, api.CodeInternal, fmt.Sprintf("cannot copy the song: %v", err))
		return
	}
	if err := h.validateInDir(cfg, h.ProjectDir(req.ID)); err != nil {
		fail(http.StatusUnprocessableEntity, api.CodeValidation, err.Error())
		return
	}
	api.WriteJSON(w, http.StatusCreated, map[string]string{"id": req.ID})
}
