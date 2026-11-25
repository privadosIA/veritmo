// Package prompts is what the store of prompt presets is made of, shared by the server (which serves it) and by the command that fills it: the preset,
// its kinds and limits, its check, and its store. The presets are rows of the database (db.go: DB, its migration and the importer of the files they
// were kept in before, <dir>/<kind>/<id>.json); the folder under dir is only where the downloads of `veritmo prompts fetch` are kept (.cache).
package prompts

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"veritmo/internal/platform/settings"
	store "veritmo/internal/platform/store"
)

// Kinds are the kinds of preset.
var Kinds = []string{"image", "video", "motion", "music", "animation", "model3d"}

// Limits.
const (
	MaxPromptLength = 2000
	maxTitle        = 200
	maxTags         = 20
	maxTag          = 40
)

// StoreDir is the folder the files of the presets (the downloads in .cache, and the presets of before the database) are under: the media directory's
// prompts/ and, with no media directory, prompts/ beside the projects directory. The media directory is the one given, else MEDIA_DIR, else the
// one written in ~/.config/veritmo/media-dir (what scripts/serve.sh reads too); "off" means none.
func StoreDir(mediaDir, projectsDir string) string {
	if mediaDir == "" {
		mediaDir = os.Getenv("MEDIA_DIR")
		if mediaDir == "" {
			mediaDir = savedMediaDir()
		}
	}
	if mediaDir != "" && mediaDir != "off" {
		return filepath.Join(mediaDir, "prompts")
	}
	return filepath.Join(filepath.Dir(projectsDir), "prompts")
}

// savedMediaDir is the media directory written by hand in ~/.config/veritmo/media-dir.
func savedMediaDir() string {
	dir := settings.Dir()
	if dir == "" {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(dir, "media-dir"))
	if err != nil {
		return ""
	}
	line, _, _ := strings.Cut(string(b), "\n")
	return strings.TrimSpace(line)
}

// CacheDir is the folder under the store's directory where downloads are kept: it is not a kind, so it is never read as presets.
const CacheDir = ".cache"

// Preset is one prompt.
type Preset struct {
	ID        string   `json:"id"`
	Kind      string   `json:"kind"`
	Title     string   `json:"title"`
	Prompt    string   `json:"prompt"`
	Negative  string   `json:"negative"`
	Tags      []string `json:"tags"`
	Engine    string   `json:"engine"`
	Language  string   `json:"language"`
	Source    string   `json:"source"`
	SourceID  string   `json:"source_id"`
	Licence   string   `json:"licence"`
	NSFW      bool     `json:"nsfw"`
	Own       bool     `json:"own"`
	CreatedAt string   `json:"created_at"`
}

// ValidKind says whether k is a kind of preset.
func ValidKind(k string) bool {
	for _, x := range Kinds {
		if x == k {
			return true
		}
	}
	return false
}

// Key is what says a preset is already kept: its kind, source and source id ("" when it has no source id).
func Key(p *Preset) string {
	if p.Source == "" || p.SourceID == "" {
		return ""
	}
	return p.Kind + "\x00" + p.Source + "\x00" + p.SourceID
}

// Clean checks a preset a client or a source gave and fills what it leaves out; it returns the reason it is refused, or "".
func Clean(p *Preset) string {
	p.Title, p.Prompt, p.Negative = strings.TrimSpace(p.Title), strings.TrimSpace(p.Prompt), strings.TrimSpace(p.Negative)
	switch {
	case !ValidKind(p.Kind):
		return fmt.Sprintf("kind must be one of %s, got %q", strings.Join(Kinds, ", "), p.Kind)
	case p.Prompt == "":
		return "prompt is empty"
	case len([]rune(p.Prompt)) > MaxPromptLength || len([]rune(p.Negative)) > MaxPromptLength:
		return fmt.Sprintf("prompt and negative are limited to %d characters", MaxPromptLength)
	case len([]rune(p.Title)) > maxTitle:
		return fmt.Sprintf("title is limited to %d characters", maxTitle)
	case len(p.Tags) > maxTags:
		return fmt.Sprintf("at most %d tags", maxTags)
	}
	tags := make([]string, 0, len(p.Tags))
	for _, t := range p.Tags {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" {
			continue
		}
		if len([]rune(t)) > maxTag {
			return fmt.Sprintf("a tag is limited to %d characters", maxTag)
		}
		tags = append(tags, t)
	}
	p.Tags = tags
	if p.Title == "" {
		p.Title = string([]rune(p.Prompt)[:min(60, len([]rune(p.Prompt)))])
	}
	return ""
}

// NewID makes the id of a preset.
func NewID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return "p-" + hex.EncodeToString(b[:])
}

// Path is where the file of a preset is.
func Path(dir string, p *Preset) string { return filepath.Join(dir, p.Kind, p.ID+".json") }

// Save writes the file of a preset (atomically), making its kind folder if needed.
func Save(dir string, p *Preset) error {
	if err := os.MkdirAll(filepath.Join(dir, p.Kind), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	path := Path(dir, p)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func lstat(path string) (os.FileInfo, error) { return os.Lstat(path) }

// readKindReport reads the presets of one kind folder like ReadKind and names the files it skipped and why.
func readKindReport(dir, kind string) ([]*Preset, []store.Skipped) {
	files, _ := os.ReadDir(filepath.Join(dir, kind))
	var out []*Preset
	var skipped []store.Skipped
	for _, f := range files {
		if f.IsDir() || !strings.HasSuffix(f.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, kind, f.Name())
		b, err := os.ReadFile(path)
		if err != nil {
			skipped = append(skipped, store.Skipped{Path: path, Reason: err.Error()})
			continue
		}
		var p Preset
		switch {
		case json.Unmarshal(b, &p) != nil:
			skipped = append(skipped, store.Skipped{Path: path, Reason: "it is not a JSON preset"})
		case p.ID == "" || p.Kind != kind || p.ID+".json" != f.Name():
			skipped = append(skipped, store.Skipped{Path: path, Reason: "its id or its kind does not match where the file is"})
		default:
			out = append(out, &p)
		}
	}
	return out, skipped
}
