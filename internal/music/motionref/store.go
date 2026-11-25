// Package motionref keeps the videos the user gives as examples of a good piece (references), analyses them with what
// the server already has (frames, colours, cuts, tempo, a description of the look when a vision model is there) and
// draws styles from them. A reference lives in a folder of the settings directory; the video itself is never kept.
// See specs/motion-references.
package motionref

import (
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

// The states of a reference.
const (
	StateAnalysing = "analysing"
	StateReady     = "ready"
	StateFailed    = "failed"
)

// Limits of what the user writes about a reference and of the video.
const (
	MaxTitle   = 120
	MaxNotes   = 2000
	MaxTags    = 10
	MaxTagLen  = 40
	MaxSeconds = 20 * 60
	// IDPrefix starts every reference id.
	IDPrefix = "ref-"
)

// ErrNotFound is the reference that does not exist.
var ErrNotFound = errors.New("reference not found")

var idPattern = regexp.MustCompile(`^ref-[0-9]{8}-[0-9]{6}-[0-9a-f]{6}$`)

// ValidID reports whether s looks like the id of a reference.
func ValidID(s string) bool { return idPattern.MatchString(s) }

// Colour is a dominant colour with the share of the pixels it covers.
type Colour struct {
	Hex   string  `json:"hex"`
	Share float64 `json:"share"`
}

// Analysis is what the server measured on a video.
type Analysis struct {
	Duration float64 `json:"duration"`
	FPS      float64 `json:"fps"`
	Width    int     `json:"width"`
	Height   int     `json:"height"`
	// Frames are the file names of the key frames, in order, inside the reference's frames folder.
	Frames []string `json:"frames"`
	// Colours are the dominant colours of the whole video; Thirds those of each third of it.
	Colours []Colour   `json:"colours"`
	Thirds  [][]Colour `json:"thirds"`
	// Cuts is the number of changes of picture (one less than the shots) and MeanShot the mean length of a shot, in seconds.
	Cuts     int     `json:"cuts"`
	MeanShot float64 `json:"mean_shot"`
	// Shots are the shots of the video, Rhythm the sheet made from them and RhythmText the sheet as one paragraph. A reference
	// analysed before they existed has none until its breakdown is run.
	Shots      []Shot  `json:"shots,omitempty"`
	Rhythm     *Rhythm `json:"rhythm,omitempty"`
	RhythmText string  `json:"rhythm_text,omitempty"`
	// BreakdownState says where the reading of the shots by the vision model is (the BreakdownPending... states) and
	// BreakdownNote why it was skipped or failed.
	BreakdownState string `json:"breakdown_state,omitempty"`
	BreakdownNote  string `json:"breakdown_note,omitempty"`
	// Tempo (bpm) and Key are estimates of the audio; absent without sound or when there is no clear pulse or key.
	Tempo *float64 `json:"tempo,omitempty"`
	Key   *string  `json:"key,omitempty"`
	// Description is the look of the video as the vision model wrote it; DescriptionNote says why there is none.
	Description     string `json:"description,omitempty"`
	DescriptionNote string `json:"description_note,omitempty"`
}

// Reference is reference.json.
type Reference struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Notes     string    `json:"notes,omitempty"`
	Tags      []string  `json:"tags"`
	Source    string    `json:"source,omitempty"`
	State     string    `json:"state"`
	Error     string    `json:"error,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	Analysis  *Analysis `json:"analysis,omitempty"`
}

// Store is the library: one folder per reference under Dir.
type Store struct {
	Dir string
	mu  sync.Mutex
	// db, when set, keeps the records in the database; Dir then holds only the key frames.
	db *store.Store
}

// NewStore makes a store over dir.
func NewStore(dir string) *Store { return &Store{Dir: dir} }

// NewID makes the id of a new reference (time and random hex, like the other managed ids).
func NewID() string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return IDPrefix + time.Now().UTC().Format("20060102-150405") + "-" + hex.EncodeToString(b)
}

// Path is the folder of a reference.
func (s *Store) Path(id string) string { return filepath.Join(s.Dir, id) }

// FramesDir is the folder of the key frames of a reference.
func (s *Store) FramesDir(id string) string { return filepath.Join(s.Path(id), "frames") }

// ShotsDir is the folder of the pictures of the shots the vision model read: the start and the middle of each, kept with the
// reference like its key frames and deleted with it.
func (s *Store) ShotsDir(id string) string { return filepath.Join(s.FramesDir(id), "shots") }

// SaveAnalysis replaces the analysis of a reference with an and saves it, keeping what the user may have changed meanwhile (the
// title, the notes and the tags). A deleted reference is ErrNotFound.
func (s *Store) SaveAnalysis(id string, an *Analysis) error {
	ref, err := s.Get(id)
	if err != nil {
		return err
	}
	ref.Analysis = an
	return s.Save(ref)
}

// CheckText validates what the user writes about a reference and returns it cleaned (trimmed, tags without
// repetitions); the error names the field.
func CheckText(title, notes string, tags []string) (string, string, []string, error) {
	title, notes = strings.TrimSpace(title), strings.TrimSpace(notes)
	if n := len([]rune(title)); n > MaxTitle {
		return "", "", nil, fmt.Errorf("title has %d characters, the most is %d", n, MaxTitle)
	}
	if n := len([]rune(notes)); n > MaxNotes {
		return "", "", nil, fmt.Errorf("notes has %d characters, the most is %d", n, MaxNotes)
	}
	out := []string{}
	seen := map[string]bool{}
	for _, t := range tags {
		t = strings.TrimSpace(t)
		if t == "" || seen[strings.ToLower(t)] {
			continue
		}
		if n := len([]rune(t)); n > MaxTagLen {
			return "", "", nil, fmt.Errorf("a tag has %d characters, the most is %d", n, MaxTagLen)
		}
		seen[strings.ToLower(t)] = true
		out = append(out, t)
	}
	if len(out) > MaxTags {
		return "", "", nil, fmt.Errorf("tags has %d entries, the most is %d", len(out), MaxTags)
	}
	return title, notes, out, nil
}

// Create makes the folder of a new reference in the state analysing and writes its file.
func (s *Store) Create(title, notes string, tags []string, source string) (Reference, error) {
	if tags == nil {
		tags = []string{}
	}
	ref := Reference{ID: NewID(), Title: title, Notes: notes, Tags: tags, Source: source, State: StateAnalysing, CreatedAt: time.Now().UTC()}
	if err := os.MkdirAll(s.FramesDir(ref.ID), 0o755); err != nil {
		return Reference{}, err
	}
	var err error
	if s.db != nil {
		err = s.dbCreate(ref)
	} else {
		err = s.Save(ref)
	}
	if err != nil {
		_ = os.RemoveAll(s.Path(ref.ID))
		return Reference{}, err
	}
	return ref, nil
}

// Save writes the file of a reference whose folder exists (a deleted reference is not brought back).
func (s *Store) Save(ref Reference) error {
	if s.db != nil {
		return s.dbSave(ref)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := os.Stat(s.Path(ref.ID)); err != nil {
		return ErrNotFound
	}
	b, err := json.MarshalIndent(ref, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(s.Path(ref.ID), "reference.json.tmp")
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(s.Path(ref.ID), "reference.json"))
}

// Get reads a reference.
func (s *Store) Get(id string) (Reference, error) {
	if !ValidID(id) {
		return Reference{}, ErrNotFound
	}
	if s.db != nil {
		return s.dbGet(id)
	}
	b, err := os.ReadFile(filepath.Join(s.Path(id), "reference.json"))
	if err != nil {
		return Reference{}, ErrNotFound
	}
	var ref Reference
	if err := json.Unmarshal(b, &ref); err != nil {
		return Reference{}, ErrNotFound
	}
	if ref.Tags == nil {
		ref.Tags = []string{}
	}
	return ref, nil
}

// Update changes the fields given (nil keeps the field) of a reference and saves it.
func (s *Store) Update(id string, title, notes *string, tags *[]string) (Reference, error) {
	ref, err := s.Get(id)
	if err != nil {
		return Reference{}, err
	}
	t, n, g := ref.Title, ref.Notes, ref.Tags
	if title != nil {
		t = *title
	}
	if notes != nil {
		n = *notes
	}
	if tags != nil {
		g = *tags
	}
	if ref.Title, ref.Notes, ref.Tags, err = CheckText(t, n, g); err != nil {
		return Reference{}, err
	}
	return ref, s.Save(ref)
}

// List returns the references, newest first.
func (s *Store) List() []Reference {
	if s.db != nil {
		return s.dbList()
	}
	entries, _ := os.ReadDir(s.Dir)
	out := []Reference{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if ref, err := s.Get(e.Name()); err == nil {
			out = append(out, ref)
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

// Delete removes a reference with its files.
func (s *Store) Delete(id string) error {
	if _, err := s.Get(id); err != nil {
		return err
	}
	if s.db != nil {
		if err := s.dbDelete(id); err != nil {
			return err
		}
	}
	return os.RemoveAll(s.Path(id))
}

// MarkInterrupted sets to failed the references that were being analysed when the server stopped, and says how many.
func (s *Store) MarkInterrupted() int {
	n := 0
	for _, ref := range s.List() {
		if ref.State == StateAnalysing {
			ref.State, ref.Error = StateFailed, "the analysis was interrupted when the server stopped"
			if s.Save(ref) == nil {
				n++
			}
		} else if a := ref.Analysis; a != nil && (a.BreakdownState == BreakdownPending || a.BreakdownState == BreakdownReading) {
			a.BreakdownState, a.BreakdownNote = BreakdownFailed, "the reading of the shots was interrupted when the server stopped"
			if s.Save(ref) == nil {
				n++
			}
		}
	}
	return n
}
