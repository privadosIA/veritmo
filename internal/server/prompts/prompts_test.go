package prompts

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	pstore "veritmo/internal/platform/store"
	store "veritmo/internal/prompts"
)

// fakeHost is a server over a database in memory: db is its store of presets, and a second handle on the same database (store.NewDB(st)) plays the
// command that fills it from another process.
type fakeHost struct {
	st    *pstore.Store
	db    store.Store
	adult bool
}

func (f *fakeHost) Presets() store.Store { return f.db }
func (f *fakeHost) ShowsAdult() bool     { return f.adult }

func newHost(t *testing.T) *fakeHost {
	t.Helper()
	reg := pstore.NewRegistry()
	store.RegisterStore(reg, t.TempDir())
	st, err := pstore.OpenMemory(reg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	return &fakeHost{st: st, db: store.NewDB(st)}
}

func mux(h *Handler) *http.ServeMux {
	m := http.NewServeMux()
	h.Register(func(p string, f http.HandlerFunc) { m.HandleFunc(p, f) })
	return m
}

func call(t *testing.T, m *http.ServeMux, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, httptest.NewRequest(method, path, &buf))
	return rec
}

type listAnswer struct {
	Items []Preset `json:"items"`
	Total int      `json:"total"`
}

func list(t *testing.T, m *http.ServeMux, query string) listAnswer {
	t.Helper()
	rec := call(t, m, http.MethodGet, "/api/prompts"+query, nil)
	var a listAnswer
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &a) != nil {
		t.Fatalf("%s: %d %s", query, rec.Code, rec.Body)
	}
	return a
}

func bundle(items ...map[string]any) map[string]any {
	return map[string]any{"format": "prompt-presets", "version": 1, "items": items}
}

func TestPrompts_OwnPresetsSurviveARestartAndCanBeDeleted(t *testing.T) {
	host := newHost(t)
	m := mux(New(host))
	rec := call(t, m, http.MethodPost, "/api/prompts", map[string]any{"kind": "image", "title": "Neon", "prompt": "neon street at night", "tags": []string{"Night", " "}})
	var p Preset
	_ = json.Unmarshal(rec.Body.Bytes(), &p)
	if rec.Code != http.StatusCreated || !p.Own || p.Source != "user" || p.ID == "" || len(p.Tags) != 1 || p.Tags[0] != "night" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	m2 := mux(New(host)) // the server restarts
	if a := list(t, m2, "?kind=image"); a.Total != 1 || a.Items[0].ID != p.ID {
		t.Fatalf("%+v", a)
	}
	if rec := call(t, m2, http.MethodDelete, "/api/prompts/"+p.ID, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("%d", rec.Code)
	}
	if a := list(t, mux(New(host)), ""); a.Total != 0 || a.Items == nil {
		t.Fatalf("%+v", a)
	}
	if rec := call(t, m2, http.MethodDelete, "/api/prompts/"+p.ID, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("%d", rec.Code)
	}
}

func TestPrompts_BadPresetsAreRefused(t *testing.T) {
	m := mux(New(newHost(t)))
	for _, body := range []map[string]any{
		{"kind": "audio", "prompt": "x"}, {"kind": "image", "prompt": " "}, {"kind": "image", "prompt": strings.Repeat("a", 2001)},
	} {
		if rec := call(t, m, http.MethodPost, "/api/prompts", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%v: %d", body, rec.Code)
		}
	}
}

func TestPrompts_ImportAddsSkipsAndRefuses(t *testing.T) {
	m := mux(New(newHost(t)))
	good := map[string]any{"kind": "video", "prompt": "slow dolly", "source": "civitai", "source_id": "42", "licence": "CC0"}
	b := bundle(good,
		map[string]any{"kind": "video", "prompt": "no licence", "source": "x"},
		map[string]any{"kind": "video", "prompt": "no kind of this sort", "source": "x", "licence": "MIT", "source_id": "1"},
		map[string]any{"kind": "image", "prompt": strings.Repeat("a", 2001), "source": "x", "licence": "MIT"},
		map[string]any{"kind": "audio", "prompt": "bad kind", "source": "x", "licence": "MIT"})
	// a kind that exists but a first one already there is "already"
	rec := call(t, m, http.MethodPost, "/api/prompts/import", b)
	var a struct {
		Added, Already int
		Refused        []refusal
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &a)
	if rec.Code != http.StatusOK || a.Added != 2 || a.Already != 0 || len(a.Refused) != 3 || a.Refused[0].Index != 1 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	rec = call(t, m, http.MethodPost, "/api/prompts/import", bundle(good))
	_ = json.Unmarshal(rec.Body.Bytes(), &a)
	if a.Added != 0 || a.Already != 1 {
		t.Fatalf("%s", rec.Body)
	}
	// bundled presets cannot be deleted
	id := list(t, m, "?kind=video&q=dolly").Items[0].ID
	if rec := call(t, m, http.MethodDelete, "/api/prompts/"+id, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("%d", rec.Code)
	}
	if list(t, m, "?kind=video&q=dolly").Items[0].Own {
		t.Fatal("imported presets are not own")
	}
	for _, body := range []map[string]any{{"format": "other", "version": 1, "items": []any{}}, {"format": "prompt-presets", "version": 2, "items": []any{}}} {
		if rec := call(t, m, http.MethodPost, "/api/prompts/import", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%v: %d", body, rec.Code)
		}
	}
}

func TestPrompts_SearchByTextTagsAndKind(t *testing.T) {
	m := mux(New(newHost(t)))
	mk := func(i string, kind, title, prompt string, tags ...string) map[string]any {
		return map[string]any{"kind": kind, "title": title, "prompt": prompt, "tags": tags, "source": "s", "source_id": i, "licence": "MIT"}
	}
	call(t, m, http.MethodPost, "/api/prompts/import", bundle(
		mk("1", "image", "Neon city", "a street", "cinematic", "night"),
		mk("2", "image", "A street", "neon signs in the rain", "night"),
		mk("3", "image", "Forest", "tall trees", "cinematic"),
		mk("4", "video", "Neon video", "neon", "night")))
	if a := list(t, m, "?kind=image&q=NEON"); a.Total != 2 || a.Items[0].Title != "Neon city" {
		t.Fatalf("the title match goes first: %+v", a)
	}
	if a := list(t, m, "?kind=image&tags=cinematic,night"); a.Total != 1 || a.Items[0].Title != "Neon city" {
		t.Fatalf("%+v", a)
	}
	if a := list(t, m, "?q=neon&limit=1"); a.Total != 3 || len(a.Items) != 1 {
		t.Fatalf("%+v", a)
	}
	for _, q := range []string{"?kind=audio", "?limit=0", "?limit=x"} {
		if rec := call(t, m, http.MethodGet, "/api/prompts"+q, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", q, rec.Code)
		}
	}
}

func TestPrompts_ACensoredServerHidesNSFW(t *testing.T) {
	host := newHost(t)
	m := mux(New(host))
	call(t, m, http.MethodPost, "/api/prompts/import", bundle(
		map[string]any{"kind": "image", "prompt": "plain", "source": "s", "licence": "MIT", "source_id": "1"},
		map[string]any{"kind": "image", "prompt": "adult", "source": "s", "licence": "MIT", "source_id": "2", "nsfw": true}))
	if a := list(t, m, ""); a.Total != 1 || a.Items[0].Prompt != "plain" {
		t.Fatalf("%+v", a)
	}
	host.adult = true
	if a := list(t, m, ""); a.Total != 2 {
		t.Fatalf("%+v", a)
	}
}

func TestPrompts_APresetWrittenByAnotherProcessIsListedWithoutARestart(t *testing.T) {
	host := newHost(t)
	m := mux(New(host))
	if a := list(t, m, ""); a.Total != 0 {
		t.Fatalf("%+v", a)
	}
	// another process (the fetch command) writes presets to the database the server uses
	other := store.NewDB(host.st)
	var written []*store.Preset
	for i, text := range []string{"first", "second"} {
		written = append(written, &store.Preset{ID: store.NewID(), Kind: "video", Title: text, Prompt: text + " prompt", Source: "x", SourceID: string(rune('a' + i)), Licence: "MIT", CreatedAt: "2026-11-19T10:00:00Z"})
	}
	if err := other.Save(written); err != nil {
		t.Fatal(err)
	}
	a := list(t, m, "")
	if a.Total != 2 || a.Items[0].Own {
		t.Fatalf("what the command wrote is listed, as bundled: %+v", a)
	}
	// and it counts as already imported
	rec := call(t, m, http.MethodPost, "/api/prompts/import", bundle(map[string]any{"kind": "video", "prompt": "first prompt", "source": "x", "source_id": "a", "licence": "MIT"}))
	if !strings.Contains(rec.Body.String(), `"already":1`) {
		t.Fatalf("%s", rec.Body)
	}
	// a preset removed by the other process goes away
	if err := other.Remove(a.Items[0].ID); err != nil {
		t.Fatal(err)
	}
	if a := list(t, m, ""); a.Total != 1 {
		t.Fatalf("%+v", a)
	}
}

func TestPrompts_APageOfASearchWithItsTotalInTheHeader(t *testing.T) {
	host := newHost(t)
	m := mux(New(host))
	var items []map[string]any
	for i := 0; i < 120; i++ {
		kind, title := "image", fmt.Sprintf("Neon %03d", i)
		if i%4 == 3 {
			kind, title = "video", fmt.Sprintf("Other %03d", i)
		}
		items = append(items, map[string]any{"kind": kind, "title": title, "prompt": "a prompt with neon", "source": "s", "source_id": fmt.Sprint(i), "licence": "MIT"})
	}
	if rec := call(t, m, http.MethodPost, "/api/prompts/import", bundle(items...)); rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	rec := call(t, m, http.MethodGet, "/api/prompts?kind=image&q=neon&limit=50", nil)
	var a listAnswer
	_ = json.Unmarshal(rec.Body.Bytes(), &a)
	if len(a.Items) != 50 || a.Total != 90 || rec.Header().Get("X-Total-Count") != "90" || !strings.Contains(rec.Header().Get("Link"), `rel="next"`) {
		t.Fatalf("%d items, total %d, header %q, link %q", len(a.Items), a.Total, rec.Header().Get("X-Total-Count"), rec.Header().Get("Link"))
	}
	for _, p := range a.Items {
		if p.Kind != "image" || !strings.Contains(strings.ToLower(p.Title+p.Prompt), "neon") {
			t.Fatalf("%+v", p)
		}
	}
	// the second page does not repeat the first; sorted by title the order is total
	first := list(t, m, "?kind=image&sort=title&limit=40")
	second := list(t, m, "?kind=image&sort=title&limit=40&offset=40")
	if first.Items[0].Title != "Neon 000" || second.Items[0].Title <= first.Items[39].Title || len(second.Items) != 40 {
		t.Fatalf("%q %q", first.Items[39].Title, second.Items[0].Title)
	}
	if desc := list(t, m, "?kind=image&sort=-title&limit=1"); desc.Items[0].Title != "Neon 118" {
		t.Fatalf("%+v", desc.Items)
	}
	// no parameter: the page it always was (50, no header); a limit above 200 is 200
	rec = call(t, m, http.MethodGet, "/api/prompts", nil)
	_ = json.Unmarshal(rec.Body.Bytes(), &a)
	if len(a.Items) != 50 || a.Total != 120 || rec.Header().Get("X-Total-Count") != "" {
		t.Fatalf("%d %d %q", len(a.Items), a.Total, rec.Header().Get("X-Total-Count"))
	}
	if all := list(t, m, "?limit=500"); len(all.Items) != 120 {
		t.Fatalf("%d", len(all.Items))
	}
	for _, q := range []string{"?sort=prompt", "?offset=-1", "?offset=x"} {
		if rec := call(t, m, http.MethodGet, "/api/prompts"+q, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", q, rec.Code)
		}
	}
	// the characters of a LIKE are searched as text
	call(t, m, http.MethodPost, "/api/prompts", map[string]any{"kind": "image", "prompt": "100% real_deal"})
	if a := list(t, m, "?q=100%25+real_"); a.Total != 1 {
		t.Fatalf("%+v", a)
	}
}

func TestPrompts_OnePresetReadAndChanged(t *testing.T) {
	host := newHost(t)
	m := mux(New(host))
	rec := call(t, m, http.MethodPost, "/api/prompts", map[string]any{"kind": "image", "title": "Mine", "prompt": "first", "tags": []string{"a"}})
	var mine Preset
	_ = json.Unmarshal(rec.Body.Bytes(), &mine)
	call(t, m, http.MethodPost, "/api/prompts/import", bundle(map[string]any{"kind": "image", "prompt": "bundled", "source": "s", "source_id": "1", "licence": "MIT"}))
	bundled := list(t, m, "?q=bundled").Items[0]

	if rec := call(t, m, http.MethodGet, "/api/prompts/"+mine.ID, nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"prompt":"first"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := call(t, m, http.MethodGet, "/api/prompts/p-nope", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("%d", rec.Code)
	}
	rec = call(t, m, http.MethodPut, "/api/prompts/"+mine.ID, map[string]any{"title": "Renamed", "prompt": "second", "tags": []string{"B", "c"}, "nsfw": true})
	var got Preset
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if rec.Code != http.StatusOK || got.ID != mine.ID || got.Title != "Renamed" || got.Prompt != "second" || len(got.Tags) != 2 || got.Tags[0] != "b" || !got.NSFW ||
		!got.Own || got.Source != "user" || got.CreatedAt != mine.CreatedAt || got.Kind != "image" {
		t.Fatalf("%d %+v", rec.Code, got)
	}
	// the change is what the list shows
	host.adult = true
	if a := list(t, m, "?q=second"); a.Total != 1 || a.Items[0].Title != "Renamed" {
		t.Fatalf("%+v", a)
	}
	// a bundled preset is not changed
	if rec := call(t, m, http.MethodPut, "/api/prompts/"+bundled.ID, map[string]any{"prompt": "changed"}); rec.Code != http.StatusForbidden {
		t.Fatalf("%d", rec.Code)
	}
	if after, _ := host.db.Get(bundled.ID); after.Prompt != "bundled" {
		t.Fatalf("%+v", after)
	}
	for name, body := range map[string]map[string]any{"empty prompt": {"prompt": " "}, "other kind": {"kind": "video", "prompt": "x"}, "bad kind": {"kind": "audio", "prompt": "x"}, "long": {"prompt": strings.Repeat("a", 2001)}} {
		if rec := call(t, m, http.MethodPut, "/api/prompts/"+mine.ID, body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", name, rec.Code)
		}
	}
	if rec := call(t, m, http.MethodPut, "/api/prompts/p-nope", map[string]any{"prompt": "x"}); rec.Code != http.StatusNotFound {
		t.Fatalf("%d", rec.Code)
	}
	// a censored server does not know a preset marked nsfw
	host.adult = false
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		if rec := call(t, m, method, "/api/prompts/"+mine.ID, map[string]any{"prompt": "x"}); rec.Code != http.StatusNotFound {
			t.Errorf("%s of a hidden preset: %d", method, rec.Code)
		}
	}
}

func TestPrompts_TheBestMatchComesFirstAndTagsAreMatchedWhole(t *testing.T) {
	m := mux(New(newHost(t)))
	mk := func(i, title, prompt string, tags ...string) map[string]any {
		return map[string]any{"kind": "image", "title": title, "prompt": prompt, "tags": tags, "source": "s", "source_id": i, "licence": "MIT"}
	}
	call(t, m, http.MethodPost, "/api/prompts/import", bundle(
		mk("1", "Quiet", "a neon sign", "x"),
		mk("2", "Neon", "nothing", "y"),
		mk("3", "Other", "nothing", "neons"),
		mk("4", "Cats", "cat", "cat", "cats"),
		mk("5", "Dogs", "dog", "dog")))
	a := list(t, m, "?q=neon")
	if a.Total != 3 || a.Items[0].Title != "Neon" || a.Items[1].Title != "Other" || a.Items[2].Title != "Quiet" {
		t.Fatalf("title 3, tag 2, prompt 1: %+v", a.Items)
	}
	// a tag is matched whole: cat is not cats
	if a := list(t, m, "?tags=cat"); a.Total != 1 || a.Items[0].Title != "Cats" {
		t.Fatalf("%+v", a.Items)
	}
	if a := list(t, m, "?tags=ca"); a.Total != 0 {
		t.Fatalf("%+v", a.Items)
	}
}

func TestPrompts_TheKindModel3DIsOneMoreKind(t *testing.T) {
	m := mux(New(newHost(t)))
	rec := call(t, m, http.MethodPost, "/api/prompts", map[string]any{"kind": "model3d", "title": "Skate", "prompt": "a glossy pink roller skate, studio render", "tags": []string{"props"}})
	var p Preset
	_ = json.Unmarshal(rec.Body.Bytes(), &p)
	if rec.Code != http.StatusCreated || p.Kind != "model3d" || !p.Own {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if a := list(t, m, "?kind=model3d&q=skate"); a.Total != 1 || a.Items[0].ID != p.ID {
		t.Errorf("search: %+v", a)
	}
	if a := list(t, m, "?kind=model3d&tags=props"); a.Total != 1 {
		t.Errorf("by tag: %+v", a)
	}
	if a := list(t, m, "?kind=image"); a.Total != 0 {
		t.Errorf("not in another kind: %+v", a)
	}
	// update and delete like the others; the kind cannot change
	if rec := call(t, m, http.MethodPut, "/api/prompts/"+p.ID, map[string]any{"prompt": "a glossy pink roller skate on its own"}); rec.Code != http.StatusOK {
		t.Errorf("update: %d %s", rec.Code, rec.Body)
	}
	if rec := call(t, m, http.MethodPut, "/api/prompts/"+p.ID, map[string]any{"kind": "image", "prompt": "x"}); rec.Code != http.StatusBadRequest {
		t.Errorf("the kind of a preset cannot be changed: %d", rec.Code)
	}
	// a bundle with items of the kind is added; the same bundle again is already there
	b := bundle(map[string]any{"kind": "model3d", "title": "Chair", "prompt": "a wooden chair", "source": "mine", "source_id": "1", "licence": "CC0"},
		map[string]any{"kind": "model3d", "title": "Lamp", "prompt": "a brass desk lamp", "source": "mine", "source_id": "2", "licence": "CC0"})
	rec = call(t, m, http.MethodPost, "/api/prompts/import", b)
	var a struct{ Added, Already int }
	_ = json.Unmarshal(rec.Body.Bytes(), &a)
	if rec.Code != http.StatusOK || a.Added != 2 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := call(t, m, http.MethodPost, "/api/prompts/import", b); !strings.Contains(rec.Body.String(), `"already":2`) {
		t.Errorf("again: %s", rec.Body)
	}
	if got := list(t, m, "?kind=model3d"); got.Total != 3 {
		t.Errorf("the kind has its three: %+v", got)
	}
	if rec := call(t, m, http.MethodDelete, "/api/prompts/"+p.ID, nil); rec.Code != http.StatusNoContent {
		t.Errorf("delete: %d", rec.Code)
	}
	// a kind that is none lists the accepted ones, now with this one
	rec = call(t, m, http.MethodGet, "/api/prompts?kind=audio", nil)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "model3d") {
		t.Errorf("an unknown kind: %d %s", rec.Code, rec.Body)
	}
}
