package musicmotion

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"veritmo/internal/platform/store"
)

// Drafts of parts (spec motion-part-drafts): a part the assistant wrote from a sentence and, optionally, a picture. A
// draft is checked like any part, has a preview and is only a draft until the user approves it.

// The states of a draft.
const (
	DraftWriting = "writing"
	DraftReady   = "ready"
	DraftFailed  = "failed"
)

// DraftPreviewFrames is how many frames of a beat the preview has.
const DraftPreviewFrames = 4

var (
	// ErrDraftNotFound is the draft that does not exist.
	ErrDraftNotFound = errors.New("draft not found")
	// ErrDraftNotReady is a draft that cannot be approved yet.
	ErrDraftNotReady = errors.New("the draft is not ready")
	// ErrNameTaken is a part of that name already in the catalog of the user's parts.
	ErrNameTaken = errors.New("a part with that name already is in the catalog of the user's parts")
	// ErrMinorPicture is what the describer returns for a picture with a person who is, or may be, a minor.
	ErrMinorPicture = errors.New("a person in the picture is, or looks, under 18, or the age could not be confirmed")
)

// Draft is draft.json (the files of the part are beside it).
type Draft struct {
	ID          string `json:"id"`
	State       string `json:"state"`
	Request     string `json:"request"`
	WithPicture bool   `json:"with_picture"`
	Source      string `json:"source,omitempty"`
	Description string `json:"description,omitempty"`
	// Replaces is the name of the part whose text the assistant is changing (a draft asked for with a request and a
	// manifest and fragment); that name may be kept although the catalog has it.
	Replaces string `json:"replaces,omitempty"`
	// Model is the model the person chose to write the part; empty when the routing's own was used. A retry keeps it.
	Model string `json:"model,omitempty"`
	// Provider is the saved provider the person chose for this draft (instead of the routing); a retry keeps it.
	Provider string   `json:"provider,omitempty"`
	Attempts int      `json:"attempts"`
	Name     string   `json:"name,omitempty"`
	Problems []string `json:"problems"`
	// Warnings are what the checks let pass but the person should know: a {{asset:name}} slot the theme has no drawing for.
	Warnings []string `json:"warnings,omitempty"`
	// Repairs is what the program repaired in the last answer of a scene before the checks (colours, fonts, Math.random()); empty for none.
	Repairs   []Repair  `json:"repairs,omitempty"`
	Error     string    `json:"error,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	// Preview lists the preview frames (file names); Manifest and Fragment are the text of the two files. They are
	// filled when a draft is read.
	Preview  []string `json:"preview"`
	Manifest string   `json:"manifest,omitempty"`
	Fragment string   `json:"fragment,omitempty"`
	// Origin is OriginLearned for a draft a learning proposed (LearningID names it); empty for one asked for by hand.
	Origin     string `json:"origin,omitempty"`
	LearningID string `json:"learning_id,omitempty"`
	// ReferenceID, VideoTitle and Video say which reference video inspired the draft (its title and its address). They are
	// kept in the draft itself so that the origin survives the reference being deleted.
	ReferenceID string `json:"reference_id,omitempty"`
	VideoTitle  string `json:"video_title,omitempty"`
	Video       string `json:"video,omitempty"`
	// Scene says the draft is a scene written for one script piece of one piece (see scene_draft.go) and Brief is the idea the
	// person asked for; the writer's input is built from the saved piece each time, so a retry sees the piece as it is now.
	Scene *SceneRef `json:"scene,omitempty"`
	Brief string    `json:"brief,omitempty"`
	// ReviewAsk is what the request asked of the review (nil: none was asked) and Review the grade of the version that was left
	// ready (see review.go); a retry keeps the ask.
	ReviewAsk *ReviewAsk `json:"review_ask,omitempty"`
	Review    *Review    `json:"review,omitempty"`
}

// OriginLearned marks what a learning from a reference made (spec motion-learning).
const OriginLearned = "learned"

var draftIDPattern = regexp.MustCompile(`^draft-[0-9]{8}-[0-9]{6}-[0-9a-f]{6}$`)

// ValidDraftID reports whether s looks like the id of a draft.
func ValidDraftID(s string) bool { return draftIDPattern.MatchString(s) }

// DraftStore keeps the drafts, a folder each.
type DraftStore struct {
	Dir string
	mu  sync.Mutex
	// db, when set, keeps the records and the text of the files in the database; Dir then holds only the pictures and the previews.
	db *store.Store
}

// NewDraftStore makes a store over dir.
func NewDraftStore(dir string) *DraftStore { return &DraftStore{Dir: dir} }

func (s *DraftStore) path(id string) string { return filepath.Join(s.Dir, id) }

// PreviewPath is the file of a preview frame; the name must be of the form 00.png.
func (s *DraftStore) PreviewPath(id, name string) string {
	return filepath.Join(s.path(id), "preview", name)
}

// Create makes a draft in the state writing; picture (JPEG or PNG), when not nil, is kept as its own copy.
func (s *DraftStore) Create(request, source string, picture []byte) (Draft, error) {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	d := Draft{ID: "draft-" + time.Now().UTC().Format("20060102-150405") + "-" + hex.EncodeToString(b), State: DraftWriting, Request: request,
		WithPicture: picture != nil, Source: source, Problems: []string{}, Preview: []string{}, CreatedAt: time.Now().UTC()}
	if err := os.MkdirAll(filepath.Join(s.path(d.ID), "preview"), 0o755); err != nil {
		return Draft{}, err
	}
	if picture != nil {
		if err := os.WriteFile(filepath.Join(s.path(d.ID), "frame.jpg"), picture, 0o644); err != nil {
			_ = os.RemoveAll(s.path(d.ID))
			return Draft{}, err
		}
	}
	var err error
	if s.db != nil {
		err = s.dbCreate(d)
	} else {
		err = s.Save(d)
	}
	if err != nil {
		_ = os.RemoveAll(s.path(d.ID))
		return Draft{}, err
	}
	return d, nil
}

// Save writes draft.json of a draft whose folder exists (a deleted draft is not brought back).
func (s *DraftStore) Save(d Draft) error {
	if s.db != nil {
		return s.dbSave(d)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := os.Stat(s.path(d.ID)); err != nil {
		return ErrDraftNotFound
	}
	d.Manifest, d.Fragment = "", ""
	if d.Problems == nil {
		d.Problems = []string{}
	}
	if d.Preview == nil {
		d.Preview = []string{}
	}
	raw, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(s.path(d.ID), "draft.json.tmp")
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(s.path(d.ID), "draft.json"))
}

// SaveFiles writes the two files of the part of a draft.
func (s *DraftStore) SaveFiles(id, manifest, fragment string) error {
	if s.db != nil {
		return s.dbSetFiles(id, map[string]any{"manifest_json": store.Text(manifest), "fragment_html": store.Text(fragment)})
	}
	if _, err := os.Stat(s.path(id)); err != nil {
		return ErrDraftNotFound
	}
	if err := os.WriteFile(filepath.Join(s.path(id), "manifest.json"), []byte(manifest), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.path(id), "part.html"), []byte(fragment), 0o644)
}

// SavePreview replaces the preview frames of a draft and returns their names.
func (s *DraftStore) SavePreview(id string, frames [][]byte) ([]string, error) {
	dir := filepath.Join(s.path(id), "preview")
	if s.db != nil {
		if !s.dbExists(id) {
			return nil, ErrDraftNotFound
		}
	} else if _, err := os.Stat(s.path(id)); err != nil {
		return nil, ErrDraftNotFound
	}
	_ = os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	names := []string{}
	for i, f := range frames {
		name := fmt.Sprintf("%02d.png", i)
		if err := os.WriteFile(filepath.Join(dir, name), f, 0o644); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, nil
}

// Get reads a draft with the text of its files.
func (s *DraftStore) Get(id string) (Draft, error) {
	if !ValidDraftID(id) {
		return Draft{}, ErrDraftNotFound
	}
	if s.db != nil {
		return s.dbGet(id)
	}
	raw, err := os.ReadFile(filepath.Join(s.path(id), "draft.json"))
	if err != nil {
		return Draft{}, ErrDraftNotFound
	}
	var d Draft
	if json.Unmarshal(raw, &d) != nil {
		return Draft{}, ErrDraftNotFound
	}
	if m, err := os.ReadFile(filepath.Join(s.path(id), "manifest.json")); err == nil {
		d.Manifest = string(m)
	}
	if f, err := os.ReadFile(filepath.Join(s.path(id), "part.html")); err == nil {
		d.Fragment = string(f)
	}
	if d.Problems == nil {
		d.Problems = []string{}
	}
	if d.Preview == nil {
		d.Preview = []string{}
	}
	return d, nil
}

// List returns the drafts, newest first.
func (s *DraftStore) List() []Draft {
	if s.db != nil {
		return s.dbList()
	}
	entries, _ := os.ReadDir(s.Dir)
	out := []Draft{}
	for _, e := range entries {
		if d, err := s.Get(e.Name()); err == nil {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID > out[j].ID
	})
	return out
}

// Delete removes a draft with its files.
func (s *DraftStore) Delete(id string) error {
	if _, err := s.Get(id); err != nil {
		return err
	}
	if s.db != nil {
		if err := s.DeleteRecord(id); err != nil {
			return err
		}
	}
	return os.RemoveAll(s.path(id))
}

// MarkInterrupted sets to failed the drafts that were being written when the server stopped.
func (s *DraftStore) MarkInterrupted() int {
	n := 0
	for _, d := range s.List() {
		if d.State == DraftWriting {
			d.State, d.Error = DraftFailed, "the work was interrupted when the server stopped"
			if s.Save(d) == nil {
				n++
			}
		}
	}
	return n
}

// DraftEngine does the work of a draft: it describes the picture, asks the assistant, checks, retries and draws the
// preview. Every dependency is a seam, so the tests give fakes.
type DraftEngine struct {
	Store *DraftStore
	Cat   Catalog
	// Browser is the path of the browser that checks and draws the parts.
	Browser string
	// Writer is the assistant; nil means there is none.
	Writer Writer
	// Describe describes a picture after the age check; it returns ErrMinorPicture for a minor. Nil means there is no
	// vision model.
	Describe func(ctx context.Context, picture []byte) (string, error)
	// SceneInput builds what a scene is written from out of the saved piece; nil means there are no pieces to write for.
	SceneInput func(ref SceneRef, brief string) (SceneDraftInput, error)
	// Vision is the model that reviews the previews of a draft that asked for a review; nil means there is none (the draft is
	// then ready with a review that says so).
	Vision Vision
}

// sceneName is the name of the next scene part of a piece: scene-<short id of the piece>-<n>, with n the first free number
// (the names of a part are limited to 32 characters, so the piece's id is shortened to its last characters; the piece
// itself is in the part's manifest, scene.piece).
func (e *DraftEngine) sceneName(piece string) string {
	short := piece
	if i := strings.LastIndex(piece, "-"); i >= 0 && len(piece)-i-1 >= 4 {
		short = piece[i+1:]
	}
	short = sceneNameClean.ReplaceAllString(strings.ToLower(short), "")
	if len(short) > 10 {
		short = short[len(short)-10:]
	}
	taken := map[string]bool{}
	for _, p := range e.Cat.ListAll() {
		taken[p.Name] = true
	}
	for _, d := range e.Store.List() {
		taken[d.Name] = true
	}
	for n := 1; ; n++ {
		if name := fmt.Sprintf("scene-%s-%d", short, n); !taken[name] {
			return name
		}
	}
}

var sceneNameClean = regexp.MustCompile(`[^a-z0-9]`)

// sceneManifest gives the manifest of a scene draft its name and its scene: the program decides both, not the writer.
func sceneManifest(d Draft, manifest []byte) ([]byte, error) {
	var m map[string]any
	if err := json.Unmarshal(manifest, &m); err != nil {
		return nil, fmt.Errorf("manifest.json is not valid JSON: %w", err)
	}
	m["name"] = d.Name
	m["scene"] = map[string]string{"piece": d.Scene.Piece, "script_piece": d.Scene.ScriptPiece}
	return json.MarshalIndent(m, "", "  ")
}

// sceneOptions are the options a scene draft is checked and previewed with: the real scene of its script piece.
func (e *DraftEngine) sceneOptions(d Draft) (CheckOptions, error) {
	if d.Scene == nil {
		return CheckOptions{}, nil
	}
	if e.SceneInput == nil {
		return CheckOptions{}, errors.New("there is no piece to check a scene in")
	}
	in, err := e.SceneInput(*d.Scene, d.Brief)
	if err != nil {
		return CheckOptions{}, err
	}
	return CheckOptions{Scene: &in.Scene, SceneRules: true, Allow: in.Brief + " " + in.Title}, nil
}

func (e *DraftEngine) fail(d Draft, msg string) {
	d.State, d.Error = DraftFailed, msg
	_ = e.Store.Save(d)
}

// Run does the work of a draft that is in the state writing: it is the whole job, to be run in the background. A
// cancelled context writes nothing more (the draft was deleted).
func (e *DraftEngine) Run(ctx context.Context, id string) {
	d, err := e.Store.Get(id)
	if err != nil {
		return
	}
	d.State, d.Error, d.Problems = DraftWriting, "", []string{}
	if d.WithPicture && d.Description == "" {
		pic, err := os.ReadFile(filepath.Join(e.Store.path(id), "frame.jpg"))
		if err != nil {
			e.fail(d, "the picture of the draft is not there any more")
			return
		}
		if e.Describe == nil {
			e.fail(d, "there is no vision model to describe the picture")
			return
		}
		text, err := e.Describe(ctx, pic)
		if ctx.Err() != nil {
			return
		}
		switch {
		case errors.Is(err, ErrMinorPicture):
			_ = os.Remove(filepath.Join(e.Store.path(id), "frame.jpg"))
			e.fail(d, "no part was written: "+err.Error()+"; the picture was deleted")
			return
		case err != nil:
			e.fail(d, "the picture could not be described: "+err.Error())
			return
		}
		d.Description = text
		_ = e.Store.Save(d)
	}
	if e.Writer == nil {
		e.fail(d, "there is no assistant to write the part")
		return
	}
	in := DraftInput{Request: d.Request, Description: d.Description, Cat: e.Cat}
	system := DraftSystem()
	var sin *SceneDraftInput
	var opts CheckOptions
	if d.Scene != nil {
		if e.SceneInput == nil {
			e.fail(d, "there is no piece to write a scene for")
			return
		}
		v, err := e.SceneInput(*d.Scene, d.Brief)
		if err != nil {
			e.fail(d, "the scene cannot be written: "+err.Error())
			return
		}
		sin, opts, system = &v, CheckOptions{Scene: &v.Scene, SceneRules: true, Allow: v.Brief + " " + v.Title}, SceneSystem()
		if d.Name == "" {
			d.Name = e.sceneName(d.Scene.Piece)
			_ = e.Store.Save(d)
		}
	}
	if d.Source == RewriteSource {
		in.BaseManifest, in.BaseFragment, _ = e.Store.LoadBase(id)
		in.BaseName = d.Replaces
	}
	problem := ""
	var server []string // what is the server's own fault on the page (CheckOptions.Server): shown on the ready draft, never refused
	opts.Server = &server
	// With a review, a draft that passes the checks is graded; one under the minimum is written again with the notes, up to
	// the rewrites asked for, and the best version is the one left ready.
	var (
		best    *candidate
		history []ReviewAttempt
		rounds  int
	)
	finish := func(c *candidate) {
		if err := e.Store.SaveFiles(id, string(c.manifest), c.fragment); err != nil {
			return
		}
		names, err := e.Store.SavePreview(id, c.frames)
		if err != nil {
			return
		}
		rev := c.review
		rev.Attempts = history
		d.State, d.Name, d.Problems, d.Preview, d.Error = DraftReady, c.part.Name, append([]string{}, server...), names, ""
		d.Warnings, d.Review = draftWarnings(c.part, opts), &rev
		_ = e.Store.Save(d)
	}
	for checks := 0; checks < Attempts; {
		d.Attempts++
		user := DraftUser(in, problem)
		if sin != nil {
			user = SceneUser(*sin, e.Cat, problem)
		}
		answer, err := e.Writer.Write(ctx, system, user, 6000)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			e.fail(d, "the assistant failed: "+err.Error())
			return
		}
		var (
			manifest []byte
			fragment string
			part     Part
			probs    []string
		)
		if sin != nil {
			manifest, fragment, part, probs = parseSceneAnswer(answer, d)
			d.Repairs = nil
			if len(probs) == 0 && (len(sceneStyleProblems(fragment)) > 0 || reMathRandom.MatchString(fragment) || reVarTypo.MatchString(fragment) || reStyleTypo.MatchString(fragment)) {
				// what the checks would refuse and a program can fix is fixed here, and checked like any answer
				if fixed, reps := RepairScene(fragment, sin.Scene.Design); len(reps) > 0 {
					if p2, probs2 := ParsePart(manifest, fixed); len(probs2) == 0 {
						fragment, part, d.Repairs = fixed, p2, reps
					}
				}
			}
		} else {
			manifest, fragment, part, probs = parseDraftAnswer(answer, e.Cat, d.Replaces)
		}
		if manifest != nil {
			_ = e.Store.SaveFiles(id, string(manifest), fragment)
		}
		if len(probs) == 0 {
			var cerr error
			probs, cerr = CheckPart(ctx, e.Browser, part, opts)
			if ctx.Err() != nil {
				return
			}
			if cerr != nil {
				e.fail(d, "the part could not be checked: "+cerr.Error())
				return
			}
			if sin != nil && len(probs) > 0 {
				// what only the drawn page shows (a word under the floor, texts that overlap or leave the frame) is repaired by a script
				// at the end of the fragment; the page is drawn and checked again, and kept only if it passes
				if f2, p2, reps, ok := e.repairLayout(ctx, sin.Scene, manifest, fragment, probs, opts); ok {
					fragment, part, probs = f2, p2, nil
					d.Repairs = append(d.Repairs, reps...)
					_ = e.Store.SaveFiles(id, string(manifest), fragment)
				} else if ctx.Err() != nil {
					return
				}
			}
		}
		if len(probs) == 0 {
			frames, perr := PreviewFramesIn(ctx, e.Browser, part, DraftPreviewFrames, opts)
			if ctx.Err() != nil {
				return
			}
			if perr != nil {
				e.fail(d, "the preview could not be drawn: "+perr.Error())
				return
			}
			if d.ReviewAsk != nil {
				var lines []Line
				if opts.Scene != nil {
					lines = opts.Scene.Lines
				}
				world := ""
				if sin != nil {
					world = sin.World
				}
				rev, rerr := e.reviewFrames(ctx, frames, lines, d.Brief, world)
				if rerr != nil {
					return
				}
				c := &candidate{manifest: manifest, fragment: fragment, part: part, frames: frames, review: rev}
				if rev.Error == "" {
					history = append(history, rev.attempt())
				}
				if best == nil || rev.Score > best.review.Score {
					best = c
				}
				if rev.Error == "" && rev.Score < d.ReviewAsk.Min && rounds < d.ReviewAsk.Rewrites() {
					rounds++
					problem = ProblemFromReview(rev)
					d.Problems = []string{}
					_ = e.Store.Save(d)
					continue // a rewrite from the review does not count as an attempt that failed the checks
				}
				finish(best)
				return
			}
			names, err := e.Store.SavePreview(id, frames)
			if err != nil {
				return
			}
			d.State, d.Name, d.Problems, d.Preview, d.Error = DraftReady, part.Name, append([]string{}, server...), names, ""
			d.Warnings = draftWarnings(part, opts)
			_ = e.Store.Save(d)
			return
		}
		checks++
		d.Problems = probs
		_ = e.Store.Save(d)
		problem = strings.Join(probs, "; ")
	}
	if best != nil { // the rewrites from the review failed the checks: the best version that was graded stays
		finish(best)
		return
	}
	d.State, d.Error = DraftFailed, fmt.Sprintf("no answer passed the checks in %d attempts", Attempts)
	_ = e.Store.Save(d)
}

// candidate is a version of a draft that passed the checks and was graded.
type candidate struct {
	manifest []byte
	fragment string
	part     Part
	frames   [][]byte
	review   Review
}

// reviewFrames grades the frames with the engine's vision model (a Review that says so when there is none).
func (e *DraftEngine) reviewFrames(ctx context.Context, frames [][]byte, lines []Line, brief, world string) (Review, error) {
	if e.Vision == nil {
		return Review{Error: "there is no vision model to review the drafts"}, nil
	}
	return ReviewFrames(ctx, e.Vision, frames, lines, brief, world)
}

// draftWarnings are the {{asset:name}} slots of the part that the theme of the scene it is checked in has no drawing for (the
// standard check scene has no theme, so every slot is then reported: the part is meant for a piece whose theme has it).
func draftWarnings(p Part, opts CheckOptions) []string {
	var theme *Theme
	if opts.Scene != nil {
		theme = opts.Scene.Design.Theme
	}
	var out []string
	for _, name := range UnknownAssets(p.Fragment, theme) {
		out = append(out, fmt.Sprintf("unknown asset %s: the theme of the design has no drawing with that name, so the slot is empty", name))
	}
	return out
}

// parseSceneAnswer reads the answer of the scene writer: as a part's, with the name and the scene the program decides.
func parseSceneAnswer(answer string, d Draft) (manifest []byte, fragment string, part Part, problems []string) {
	j := reJSONBlock.FindStringSubmatch(answer)
	h := reHTMLBlock.FindStringSubmatch(answer)
	if j == nil {
		problems = append(problems, "the answer has no ```json block with manifest.json")
	}
	if h == nil {
		problems = append(problems, "the answer has no ```html block with part.html")
	}
	if len(problems) > 0 {
		return nil, "", Part{}, problems
	}
	raw, err := sceneManifest(d, []byte(strings.TrimSpace(j[1])))
	if err != nil {
		return nil, "", Part{}, []string{err.Error()}
	}
	fragment = strings.TrimSpace(h[1])
	part, problems = ParsePart(raw, fragment)
	return raw, fragment, part, problems
}

// Edit replaces the manifest and the fragment of a draft after checking them like a draft. The problems are
// returned and nothing is changed when they do not pass; when they do, the files are saved, the preview is drawn
// again and the draft is ready.
func (e *DraftEngine) Edit(ctx context.Context, id, manifest, fragment string) (Draft, []string, error) {
	d, err := e.Store.Get(id)
	if err != nil {
		return Draft{}, nil, err
	}
	opts, err := e.sceneOptions(d)
	if err != nil {
		return Draft{}, nil, err
	}
	if d.Scene != nil { // the program decides the name and the scene of a scene
		raw, merr := sceneManifest(d, []byte(manifest))
		if merr != nil {
			return d, []string{merr.Error()}, nil
		}
		manifest = string(raw)
	}
	part, probs := ParsePart([]byte(manifest), fragment)
	if len(probs) == 0 && d.Scene == nil {
		if _, taken := e.Cat.Get(part.Name); taken && part.Name != d.Replaces {
			probs = []string{fmt.Sprintf("the name %q is already a part of the catalog: choose another name", part.Name)}
		}
	}
	if len(probs) == 0 {
		if probs, err = CheckPart(ctx, e.Browser, part, opts); err != nil {
			return Draft{}, nil, err
		}
	}
	if len(probs) > 0 {
		return d, probs, nil
	}
	frames, err := PreviewFramesIn(ctx, e.Browser, part, DraftPreviewFrames, opts)
	if err != nil {
		return Draft{}, nil, err
	}
	if err := e.Store.SaveFiles(id, manifest, fragment); err != nil {
		return Draft{}, nil, err
	}
	if d.Preview, err = e.Store.SavePreview(id, frames); err != nil {
		return Draft{}, nil, err
	}
	d.State, d.Name, d.Problems, d.Error = DraftReady, part.Name, []string{}, ""
	d.Warnings = draftWarnings(part, opts)
	if err := e.Store.Save(d); err != nil {
		return Draft{}, nil, err
	}
	got, err := e.Store.Get(id)
	return got, nil, err
}

// PreparedApproval is a ready draft that passed the checks of an approval and is waiting to enter the catalog.
type PreparedApproval struct {
	Draft  Draft
	Part   Part
	Origin *PartOrigin
}

// PrepareApproval checks a ready draft again, with everything an approval checks (rules, page, motion, a name that is not taken), and changes
// nothing: it is the slow part of an approval (it draws the page in a browser), so it is done before anything is written. The problems come
// back when it does not pass.
func (e *DraftEngine) PrepareApproval(ctx context.Context, id string) (*PreparedApproval, []string, error) {
	d, err := e.Store.Get(id)
	if err != nil {
		return nil, nil, err
	}
	if d.State != DraftReady {
		return nil, nil, ErrDraftNotReady
	}
	if e.Cat.userStore() == nil {
		return nil, nil, errors.New("there is nowhere to keep the user's parts")
	}
	opts, err := e.sceneOptions(d)
	if err != nil {
		return nil, []string{"the scene cannot be approved: " + err.Error()}, nil
	}
	part, probs := ParsePart([]byte(d.Manifest), d.Fragment)
	if len(probs) == 0 {
		if taken, ok := e.Cat.Get(part.Name); ok {
			if taken.Source == "user" {
				return nil, nil, ErrNameTaken
			}
			probs = []string{fmt.Sprintf("the name %q is already a part of the catalog: edit the draft and choose another name", part.Name)}
		}
	}
	if len(probs) == 0 {
		if probs, err = CheckPart(ctx, e.Browser, part, opts); err != nil {
			return nil, nil, err
		}
	}
	if len(probs) > 0 {
		return nil, probs, nil
	}
	return &PreparedApproval{Draft: d, Part: part, Origin: OriginOfDraft(d)}, nil, nil
}

// CommitApproval keeps a prepared approval's part in the catalog cat (ErrNameTaken when it has a part of that name). It is quick, so a caller
// can do it inside a transaction together with other writes; it does not touch the draft.
func (e *DraftEngine) CommitApproval(cat Catalog, p *PreparedApproval) error {
	if err := cat.CreatePart([]byte(p.Draft.Manifest), p.Draft.Fragment, p.Origin); err != nil {
		if errors.Is(err, ErrPartExists) {
			return ErrNameTaken
		}
		return err
	}
	return nil
}

// Approve checks a ready draft again, keeps its part in the catalog, where it is listed, and deletes the draft. Nothing else puts a draft in
// the catalog.
func (e *DraftEngine) Approve(ctx context.Context, id string) (Part, []string, error) {
	p, probs, err := e.PrepareApproval(ctx, id)
	if err != nil || len(probs) > 0 {
		return Part{}, probs, err
	}
	if _, inDB := e.Cat.Parts.(dbParts); inDB && e.Store.db != nil {
		// the part and the end of the draft are one change: if the part cannot be kept the draft stays ready, and the other way round
		err = e.Store.db.Tx(func(tx *store.Store) error {
			if err := e.CommitApproval(e.Cat.WithParts(NewDBParts(tx)), p); err != nil {
				return err
			}
			return e.Store.On(tx).DeleteRecord(id)
		})
		if err != nil {
			return Part{}, nil, err
		}
		e.Store.RemoveMedia(id)
	} else {
		if err := e.CommitApproval(e.Cat, p); err != nil {
			return Part{}, nil, err
		}
		_ = e.Store.Delete(id)
	}
	got, _ := e.Cat.Get(p.Part.Name)
	return got, nil, nil
}

// Restart prepares a failed draft for another attempt: it goes back to writing with no attempts counted and no
// error. The caller then runs it. A draft that is being written or is ready is refused.
func (e *DraftEngine) Restart(id string) error {
	d, err := e.Store.Get(id)
	if err != nil {
		return err
	}
	if d.State != DraftFailed {
		return fmt.Errorf("only a failed draft can be tried again (it is %s)", d.State)
	}
	if d.WithPicture && d.Description == "" {
		if _, err := os.Stat(filepath.Join(e.Store.path(id), "frame.jpg")); err != nil {
			return errors.New("the picture of the draft was deleted, so it cannot be tried again: ask for a new draft")
		}
	}
	d.State, d.Error, d.Attempts, d.Problems, d.Review = DraftWriting, "", 0, []string{}, nil
	return e.Store.Save(d)
}

// HandWritten is the request of a draft whose files were written by a person, not by the assistant.
const HandWritten = "(written by hand)"

// CreateFromFiles makes a draft out of a manifest and a fragment somebody wrote, with no assistant: they are checked
// like an edit is (rules, page, motion, a name that is not in the catalog) and, if they pass, the preview is drawn and
// the draft is ready. If they do not pass the draft is made anyway, failed, with the problems and the two files, so that
// it can be edited.
func (e *DraftEngine) CreateFromFiles(ctx context.Context, manifest, fragment string) (Draft, error) {
	d, err := e.Store.Create(HandWritten, "manual", nil)
	if err != nil {
		return Draft{}, err
	}
	if err := e.Store.SaveFiles(d.ID, manifest, fragment); err != nil {
		_ = e.Store.Delete(d.ID)
		return Draft{}, err
	}
	part, probs := ParsePart([]byte(manifest), fragment)
	if len(probs) == 0 {
		if _, taken := e.Cat.Get(part.Name); taken {
			probs = []string{fmt.Sprintf("the name %q is already a part of the catalog: choose another name", part.Name)}
		}
	}
	if len(probs) == 0 {
		if probs, err = CheckPart(ctx, e.Browser, part, CheckOptions{}); err != nil {
			_ = e.Store.Delete(d.ID)
			return Draft{}, err
		}
	}
	if len(probs) > 0 {
		d.State, d.Problems, d.Error = DraftFailed, probs, "the part does not pass the checks: "+strings.Join(probs, "; ")
		if err := e.Store.Save(d); err != nil {
			return Draft{}, err
		}
		return e.Store.Get(d.ID)
	}
	frames, err := PreviewFrames(ctx, e.Browser, part, DraftPreviewFrames)
	if err != nil {
		_ = e.Store.Delete(d.ID)
		return Draft{}, err
	}
	if d.Preview, err = e.Store.SavePreview(d.ID, frames); err != nil {
		return Draft{}, err
	}
	d.State, d.Name, d.Problems = DraftReady, part.Name, []string{}
	if err := e.Store.Save(d); err != nil {
		return Draft{}, err
	}
	return e.Store.Get(d.ID)
}

// RewriteSource is the source of a draft the assistant writes from the text of a part and an instruction.
const RewriteSource = "rewrite"

// SaveBase keeps the part a rewrite starts from, apart from the files of the draft (which the attempts replace).
func (s *DraftStore) SaveBase(id, manifest, fragment string) error {
	if s.db != nil {
		return s.dbSetFiles(id, map[string]any{"base_manifest_json": store.Text(manifest), "base_fragment_html": store.Text(fragment)})
	}
	if _, err := os.Stat(s.path(id)); err != nil {
		return ErrDraftNotFound
	}
	if err := os.WriteFile(filepath.Join(s.path(id), "base-manifest.json"), []byte(manifest), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.path(id), "base-part.html"), []byte(fragment), 0o644)
}

// LoadBase reads what SaveBase kept.
func (s *DraftStore) LoadBase(id string) (manifest, fragment string, err error) {
	if s.db != nil {
		return s.dbLoadBase(id)
	}
	m, err := os.ReadFile(filepath.Join(s.path(id), "base-manifest.json"))
	if err != nil {
		return "", "", err
	}
	f, err := os.ReadFile(filepath.Join(s.path(id), "base-part.html"))
	return string(m), string(f), err
}
