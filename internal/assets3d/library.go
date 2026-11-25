package assets3d

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"veritmo/internal/video/blender"
)

// DirEnv names the environment variable that moves the folder of the library.
const DirEnv = "MODELS3D_DIR"

// DefaultDir is the folder of the library inside the server's tree (it is not committed).
const DefaultDir = "models3d"

// ResolveDir gives the folder the library lives in: $MODELS3D_DIR, else models3d/ in the working directory.
func ResolveDir(getenv func(string) string) string {
	if v := strings.TrimSpace(getenv(DirEnv)); v != "" {
		return v
	}
	return DefaultDir
}

// The states of a pack.
const (
	StateAvailable  = "available"
	StateInstalling = "installing"
	StateInstalled  = "installed"
	StateFailed     = "failed"
)

// Errors the callers tell apart.
var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("conflict")
	ErrInvalid  = errors.New("invalid")
)

// Model is one model of an installed pack as it is indexed.
type Model struct {
	Name string `json:"name"`
	// File is the path of the .glb under the pack's files folder, with forward slashes.
	File      string   `json:"file"`
	Clips     []string `json:"clips"`
	Triangles int      `json:"triangles"`
	Needs     []string `json:"needs"`
	Tags      []string `json:"tags"`
	// Preview is the picture the pack itself has for the model, relative to the files folder ("" when it has none).
	Preview string `json:"preview,omitempty"`
	// Generated says how a model of the pack "generated" was made (nil for every other model).
	Generated *GeneratedInfo `json:"generated,omitempty"`
}

// GeneratedInfo is what the library keeps of a model that an engine made: the prompt (or the name of the picture), the engine and its version
// and licence, and when.
type GeneratedInfo struct {
	Prompt        string    `json:"prompt,omitempty"`
	Picture       string    `json:"picture,omitempty"`
	Engine        string    `json:"engine"`
	EngineVersion string    `json:"engine_version,omitempty"`
	Detail        string    `json:"detail,omitempty"`
	Licence       string    `json:"licence"`
	Attribution   bool      `json:"attribution"`
	GeneratedAt   time.Time `json:"generated_at"`
	// Project and Entry say which project made the model and which of its models (the name the entry gave it, with -<n> for a variant after the
	// first); empty for a model made by the one-off command. Adult marks what the person asked for as for people of 18 or older.
	Project string `json:"project,omitempty"`
	Entry   string `json:"entry,omitempty"`
	Adult   bool   `json:"adult,omitempty"`
}

// Skipped is a file of a pack that was left out of the index and why.
type Skipped struct {
	File   string `json:"file"`
	Reason string `json:"reason"`
}

// Record is what the library keeps of an installed pack (pack.json).
type Record struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Author      string    `json:"author"`
	Licence     string    `json:"licence"`
	Attribution bool      `json:"attribution"`
	Source      string    `json:"source"` // catalog or user
	Page        string    `json:"page,omitempty"`
	Version     string    `json:"version,omitempty"`
	SHA256      string    `json:"sha256,omitempty"`
	SizeBytes   int64     `json:"size_bytes"`
	Description string    `json:"description,omitempty"`
	Tags        []string  `json:"tags,omitempty"`
	InstalledAt time.Time `json:"installed_at"`
	Models      []Model   `json:"models"`
	Skipped     []Skipped `json:"skipped,omitempty"`
}

// Progress is how much of the zip of an install is down.
type Progress struct {
	Done  int64 `json:"done"`
	Total int64 `json:"total"`
}

// PackView is a pack as the API shows it.
type PackView struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Author      string    `json:"author"`
	Licence     string    `json:"licence"`
	Attribution bool      `json:"attribution"`
	Source      string    `json:"source"`
	Page        string    `json:"page,omitempty"`
	Version     string    `json:"version,omitempty"`
	SizeBytes   int64     `json:"size_bytes"`
	Description string    `json:"description,omitempty"`
	Tags        []string  `json:"tags"`
	State       string    `json:"state"`
	Error       string    `json:"error,omitempty"`
	Progress    *Progress `json:"progress,omitempty"`
	Models      int       `json:"models"`
	Skipped     []Skipped `json:"skipped,omitempty"`
}

// ModelView is a model of the catalog as the API shows it.
type ModelView struct {
	ID          string   `json:"id"`
	Pack        string   `json:"pack"`
	Name        string   `json:"name"`
	Tags        []string `json:"tags"`
	Clips       []string `json:"clips"`
	Triangles   int      `json:"triangles"`
	Needs       []string `json:"needs"`
	Licence     string   `json:"licence"`
	Author      string   `json:"author"`
	Attribution bool     `json:"attribution"`
	// What made a generated model (empty for the others).
	Prompt        string     `json:"prompt,omitempty"`
	Engine        string     `json:"engine,omitempty"`
	EngineVersion string     `json:"engine_version,omitempty"`
	GeneratedAt   *time.Time `json:"generated_at,omitempty"`
	// The project and the entry that made it, and whether it is marked as adult (generated models only).
	Project string `json:"project,omitempty"`
	Entry   string `json:"entry,omitempty"`
	Adult   bool   `json:"adult,omitempty"`
	// Source is where the model comes from: catalog (a pack of the catalog), user (one that was imported) or generated (made by an engine here).
	Source string `json:"source"`
}

// Library is the folder of packs and the jobs that fill it.
type Library struct {
	dir    string
	defs   []PackDef
	client *http.Client

	mu   sync.Mutex
	jobs map[string]*job
	sem  chan struct{}

	// Preview draws a small frame of a model with Blender (see thumbnails.go); nil: no thumbnails but the packs' own.
	Preview func(ctx context.Context, sources string, description []byte, width int) ([]byte, error)
}

// New makes the library of the folder dir for the packs of defs. It removes what a stopped install left behind.
func New(dir string, defs []PackDef) *Library {
	l := &Library{dir: dir, defs: defs, client: &http.Client{Timeout: 10 * time.Minute}, jobs: map[string]*job{}, sem: make(chan struct{}, 2)}
	l.sweep()
	return l
}

// SetClient changes the HTTP client of the downloads (the tests give theirs).
func (l *Library) SetClient(c *http.Client) { l.client = c }

// Dir is the folder of the library.
func (l *Library) Dir() string { return l.dir }

func (l *Library) packDir(id string) string { return filepath.Join(l.dir, id) }

// FilesDir is the folder where the files of an installed pack are, as the pack had them.
func (l *Library) FilesDir(id string) string { return filepath.Join(l.packDir(id), "files") }

// sweep removes the temporary folders of an install that was stopped.
func (l *Library) sweep() {
	entries, _ := os.ReadDir(l.dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".partial") || strings.HasPrefix(e.Name(), ".tmp-") {
			_ = os.RemoveAll(filepath.Join(l.dir, e.Name()))
		}
	}
}

func (l *Library) def(id string) (PackDef, bool) {
	for _, d := range l.defs {
		if d.ID == id {
			return d, true
		}
	}
	return PackDef{}, false
}

// record reads the record of an installed pack.
func (l *Library) record(id string) (*Record, bool) {
	raw, err := os.ReadFile(filepath.Join(l.packDir(id), "pack.json"))
	if err != nil {
		return nil, false
	}
	var r Record
	if json.Unmarshal(raw, &r) != nil || r.ID != id {
		return nil, false
	}
	return &r, true
}

// installed lists the records of the installed packs, by id.
func (l *Library) installed() []*Record {
	entries, _ := os.ReadDir(l.dir)
	var out []*Record
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") || strings.HasSuffix(e.Name(), ".partial") {
			continue
		}
		if r, ok := l.record(e.Name()); ok {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Packs lists the packs of the list and the ones the person imported, with their state.
func (l *Library) Packs() []PackView {
	var out []PackView
	have := map[string]bool{}
	for _, r := range l.installed() {
		have[r.ID] = true
		out = append(out, l.viewOf(r.ID))
	}
	for _, d := range l.defs {
		if !have[d.ID] {
			out = append(out, l.viewOf(d.ID))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Pack is one pack, from the list or imported; ErrNotFound when it is neither.
func (l *Library) Pack(id string) (PackView, error) {
	if _, ok := l.def(id); !ok {
		if _, ok := l.record(id); !ok {
			return PackView{}, ErrNotFound
		}
	}
	return l.viewOf(id), nil
}

func (l *Library) viewOf(id string) PackView {
	v := PackView{ID: id, State: StateAvailable, Tags: []string{}}
	if d, ok := l.def(id); ok {
		v.Name, v.Author, v.Licence, v.Source, v.Page, v.Version, v.SizeBytes, v.Description = d.Name, d.Author, d.Licence, "catalog", d.Page, d.Version, d.SizeBytes, d.Description
		v.Tags = append([]string{}, d.Tags...)
	}
	if r, ok := l.record(id); ok {
		v.Name, v.Author, v.Licence, v.Attribution, v.Source, v.Page, v.Version, v.Description = r.Name, r.Author, r.Licence, r.Attribution, r.Source, r.Page, r.Version, r.Description
		v.SizeBytes, v.State, v.Models, v.Skipped = r.SizeBytes, StateInstalled, len(r.Models), r.Skipped
		v.Tags = append([]string{}, r.Tags...)
	}
	l.mu.Lock()
	if j := l.jobs[id]; j != nil {
		switch {
		case j.running:
			v.State = StateInstalling
			v.Progress = &Progress{Done: j.done.Load(), Total: j.total.Load()}
		case j.err != "" && v.State != StateInstalled:
			v.State, v.Error = StateFailed, j.err
		}
	}
	l.mu.Unlock()
	return v
}

// previewKind ranks the folders where a pack keeps a picture of each model: Kenney's Previews are named like the model; some kits have
// instead an Isometric view (<name>_NE.png, _NW, _SE, _SW) and a Side view (<name>.png). The first kind that has the model's picture wins.
var previewKind = map[string]int{"previews": 0, "preview": 0, "isometric": 1, "side": 2}

// previewName is the model a picture in a preview folder belongs to: its stem, without the corner of an isometric view.
func previewName(stem string, kind int) string {
	if kind == 1 {
		for _, c := range []string{"_NE", "_NW", "_SE", "_SW"} {
			if strings.HasSuffix(stem, c) {
				return strings.TrimSuffix(stem, c)
			}
		}
	}
	return stem
}

var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,95}$`)

// index reads the models of the files folder of a pack. A .glb that is not a usable model, or whose name is not a plain name or is
// the name of another one, is left out and named.
func index(files string, packTags []string) ([]Model, []Skipped) {
	var models []Model
	var skipped []Skipped
	seen := map[string]bool{}
	previews := map[string]string{}
	previewRank := map[string]int{}
	var glbs []string
	_ = filepath.WalkDir(files, func(p string, d os.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return nil
		}
		rel, _ := filepath.Rel(files, p)
		rel = filepath.ToSlash(rel)
		switch strings.ToLower(path.Ext(rel)) {
		case ".glb":
			glbs = append(glbs, rel)
		case ".png", ".jpg", ".jpeg", ".webp":
			if kind, ok := previewKind[strings.ToLower(path.Base(path.Dir(rel)))]; ok {
				stem := strings.TrimSuffix(path.Base(rel), path.Ext(rel))
				name := previewName(stem, kind)
				// the best kind wins; inside one kind the first picture by name (the north-east corner of an isometric view comes first)
				if cur, have := previews[name]; !have || kind < previewRank[name] || (kind == previewRank[name] && rel < cur) {
					previews[name], previewRank[name] = rel, kind
				}
			}
		}
		return nil
	})
	sort.Strings(glbs)
	for _, rel := range glbs {
		name := strings.TrimSuffix(path.Base(rel), path.Ext(rel))
		switch {
		case !namePattern.MatchString(name):
			skipped = append(skipped, Skipped{rel, "the name is not a plain name (letters, digits, '.', '_' or '-')"})
			continue
		case seen[name]:
			skipped = append(skipped, Skipped{rel, "another model of the pack has the same name"})
			continue
		}
		m, err := blender.ReadModel(filepath.Join(files, filepath.FromSlash(rel)))
		if err != nil {
			skipped = append(skipped, Skipped{rel, "not a usable model: " + err.Error()})
			continue
		}
		if m.Triangles > blender.MaxModelTriangles {
			skipped = append(skipped, Skipped{rel, fmt.Sprintf("%d triangles, at most %d", m.Triangles, blender.MaxModelTriangles)})
			continue
		}
		seen[name] = true
		models = append(models, Model{Name: name, File: rel, Clips: nonNil(m.Clips), Triangles: m.Triangles, Needs: nonNil(m.Needs),
			Tags: tagsOf(name, packTags), Preview: previews[name]})
	}
	return models, skipped
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// tagsOf are the tags of the pack and the words of the model's name, lower case and without repeats.
func tagsOf(name string, packTags []string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(t string) {
		t = strings.ToLower(strings.TrimSpace(t))
		if t != "" && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	for _, t := range packTags {
		add(t)
	}
	for _, w := range strings.FieldsFunc(name, func(r rune) bool { return r == '-' || r == '_' || r == '.' }) {
		add(w)
	}
	return out
}

func viewOfModel(r *Record, m Model) ModelView {
	v := ModelView{ID: r.ID + "/" + m.Name, Pack: r.ID, Name: m.Name, Tags: m.Tags, Clips: m.Clips, Triangles: m.Triangles, Needs: m.Needs,
		Licence: r.Licence, Author: r.Author, Attribution: r.Attribution, Source: r.Source}
	if g := m.Generated; g != nil {
		v.Source, v.Project, v.Entry, v.Adult = "generated", g.Project, g.Entry, g.Adult
		// the licence of a generated model is its engine's, not the pack's
		at := g.GeneratedAt
		v.Licence, v.Attribution, v.Prompt, v.Engine, v.EngineVersion, v.GeneratedAt = g.Licence, g.Attribution, g.Prompt, g.Engine, g.EngineVersion, &at
		if g.Prompt == "" {
			v.Prompt = g.Picture
		}
	}
	return v
}

// Query is what a search of the catalog asks for.
type Query struct {
	Pack, Q, Tag string
	// Source (catalog, user or generated), Engine and Project keep the models of that origin; Engine and Project mean generated models.
	Source, Engine, Project string
	HasClips                bool
	// HideAdult leaves out the models marked as adult (a server that does not show them).
	HideAdult     bool
	Limit, Offset int
}

// Models searches the models of the installed packs and returns the page and the total.
func (l *Library) Models(q Query) ([]ModelView, int) {
	needle := strings.ToLower(strings.TrimSpace(q.Q))
	var all []ModelView
	for _, r := range l.installed() {
		if q.Pack != "" && r.ID != q.Pack {
			continue
		}
		for _, m := range r.Models {
			if q.HasClips && len(m.Clips) == 0 {
				continue
			}
			if q.Tag != "" && !contains(m.Tags, strings.ToLower(q.Tag)) {
				continue
			}
			if !originMatches(r, m, q) {
				continue
			}
			if needle != "" && !strings.Contains(strings.ToLower(m.Name), needle) && !strings.Contains(strings.ToLower(strings.Join(m.Tags, " ")), needle) &&
				(m.Generated == nil || !strings.Contains(strings.ToLower(m.Generated.Prompt), needle)) {
				continue
			}
			all = append(all, viewOfModel(r, m))
		}
	}
	total := len(all)
	if q.Offset > 0 {
		if q.Offset >= len(all) {
			all = nil
		} else {
			all = all[q.Offset:]
		}
	}
	if q.Limit > 0 && len(all) > q.Limit {
		all = all[:q.Limit]
	}
	if all == nil {
		all = []ModelView{}
	}
	return all, total
}

// originMatches applies the filters by origin and the adult rule of a query to a model.
func originMatches(r *Record, m Model, q Query) bool {
	g := m.Generated
	if q.HideAdult && g != nil && g.Adult {
		return false
	}
	if q.Source != "" {
		src := r.Source
		if g != nil {
			src = "generated"
		}
		if src != q.Source {
			return false
		}
	}
	if q.Engine != "" && (g == nil || g.Engine != q.Engine) {
		return false
	}
	if q.Project != "" && (g == nil || g.Project != q.Project) {
		return false
	}
	return true
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// Model is one model by its pack and name.
func (l *Library) Model(pack, name string) (ModelView, error) {
	r, m, err := l.find(pack, name)
	if err != nil {
		return ModelView{}, err
	}
	return viewOfModel(r, *m), nil
}

// ModelFile is the path of the binary glTF of a model, as the library keeps it.
func (l *Library) ModelFile(pack, name string) (string, error) {
	_, m, err := l.find(pack, name)
	if err != nil {
		return "", err
	}
	return filepath.Join(l.FilesDir(pack), filepath.FromSlash(m.File)), nil
}

func (l *Library) find(pack, name string) (*Record, *Model, error) {
	r, ok := l.record(pack)
	if !ok {
		return nil, nil, fmt.Errorf("%w: pack %q is not installed", ErrNotFound, pack)
	}
	for i := range r.Models {
		if r.Models[i].Name == name {
			return r, &r.Models[i], nil
		}
	}
	return nil, nil, fmt.Errorf("%w: pack %q has no model %q", ErrNotFound, pack, name)
}

// ParseID splits "<pack>/<name>" into its two parts.
func ParseID(id string) (pack, name string, ok bool) {
	pack, name, ok = strings.Cut(id, "/")
	return pack, name, ok && pack != "" && name != "" && !strings.Contains(name, "/")
}

// writeRecord writes pack.json (the last thing an install does, so that a pack with a record is a complete one).
func writeRecord(dir string, r *Record) error {
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "pack.json"), append(raw, '\n'), 0o644)
}
