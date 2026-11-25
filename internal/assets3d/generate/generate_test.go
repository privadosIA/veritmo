package generate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"veritmo/internal/assets3d"
	"veritmo/internal/platform/musicsvc"
)

func glb(indices int) []byte {
	js := fmt.Sprintf(`{"accessors":[{"count":%d}],"meshes":[{"primitives":[{"attributes":{"POSITION":0},"indices":0}]}]}`, indices)
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

var png = append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...)

// fakeService speaks the protocol of docker/model3d/server.py: POST /generate, GET /jobs/{id}, GET /jobs/{id}/model, DELETE /jobs/{id}.
type fakeService struct {
	mu       sync.Mutex
	srv      *httptest.Server
	received []byte
	engine   string
	detail   string
	fail     string // the sentence a job fails with
	states   int    // how many "running" answers before done
	deleted  int
	glb      []byte
}

func newFakeService(t *testing.T) *fakeService {
	f := &fakeService{glb: glb(300), states: 1}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /generate", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		buf := new(bytes.Buffer)
		buf.ReadFrom(r.Body)
		f.received, f.engine, f.detail = buf.Bytes(), r.URL.Query().Get("engine"), r.URL.Query().Get("detail")
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprint(w, `{"id":"job1"}`)
	})
	mux.HandleFunc("GET /jobs/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		v := map[string]any{"id": "job1", "engine": f.engine, "engine_version": "triposr@107cefd", "detail": f.detail, "detail_used": f.detail, "triangles": 100}
		switch {
		case f.fail != "":
			v["state"], v["error"] = "failed", f.fail
		case f.states > 0:
			f.states--
			v["state"] = "running"
		default:
			v["state"] = "done"
		}
		json.NewEncoder(w).Encode(v)
	})
	mux.HandleFunc("GET /engines", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"engines": {"triposr": {"built": true, "missing": [], "ready": true, "version": "triposr@107cefd"}, "sf3d": {"built": false, "missing": [], "ready": false}}}`)
	})
	mux.HandleFunc("GET /jobs/{id}/model", func(w http.ResponseWriter, r *http.Request) { w.Write(f.glb) })
	mux.HandleFunc("DELETE /jobs/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.deleted++
		f.mu.Unlock()
		fmt.Fprint(w, `{"deleted":true}`)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

type fakeOpener struct {
	f   *fakeService
	err error
	got string
}

func (o *fakeOpener) Open(ctx context.Context, engine string) (*Client, func(), error) {
	o.got = engine
	if o.err != nil {
		return nil, func() {}, o.err
	}
	return &Client{BaseURL: o.f.srv.URL, PollEvery: time.Millisecond}, func() {}, nil
}

func deps(t *testing.T) (Deps, *fakeService, *assets3d.Library, *[]string) {
	f := newFakeService(t)
	lib := assets3d.New(t.TempDir(), nil)
	var asked []string
	steps := &[]string{}
	return Deps{Service: &fakeOpener{f: f}, Library: lib, OnStep: func(s string) { *steps = append(*steps, s) },
		Picture: func(ctx context.Context, p string) ([]byte, error) { asked = append(asked, p); return png, nil }}, f, lib, steps
}

func TestGenerateFromAPromptMakesThePictureAndPutsTheModelInTheLibrary(t *testing.T) {
	d, f, lib, steps := deps(t)
	out, err := Generate(context.Background(), d, Input{Prompt: "  a retro roller skate, glossy pink  "})
	if err != nil {
		t.Fatal(err)
	}
	if out.Model != "generated/a-retro-roller-skate-glossy" || out.Triangles != 100 || out.EngineVersion != "triposr@107cefd" || out.DetailUsed != "normal" {
		t.Errorf("%+v", out)
	}
	if !bytes.Equal(f.received, png) || f.engine != "triposr" || f.detail != "normal" {
		t.Errorf("what the service got: %d bytes engine=%s detail=%s", len(f.received), f.engine, f.detail)
	}
	if strings.Join(*steps, ",") != "picture,model" {
		t.Errorf("steps %v", *steps)
	}
	if f.deleted != 1 {
		t.Errorf("the job is forgotten: %d", f.deleted)
	}
	v, err := lib.Model("generated", "a-retro-roller-skate-glossy")
	if err != nil || v.Prompt != "a retro roller skate, glossy pink" || v.Engine != "triposr" || v.Licence != "MIT" || v.Attribution {
		t.Errorf("the library records what made it: %+v %v", v, err)
	}
}

func TestThePromptIsCompletedForOneObjectOnAPlainBackground(t *testing.T) {
	var got string
	d, _, _, _ := deps(t)
	d.Picture = func(ctx context.Context, p string) ([]byte, error) { got = p; return png, nil }
	if _, err := Generate(context.Background(), d, Input{Prompt: "a golden trophy cup"}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "a golden trophy cup, single object centered, isolated on a plain pure white background") {
		t.Errorf("%q", got)
	}
}

func TestGenerateFromAPictureMakesNoPicture(t *testing.T) {
	d, f, lib, steps := deps(t)
	d.Picture = func(ctx context.Context, p string) ([]byte, error) { t.Error("no picture is made"); return nil, nil }
	out, err := Generate(context.Background(), d, Input{Image: png, ImageName: "lampara.png", Engine: "triposr", Detail: "low", Name: "lampara"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Name != "lampara" || f.detail != "low" || strings.Join(*steps, ",") != "model" {
		t.Errorf("%+v detail=%s steps=%v", out, f.detail, *steps)
	}
	if v, _ := lib.Model("generated", "lampara"); v.Prompt != "lampara.png" {
		t.Errorf("the picture's name is what it says: %+v", v)
	}
}

func TestGenerateRefusesWhatItCanBeToldAtOnceNamingIt(t *testing.T) {
	d, f, _, _ := deps(t)
	big := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, MaxImageBytes)...)
	for name, tc := range map[string]struct {
		in   Input
		want string
	}{
		"both":          {Input{Prompt: "x", Image: png}, "not both"},
		"neither":       {Input{}, "a prompt or a picture"},
		"blank":         {Input{Prompt: "   "}, "a prompt or a picture"},
		"long":          {Input{Prompt: strings.Repeat("a", MaxPromptRunes+1)}, "longer than"},
		"engine":        {Input{Prompt: "x", Engine: "dalle"}, "engine must be"},
		"detail":        {Input{Prompt: "x", Detail: "ultra"}, "detail must be"},
		"a name":        {Input{Prompt: "x", Name: "../x"}, "plain name"},
		"not a picture": {Input{Image: []byte("just text")}, "png, jpg or webp"},
		"too big":       {Input{Image: big}, "larger than"},
	} {
		_, err := Generate(context.Background(), d, tc.in)
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if f.received != nil {
		t.Error("nothing reached the service")
	}
	d.Picture = nil
	if _, err := Generate(context.Background(), d, Input{Prompt: "x"}); !errors.Is(err, ErrNoPictureMaker) {
		t.Errorf("a prompt with no way to make the picture: %v", err)
	}
}

func TestAnEngineThatCannotBeUsedIsRefusedWithItsReasonAndNothingIsMade(t *testing.T) {
	d, _, lib, _ := deps(t)
	d.Service = &fakeOpener{err: &musicsvc.UnavailableError{Reason: musicsvc.ReasonNotRunning, Detail: "the service is not running"}}
	d.Picture = func(ctx context.Context, p string) ([]byte, error) {
		t.Error("the picture is not made first")
		return nil, nil
	}
	_, err := Generate(context.Background(), d, Input{Prompt: "x"})
	var ue *musicsvc.UnavailableError
	if !errors.As(err, &ue) || ue.Reason != musicsvc.ReasonNotRunning {
		t.Errorf("%v", err)
	}
	if _, e := lib.Pack("generated"); e == nil {
		t.Error("nothing is in the library")
	}
}

func TestFailuresOfThePictureAndOfTheServiceAreSaidAndLeaveNothing(t *testing.T) {
	d, f, lib, _ := deps(t)
	d.Picture = func(ctx context.Context, p string) ([]byte, error) { return nil, errors.New("sd ran out of memory") }
	if _, err := Generate(context.Background(), d, Input{Prompt: "x"}); err == nil || !strings.Contains(err.Error(), "the picture of the prompt could not be made") || !strings.Contains(err.Error(), "out of memory") {
		t.Errorf("the picture: %v", err)
	}
	f.fail = "the engine made no surface"
	_, err := Generate(context.Background(), d, Input{Image: png})
	if !errors.Is(err, ErrFailed) || !strings.Contains(err.Error(), "no surface") {
		t.Errorf("the service: %v", err)
	}
	if _, e := lib.Pack("generated"); e == nil {
		t.Error("nothing is in the library")
	}
	// a model the program cannot use is refused as it comes, not stored
	f.fail, f.glb = "", []byte("not a glb")
	if _, err := Generate(context.Background(), d, Input{Image: png}); !errors.Is(err, assets3d.ErrInvalid) {
		t.Errorf("a bad model: %v", err)
	}
}

func TestACancelledGenerationStopsWaiting(t *testing.T) {
	d, f, _, _ := deps(t)
	f.states = 1 << 30
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := Generate(ctx, d, Input{Image: png}); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("%v", err)
	}
}

func TestTheEnginesAndWhatTheyNeed(t *testing.T) {
	if got := Engines(); got[0] != EngineTripoSR || len(got) != 2 {
		t.Errorf("%v", got)
	}
	if i, _ := Info(EngineTripoSR); i.Licence != "MIT" || i.Attribution || i.Gated {
		t.Errorf("%+v", i)
	}
	if i, _ := Info(EngineSF3D); !i.Gated || !i.Attribution || !strings.Contains(i.Licence, "Stability AI Community License") {
		t.Errorf("%+v", i)
	}
	if m := Missing(EngineTripoSR)(t.TempDir()); len(m) != 4 {
		t.Errorf("%v", m)
	}
	if got := Spec(EngineTripoSR); got.Container != "vt-model3d" || got.Docker.Image != Image || got.Docker.Port != 8790 || got.WeightsDir != "models/model3d" {
		t.Errorf("%+v", got)
	}
	if Spec(EngineSF3D).Container != Spec(EngineTripoSR).Container {
		t.Error("both engines are the one container")
	}
}

func TestClientTellsTheVersionOfAnEngineBeforeAnythingIsMade(t *testing.T) {
	f := newFakeService(t)
	c := &Client{BaseURL: f.srv.URL}
	if v, err := c.EngineVersion(context.Background(), "triposr"); err != nil || v != "triposr@107cefd" {
		t.Errorf("triposr: %q %v", v, err)
	}
	// an engine the image was built without, or a service older than the field, has no version to tell
	if v, err := c.EngineVersion(context.Background(), "sf3d"); err != nil || v != "" {
		t.Errorf("sf3d: %q %v", v, err)
	}
	if _, err := (&Client{BaseURL: "http://127.0.0.1:1"}).EngineVersion(context.Background(), "triposr"); err == nil {
		t.Errorf("a service that does not answer is an error")
	}
}

func TestFetchTripoSRDownloadsCheckedFilesAndSkipsWhatIsThere(t *testing.T) {
	hits := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits[r.URL.Path]++
		switch r.URL.Path {
		case "/cut.onnx":
			w.Write([]byte("cutout"))
		case "/stabilityai/TripoSR/resolve/main/config.yaml":
			w.Write([]byte("config"))
		case "/stabilityai/TripoSR/resolve/main/model.ckpt":
			w.Write([]byte("tampered"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	sum := func(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
	f := Fetcher{HFBase: srv.URL, CutoutURL: srv.URL + "/cut.onnx", files: []weightFile{
		{rel: "rembg/u2net.onnx", url: CutoutURL, sha: sum("cutout")},
		{rel: "triposr/config.yaml", repo: "stabilityai/TripoSR", file: "config.yaml", sha: sum("config")},
		{rel: "triposr/model.ckpt", repo: "stabilityai/TripoSR", file: "model.ckpt", sha: sum("the real model")},
	}}
	dir := t.TempDir()
	var lines []string
	err := f.Fetch(context.Background(), EngineTripoSR, dir, "", func(l string) { lines = append(lines, l) })
	if err == nil || !strings.Contains(err.Error(), "triposr/model.ckpt") || !strings.Contains(err.Error(), "sha256") {
		t.Fatalf("a file with another hash must fail: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "triposr/model.ckpt")); err == nil {
		t.Error("a file with the wrong hash was kept")
	}
	if _, err := os.Stat(filepath.Join(dir, "triposr/model.ckpt.part")); err == nil {
		t.Error("the .part was left behind")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "triposr/config.yaml")); string(b) != "config" {
		t.Errorf("config.yaml: %q", b)
	}
	// the second run downloads only what failed
	f.files[2].sha = sum("tampered")
	if err := f.Fetch(context.Background(), EngineTripoSR, dir, "", func(string) {}); err != nil {
		t.Fatal(err)
	}
	if hits["/cut.onnx"] != 1 || hits["/stabilityai/TripoSR/resolve/main/config.yaml"] != 1 || hits["/stabilityai/TripoSR/resolve/main/model.ckpt"] != 2 {
		t.Errorf("what was already there is not downloaded again: %v", hits)
	}
	f.files[0].url = CutoutURL
	f.CutoutURL = srv.URL + "/missing"
	os.Remove(filepath.Join(dir, "rembg/u2net.onnx"))
	if err := f.Fetch(context.Background(), EngineTripoSR, dir, "", func(string) {}); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("a refusal is an error: %v", err)
	}
}

func TestFetchStableFastSendsTheKeyOnlyForTheGatedFilesAndNeverWithoutOne(t *testing.T) {
	auth := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth[r.URL.Path] = r.Header.Get("Authorization")
		switch r.URL.Path {
		case "/cut.onnx", "/tets.npz":
			w.Write([]byte("public"))
		case "/" + SF3DRepo + "/resolve/main/config.yaml":
			if r.Header.Get("Authorization") != "Bearer hf_key" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			w.Write([]byte("config"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	sum := func(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
	files := []weightFile{
		{rel: "rembg/u2net.onnx", url: CutoutURL, sha: sum("public")},
		{rel: "sf3d/160_tets.npz", url: TetsURL, sha: sum("public")},
		{rel: "sf3d/config.yaml", repo: SF3DRepo, file: "config.yaml", gated: true, sha: sum("config")},
	}
	f := Fetcher{HFBase: srv.URL, CutoutURL: srv.URL + "/cut.onnx", TetsURL: srv.URL + "/tets.npz", files: files}
	dir := t.TempDir()
	// no key: the public files come, and it stops at the gated one without asking Hugging Face for it
	err := f.Fetch(context.Background(), EngineSF3D, dir, "  ", func(string) {})
	if err == nil || !strings.Contains(err.Error(), "Hugging Face key") {
		t.Fatalf("without a key: %v", err)
	}
	if _, ok := auth["/"+SF3DRepo+"/resolve/main/config.yaml"]; ok {
		t.Error("Hugging Face was asked for a gated file without a key")
	}
	// a key that the account has not got the licence for
	err = f.Fetch(context.Background(), EngineSF3D, dir, "hf_other", func(string) {})
	if err == nil || !strings.Contains(err.Error(), "accept the licence") {
		t.Fatalf("a refused key: %v", err)
	}
	// the right key
	if err := f.Fetch(context.Background(), EngineSF3D, dir, "hf_key", func(string) {}); err != nil {
		t.Fatal(err)
	}
	if auth["/cut.onnx"] != "" || auth["/tets.npz"] != "" {
		t.Errorf("the key went to a public address: %v", auth)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "sf3d/config.yaml")); string(b) != "config" {
		t.Errorf("config.yaml: %q", b)
	}
	if NeedsKey([]string{"sf3d/160_tets.npz", "rembg/u2net.onnx"}) || !NeedsKey([]string{"sf3d/model.safetensors"}) {
		t.Error("NeedsKey: only the files of the licence need the key")
	}
}
