package library3d

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"veritmo/internal/assets3d"
	"veritmo/internal/project/config"
)

func glb(js string) []byte {
	for len(js)%4 != 0 {
		js += " "
	}
	out := []byte("glTF")
	out = binary.LittleEndian.AppendUint32(out, 2)
	out = binary.LittleEndian.AppendUint32(out, uint32(20+len(js)))
	out = binary.LittleEndian.AppendUint32(out, uint32(len(js)))
	out = binary.LittleEndian.AppendUint32(out, 0x4E4F534A)
	return append(out, js...)
}

func zipOf(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, data := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write(data)
	}
	zw.Close()
	return buf.Bytes()
}

func pack(t *testing.T) []byte {
	return zipOf(t, map[string][]byte{
		"GLB/char-a.glb":            glb(`{"animations":[{"name":"idle"},{"name":"walk"}],"accessors":[{"count":30}],"meshes":[{"primitives":[{"attributes":{"POSITION":0},"indices":0}]}],"images":[{"uri":"Textures/colormap.png"}]}`),
		"GLB/prop.glb":              glb(`{"accessors":[{"count":30}],"meshes":[{"primitives":[{"attributes":{"POSITION":0},"indices":0}]}]}`),
		"GLB/Textures/colormap.png": []byte("atlas"),
		"Previews/char-a.png":       []byte("preview"),
		"License.txt":               []byte("Creative Commons Zero"),
	})
}

type server struct {
	h   *Handler
	mux *http.ServeMux
}

func newServer(t *testing.T, defs []assets3d.PackDef) *server {
	lib := assets3d.New(t.TempDir(), defs)
	h := New(lib)
	mux := http.NewServeMux()
	h.Register(func(p string, f http.HandlerFunc) { mux.HandleFunc(p, f) })
	return &server{h, mux}
}

func (s *server) do(method, path string, body []byte) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, httptest.NewRequest(method, path, bytes.NewReader(body)))
	return rec
}

func (s *server) importPack(t *testing.T, id string) {
	t.Helper()
	if rec := s.do(http.MethodPost, "/api/models3d/packs/import?id="+id+"&name=Mine&author=Me&licence=CC0", pack(t)); rec.Code != http.StatusCreated {
		t.Fatalf("import: %d %s", rec.Code, rec.Body)
	}
}

func TestPacksListShowsTheOnesThatShip(t *testing.T) {
	s := newServer(t, assets3d.DefaultPacks())
	rec := s.do(http.MethodGet, "/api/models3d/packs", nil)
	var out struct{ Packs []assets3d.PackView }
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusOK || len(out.Packs) == 0 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	for _, p := range out.Packs {
		if p.State != "available" || p.Licence != "CC0" || p.SizeBytes == 0 || p.Page == "" {
			t.Errorf("%+v", p)
		}
	}
	if rec := s.do(http.MethodGet, "/api/models3d/packs/nope", nil); rec.Code != http.StatusNotFound {
		t.Errorf("%d", rec.Code)
	}
}

func TestImportListSearchAndDelete(t *testing.T) {
	s := newServer(t, nil)
	s.importPack(t, "mine")
	rec := s.do(http.MethodGet, "/api/models3d/packs/mine", nil)
	var p assets3d.PackView
	json.Unmarshal(rec.Body.Bytes(), &p)
	if p.State != "installed" || p.Source != "user" || p.Models != 2 || p.Author != "Me" {
		t.Errorf("%+v", p)
	}
	for name, tc := range map[string]struct {
		query string
		want  int
	}{"all": {"", 2}, "a search": {"?q=char", 1}, "clips": {"?has_clips=true", 1}, "tag": {"?tag=prop", 1}, "pack": {"?pack=nope", 0}} {
		rec := s.do(http.MethodGet, "/api/models3d/models"+tc.query, nil)
		var out struct {
			Models []assets3d.ModelView `json:"models"`
			Total  int                  `json:"total"`
		}
		json.Unmarshal(rec.Body.Bytes(), &out)
		if rec.Code != http.StatusOK || len(out.Models) != tc.want {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	// a page: the headers say where the others are
	rec = s.do(http.MethodGet, "/api/models3d/models?limit=1", nil)
	if rec.Header().Get("X-Total-Count") != "2" || !strings.Contains(rec.Header().Get("Link"), `rel="next"`) {
		t.Errorf("headers: %v", rec.Header())
	}
	if rec := s.do(http.MethodGet, "/api/models3d/models?limit=0", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("a bad limit: %d", rec.Code)
	}
	if rec := s.do(http.MethodGet, "/api/models3d/models?has_clips=maybe", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("a bad has_clips: %d", rec.Code)
	}
	rec = s.do(http.MethodGet, "/api/models3d/models/mine/char-a", nil)
	var m assets3d.ModelView
	json.Unmarshal(rec.Body.Bytes(), &m)
	if rec.Code != http.StatusOK || m.ID != "mine/char-a" || strings.Join(m.Clips, ",") != "idle,walk" || strings.Join(m.Needs, ",") != "colormap.png" {
		t.Errorf("%d %s", rec.Code, rec.Body)
	}
	if rec := s.do(http.MethodGet, "/api/models3d/models/mine/nope", nil); rec.Code != http.StatusNotFound {
		t.Errorf("%d", rec.Code)
	}
	// thumbnails: the pack's own, and a 503 for the one that has none and cannot be drawn
	if rec := s.do(http.MethodGet, "/api/models3d/models/mine/char-a/thumbnail", nil); rec.Code != http.StatusOK || rec.Body.String() != "preview" || rec.Header().Get("Content-Type") != "image/png" {
		t.Errorf("%d %q %s", rec.Code, rec.Body, rec.Header().Get("Content-Type"))
	}
	if rec := s.do(http.MethodGet, "/api/models3d/models/mine/prop/thumbnail", nil); rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "Blender") {
		t.Errorf("%d %s", rec.Code, rec.Body)
	}
	if rec := s.do(http.MethodDelete, "/api/models3d/packs/mine", nil); rec.Code != http.StatusNoContent {
		t.Errorf("%d", rec.Code)
	}
	if rec := s.do(http.MethodDelete, "/api/models3d/packs/mine", nil); rec.Code != http.StatusNotFound {
		t.Errorf("%d", rec.Code)
	}
}

func TestModelFileIsServedWithItsETag(t *testing.T) {
	s := newServer(t, nil)
	s.importPack(t, "mine")
	rec := s.do(http.MethodGet, "/api/models3d/models/mine/prop/file", nil)
	want := glb(`{"accessors":[{"count":30}],"meshes":[{"primitives":[{"attributes":{"POSITION":0},"indices":0}]}]}`)
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), want) {
		t.Fatalf("%d, %d bytes (want %d)", rec.Code, rec.Body.Len(), len(want))
	}
	h := rec.Header()
	if h.Get("Content-Type") != "model/gltf-binary" || h.Get("Content-Length") != fmt.Sprint(len(want)) || h.Get("ETag") == "" || !strings.HasPrefix(h.Get("Cache-Control"), "private, max-age=") {
		t.Errorf("headers: %v", h)
	}
	// the same ETag answers 304 and no body
	req := httptest.NewRequest(http.MethodGet, "/api/models3d/models/mine/prop/file", nil)
	req.Header.Set("If-None-Match", h.Get("ETag"))
	again := httptest.NewRecorder()
	s.mux.ServeHTTP(again, req)
	if again.Code != http.StatusNotModified || again.Body.Len() != 0 {
		t.Errorf("If-None-Match: %d, %d bytes", again.Code, again.Body.Len())
	}
	// a model that is not there, and a pack that is not
	for _, p := range []string{"/api/models3d/models/mine/nope/file", "/api/models3d/models/nope/prop/file"} {
		if rec := s.do(http.MethodGet, p, nil); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "not_found") {
			t.Errorf("%s: %d %s", p, rec.Code, rec.Body)
		}
	}
}

func TestImportErrorsHaveTheirOwnStatus(t *testing.T) {
	s := newServer(t, nil)
	s.importPack(t, "mine")
	for name, tc := range map[string]struct {
		path string
		body []byte
		want int
	}{
		"no licence":   {"/api/models3d/packs/import?id=x", pack(t), http.StatusBadRequest},
		"an id in use": {"/api/models3d/packs/import?id=mine&licence=CC0", pack(t), http.StatusConflict},
		"no model":     {"/api/models3d/packs/import?id=y&licence=CC0", zipOf(t, map[string][]byte{"a.png": []byte("x")}), http.StatusUnprocessableEntity},
		"not a zip":    {"/api/models3d/packs/import?id=z&licence=CC0", []byte("text"), http.StatusBadRequest},
		"unsafe":       {"/api/models3d/packs/import?id=w&licence=CC0", zipOf(t, map[string][]byte{"../x.glb": []byte("x")}), http.StatusBadRequest},
	} {
		if rec := s.do(http.MethodPost, tc.path, tc.body); rec.Code != tc.want {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
}

// kenney is a host like Kenney's: the page has the download id, the link needs the cookie and the page as referer.
func kenney(t *testing.T, zipBytes []byte, slow chan struct{}) *httptest.Server {
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/assets/p", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "s", Value: "1", Path: "/"})
		fmt.Fprint(w, `<a data-download-id='abc'>x</a>`)
	})
	mux.HandleFunc("/download-link/", func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie("s"); err != nil || c.Value != "1" || !strings.HasSuffix(r.Referer(), "/assets/p") {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		fmt.Fprintf(w, `{"url":%q}`, srv.URL+"/download/x")
	})
	mux.HandleFunc("/download/", func(w http.ResponseWriter, r *http.Request) {
		if slow != nil {
			select {
			case <-slow:
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(zipBytes)))
		w.Write(zipBytes)
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestInstallCancelAndTheStatesOfAPack(t *testing.T) {
	z := pack(t)
	sum := sha256.Sum256(z)
	slow := make(chan struct{})
	srv := kenney(t, z, slow)
	def := assets3d.PackDef{ID: "p", Name: "P", Author: "A", Licence: "CC0", Page: srv.URL + "/assets/p", Version: "1", SizeBytes: int64(len(z)), SHA256: hex.EncodeToString(sum[:]),
		Fetch: "kenney", LicenceWords: []string{"Creative Commons Zero"}, Tags: []string{"characters"}}
	s := newServer(t, []assets3d.PackDef{def})
	if rec := s.do(http.MethodPost, "/api/models3d/packs/nope/install", nil); rec.Code != http.StatusNotFound {
		t.Errorf("not in the list: %d", rec.Code)
	}
	if rec := s.do(http.MethodPost, "/api/models3d/packs/p/cancel", nil); rec.Code != http.StatusConflict {
		t.Errorf("cancel of what is not installing: %d", rec.Code)
	}
	rec := s.do(http.MethodPost, "/api/models3d/packs/p/install", nil)
	var p assets3d.PackView
	json.Unmarshal(rec.Body.Bytes(), &p)
	if rec.Code != http.StatusAccepted || p.State != "installing" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := s.do(http.MethodPost, "/api/models3d/packs/p/install", nil); rec.Code != http.StatusConflict {
		t.Errorf("a second install: %d", rec.Code)
	}
	if rec := s.do(http.MethodDelete, "/api/models3d/packs/p", nil); rec.Code != http.StatusConflict {
		t.Errorf("delete while installing: %d", rec.Code)
	}
	if rec := s.do(http.MethodPost, "/api/models3d/packs/p/cancel", nil); rec.Code != http.StatusOK {
		t.Fatalf("cancel: %d %s", rec.Code, rec.Body)
	}
	json.Unmarshal(s.do(http.MethodGet, "/api/models3d/packs/p", nil).Body.Bytes(), &p)
	if p.State != "available" {
		t.Errorf("after cancel: %+v", p)
	}
	// and now through to the end
	close(slow)
	if rec := s.do(http.MethodPost, "/api/models3d/packs/p/install", nil); rec.Code != http.StatusAccepted {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		json.Unmarshal(s.do(http.MethodGet, "/api/models3d/packs/p", nil).Body.Bytes(), &p)
		if p.State != "installing" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if p.State != "installed" || p.Models != 2 || p.Source != "catalog" {
		t.Fatalf("installed: %+v", p)
	}
	if rec := s.do(http.MethodPost, "/api/models3d/packs/p/install", nil); rec.Code != http.StatusConflict {
		t.Errorf("installing an installed pack: %d", rec.Code)
	}
}

type fakeHost struct{ adult bool }

func (f fakeHost) ShowsAdult() bool                                  { return f.adult }
func (f fakeHost) RefuseAdultConfig(http.ResponseWriter, *bool) bool { return false }
func (f fakeHost) ProjectVisible(string) bool                        { return true }
func (f fakeHost) PictureDefaults() (config.Defaults, error)         { return config.Defaults{}, nil }

func TestCatalogFiltersByOriginAndSearchesPrompts(t *testing.T) {
	s := newServer(t, nil)
	s.importPack(t, "mine")
	add := func(name, prompt, engine, project string, adult bool) {
		_, err := s.h.Library().AddGenerated(glb(`{"accessors":[{"count":30}],"meshes":[{"primitives":[{"attributes":{"POSITION":0},"indices":0}]}]}`),
			assets3d.GenMeta{Name: name, Prompt: prompt, Engine: engine, Licence: "MIT", Project: project, Entry: name, Adult: adult})
		if err != nil {
			t.Fatal(err)
		}
	}
	add("patin", "a retro roller skate, glossy pink", "triposr", "objetos", false)
	add("copa", "a golden trophy", "sf3d", "objetos", false)
	add("gato", "a white cat", "triposr", "mascotas", true)
	names := func(query string) string {
		rec := s.do(http.MethodGet, "/api/models3d/models?"+query, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", query, rec.Code, rec.Body)
		}
		var body struct {
			Models []struct{ Name, Source, Project string }
			Total  int
		}
		json.Unmarshal(rec.Body.Bytes(), &body)
		var out []string
		for _, m := range body.Models {
			out = append(out, m.Name)
		}
		if body.Total != len(out) {
			t.Errorf("%s: total %d for %d models", query, body.Total, len(out))
		}
		return strings.Join(out, ",")
	}
	// a censored server (no host, or one that says no) does not list what is for adults
	if got := names("source=generated"); got != "patin,copa" && got != "copa,patin" {
		t.Errorf("generated: %q", got)
	}
	if got := names("source=user"); got != "char-a,prop,tree" && !strings.Contains(got, "char-a") {
		t.Errorf("user: %q", got)
	}
	if got := names("source=catalog"); got != "" {
		t.Errorf("catalog: %q", got)
	}
	if got := names("engine=sf3d"); got != "copa" {
		t.Errorf("engine: %q", got)
	}
	if got := names("project=objetos&engine=triposr"); got != "patin" {
		t.Errorf("project and engine: %q", got)
	}
	if got := names("q=roller+skate"); got != "patin" {
		t.Errorf("prompt search: %q", got)
	}
	if got := names("q=cat"); got != "" {
		t.Errorf("the adult model is not found by a censored server: %q", got)
	}
	if rec := s.do(http.MethodGet, "/api/models3d/models/generated/gato", nil); rec.Code != http.StatusNotFound {
		t.Errorf("detail of an adult model: %d %s", rec.Code, rec.Body)
	}
	if rec := s.do(http.MethodGet, "/api/models3d/models/generated/gato/thumbnail", nil); rec.Code != http.StatusNotFound {
		t.Errorf("thumbnail of an adult model: %d", rec.Code)
	}
	if rec := s.do(http.MethodGet, "/api/models3d/models/generated/gato/file", nil); rec.Code != http.StatusNotFound {
		t.Errorf("file of an adult model: %d", rec.Code)
	}
	// a server that shows everything does
	s.h.SetHost(fakeHost{adult: true})
	if got := names("q=cat"); got != "gato" {
		t.Errorf("an open server finds it: %q", got)
	}
	rec := s.do(http.MethodGet, "/api/models3d/models/generated/gato", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"adult":true`) || !strings.Contains(rec.Body.String(), `"project":"mascotas"`) {
		t.Errorf("detail: %d %s", rec.Code, rec.Body)
	}
	for _, bad := range []string{"source=cloud", "engine=nerf"} {
		if rec := s.do(http.MethodGet, "/api/models3d/models?"+bad, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", bad, rec.Code)
		}
	}
}
