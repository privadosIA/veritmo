package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"

	"veritmo/internal/music/motionref"
	"veritmo/internal/music/musicmotion"
	"veritmo/internal/video/motion"
)

// presetWriter is an assistant that answers with a fixed text.
type presetWriter string

func (w presetWriter) Write(context.Context, string, string, int) (string, error) {
	return string(w), nil
}
func (w presetWriter) Model() string { return "fake" }

// presetServer is a refServer whose analyses give a palette and a description, and whose assistant is replaced by
// answer ("" is no assistant).
func presetServer(t *testing.T, answer string) *Server {
	t.Helper()
	s, _ := refServer(t, "30", nil)
	s.assistant.RefAnalyse = func(ctx context.Context, video, framesDir string) (*motionref.Analysis, error) {
		return &motionref.Analysis{Duration: 30, FPS: 30, Width: 1280, Height: 720, Frames: []string{}, Cuts: 6, MeanShot: 4.3,
			Colours: []motionref.Colour{{Hex: "#07120a", Share: 0.7}, {Hex: "#22e07a", Share: 0.2}, {Hex: "#d8f5e0", Share: 0.1}}, Thirds: [][]motionref.Colour{},
			Description: "A dark terminal screen in monospace with a neon green glow."}, nil
	}
	s.motionWriter = func(context.Context) (motion.Writer, motion.ModelInfo, error) {
		if answer == "" {
			return nil, motion.ModelInfo{}, errors.New("no model in the tests")
		}
		return presetWriter(answer), motion.ModelInfo{}, nil
	}
	return s
}

func readyReference(t *testing.T, s *Server) motionref.Reference {
	t.Helper()
	_, ref := addReference(t, s, `{"url":"`+goodURL+`","title":"Terminal"}`)
	s.assistant.WaitReferences()
	_, ref = getReference(t, s, ref.ID)
	if ref.State != motionref.StateReady {
		t.Fatalf("%+v", ref)
	}
	return ref
}

func newPreset(t *testing.T, s *Server, body string) (int, musicmotion.Preset) {
	t.Helper()
	rec := call(s.Handler(), http.MethodPost, "/api/music-motion/presets", body)
	var p musicmotion.Preset
	_ = json.Unmarshal(rec.Body.Bytes(), &p)
	return rec.Code, p
}

func TestPresets_AStyleIsDrawnFromAReferenceWithNoAssistant(t *testing.T) {
	s := presetServer(t, "")
	ref := readyReference(t, s)
	code, p := newPreset(t, s, `{"reference_id":"`+ref.ID+`"}`)
	if code != http.StatusCreated || p.Name != "Terminal" || p.ReferenceID != ref.ID || len(p.Parts) != 0 || p.Rhythm != 4.3 {
		t.Fatalf("%d %+v", code, p)
	}
	if p.Design.Validate() != nil || p.Design.Display != "mono" || p.Design.VerseStyle != musicmotion.VerseStyleGlow {
		t.Fatalf("%+v", p.Design)
	}
	code, p = newPreset(t, s, `{"reference_id":"`+ref.ID+`","name":"Mine"}`)
	if code != http.StatusCreated || p.Name != "Mine" {
		t.Fatalf("%d %+v", code, p)
	}
}

func TestPresets_TheAssistantChoosesPartsAndAnUnknownOneIsLeftOut(t *testing.T) {
	s := presetServer(t, `{"parts":["phosphor-terminal","nebula","not-a-part"]}`)
	ref := readyReference(t, s)
	code, p := newPreset(t, s, `{"reference_id":"`+ref.ID+`"}`)
	if code != http.StatusCreated || len(p.Parts) != 2 || p.Parts[0] != "phosphor-terminal" {
		t.Fatalf("%d %+v", code, p.Parts)
	}
}

func TestPresets_AReferenceThatIsNotReadyOrNotThereIsRefused(t *testing.T) {
	block := make(chan struct{})
	s, _ := refServer(t, "30", block)
	_, ref := addReference(t, s, `{"url":"`+goodURL+`"}`)
	for _, body := range []string{`{"reference_id":"` + ref.ID + `"}`, `{"reference_id":"ref-nope"}`, `{}`} {
		if code, _ := newPreset(t, s, body); code != http.StatusUnprocessableEntity {
			t.Fatalf("%s: %d", body, code)
		}
	}
	close(block)
	s.assistant.WaitReferences()
	if list := s.generation.PresetStore().List(); len(list) != 0 {
		t.Fatalf("nothing is created: %+v", list)
	}
}

func TestPresets_EditListAndDelete(t *testing.T) {
	s := presetServer(t, "")
	ref := readyReference(t, s)
	_, p := newPreset(t, s, `{"reference_id":"`+ref.ID+`"}`)
	put := func(mutate func(m map[string]any)) (int, string) {
		var m map[string]any
		b, _ := json.Marshal(p)
		_ = json.Unmarshal(b, &m)
		mutate(m)
		out, _ := json.Marshal(m)
		rec := call(s.Handler(), http.MethodPut, "/api/music-motion/presets/"+p.ID, string(out))
		return rec.Code, rec.Body.String()
	}
	// An accent with no contrast is refused naming the field.
	code, body := put(func(m map[string]any) { m["design"].(map[string]any)["accent"] = p.Design.Dark })
	if code != http.StatusUnprocessableEntity || !strings.Contains(body, "design.accent") {
		t.Fatalf("%d %s", code, body)
	}
	if code, body := put(func(m map[string]any) { m["rhythm"] = 99 }); code != 422 || !strings.Contains(body, "rhythm") {
		t.Fatalf("%d %s", code, body)
	}
	if code, body := put(func(m map[string]any) { m["parts"] = []string{"nope"} }); code != 422 || !strings.Contains(body, "parts[0]") {
		t.Fatalf("%d %s", code, body)
	}
	if code, body := put(func(m map[string]any) { m["name"] = "Renamed"; m["rhythm"] = 6.5; m["parts"] = []string{"nebula"} }); code != 200 || !strings.Contains(body, "Renamed") {
		t.Fatalf("%d %s", code, body)
	}
	got, _ := s.generation.PresetStore().Get(p.ID)
	if got.Name != "Renamed" || got.Rhythm != 6.5 || len(got.Parts) != 1 || got.ReferenceID != ref.ID {
		t.Fatalf("%+v", got)
	}
	rec := call(s.Handler(), http.MethodGet, "/api/music-motion/presets", "")
	var list struct{ Presets []musicmotion.Preset }
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list.Presets) != 1 {
		t.Fatalf("%s", rec.Body)
	}
	if rec := call(s.Handler(), http.MethodDelete, "/api/music-motion/presets/"+p.ID, ""); rec.Code != 204 {
		t.Fatalf("%d", rec.Code)
	}
	for _, m := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		if rec := call(s.Handler(), m, "/api/music-motion/presets/"+p.ID, `{}`); rec.Code != 404 {
			t.Fatalf("%s: %d", m, rec.Code)
		}
	}
}

func TestPresets_AStyleStaysWhenItsReferenceGoes(t *testing.T) {
	s := presetServer(t, "")
	ref := readyReference(t, s)
	_, p := newPreset(t, s, `{"reference_id":"`+ref.ID+`"}`)
	if rec := call(s.Handler(), http.MethodDelete, "/api/music-motion/references/"+ref.ID, ""); rec.Code != 204 {
		t.Fatalf("%d", rec.Code)
	}
	if _, err := os.Stat(s.assistant.RefStore().Path(ref.ID)); err == nil {
		t.Fatal("the reference is gone")
	}
	if got, err := s.generation.PresetStore().Get(p.ID); err != nil || got.ReferenceID != ref.ID {
		t.Fatalf("%v %+v", err, got)
	}
}

func TestPresets_AStyleWithNoReferenceIsMadeFromItsFields(t *testing.T) {
	s := presetServer(t, "")
	ref := readyReference(t, s)
	_, base := newPreset(t, s, `{"reference_id":"`+ref.ID+`"}`)
	body := func(mutate func(m map[string]any)) string {
		m := map[string]any{"name": "Scratch", "design": base.Design, "rhythm": 6, "parts": []string{"nebula"}}
		if mutate != nil {
			mutate(m)
		}
		out, _ := json.Marshal(m)
		return string(out)
	}
	code, p := newPreset(t, s, body(nil))
	if code != http.StatusCreated || p.Name != "Scratch" || p.ReferenceID != "" || p.Rhythm != 6 || len(p.Parts) != 1 || p.ID == "" {
		t.Fatalf("%d %+v", code, p)
	}
	if got, err := s.generation.PresetStore().Get(p.ID); err != nil || got.Name != "Scratch" {
		t.Fatalf("%+v %v", got, err)
	}
	// Refused, naming the field, and nothing stored.
	before := len(s.generation.PresetStore().List())
	rec := call(s.Handler(), http.MethodPost, "/api/music-motion/presets", body(func(m map[string]any) {
		d := base.Design
		d.Accent = d.Dark
		m["design"] = d
	}))
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "design.accent") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := call(s.Handler(), http.MethodPost, "/api/music-motion/presets", body(func(m map[string]any) { m["name"] = "" })); rec.Code != 422 {
		t.Fatalf("no name: %d", rec.Code)
	}
	if rec := call(s.Handler(), http.MethodPost, "/api/music-motion/presets", body(func(m map[string]any) { delete(m, "design") })); rec.Code != 422 {
		t.Fatalf("no design: %d", rec.Code)
	}
	if rec := call(s.Handler(), http.MethodPost, "/api/music-motion/presets", body(func(m map[string]any) { m["reference_id"] = ref.ID })); rec.Code != 422 {
		t.Fatalf("mixed: %d", rec.Code)
	}
	if after := len(s.generation.PresetStore().List()); after != before {
		t.Fatalf("stored %d, want %d", after, before)
	}
}
