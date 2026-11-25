package library3d

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"veritmo/internal/assets3d"
	"veritmo/internal/assets3d/generate"
	"veritmo/internal/platform/events"
	"veritmo/internal/platform/musicsvc"
	"veritmo/internal/project/config"
	"veritmo/internal/server/servertest"
	"veritmo/internal/server/servertest/stack"
	"veritmo/internal/server/workspace"
)

// service3D speaks the protocol of docker/model3d/server.py, as an external service (the way the compose stack runs it).
type service3D struct {
	mu       sync.Mutex
	srv      *httptest.Server
	ready    bool
	asked    []string // the engine and the detail of each model asked for
	pictures [][]byte
}

func newService3D(t *testing.T) *service3D {
	f := &service3D{ready: true}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		missing := []string{}
		if !f.ready {
			missing = []string{"triposr/model.ckpt"}
		}
		json.NewEncoder(w).Encode(map[string]any{"ready": f.ready, "version": "1", "missing": missing, "gpu": map[string]any{"name": "x", "free_mb": 9000, "total_mb": 12000}})
	})
	mux.HandleFunc("GET /engines", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"engines": {"triposr": {"built": true, "missing": [], "ready": true, "version": "triposr@107cefd"}, "sf3d": {"built": true, "missing": [], "ready": true, "version": "sf3d@ff21fc4"}}}`)
	})
	mux.HandleFunc("POST /generate", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		buf := new(bytes.Buffer)
		buf.ReadFrom(r.Body)
		f.asked = append(f.asked, r.URL.Query().Get("engine")+"/"+r.URL.Query().Get("detail"))
		f.pictures = append(f.pictures, buf.Bytes())
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprintf(w, `{"id":"job%d"}`, len(f.asked))
	})
	mux.HandleFunc("GET /jobs/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		last := strings.SplitN(f.asked[len(f.asked)-1], "/", 2)
		json.NewEncoder(w).Encode(map[string]any{"id": r.PathValue("id"), "state": "done", "engine": last[0], "engine_version": "triposr@107cefd", "detail": last[1], "detail_used": last[1], "triangles": 10})
	})
	mux.HandleFunc("GET /jobs/{id}/model", func(w http.ResponseWriter, r *http.Request) {
		w.Write(glb(`{"accessors":[{"count":30}],"meshes":[{"primitives":[{"attributes":{"POSITION":0},"indices":0}]}]}`))
	})
	mux.HandleFunc("DELETE /jobs/{id}", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"deleted":true}`) })
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *service3D) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.asked)
}

// genHost is what the server gives the handler: it shows no adult content (the stack's Adult is false) and draws pictures with a model of its own.
type genHost struct {
	st      *stack.Stack
	noModel bool
}

func (h genHost) ShowsAdult() bool { return h.st.Adult }
func (h genHost) RefuseAdultConfig(w http.ResponseWriter, adult *bool) bool {
	return h.st.RefuseAdult(w, adult)
}
func (h genHost) ProjectVisible(string) bool { return true }
func (h genHost) PictureDefaults() (config.Defaults, error) {
	if h.noModel {
		return config.Defaults{}, fmt.Errorf("no image model is installed that fits this machine")
	}
	return config.Defaults{Model: "models/m.safetensors", Sampler: "euler", Resolution: "768x768", Steps: 20}, nil
}

// genStack is the handler over a real workspace (and its run queue) and a service that is another process.
type genStack struct {
	t *testing.T
	*stack.Stack
	h    *Handler
	lib  *assets3d.Library
	svc  *service3D
	host *genHost
}

func newGenStack(t *testing.T) *genStack {
	t.Helper()
	servertest.IsolateSettings(t)
	st := stack.New(t, nil)
	lib := assets3d.New(t.TempDir(), nil)
	svc := newService3D(t)
	g := &genStack{t: t, Stack: st, lib: lib, svc: svc, host: &genHost{st: st}}
	service := &generate.Service{External: musicsvc.External{Host: svc.srv.URL, Consented: true}, NoStart: true}
	g.h = New(lib)
	g.h.SetHost(g.host)
	g.h.SetWorkspace(st.Workspace)
	g.h.SetService(service)
	st.Workspace.SetModelLibrary(lib)
	st.Workspace.SetModelService(service)
	st.Register(g.h)
	st.Finish()
	return g
}

func pngBytes() []byte {
	var buf bytes.Buffer
	_ = png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 8, 8)))
	return buf.Bytes()
}

func (g *genStack) postJSON(body any) *httptest.ResponseRecorder {
	return servertest.DoJSON(g.t, g.Handler(), http.MethodPost, "/api/models3d/generations", body)
}

func (g *genStack) postMultipart(t *testing.T, fields map[string]string, picture []byte) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		mw.WriteField(k, v)
	}
	if picture != nil {
		fw, _ := mw.CreateFormFile("image", "gato.png")
		fw.Write(picture)
	}
	mw.Close()
	r := httptest.NewRequest(http.MethodPost, "/api/models3d/generations", &buf)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	g.Handler().ServeHTTP(rec, r)
	return rec
}

func (g *genStack) get(path string) *httptest.ResponseRecorder {
	return servertest.Call(g.Handler(), http.MethodGet, path, "")
}

func decodeGen(t *testing.T, rec *httptest.ResponseRecorder) generationView {
	t.Helper()
	var v generationView
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("not a generation: %v: %s", err, rec.Body)
	}
	return v
}

// waitState polls a generation until its state is one of the states.
func (g *genStack) waitState(t *testing.T, id string, states ...string) generationView {
	t.Helper()
	var v generationView
	servertest.WaitUntil(t, func() bool {
		rec := g.get("/api/models3d/generations/" + id)
		if rec.Code != http.StatusOK {
			return false
		}
		v = decodeGen(t, rec)
		for _, s := range states {
			if v.State == s {
				return true
			}
		}
		return false
	})
	return v
}

func TestGeneration_AFromAPictureMakesTheModelThroughTheRunQueueAndKeepsItWhenDeleted(t *testing.T) {
	g := newGenStack(t)
	rec := g.postMultipart(t, map[string]string{"detail": "low", "client": "studio", "name": "gato"}, pngBytes())
	if rec.Code != http.StatusAccepted {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	v := decodeGen(t, rec)
	if !strings.HasPrefix(v.ID, "api-m3d-") || v.Client != "studio" || v.Engine != "triposr" || v.Detail != "low" || v.Name != "gato" || v.Run == "" {
		t.Fatalf("%+v", v)
	}
	done := g.waitState(t, v.ID, genDone, genFailed)
	if done.State != genDone || done.Model != "generated/gato" || done.FinishedAt == nil || done.Error != "" {
		t.Fatalf("%+v", done)
	}
	if g.svc.count() != 1 || g.svc.asked[0] != "triposr/low" || !bytes.Equal(g.svc.pictures[0], pngBytes()) {
		t.Errorf("the service was asked: %v", g.svc.asked)
	}
	// the model is in the catalog with the generation that made it
	rec = g.get("/api/models3d/models?project=" + v.ID)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"name":"gato"`) || !strings.Contains(rec.Body.String(), `"source":"generated"`) {
		t.Errorf("catalog: %d %s", rec.Code, rec.Body)
	}
	// the generation is a managed project: only `managed=include` lists it, with its kind and client
	if rec := g.get("/api/projects"); strings.Contains(rec.Body.String(), v.ID) {
		t.Errorf("a hidden project is listed: %s", rec.Body)
	}
	rec = g.get("/api/projects?managed=include")
	if !strings.Contains(rec.Body.String(), v.ID) || !strings.Contains(rec.Body.String(), `"kind":"model_generation"`) || !strings.Contains(rec.Body.String(), `"mode":"models3d"`) ||
		!strings.Contains(rec.Body.String(), `"client":"studio"`) {
		t.Errorf("managed=include: %s", rec.Body)
	}
	// its run is in the history under the mode models3d
	rec = g.get("/api/runs?mode=models3d")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), v.Run) {
		t.Errorf("runs by mode: %d %s", rec.Code, rec.Body)
	}
	// deleting the generation removes its project and not its model
	if rec := servertest.Call(g.Handler(), http.MethodDelete, "/api/models3d/generations/"+v.ID, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if rec := g.get("/api/models3d/generations/" + v.ID); rec.Code != http.StatusNotFound {
		t.Errorf("after delete: %d", rec.Code)
	}
	if _, err := os.Stat(g.Workspace.ProjectDir(v.ID)); err == nil {
		t.Errorf("the project folder is still there")
	}
	if m, err := g.lib.Model("generated", "gato"); err != nil || m.Project != v.ID {
		t.Errorf("the model stays: %+v %v", m, err)
	}
}

func TestGeneration_ThePromptNameDefaultsAndTheJSONForm(t *testing.T) {
	g := newGenStack(t)
	// a prompt is drawn by the image model of the machine: the run is replaced by one that walks the steps and puts the model in the library
	release := make(chan struct{})
	g.Workspace.Execute = func(ctx context.Context, rn workspace.ExecRun, em *events.Emitter) (string, error) {
		em.PipelineStart()
		if rn.Cfg.Output.Mode != config.ModeModels3D || len(rn.Cfg.Models) != 1 || rn.Cfg.Defaults.Model != "models/m.safetensors" {
			t.Errorf("the project of the call: %+v %+v", rn.Cfg.Output, rn.Cfg.Defaults)
		}
		e := rn.Cfg.Models[0]
		if e.Prompt != "a golden trophy cup" || e.Engine != "triposr" || e.Detail != "normal" || e.ID != "a-golden-trophy-cup" || rn.Cfg.Client != "" && rn.Cfg.Client != "unknown" {
			t.Errorf("the entry: %+v client %q", e, rn.Cfg.Client)
		}
		em.PhaseStart(e.ID, "render")
		<-release
		em.PhaseEnd(e.ID, "render", "x.png")
		em.PhaseStart(e.ID, "model3d")
		<-release
		_, err := g.lib.AddGenerated(glb(`{"accessors":[{"count":30}],"meshes":[{"primitives":[{"attributes":{"POSITION":0},"indices":0}]}]}`),
			assets3d.GenMeta{Name: e.ID, Prompt: e.Prompt, Engine: "triposr", Licence: "MIT", Project: rn.Rec.Project, Entry: e.ID})
		em.PhaseEnd(e.ID, "model3d", "m.glb")
		em.Done("out/" + rn.Rec.Project + "/manifest.json")
		return "out/" + rn.Rec.Project + "/manifest.json", err
	}
	rec := g.postJSON(map[string]any{"prompt": "a golden trophy cup"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	v := decodeGen(t, rec)
	if v.Client != "unknown" || v.Name != "a-golden-trophy-cup" || v.Prompt != "a golden trophy cup" {
		t.Errorf("%+v", v)
	}
	if got := g.waitStep(t, v.ID, "picture"); got.State != genRunning {
		t.Errorf("%+v", got)
	}
	release <- struct{}{}
	g.waitStep(t, v.ID, "model")
	release <- struct{}{}
	done := g.waitState(t, v.ID, genDone, genFailed)
	if done.State != genDone || done.Model != "generated/a-golden-trophy-cup" || done.Step != "" {
		t.Errorf("%+v", done)
	}
}

func (g *genStack) waitStep(t *testing.T, id, step string) generationView {
	t.Helper()
	var v generationView
	servertest.WaitUntil(t, func() bool {
		v = decodeGen(t, g.get("/api/models3d/generations/"+id))
		return v.Step == step
	})
	return v
}

func TestGeneration_Refusals(t *testing.T) {
	g := newGenStack(t)
	long := strings.Repeat("x", 1001)
	for _, tc := range []struct {
		name string
		body map[string]any
		want string
	}{
		{"neither", map[string]any{}, "send a prompt or a picture"},
		{"an empty prompt", map[string]any{"prompt": "   "}, "send a prompt or a picture"},
		{"a long prompt", map[string]any{"prompt": long}, "longer than 1000"},
		{"an unknown engine", map[string]any{"prompt": "x", "engine": "nerf"}, "engine must be one of triposr, sf3d"},
		{"an unknown detail", map[string]any{"prompt": "x", "detail": "ultra"}, "detail must be low, normal or high"},
		{"a name that is not plain", map[string]any{"prompt": "x", "name": "Mi Modelo"}, "name"},
		{"a client that is not one", map[string]any{"prompt": "x", "client": "Studio One"}, "client"},
		{"an unknown field", map[string]any{"prompt": "x", "colour": "red"}, "not a generation request"},
		{"an image in the JSON body", map[string]any{"image": "gato.png"}, "multipart"},
	} {
		rec := g.postJSON(tc.body)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tc.want) {
			t.Errorf("%s: %d %s", tc.name, rec.Code, rec.Body)
		}
	}
	// a prompt and a picture together
	if rec := g.postMultipart(t, map[string]string{"prompt": "x"}, pngBytes()); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "not both") {
		t.Errorf("both: %d %s", rec.Code, rec.Body)
	}
	// a picture that is not one
	if rec := g.postMultipart(t, nil, []byte("not a picture")); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "png, jpg or webp") {
		t.Errorf("not a picture: %d %s", rec.Code, rec.Body)
	}
	if rec := g.postMultipart(t, map[string]string{"adult": "maybe"}, pngBytes()); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "adult") {
		t.Errorf("adult: %d %s", rec.Code, rec.Body)
	}
	// nothing was queued by any of them
	if rec := g.get("/api/models3d/generations"); strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Errorf("a refusal left something: %s", rec.Body)
	}
}

func TestGeneration_NothingIsQueuedWhenTheEngineOrTheImageModelCannotBeUsed(t *testing.T) {
	g := newGenStack(t)
	g.svc.mu.Lock()
	g.svc.ready = false
	g.svc.mu.Unlock()
	rec := g.postMultipart(t, nil, pngBytes())
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), `"code":"unavailable"`) {
		t.Fatalf("engine not ready: %d %s", rec.Code, rec.Body)
	}
	g.svc.mu.Lock()
	g.svc.ready = true
	g.svc.mu.Unlock()
	g.host.noModel = true
	rec = g.postJSON(map[string]any{"prompt": "a lamp"})
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "no_image_model") {
		t.Fatalf("no image model: %d %s", rec.Code, rec.Body)
	}
	// a picture needs no image model
	if rec := g.postMultipart(t, nil, pngBytes()); rec.Code != http.StatusAccepted {
		t.Errorf("a picture needs no image model: %d %s", rec.Code, rec.Body)
	}
}

func TestGeneration_AdultIsRefusedByACensoredServerAndAcceptedByOneThatShowsIt(t *testing.T) {
	g := newGenStack(t)
	rec := g.postJSON(map[string]any{"prompt": "x", "adult": true})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "content_not_allowed") {
		t.Fatalf("censored: %d %s", rec.Code, rec.Body)
	}
	if rec := g.postJSON(map[string]any{"prompt": "x", "adult": false}); rec.Code != http.StatusAccepted {
		t.Errorf("adult:false: %d %s", rec.Code, rec.Body)
	}
	g2 := newGenStack(t)
	g2.Adult = true
	g2.Workspace.Execute = func(ctx context.Context, rn workspace.ExecRun, em *events.Emitter) (string, error) {
		if rn.Cfg.Adult == nil || !*rn.Cfg.Adult || !rn.Cfg.Models[0].Adult {
			t.Errorf("the mark goes to the project and its entry: %+v", rn.Cfg.Adult)
		}
		return "", nil
	}
	if rec := g2.postJSON(map[string]any{"prompt": "x", "adult": true}); rec.Code != http.StatusAccepted {
		t.Errorf("an open server: %d %s", rec.Code, rec.Body)
	}
}

func TestGeneration_ReadListCancelAndTheirRefusals(t *testing.T) {
	g := newGenStack(t)
	block := make(chan struct{})
	g.Workspace.Execute = func(ctx context.Context, rn workspace.ExecRun, em *events.Emitter) (string, error) {
		em.PipelineStart()
		select {
		case <-block:
			return "out/x/manifest.json", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	mk := func(client string) generationView {
		rec := g.postMultipart(t, map[string]string{"client": client}, pngBytes())
		if rec.Code != http.StatusAccepted {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		return decodeGen(t, rec)
	}
	a := mk("studio")
	g.waitState(t, a.ID, genRunning)
	time.Sleep(5 * time.Millisecond) // the ids carry the second they were made in and a random part: two calls are two ids
	b := mk("other")
	// the second waits for the first: one heavy process at a time
	if v := decodeGen(t, g.get("/api/models3d/generations/"+b.ID)); v.State != genQueued {
		t.Errorf("the second: %+v", v)
	}
	// the list: newest first, filterable by client, pageable
	var list []generationView
	json.Unmarshal(g.get("/api/models3d/generations").Body.Bytes(), &list)
	if len(list) != 2 {
		t.Fatalf("list: %+v", list)
	}
	rec := g.get("/api/models3d/generations?client=studio")
	var one []generationView
	json.Unmarshal(rec.Body.Bytes(), &one)
	if len(one) != 1 || one[0].ID != a.ID {
		t.Errorf("client=studio: %+v %v", one, rec.Header())
	}
	if rec := g.get("/api/models3d/generations?limit=1"); rec.Header().Get("X-Total-Count") != "2" || strings.Count(rec.Body.String(), `"id":"api-m3d-`) != 1 {
		t.Errorf("limit=1: %v %s", rec.Header(), rec.Body)
	}
	for _, q := range []string{"client=Bad%20One", "sort=nothing", "limit=x"} {
		if rec := g.get("/api/models3d/generations?" + q); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", q, rec.Code)
		}
	}
	// cancel: a running one and a queued one; once stopped it cannot be stopped again
	for _, id := range []string{a.ID, b.ID} {
		rec := servertest.Call(g.Handler(), http.MethodPost, "/api/models3d/generations/"+id+"/cancel", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("cancel %s: %d %s", id, rec.Code, rec.Body)
		}
		if v := g.waitState(t, id, genCancelled); v.Error != "cancelled" {
			t.Errorf("%+v", v)
		}
		if rec := servertest.Call(g.Handler(), http.MethodPost, "/api/models3d/generations/"+id+"/cancel", ""); rec.Code != http.StatusConflict {
			t.Errorf("cancel again: %d %s", rec.Code, rec.Body)
		}
	}
	for _, path := range []string{"/api/models3d/generations/api-m3d-nothing", "/api/models3d/generations/api-gen-20260101-000000-abcdef", "/api/models3d/generations/x"} {
		if rec := g.get(path); rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d", path, rec.Code)
		}
	}
	if rec := servertest.Call(g.Handler(), http.MethodPost, "/api/models3d/generations/api-m3d-nothing/cancel", ""); rec.Code != http.StatusNotFound {
		t.Errorf("cancel of nothing: %d", rec.Code)
	}
	if rec := servertest.Call(g.Handler(), http.MethodDelete, "/api/models3d/generations/api-m3d-nothing", ""); rec.Code != http.StatusNotFound {
		t.Errorf("delete of nothing: %d", rec.Code)
	}
	close(block)
}

func TestGeneration_AFailedRunSaysWhy(t *testing.T) {
	g := newGenStack(t)
	g.Workspace.Execute = func(ctx context.Context, rn workspace.ExecRun, em *events.Emitter) (string, error) {
		em.PipelineStart()
		em.Error("_global", "the model a could not be made: CUDA out of memory")
		return "", fmt.Errorf("the model a could not be made: CUDA out of memory")
	}
	v := decodeGen(t, g.postMultipart(t, nil, pngBytes()))
	done := g.waitState(t, v.ID, genFailed)
	if !strings.Contains(done.Error, "CUDA out of memory") || done.Model != "" {
		t.Errorf("%+v", done)
	}
}

func TestGeneration_TheRoutesNeedAWorkspace(t *testing.T) {
	h := New(assets3d.New(t.TempDir(), nil))
	mux := http.NewServeMux()
	h.Register(func(p string, f http.HandlerFunc) { mux.HandleFunc(p, f) })
	for _, c := range []struct{ method, path string }{{http.MethodGet, "/api/models3d/generations"}, {http.MethodPost, "/api/models3d/generations"}, {http.MethodGet, "/api/models3d/generations/x"}} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(c.method, c.path, strings.NewReader("{}")))
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s: %d", c.method, c.path, rec.Code)
		}
	}
}

// While a run is making 3D models the backend cannot change and the engine cannot be stopped (the service would go away under it): 409 for both,
// and once the run has ended nothing stands in the way.
func TestGeneration_TheBackendAndTheStopWaitForARunThatMakesModels(t *testing.T) {
	g := newGenStack(t)
	release := make(chan struct{})
	g.Workspace.Execute = func(ctx context.Context, rn workspace.ExecRun, em *events.Emitter) (string, error) {
		em.PipelineStart()
		<-release
		em.Done("out/" + rn.Rec.Project + "/manifest.json")
		return "out/" + rn.Rec.Project + "/manifest.json", nil
	}
	// a handler whose service runs here (the one of the stack is external, and an external one has nothing to change)
	local := New(g.lib)
	local.SetWorkspace(g.Workspace)
	local.SetService(&generate.Service{NoStart: true})

	rec := g.postMultipart(t, map[string]string{"name": "gato"}, pngBytes())
	if rec.Code != http.StatusAccepted {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	v := decodeGen(t, rec)
	g.waitState(t, v.ID, genRunning)

	if err := local.PrepareBackend(context.Background(), "dotnet"); err == nil || err.Status != http.StatusConflict || !strings.Contains(err.Msg, "making 3D models") {
		t.Errorf("the backend changes under a run: %+v", err)
	}
	stop := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/models3d/engines/triposr/stop", nil)
	req.SetPathValue("engine", "triposr")
	local.handleEngineStop(stop, req)
	if stop.Code != http.StatusConflict || !strings.Contains(stop.Body.String(), "making 3D models") {
		t.Errorf("the engine stops under a run: %d %s", stop.Code, stop.Body)
	}

	release <- struct{}{}
	g.waitState(t, v.ID, genDone, genFailed)
	servertest.WaitUntil(t, func() bool { return !g.Workspace.UsesModels3D() })
	if local.generating() {
		t.Errorf("the run has ended")
	}
}
