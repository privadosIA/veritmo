package music

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"veritmo/internal/music/clap"
	"veritmo/internal/platform/musicsvc"
	"veritmo/internal/platform/runner"
)

// clapServer is listenSetup plus a CLAP service that is running and answers /classify with fixed tags. It
// returns what CLAP was asked.
func clapServer(t *testing.T, running bool) (*testServer, *[]string, *clap.Result) {
	t.Helper()
	isolateSettings(t)
	s, _ := newProjectsServer(t)
	asked := listenSetup(t, s, sungWords, 20)

	reply := &clap.Result{Categories: map[string][]clap.Tag{
		"genre":      {{Tag: "techno", Score: 0.6}, {Tag: "pop", Score: 0.5}, {Tag: "rock", Score: 0.4}, {Tag: "jazz", Score: 0.3}},
		"mood":       {{Tag: "energetic", Score: 0.5}, {Tag: "dark", Score: 0.4}, {Tag: "sad", Score: 0.3}},
		"instrument": {{Tag: "synth", Score: 0.5}, {Tag: "drums", Score: 0.4}, {Tag: "bass", Score: 0.3}, {Tag: "piano", Score: 0.2}, {Tag: "harp", Score: 0.1}},
	}}
	svc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			json.NewEncoder(w).Encode(musicsvc.Health{Ready: true, Version: clap.Version, GPU: &musicsvc.GPU{Name: "x", FreeMB: 9000, TotalMB: 12000}})
		case "/classify":
			var req struct {
				WAV    string              `json:"wav_base64"`
				Labels map[string][]string `json:"labels"`
			}
			json.NewDecoder(r.Body).Decode(&req)
			if req.WAV == "" || len(req.Labels["genre"]) == 0 || len(req.Labels["instrument"]) == 0 {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			json.NewEncoder(w).Encode(reply)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(svc.Close)
	u, _ := url.Parse(svc.URL)
	port, _ := strconv.Atoi(u.Port())

	installServices(t, s)
	fr := runner.NewFakeRunner()
	fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
		if inv.Name == "nvidia-smi" {
			return runner.ScriptedResult{Stdout: []byte("1, NVIDIA GeForce RTX 5060 Ti, 15900, 16311\n")}
		}
		return runner.ScriptedResult{}
	})
	s.heartMulaRunner = fr
	if running {
		// A service started by hand, listening on the fake CLAP's port: the program only needs its state.
		t.Setenv("VERITMO_FAKE_SERVICE", "silent")
		startFake(t, s, clap.Spec(), port)
	}
	s.clapWeights = t.TempDir()
	snap := filepath.Join(s.clapWeights, "hub", "models--laion--clap-htsat-unfused", "snapshots", "abc")
	if err := os.MkdirAll(snap, 0o755); err != nil {
		t.Fatal(err)
	}
	for f, body := range map[string]string{"config.json": "{}", "pytorch_model.bin": "weights"} {
		if err := os.WriteFile(filepath.Join(snap, f), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s.engines.ttl = 1
	return s, asked, reply
}

func TestListen_WithClapTheAssistantIsGivenTheSoundTags(t *testing.T) {
	s, asked, _ := clapServer(t, true)
	code, body := postAudio(s, "/api/assist/listen?method=clap", []byte("webm-bytes"))
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	var a listenAnswer
	if err := json.Unmarshal([]byte(body), &a); err != nil {
		t.Fatal(err)
	}
	// 3 genres, 2 moods and 4 instruments, closest first
	if strings.Join(a.Heard, ",") != "techno,pop,rock,energetic,dark,synth,drums,bass,piano" {
		t.Errorf("heard = %v", a.Heard)
	}
	if len(*asked) != 1 || !strings.Contains((*asked)[0], "Sound tags") || !strings.Contains((*asked)[0], "techno, pop, rock") {
		t.Errorf("the assistant is told what CLAP heard: %v", *asked)
	}
}

func TestListen_WithoutClapNothingIsAskedOfIt(t *testing.T) {
	s, asked, _ := clapServer(t, true)
	code, body := postAudio(s, "/api/assist/listen", []byte("webm-bytes"))
	if code != http.StatusOK || strings.Contains(body, `"heard"`) || strings.Contains((*asked)[0], "Sound tags") {
		t.Fatalf("%d %s %v", code, body, *asked)
	}
}

func TestListen_ClapStoppedIsA503WithTheReason(t *testing.T) {
	s, asked, _ := clapServer(t, false)
	code, body := postAudio(s, "/api/assist/listen?method=clap", []byte("x"))
	if code != http.StatusServiceUnavailable || !strings.Contains(body, `"reason":"not_running"`) {
		t.Fatalf("%d %s", code, body)
	}
	if len(*asked) != 0 {
		t.Errorf("the assistant must not run: %v", *asked)
	}
}

// muqSetup installs a fake diffrhythm2 executable and the converted audio weights, and makes the media runner answer
// `classify` with the JSON of the real one. It returns the invocations of the executable.
func muqSetup(t *testing.T, s *testServer, withWeights bool) *[]runner.Invocation {
	t.Helper()
	services := t.TempDir()
	if err := os.MkdirAll(filepath.Join(services, "diffrhythm2"), 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(services, "diffrhythm2", "diffrhythm2")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	s.Services = &musicsvc.ServiceHost{Dir: services}
	s.diffRhythm2Weights = t.TempDir()
	if withWeights {
		if err := os.WriteFile(filepath.Join(s.diffRhythm2Weights, muqAudioFile), []byte("w"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	calls := &[]runner.Invocation{}
	s.MediaRunner.(*runner.FakeRunner).SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
		switch inv.Name {
		case "ffmpeg":
			if strings.Contains(strings.Join(inv.Args, " "), "f32le") {
				return runner.ScriptedResult{Stdout: make([]byte, 4*22050)}
			}
			return runner.ScriptedResult{Err: os.WriteFile(inv.Args[len(inv.Args)-1], wavOf(20), 0o644)}
		case "/fake/whisper-cli":
			for i, a := range inv.Args {
				if a == "-of" {
					return runner.ScriptedResult{Err: os.WriteFile(inv.Args[i+1]+".json", []byte(sungWords), 0o644)}
				}
			}
		case exe:
			*calls = append(*calls, inv)
			return runner.ScriptedResult{Stdout: []byte(`{"clips":2,"seconds":20,"categories":{` +
				`"genre":[{"tag":"folk","text":"t","score":0.43},{"tag":"pop","text":"t","score":0.38},{"tag":"country","text":"t","score":0.36},{"tag":"rock","text":"t","score":0.3}],` +
				`"mood":[{"tag":"sad","text":"t","score":0.1},{"tag":"hopeful","text":"t","score":0.09},{"tag":"calm","text":"t","score":0.08}],` +
				`"instrument":[{"tag":"acoustic guitar","text":"t","score":0.4},{"tag":"guitar","text":"t","score":0.3}]}}`)}
		}
		return runner.ScriptedResult{}
	})
	return calls
}

func TestMuQ_ReadsTheSongWithTheCLIAndSaysWhyItCannotWhenItMissesWeights(t *testing.T) {
	s, _, _ := clapServer(t, true)
	calls := muqSetup(t, s, true)
	h := s.Handler()
	createProject(t, s, "songs")
	putSource(t, s, "songs", "song.mp3", []byte("MP3"))
	rec := doJSON(t, h, http.MethodPost, "/api/projects/songs/recognize", map[string]any{"source": "song.mp3", "method": "muq"})
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var ans struct {
		Method string   `json:"method"`
		Tags   []string `json:"tags"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &ans)
	want := "folk pop country sad hopeful acoustic guitar guitar"
	if ans.Method != "muq" || strings.Join(ans.Tags, " ") != want {
		t.Errorf("%s", rec.Body)
	}
	if len(*calls) != 1 || (*calls)[0].Args[0] != "classify" {
		t.Errorf("calls: %+v", *calls)
	}
	// the listen call takes the same method
	if code, body := postAudio(s, "/api/assist/listen?method=muq", []byte("x")); code != http.StatusOK || !strings.Contains(body, `"heard":["folk"`) {
		t.Errorf("listen: %d %s", code, body)
	}
	var out struct {
		Recognizers []recognizerView `json:"recognizers"`
	}
	_ = json.Unmarshal(doJSON(t, h, http.MethodGet, "/api/music/recognizers", nil).Body.Bytes(), &out)
	if !out.Recognizers[2].Available {
		t.Errorf("%+v", out.Recognizers)
	}

	s2, _, _ := clapServer(t, true)
	muqSetup(t, s2, false)
	createProject(t, s2, "songs")
	putSource(t, s2, "songs", "song.mp3", []byte("MP3"))
	rec = doJSON(t, s2.Handler(), http.MethodPost, "/api/projects/songs/recognize", map[string]any{"source": "song.mp3", "method": "muq"})
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), `"reason":"no_weights"`) {
		t.Errorf("no weights: %d %s", rec.Code, rec.Body)
	}
}

func TestListen_AnUnknownMethodIsRefused(t *testing.T) {
	s, _, _ := clapServer(t, true)
	if code, _ := postAudio(s, "/api/assist/listen?method=nope", []byte("x")); code != http.StatusBadRequest {
		t.Errorf("nope: %d", code)
	}
}

func TestRecognizers_SaysWhatCanBeUsed(t *testing.T) {
	s, _, _ := clapServer(t, true)
	var out struct {
		Recognizers []recognizerView `json:"recognizers"`
	}
	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/music/recognizers", nil)
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || len(out.Recognizers) != 3 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	byID := map[string]recognizerView{}
	for _, r := range out.Recognizers {
		byID[r.ID] = r
	}
	if !byID["whisper"].Available || !byID["clap"].Available || byID["muq"].Available || byID["muq"].Reason != "no_service" {
		t.Errorf("%+v", byID)
	}

	s2, _, _ := clapServer(t, false)
	rec = doJSON(t, s2.Handler(), http.MethodGet, "/api/music/recognizers", nil)
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	for _, r := range out.Recognizers {
		if r.ID == "clap" && (r.Available || r.Reason != musicsvc.ReasonNotRunning) {
			t.Errorf("clap stopped: %+v", r)
		}
	}
}

func TestRecognize_ASourceOfTheProject(t *testing.T) {
	s, _, _ := clapServer(t, true)
	h := s.Handler()
	id := "songs"
	createProject(t, s, id)
	putSource(t, s, id, "song.mp3", []byte("MP3"))
	rec := doJSON(t, h, http.MethodPost, "/api/projects/"+id+"/recognize", map[string]any{"source": "song.mp3", "method": "clap"})
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var ans struct {
		Method string   `json:"method"`
		Tags   []string `json:"tags"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &ans)
	if ans.Method != "clap" || len(ans.Tags) != 9 || ans.Tags[0] != "techno" {
		t.Errorf("%s", rec.Body)
	}
	if rec := doJSON(t, h, http.MethodPost, "/api/projects/"+id+"/recognize", map[string]any{"source": "song.mp3", "method": "muq"}); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("muq without the executable: %d", rec.Code)
	}
	if rec := doJSON(t, h, http.MethodPost, "/api/projects/"+id+"/recognize", map[string]any{"source": "song.mp3", "method": "whisper"}); rec.Code != http.StatusBadRequest {
		t.Errorf("whisper: %d", rec.Code)
	}
}
