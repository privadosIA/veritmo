package pipeline

// motion.go: the motion-graphics scenes and overlays of a video project. The page of each is
// a file of the project (motion/), written by the language model the first time and reused
// after; Chrome plays it frame by frame to a video (see internal/video/motion).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"veritmo/internal/music/hyperframes"
	"veritmo/internal/music/songmap"
	"veritmo/internal/platform/cache"
	"veritmo/internal/platform/events"
	"veritmo/internal/platform/runner"
	"veritmo/internal/platform/settings"
	"veritmo/internal/project/config"
	"veritmo/internal/video/assemble"
	"veritmo/internal/video/motion"
	"veritmo/internal/video/render"
)

// Seams for the tests, which have no Chrome or model to hand.
var (
	newMotionWriter = motion.NewWriter
	renderMotion    = motion.Render
	findBrowser     = func() (motion.Browser, error) { return motion.Find(settings.Browser()) }
)

// MotionProvenance is how a scene's page came to be, for the clip's metadata.
type MotionProvenance struct {
	Kind       string // "motion_graphic" or "overlay"
	Browser    string // version of Chrome/Chromium
	Model      string // the model that wrote the page, empty when it was not written in this run
	Provider   string
	Generated  bool // written in this run
	PagePath   string
	PageSHA256 string
}

// motionRun holds what the scenes of one run share: the browser and the writer, found on first use.
type motionRun struct {
	mu sync.Mutex
	// parts is the catalog of parts (Options.MotionParts); nil when the program has none.
	parts   MotionParts
	browser motion.Browser
	berr    error
	bset    bool
	writer  motion.Writer
	info    motion.ModelInfo
	werr    error
	wset    bool
	// The map of the project's song (defaults.motion.song), read on first use.
	song    songmap.Map
	songKey string
	songErr error
	songSet bool
}

func (m *motionRun) Browser() (motion.Browser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.bset {
		m.browser, m.berr = findBrowser()
		m.bset = true
	}
	return m.browser, m.berr
}

func (m *motionRun) Info() motion.ModelInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.info
}

// Write makes motionRun the Writer handed to the generator: the model is looked up only when
// a page is actually missing.
func (m *motionRun) Write(ctx context.Context, system, user string, maxTokens int) (string, error) {
	m.mu.Lock()
	if !m.wset {
		m.writer, m.info, m.werr = newMotionWriter(ctx)
		m.wset = true
	}
	w, err := m.writer, m.werr
	m.mu.Unlock()
	if err != nil {
		return "", err
	}
	return w.Write(ctx, system, user, maxTokens)
}

// scenePages are the stored pages of a scene.
type scenePages struct {
	Scene   *MotionProvenance
	Overlay *MotionProvenance
}

// estimateSeconds is how long a scene is expected to last before its narration is
// synthesized: exact for a silent scene, about 2.5 words a second otherwise. Only the page's
// first draft is paced by it; the render tells the page the real duration.
func estimateSeconds(s config.Scene) float64 {
	if s.IsSilent() {
		return s.SilenceDuration()
	}
	d := float64(len(strings.Fields(s.TTSText))) / 2.5
	if d < 2 {
		d = 2
	}
	return d
}

// overlayWindow is the part of a scene an overlay covers, in seconds, given the scene's length.
func overlayWindow(o config.Overlay, length float64) (start, end float64) {
	start, end = o.ResolvedStart(), length
	if o.End != nil && *o.End < length {
		end = *o.End
	}
	return start, end
}

// MotionRequest is what the model is asked for the page of a scene (or of its overlay), with the
// scene's description, its narration, the project's style and size, and an estimate of the
// length the page is paced to. It errors when the scene has no such page or the resolution is invalid.
func MotionRequest(cfg *config.Config, scene config.Scene, overlay bool) (motion.Request, error) {
	width, height, err := render.ParseResolution(cfg.Defaults.Resolution)
	if err != nil {
		return motion.Request{}, err
	}
	r := motion.Request{Narration: scene.TTSText, Style: cfg.Defaults.SubjectPrompt, Width: width, Height: height}
	est := estimateSeconds(scene)
	switch {
	case overlay && scene.Overlay != nil:
		o := scene.Overlay
		start, end := overlayWindow(*o, est)
		if end <= start {
			end = start + 1
		}
		r.Description, r.Duration, r.Overlay, r.Position = o.Text, end-start, true, o.ResolvedPosition()
	case !overlay && scene.MotionGraphic != "" && (scene.MotionPart == "" || scene.MotionPart == "auto"):
		r.Description, r.Duration = scene.MotionGraphic, est
	default:
		return motion.Request{}, fmt.Errorf("scene %q has no %s", scene.ID, map[bool]string{false: "motion graphic", true: "overlay"}[overlay])
	}
	return r, nil
}

// ensureMotionPages makes sure the pages a scene needs are stored, asking the model for
// the ones that are not. It emits a "motion" phase around the model's work.
func (m *motionRun) ensureMotionPages(ctx context.Context, cfg *config.Config, scene config.Scene, base string, width, height int, emitter *events.Emitter) (scenePages, error) {
	var out scenePages
	if !scene.IsMotionGraphic() && scene.Overlay == nil {
		return out, nil
	}
	br, err := m.Browser()
	if err != nil {
		return out, err
	}
	one := func(overlay bool, r motion.Request) (*MotionProvenance, error) {
		path := motion.PagePath(base, scene.ID, overlay)
		_, statErr := os.Stat(path)
		missing := os.IsNotExist(statErr)
		if missing {
			emitter.PhaseStart(scene.ID, "motion")
		}
		html, generated, err := motion.Ensure(ctx, m, br.Path, base, scene.ID, r, motion.Limits{})
		if missing {
			if err != nil {
				return nil, err
			}
			emitter.PhaseEnd(scene.ID, "motion", filepath.Join(motion.Dir, filepath.Base(path)))
		} else if err != nil {
			return nil, err
		}
		kind := "motion_graphic"
		if overlay {
			kind = "overlay"
		}
		p := &MotionProvenance{Kind: kind, Browser: br.Version, Generated: generated, PagePath: filepath.Join(motion.Dir, filepath.Base(path)), PageSHA256: motion.SHA256(html)}
		meta, _ := motion.LoadMeta(base, scene.ID, overlay)
		meta.Kind, meta.Browser = kind, br.Version
		if generated {
			info := m.Info()
			p.Model, p.Provider = info.Model, info.Provider
			meta.Model, meta.Provider = info.Model, info.Provider
			meta.GeneratedSHA256, meta.GeneratedAt = p.PageSHA256, time.Now().UTC()
		}
		if err := motion.StoreMeta(base, scene.ID, overlay, meta); err != nil {
			emitter.Warn(scene.ID, fmt.Sprintf("cannot save the details of the page: %v", err))
		}
		return p, nil
	}
	if scene.MotionPart == "auto" {
		if chosen, ok := m.resolveAuto(ctx, cfg, scene, base, emitter); ok {
			scene = chosen
		}
	}
	if scene.MotionPart != "" && scene.MotionPart != "auto" {
		song, songStart, songLength := m.sliceFor(ctx, cfg, scene, base, emitter)
		p, err := ensurePartPage(m.parts, cfg, scene, base, width, height, br, song, songStart, songLength, emitter)
		if err != nil {
			return out, fmt.Errorf("the page of the motion graphic: %w", err)
		}
		out.Scene = p
	} else if scene.MotionGraphic != "" {
		r, _ := MotionRequest(cfg, scene, false)
		r.Width, r.Height = width, height
		p, err := one(false, r)
		if err != nil {
			return out, fmt.Errorf("the page of the motion graphic: %w", err)
		}
		out.Scene = p
	}
	if scene.Overlay != nil {
		r, _ := MotionRequest(cfg, scene, true)
		r.Width, r.Height = width, height
		p, err := one(true, r)
		if err != nil {
			return out, fmt.Errorf("the page of the overlay: %w", err)
		}
		out.Overlay = p
	}
	return out, nil
}

// pageSHA is the hash of a scene's stored page, or "none" when it has not been written.
func pageSHA(base, sceneID string, overlay bool) string {
	data, err := os.ReadFile(motion.PagePath(base, sceneID, overlay))
	if err != nil {
		return "none"
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// songFor is the song map of a scene's page or of its overlay (the page of a music piece reads the beat clock from it, and so does the
// page of a piece drawn over a video); nil for a scene that has no song map stored, whose pages get neither song nor beat clock.
func songFor(base, sceneID string, overlay bool) []byte {
	return motion.LoadSong(base, sceneID)
}

// songSHA is the hash of the song map stored for a scene, or "none".
func songSHA(base, sceneID string) string {
	data, err := os.ReadFile(motion.SongPath(base, sceneID))
	if err != nil {
		return "none"
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// MotionImageHash is the image hash of a motion-graphic scene: the content of its page, the
// size and frame rate it is drawn at and what its length depends on (the narration, or the
// silence), so that a longer narration draws it again.
func MotionImageHash(cfg *config.Config, scene config.Scene, base, audioHash string) string {
	length := audioHash
	if scene.IsSilent() {
		length = fmt.Sprintf("silence=%v", scene.SilenceDuration())
	}
	source := fmt.Sprintf("motion:%s:%s:fps=%d", pageSHA(base, scene.ID, false), length, cfg.Defaults.FPS) + songKey(base, scene.ID)
	if scene.Layer {
		source += ":layer" // a layer is drawn on transparent: it is another video from the same page
	}
	return cache.ImageHash(cache.ImageParams{
		Resolution:  cfg.Defaults.Resolution,
		SourceImage: source,
	})
}

// songKey is added to the image hash of a scene that has a song map, so a change of the map (a new tempo) draws it
// again; nothing is added for an ordinary scene, whose hash stays as it was.
func songKey(base, sceneID string) string {
	if h := songSHA(base, sceneID); h != "none" {
		return ":song=" + h
	}
	return ""
}

// OverlayKey identifies a scene's overlay for the clip hash; empty when it has none.
func OverlayKey(cfg *config.Config, scene config.Scene, base string) string {
	o := scene.Overlay
	if o == nil {
		return ""
	}
	end := "end"
	if o.End != nil {
		end = fmt.Sprintf("%.3f", *o.End)
	}
	return fmt.Sprintf("%s:%.3f-%s:%s:%s:fps=%d", pageSHA(base, scene.ID, true), o.ResolvedStart(), end, o.ResolvedPosition(), cfg.Defaults.Resolution, cfg.Defaults.FPS) + songKey(base, scene.ID)
}

// SceneClipHash is the clip hash of a scene, with its overlay folded in.
func SceneClipHash(cfg *config.Config, scene config.Scene, base string) string {
	p := SceneClipParams(cfg, scene, base)
	p.Overlay = OverlayKey(cfg, scene, base)
	return cache.ClipHash(p)
}

// MotionPath is where the video of a motion-graphic scene is written, relative to the project.
func MotionPath(sceneID string) string {
	return filepath.Join("tmp", fmt.Sprintf("scene_%s.motion.mp4", sceneID))
}

// OverlayVideoPath is where the video of a scene's overlay is written, relative to the project.
func OverlayVideoPath(sceneID, ext string) string {
	return filepath.Join("tmp", fmt.Sprintf("scene_%s.overlay.%s", sceneID, ext))
}

// renderPage plays a stored page to a video of the given length.
func (m *motionRun) renderPage(ctx context.Context, base, sceneID string, overlay bool, width, height, fps int, seconds float64, out string) error {
	br, err := m.Browser()
	if err != nil {
		return err
	}
	data, err := os.ReadFile(motion.PagePath(base, sceneID, overlay))
	if err != nil {
		return err
	}
	page := motion.Page{HTML: string(data), Width: width, Height: height, Duration: seconds, Transparent: overlay, Song: songFor(base, sceneID, overlay)}
	hyperframes.Extend(&page) // a page that embeds HyperFrames blocks gets their files and libraries
	_, problems, err := renderMotion(ctx, motion.Job{
		Page:    page,
		FPS:     fps,
		Browser: br.Path,
		Out:     joinDir(base, out),
	})
	if err != nil {
		return err
	}
	_ = problems // the page was checked when it was written; a later script error is shown by the next check
	return nil
}

// renderMotionScene plays the page of a motion-graphic scene to its video and writes the
// video's first frame as the scene picture.
func renderMotionScene(ctx context.Context, mo *motionRun, r runner.CommandRunner, base string, scene config.Scene, width, height, fps int, seconds float64, pngPath string) error {
	out := MotionPath(scene.ID)
	if err := mo.renderPage(ctx, base, scene.ID, false, width, height, fps, seconds, out); err != nil {
		return err
	}
	if _, err := r.Run(ctx, "ffmpeg", assemble.BuildPosterArgs(out, width, height, 0, pngPath)); err != nil {
		return fmt.Errorf("scene %q: cannot take the first frame of the motion graphic: %w", scene.ID, err)
	}
	return nil
}

// --- scenes made from a part of the catalog -------------------------------------------------------

// PartRequest is what the builder of a scene's page needs: the part and its parameters, the line of text, the tempo of
// the clock, the length and size of the scene, and the design (the project's, or none: the builder then chooses a
// palette from Style, the words of the project's subject prompt).
type PartRequest struct {
	Part     string               `json:"part"`
	Params   map[string]any       `json:"params,omitempty"`
	Text     string               `json:"text,omitempty"`
	BPM      float64              `json:"bpm"`
	Duration float64              `json:"duration"`
	Width    int                  `json:"width"`
	Height   int                  `json:"height"`
	Design   *config.MotionDesign `json:"design,omitempty"`
	Style    string               `json:"style,omitempty"`
	// Song, when the project has a song, is the map of the song (JSON) cut to the scene's span: the page's beat clock reads
	// it instead of the fixed tempo, and BPM is then 0.
	Song json.RawMessage `json:"song,omitempty"`
	// SongStart is where the scene starts in the song and SongLength how long the song is (both 0 without a song).
	SongStart  float64 `json:"song_start,omitempty"`
	SongLength float64 `json:"song_length,omitempty"`
}

// PartPage is the page the builder makes and the song map (JSON) its beat clock reads.
type PartPage struct {
	HTML string
	Song []byte
}

// MotionEngine is the music motion engine as a program holds it: the catalog the configuration checks the parts of a
// scene against, and what builds the pages of the scenes made from them.
type MotionEngine interface {
	config.PartCatalog
	MotionParts
}

// MotionParts is what a run needs of the catalog of parts: the page of a scene made from a part, and the choice of a part
// by the model. The music motion engine implements it (it imports this package, so this package cannot import it) and
// the program gives it in Options. With none, a scene with a part fails with an error that says so, and the choice
// falls back to the page written by the model.
type MotionParts interface {
	// Page builds the page and the beat clock of a scene made from a part.
	Page(PartRequest) (PartPage, error)
	// Choose asks the model to choose a part of the catalog and fill in its parameters for a scene: it validates the
	// answer and asks again with the reason, and returns an error when no answer is valid.
	Choose(ctx context.Context, w motion.Writer, r PartChoiceRequest) (PartChoice, error)
}

// PartChoiceRequest is what the model is told to choose the part of a scene made with motion_part: auto.
type PartChoiceRequest struct {
	Description string
	Narration   string
	Duration    float64
	// Concept is the visual idea of the project's design, when it has one.
	Concept string
}

// PartChoice is the part the model chose, its parameters and, optionally, the line of text to show.
type PartChoice struct {
	Part   string         `json:"part"`
	Params map[string]any `json:"params,omitempty"`
	Text   string         `json:"text,omitempty"`
}

func choicePath(base, sceneID string) string {
	return filepath.Join(base, motion.Dir, sceneID+".choice.json")
}

// storedChoice is motion/<scene>.choice.json: the part the model chose for a scene and what it was chosen from, so a later
// run does not ask again.
type storedChoice struct {
	Description string     `json:"description"`
	Choice      PartChoice `json:"choice"`
}

func descriptionKey(scene config.Scene) string {
	sum := sha256.Sum256([]byte(scene.MotionGraphic + "\x00" + scene.TTSText))
	return hex.EncodeToString(sum[:])
}

// resolveAuto turns a scene with motion_part: auto into a scene with a named part: from the choice kept for it, or by asking
// the model (once). It returns ok false when no part could be chosen (the reason is warned), so the scene goes the way a
// scene with only motion_graphic goes.
func (m *motionRun) resolveAuto(ctx context.Context, cfg *config.Config, scene config.Scene, base string, emitter *events.Emitter) (config.Scene, bool) {
	var kept storedChoice
	hadChoice := false
	if data, err := os.ReadFile(choicePath(base, scene.ID)); err == nil && json.Unmarshal(data, &kept) == nil && kept.Choice.Part != "" {
		hadChoice = true
		if kept.Description == descriptionKey(scene) {
			return withChoice(scene, kept.Choice), true
		}
	}
	if _, err := os.Stat(motion.PagePath(base, scene.ID, false)); err == nil && !hadChoice {
		return scene, false // a page was written for it before (by the model): it is used as it is, no new choice
	}
	if m.parts == nil {
		emitter.Warn(scene.ID, "the choice of a part is not available in this program: the page is written by the model")
		return scene, false
	}
	emitter.PhaseStart(scene.ID, "motion")
	req := PartChoiceRequest{Description: scene.MotionGraphic, Narration: scene.TTSText, Duration: estimateSeconds(scene)}
	if d := cfg.Defaults.Motion; d != nil && d.Design != nil {
		req.Concept = d.Design.Concept
	}
	choice, err := m.parts.Choose(ctx, m, req)
	emitter.PhaseEnd(scene.ID, "motion", filepath.Join(motion.Dir, scene.ID+".choice.json"))
	if err != nil {
		emitter.Warn(scene.ID, fmt.Sprintf("the model could not choose a part of the catalog (%v): the page is written by the model", err))
		return scene, false
	}
	if data, err := json.MarshalIndent(storedChoice{Description: descriptionKey(scene), Choice: choice}, "", "  "); err == nil {
		_ = os.MkdirAll(filepath.Join(base, motion.Dir), 0o755)
		_ = os.WriteFile(choicePath(base, scene.ID), append(data, '\n'), 0o644)
	}
	return withChoice(scene, choice), true
}

// withChoice is the scene as if the part had been written in it.
func withChoice(scene config.Scene, c PartChoice) config.Scene {
	scene.MotionPart, scene.MotionParams = c.Part, c.Params
	if scene.MotionText == "" {
		scene.MotionText = c.Text
	}
	return scene
}

// partRecord is motion/<scene>.part.json: what the stored page of a scene was built from, so that a change of any of it
// builds the page again and an edit of the page itself is kept until then.
type partRecord struct {
	Key        string `json:"key"`
	Part       string `json:"part"`
	PageSHA256 string `json:"page_sha256"`
}

func partRecordPath(base, sceneID string) string {
	return filepath.Join(base, motion.Dir, sceneID+".part.json")
}

// partRequestOf is the request that builds a scene's page. The tempo is the scene's, else the project's, else the default.
func partRequestOf(cfg *config.Config, scene config.Scene, width, height int) PartRequest {
	bpm := config.DefaultMotionBPM
	var design *config.MotionDesign
	if d := cfg.Defaults.Motion; d != nil {
		if d.BPM != nil {
			bpm = *d.BPM
		}
		design = d.Design
	}
	if scene.MotionBPM != nil {
		bpm = *scene.MotionBPM
	}
	return PartRequest{Part: scene.MotionPart, Params: scene.MotionParams, Text: scene.MotionText, BPM: bpm, Duration: estimateSeconds(scene),
		Width: width, Height: height, Design: design, Style: cfg.Defaults.SubjectPrompt}
}

// partKey identifies what a page is built from. The length only counts for a silent scene (its length is exact; a narrated
// scene's is only known after its narration, and the page is told the real one when it is drawn).
func partKey(r PartRequest, silent bool) string {
	if !silent {
		r.Duration = 0
	}
	data, _ := json.Marshal(r)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// ensurePartPage makes sure the page of a scene made from a part is stored: it is built (with no model) when there is none
// or when what it was built from has changed, and left alone otherwise, so an edit of the page by the user is kept.
func ensurePartPage(parts MotionParts, cfg *config.Config, scene config.Scene, base string, width, height int, br motion.Browser, song []byte, songStart, songLength float64, emitter *events.Emitter) (*MotionProvenance, error) {
	r := partRequestOf(cfg, scene, width, height)
	if len(song) > 0 {
		r.Song, r.BPM, r.SongStart, r.SongLength = song, 0, songStart, songLength // the beats come from the song
	}
	key := partKey(r, scene.IsSilent())
	path := motion.PagePath(base, scene.ID, false)
	var rec partRecord
	if data, err := os.ReadFile(partRecordPath(base, scene.ID)); err == nil {
		_ = json.Unmarshal(data, &rec)
	}
	_, statErr := os.Stat(path)
	built := false
	if os.IsNotExist(statErr) || rec.Key != key {
		if parts == nil {
			return nil, fmt.Errorf("scene %q is made from the part %q but this program has no catalog of parts linked in", scene.ID, scene.MotionPart)
		}
		emitter.PhaseStart(scene.ID, "motion")
		page, err := parts.Page(r)
		if err != nil {
			return nil, fmt.Errorf("scene %q, part %q: %w", scene.ID, scene.MotionPart, err)
		}
		if err := motion.Store(base, scene.ID, false, page.HTML); err != nil {
			return nil, err
		}
		if err := os.WriteFile(motion.SongPath(base, scene.ID), page.Song, 0o644); err != nil {
			return nil, err
		}
		rec = partRecord{Key: key, Part: scene.MotionPart, PageSHA256: motion.SHA256(page.HTML)}
		data, _ := json.MarshalIndent(rec, "", "  ")
		if err := os.WriteFile(partRecordPath(base, scene.ID), append(data, '\n'), 0o644); err != nil {
			return nil, err
		}
		emitter.PhaseEnd(scene.ID, "motion", filepath.Join(motion.Dir, filepath.Base(path)))
		built = true
	}
	html, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	p := &MotionProvenance{Kind: "motion_graphic", Browser: br.Version, Generated: built, PagePath: filepath.Join(motion.Dir, filepath.Base(path)), PageSHA256: motion.SHA256(string(html))}
	meta, _ := motion.LoadMeta(base, scene.ID, false)
	meta.Kind, meta.Browser = "motion_graphic", br.Version
	if built {
		p.Provider, p.Model = "catalog", scene.MotionPart
		meta.Provider, meta.Model = "catalog", scene.MotionPart
		meta.GeneratedSHA256, meta.GeneratedAt = p.PageSHA256, time.Now().UTC()
	}
	if err := motion.StoreMeta(base, scene.ID, false, meta); err != nil {
		emitter.Warn(scene.ID, fmt.Sprintf("cannot save the details of the page: %v", err))
	}
	return p, nil
}
