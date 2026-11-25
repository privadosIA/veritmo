package hyperframes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"veritmo/internal/platform/store"
)

// DirEnv names the environment variable that moves the folder of the blocks.
const DirEnv = "HYPERFRAMES_DIR"

// DefaultDir is the folder of the blocks inside the server's tree (it is not committed).
const DefaultDir = "hyperframes"

// ResolveDir gives the folder the blocks live in: $HYPERFRAMES_DIR, else hyperframes/ in the working directory.
func ResolveDir(getenv func(string) string) string {
	if v := strings.TrimSpace(getenv(DirEnv)); v != "" {
		return v
	}
	return DefaultDir
}

// RepoURL is the address of the HyperFrames repository, kept in the origin of the blocks.
const RepoURL = "https://github.com/heygen-com/hyperframes"

// Origin says where a block came from.
type Origin struct {
	URL     string `json:"url"`
	Version string `json:"version,omitempty"`
	Commit  string `json:"commit,omitempty"`
	// Poster and Video are the addresses the registry gives for its own preview; they are kept as a reference and never fetched.
	Poster string `json:"poster,omitempty"`
	Video  string `json:"video,omitempty"`
}

// Block is a HyperFrames block as it is kept: the registry's own metadata plus what the server knows about it. The files
// of the block are stored untouched next to block.json.
type Block struct {
	Name        string   `json:"name"`
	Title       string   `json:"title"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	License     string   `json:"license,omitempty"`
	Author      string   `json:"author,omitempty"`
	Width       int      `json:"width"`
	Height      int      `json:"height"`
	Duration    float64  `json:"duration"`
	// HTML is the main file of the block, relative to its folder.
	HTML string `json:"html"`
	// Files are all the files of the block, relative to its folder (including the main one and block.json's siblings).
	Files []string `json:"files"`
	// Variables are the registry's variables of the block, as it declares them.
	Variables json.RawMessage `json:"variables,omitempty"`
	Origin    Origin          `json:"origin"`
	// Kind is "component" for a piece of the registry's components (a mountable sub-composition drawn through its demo), empty for a block.
	Kind string `json:"kind,omitempty"`
	// Family, Profile and Jobs are the registry's own classification of a component; SyncPoints its named moments (id, phase, offset).
	Family     string          `json:"family,omitempty"`
	Profile    string          `json:"profile,omitempty"`
	Jobs       []string        `json:"jobs,omitempty"`
	SyncPoints json.RawMessage `json:"sync_points,omitempty"`
	Imported   time.Time       `json:"imported"`
	// Edited says the user changed the block: an import does not overwrite it unless forced.
	Edited bool `json:"edited,omitempty"`
	// Placeholders are files the registry lists for the block and does not ship, made by the store so that the block can be drawn (see
	// placeholders.go): pictures the server generated or procedural ones, a silent clip, the open fonts. They are not the registry's own.
	Placeholders []string `json:"placeholders,omitempty"`
	// Checks is the outcome of the last run of the minimum checks (nil: not run yet).
	Checks *CheckResult `json:"checks,omitempty"`
}

// CheckResult is what the minimum checks found.
type CheckResult struct {
	OK       bool      `json:"ok"`
	Problems []string  `json:"problems,omitempty"`
	Warnings []string  `json:"warnings,omitempty"`
	At       time.Time `json:"at"`
	// NeedsGPU says the block did not draw here because it asks for WebGPU or WebGL and this machine has none the browser
	// can use: not a defect of the block.
	NeedsGPU bool `json:"needs_gpu,omitempty"`
	// MissingFiles are files the registry lists for the block that it does not ship (pictures, video, audio): the block cannot
	// look as designed until they are supplied.
	MissingFiles []string `json:"missing_files,omitempty"`
	// Runtime is what drew the block in the checks: "upstream" (HyperFrames' own) or "builtin".
	Runtime string `json:"runtime,omitempty"`
}

// Store is the catalog of the blocks: the record of each block is a row of the database when the store has one (NewStoreDB: the server and the
// commands) and its block.json otherwise; the files of a block are always in its folder under Dir.
type Store struct {
	Dir string
	db  *store.Store
}

var blockName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,80}$`)

// ValidName says whether name can be a folder of the store.
func ValidName(name string) bool { return blockName.MatchString(name) }

func (s Store) blocksDir() string { return filepath.Join(s.Dir, "blocks") }

// Path is the folder of a block.
func (s Store) Path(name string) (string, error) {
	if !ValidName(name) {
		return "", fmt.Errorf("%q is not a block name", name)
	}
	return filepath.Join(s.blocksDir(), name), nil
}

// ErrNotFound is returned for a block that is not in the store.
var ErrNotFound = errors.New("no such block")

// Get reads the metadata of a block.
func (s Store) Get(name string) (Block, error) {
	if s.db != nil {
		return s.getRow(name)
	}
	dir, err := s.Path(name)
	if err != nil {
		return Block{}, err
	}
	b, err := os.ReadFile(filepath.Join(dir, "block.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return Block{}, ErrNotFound
	}
	if err != nil {
		return Block{}, err
	}
	var blk Block
	if err := json.Unmarshal(b, &blk); err != nil {
		return Block{}, fmt.Errorf("block.json of %s: %w", name, err)
	}
	return blk, nil
}

// List gives every block, by name.
func (s Store) List() ([]Block, error) {
	if s.db != nil {
		return s.listRows()
	}
	ents, err := os.ReadDir(s.blocksDir())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Block
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		if b, err := s.Get(e.Name()); err == nil {
			out = append(out, b)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Save writes the record of a block: its row, or its block.json without a database.
func (s Store) Save(b Block) error {
	dir, err := s.Path(b.Name)
	if err != nil {
		return err
	}
	if s.db != nil {
		return s.saveRow(b)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, ".block.json.tmp")
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, "block.json"))
}

// Read returns a file of a block by its path inside the block. A path that leaves the folder or is not a file of the
// block is refused.
func (s Store) Read(name, rel string) ([]byte, error) {
	p, err := s.filePath(name, rel)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(p)
}

func (s Store) filePath(name, rel string) (string, error) {
	dir, err := s.Path(name)
	if err != nil {
		return "", err
	}
	rel = strings.TrimPrefix(filepath.ToSlash(rel), "/")
	clean := filepath.Clean(filepath.FromSlash(rel))
	if rel == "" || clean == "." || clean == "block.json" || strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
		return "", fmt.Errorf("%q is not a file of the block", rel)
	}
	p := filepath.Join(dir, clean)
	if fi, err := os.Stat(p); err != nil || !fi.Mode().IsRegular() {
		return "", fs.ErrNotExist
	}
	return p, nil
}

// WriteHTML replaces the main file of a block (an edit by the user) and marks the block as edited, with the result of the checks that
// passed it. The picture kept as its thumbnail is dropped: it is drawn again on the next request.
func (s Store) WriteHTML(name, html string, checks CheckResult) error {
	b, err := s.Get(name)
	if err != nil {
		return err
	}
	dir, _ := s.Path(name)
	if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(b.HTML)), []byte(html), 0o644); err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(dir, ThumbFile))
	b.Edited = true
	b.Checks = &checks
	return s.Save(b)
}

// ThumbFile is the name of the thumbnail next to the block's files; it is not one of the block's own files.
const ThumbFile = ".thumb.jpg"

// Source is what Import needs to know about where the blocks are.
type Source struct {
	// Dir is the folder with the blocks (registry/blocks of a clone of the repository): <name>/<name>.html and registry-item.json.
	Dir string
	// Version and Commit are recorded in the origin of every block.
	Version, Commit string
	// URL is the address of the repository.
	URL string
	// NoFetch leaves the files the registry keeps off git (pictures, clips) unfetched; the store then makes placeholders for them.
	NoFetch bool
}

// Imported is what became of one block in an import.
type Imported struct {
	Name    string `json:"name"`
	Written bool   `json:"written"`
	// Skipped is why it was left as it was (it was edited), when it was.
	Skipped string `json:"skipped,omitempty"`
	Error   string `json:"error,omitempty"`
}

type registryItem struct {
	Name        string   `json:"name"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
	License     string   `json:"license"`
	Author      string   `json:"author"`
	Dimensions  struct {
		Width  int `json:"width"`
		Height int `json:"height"`
	} `json:"dimensions"`
	Duration float64 `json:"duration"`
	Files    []struct {
		Path string `json:"path"`
		Type string `json:"type"`
	} `json:"files"`
	Variables  json.RawMessage `json:"variables"`
	Family     string          `json:"family"`
	Profile    string          `json:"profile"`
	Jobs       []string        `json:"jobs"`
	SyncPoints json.RawMessage `json:"syncPoints"`
	Preview    struct {
		Video  string `json:"video"`
		Poster string `json:"poster"`
	} `json:"preview"`
}

// Import copies blocks from a clone of the registry into the store, whole and untouched. only limits it to those names (empty: all);
// force overwrites blocks the user edited. It never stops at one bad block.
func (s Store) Import(src Source, only []string, force bool) ([]Imported, error) {
	return s.importDir(src, only, force, "")
}

// ImportComponents is Import for the components of the registry (registry/components): each is stored whole as a block of kind component whose
// main file is its demo composition, with the size and length that composition declares.
func (s Store) ImportComponents(src Source, only []string, force bool) ([]Imported, error) {
	return s.importDir(src, only, force, "component")
}

func (s Store) importDir(src Source, only []string, force bool, kind string) ([]Imported, error) {
	ents, err := os.ReadDir(src.Dir)
	if err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, n := range only {
		want[n] = true
	}
	var out []Imported
	for _, e := range ents {
		if !e.IsDir() || len(want) > 0 && !want[e.Name()] {
			continue
		}
		r := Imported{Name: e.Name()}
		switch w, skipped, err := s.importOne(src, e.Name(), force, kind); {
		case err != nil:
			r.Error = err.Error()
		case skipped != "":
			r.Skipped = skipped
		default:
			r.Written = w
		}
		out = append(out, r)
	}
	return out, nil
}

func (s Store) importOne(src Source, name string, force bool, kind string) (bool, string, error) {
	if !ValidName(name) {
		return false, "", fmt.Errorf("%q is not a valid block name", name)
	}
	from := filepath.Join(src.Dir, name)
	raw, err := os.ReadFile(filepath.Join(from, "registry-item.json"))
	if err != nil {
		return false, "", fmt.Errorf("no registry-item.json: %w", err)
	}
	var item registryItem
	if err := json.Unmarshal(raw, &item); err != nil {
		return false, "", fmt.Errorf("registry-item.json: %w", err)
	}
	if old, err := s.Get(name); err == nil && old.Edited && !force {
		return false, "edited by the user: kept (use force to replace it)", nil
	}
	html := ""
	if kind == "component" {
		html = "demo.html"
		demo, err := os.ReadFile(filepath.Join(from, html))
		if err != nil {
			return false, "", errors.New("the component has no demo.html")
		}
		item.Dimensions.Width, item.Dimensions.Height, item.Duration = demoGeometry(string(demo))
	} else {
		for _, f := range item.Files {
			if f.Type == "hyperframes:composition" {
				html = f.Path
				break
			}
		}
		if html == "" {
			return false, "", errors.New("the registry item lists no composition")
		}
	}
	dir, _ := s.Path(name)
	tmp := dir + ".importing"
	_ = os.RemoveAll(tmp)
	var files []string
	err = filepath.WalkDir(from, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(from, p)
		if rel == "registry-item.json" {
			return nil
		}
		if err := copyFile(p, filepath.Join(tmp, rel)); err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		_ = os.RemoveAll(tmp)
		return false, "", err
	}
	if err := os.WriteFile(filepath.Join(tmp, "registry-item.json"), raw, 0o644); err != nil {
		_ = os.RemoveAll(tmp)
		return false, "", err
	}
	sort.Strings(files)
	if err := os.RemoveAll(dir); err != nil {
		_ = os.RemoveAll(tmp)
		return false, "", err
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return false, "", err
	}
	if err := os.Rename(tmp, dir); err != nil {
		return false, "", err
	}
	title := item.Title
	if title == "" {
		title = name
	}
	defer func() {
		if !src.NoFetch {
			_, _ = s.FetchAssets(context.Background(), name)
		}
		_, _ = s.FillMissing(name)
	}()
	if err := s.Save(Block{
		Name: name, Title: title, Description: item.Description, Tags: item.Tags, License: item.License, Author: item.Author,
		Width: item.Dimensions.Width, Height: item.Dimensions.Height, Duration: item.Duration,
		HTML: html, Files: files, Variables: item.Variables, Kind: kind, Family: item.Family, Profile: item.Profile, Jobs: item.Jobs, SyncPoints: item.SyncPoints,
		Origin:   Origin{URL: src.URL, Version: src.Version, Commit: src.Commit, Poster: item.Preview.Poster, Video: item.Preview.Video},
		Imported: time.Now().UTC().Truncate(time.Second),
	}); err != nil {
		_ = os.RemoveAll(dir) // the folder was just written: a block with no record is not left behind
		return false, "", err
	}
	return true, "", nil
}

func copyFile(from, to string) error {
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(to)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

var (
	reDemoDur = regexp.MustCompile(`data-duration="([0-9.]+)"`)
	reDemoW   = regexp.MustCompile(`data-width="([0-9]+)"`)
	reDemoH   = regexp.MustCompile(`data-height="([0-9]+)"`)
)

// demoGeometry reads the size and length of a demo composition from its first mount (data-width, data-height, data-duration), 1920x1080 and
// 5 seconds when it does not say.
func demoGeometry(html string) (w, h int, dur float64) {
	w, h, dur = 1920, 1080, 5
	if m := reDemoW.FindStringSubmatch(html); m != nil {
		_, _ = fmt.Sscanf(m[1], "%d", &w)
	}
	if m := reDemoH.FindStringSubmatch(html); m != nil {
		_, _ = fmt.Sscanf(m[1], "%d", &h)
	}
	if m := reDemoDur.FindStringSubmatch(html); m != nil {
		_, _ = fmt.Sscanf(m[1], "%g", &dur)
	}
	if w < 1 || h < 1 || dur <= 0 {
		return 1920, 1080, 5
	}
	return w, h, dur
}
