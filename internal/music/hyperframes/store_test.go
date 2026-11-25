package hyperframes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"veritmo/internal/video/motion"
)

func fixture(t *testing.T) string {
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
	w("aurora/registry-item.json", `{"name":"aurora","title":"Aurora","tags":["a"],"license":"Apache-2.0","dimensions":{"width":1920,"height":1080},"duration":2,
 "files":[{"path":"aurora.html","type":"hyperframes:composition"},{"path":"lib/x.js","type":"hyperframes:asset"}],"preview":{"poster":"https://p/a.png"}}`)
	w("aurora/aurora.html", "<html>a</html>")
	w("aurora/lib/x.js", "x")
	w("bad/registry-item.json", `{"name":"bad","files":[]}`)
	return reg
}

func TestImportKeepsTheBlockWhole(t *testing.T) {
	eachStore(t, func(t *testing.T, newStore func() Store) {
		s := newStore()
		res, err := s.Import(Source{Dir: fixture(t), URL: "u", Commit: "c"}, nil, false)
		if err != nil {
			t.Fatal(err)
		}
		if len(res) != 2 || !res[0].Written || res[1].Error == "" {
			t.Fatalf("%+v", res)
		}
		b, err := s.Get("aurora")
		if err != nil || b.Width != 1920 || b.HTML != "aurora.html" || b.Origin.Commit != "c" || b.Origin.Poster == "" || b.License != "Apache-2.0" {
			t.Fatalf("%+v %v", b, err)
		}
		if got, err := s.Read("aurora", "lib/x.js"); err != nil || string(got) != "x" {
			t.Fatalf("%q %v", got, err)
		}
		if _, err := s.Read("aurora", "../bad/registry-item.json"); err == nil {
			t.Fatal("read outside the block")
		}
		if _, err := s.Read("aurora", "block.json"); err == nil {
			t.Fatal("block.json is not a file of the block")
		}
		if _, err := s.Get("nope"); err != ErrNotFound {
			t.Fatalf("%v", err)
		}
		if l, _ := s.List(); len(l) != 1 {
			t.Fatalf("%d", len(l))
		}
	})
}

func TestImportKeepsEditedBlocks(t *testing.T) {
	eachStore(t, func(t *testing.T, newStore func() Store) {
		s := newStore()
		reg := fixture(t)
		if _, err := s.Import(Source{Dir: reg}, []string{"aurora"}, false); err != nil {
			t.Fatal(err)
		}
		b, _ := s.Get("aurora")
		b.Edited = true
		if err := s.Save(b); err != nil {
			t.Fatal(err)
		}
		res, _ := s.Import(Source{Dir: reg}, []string{"aurora"}, false)
		if res[0].Written || res[0].Skipped == "" {
			t.Fatalf("%+v", res[0])
		}
		if res, _ = s.Import(Source{Dir: reg}, []string{"aurora"}, true); !res[0].Written {
			t.Fatalf("%+v", res[0])
		}
	})
}

func TestResolveDir(t *testing.T) {
	if ResolveDir(func(string) string { return "" }) != DefaultDir {
		t.Fatal("default")
	}
	if ResolveDir(func(string) string { return " /x " }) != "/x" {
		t.Fatal("env")
	}
}

func TestPageGivesTheVariablesTheirDefaultsAndOverrides(t *testing.T) {
	eachStore(t, func(t *testing.T, newStore func() Store) {
		reg := t.TempDir()
		blk := filepath.Join(reg, "demo")
		if err := os.MkdirAll(blk, 0o755); err != nil {
			t.Fatal(err)
		}
		item := `{"name":"demo","title":"Demo","dimensions":{"width":320,"height":180},"duration":2,"files":[{"path":"demo.html","type":"hyperframes:composition"}],
 "variables":[{"id":"word","default":"hi"},{"id":"size","default":3}]}`
		for p, s := range map[string]string{"registry-item.json": item, "demo.html": "<html></html>"} {
			if err := os.WriteFile(filepath.Join(blk, p), []byte(s), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		st := newStore()
		if _, err := st.Import(Source{Dir: reg}, nil, false); err != nil {
			t.Fatal(err)
		}
		d := Drawer{Store: st}
		_, pg, err := d.Page("demo", "", 0, nil)
		if err != nil || !strings.Contains(pg.Init, `"word":"hi"`) || !strings.Contains(pg.Init, `"size":3`) || pg.Duration != 2 {
			t.Fatalf("%v %s %v", err, pg.Init, pg.Duration)
		}
		_, pg, _ = d.Page("demo", "", 5, map[string]any{"word": "hello"})
		if !strings.Contains(pg.Init, `"word":"hello"`) || !strings.Contains(pg.Init, `"size":3`) || pg.Duration != 5 {
			t.Fatalf("%s %v", pg.Init, pg.Duration)
		}
	})
}

func TestImportComponentsUsesTheDemoAndItsGeometry(t *testing.T) {
	eachStore(t, func(t *testing.T, newStore func() Store) {
		reg := t.TempDir()
		dir := filepath.Join(reg, "cursor")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		files := map[string]string{
			"registry-item.json": `{"name":"cursor","title":"Cursor","tags":["cursor"],"family":"pointers","profile":"interaction","jobs":["demonstrate"],"syncPoints":[{"id":"click","phase":"in","offset":2}],"files":[{"path":"cursor.html","type":"hyperframes:snippet"}]}`,
			"cursor.html":        "<template>snippet</template>",
			"demo.html":          `<html><body><div data-composition-src="./cursor.html" data-start="0" data-duration="4.8" data-width="1280" data-height="720"></div></body></html>`,
		}
		for p, c := range files {
			if err := os.WriteFile(filepath.Join(dir, p), []byte(c), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		st := newStore()
		res, err := st.ImportComponents(Source{Dir: reg}, nil, false)
		if err != nil || len(res) != 1 || !res[0].Written {
			t.Fatalf("%+v %v", res, err)
		}
		b, err := st.Get("cursor")
		if err != nil || b.Kind != "component" || b.HTML != "demo.html" || b.Width != 1280 || b.Height != 720 || b.Duration != 4.8 || b.Family != "pointers" || len(b.Jobs) != 1 || len(b.SyncPoints) == 0 {
			t.Fatalf("%+v %v", b, err)
		}
		if _, err := st.Read("cursor", "cursor.html"); err != nil {
			t.Fatal("the snippet is kept with the component")
		}
		if w, h, d := demoGeometry("<html></html>"); w != 1920 || h != 1080 || d != 5 {
			t.Fatalf("%d %d %g", w, h, d)
		}
	})
}

func TestAPageThatMountsUsesTheUpstreamRuntimeOnlyWhenInstalled(t *testing.T) {
	d := Drawer{}
	if d.UsesUpstream("<html><body></body></html>") {
		t.Fatal("a page that mounts nothing uses the builtin runtime")
	}
	if !(Drawer{Runtime: "upstream"}).UsesUpstream("<p>x</p>") || (Drawer{Runtime: "builtin"}).UsesUpstream(`<div data-composition-src="a.html">`) {
		t.Fatal("the runtime can be forced")
	}
}

func TestExtendAddsTheUpstreamRuntimeToFramesOfAPageThatMounts(t *testing.T) {
	old := UpstreamFile
	UpstreamFile = filepath.Join(t.TempDir(), "rt.js")
	t.Cleanup(func() { UpstreamFile = old })
	if err := os.WriteFile(UpstreamFile, []byte("/*runtime*/"), 0o644); err != nil {
		t.Fatal(err)
	}
	plain := motion.Page{HTML: `<div data-hf-block="a"></div>`}
	Extend(&plain)
	if strings.Contains(plain.Init, "/*runtime*/") || plain.Resource == nil || !plain.GPU {
		t.Fatalf("a page whose blocks mount nothing keeps the builtin runtime: %v", plain.Init)
	}
	mounts := motion.Page{HTML: `<div data-hf-block="a"></div><iframe srcdoc="&lt;div data-composition-src=./x.html&gt;">`}
	Extend(&mounts)
	if !strings.Contains(mounts.Init, "window.self !== window.top") || !strings.Contains(mounts.Init, "/*runtime*/") {
		t.Fatalf("a page that mounts gets the upstream runtime in its frames only: %v", mounts.Init)
	}
	none := motion.Page{HTML: "<p>nothing</p>"}
	Extend(&none)
	if none.Init != "" || none.Resource != nil {
		t.Fatal("a page with no block is left as it is")
	}
}

func TestFillMissingMakesWhatTheRegistryDoesNotShipAndSaysSo(t *testing.T) {
	eachStore(t, func(t *testing.T, newStore func() Store) {
		reg := t.TempDir()
		dir := filepath.Join(reg, "gallery")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		item := `{"name":"gallery","title":"G","dimensions":{"width":320,"height":180},"duration":2,"files":[
 {"path":"gallery.html","type":"hyperframes:composition"},
 {"path":"assets/covers/a.jpg","type":"hyperframes:asset"},{"path":"assets/covers/b.jpg","type":"hyperframes:asset"},
 {"path":"assets/matcap-1.png","type":"hyperframes:asset"},{"path":"assets/x.bin","type":"hyperframes:asset"}]}`
		for p, c := range map[string]string{"registry-item.json": item, "gallery.html": "<html></html>"} {
			if err := os.WriteFile(filepath.Join(dir, p), []byte(c), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		st := newStore()
		// a picture the server generated is used in place of a procedural one
		if err := os.MkdirAll(filepath.Join(st.Dir, PlaceholdersDir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(st.Dir, PlaceholdersDir, "a.jpg"), []byte("GENERATED"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := st.Import(Source{Dir: reg}, nil, false); err != nil {
			t.Fatal(err)
		}
		b, err := st.Get("gallery")
		if err != nil || len(b.Placeholders) != 3 {
			t.Fatalf("%+v %v", b.Placeholders, err)
		}
		if got, _ := st.Read("gallery", "assets/covers/a.jpg"); string(got) != "GENERATED" {
			t.Fatalf("the generated picture is the one used: %q", got)
		}
		if got, _ := st.Read("gallery", "assets/covers/b.jpg"); len(got) < 1000 || got[0] != 0xFF {
			t.Fatal("a procedural jpg is made when none was generated")
		}
		if got, _ := st.Read("gallery", "assets/matcap-1.png"); len(got) < 1000 || got[1] != 'P' {
			t.Fatal("a procedural png is made")
		}
		if _, err := st.Read("gallery", "assets/x.bin"); err == nil {
			t.Fatal("what cannot be made stays missing")
		}
		// the registry's own files are never replaced
		if again, _ := st.FillMissing("gallery"); len(again) != 0 {
			t.Fatalf("nothing more to make: %v", again)
		}
	})
}

func TestFetchAssetsChecksTheDownloadAgainstItsAddressAndReplacesPlaceholders(t *testing.T) {
	eachStore(t, func(t *testing.T, newStore func() Store) {
		body := []byte("the real picture")
		sum := sha256.Sum256(body)
		id := hex.EncodeToString(sum[:])[:16]
		bad := "0000000000000000"
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(body) }))
		defer srv.Close()
		oldHost, oldClient := AssetHost, assetClient
		AssetHost, assetClient = strings.TrimPrefix(srv.URL, "https://"), srv.Client()
		t.Cleanup(func() { AssetHost, assetClient = oldHost, oldClient })

		reg := t.TempDir()
		dir := filepath.Join(reg, "gallery")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		item := `{"name":"gallery","title":"G","dimensions":{"width":320,"height":180},"duration":2,"files":[
 {"path":"gallery.html","type":"hyperframes:composition"},
 {"path":"assets/good.jpg","type":"hyperframes:asset","url":"` + srv.URL + `/a/` + id + `.jpg"},
 {"path":"assets/bad.jpg","type":"hyperframes:asset","url":"` + srv.URL + `/a/` + bad + `.jpg"},
 {"path":"assets/elsewhere.jpg","type":"hyperframes:asset","url":"https://evil.example/a/` + id + `.jpg"}]}`
		for p, c := range map[string]string{"registry-item.json": item, "gallery.html": "<html></html>"} {
			if err := os.WriteFile(filepath.Join(dir, p), []byte(c), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		st := newStore()
		if _, err := st.Import(Source{Dir: reg}, nil, false); err != nil {
			t.Fatal(err)
		}
		if _, err := st.FetchAssets(context.Background(), "gallery"); err != nil {
			t.Logf("fetch: %v", err)
		}
		if got, _ := st.Read("gallery", "assets/good.jpg"); string(got) != string(body) {
			t.Fatalf("the real file is fetched, not made up: %q", got)
		}
		b, _ := st.Get("gallery")
		for _, p := range b.Placeholders {
			if p == "assets/good.jpg" {
				t.Fatal("a fetched file is not a placeholder")
			}
		}
		// a file whose hash does not match its address, and one from another host, are never kept as fetched: they get placeholders
		if got, _ := st.Read("gallery", "assets/bad.jpg"); string(got) == string(body) {
			t.Fatal("a download that does not match its address was kept")
		}
		if !contains(b.Placeholders, "assets/bad.jpg") || !contains(b.Placeholders, "assets/elsewhere.jpg") {
			t.Fatalf("%v", b.Placeholders)
		}
	})
}
