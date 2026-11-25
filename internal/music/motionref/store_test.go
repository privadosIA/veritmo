package motionref

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"veritmo/internal/music/musicmotion"
	"veritmo/internal/platform/store"
)

func TestStoreKeepsEditsListsAndDeletes(t *testing.T) {
	eachStore(t, func(t *testing.T, s *Store) {
		a, err := s.Create("one", "", nil, "https://www.youtube.com/watch?v=dQw4w9WgXcQ")
		if err != nil || a.State != StateAnalysing || !ValidID(a.ID) {
			t.Fatalf("%+v %v", a, err)
		}
		time.Sleep(1100 * time.Millisecond)
		b, _ := s.Create("two", "", []string{"x"}, "")
		list := s.List()
		if len(list) != 2 || list[0].ID != b.ID {
			t.Fatalf("newest first: %+v", list)
		}
		notes := "bright and fast"
		got, err := s.Update(a.ID, nil, &notes, &[]string{"neon", "Neon", " ", "terminal"})
		if err != nil || got.Notes != notes || len(got.Tags) != 2 || got.Title != "one" {
			t.Fatalf("%+v %v", got, err)
		}
		if _, err := s.Update(a.ID, ptr(strings.Repeat("x", MaxTitle+1)), nil, nil); err == nil || !strings.Contains(err.Error(), "title") {
			t.Fatalf("a long title is refused naming the field: %v", err)
		}
		if err := s.Delete(a.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(s.Path(a.ID)); err == nil {
			t.Fatal("the files go with the reference")
		}
		if _, err := s.Get(a.ID); err != ErrNotFound {
			t.Fatalf("%v", err)
		}
		if err := s.Save(a); err != ErrNotFound {
			t.Fatalf("a deleted reference is not brought back: %v", err)
		}
		if _, err := s.Get("../etc"); err != ErrNotFound {
			t.Fatalf("%v", err)
		}
	})
}

func TestARestartMarksTheAnalysesThatWereRunningAsFailed(t *testing.T) {
	eachStore(t, func(t *testing.T, s *Store) {
		a, _ := s.Create("a", "", nil, "")
		b, _ := s.Create("b", "", nil, "")
		b.State = StateReady
		_ = s.Save(b)
		if n := s.MarkInterrupted(); n != 1 {
			t.Fatalf("%d", n)
		}
		got, _ := s.Get(a.ID)
		if got.State != StateFailed || !strings.Contains(got.Error, "interrupted") {
			t.Fatalf("%+v", got)
		}
		if got, _ := s.Get(b.ID); got.State != StateReady {
			t.Fatal("a finished one is left alone")
		}
		if _, err := os.Stat(filepath.Join(s.Path(a.ID), "frames")); err != nil {
			t.Fatal(err) // the key frames stay a folder, whatever keeps the record
		}
	})
}

func ptr(s string) *string { return &s }

// A reference keeps its shots, its sheet and the state of their reading through a save and a load, and one written before
// they existed (no such fields) loads with none of them.
func TestStoreRoundTripsTheBreakdownAndLoadsAnOldReference(t *testing.T) {
	eachStore(t, func(t *testing.T, s *Store) {
		ref, err := s.Create("with shots", "", nil, "")
		if err != nil {
			t.Fatal(err)
		}
		yes := true
		ref.State = StateReady
		ref.Analysis = &Analysis{Duration: 6, Cuts: 1, MeanShot: 3, Shots: []Shot{
			{Index: 0, Start: 0, End: 3, Motion: 0.1, Colour: "#ff0000"},
			{Index: 1, Start: 3, End: 6, Motion: 0.5, Colour: "#0000ff", BeatCut: &yes, Read: &ShotReading{Subject: "typography", Text: "word", TextSize: "huge", Entrance: "word", Camera: "static"}},
		}}
		ref.Analysis.Rhythm = buildRhythm(ref.Analysis.Shots, 6)
		ref.Analysis.RhythmText = RhythmText(ref.Analysis.Rhythm)
		ref.Analysis.BreakdownState = "done"
		if err := s.Save(ref); err != nil {
			t.Fatal(err)
		}
		got, err := s.Get(ref.ID)
		if err != nil || len(got.Analysis.Shots) != 2 || got.Analysis.Shots[1].Read == nil || got.Analysis.Shots[1].Read.Entrance != "word" ||
			got.Analysis.Rhythm == nil || got.Analysis.RhythmText != ref.Analysis.RhythmText || got.Analysis.BreakdownState != "done" || *got.Analysis.Shots[1].BeatCut != true {
			t.Fatalf("%+v %v", got.Analysis, err)
		}
		old := Reference{ID: ref.ID, Title: "old", State: StateReady, Tags: []string{}, CreatedAt: ref.CreatedAt, Analysis: &Analysis{Duration: 6, Cuts: 1, MeanShot: 3}}
		if err := s.Save(old); err != nil {
			t.Fatal(err)
		}
		raw := rawReference(t, s, ref.ID)
		for _, field := range []string{`"shots"`, `"rhythm"`, `"rhythm_text"`, `"breakdown_state"`} {
			if strings.Contains(string(raw), field) {
				t.Fatalf("an analysis without them writes no %s: %s", field, raw)
			}
		}
		back, err := s.Get(ref.ID)
		if err != nil || back.Analysis.Shots != nil || back.Analysis.Rhythm != nil {
			t.Fatalf("%+v %v", back.Analysis, err)
		}
	})
}

// eachStore runs a test on both implementations of the library: the folders and the database.
func eachStore(t *testing.T, fn func(t *testing.T, s *Store)) {
	t.Helper()
	t.Run("folder", func(t *testing.T) { fn(t, NewStore(t.TempDir())) })
	t.Run("database", func(t *testing.T) { fn(t, NewStoreDB(t.TempDir(), memStore(t))) })
}

func memStore(t *testing.T) *store.Store {
	t.Helper()
	reg := store.NewRegistry()
	RegisterStore(reg, Folders{})
	st, err := store.OpenMemory(reg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	return st
}

// rawReference is the record of a reference as it is kept: the file, or the document of its row.
func rawReference(t *testing.T, s *Store, id string) []byte {
	t.Helper()
	if s.db != nil {
		var r referenceRow
		if err := s.db.DB.Where("id = ?", id).Take(&r).Error; err != nil {
			t.Fatal(err)
		}
		return []byte(r.Doc)
	}
	raw, _ := os.ReadFile(filepath.Join(s.Path(id), "reference.json"))
	return raw
}

func TestPresetStore_KeepsListsReplacesAndDeletesOnBothImplementations(t *testing.T) {
	for name, s := range map[string]*PresetStore{"folder": NewPresetStore(t.TempDir()), "database": NewPresetStoreDB(memStore(t))} {
		t.Run(name, func(t *testing.T) {
			a, err := s.Add(musicmotion.Preset{Name: "neon", Parts: []string{"p1"}})
			if err != nil || !ValidPresetID(a.ID) || a.CreatedAt.IsZero() {
				t.Fatalf("%+v %v", a, err)
			}
			time.Sleep(1100 * time.Millisecond)
			b, _ := s.Add(musicmotion.Preset{Name: "calm"})
			list := s.List()
			if len(list) != 2 || list[0].ID != b.ID || list[1].Parts[0] != "p1" || list[0].Parts == nil || list[0].Adjustments == nil {
				t.Fatalf("newest first, with no nil lists: %+v", list)
			}
			a.Name = "neon two"
			if err := s.Replace(a); err != nil {
				t.Fatal(err)
			}
			if got, _ := s.Get(a.ID); got.Name != "neon two" {
				t.Fatalf("%+v", got)
			}
			if err := s.Replace(musicmotion.Preset{ID: "preset-20260101-000000-aaaaaa"}); err != ErrPresetNotFound {
				t.Fatalf("a style that is not there is not made by Replace: %v", err)
			}
			if err := s.Delete(a.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Get(a.ID); err != ErrPresetNotFound {
				t.Fatalf("%v", err)
			}
			if err := s.Delete(a.ID); err != ErrPresetNotFound {
				t.Fatalf("%v", err)
			}
		})
	}
}
