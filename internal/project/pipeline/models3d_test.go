package pipeline

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"veritmo/internal/assets3d"
	"veritmo/internal/assets3d/generate"
	"veritmo/internal/platform/events"
	"veritmo/internal/platform/musicsvc"
	"veritmo/internal/platform/runner"
	"veritmo/internal/project/config"
	"veritmo/internal/project/export"
)

// tinyGLB is the smallest binary glTF the library takes: one mesh with `indices` indices.
func tinyGLB(indices int) []byte {
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

// fake3D speaks the protocol of docker/model3d/server.py: GET /engines, POST /generate, GET /jobs/{id}, GET /jobs/{id}/model, DELETE /jobs/{id}.
type fake3D struct {
	mu      sync.Mutex
	srv     *httptest.Server
	version string
	// made is what was asked: the engine, the detail and the picture of each model.
	made []fake3DAsk
	// onModel is called after each model is made (a test cancels the run from it).
	onModel func(n int)
	// fail makes every job fail with this sentence.
	fail string
}

type fake3DAsk struct {
	Engine, Detail string
	Picture        []byte
}

func newFake3D(t *testing.T) *fake3D {
	f := &fake3D{version: "triposr@107cefd"}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /engines", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"engines": map[string]any{
			"triposr": map[string]any{"built": true, "ready": true, "version": f.version},
			"sf3d":    map[string]any{"built": true, "ready": true, "version": "sf3d@ff21fc4"},
		}})
	})
	mux.HandleFunc("POST /generate", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		buf := new(bytes.Buffer)
		buf.ReadFrom(r.Body)
		f.made = append(f.made, fake3DAsk{Engine: r.URL.Query().Get("engine"), Detail: r.URL.Query().Get("detail"), Picture: buf.Bytes()})
		n := len(f.made)
		f.mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprintf(w, `{"id":"job%d"}`, n)
	})
	mux.HandleFunc("GET /jobs/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		last := f.made[len(f.made)-1]
		v := map[string]any{"id": r.PathValue("id"), "engine": last.Engine, "engine_version": f.version, "detail": last.Detail, "detail_used": last.Detail, "triangles": 100, "state": "done"}
		if f.fail != "" {
			v["state"], v["error"] = "failed", f.fail
		}
		json.NewEncoder(w).Encode(v)
	})
	mux.HandleFunc("GET /jobs/{id}/model", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		n := len(f.made)
		f.mu.Unlock()
		w.Write(tinyGLB(300 + n)) // different bytes for each model
	})
	// the client drops a job once it has its model: a test cancels the run from here, when the model is safe
	mux.HandleFunc("DELETE /jobs/{id}", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"deleted":true}`)
		f.mu.Lock()
		n, cb := len(f.made), f.onModel
		f.mu.Unlock()
		if cb != nil {
			cb(n)
		}
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fake3D) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.made)
}

type fakeOpen3D struct {
	f      *fake3D
	err    error
	opened []string
}

func (o *fakeOpen3D) Open(ctx context.Context, engine string) (*generate.Client, func(), error) {
	o.opened = append(o.opened, engine)
	if o.err != nil {
		return nil, func() {}, o.err
	}
	return &generate.Client{BaseURL: o.f.srv.URL, PollEvery: time.Millisecond}, func() {}, nil
}

// models3DRun is a models3d project in the current directory, ready to run with a fake `sd` and a fake service.
type models3DRun struct {
	t   *testing.T
	cfg *config.Config
	fr  *runner.FakeRunner
	svc *fake3D
	op  *fakeOpen3D
	lib *assets3d.Library
	// sdSeeds is the seed of each picture the fake sd was asked for, in order; sdPrompts their prompts.
	sdSeeds   []string
	sdPrompts []string
}

func newModels3DRun(t *testing.T, models []config.ModelEntry) *models3DRun {
	t.Helper()
	chdirTemp(t)
	os.MkdirAll("sources", 0o755)
	os.WriteFile("m.safetensors", []byte("stub"), 0o644)
	os.WriteFile(filepath.Join("sources", "gato.png"), []byte("\x89PNG\r\n\x1a\nthe cat"), 0o644)
	r := &models3DRun{t: t, svc: newFake3D(t), lib: assets3d.New(t.TempDir(), nil)}
	r.op = &fakeOpen3D{f: r.svc}
	r.cfg = &config.Config{Project: "objetos", Output: config.Output{Mode: config.ModeModels3D}, Models: models,
		Defaults: config.Defaults{Model: "m.safetensors", Sampler: "euler", Steps: 20}}
	r.fr = runner.NewFakeRunner()
	r.fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
		if inv.Name == "sd" {
			for i, a := range inv.Args {
				switch a {
				case "-o":
					os.MkdirAll(filepath.Dir(inv.Args[i+1]), 0o755)
					os.WriteFile(inv.Args[i+1], []byte("\x89PNG\r\n\x1a\nsd "+strings.Join(inv.Args, " ")), 0o644)
				case "-s":
					r.sdSeeds = append(r.sdSeeds, inv.Args[i+1])
				case "-p":
					r.sdPrompts = append(r.sdPrompts, inv.Args[i+1])
				}
			}
		}
		return runner.ScriptedResult{}
	})
	return r
}

func (r *models3DRun) run(ctx context.Context, opts ...func(*Options)) (string, string, error) {
	r.t.Helper()
	var buf bytes.Buffer
	o := Options{Runner: r.fr, KeepTmp: true, Models3D: &Models3DServices{Service: r.op, Library: r.lib}}
	for _, f := range opts {
		f(&o)
	}
	art, err := Run(ctx, r.cfg, o, events.New(&buf, true))
	return art, buf.String(), err
}

func (r *models3DRun) sdCalls() int {
	n := 0
	for _, c := range r.fr.Calls() {
		if c.Name == "sd" {
			n++
		}
	}
	return n
}

func (r *models3DRun) manifest() export.Models3DManifest {
	r.t.Helper()
	m, err := export.ReadModels3DManifest(filepath.Join("out", r.cfg.Project, "manifest.json"))
	if err != nil {
		r.t.Fatal(err)
	}
	return m
}

func generatedNames(lib *assets3d.Library) []string {
	models, _ := lib.Models(assets3d.Query{Pack: assets3d.GeneratedPackID})
	var names []string
	for _, m := range models {
		names = append(names, m.Name)
	}
	return names
}

func TestModels3D_TwoEntriesMakeTheirFilesTheManifestAndTheLibraryEntries(t *testing.T) {
	r := newModels3DRun(t, []config.ModelEntry{
		{ID: "patin", Prompt: "a retro roller skate, glossy pink"},
		{ID: "gato", Image: "gato.png", Detail: "high"},
	})
	art, events, err := r.run(context.Background())
	if err != nil {
		t.Fatalf("%v\n%s", err, events)
	}
	if art != filepath.Join("out", "objetos", "manifest.json") {
		t.Errorf("artifact = %s", art)
	}
	for _, f := range []string{"patin.glb", "gato.glb"} {
		if b, err := os.ReadFile(filepath.Join("out", "objetos", f)); err != nil || !bytes.HasPrefix(b, []byte("glTF")) {
			t.Errorf("%s: %v", f, err)
		}
	}
	m := r.manifest()
	if m.Mode != "models3d" || m.Project != "objetos" || len(m.Models) != 2 {
		t.Fatalf("manifest = %+v", m)
	}
	p, g := m.Models[0], m.Models[1]
	if p.Entry != "patin" || p.File != filepath.Join("out", "objetos", "patin.glb") || p.Engine != "triposr" || p.EngineVersion != "triposr@107cefd" ||
		p.Detail != "normal" || p.Prompt != "a retro roller skate, glossy pink" || p.Triangles != 100 || p.Key == "" || p.Seed == 0 {
		t.Errorf("the prompt entry: %+v", p)
	}
	if g.Entry != "gato" || g.Picture != "gato.png" || g.Prompt != "" || g.Detail != "high" {
		t.Errorf("the picture entry: %+v", g)
	}
	// the picture of the prompt was made by sd with the completed prompt; the picture of the entry was sent as it is
	if r.sdCalls() != 1 || !strings.Contains(r.sdPrompts[0], "a retro roller skate, glossy pink"+generate.PromptSuffix) {
		t.Errorf("sd calls = %d, prompts = %q", r.sdCalls(), r.sdPrompts)
	}
	if r.svc.made[1].Detail != "high" || !bytes.Contains(r.svc.made[1].Picture, []byte("the cat")) {
		t.Errorf("the entry's own picture goes to the service: %+v", r.svc.made[1])
	}
	// both are in the library, with what made them
	models, _ := r.lib.Models(assets3d.Query{Pack: assets3d.GeneratedPackID})
	if len(models) != 2 {
		t.Fatalf("library: %+v", models)
	}
	byName := map[string]assets3d.ModelView{}
	for _, v := range models {
		byName[v.Name] = v
	}
	if v := byName["patin"]; v.Project != "objetos" || v.Entry != "patin" || v.Source != "generated" || v.Prompt != "a retro roller skate, glossy pink" {
		t.Errorf("patin in the library: %+v", v)
	}
	// phases: the picture phase only for the entry that needs one
	if !strings.Contains(events, `"scene_id":"patin","phase":"render"`) || strings.Contains(events, `"scene_id":"gato","phase":"render"`) ||
		!strings.Contains(events, `"scene_id":"patin","phase":"model3d"`) || !strings.Contains(events, `"scene_id":"gato","phase":"model3d"`) {
		t.Errorf("events:\n%s", events)
	}
}

func TestModels3D_AnUnchangedProjectMakesNothingAgain(t *testing.T) {
	r := newModels3DRun(t, []config.ModelEntry{{ID: "patin", Prompt: "a roller skate"}, {ID: "gato", Image: "gato.png"}})
	if _, ev, err := r.run(context.Background()); err != nil {
		t.Fatalf("%v\n%s", err, ev)
	}
	sd, made := r.sdCalls(), r.svc.count()
	_, ev, err := r.run(context.Background())
	if err != nil {
		t.Fatalf("%v\n%s", err, ev)
	}
	if r.sdCalls() != sd || r.svc.count() != made {
		t.Errorf("an unchanged run made something: sd %d -> %d, models %d -> %d", sd, r.sdCalls(), made, r.svc.count())
	}
	if !strings.Contains(ev, `"type":"skipped","scene_id":"patin","phase":"render"`) {
		t.Errorf("the picture phase is reported as skipped:\n%s", ev)
	}
	if len(r.manifest().Models) != 2 || len(generatedNames(r.lib)) != 2 {
		t.Errorf("the manifest and the library keep both: %+v %v", r.manifest().Models, generatedNames(r.lib))
	}
}

func TestModels3D_ChangingOnePromptMakesOnlyThatModelAndReplacesItsLibraryEntry(t *testing.T) {
	r := newModels3DRun(t, []config.ModelEntry{{ID: "patin", Prompt: "a roller skate"}, {ID: "copa", Prompt: "a golden trophy"}})
	if _, ev, err := r.run(context.Background()); err != nil {
		t.Fatalf("%v\n%s", err, ev)
	}
	before := r.manifest()
	r.cfg.Models[0].Prompt = "a green roller skate"
	if _, ev, err := r.run(context.Background()); err != nil {
		t.Fatalf("%v\n%s", err, ev)
	}
	if r.sdCalls() != 3 || r.svc.count() != 3 {
		t.Errorf("only one entry is made again: sd %d, models %d", r.sdCalls(), r.svc.count())
	}
	after := r.manifest()
	if after.Models[0].Key == before.Models[0].Key || after.Models[1].Key != before.Models[1].Key {
		t.Errorf("keys: %s %s -> %s %s", before.Models[0].Key, before.Models[1].Key, after.Models[0].Key, after.Models[1].Key)
	}
	if after.Models[0].Prompt != "a green roller skate" {
		t.Errorf("manifest: %+v", after.Models[0])
	}
	if names := generatedNames(r.lib); len(names) != 2 {
		t.Errorf("the library holds the model once, replaced: %v", names)
	}
	if v, _ := r.lib.Model("generated", "patin"); v.Prompt != "a green roller skate" {
		t.Errorf("the library entry was replaced: %+v", v)
	}
}

func TestModels3D_ChangingTheDetailTheEngineOrItsVersionMakesTheModelAgain(t *testing.T) {
	r := newModels3DRun(t, []config.ModelEntry{{ID: "patin", Prompt: "a roller skate"}})
	r.run(context.Background())
	r.cfg.Models[0].Detail = "high"
	r.run(context.Background())
	if r.svc.count() != 2 || r.sdCalls() != 1 {
		t.Errorf("a new detail: the picture stays, the model is made again: sd %d models %d", r.sdCalls(), r.svc.count())
	}
	r.svc.version = "triposr@aaaaaaa"
	r.run(context.Background())
	if r.svc.count() != 3 {
		t.Errorf("a new engine version: models %d", r.svc.count())
	}
	r.cfg.Models[0].Engine = "sf3d"
	r.run(context.Background())
	if r.svc.count() != 4 || r.svc.made[3].Engine != "sf3d" {
		t.Errorf("another engine: %+v", r.svc.made)
	}
}

func TestModels3D_ForceMakesEverythingAgain(t *testing.T) {
	r := newModels3DRun(t, []config.ModelEntry{{ID: "patin", Prompt: "a roller skate"}})
	r.run(context.Background())
	r.run(context.Background(), func(o *Options) { o.Force = true })
	if r.svc.count() != 2 {
		t.Errorf("--force: %d models", r.svc.count())
	}
}

func TestModels3D_VariantsMakeModelsWithDifferentSeeds(t *testing.T) {
	three := 3
	seed := 100
	r := newModels3DRun(t, []config.ModelEntry{{ID: "copa", Prompt: "a golden trophy", Variants: &three, Seed: &seed}})
	if _, ev, err := r.run(context.Background()); err != nil {
		t.Fatalf("%v\n%s", err, ev)
	}
	for _, f := range []string{"copa.glb", "copa-2.glb", "copa-3.glb"} {
		if _, err := os.Stat(filepath.Join("out", "objetos", f)); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
	if strings.Join(r.sdSeeds, ",") != "100,101,102" {
		t.Errorf("seeds = %v", r.sdSeeds)
	}
	m := r.manifest()
	if len(m.Models) != 3 || m.Models[0].Variant != 0 || m.Models[2].Variant != 2 || m.Models[2].Name != "copa-3" || m.Models[2].Seed != 102 {
		t.Errorf("manifest: %+v", m.Models)
	}
	if names := generatedNames(r.lib); strings.Join(names, ",") != "copa,copa-2,copa-3" {
		t.Errorf("library: %v", names)
	}
	// the three pictures were different, so the three keys are
	if m.Models[0].Key == m.Models[1].Key || m.Models[1].Key == m.Models[2].Key {
		t.Errorf("keys: %s %s %s", m.Models[0].Key[:8], m.Models[1].Key[:8], m.Models[2].Key[:8])
	}
}

func TestModels3D_ThePromptGivesTheSeedWhenTheEntryHasNone(t *testing.T) {
	r := newModels3DRun(t, []config.ModelEntry{{ID: "a", Prompt: "same prompt"}, {ID: "b", Prompt: "same prompt"}, {ID: "c", Prompt: "another prompt"}})
	if _, ev, err := r.run(context.Background()); err != nil {
		t.Fatalf("%v\n%s", err, ev)
	}
	if r.sdSeeds[0] != r.sdSeeds[1] || r.sdSeeds[0] == r.sdSeeds[2] {
		t.Errorf("seeds = %v", r.sdSeeds)
	}
	if _, err := strconv.ParseInt(r.sdSeeds[0], 10, 64); err != nil {
		t.Errorf("seed %q: %v", r.sdSeeds[0], err)
	}
}

func TestModels3D_AServiceThatIsDownRefusesTheRunBeforeAnythingIsMade(t *testing.T) {
	r := newModels3DRun(t, []config.ModelEntry{{ID: "patin", Prompt: "a roller skate"}})
	r.op.err = &musicsvc.UnavailableError{Reason: musicsvc.ReasonNotRunning, Detail: "the 3D service is not running"}
	_, ev, err := r.run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not_running") || !strings.Contains(err.Error(), "triposr") {
		t.Fatalf("err = %v", err)
	}
	if r.sdCalls() != 0 || r.svc.count() != 0 {
		t.Errorf("something was made: sd %d, models %d", r.sdCalls(), r.svc.count())
	}
	if _, err := os.Stat(filepath.Join("out", "objetos", "patin.glb")); err == nil {
		t.Errorf("a model was written")
	}
	if !strings.Contains(ev, `"type":"error"`) {
		t.Errorf("the refusal is an error event:\n%s", ev)
	}
}

func TestModels3D_ARunWithoutAServiceSaysSo(t *testing.T) {
	r := newModels3DRun(t, []config.ModelEntry{{ID: "patin", Prompt: "a roller skate"}})
	_, _, err := r.run(context.Background(), func(o *Options) { o.Models3D = nil })
	if err == nil || !strings.Contains(err.Error(), "no 3D service") {
		t.Errorf("err = %v", err)
	}
}

func TestModels3D_CancellingLeavesTheModelsAlreadyMade(t *testing.T) {
	r := newModels3DRun(t, []config.ModelEntry{{ID: "a", Prompt: "first"}, {ID: "b", Prompt: "second"}})
	ctx, cancel := context.WithCancel(context.Background())
	r.svc.onModel = func(n int) {
		if n == 1 {
			cancel() // stops the run while the second entry has not begun
		}
	}
	_, _, err := r.run(ctx)
	if err == nil || !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "cancel") {
		t.Fatalf("err = %v", err)
	}
	if _, serr := os.Stat(filepath.Join("out", "objetos", "a.glb")); serr != nil {
		t.Errorf("the first model stays: %v", serr)
	}
	if _, serr := os.Stat(filepath.Join("out", "objetos", "b.glb")); serr == nil {
		t.Errorf("the second was not made")
	}
	m := r.manifest()
	if len(m.Models) != 1 || m.Models[0].Entry != "a" {
		t.Errorf("manifest of what was made: %+v", m.Models)
	}
	// run again: the first is kept, the second is made
	r.svc.onModel = nil
	if _, ev, err := r.run(context.Background()); err != nil {
		t.Fatalf("%v\n%s", err, ev)
	}
	if r.svc.count() != 2 || len(r.manifest().Models) != 2 {
		t.Errorf("after the cancel: models %d, manifest %d", r.svc.count(), len(r.manifest().Models))
	}
}

func TestModels3D_AFailureOfTheServiceNamesTheModelAndKeepsWhatWasMade(t *testing.T) {
	r := newModels3DRun(t, []config.ModelEntry{{ID: "a", Image: "gato.png"}})
	r.svc.fail = "CUDA out of memory"
	_, ev, err := r.run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "the model a could not be made") || !strings.Contains(err.Error(), "CUDA out of memory") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(ev, `"type":"error"`) {
		t.Errorf("events:\n%s", ev)
	}
}

func TestModels3D_OnlyMakesOneEntryAndKeepsTheLinesOfTheOthers(t *testing.T) {
	r := newModels3DRun(t, []config.ModelEntry{{ID: "a", Prompt: "first"}, {ID: "b", Prompt: "second"}})
	if _, ev, err := r.run(context.Background()); err != nil {
		t.Fatalf("%v\n%s", err, ev)
	}
	r.cfg.Models[1].Prompt = "second, changed"
	if _, ev, err := r.run(context.Background(), func(o *Options) { o.Only = "a" }); err != nil {
		t.Fatalf("%v\n%s", err, ev)
	}
	if r.svc.count() != 2 {
		t.Errorf("--only a: b is not touched: %d models", r.svc.count())
	}
	if m := r.manifest(); len(m.Models) != 2 || m.Models[1].Prompt != "second" {
		t.Errorf("the line of b is what it was: %+v", m.Models)
	}
}

func TestModels3D_TheAdultMarkGoesToTheManifestAndTheLibrary(t *testing.T) {
	r := newModels3DRun(t, []config.ModelEntry{{ID: "a", Prompt: "first", Adult: true}, {ID: "b", Prompt: "second"}})
	if _, ev, err := r.run(context.Background()); err != nil {
		t.Fatalf("%v\n%s", err, ev)
	}
	m := r.manifest()
	if !m.Models[0].Adult || m.Models[1].Adult {
		t.Errorf("manifest: %+v", m.Models)
	}
	if v, _ := r.lib.Model("generated", "a"); !v.Adult {
		t.Errorf("library: %+v", v)
	}
	// changing the mark does not make the model again, it changes the mark
	r.cfg.Models[0].Adult = false
	if _, ev, err := r.run(context.Background()); err != nil {
		t.Fatalf("%v\n%s", err, ev)
	}
	if r.svc.count() != 2 {
		t.Errorf("a new mark made a model again: %d", r.svc.count())
	}
	if v, _ := r.lib.Model("generated", "a"); v.Adult || r.manifest().Models[0].Adult {
		t.Errorf("the mark was not changed: %+v", v)
	}
}

func TestModels3D_PhasesBinariesAndTheOnlyCheck(t *testing.T) {
	phases, concat := PhasePlan(config.ModeModels3D)
	if strings.Join(phases, ",") != "render,model3d" || concat {
		t.Errorf("phases = %v %v", phases, concat)
	}
	cfg := &config.Config{Output: config.Output{Mode: config.ModeModels3D}, Models: []config.ModelEntry{{ID: "a", Prompt: "x"}, {ID: "b", Image: "b.png"}}}
	if got := RequiredBinariesFor(cfg, ""); strings.Join(got, ",") != "sd" {
		t.Errorf("a prompt needs sd: %v", got)
	}
	if got := RequiredBinariesFor(cfg, "b"); len(got) != 0 {
		t.Errorf("only an entry with its own picture needs no program: %v", got)
	}
	if err := CheckOnlyScene(cfg, "b"); err != nil {
		t.Error(err)
	}
	if err := CheckOnlyScene(cfg, "zz"); err == nil || !strings.Contains(err.Error(), `model "zz" not found`) {
		t.Errorf("err = %v", err)
	}
	if err := ApplyModeOverride(cfg, config.ModeModels3D); err != nil {
		t.Error(err)
	}
}
