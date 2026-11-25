package music

// musicmotion.go is the music motion API (spec music-motion): a motion-graphics piece that follows a song. A piece is
// made from a song of a music project, with a design and a script that can be read and edited; it is rendered as a
// project of its own, hidden like the ones of the generation API (it has a reserved name and is left out of the
// project list and the statistics), on the normal queue. The engine is in internal/music/musicmotion.

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"veritmo/internal/server/generation"

	"veritmo/internal/assistant/assist"
	"veritmo/internal/music/audiomeasure"
	"veritmo/internal/music/musicmotion"
	"veritmo/internal/music/separate"
	"veritmo/internal/music/songmap"
	"veritmo/internal/music/transcribe"
	"veritmo/internal/project/config"
	"veritmo/internal/server/api"
	mmrender "veritmo/internal/video/render"
)

// pieceInfo is piece.json.
type pieceInfo struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	Project   string    `json:"project"`
	TrackID   string    `json:"track_id"`
	Variant   int       `json:"variant"`
	Title     string    `json:"title,omitempty"`
	Style     string    `json:"style"`
	Level     string    `json:"level"`
	FPS       int       `json:"fps"`
	Width     int       `json:"width"`
	Height    int       `json:"height"`
	Client    string    `json:"client"`
	// SizeBytes is what the piece keeps on disk, measured when it was last needed (nil: not measured yet): after a render finishes and after its
	// storage is cleaned, never on a read. SizeRunID is the run the size was measured after.
	SizeBytes *int64 `json:"size_bytes,omitempty"`
	SizeRunID string `json:"size_run_id,omitempty"`
	// SongFile is the song kept in the piece's folder.
	SongFile string `json:"song_file"`
	// Style of the song as written (the caption), for the assistant.
	Caption string `json:"caption,omitempty"`
	// Lyrics are the written lyrics of the song and Language its vocal language (empty: Whisper detects it); they are
	// kept so the words can be heard later.
	Lyrics   string `json:"lyrics,omitempty"`
	Language string `json:"language,omitempty"`
	// Preset is the style the piece started from (a note: later edits of the style do not change the piece).
	Preset string `json:"preset,omitempty"`
	// Prompt is the person's direction for the video: what proposals read when they send none of their own.
	Prompt string `json:"prompt,omitempty"`
	// World, Device and Hook are the storyline of the plan the piece was made from (see assist.PlanDevices): the proposals and
	// the scene writer read them so that the scenes belong to one film. The scene batch fills World when a piece has none.
	World  string `json:"world,omitempty"`
	Device string `json:"device,omitempty"`
	Hook   string `json:"hook,omitempty"`
	// Reference is the id of the reference video of the library the piece follows the rhythm of; the proposals read its sheet and
	// the scene writer its shots.
	Reference string `json:"reference,omitempty"`
	// ScriptEdited is true once the user has put a script of their own.
	ScriptEdited bool   `json:"script_edited"`
	ScriptSource string `json:"script_source,omitempty"`
	RunID        string `json:"run_id,omitempty"`
	// Renders counts the renders launched; Recorded says that the time of the first one is in the render history.
	Renders  int  `json:"renders"`
	Recorded bool `json:"recorded"`
}

func (h *Handler) pieceDir(id string) string { return h.ws.ProjectDir(id) }

// pieceSources is the sources folder of a piece's project: its song, the descriptions of its 3D pieces and the models they place.
func (h *Handler) pieceSources(id string) string {
	return filepath.Join(h.pieceDir(id), config.SourcesSubdir)
}

func (h *Handler) readPiece(id string) (*pieceInfo, bool) {
	if !strings.HasPrefix(id, piecePrefix) || !api.ProjectIDPattern.MatchString(id) {
		return nil, false
	}
	return h.pieces().get(id)
}

func (h *Handler) writePiece(info *pieceInfo) error { return h.pieces().put(info) }

// mmCatalog and mmLibrary are what the pieces are made from: the parts and characters that come with the program and
// the ones the user added in the folder of the server's settings.
func (h *Handler) mmCatalog() musicmotion.Catalog { return h.host.MotionCatalog() }

func (h *Handler) mmLibrary() musicmotion.Library {
	return musicmotion.NewLibrary(musicmotion.DBLibrary(h.ws.Store()))
}

// mmHistory keeps the times of the renders of this machine, in the database.
func (h *Handler) mmHistory() musicmotion.HistoryStore { return musicmotion.DBHistory(h.ws.Store()) }

// --- the catalog -------------------------------------------------------------------------------

// CatalogParam is a parameter of a part as the API shows it: its default is written as text (a number as `3`, a
// flag as `true`) and its type says how to read it.
type CatalogParam struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Description string   `json:"description,omitempty"`
	Default     string   `json:"default"`
	Min         *float64 `json:"min,omitempty"`
	Max         *float64 `json:"max,omitempty"`
	MaxLength   int      `json:"max_length,omitempty"`
	Options     []string `json:"options,omitempty"`
	// Animatable says a script may change the parameter over a piece (`animate`).
	Animatable bool `json:"animatable,omitempty"`
}

// CatalogPart is a part of the catalog as the piece pages list it: its name, what it does, its parameters and where it comes from.
type CatalogPart struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Levels      []string `json:"levels"`
	CostMS      int      `json:"cost_ms"`
	Manual      bool     `json:"manual,omitempty"`
	// FixedCopy says the part draws example text of its own (see musicmotion.Part.HasFixedCopy).
	FixedCopy bool   `json:"fixed_copy,omitempty"`
	Source    string `json:"source"`
	// Origin is the reference video a part of the user came from; absent for the parts that come with the program and for
	// the ones written by hand.
	Origin *musicmotion.PartOrigin `json:"origin,omitempty"`
	// Verse and Hit say who draws the lyric and the word hit when this is the main part of a piece: "page" (absent too) or
	// "own" (the part draws them from window.__scene). Scene is set on the scenes written for a piece (listed only with
	// include=scene or piece=<id>).
	Verse string                `json:"verse,omitempty"`
	Hit   string                `json:"hit,omitempty"`
	Scene *musicmotion.SceneRef `json:"scene,omitempty"`
	// Timeline says the part animates with a registered GSAP timeline (it needs the GSAP file installed on the server; a dashboard that
	// cannot draw timelines previews it from the server).
	Timeline bool `json:"timeline,omitempty"`
	// Pictures is how many pictures of an imported block the part carries inside; they keep their own colours.
	Pictures int            `json:"pictures,omitempty"`
	Params   []CatalogParam `json:"params"`
}

// CatalogCharacter is a character of the catalog with the poses it can take.
type CatalogCharacter struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Poses       []string `json:"poses"`
	Expressions []string `json:"expressions"`
	Source      string   `json:"source"`
}

// CatalogBackground is a background of the catalog and where it comes from.
type CatalogBackground struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Source      string `json:"source"`
}

// PieceCatalog is everything a piece can be made of (parts, characters, backgrounds, levels, styles, frame rates and actions): the answer of GET /api/music-motion/catalog.
type PieceCatalog struct {
	Parts       []CatalogPart       `json:"parts"`
	Characters  []CatalogCharacter  `json:"characters"`
	Backgrounds []CatalogBackground `json:"backgrounds"`
	Levels      []string            `json:"levels"`
	Styles      []string            `json:"styles"`
	FrameRates  []int               `json:"frame_rates"`
	Actions     []string            `json:"actions"`
	Fonts       []string            `json:"fonts"`
	// FontStacks is the CSS stack of each font name, so a page drawn elsewhere uses the same fonts.
	FontStacks map[string]string `json:"font_stacks"`
	// FontFiles says which names are embedded in the pages (the fonts of the pack and the old names that map to it), with the
	// family, the weights and the licence of each: a page drawn elsewhere can embed the same files.
	FontFiles map[string]musicmotion.FontFile `json:"font_files"`
	Modes     []string                        `json:"modes"`
	// Features names what this server supports beyond the base script, so a dashboard can tell before it saves.
	Features []string `json:"features"`
}

// catalogPartOf is a part as the catalog lists it.
func catalogPartOf(p musicmotion.Part) CatalogPart {
	cost := p.CostMS
	if cost == 0 {
		cost = musicmotion.DefaultCostMS
	}
	params := []CatalogParam{}
	for _, pr := range p.Params {
		params = append(params, CatalogParam{Name: pr.Name, Type: pr.Type, Description: pr.Description, Default: fmt.Sprint(pr.Default),
			Min: pr.Min, Max: pr.Max, MaxLength: pr.MaxLength, Options: pr.Options, Animatable: pr.Animatable})
	}
	return CatalogPart{Name: p.Name, Description: p.Description, Levels: p.Levels, CostMS: cost, Manual: p.Manual, FixedCopy: p.HasFixedCopy, Source: p.Source, Origin: p.Origin, Verse: p.Verse, Hit: p.Hit, Scene: p.Scene, Timeline: p.Timeline(), Pictures: p.Pictures, Params: params}
}

// GET /api/music-motion/catalog
func (h *Handler) handleMusicMotionCatalog(w http.ResponseWriter, r *http.Request) {
	out := PieceCatalog{Parts: []CatalogPart{}, Characters: []CatalogCharacter{}, Backgrounds: []CatalogBackground{},
		Levels: musicmotion.Levels, Styles: musicmotion.Styles, FrameRates: musicmotion.FrameRates, Actions: musicmotion.Actions,
		Fonts: musicmotion.FontNames(), FontStacks: musicmotion.Fonts, FontFiles: musicmotion.FontFiles(), Modes: []string{musicmotion.ModeDark, musicmotion.ModeLight},
		Features: musicmotion.Features}
	// The scenes written for a piece are not in the list unless asked for: all of them with include=scene, or those of one piece
	// with piece=<id> (which lists only that piece's scenes besides the parts everybody uses).
	cat := h.mmCatalog()
	for _, p := range cat.List() {
		out.Parts = append(out.Parts, catalogPartOf(p))
	}
	q := r.URL.Query()
	switch {
	case q.Get("piece") != "":
		for _, p := range cat.ListScenes(q.Get("piece")) {
			out.Parts = append(out.Parts, catalogPartOf(p))
		}
	case q.Get("include") == "scene":
		for _, p := range cat.ListScenes("") {
			out.Parts = append(out.Parts, catalogPartOf(p))
		}
	}
	lib := h.mmLibrary()
	for _, c := range lib.Characters() {
		ex := c.Expressions
		if ex == nil {
			ex = []string{}
		}
		out.Characters = append(out.Characters, CatalogCharacter{Name: c.Name, Description: c.Description, Poses: c.PoseNames(), Expressions: ex, Source: c.Source})
	}
	for _, b := range lib.Backgrounds() {
		out.Backgrounds = append(out.Backgrounds, CatalogBackground{Name: b.Name, Description: b.Description, Source: b.Source})
	}
	api.WriteJSON(w, http.StatusOK, out)
}

// --- a piece as the API shows it ----------------------------------------------------------------

// PieceSource names the song of a piece: a track of a music project and its variant.
type PieceSource struct {
	Project string `json:"project"`
	TrackID string `json:"track_id"`
	Variant int    `json:"variant"`
}

// PieceView is a piece as the API shows it: its song, its state and progress, its script and its video once rendered.
type PieceView struct {
	ID           string                `json:"id"`
	State        string                `json:"state"` // draft, queued, running, done or failed
	Progress     int                   `json:"progress"`
	Title        string                `json:"title,omitempty"`
	Source       PieceSource           `json:"source"`
	Style        string                `json:"style"`
	Level        string                `json:"level"`
	FPS          int                   `json:"fps"`
	Width        int                   `json:"width"`
	Height       int                   `json:"height"`
	Client       string                `json:"client"`
	CreatedAt    time.Time             `json:"created_at"`
	RunID        string                `json:"run_id,omitempty"`
	Estimate     *musicmotion.Estimate `json:"estimate,omitempty"`
	Video        string                `json:"video,omitempty"`
	Error        string                `json:"error,omitempty"`
	SizeBytes    int64                 `json:"size_bytes"`
	Design       *musicmotion.Stored   `json:"design,omitempty"`
	Script       *musicmotion.Script   `json:"script,omitempty"`
	ScriptSource string                `json:"script_source,omitempty"`
	// FallbackPieces are the pieces of a fallback script that still use a part the fixed rule chose (no scene of their own).
	FallbackPieces []string `json:"fallback_pieces,omitempty"`
	// Preset is the style the piece started from, when it was made with one.
	Preset string `json:"preset,omitempty"`
	// Prompt is the direction the proposals read; Proposal the last proposal written in the background, when there was one.
	Prompt string `json:"prompt,omitempty"`
	// World, Device and Hook are the storyline of the piece (empty for a piece made without a plan).
	World  string `json:"world,omitempty"`
	Device string `json:"device,omitempty"`
	Hook   string `json:"hook,omitempty"`
	// Reference is the reference video the piece names, when it names one.
	Reference string    `json:"reference,omitempty"`
	Proposal  *Proposal `json:"proposal,omitempty"`
	// Scenes is the last batch of scenes written for the script of the piece, when there was one.
	Scenes *SceneBatch `json:"scenes,omitempty"`
	// Review is the last review of the whole piece by the vision model (`POST /pieces/{id}/review`), when there was one.
	Review  *PieceReview `json:"review,omitempty"`
	SongMap *songmap.Map `json:"song_map,omitempty"`
	// WordsNote says why the words were not heard when they were asked for; ScriptWarning that the script, which the
	// user edited, may need adjusting to the new sections. Both are only in the answer that causes them.
	WordsNote     string `json:"words_note,omitempty"`
	ScriptWarning string `json:"script_warning,omitempty"`
}

func (h *Handler) pieceViewOf(info *pieceInfo, full bool) PieceView {
	v := PieceView{ID: info.ID, State: "draft", Title: info.Title, Source: PieceSource{Project: info.Project, TrackID: info.TrackID, Variant: info.Variant},
		Style: info.Style, Level: info.Level, FPS: info.FPS, Width: info.Width, Height: info.Height, Client: info.Client, CreatedAt: info.CreatedAt,
		ScriptSource: info.ScriptSource, Preset: info.Preset, Prompt: info.Prompt, World: info.World, Device: info.Device, Hook: info.Hook, Reference: info.Reference}
	script, hasScript := musicmotion.LoadScriptAt(h.place(info.ID))
	if hasScript {
		// the parts the script draws are read once, for the estimate and for the pieces that are placeholders
		parts := h.mmCatalog().GetMany(script.PartNames())
		est := musicmotion.EstimateRenderWith(script, parts, info.FPS, info.Width, info.Height, h.mmHistory())
		v.Estimate = &est
		if info.ScriptSource == "fallback" {
			v.FallbackPieces = musicmotion.FallbackPiecesWith(script, parts)
		}
	}
	if info.RunID != "" {
		if run, err := h.ws.RunRecord(info.RunID); err == nil {
			state, msg := generation.StateOf(run)
			v.State, v.RunID, v.Error = state, run.ID, msg
			v.Progress = h.gen.ProgressOf(run, 3*len(script.Pieces)+1)
			if state == generation.GenDone {
				out := "out/" + info.ID + ".mp4"
				if fi, err := os.Stat(filepath.Join(h.pieceDir(info.ID), filepath.FromSlash(out))); err == nil && fi.Mode().IsRegular() {
					v.Video = "/api/projects/" + info.ID + "/artifacts/" + out
					h.recordPieceRender(info, run, script)
				} else {
					v.State, v.Error = generation.GenFailed, "the video is not there any more"
				}
			}
		}
	}
	// The size is kept with the piece: a piece that has none yet is measured once, and again after a render finished or its storage was cleaned.
	if info.SizeBytes == nil || (v.State == generation.GenDone && info.SizeRunID != info.RunID) {
		h.refreshPieceSize(info)
	}
	v.SizeBytes = *info.SizeBytes
	v.Proposal = h.proposalOf(info.ID)
	v.Scenes = h.sceneBatchOf(info.ID)
	v.Review = h.pieceReviewOf(info.ID)
	if full {
		if st, ok := musicmotion.LoadDesignAt(h.place(info.ID)); ok {
			v.Design = &st
		}
		if hasScript {
			v.Script = &script
		}
		if m, ok := h.pieceMap(info.ID); ok {
			v.SongMap = &m
		}
	}
	return v
}

// refreshPieceSize measures what the piece keeps on disk and keeps the number with the piece. It is the only place that walks the folder.
func (h *Handler) refreshPieceSize(info *pieceInfo) {
	n := h.ws.ProjectSize(info.ID)
	info.SizeBytes, info.SizeRunID = &n, info.RunID
	_ = h.writePiece(info)
}

// pieceStorageChanged measures a piece again when its storage was cleaned (a hook of the workspace).
func (h *Handler) pieceStorageChanged(id string) {
	if !strings.HasPrefix(id, piecePrefix) {
		return
	}
	if info, ok := h.readPiece(id); ok {
		h.refreshPieceSize(info)
	}
}

// recordPieceRender keeps the time of the first render of a piece in the render history: in the first render every
// scene is drawn, so its time against the forecast says how fast this machine is; later renders may come from the cache.
func (h *Handler) recordPieceRender(info *pieceInfo, run *Run, script musicmotion.Script) {
	if info.Recorded || info.Renders != 1 || run.StartedAt == nil || run.FinishedAt == nil {
		return
	}
	took := run.FinishedAt.Sub(*run.StartedAt).Seconds()
	_, frames := 0.0, 0
	for _, p := range script.Pieces {
		frames += int(math.Round((p.End - p.Start) * float64(info.FPS)))
	}
	_ = musicmotion.RecordRender(script, h.mmCatalog(), info.FPS, info.Width, info.Height, frames, took, h.mmHistory())
	info.Recorded = true
	_ = h.writePiece(info)
}

// --- create ------------------------------------------------------------------------------------

// PieceRequest is the body of POST /api/music-motion/pieces: the song, the style and the idea of the piece.
type PieceRequest struct {
	// Project, TrackID and Variant name the song: a track of a music project (as the videoclip does).
	Project    string  `json:"project"`
	TrackID    string  `json:"track_id"`
	Variant    int     `json:"variant"`
	Style      string  `json:"style,omitempty"`
	Level      string  `json:"level,omitempty"`
	FPS        int     `json:"fps,omitempty"`
	Resolution string  `json:"resolution,omitempty"`
	Client     *string `json:"client,omitempty"`
	// Design, when given, is the design of the piece (it is then the user's); otherwise a palette is chosen.
	Design *musicmotion.Design `json:"design,omitempty"`
	// Transcribe hears when each word is sung (Whisper, on the CPU: about a third of the song's length) instead of
	// spreading the lyrics over the sections. When it cannot be done the piece is made anyway, with the times
	// estimated, and words_note says why.
	Transcribe bool `json:"transcribe,omitempty"`
	// Separate says whether the voice is separated from the song before its words are heard (see SeparateMode); it only
	// matters with Transcribe. Absent is "auto".
	Separate SeparateMode `json:"separate,omitempty"`
	// Preset is the id of a style (see /api/music-motion/presets): the design of the piece starts as the style's and the
	// fixed rule uses the style's parts and rhythm. Without it the piece is made as always.
	Preset string `json:"preset,omitempty"`
	// Prompt is the person's direction for the video (at most 2000 characters); it is kept with the piece and read by the proposals.
	Prompt string `json:"prompt,omitempty"`
	// World, Device and Hook are the storyline of a plan (POST /api/assist/plan answers them): kept with the piece and read by the
	// proposals and the scene writer. Device is one of the devices the plan offers; World and Hook are one line each.
	World  string `json:"world,omitempty"`
	Device string `json:"device,omitempty"`
	Hook   string `json:"hook,omitempty"`
	// Reference is the id of a ready reference of the library (see /api/music-motion/references): its rhythm steers the proposals
	// and its shots the scenes. It is kept with the piece.
	Reference string `json:"reference,omitempty"`
}

const (
	mmDefaultResolution = "1280x720"
	mmSampleRate        = audiomeasure.Rate
)

func newPieceID() string { return newManagedID(piecePrefix) }

// songSamples decodes a song to mono samples at the rate of the measures.
func (h *Handler) songSamples(ctx context.Context, path string) ([]float32, error) {
	if _, err := h.host.LookPath("ffmpeg"); err != nil {
		return nil, fmt.Errorf("ffmpeg is needed to read the song")
	}
	raw, err := h.host.Runner().Run(ctx, "ffmpeg", []string{"-v", "error", "-i", path, "-vn", "-ac", "1", "-ar", fmt.Sprint(mmSampleRate), "-f", "f32le", "-"})
	if err != nil {
		return nil, fmt.Errorf("cannot read the song: %s", api.LastLines(err.Error(), 3))
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("the song has no audio")
	}
	if len(raw) > measureMaxBytes {
		return nil, fmt.Errorf("the song is too long (more than 20 minutes)")
	}
	samples := make([]float32, len(raw)/4)
	for i := range samples {
		samples[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[i*4:]))
	}
	return samples, nil
}

// POST /api/music-motion/pieces
func (h *Handler) handleMusicMotionCreate(w http.ResponseWriter, r *http.Request) {
	var req PieceRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
		return
	}
	if req.Style == "" {
		req.Style = musicmotion.StyleTypographic
	}
	if req.Level == "" {
		req.Level = musicmotion.LevelSimple
	}
	if req.Resolution == "" {
		req.Resolution = mmDefaultResolution
	}
	if !slices.Contains(musicmotion.Styles, req.Style) {
		api.Refuse(w, "style %q is not one of %s", req.Style, strings.Join(musicmotion.Styles, ", "))
		return
	}
	if n := len([]rune(req.Prompt)); n > musicmotion.MaxPrompt {
		api.Refuse(w, "the prompt has %d characters: at most %d", n, musicmotion.MaxPrompt)
		return
	}
	if req.Device != "" && !slices.Contains(assist.PlanDevices, req.Device) {
		api.Refuse(w, "device %q is not one of %s", req.Device, strings.Join(assist.PlanDevices, ", "))
		return
	}
	if len([]rune(req.World)) > assist.MaxPlanLine || len([]rune(req.Hook)) > assist.MaxPlanLine {
		api.Refuse(w, "world and hook are one line each: at most %d characters", assist.MaxPlanLine)
		return
	}
	if !h.checkReference(w, req.Reference) {
		return
	}
	if !req.Separate.Valid() {
		api.Refuse(w, "separate must be true, false or %q", SeparateAuto)
		return
	}
	if musicmotion.LevelRank(req.Level) < 0 {
		api.Refuse(w, "level %q is not one of %s", req.Level, strings.Join(musicmotion.Levels, ", "))
		return
	}
	fps, err := musicmotion.ResolveFPS(req.Style, req.FPS)
	if err != nil {
		api.Refuse(w, "%v", err)
		return
	}
	width, height, err := mmrender.ParseResolution(req.Resolution)
	if err != nil {
		api.Refuse(w, "resolution %q is not valid: %v", req.Resolution, err)
		return
	}
	client, ok := requestClient(w, req.Client)
	if !ok {
		return
	}
	if !api.ProjectIDPattern.MatchString(req.Project) || !h.ws.ProjectExists(req.Project) {
		api.Refuse(w, "the music project %q does not exist", req.Project)
		return
	}
	song, _, err := h.ws.SongOf(req.Project, req.TrackID, req.Variant)
	if err != nil {
		api.Refuse(w, "%v", err)
		return
	}
	if req.Design != nil {
		if err := req.Design.Validate(); err != nil {
			api.Refuse(w, "%v", err)
			return
		}
	}
	var preset *musicmotion.Preset
	if req.Preset != "" {
		p, err := h.gen.PresetStore().Get(req.Preset)
		if err != nil {
			api.Refuse(w, "the style %q does not exist", req.Preset)
			return
		}
		preset = &p
		if req.Design == nil { // the style's design, which the user can still edit
			d := p.Design
			req.Design = &d
		}
	}
	src := filepath.Join(h.ws.ProjectDir(req.Project), filepath.FromSlash(song.File))
	samples, err := h.songSamples(r.Context(), src)
	if err != nil {
		api.Refuse(w, "%v", err)
		return
	}
	lyrics := ""
	if !song.Instrumental {
		lyrics = song.LyricsSung
		if strings.TrimSpace(lyrics) == "" {
			lyrics = song.LyricsRequested
		}
	}
	bpm := float64(song.BPM)
	if bpm <= 0 {
		if m := audiomeasure.Measure(samples); m.BPM != nil {
			bpm = *m.BPM
		}
	}
	data, err := os.ReadFile(src)
	if err != nil {
		api.Refuse(w, "cannot read the song: %v", err)
		return
	}
	sum := sha256.Sum256(data)
	in := songmap.Input{BPM: bpm, Lyrics: lyrics, EstimateWords: true, AudioKey: hex.EncodeToString(sum[:])}
	id := newPieceID()
	dir := h.pieceDir(id)
	cleanup := func() { _ = os.RemoveAll(dir) }
	wordsNote := ""
	if req.Transcribe {
		hr, problem, herr := h.hearWords(r.Context(), src, song.VocalLanguage, lyrics, dir, req.Separate)
		switch {
		case problem != nil && hr.separationRefused:
			cleanup() // nothing was made: the voice was asked for and cannot be separated
			problem.write(w)
			return
		case problem != nil:
			wordsNote = "the words were not heard: " + problem.msg
		case herr != nil && hr.separationRefused:
			cleanup()
			if r.Context().Err() == nil {
				api.WriteError(w, http.StatusBadGateway, api.CodeInternal, herr.Error())
			}
			return
		case herr != nil:
			wordsNote = "the words were not heard: " + herr.Error()
		case len(hr.Heard.Words) == 0:
			wordsNote = joinNotes("no words were heard in the song", hr.Note)
		default:
			in.Heard, in.HeardSource, in.Voice, in.Attacks = hr.Heard.Words, hr.Source, hr.Env, hr.Attacks
			wordsNote = joinNotes(timingNote(hr.Heard), hr.Note)
		}
	}
	m := songmap.Analyse(samples, mmSampleRate, in)

	if err := os.MkdirAll(dir, 0o755); err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	songFile := "song" + strings.ToLower(filepath.Ext(song.File))
	if err := os.WriteFile(filepath.Join(dir, songFile), data, 0o644); err != nil {
		cleanup()
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	docs := musicmotion.NewMemPlace() // the documents of the piece are kept with it when it is complete, or not at all
	if raw, err := songmap.Encode(m); err == nil {
		_ = docs.WriteDoc(musicmotion.DocSongMap, raw)
	} else {
		cleanup()
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	tags := make([]string, 0, len(m.Sections))
	for _, sec := range m.Sections {
		tags = append(tags, sec.Tag)
	}
	var design musicmotion.Stored
	if req.Design != nil {
		design, err = musicmotion.SetDesignAt(docs, *req.Design)
	} else {
		design, err = musicmotion.ResolveAt(r.Context(), nil, docs, musicmotion.Song{Style: song.Caption, Lyrics: lyrics, SectionTags: tags}, false)
	}
	if err != nil {
		cleanup()
		api.Refuse(w, "%v", err)
		return
	}
	scriptIn := musicmotion.ScriptInput{Map: m, Design: design.Design, Level: req.Level, Cat: h.mmCatalog()}
	scriptIn.Reference, _ = h.referenceSheet(&pieceInfo{Reference: strings.TrimSpace(req.Reference)})
	if preset != nil {
		scriptIn.Parts, scriptIn.Rhythm = preset.Parts, preset.Rhythm
	}
	prop, err := musicmotion.ProposeScript(r.Context(), nil, scriptIn)
	if err != nil {
		cleanup()
		api.Refuse(w, "%v", err)
		return
	}
	if req.Style == musicmotion.StyleIllustrated {
		musicmotion.AddStages(&prop.Script, h.mmLibrary(), design.Design)
	}
	if err := prop.Script.ValidateAll(h.mmCatalog(), h.mmLibrary(), m, design.Design, width, height); err != nil {
		cleanup()
		api.Refuse(w, "%v", err)
		return
	}
	if err := musicmotion.SaveScriptAt(docs, prop.Script); err != nil {
		cleanup()
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	title := song.Title
	info := &pieceInfo{ID: id, CreatedAt: time.Now().UTC(), Project: req.Project, TrackID: req.TrackID, Variant: req.Variant, Title: title, Style: req.Style,
		Level: req.Level, FPS: fps, Width: width, Height: height, Client: client, SongFile: songFile, Caption: song.Caption, ScriptSource: prop.Source,
		Lyrics: lyrics, Language: song.VocalLanguage, Preset: req.Preset, Prompt: strings.TrimSpace(req.Prompt), World: clipLine(req.World), Device: req.Device, Hook: clipLine(req.Hook), Reference: strings.TrimSpace(req.Reference)}
	if err := h.pieces().create(info, docs.Docs); err != nil {
		cleanup()
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	view := h.pieceViewOf(info, true)
	view.WordsNote = wordsNote
	api.WriteJSON(w, http.StatusCreated, view)
}

// --- read --------------------------------------------------------------------------------------

func (h *Handler) pieceFromPath(w http.ResponseWriter, r *http.Request) (*pieceInfo, bool) {
	id := r.PathValue("id")
	info, ok := h.readPiece(id)
	if !ok {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("piece %q not found", id))
		return nil, false
	}
	return info, true
}

func sortPieces(v []PieceView) {
	for i := 1; i < len(v); i++ {
		for j := i; j > 0 && (v[j].CreatedAt.After(v[j-1].CreatedAt) || (v[j].CreatedAt.Equal(v[j-1].CreatedAt) && v[j].ID > v[j-1].ID)); j-- {
			v[j], v[j-1] = v[j-1], v[j]
		}
	}
}

// GET /api/music-motion/pieces/{id}
func (h *Handler) handleMusicMotionGet(w http.ResponseWriter, r *http.Request) {
	info, ok := h.pieceFromPath(w, r)
	if !ok {
		return
	}
	api.WriteJSON(w, http.StatusOK, h.pieceViewOf(info, true))
}

// --- edit --------------------------------------------------------------------------------------

func (h *Handler) pieceMap(id string) (songmap.Map, bool) {
	raw, ok := h.place(id).ReadDoc(musicmotion.DocSongMap)
	if !ok {
		return songmap.Map{}, false
	}
	var m songmap.Map
	return m, json.Unmarshal(raw, &m) == nil
}

// savePieceMap keeps the song map of a piece.
func (h *Handler) savePieceMap(id string, m songmap.Map) error {
	raw, err := songmap.Encode(m)
	if err != nil {
		return err
	}
	return h.place(id).WriteDoc(musicmotion.DocSongMap, raw)
}

// refuseIfRendering stops an edit while the piece is being drawn: the files of the project are in use.
func (h *Handler) refuseIfRendering(w http.ResponseWriter, info *pieceInfo) bool {
	if h.refuseIfProposing(w, info) {
		return true
	}
	if run, busy := h.ws.ProjectBusy(info.ID); busy {
		api.WriteError(w, http.StatusConflict, api.CodeConflict, fmt.Sprintf("run %s is drawing this piece: wait for it or cancel it", run))
		return true
	}
	return false
}

// PUT /api/music-motion/pieces/{id}/design
func (h *Handler) handleMusicMotionSetDesign(w http.ResponseWriter, r *http.Request) {
	info, ok := h.pieceFromPath(w, r)
	if !ok || h.refuseIfRendering(w, info) {
		return
	}
	var d musicmotion.Design
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&d); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
		return
	}
	before, hadDesign := musicmotion.LoadDesignAt(h.place(info.ID))
	// A script that is already stored must still be valid with the new design.
	script, has := musicmotion.LoadScriptAt(h.place(info.ID))
	if has {
		if m, ok := h.pieceMap(info.ID); ok {
			if err := script.WithSources(h.pieceSources(info.ID)).ValidateAll(h.mmCatalog(), h.mmLibrary(), m, d, info.Width, info.Height); err != nil {
				api.Refuse(w, "%v", err)
				return
			}
		}
	}
	st, err := musicmotion.SetDesignAt(h.place(info.ID), d)
	if err != nil {
		api.Refuse(w, "%v", err)
		return
	}
	if has && hadDesign && musicmotion.ModesChanged(before.Design, d) {
		st.Notice = musicmotion.OwnModeNotice(script, d)
	}
	api.WriteJSON(w, http.StatusOK, st)
}

// PUT /api/music-motion/pieces/{id}/script
func (h *Handler) handleMusicMotionSetScript(w http.ResponseWriter, r *http.Request) {
	info, ok := h.pieceFromPath(w, r)
	if !ok || h.refuseIfRendering(w, info) {
		return
	}
	var script musicmotion.Script
	api.RaiseBodyLimit(w, r, 4<<20)
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&script); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
		return
	}
	m, ok := h.pieceMap(info.ID)
	st, hasDesign := musicmotion.LoadDesignAt(h.place(info.ID))
	if !ok || !hasDesign {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, "the piece has lost its song map or its design")
		return
	}
	if err := script.WithSources(h.pieceSources(info.ID)).ValidateAll(h.mmCatalog(), h.mmLibrary(), m, st.Design, info.Width, info.Height); err != nil {
		api.Refuse(w, "%v", err)
		return
	}
	if err := script.CheckSceneParts(h.mmCatalog(), info.ID); err != nil {
		api.Refuse(w, "%v", err)
		return
	}
	if err := musicmotion.SaveScriptAt(h.place(info.ID), script); err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	info.ScriptEdited, info.ScriptSource = true, "user"
	_ = h.writePiece(info)
	api.WriteJSON(w, http.StatusOK, script)
}

// lineTexts lists the sung lines of a song map, in order.
func lineTexts(m songmap.Map) []string {
	var out []string
	last := -1
	for _, w := range m.Words {
		if w.Line != last || len(out) == 0 {
			out = append(out, w.Text)
			last = w.Line
			continue
		}
		out[len(out)-1] += " " + w.Text
	}
	return out
}

// --- render ------------------------------------------------------------------------------------

// RenderAnswer is the answer of the render of a piece: the run that makes the video and the estimate of how long it takes.
type RenderAnswer struct {
	RunID    string               `json:"run_id"`
	Estimate musicmotion.Estimate `json:"estimate"`
}

// POST /api/music-motion/pieces/{id}/render
func (h *Handler) handleMusicMotionRender(w http.ResponseWriter, r *http.Request) {
	info, ok := h.pieceFromPath(w, r)
	if !ok || h.refuseIfProposing(w, info) {
		return
	}
	dir := h.pieceDir(info.ID)
	m, okMap := h.pieceMap(info.ID)
	st, okDesign := musicmotion.LoadDesignAt(h.place(info.ID))
	script, okScript := musicmotion.LoadScriptAt(h.place(info.ID))
	if !okMap || !okDesign || !okScript {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, "the piece has lost its song map, its design or its script")
		return
	}
	cat, lib := h.mmCatalog(), h.mmLibrary()
	if err := script.WithSources(h.pieceSources(info.ID)).ValidateAll(cat, lib, m, st.Design, info.Width, info.Height); err != nil {
		api.Refuse(w, "the script is not valid: %v", err)
		return
	}
	if id, busy := h.ws.ProjectBusy(info.ID); busy {
		api.WriteError(w, http.StatusConflict, api.CodeConflict, fmt.Sprintf("run %s is already drawing this piece", id))
		return
	}
	pages, scenes3D := 0, 0
	for _, p := range script.Pieces {
		if p.Blender != nil {
			scenes3D++
		} else {
			pages++
		}
	}
	if pages > 0 { // the browser draws the pieces made from parts; a script of 3D pieces only does not need it
		if _, err := h.ws.MotionBrowserFor(); err != nil {
			api.WriteError(w, http.StatusServiceUnavailable, api.CodeUnavailable, err.Error())
			return
		}
	}
	if scenes3D > 0 {
		if err := h.ws.BlenderUsable(r.Context()); err != nil {
			api.WriteError(w, http.StatusServiceUnavailable, api.CodeUnavailable, "the 3D pieces are drawn by Blender: "+err.Error())
			return
		}
	}
	spec := musicmotion.ProjectSpec{Dir: dir, Name: info.ID, SongFile: filepath.Join(dir, info.SongFile), Map: m, Design: st.Design, Script: script,
		Cat: cat, Lib: lib, FPS: info.FPS, Width: info.Width, Height: info.Height, Client: info.Client}
	if _, err := musicmotion.WriteProject(spec); err != nil {
		api.Refuse(w, "%v", err)
		return
	}
	if err := h.ws.SyncProjectFromFolder(info.ID, "run"); err != nil { // the project of the piece is in the database before the run
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	if err := h.ws.AdoptProject(info.ID); err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	est := musicmotion.EstimateRender(script, cat, info.FPS, info.Width, info.Height, h.mmHistory())
	rec, _, lerr := h.ws.Launch(info.ID, runRequest{GPU: h.ws.GPUForRun(info.ID, runRequest{}), KeepTmp: true})
	if lerr != nil {
		api.WriteError(w, lerr.Status, lerr.Code, lerr.Msg)
		return
	}
	info.RunID = rec.ID
	info.Renders++
	if err := h.writePiece(info); err != nil {
		_, _ = h.ws.CancelRun(rec.ID)
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	api.WriteJSON(w, http.StatusAccepted, RenderAnswer{RunID: rec.ID, Estimate: est})
}

// DELETE /api/music-motion/pieces/{id}
func (h *Handler) handleMusicMotionDelete(w http.ResponseWriter, r *http.Request) {
	info, ok := h.pieceFromPath(w, r)
	if !ok {
		return
	}
	h.proposals.cancel(info.ID)
	if lerr := h.ws.RemoveManaged(info.ID); lerr != nil {
		api.WriteError(w, lerr.Status, lerr.Code, lerr.Msg)
		return
	}
	h.deletePieceRows(info.ID) // its rows and the scenes written for it go together
	h.deleteScenesOf(info.ID)  // and the drafts of those scenes
	w.WriteHeader(http.StatusNoContent)
}

// --- hearing the words ---------------------------------------------------------------------------

// timingNote says how the times of the words were found when it was not by the time of each token: the model has no DTW preset
// (or the run gave none), so each word has the span of the slice Whisper cut it in. Empty when they are token times.
func timingNote(h transcribe.Heard) string {
	if h.Timing == transcribe.TimingSegments {
		return "the words were timed by segments, not by token: this model has no word-timing preset (or the run gave no token times), so a word may land early or late by a good part of a second"
	}
	return ""
}

// SeparateMode says whether the voice of a song is separated from the rest before its words are heard: "auto" (the default:
// the voice is separated when the separator is installed and fits, and the mix heard with a note otherwise; see separateByDefault),
// "true" (always: the request is refused when it
// cannot) or "false" (never). In JSON it is one of those strings or a boolean.
type SeparateMode string

const (
	// SeparateAuto hears the mix, or the voice kept with the piece when there is one; with separateByDefault it would separate
	// the voice when the separator is installed and fits.
	SeparateAuto SeparateMode = "auto"
	// SeparateOn always separates the voice and refuses the request when it cannot.
	SeparateOn SeparateMode = "true"
	// SeparateOff never separates the voice.
	SeparateOff SeparateMode = "false"
)

// UnmarshalJSON reads a boolean or a string; whether the string is one of the modes is checked by Valid.
func (m *SeparateMode) UnmarshalJSON(b []byte) error {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	switch x := v.(type) {
	case bool:
		*m = SeparateOff
		if x {
			*m = SeparateOn
		}
	case string:
		*m = SeparateMode(strings.ToLower(strings.TrimSpace(x)))
	case nil:
		*m = ""
	default:
		return fmt.Errorf("separate must be true, false or %q", SeparateAuto)
	}
	return nil
}

// Valid says whether the mode is one of the three (empty is "auto").
func (m SeparateMode) Valid() bool {
	return m == "" || m == SeparateAuto || m == SeparateOn || m == SeparateOff
}

// resolved is the mode with the default filled in.
func (m SeparateMode) resolved() SeparateMode {
	if m == "" {
		return SeparateAuto
	}
	return m
}

// separateByDefault says whether SeparateAuto separates the voice, when the separator is installed and fits. It does, by the rule of
// the bench against the attacks of the voice (`veritmo words bench`, 2026-11-14, ten distinct songs made with HeartMuLa, an
// Intel Core Ultra 7 265, large-v3-turbo-q5_0): moving the heard words onto the attacks lifts the share within 50 ms of an attack from
// 26.4 % to 62.0 % on the median song (33.7 points, the rule asks for 20) and the whole voice way takes 1.22 times the length of the
// song (the rule asks for under 3). Two things the numbers do not say: the gain is by construction (a word moved onto an attack is
// within 50 ms of it), so it measures how many words were moved, not whether each went to its own syllable; and the separation alone,
// before the snap, did not improve the alignment (26.4 % within 50 ms against 28.3 % on the mix). So `auto` costs about 110 s per
// 90 s song for the snap and the cleaner voice; `separate: false` is the fast way and flipping this constant restores the mix as the
// default. See docs/MUSIC_MODE_MOTION.md, "Timing the words".
const separateByDefault = true

// stemFile is the voice of the song of a piece, kept next to its song map so that hearing the words again does not
// separate it again; it goes when the piece goes.
const stemFile = "vocals-16k.wav"

// minVoicedShare is the share of the song in which a voice must be heard for a separated stem to count: under it the
// separation found nothing to keep (an instrumental) and the mix is heard instead.
const minVoicedShare = 0.05

// joinNotes joins the notes of a hearing with "; ", leaving out the empty ones.
func joinNotes(notes ...string) string {
	var out []string
	for _, n := range notes {
		if n != "" {
			out = append(out, n)
		}
	}
	return strings.Join(out, "; ")
}

// hearing is the result of hearing the words of a song.
type hearing struct {
	Heard transcribe.Heard
	// Source is what the words were read from: songmap.SourceHeard (the mix) or songmap.SourceHeardVoice.
	Source string
	// Env is the envelope of the separated voice and Attacks its attacks; nil when the mix was heard.
	Env     *separate.Env
	Attacks []float64
	// Note says why the voice was not separated, or why a stem was not used; empty when it was used or not asked for.
	Note string
	// separationRefused is set, together with the problem or the error returned, when the voice was asked for with
	// SeparateOn and could not be separated: the caller refuses the request instead of hearing the mix.
	separationRefused bool
}

// voiceStem is the separated voice of a song: its 16 kHz wav, its envelope and its attacks (empty Path for none).
type voiceStem struct {
	Path    string
	Env     *separate.Env
	Attacks []float64
}

// voiceOf gives the voice of the song (a kept stem, or one separated now and kept in keepDir) with its envelope and its attacks.
// An empty Path with a note means the mix is to be heard. problem and err are only returned when mode is SeparateOn and the voice
// cannot be had; with SeparateAuto they become the note. tmp is a folder the caller removes.
func (h *Handler) voiceOf(ctx context.Context, song, keepDir, tmp string, mode SeparateMode) (v voiceStem, note string, problem *toolProblem, err error) {
	if mode == SeparateOff {
		return voiceStem{}, "", nil, nil
	}
	separating := mode == SeparateOn || separateByDefault
	fail := func(why string, p *toolProblem, e error) (voiceStem, string, *toolProblem, error) {
		if mode == SeparateOn {
			return voiceStem{}, "", p, e
		}
		return voiceStem{}, "the voice was not separated, so the words were heard in the mix: " + why, nil, nil
	}
	useStem := func(stem string) (voiceStem, string, bool) {
		e, attacks, err := separate.AnalyseFile(stem)
		if err != nil {
			return voiceStem{}, "the separated voice could not be read, so the words were heard in the mix: " + err.Error(), false
		}
		if share := e.VoicedShare(); share < minVoicedShare {
			return voiceStem{}, fmt.Sprintf("the separated voice has a voice in only %.0f %% of the song, so the words were heard in the mix", share*100), false
		}
		return voiceStem{Path: stem, Env: &e, Attacks: attacks}, "", true
	}
	kept := filepath.Join(keepDir, stemFile)
	if _, serr := os.Stat(kept); serr == nil {
		if got, n, ok := useStem(kept); ok {
			return got, "", nil, nil
		} else if n != "" {
			note = n
		}
	}
	if !separating { // SeparateAuto, and the measure says the mix is as good: a voice kept with the piece was used above
		return voiceStem{}, note, nil, nil
	}
	bin, model, p := h.separateReady()
	if p == nil {
		p = h.separateFits()
	}
	if p != nil {
		return fail(p.msg, p, nil)
	}
	wav, serr := h.separateVoice(ctx, bin, model, song, tmp)
	if serr != nil {
		if ctx.Err() != nil {
			return voiceStem{}, "", nil, ctx.Err()
		}
		return fail("separating it failed: "+serr.Error(), nil, serr)
	}
	got, n, ok := useStem(wav)
	if !ok {
		return voiceStem{}, joinNotes(note, n), nil, nil
	}
	if err := os.MkdirAll(keepDir, 0o755); err == nil {
		if raw, rerr := os.ReadFile(got.Path); rerr == nil && os.WriteFile(kept, raw, 0o644) == nil {
			got.Path = kept
		}
	}
	return got, "", nil, nil // when it could not be kept, it still serves now
}

// hearWords reads the words of a song by word with Whisper, on the voice separated from it when mode and the machine allow
// it (see SeparateMode); keepDir is where a separated voice is kept. problem is why it cannot be done now (Whisper or ffmpeg
// missing, not enough memory, a run in progress, or, with SeparateOn, the separator), err what went wrong while doing it.
// When the voice stem has the song's lyrics and Whisper recognises fewer than songmap.VoiceLossRatio of their words on it, the mix is
// read as well and the hearing that recognises more is kept (the stem on a tie), always placed with the stem's envelope and
// attacks.
func (h *Handler) hearWords(ctx context.Context, song, language, lyrics, keepDir string, mode SeparateMode) (hearing, *toolProblem, error) {
	mode = mode.resolved()
	bin, model, size, problem := h.whisperReady()
	if problem != nil {
		return hearing{}, problem, nil
	}
	if problem := h.whisperRoom(size); problem != nil {
		return hearing{}, problem, nil
	}
	dir, err := os.MkdirTemp("", "hear-*")
	if err != nil {
		return hearing{}, nil, err
	}
	defer os.RemoveAll(dir)
	out := hearing{Source: songmap.SourceHeard}
	voice, note, vproblem, verr := h.voiceOf(ctx, song, keepDir, dir, mode)
	if vproblem != nil || verr != nil {
		out.separationRefused = mode == SeparateOn && ctx.Err() == nil
		return out, vproblem, verr
	}
	out.Note = note
	ctx, cancel := context.WithTimeout(ctx, transcribeTimeout)
	defer cancel()
	tool := transcribe.Tool{Runner: h.host.Runner(), Binary: bin, Model: model}
	// mixWav converts the song itself to the 16 kHz mono wav Whisper reads.
	mixWav := func() (string, error) {
		wav := filepath.Join(dir, "audio.wav")
		if _, err := h.host.Runner().Run(ctx, "ffmpeg", []string{"-y", "-v", "error", "-i", song, "-vn", "-ac", "1", "-ar", "16000", wav}); err != nil {
			return "", fmt.Errorf("cannot read the song: %s", api.LastLines(err.Error(), 3))
		}
		return wav, nil
	}
	// hear runs Whisper on a wav and turns its failures into the answers of this function.
	hear := func(wav string) (transcribe.Heard, error) {
		words, err := tool.RunWords(ctx, wav, language)
		if err != nil {
			if ctx.Err() == context.DeadlineExceeded {
				return transcribe.Heard{}, fmt.Errorf("hearing the words took more than 10 minutes and was stopped")
			}
			return transcribe.Heard{}, err
		}
		return words, nil
	}
	wav := voice.Path
	if wav != "" {
		out.Source, out.Env, out.Attacks = songmap.SourceHeardVoice, voice.Env, voice.Attacks
	} else {
		var err error
		if wav, err = mixWav(); err != nil {
			return hearing{}, nil, err
		}
	}
	words, err := hear(wav)
	if err != nil {
		return hearing{}, nil, err
	}
	out.Heard = words
	if voice.Path != "" {
		if err := h.hearMixIfVoiceLost(&out, lyrics, mixWav, hear); err != nil {
			return hearing{}, nil, err
		}
	}
	return out, nil, nil
}

// hearMixIfVoiceLost compares what the stem's hearing recognised of the written lyrics with songmap.VoiceLossRatio and, when it is
// under it, hears the mix as well and keeps the hearing that recognises more words (the stem on a tie). The envelope and
// the attacks of the stem stay in out either way, and Source stays heard-voice: the voice was separated and gives the times.
// Without lyrics there is nothing to count against and nothing is done.
func (h *Handler) hearMixIfVoiceLost(out *hearing, lyrics string, mixWav func() (string, error), hear func(string) (transcribe.Heard, error)) error {
	written := songmap.WrittenWords(lyrics)
	if len(written) == 0 {
		return nil
	}
	voiceMatched := songmap.MatchedCount(written, out.Heard.Words, out.Env)
	if !songmap.VoiceLost(voiceMatched, len(written)) {
		return nil
	}
	wav, err := mixWav()
	if err != nil {
		return err
	}
	mix, err := hear(wav)
	if err != nil {
		return err
	}
	if mixMatched := songmap.MatchedCount(written, mix.Words, out.Env); mixMatched > voiceMatched {
		out.Heard = mix
		out.Note = joinNotes(out.Note, fmt.Sprintf("the separated voice heard %d of %d written words and the mix %d: the words were heard on the mix and placed with the voice's attacks", voiceMatched, len(written), mixMatched))
	}
	return nil
}

// POST /api/music-motion/pieces/{id}/transcribe
func (h *Handler) handleMusicMotionTranscribe(w http.ResponseWriter, r *http.Request) {
	info, ok := h.pieceFromPath(w, r)
	if !ok {
		return
	}
	var req struct {
		Separate SeparateMode `json:"separate"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
		return
	}
	if !req.Separate.Valid() {
		api.Refuse(w, "separate must be true, false or %q", SeparateAuto)
		return
	}
	dir := h.pieceDir(info.ID)
	m, okMap := h.pieceMap(info.ID)
	st, okDesign := musicmotion.LoadDesignAt(h.place(info.ID))
	if !okMap || !okDesign {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, "the piece has lost its song map or its design")
		return
	}
	lyrics, language := info.Lyrics, info.Language
	if lyrics == "" && info.Project != "" { // a piece made before the lyrics were kept
		if song, _, err := h.ws.SongOf(info.Project, info.TrackID, info.Variant); err == nil && !song.Instrumental {
			lyrics = song.LyricsSung
			if strings.TrimSpace(lyrics) == "" {
				lyrics = song.LyricsRequested
			}
			language = song.VocalLanguage
		}
	}
	hr, problem, herr := h.hearWords(r.Context(), filepath.Join(dir, info.SongFile), language, lyrics, dir, req.Separate)
	if problem != nil {
		problem.write(w)
		return
	}
	if herr != nil {
		if r.Context().Err() == nil {
			api.WriteError(w, http.StatusBadGateway, api.CodeInternal, herr.Error())
		}
		return
	}
	heard := hr.Heard
	if len(heard.Words) == 0 {
		view := h.pieceViewOf(info, true)
		view.WordsNote = joinNotes("no words were heard in the song: the map keeps its times", hr.Note)
		api.WriteJSON(w, http.StatusOK, view)
		return
	}
	m = songmap.WithHeard(m, lyrics, heard.Words, hr.Env, hr.Attacks, hr.Source)
	if err := h.savePieceMap(info.ID, m); err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	info.Lyrics, info.Language = lyrics, language
	warning, ok := h.followTheMap(w, info, m, st.Design)
	if !ok {
		return
	}
	if err := h.writePiece(info); err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	view := h.pieceViewOf(info, true)
	view.ScriptWarning = warning
	view.WordsNote = joinNotes(timingNote(heard), hr.Note)
	api.WriteJSON(w, http.StatusOK, view)
}

// followTheMap makes the script of a piece follow its song map after the times of the words changed: a script the user did not
// edit is proposed again with the fixed rule; one the user edited is kept, and the warning says how many of its pieces end
// up more than a second from a boundary of the new sections. It answers the error itself and says false when it fails.
func (h *Handler) followTheMap(w http.ResponseWriter, info *pieceInfo, m songmap.Map, d musicmotion.Design) (warning string, ok bool) {
	if !info.ScriptEdited {
		script, err := h.fixedScriptFor(info, m, d)
		if err != nil {
			api.Refuse(w, "%v", err)
			return "", false
		}
		if err := musicmotion.SaveScriptAt(h.place(info.ID), script); err != nil {
			api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
			return "", false
		}
		info.ScriptSource = "fallback"
		return "", true
	}
	if script, has := musicmotion.LoadScriptAt(h.place(info.ID)); has {
		if n := piecesOffTheSections(script, m); n > 0 {
			warning = fmt.Sprintf("the script was edited by the user and was kept; %d of its pieces start or end more than a second from a boundary of the new sections, so it may need adjusting", n)
		}
	}
	return warning, true
}

// WordsRequest is the body of PATCH /api/music-motion/pieces/{id}/words.
type WordsRequest struct {
	Words []songmap.WordMove `json:"words"`
}

// PATCH /api/music-motion/pieces/{id}/words: moves words of the song map by hand (see songmap.MoveWords).
func (h *Handler) handleMusicMotionWords(w http.ResponseWriter, r *http.Request) {
	info, ok := h.pieceFromPath(w, r)
	if !ok || h.refuseIfRendering(w, info) {
		return
	}
	var req WordsRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
		return
	}
	m, okMap := h.pieceMap(info.ID)
	st, okDesign := musicmotion.LoadDesignAt(h.place(info.ID))
	if !okMap || !okDesign {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, "the piece has lost its song map or its design")
		return
	}
	moved, err := songmap.MoveWords(m, info.Lyrics, req.Words)
	if err != nil {
		api.Refuse(w, "%v", err)
		return
	}
	warning, ok := h.followTheMap(w, info, moved, st.Design)
	if !ok {
		return
	}
	if err := h.savePieceMap(info.ID, moved); err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	if err := h.writePiece(info); err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	view := h.pieceViewOf(info, true)
	view.ScriptWarning = warning
	api.WriteJSON(w, http.StatusOK, view)
}

// fixedScriptFor makes the script of a piece with the fixed rule (no assistant), for its level and style.
func (h *Handler) fixedScriptFor(info *pieceInfo, m songmap.Map, d musicmotion.Design) (musicmotion.Script, error) {
	prop, err := musicmotion.ProposeScript(context.Background(), nil, musicmotion.ScriptInput{Map: m, Design: d, Level: info.Level, Cat: h.mmCatalog()})
	if err != nil {
		return musicmotion.Script{}, err
	}
	if info.Style == musicmotion.StyleIllustrated {
		musicmotion.AddStages(&prop.Script, h.mmLibrary(), d)
	}
	if err := prop.Script.WithSources(h.pieceSources(info.ID)).ValidateAll(h.mmCatalog(), h.mmLibrary(), m, d, info.Width, info.Height); err != nil {
		return musicmotion.Script{}, err
	}
	return prop.Script, nil
}

// piecesOffTheSections counts the pieces of a script that start or end more than a second from every boundary of the
// sections of the map (the start and the end of the song count as boundaries).
func piecesOffTheSections(script musicmotion.Script, m songmap.Map) int {
	bounds := []float64{0, m.Duration}
	for _, sec := range m.Sections {
		bounds = append(bounds, sec.Start, sec.End)
	}
	off := func(t float64) bool {
		for _, b := range bounds {
			if math.Abs(b-t) <= 1 {
				return false
			}
		}
		return true
	}
	n := 0
	for _, p := range script.Pieces {
		if off(p.Start) || off(p.End) {
			n++
		}
	}
	return n
}

// UserPart is a part of the user's folder as it is carried to another server: its manifest and its part.html.
type UserPart struct {
	Name     string                  `json:"name"`
	Source   string                  `json:"source"`
	Origin   *musicmotion.PartOrigin `json:"origin,omitempty"`
	Manifest musicmotion.Manifest    `json:"manifest"`
	Fragment string                  `json:"fragment"`
}

// GET /api/music-motion/parts/{name}: the manifest and the part.html of a part, the user's or one that comes with the
// program. The part is found through the catalog, never through a path built from the request, so no name can reach
// outside the folders.
func (h *Handler) handleMusicMotionPartGet(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	p, ok := h.mmCatalog().Get(name)
	if !ok {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("part %q not found", name))
		return
	}
	api.WriteJSON(w, http.StatusOK, UserPart{Name: p.Name, Source: p.Source, Origin: p.Origin, Manifest: p.Manifest, Fragment: p.Fragment})
}
