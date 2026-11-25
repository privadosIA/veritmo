package assets3d

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func glbBytes(js string) []byte {
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

// character is a model with clips and a texture that the pack keeps in another folder, like Kenney's.
func character(clips ...string) []byte {
	var an []string
	for _, c := range clips {
		an = append(an, fmt.Sprintf(`{"name":%q}`, c))
	}
	return glbBytes(`{"animations":[` + strings.Join(an, ",") + `],"accessors":[{"count":300}],"meshes":[{"primitives":[{"attributes":{"POSITION":0},"indices":0}]}],"images":[{"uri":"Textures/colormap.png"}]}`)
}

type entry struct {
	name string
	data []byte
	mode uint32 // 0: a regular file
}

func makeZip(t testing.TB, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		if e.mode != 0 {
			h.SetMode(fs.FileMode(e.mode))
		}
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(e.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// goodPack is a pack like Kenney's: models in a folder with their atlas, previews, the licence, and a copy in another format.
func goodPack(t testing.TB) []byte {
	return makeZip(t,
		entry{name: "Models/GLB format/char-a.glb", data: character("idle", "sprint", "walk")},
		entry{name: "Models/GLB format/char-b.glb", data: character("idle")},
		entry{name: "Models/GLB format/prop-lamp.glb", data: glbBytes(`{"accessors":[{"count":30}],"meshes":[{"primitives":[{"attributes":{"POSITION":0},"indices":0}]}]}`)},
		entry{name: "Models/GLB format/prop-bare.glb", data: glbBytes(`{"accessors":[{"count":30}],"meshes":[{"primitives":[{"attributes":{"POSITION":0},"indices":0}]}]}`)},
		entry{name: "Models/GLB format/broken.glb", data: []byte("not a model")},
		entry{name: "Models/GLB format/Textures/colormap.png", data: []byte("atlas pixels")},
		entry{name: "Models/FBX format/char-a.fbx", data: []byte("fbx")},
		entry{name: "Previews/char-a.png", data: []byte("preview of a")},
		entry{name: "Isometric/prop-lamp_SW.png", data: []byte("isometric lamp SW")},
		entry{name: "Isometric/prop-lamp_NE.png", data: []byte("isometric lamp")},
		entry{name: "Side/prop-lamp.png", data: []byte("side lamp")},
		entry{name: "Side/char-b.png", data: []byte("side of b")},
		entry{name: "License.txt", data: []byte("License: (Creative Commons Zero, CC0)")},
	)
}

func hashOf(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// fakeHost is a Kenney-like host: the page sets a cookie and holds the download id, the link is only given to a request that
// carries the cookie and comes from the page, and the zip is behind a signed address.
type fakeHost struct {
	srv       *httptest.Server
	zip       []byte
	linkHosts atomic.Int32
	pageHasID bool
	linkURL   func(base string) string
	slow      chan struct{} // when set, the zip never answers until it is closed
}

func newFakeHost(t testing.TB, zipBytes []byte) *fakeHost {
	f := &fakeHost{zip: zipBytes, pageHasID: true}
	mux := http.NewServeMux()
	mux.HandleFunc("/assets/test-pack", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "s1", Path: "/"})
		if f.pageHasID {
			fmt.Fprint(w, `<html><a id='donate-text' href='#' data-download-id='6173736574' onclick='x'>go</a></html>`)
		} else {
			fmt.Fprint(w, `<html>nothing here</html>`)
		}
	})
	mux.HandleFunc("/download-link/", func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("session")
		if err != nil || c.Value != "s1" || !strings.HasSuffix(r.Referer(), "/assets/test-pack") {
			http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
			return
		}
		f.linkHosts.Add(1)
		u := f.srv.URL + "/download/signed?expires=1"
		if f.linkURL != nil {
			u = f.linkURL(f.srv.URL)
		}
		fmt.Fprintf(w, `{"url":%q}`, u)
	})
	mux.HandleFunc("/download/", func(w http.ResponseWriter, r *http.Request) {
		if f.slow != nil {
			select {
			case <-f.slow:
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(f.zip)))
		_, _ = w.Write(f.zip)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeHost) def(zipBytes []byte) PackDef {
	return PackDef{ID: "test-pack", Name: "Test pack", Author: "Tester", Licence: "CC0", Page: f.srv.URL + "/assets/test-pack", Version: "1", SizeBytes: int64(len(zipBytes)),
		SHA256: hashOf(zipBytes), Fetch: "kenney", LicenceWords: []string{"Creative Commons Zero"}, Description: "a test", Tags: []string{"characters"}}
}

// installed makes a library with the pack of the fake host installed.
func installed(t testing.TB) (*Library, *fakeHost) {
	t.Helper()
	z := goodPack(t)
	f := newFakeHost(t, z)
	l := New(t.TempDir(), []PackDef{f.def(z)})
	if err := l.Install("test-pack"); err != nil {
		t.Fatal(err)
	}
	l.Wait("test-pack")
	if v, _ := l.Pack("test-pack"); v.State != StateInstalled {
		t.Fatalf("the pack is not installed: %+v", v)
	}
	return l, f
}
