package motionref

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"
	"veritmo/internal/platform/store"

	"veritmo/internal/music/musicmotion"
)

// PresetIDPrefix starts every style id.
const PresetIDPrefix = "preset-"

// ErrPresetNotFound is the style that does not exist.
var ErrPresetNotFound = errors.New("style not found")

var presetIDPattern = regexp.MustCompile(`^preset-[0-9]{8}-[0-9]{6}-[0-9a-f]{6}$`)

// ValidPresetID reports whether s looks like the id of a style.
func ValidPresetID(s string) bool { return presetIDPattern.MatchString(s) }

// PresetStore keeps the styles, one JSON file each. A style stays when the reference it came from is deleted.
type PresetStore struct {
	Dir string
	mu  sync.Mutex
	db  *store.Store
}

// NewPresetStore makes a store over dir.
func NewPresetStore(dir string) *PresetStore { return &PresetStore{Dir: dir} }

func newPresetID() string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return PresetIDPrefix + time.Now().UTC().Format("20060102-150405") + "-" + hex.EncodeToString(b)
}

func (s *PresetStore) path(id string) string { return filepath.Join(s.Dir, id+".json") }

// Add gives a new style an id and keeps it.
func (s *PresetStore) Add(p musicmotion.Preset) (musicmotion.Preset, error) {
	p.ID = newPresetID()
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now().UTC()
	}
	return p, s.write(p)
}

// Replace keeps a style that already exists under its id.
func (s *PresetStore) Replace(p musicmotion.Preset) error {
	if _, err := s.Get(p.ID); err != nil {
		return err
	}
	return s.write(p)
}

func (s *PresetStore) write(p musicmotion.Preset) error {
	if s.db != nil {
		return s.dbWrite(p)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return err
	}
	if p.Parts == nil {
		p.Parts = []string{}
	}
	if p.Adjustments == nil {
		p.Adjustments = []string{}
	}
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path(p.ID) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path(p.ID))
}

// Get reads a style.
func (s *PresetStore) Get(id string) (musicmotion.Preset, error) {
	if !ValidPresetID(id) {
		return musicmotion.Preset{}, ErrPresetNotFound
	}
	if s.db != nil {
		return s.dbGet(id)
	}
	b, err := os.ReadFile(s.path(id))
	if err != nil {
		return musicmotion.Preset{}, ErrPresetNotFound
	}
	var p musicmotion.Preset
	if json.Unmarshal(b, &p) != nil {
		return musicmotion.Preset{}, ErrPresetNotFound
	}
	if p.Parts == nil {
		p.Parts = []string{}
	}
	if p.Adjustments == nil {
		p.Adjustments = []string{}
	}
	return p, nil
}

// List returns the styles, newest first.
func (s *PresetStore) List() []musicmotion.Preset {
	if s.db != nil {
		return s.dbList()
	}
	entries, _ := os.ReadDir(s.Dir)
	out := []musicmotion.Preset{}
	for _, e := range entries {
		name := e.Name()
		if filepath.Ext(name) != ".json" {
			continue
		}
		if p, err := s.Get(name[:len(name)-len(".json")]); err == nil {
			out = append(out, p)
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

// Delete removes a style.
func (s *PresetStore) Delete(id string) error {
	if _, err := s.Get(id); err != nil {
		return err
	}
	if s.db != nil {
		return s.dbDelete(id)
	}
	return os.Remove(s.path(id))
}

// FromReference draws a style from a reference that is ready: its colours, the length of its shots and its
// description (the assistant w, which may be nil, chooses the parts).
func FromReference(ctx context.Context, w musicmotion.Writer, cat musicmotion.Catalog, ref Reference) (musicmotion.Preset, error) {
	if ref.State != StateReady || ref.Analysis == nil {
		return musicmotion.Preset{}, errors.New("the reference has not been analysed (its state is " + ref.State + ")")
	}
	src := musicmotion.PresetSource{Name: ref.Title, ReferenceID: ref.ID, MeanShot: ref.Analysis.MeanShot, Description: ref.Analysis.Description}
	for _, c := range ref.Analysis.Colours {
		src.Colours = append(src.Colours, musicmotion.PresetColour{Hex: c.Hex, Share: c.Share})
	}
	return musicmotion.MakePreset(ctx, w, cat, src)
}
