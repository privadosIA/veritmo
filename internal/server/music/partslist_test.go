package music

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"veritmo/internal/music/musicmotion"
)

func addPart(t *testing.T, s *testServer, name, description string) {
	t.Helper()
	m := fmt.Sprintf(`{"name":%q,"description":%q,"version":1,"levels":["simple"],"params":[]}`, name, description)
	if err := s.mmCatalog().CreatePart([]byte(m), draftMoving, nil); err != nil {
		t.Fatal(err)
	}
}

func listParts(t *testing.T, s *testServer, query string) (*httptest.ResponseRecorder, []CatalogPart) {
	t.Helper()
	rec := call(s.Handler(), http.MethodGet, "/api/music-motion/parts"+query, "")
	var out []CatalogPart
	if rec.Code == 200 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("%v: %s", err, rec.Body)
		}
	}
	return rec, out
}

func partNames(ps []CatalogPart) string {
	var n []string
	for _, p := range ps {
		n = append(n, p.Name)
	}
	return strings.Join(n, ",")
}

func TestParts_TheListHasTheCatalogWithoutScenesAndFiltersBySourceLevelAndText(t *testing.T) {
	s := mmServer(t)
	addPart(t, s, "zz-clock", "A ticking clock")
	addPart(t, s, "aa-sunrise", "A rising sun")
	rec, all := listParts(t, s, "")
	if rec.Code != 200 || rec.Header().Get("X-Total-Count") != "" {
		t.Fatalf("a list asked with no parameter is not paged: %d %v", rec.Code, rec.Header())
	}
	if len(all) < 3 || !strings.Contains(partNames(all), "zz-clock") || !strings.Contains(partNames(all), "blank") {
		t.Fatalf("%s", partNames(all))
	}
	_, users := listParts(t, s, "?source=user")
	if partNames(users) != "aa-sunrise,zz-clock" {
		t.Fatalf("source=user: %s", partNames(users))
	}
	_, builtins := listParts(t, s, "?source=builtin")
	for _, p := range builtins {
		if p.Source != "builtin" {
			t.Fatalf("%+v", p)
		}
	}
	_, found := listParts(t, s, "?q=RISING")
	if partNames(found) != "aa-sunrise" {
		t.Fatalf("q is searched without regard to case in the name and the description: %s", partNames(found))
	}
	if _, advanced := listParts(t, s, "?source=user&level=advanced"); len(advanced) != 0 {
		t.Fatalf("level: %s", partNames(advanced))
	}
	for _, bad := range []string{"?source=nobody", "?scene=maybe", "?sort=password", "?limit=0"} {
		if rec, _ := listParts(t, s, bad); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", bad, rec.Code)
		}
	}
}

func TestParts_TheListIsPagedSortedAndCounted(t *testing.T) {
	s := mmServer(t)
	for i := 0; i < 5; i++ {
		addPart(t, s, "pp-part-"+strconv.Itoa(i), "page test")
		time.Sleep(15 * time.Millisecond) // the order of updated_at
	}
	rec, page := listParts(t, s, "?source=user&limit=2&offset=1")
	if partNames(page) != "pp-part-1,pp-part-2" || rec.Header().Get("X-Total-Count") != "5" {
		t.Fatalf("%s total %s", partNames(page), rec.Header().Get("X-Total-Count"))
	}
	if l := rec.Header().Get("Link"); !strings.Contains(l, `rel="next"`) || !strings.Contains(l, `rel="prev"`) {
		t.Fatalf("Link = %q", l)
	}
	_, newest := listParts(t, s, "?source=user&sort=-updated_at&limit=2")
	if partNames(newest) != "pp-part-4,pp-part-3" {
		t.Fatalf("newest first: %s", partNames(newest))
	}
	_, byName := listParts(t, s, "?source=user&sort=-name&limit=1")
	if partNames(byName) != "pp-part-4" {
		t.Fatalf("%s", partNames(byName))
	}
	if rec, beyond := listParts(t, s, "?source=user&offset=50"); rec.Code != 200 || len(beyond) != 0 || rec.Header().Get("X-Total-Count") != "5" {
		t.Fatalf("beyond the end: %d %d", rec.Code, len(beyond))
	}
}

func TestParts_TheScenesOfAPieceAreListedByPieceAndLeftOutByDefault(t *testing.T) {
	s := mmServer(t)
	for piece, scripts := range map[string][]string{"piece-a": {"p1", "p2"}, "piece-b": {"p1"}} {
		for _, sp := range scripts {
			name := "scene-" + piece[len(piece)-1:] + "-" + sp
			m := fmt.Sprintf(`{"name":%q,"description":"a scene","version":1,"levels":["simple"],"params":[],"scene":{"piece":%q,"script_piece":%q}}`, name, piece, sp)
			if err := s.mmCatalog().CreatePart([]byte(m), draftMoving, nil); err != nil {
				t.Fatal(err)
			}
		}
	}
	_, plain := listParts(t, s, "?source=user")
	if len(plain) != 0 {
		t.Fatalf("scenes are left out by default: %s", partNames(plain))
	}
	_, ofA := listParts(t, s, "?piece=piece-a")
	if partNames(ofA) != "scene-a-p1,scene-a-p2" || ofA[0].Scene == nil || ofA[0].Scene.Piece != "piece-a" {
		t.Fatalf("%s", partNames(ofA))
	}
	if _, all := listParts(t, s, "?scene=true"); len(all) != 3 {
		t.Fatalf("scene=true: %s", partNames(all))
	}
}

func TestParts_APartOfTheUserIsCreatedAfterTheChecksOfAnApproval(t *testing.T) {
	s, _ := draftServer(t)
	post := func(manifest, fragment string) *httptest.ResponseRecorder {
		b, _ := json.Marshal(DraftPut{Manifest: manifest, Fragment: fragment})
		return call(s.Handler(), http.MethodPost, "/api/music-motion/parts", string(b))
	}
	rec := post(draftManifest, draftMoving)
	var got CatalogPart
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if rec.Code != http.StatusCreated || got.Name != "probe" || got.Source != "user" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if p, ok := s.mmCatalog().Get("probe"); !ok || p.Fragment != draftMoving {
		t.Fatalf("the part is in the catalog: %+v %v", p, ok)
	}
	if rec := post(draftManifest, draftMoving); rec.Code != http.StatusConflict {
		t.Fatalf("a name that exists: %d %s", rec.Code, rec.Body)
	}
	if rec := post(strings.Replace(draftManifest, `"probe"`, `"blank"`, 1), draftMoving); rec.Code != http.StatusConflict {
		t.Fatalf("the name of a part that comes with the program: %d %s", rec.Code, rec.Body)
	}
	external := `<img src="https://x.invalid/a.png"><script>window.__partSeek=function(t,b){};</script>`
	if rec := post(strings.Replace(draftManifest, `"probe"`, `"external"`, 1), external); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "external") {
		t.Fatalf("a part that fails the checks: %d %s", rec.Code, rec.Body)
	}
	if _, ok := s.mmCatalog().Get("external"); ok {
		t.Fatal("nothing is kept when the checks fail")
	}
	if rec := post(strings.Replace(draftManifest, `"probe"`, `"still"`, 1), draftStill); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("a part that does not move: %d", rec.Code)
	}
	if rec := call(s.Handler(), http.MethodPost, "/api/music-motion/parts", "{not json"); rec.Code != http.StatusBadRequest {
		t.Fatalf("%d", rec.Code)
	}
}

var _ = musicmotion.LevelSimple

func TestPieceSize_IsKeptWithThePieceAndMeasuredAgainOnlyWhenItsStorageChanges(t *testing.T) {
	s := mmServer(t)
	v := newPiece(t, s, nil)
	extra := filepath.Join(s.ProjectsDir, v.ID, "out", "extra.bin")
	if err := os.MkdirAll(filepath.Dir(extra), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(extra, make([]byte, 5000), 0o644); err != nil {
		t.Fatal(err)
	}
	read := func() int64 {
		var got PieceView
		rec := call(s.Handler(), http.MethodGet, "/api/music-motion/pieces/"+v.ID, "")
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("%v %s", err, rec.Body)
		}
		return got.SizeBytes
	}
	if got := read(); got != v.SizeBytes {
		t.Fatalf("a read does not walk the folder: %d, want %d", got, v.SizeBytes)
	}
	if rec := call(s.Handler(), http.MethodGet, "/api/music-motion/pieces", ""); rec.Code != 200 {
		t.Fatal(rec.Body)
	}
	s.Stack.Workspace.StorageChanged(v.ID) // what cleaning the storage of the project does
	if got := read(); got < v.SizeBytes+5000 {
		t.Fatalf("after its storage changed the size is measured again: %d", got)
	}
	s.Stack.Workspace.StorageChanged("some-other-project") // not a piece: nothing happens
}

func TestDraftsList_IsFilteredByStateAndPieceSearchedSortedAndPaged(t *testing.T) {
	s, _ := draftServer(t)
	st := s.draftStore()
	mk := func(request, state, piece string) string {
		d, err := st.Create(request, "manual", nil)
		if err != nil {
			t.Fatal(err)
		}
		d.State, d.Name = state, strings.ReplaceAll(strings.ToLower(request), " ", "-")
		if piece != "" {
			d.Scene = &musicmotion.SceneRef{Piece: piece, ScriptPiece: "p1"}
		}
		if err := st.Save(d); err != nil {
			t.Fatal(err)
		}
		time.Sleep(1100 * time.Millisecond)
		return d.ID
	}
	a := mk("A coral dot", "ready", "")
	b := mk("A blue line", "failed", "piece-a")
	c := mk("A coral wave", "failed", "piece-a")
	type listAnswer struct {
		Drafts []musicmotion.Draft `json:"drafts"`
	}
	get := func(query string) (int, listAnswer, http.Header) {
		rec := call(s.Handler(), http.MethodGet, "/api/music-motion/part-drafts"+query, "")
		var out listAnswer
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out, rec.Header()
	}
	ids := func(l listAnswer) string {
		var o []string
		for _, d := range l.Drafts {
			o = append(o, d.ID)
		}
		return strings.Join(o, ",")
	}
	if code, all, hdr := get(""); code != 200 || ids(all) != strings.Join([]string{c, b, a}, ",") || hdr.Get("X-Total-Count") != "" {
		t.Fatalf("no parameters: %d %s %v", code, ids(all), hdr)
	}
	if _, failed, hdr := get("?state=failed"); ids(failed) != c+","+b || hdr.Get("X-Total-Count") != "" {
		t.Fatalf("a filter alone is not a page: %s %v", ids(failed), hdr)
	}
	if _, failed, hdr := get("?state=failed&limit=10"); ids(failed) != c+","+b || hdr.Get("X-Total-Count") != "2" {
		t.Fatalf("state: %s %v", ids(failed), hdr)
	}
	if _, ofPiece, _ := get("?piece=piece-a&sort=created_at"); ids(ofPiece) != b+","+c {
		t.Fatalf("piece, oldest first: %s", ids(ofPiece))
	}
	if _, found, _ := get("?q=CORAL"); ids(found) != c+","+a {
		t.Fatalf("q in the request without regard to case: %s", ids(found))
	}
	if _, byName, _ := get("?sort=-name&limit=1"); ids(byName) != c {
		t.Fatalf("sort by name: %s", ids(byName))
	}
	if _, page, hdr := get("?state=failed&limit=1&offset=1"); ids(page) != b || hdr.Get("X-Total-Count") != "2" || !strings.Contains(hdr.Get("Link"), "prev") {
		t.Fatalf("a page: %s %v", ids(page), hdr)
	}
	for _, d := range func() []musicmotion.Draft { _, l, _ := get("?limit=3"); return l.Drafts }() {
		if d.Manifest != "" || d.Fragment != "" {
			t.Fatal("a list carries no text of the files")
		}
	}
	for _, bad := range []string{"?state=maybe", "?sort=colour", "?limit=0"} {
		if code, _, _ := get(bad); code != http.StatusBadRequest {
			t.Errorf("%s: %d", bad, code)
		}
	}
}
