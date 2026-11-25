package musicmotion

import (
	"context"
	"os"
	"testing"

	"veritmo/internal/platform/store"
)

func TestTheRenderHistoryKeepsTheLastTenOnBothImplementations(t *testing.T) {
	for name, h := range map[string]HistoryStore{"file": DirHistory(t.TempDir()), "database": DBHistory(memStore(t))} {
		t.Run(name, func(t *testing.T) {
			if len(h.Ratios()) != 0 {
				t.Fatal("empty at first")
			}
			for i := 1; i <= 12; i++ {
				if err := h.Add(float64(i)); err != nil {
					t.Fatal(err)
				}
			}
			got := h.Ratios()
			if len(got) != historyKeep || got[0] != 3 || got[historyKeep-1] != 12 {
				t.Fatalf("the last ten, the oldest first: %v", got)
			}
		})
	}
	if DirHistory("") != nil {
		t.Fatal("no folder, no history")
	}
}

func TestTheRenderTimesFileIsImportedAndGivesTheSameEstimate(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(HistoryPath(dir), []byte(`{"ratios":[1.5,2,2.5]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := store.NewRegistry()
	RegisterStore(reg, Folders{State: dir})
	st, err := store.OpenMemory(reg)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	recs, err := st.RunImports(context.Background(), "music-motion/render-times")
	if err != nil || recs[0].Created != 3 {
		t.Fatalf("%+v %v", recs, err)
	}
	cat := Catalog{Builtin: BuiltinParts()}
	script := Script{Pieces: []Piece{{Start: 0, End: 4, Part: "toggle"}}}
	fromFile := EstimateRender(script, cat, 30, 1280, 720, DirHistory(dir))
	fromDB := EstimateRender(script, cat, 30, 1280, 720, DBHistory(st))
	if fromFile != fromDB || !fromDB.Corrected || fromDB.Factor != 2 {
		t.Fatalf("the same estimate: %+v %+v", fromFile, fromDB)
	}
	if recs, _ := st.RunImports(context.Background(), "music-motion/render-times"); recs[0].Created != 0 || recs[0].Existing != 3 {
		t.Fatalf("a second run creates nothing: %+v", recs)
	}
	if _, err := os.Stat(HistoryPath(dir)); err != nil {
		t.Fatal("the file is untouched")
	}
}
