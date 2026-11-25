package motionref

// learning.go: the record of a learning from a reference (spec motion-learning): what it proposed, what it kept and what
// it dropped as too similar to what exists. The record holds only ids and the result of the comparison; the styles and
// the drafts it made are the ordinary ones, managed through their own routes.

import (
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

// The states of a learning.
const (
	LearningRunning = "running"
	LearningReady   = "ready"
	LearningFailed  = "failed"
)

// LearningIDPrefix starts every learning id.
const LearningIDPrefix = "learning-"

// ErrLearningNotFound is the learning that does not exist.
var ErrLearningNotFound = errors.New("learning not found")

var learningIDPattern = regexp.MustCompile(`^learning-[0-9]{8}-[0-9]{6}-[0-9a-f]{6}$`)

// ValidLearningID reports whether s looks like the id of a learning.
func ValidLearningID(s string) bool { return learningIDPattern.MatchString(s) }

// Learned is a proposal that was kept: the id of the style or of the draft it became.
type Learned struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Novel bool   `json:"novel"`
	// SimilarTo is the closest thing that exists; absent when nothing exists to compare with.
	SimilarTo *musicmotion.Similar `json:"similar_to,omitempty"`
}

// Discarded is a proposal that was not made because it is too similar to something that exists.
type Discarded struct {
	// Kind is "style" or "part".
	Kind string `json:"kind"`
	Name string `json:"name"`
	// Idea is the sentence of a part idea (absent for a style).
	Idea      string              `json:"idea,omitempty"`
	Novel     bool                `json:"novel"`
	SimilarTo musicmotion.Similar `json:"similar_to"`
	Reason    string              `json:"reason"`
}

// Skipped is something the learning could not do and why (the name of what is missing).
type Skipped struct {
	What   string `json:"what"`
	Reason string `json:"reason"`
}

// Learning is learning.json.
type Learning struct {
	ID          string      `json:"id"`
	ReferenceID string      `json:"reference_id"`
	State       string      `json:"state"`
	Reason      string      `json:"reason,omitempty"`
	Strictness  string      `json:"strictness"`
	CreatedAt   time.Time   `json:"created_at"`
	FinishedAt  *time.Time  `json:"finished_at,omitempty"`
	Styles      []Learned   `json:"styles"`
	PartDrafts  []Learned   `json:"part_drafts"`
	Discarded   []Discarded `json:"discarded"`
	Skipped     []Skipped   `json:"skipped"`
}

func (l *Learning) fill() {
	if l.Styles == nil {
		l.Styles = []Learned{}
	}
	if l.PartDrafts == nil {
		l.PartDrafts = []Learned{}
	}
	if l.Discarded == nil {
		l.Discarded = []Discarded{}
	}
	if l.Skipped == nil {
		l.Skipped = []Skipped{}
	}
}

// LearningStore keeps the learnings, one JSON file each.
type LearningStore struct {
	Dir string
	mu  sync.Mutex
	db  *store.Store
}

// NewLearningStore makes a store over dir.
func NewLearningStore(dir string) *LearningStore { return &LearningStore{Dir: dir} }

func (s *LearningStore) path(id string) string { return filepath.Join(s.Dir, id+".json") }

// Create makes a learning of a reference in the state running.
func (s *LearningStore) Create(referenceID string, st musicmotion.Strictness) (Learning, error) {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	l := Learning{ID: LearningIDPrefix + time.Now().UTC().Format("20060102-150405") + "-" + hex.EncodeToString(b), ReferenceID: referenceID,
		State: LearningRunning, Strictness: string(st), CreatedAt: time.Now().UTC()}
	l.fill()
	return l, s.Save(l)
}

// Save writes a learning.
func (s *LearningStore) Save(l Learning) error {
	if s.db != nil {
		return s.dbSave(l)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return err
	}
	l.fill()
	b, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path(l.ID) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path(l.ID))
}

// Get reads a learning.
func (s *LearningStore) Get(id string) (Learning, error) {
	if !ValidLearningID(id) {
		return Learning{}, ErrLearningNotFound
	}
	if s.db != nil {
		return s.dbGet(id)
	}
	raw, err := os.ReadFile(s.path(id))
	if err != nil {
		return Learning{}, ErrLearningNotFound
	}
	var l Learning
	if json.Unmarshal(raw, &l) != nil {
		return Learning{}, ErrLearningNotFound
	}
	l.fill()
	return l, nil
}

// List returns the learnings, newest first; a reference id (not empty) keeps only the ones of that reference.
func (s *LearningStore) List(referenceID string) []Learning {
	if s.db != nil {
		return s.dbList(referenceID)
	}
	entries, _ := os.ReadDir(s.Dir)
	out := []Learning{}
	for _, e := range entries {
		name := e.Name()
		if filepath.Ext(name) != ".json" {
			continue
		}
		if l, err := s.Get(name[:len(name)-len(".json")]); err == nil && (referenceID == "" || l.ReferenceID == referenceID) {
			out = append(out, l)
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

// Running is the learning of a reference that is running, if there is one.
func (s *LearningStore) Running(referenceID string) (Learning, bool) {
	for _, l := range s.List(referenceID) {
		if l.State == LearningRunning {
			return l, true
		}
	}
	return Learning{}, false
}

// MarkInterrupted sets to failed the learnings that were running when the server stopped, and says how many.
func (s *LearningStore) MarkInterrupted() int {
	n := 0
	for _, l := range s.List("") {
		if l.State == LearningRunning {
			now := time.Now().UTC()
			l.State, l.Reason, l.FinishedAt = LearningFailed, "the work was interrupted when the server stopped", &now
			if s.Save(l) == nil {
				n++
			}
		}
	}
	return n
}
