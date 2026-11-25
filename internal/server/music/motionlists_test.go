package music

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"veritmo/internal/music/motionref"
	"veritmo/internal/music/musicmotion"
)

type listed struct {
	code  int
	ids   []string
	total string
	link  string
}

// idsOf calls GET path and reads the ids of the items under key.
func idsOf(t *testing.T, s *testServer, path, key string) listed {
	t.Helper()
	rec := call(s.Handler(), http.MethodGet, path, "")
	out := listed{code: rec.Code, total: rec.Header().Get("X-Total-Count"), link: rec.Header().Get("Link")}
	var body map[string][]map[string]any
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s: %v %s", path, err, rec.Body)
		}
		for _, it := range body[key] {
			out.ids = append(out.ids, it["id"].(string))
		}
	}
	return out
}

func join(ids ...string) string { return strings.Join(ids, ",") }

func TestReferencesList_IsFilteredByStateSearchedSortedAndPaged(t *testing.T) {
	s := learnServer(t)
	st := s.Assistant.RefStore()
	mk := func(title, state string) string {
		ref, err := st.Create(title, "", nil, "")
		if err != nil {
			t.Fatal(err)
		}
		ref.State = state
		if err := st.Save(ref); err != nil {
			t.Fatal(err)
		}
		time.Sleep(1100 * time.Millisecond)
		return ref.ID
	}
	a := mk("Neon city", motionref.StateReady)
	b := mk("Calm sea", motionref.StateFailed)
	c := mk("Neon rain", motionref.StateReady)
	const base = "/api/music-motion/references"
	if l := idsOf(t, s, base, "references"); l.code != 200 || join(l.ids...) != join(c, b, a) || l.total != "" {
		t.Fatalf("no parameters answer all, as before: %+v", l)
	}
	if l := idsOf(t, s, base+"?state=ready", "references"); join(l.ids...) != join(c, a) || l.total != "" {
		t.Fatalf("only the ready ones: %+v", l)
	}
	if l := idsOf(t, s, base+"?q=NEON&sort=title", "references"); join(l.ids...) != join(a, c) || l.total != "2" {
		t.Fatalf("q in the title, sorted by it: %+v", l)
	}
	if l := idsOf(t, s, base+"?limit=1&offset=1", "references"); join(l.ids...) != b || l.total != "3" || !strings.Contains(l.link, "next") || !strings.Contains(l.link, "prev") {
		t.Fatalf("a page: %+v", l)
	}
	for _, bad := range []string{"?state=maybe", "?sort=colour", "?limit=0"} {
		if l := idsOf(t, s, base+bad, "references"); l.code != http.StatusBadRequest {
			t.Errorf("%s: %d", bad, l.code)
		}
	}
}

func TestPresetsList_IsSearchedSortedAndPaged(t *testing.T) {
	s := learnServer(t)
	st := s.gen.PresetStore()
	mk := func(name string) string {
		p, err := st.Add(musicmotion.Preset{Name: name})
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(1100 * time.Millisecond)
		return p.ID
	}
	a := mk("Neon")
	b := mk("Calm")
	c := mk("Neon two")
	const base = "/api/music-motion/presets"
	if l := idsOf(t, s, base, "presets"); l.code != 200 || join(l.ids...) != join(c, b, a) || l.total != "" {
		t.Fatalf("no parameters answer all, as before: %+v", l)
	}
	if l := idsOf(t, s, base+"?sort=name", "presets"); join(l.ids...) != join(b, a, c) || l.total != "3" {
		t.Fatalf("by name: %+v", l)
	}
	if l := idsOf(t, s, base+"?q=neon&sort=-name", "presets"); join(l.ids...) != join(c, a) || l.total != "2" {
		t.Fatalf("q in the name, descending: %+v", l)
	}
	if l := idsOf(t, s, base+"?limit=2", "presets"); join(l.ids...) != join(c, b) || !strings.Contains(l.link, "next") {
		t.Fatalf("a page: %+v", l)
	}
	if l := idsOf(t, s, base+"?sort=title", "presets"); l.code != http.StatusBadRequest {
		t.Fatalf("%d", l.code)
	}
}

func TestLearnings_AreFilteredPagedAndDeletedWithoutTouchingTheirStyle(t *testing.T) {
	s := learnServer(t)
	refA, refB := readyRef(t, s, "A"), readyRef(t, s, "B")
	st := s.mHandler.learningStore()
	a, _ := st.Create(refA.ID, musicmotion.StrictnessNormal)
	time.Sleep(1100 * time.Millisecond)
	b, _ := st.Create(refB.ID, musicmotion.StrictnessNormal)
	style, err := s.gen.PresetStore().Add(musicmotion.Preset{Name: "from a"})
	if err != nil {
		t.Fatal(err)
	}
	a.State, a.Styles = motionref.LearningReady, []motionref.Learned{{ID: style.ID, Name: "from a"}}
	if err := st.Save(a); err != nil {
		t.Fatal(err)
	}
	const base = "/api/music-motion/learnings"
	if l := idsOf(t, s, base, "learnings"); join(l.ids...) != join(b.ID, a.ID) || l.total != "" {
		t.Fatalf("no parameters answer all, as before: %+v", l)
	}
	if l := idsOf(t, s, base+"?reference="+refA.ID, "learnings"); join(l.ids...) != a.ID {
		t.Fatalf("by reference: %+v", l)
	}
	if l := idsOf(t, s, base+"?reference_id="+refB.ID, "learnings"); join(l.ids...) != b.ID {
		t.Fatalf("by the old name: %+v", l)
	}
	if l := idsOf(t, s, base+"?state=running&limit=5", "learnings"); join(l.ids...) != b.ID || l.total != "1" {
		t.Fatalf("by state: %+v", l)
	}
	if l := idsOf(t, s, base+"?state=maybe", "learnings"); l.code != http.StatusBadRequest {
		t.Fatalf("%d", l.code)
	}

	// a running learning is not deleted
	if rec := call(s.Handler(), http.MethodDelete, base+"/"+b.ID, ""); rec.Code != http.StatusConflict {
		t.Fatalf("running: %d %s", rec.Code, rec.Body)
	}
	if _, err := st.Get(b.ID); err != nil {
		t.Fatal("it is still there")
	}
	// a finished one is, and its style stays
	if rec := call(s.Handler(), http.MethodDelete, base+"/"+a.ID, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("finished: %d %s", rec.Code, rec.Body)
	}
	if l := idsOf(t, s, base, "learnings"); join(l.ids...) != b.ID {
		t.Fatalf("gone from the list: %+v", l)
	}
	if _, err := s.gen.PresetStore().Get(style.ID); err != nil {
		t.Fatalf("the style it proposed is still there: %v", err)
	}
	if rec := call(s.Handler(), http.MethodDelete, base+"/"+a.ID, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown: %d", rec.Code)
	}
}
