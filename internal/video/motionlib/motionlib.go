// Package motionlib keeps the library of motions: short videos of people moving (a dance, a walk, a gesture) that the video-edit tool `motion-control`
// applies to any picture. A motion is a folder of the settings directory with the control made once from its video (the depth of the people, the rest
// black, three-channel PNG at 16 frames per second) and a small preview, and a record that says what it is: a row of the database (NewStoreDB, what
// the server uses) or, without one, a motion.json in the folder. The video itself is never kept. See specs/video-edit-motion-library.
package motionlib

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

// The states of a motion: only a ready one can be used.
const (
	StateProcessing = "processing"
	StateReady      = "ready"
	StateFailed     = "failed"
)

// Limits.
const (
	// MaxSeconds is the longest video a motion is made from: its control is about 480 frames.
	MaxSeconds = 30
	MaxTitle   = 120
	MaxTags    = 10
	MaxTagLen  = 40
	// FPS is the rate of the control frames, the one the video model was measured at.
	FPS = 16
	// WindowFrames is the length of what motion control takes from a motion.
	WindowFrames = 33
	// IDPrefix starts every motion id.
	IDPrefix = "mot-"
)

// Errors a caller tells apart.
var (
	ErrNotFound = errors.New("motion not found")
	ErrNotReady = errors.New("the motion is not ready")
)

var idPattern = regexp.MustCompile(`^mot-[0-9]{8}-[0-9]{6}-[0-9a-f]{6}$`)

// ValidID reports whether s looks like the id of a motion.
func ValidID(s string) bool { return idPattern.MatchString(s) }

// Motion is motion.json.
type Motion struct {
	ID    string   `json:"id"`
	Title string   `json:"title"`
	Tags  []string `json:"tags"`
	// Source is the address (YouTube) the video was taken from; empty when it was uploaded as a file. The video itself is
	// never kept, so this is the only trace of where the movement comes from.
	Source string `json:"source,omitempty"`
	// Duration is the seconds of the video; Frames the control frames made (Duration at 16 per second).
	Duration float64 `json:"duration"`
	Frames   int     `json:"frames"`
	// People is how many people the motion shows (an estimate: people who touch count as one).
	People int `json:"people"`
	// Width and Height are the shape of the control frames, which follows the video's.
	Width     int       `json:"width"`
	Height    int       `json:"height"`
	State     string    `json:"state"`
	Error     string    `json:"error,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Store is the library: one folder per motion under Dir, with its control frames and its preview. The record of each motion is its motion.json, or a row
// of the database when the store has one (NewStoreDB: the server).
type Store struct {
	Dir string
	mu  sync.Mutex
	db  *store.Store
}

// NewStore makes a store over dir.
func NewStore(dir string) *Store { return &Store{Dir: dir} }

// NewID makes the id of a new motion (time and random hex, like the other managed ids).
func NewID() string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return IDPrefix + time.Now().UTC().Format("20060102-150405") + "-" + hex.EncodeToString(b)
}

// Path is the folder of a motion.
func (s *Store) Path(id string) string { return filepath.Join(s.Dir, id) }

// ControlDir is the folder of the control frames of a motion.
func (s *Store) ControlDir(id string) string { return filepath.Join(s.Path(id), "control") }

// PreviewPath is the preview video of a motion.
func (s *Store) PreviewPath(id string) string { return filepath.Join(s.Path(id), "preview.mp4") }

// CheckText validates what the user writes about a motion and returns it cleaned (trimmed, tags without repetitions);
// the error names the field. A blank title is allowed: the motion is then called "motion".
func CheckText(title string, tags []string) (string, []string, error) {
	title = strings.TrimSpace(title)
	if n := len([]rune(title)); n > MaxTitle {
		return "", nil, fmt.Errorf("title has %d characters, the most is %d", n, MaxTitle)
	}
	out := []string{}
	seen := map[string]bool{}
	for _, t := range tags {
		t = strings.TrimSpace(t)
		if t == "" || seen[strings.ToLower(t)] {
			continue
		}
		if n := len([]rune(t)); n > MaxTagLen {
			return "", nil, fmt.Errorf("a tag has %d characters, the most is %d", n, MaxTagLen)
		}
		seen[strings.ToLower(t)] = true
		out = append(out, t)
	}
	if len(out) > MaxTags {
		return "", nil, fmt.Errorf("tags has %d entries, the most is %d", len(out), MaxTags)
	}
	return title, out, nil
}

// Create makes the folder of a new motion in the state processing and writes its file.
func (s *Store) Create(title string, tags []string) (Motion, error) {
	title, tags, err := CheckText(title, tags)
	if err != nil {
		return Motion{}, err
	}
	if title == "" {
		title = "motion"
	}
	m := Motion{ID: NewID(), Title: title, Tags: tags, State: StateProcessing, CreatedAt: time.Now().UTC()}
	if err := os.MkdirAll(s.ControlDir(m.ID), 0o755); err != nil {
		return Motion{}, err
	}
	if err := s.Save(m); err != nil {
		_ = os.RemoveAll(s.Path(m.ID))
		return Motion{}, err
	}
	return m, nil
}

// Save writes the file of a motion whose folder exists (a deleted motion is not brought back).
func (s *Store) Save(m Motion) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := os.Stat(s.Path(m.ID)); err != nil {
		return ErrNotFound
	}
	if s.db != nil {
		return s.saveRow(m)
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(s.Path(m.ID), "motion.json.tmp")
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(s.Path(m.ID), "motion.json"))
}

// Get reads a motion.
func (s *Store) Get(id string) (Motion, error) {
	if !ValidID(id) {
		return Motion{}, ErrNotFound
	}
	if s.db != nil {
		return s.getRow(id)
	}
	b, err := os.ReadFile(filepath.Join(s.Path(id), "motion.json"))
	if err != nil {
		return Motion{}, ErrNotFound
	}
	var m Motion
	if err := json.Unmarshal(b, &m); err != nil {
		return Motion{}, ErrNotFound
	}
	if m.Tags == nil {
		m.Tags = []string{}
	}
	return m, nil
}

// List returns the motions, newest first; an empty library is an empty list.
func (s *Store) List() []Motion {
	if s.db != nil {
		return s.listRows()
	}
	entries, _ := os.ReadDir(s.Dir)
	out := []Motion{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if m, err := s.Get(e.Name()); err == nil {
			out = append(out, m)
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

// Delete removes a motion with its files.
func (s *Store) Delete(id string) error {
	if _, err := s.Get(id); err != nil {
		return err
	}
	if s.db != nil {
		if err := s.db.DB.Where("id = ?", id).Delete(&Row{}).Error; err != nil {
			return err
		}
	}
	return os.RemoveAll(s.Path(id))
}

// MarkInterrupted sets to failed the motions that were being made when the server stopped, and says how many.
func (s *Store) MarkInterrupted() int {
	n := 0
	for _, m := range s.List() {
		if m.State == StateProcessing {
			m.State, m.Error = StateFailed, "the preparation was interrupted when the server stopped"
			if s.Save(m) == nil {
				n++
			}
		}
	}
	return n
}

// Window is the control a motion gives motion control: the folder of its frames, the number of the first frame to
// take (the files are numbered from 0001, so the file number is first+1) and a description of the part used.
type Window struct {
	Dir   string
	First int
	// From and To are the seconds of the motion the window covers.
	From, To float64
}

// WindowAt is the window of WindowFrames frames starting `start` seconds into a ready motion. It is refused when the
// motion is not ready or lasts less than start plus the window.
func (s *Store) WindowAt(id string, start float64) (Window, error) {
	m, err := s.Get(id)
	if err != nil {
		return Window{}, err
	}
	if m.State != StateReady {
		why := "it is still being prepared"
		if m.State == StateFailed {
			why = "its preparation failed: " + m.Error
		}
		return Window{}, fmt.Errorf("%w (%s)", ErrNotReady, why)
	}
	first := int(start*FPS + 0.5)
	if start < 0 || first+WindowFrames > m.Frames {
		return Window{}, fmt.Errorf("the motion lasts %.1f s: a window of %.1f s from %g s on does not fit", m.Duration, float64(WindowFrames)/FPS, start)
	}
	return Window{Dir: s.ControlDir(id), First: first, From: float64(first) / FPS, To: float64(first+WindowFrames) / FPS}, nil
}
