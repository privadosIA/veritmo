package music

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/music/hyperframes"
	"veritmo/internal/music/musicmotion"
	"veritmo/internal/video/motion"
)

// hfRegistry writes two blocks the way the registry keeps them and returns the folder.
func hfRegistry(t *testing.T) string {
	t.Helper()
	reg := t.TempDir()
	w := func(p, s string) {
		p = filepath.Join(reg, p)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	w("aurora/registry-item.json", `{"name":"aurora","title":"Aurora","description":"green curtains","tags":["sky","shader"],"license":"Apache-2.0","dimensions":{"width":320,"height":180},"duration":2,"files":[{"path":"aurora.html","type":"hyperframes:composition"}]}`)
	w("aurora/OFL.txt", "SIL Open Font License")
	w("aurora/assets/Three-LICENSE.txt", "MIT licence of three")
	w("aurora/aurora.html", `<!doctype html><html><body><div id="b" style="width:50px;height:50px;background:red"></div><script>window.__timelines={};window.__timelines.aurora={pause:function(){},seek:function(t){document.getElementById('b').style.marginLeft=(t*100)+'px'}};</script></body></html>`)
	w("title/registry-item.json", `{"name":"title","title":"Title card","tags":["text"],"dimensions":{"width":320,"height":180},"duration":3,"files":[{"path":"title.html","type":"hyperframes:composition"}]}`)
	w("title/title.html", `<html><body>hi</body></html>`)
	return reg
}

func hfServer(t *testing.T) *testServer {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	s, _ := newProjectsServer(t)
	s.mHandler.hfDir = t.TempDir()
	s.mHandler.hfLibs = t.TempDir()
	if _, err := s.mHandler.hfStore().Import(hyperframes.Source{Dir: hfRegistry(t), URL: hyperframes.RepoURL}, nil, false); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestHyperFramesCatalog_ListFiltersAndReadsBlocks(t *testing.T) {
	s := hfServer(t)
	get := func(path string) (int, []byte) {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec.Code, rec.Body.Bytes()
	}
	var list HFBlockList
	code, body := get("/api/hyperframes/blocks")
	if code != 200 || json.Unmarshal(body, &list) != nil || list.Total != 2 || list.States["unchecked"] != 2 || list.LibsInstalled {
		t.Fatalf("%d %s", code, body)
	}
	if list.Fix == "" || list.Blocks[0].Name != "aurora" || list.Blocks[0].Thumbnail != "/api/hyperframes/blocks/aurora/thumbnail" || list.Blocks[0].License != "Apache-2.0" {
		t.Fatalf("%+v", list)
	}
	for q, want := range map[string]int{"?q=green": 1, "?tag=text": 1, "?q=nothing": 0, "?state=ok": 0, "?state=unchecked": 2} {
		code, body = get("/api/hyperframes/blocks" + q)
		var l HFBlockList
		if code != 200 || json.Unmarshal(body, &l) != nil || l.Total != want {
			t.Errorf("%s: %d %s", q, code, body)
		}
	}
	var d HFBlockDetail
	code, body = get("/api/hyperframes/blocks/aurora")
	if code != 200 || json.Unmarshal(body, &d) != nil || !strings.Contains(d.HTML, "__timelines") || len(d.Files) == 0 {
		t.Fatalf("%d %s", code, body)
	}
	if len(d.Licenses) != 2 || d.License != "Apache-2.0" || d.Licenses[0].File != "OFL.txt" || !strings.Contains(d.Licenses[1].Text, "three") || d.Origin.URL == "" {
		t.Fatalf("the licence is not with the block: %+v", d.Licenses)
	}
	for _, p := range []string{"/api/hyperframes/blocks/nope", "/api/hyperframes/blocks/..%2Fx", "/api/hyperframes/blocks/NOPE"} {
		if code, _ = get(p); code != http.StatusNotFound {
			t.Errorf("%s: %d", p, code)
		}
	}
}

func TestHyperFramesCatalog_RefusesBadRequests(t *testing.T) {
	s := hfServer(t)
	for _, c := range []struct {
		method, path string
		body         any
		want         int
	}{
		{http.MethodPost, "/api/hyperframes/blocks/import", map[string]any{"dir": "relative/path"}, 422},
		{http.MethodPost, "/api/hyperframes/blocks/import", map[string]any{"dir": "/definitely/not/here"}, 422},
		{http.MethodPut, "/api/hyperframes/blocks/aurora", map[string]any{"html": "  "}, 422},
		{http.MethodPost, "/api/hyperframes/blocks/aurora/preview", map[string]any{"html": ""}, 422},
		{http.MethodPost, "/api/hyperframes/blocks/aurora/preview", map[string]any{"html": "<p>x</p>", "t": 99}, 422},
		{http.MethodGet, "/api/hyperframes/blocks/aurora/frame?t=-1", nil, 422},
		{http.MethodPut, "/api/hyperframes/blocks/nope", map[string]any{"html": "<p>x</p>"}, 404},
	} {
		if rec := doJSON(t, s.Handler(), c.method, c.path, c.body); rec.Code != c.want {
			t.Errorf("%s %s: %d %s", c.method, c.path, rec.Code, rec.Body.String())
		}
	}
}

func TestHyperFramesCatalog_DrawsEditsAndImports(t *testing.T) {
	br, err := motion.Find("")
	if err != nil {
		t.Skip("no Chrome or Chromium")
	}
	s := hfServer(t)
	s.motionBrowser = func() (motion.Browser, error) { return br, nil }

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/hyperframes/blocks/aurora/thumbnail", nil))
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/jpeg" || rec.Body.Len() < 100 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/hyperframes/blocks/aurora/frame?t=1", nil))
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}

	// an edit that asks for the network is refused and nothing changes
	bad := doJSON(t, s.Handler(), http.MethodPut, "/api/hyperframes/blocks/aurora", map[string]any{"html": `<html><body><script src="https://example.com/x.js"></script></body></html>`})
	if bad.Code != 422 || !strings.Contains(bad.Body.String(), "outside itself") {
		t.Fatalf("%d %s", bad.Code, bad.Body.String())
	}
	if b, _ := s.mHandler.hfStore().Get("aurora"); b.Edited {
		t.Fatal("a refused edit marked the block")
	}
	// a good one is kept and marks the block
	good := `<!doctype html><html><body><div id="b" style="width:80px;height:80px;background:blue"></div><script>window.__timelines={};window.__timelines.aurora={pause:function(){},seek:function(t){document.getElementById('b').style.marginLeft=(t*50)+'px'}};</script></body></html>`
	ok := doJSON(t, s.Handler(), http.MethodPut, "/api/hyperframes/blocks/aurora", map[string]any{"html": good})
	if ok.Code != 200 {
		t.Fatalf("%d %s", ok.Code, ok.Body.String())
	}
	if b, _ := s.mHandler.hfStore().Get("aurora"); !b.Edited || b.Checks == nil || !b.Checks.OK {
		t.Fatalf("%+v", b)
	}

	// an import checks what it copies and leaves the edited block alone
	imp := doJSON(t, s.Handler(), http.MethodPost, "/api/hyperframes/blocks/import", map[string]any{"dir": hfRegistry(t)})
	var ans HFImportAnswer
	if imp.Code != 200 || json.Unmarshal(imp.Body.Bytes(), &ans) != nil || ans.Report == nil || ans.Report.Total != 2 {
		t.Fatalf("%d %s", imp.Code, imp.Body.String())
	}
	for _, r := range ans.Imported {
		if r.Name == "aurora" && (r.Written || r.Skipped == "") {
			t.Errorf("the edited block was overwritten: %+v", r)
		}
	}
	if ans.Report.ByState["unchecked"] != 0 {
		t.Errorf("%+v", ans.Report.ByState)
	}
}

func TestHyperFramesCatalog_RendersAClipWithoutASong(t *testing.T) {
	br, err := motion.Find("")
	if err != nil {
		t.Skip("no Chrome or Chromium")
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg")
	}
	s := hfServer(t)
	s.motionBrowser = func() (motion.Browser, error) { return br, nil }
	for _, bad := range []map[string]any{{"duration": 500}, {"duration": -1}, {"fps": 90}} {
		if rec := doJSON(t, s.Handler(), http.MethodPost, "/api/hyperframes/blocks/aurora/render", bad); rec.Code != 422 {
			t.Errorf("%v: %d", bad, rec.Code)
		}
	}
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/hyperframes/blocks/aurora/render", map[string]any{"duration": 1, "fps": 10})
	var clip HFClip
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &clip) != nil || clip.Frames != 10 || clip.Width != 320 || !strings.HasSuffix(clip.File, ".mp4") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if st, err := os.Stat(clip.Path); err != nil || st.Size() < 500 {
		t.Fatalf("%v %v", st, err)
	}
	// the same request is the same file and is not drawn again
	again := doJSON(t, s.Handler(), http.MethodPost, "/api/hyperframes/blocks/aurora/render", map[string]any{"duration": 1, "fps": 10})
	var clip2 HFClip
	if err := json.Unmarshal(again.Body.Bytes(), &clip2); err != nil || clip2.File != clip.File {
		t.Fatalf("%s", again.Body.String())
	}
	dl := httptest.NewRecorder()
	s.Handler().ServeHTTP(dl, httptest.NewRequest(http.MethodGet, clip.URL, nil))
	if dl.Code != 200 || dl.Header().Get("Content-Type") != "video/mp4" || dl.Body.Len() < 500 {
		t.Fatalf("%d", dl.Code)
	}
	for _, p := range []string{"/api/hyperframes/clips/nope.mp4", "/api/hyperframes/clips/..%2Fblocks", "/api/hyperframes/clips/.hidden.mp4"} {
		r := httptest.NewRecorder()
		s.Handler().ServeHTTP(r, httptest.NewRequest(http.MethodGet, p, nil))
		if r.Code != http.StatusNotFound && r.Code != http.StatusMovedPermanently {
			t.Errorf("%s: %d", p, r.Code)
		}
	}
}

func TestHyperFramesCatalog_ABlockListIsPagedAndSorted(t *testing.T) {
	s := hfServer(t)
	get := func(path string) (HFBlockList, *httptest.ResponseRecorder) {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		var l HFBlockList
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &l) != nil {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body)
		}
		return l, rec
	}
	l, rec := get("/api/hyperframes/blocks?limit=1")
	if len(l.Blocks) != 1 || l.Blocks[0].Name != "aurora" || l.Total != 2 || l.States["unchecked"] != 2 || rec.Header().Get("X-Total-Count") != "2" || !strings.Contains(rec.Header().Get("Link"), `rel="next"`) {
		t.Fatalf("%+v %q %q", l, rec.Header().Get("X-Total-Count"), rec.Header().Get("Link"))
	}
	if l, _ = get("/api/hyperframes/blocks?limit=1&offset=1"); len(l.Blocks) != 1 || l.Blocks[0].Name != "title" || l.Total != 2 {
		t.Fatalf("%+v", l)
	}
	// sorted by title: "Aurora" before "Title card"; reversed, the other way
	if l, _ = get("/api/hyperframes/blocks?sort=-title"); l.Blocks[0].Name != "title" || l.Blocks[1].Name != "aurora" {
		t.Fatalf("%+v", l.Blocks)
	}
	// the totals follow the search, not the page
	if l, _ = get("/api/hyperframes/blocks?q=green&limit=5"); l.Total != 1 || l.Blocks[0].Name != "aurora" || l.States["unchecked"] != 1 {
		t.Fatalf("%+v", l)
	}
	// no parameter: everything, and no header (as it always was)
	if l, rec = get("/api/hyperframes/blocks"); len(l.Blocks) != 2 || rec.Header().Get("X-Total-Count") != "" {
		t.Fatalf("%+v", l)
	}
	for _, bad := range []string{"?sort=state", "?limit=0", "?offset=-1"} {
		if rec := doJSON(t, s.Handler(), http.MethodGet, "/api/hyperframes/blocks"+bad, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", bad, rec.Code)
		}
	}
}

func TestHyperFramesCatalog_DeletingABlockLeavesThePartMadeFromIt(t *testing.T) {
	s := hfServer(t)
	// a part that was made from the block: a part of its own, in the catalog of parts
	cat := musicmotion.NewCatalog(musicmotion.NewDBParts(s.Stack.Store))
	manifest := []byte(`{"name":"hf-aurora","description":"A part made from the aurora block","version":1,"levels":["simple"],"params":[]}`)
	if err := cat.Parts.Create(manifest, `<div>aurora</div>`, nil); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(s.mHandler.hfDir, "blocks", "aurora")
	if _, err := os.Stat(dir); err != nil {
		t.Fatal(err)
	}
	if rec := doJSON(t, s.Handler(), http.MethodDelete, "/api/hyperframes/blocks/aurora", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := doJSON(t, s.Handler(), http.MethodGet, "/api/hyperframes/blocks/aurora", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("the block is still there: %d", rec.Code)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("the folder of the block is left: %v", err)
	}
	var list HFBlockList
	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/hyperframes/blocks", nil)
	if _ = json.Unmarshal(rec.Body.Bytes(), &list); list.Total != 1 || list.Blocks[0].Name != "title" {
		t.Fatalf("the other block went too: %s", rec.Body)
	}
	if p, ok := cat.Parts.Get("hf-aurora"); !ok || p.Fragment != `<div>aurora</div>` {
		t.Fatalf("the part made from the block is gone: %+v %v", p, ok)
	}
	for _, name := range []string{"aurora", "nope", "..%2Fx", "NOPE"} {
		if rec := doJSON(t, s.Handler(), http.MethodDelete, "/api/hyperframes/blocks/"+name, nil); rec.Code != http.StatusNotFound {
			t.Errorf("DELETE %s: %d", name, rec.Code)
		}
	}
}
