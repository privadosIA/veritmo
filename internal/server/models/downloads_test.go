package models

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"veritmo/internal/platform/musicsvc"
	"veritmo/internal/platform/runner"
	"veritmo/internal/server/api"
)

// newDLServer is an asset server whose download client may reach the
// loopback httptest servers.
func newDLServer(t *testing.T) *testServer {
	t.Helper()
	s := newAssetServer(t)
	s.dm.allowPrivate = true
	t.Cleanup(s.dm.shutdown)
	return s
}

func postDownload(t *testing.T, s *testServer, body string) (*httptest.ResponseRecorder, Download) {
	t.Helper()
	rec := call(s.Handler(), "POST", "/api/downloads", body)
	var out struct{ Download Download }
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out.Download
}

func waitDL(t *testing.T, s *testServer, id string, states ...string) Download {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		rec, _, ok := s.dm.get(id)
		if !ok {
			t.Fatalf("download %s vanished", id)
		}
		for _, st := range states {
			if rec.State == st {
				return rec
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	rec, _, _ := s.dm.get(id)
	t.Fatalf("download %s stuck in %q, wanted %v", id, rec.State, states)
	return Download{}
}

func fileServer(t *testing.T, payload string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/missing.safetensors"):
			http.NotFound(w, r)
		case strings.HasSuffix(r.URL.Path, "/short.safetensors"):
			w.Header().Set("Content-Length", "100")
			_, _ = w.Write([]byte("only ten b"))
		default:
			_, _ = w.Write([]byte(payload))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func noPartials(t *testing.T, s *testServer) {
	t.Helper()
	for _, k := range assetKinds {
		ents, _ := os.ReadDir(s.KindDir(k))
		for _, e := range ents {
			if strings.HasSuffix(e.Name(), ".partial") {
				t.Errorf("leftover partial %s", e.Name())
			}
		}
	}
}

func TestDownload_Success(t *testing.T) {
	s := newDLServer(t)
	src := fileServer(t, strings.Repeat("x", 1000))
	rec, d := postDownload(t, s, fmt.Sprintf(`{"kind":"model","url":%q}`, src.URL+"/a/tiny.safetensors"))
	if rec.Code != 202 || d.ID == "" {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	if d.Name != "tiny.safetensors" || d.Kind != "model" {
		t.Errorf("record = %+v", d)
	}
	fin := waitDL(t, s, d.ID, dlSucceeded, dlFailed)
	if fin.State != dlSucceeded || fin.Bytes != 1000 || fin.Total != 1000 {
		t.Fatalf("final = %+v", fin)
	}
	b, err := os.ReadFile(filepath.Join(s.assetsRoot, "models", "tiny.safetensors"))
	if err != nil || len(b) != 1000 {
		t.Fatalf("installed file: %v len %d", err, len(b))
	}
	noPartials(t, s)
}

func TestDownload_FailuresLeaveNothing(t *testing.T) {
	s := newDLServer(t)
	src := fileServer(t, "data")
	for _, name := range []string{"missing", "short"} {
		_, d := postDownload(t, s, fmt.Sprintf(`{"kind":"model","url":%q}`, src.URL+"/"+name+".safetensors"))
		fin := waitDL(t, s, d.ID, dlSucceeded, dlFailed)
		if fin.State != dlFailed || fin.Error == "" {
			t.Errorf("%s: final = %+v", name, fin)
		}
		if _, err := os.Stat(filepath.Join(s.assetsRoot, "models", name+".safetensors")); !os.IsNotExist(err) {
			t.Errorf("%s: a failed download must not install a file", name)
		}
	}
	noPartials(t, s)
}

func TestDownload_DiskSpaceRefused(t *testing.T) {
	s := newDLServer(t)
	s.dm.freeBytes = func(string) (uint64, error) { return 10, nil }
	src := fileServer(t, strings.Repeat("x", 100))
	_, d := postDownload(t, s, fmt.Sprintf(`{"kind":"model","url":%q}`, src.URL+"/big.safetensors"))
	fin := waitDL(t, s, d.ID, dlSucceeded, dlFailed)
	if fin.State != dlFailed || !strings.Contains(fin.Error, "disk space") {
		t.Fatalf("final = %+v", fin)
	}
	noPartials(t, s)
}

func TestDownload_RefusesPrivateTargets(t *testing.T) {
	s := newAssetServer(t) // allowPrivate stays false
	t.Cleanup(s.dm.shutdown)
	var hits atomic.Int32
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	t.Cleanup(src.Close)

	_, d := postDownload(t, s, fmt.Sprintf(`{"kind":"model","url":%q}`, src.URL+"/x.safetensors"))
	fin := waitDL(t, s, d.ID, dlSucceeded, dlFailed)
	if fin.State != dlFailed || !strings.Contains(fin.Error, "non-public") {
		t.Fatalf("final = %+v", fin)
	}
	if hits.Load() != 0 {
		t.Error("the private address must not be contacted")
	}

	// a public-looking URL that redirects to loopback is refused as well
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, src.URL+"/x.safetensors", http.StatusFound)
	}))
	t.Cleanup(redir.Close)
	_, d = postDownload(t, s, fmt.Sprintf(`{"kind":"model","url":%q,"name":"r.safetensors"}`, redir.URL))
	if fin := waitDL(t, s, d.ID, dlSucceeded, dlFailed); fin.State != dlFailed {
		t.Fatalf("redirect final = %+v", fin)
	}
}

func TestBlockedIP(t *testing.T) {
	for ip, want := range map[string]bool{
		"127.0.0.1": true, "::1": true, "10.1.2.3": true, "192.168.0.5": true, "172.16.0.1": true,
		"169.254.169.254": true, "0.0.0.0": true, "100.64.1.1": true, "fe80::1": true,
		"8.8.8.8": false, "140.82.112.3": false, "2606:4700::1": false,
	} {
		if got := blockedIP(parseIP(t, ip)); got != want {
			t.Errorf("blockedIP(%s) = %v, want %v", ip, got, want)
		}
	}
}

func TestDownload_RequestValidation(t *testing.T) {
	s := newDLServer(t)
	writeAsset(t, s, "model", "exists.safetensors", 3)
	cases := []struct {
		name, body string
		status     int
	}{
		{"bad json", `{`, 400},
		{"no kind", `{"url":"https://h/x.safetensors"}`, 400},
		{"unknown kind", `{"kind":"x","url":"https://h/x.safetensors"}`, 400},
		{"no source", `{"kind":"model"}`, 400},
		{"both sources", `{"kind":"model","url":"https://h/x.safetensors","repo":"a/b","file":"x"}`, 400},
		{"file scheme", `{"kind":"model","url":"file:///etc/x.safetensors"}`, 400},
		{"ftp scheme", `{"kind":"model","url":"ftp://h/x.safetensors"}`, 400},
		{"bad repo", `{"kind":"model","repo":"nope","file":"x.safetensors"}`, 400},
		{"repo traversal", `{"kind":"model","repo":"a/b","file":"../x.safetensors"}`, 400},
		{"bad extension", `{"kind":"model","url":"https://h/run.exe"}`, 422},
		{"traversal name", `{"kind":"model","url":"https://h/x.safetensors","name":"../../etc/p.safetensors"}`, 422},
		{"existing file", `{"kind":"model","url":"https://h/exists.safetensors"}`, 409},
		{"unknown catalog", `{"id":"nope"}`, 404},
	}
	for _, c := range cases {
		rec, _ := postDownload(t, s, c.body)
		if rec.Code != c.status {
			t.Errorf("%s: status %d, want %d (%s)", c.name, rec.Code, c.status, rec.Body)
			continue
		}
		decodeAPIError(t, rec.Body.String())
	}
	if ents, _ := os.ReadDir(filepath.Join(s.assetsRoot, "models")); len(ents) != 1 {
		t.Errorf("rejected requests must not write anything: %v", ents)
	}
	if got := s.dm.list(); len(got) != 0 {
		t.Errorf("rejected requests must not create jobs: %+v", got)
	}
}

func TestDownload_Overwrite(t *testing.T) {
	s := newDLServer(t)
	p := writeAsset(t, s, "model", "m.safetensors", 3)
	src := fileServer(t, "newcontent")
	body := fmt.Sprintf(`{"kind":"model","url":%q,"overwrite":true}`, src.URL+"/m.safetensors")
	rec, d := postDownload(t, s, body)
	if rec.Code != 202 {
		t.Fatalf("overwrite create: %d %s", rec.Code, rec.Body)
	}
	waitDL(t, s, d.ID, dlSucceeded, dlFailed)
	if b, _ := os.ReadFile(p); string(b) != "newcontent" {
		t.Errorf("file = %q", b)
	}
}

func TestResolveHuggingFace(t *testing.T) {
	s := newAssetServer(t)
	k, source, name, files, derr := s.resolveDownload(downloadRequest{Kind: "model", Repo: "org/name", File: "weights/model v1.safetensors"})
	if derr != nil {
		t.Fatal(derr)
	}
	if k.Kind != "model" || source != "hf:org/name/weights/model v1.safetensors" || name != "model v1.safetensors" {
		t.Errorf("k=%v source=%q name=%q", k, source, name)
	}
	if want := "https://huggingface.co/org/name/resolve/main/weights/model%20v1.safetensors"; files[0].url != want {
		t.Errorf("url = %q, want %q", files[0].url, want)
	}
	_, _, _, files, _ = s.resolveDownload(downloadRequest{Kind: "lora", Repo: "o/n", File: "l.safetensors", Revision: "v2.0", Name: "mine.safetensors"})
	if files[0].url != "https://huggingface.co/o/n/resolve/v2.0/l.safetensors" || filepath.Base(files[0].path) != "mine.safetensors" {
		t.Errorf("files = %+v", files)
	}
	if !strings.Contains(files[0].path, filepath.Join("models", "loras")) {
		t.Errorf("lora destination = %q", files[0].path)
	}
}

func TestDownload_HFTokenOnlyToHFHosts(t *testing.T) {
	s := newDLServer(t)
	s.dm.hfToken = func() string { return "hf_secret" }
	var auth atomic.Value
	auth.Store("unset")
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth.Store(r.Header.Get("Authorization"))
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(src.Close)
	_, d := postDownload(t, s, fmt.Sprintf(`{"kind":"model","url":%q}`, src.URL+"/t.safetensors"))
	waitDL(t, s, d.ID, dlSucceeded, dlFailed)
	if got := auth.Load().(string); got != "" {
		t.Errorf("token leaked to a non-HF host: %q", got)
	}
	for host, want := range map[string]bool{"huggingface.co": true, "cdn-lfs.huggingface.co": true, "evilhuggingface.co": false, "huggingface.co.evil.com": false} {
		if isHFHost(host) != want {
			t.Errorf("isHFHost(%q) = %v", host, !want)
		}
	}
}

// gate lets a test hold downloads open and release them in a chosen order.
func gatedServer(t *testing.T) (*httptest.Server, chan struct{}, *atomic.Int32) {
	t.Helper()
	release := make(chan struct{})
	started := new(atomic.Int32)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started.Add(1)
		if strings.Contains(r.URL.Path, "three") {
			started.Add(100) // the cancelled queued job must never be contacted
		}
		w.Header().Set("Content-Length", "6")
		_, _ = w.Write([]byte("abc"))
		w.(http.Flusher).Flush()
		select {
		case <-release:
			_, _ = w.Write([]byte("def"))
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close)
	return srv, release, started
}

func TestDownload_QueueOrderAndCancel(t *testing.T) {
	s := newDLServer(t)
	src, release, started := gatedServer(t)

	_, d1 := postDownload(t, s, fmt.Sprintf(`{"kind":"model","url":%q}`, src.URL+"/one.safetensors"))
	_, d2 := postDownload(t, s, fmt.Sprintf(`{"kind":"model","url":%q}`, src.URL+"/two.safetensors"))
	_, d3 := postDownload(t, s, fmt.Sprintf(`{"kind":"model","url":%q}`, src.URL+"/three.safetensors"))
	waitDL(t, s, d1.ID, dlRunning)
	if r2, _, _ := s.dm.get(d2.ID); r2.State != dlQueued {
		t.Fatalf("second must be queued, is %s", r2.State)
	}

	// listing is newest first
	list := s.dm.list()
	if len(list) != 3 || list[0].ID != d3.ID || list[2].ID != d1.ID {
		t.Errorf("list order: %+v", list)
	}

	// cancel the queued third: it never runs
	if rec := call(s.Handler(), "POST", "/api/downloads/"+d3.ID+"/cancel", ""); rec.Code != 200 {
		t.Fatalf("cancel queued: %d", rec.Code)
	}
	if r3, _, _ := s.dm.get(d3.ID); r3.State != dlCancelled {
		t.Errorf("third = %s", r3.State)
	}

	// cancel the running first: partial removed, queue advances
	if rec := call(s.Handler(), "POST", "/api/downloads/"+d1.ID+"/cancel", ""); rec.Code != 200 {
		t.Fatalf("cancel running: %d", rec.Code)
	}
	if f := waitDL(t, s, d1.ID, dlCancelled, dlFailed, dlSucceeded); f.State != dlCancelled {
		t.Fatalf("first = %+v", f)
	}
	waitDL(t, s, d2.ID, dlRunning)
	close(release)
	if f := waitDL(t, s, d2.ID, dlSucceeded, dlFailed); f.State != dlSucceeded {
		t.Fatalf("second = %+v", f)
	}
	if n := started.Load(); n >= 100 {
		t.Errorf("the cancelled queued job was contacted (counter %d)", n)
	}
	for _, n := range []string{"one", "three"} {
		if _, err := os.Stat(filepath.Join(s.assetsRoot, "models", n+".safetensors")); !os.IsNotExist(err) {
			t.Errorf("%s must not be installed", n)
		}
	}
	noPartials(t, s)

	// cancelling a finished job is a conflict; unknown is 404
	rec := call(s.Handler(), "POST", "/api/downloads/"+d2.ID+"/cancel", "")
	if rec.Code != 409 || decodeAPIError(t, rec.Body.String()) != api.CodeConflict {
		t.Errorf("cancel finished: %d %s", rec.Code, rec.Body)
	}
	if rec := call(s.Handler(), "POST", "/api/downloads/nope/cancel", ""); rec.Code != 404 {
		t.Errorf("cancel unknown: %d", rec.Code)
	}
}

func TestDownload_ShutdownCleansPartials(t *testing.T) {
	s := newAssetServer(t)
	s.dm.allowPrivate = true
	src, _, started := gatedServer(t)
	_, d := postDownload(t, s, fmt.Sprintf(`{"kind":"model","url":%q}`, src.URL+"/x.safetensors"))
	waitDL(t, s, d.ID, dlRunning)
	for started.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	s.dm.shutdown()
	if f, _, _ := s.dm.get(d.ID); f.State != dlCancelled {
		t.Errorf("state = %s", f.State)
	}
	noPartials(t, s)
}

func TestSweepPartialsAtStart(t *testing.T) {
	s := newAssetServer(t)
	p := writeAsset(t, s, "model", "old.safetensors.partial", 1)
	keep := writeAsset(t, s, "model", "keep.safetensors", 1)
	s.sweepPartials()
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Error("stale partial must be removed")
	}
	if _, err := os.Stat(keep); err != nil {
		t.Error("real assets must stay")
	}
}

func TestDownload_CatalogMultiFileIsAtomic(t *testing.T) {
	s := newDLServer(t)
	src := fileServer(t, "payload")
	saved := catalog
	t.Cleanup(func() { catalog = saved })
	catalog = append(append([]catalogEntry{}, catalog...), catalogEntry{
		ID: "test-voice", Kind: "voice", Name: "Test voice",
		Files: []catalogFile{
			{Name: "t.onnx", URL: src.URL + "/t.onnx"},
			{Name: "t.onnx.json", URL: src.URL + "/missing.safetensors"}, // 404 -> whole job fails
		},
	})
	_, d := postDownload(t, s, `{"id":"test-voice"}`)
	if len(d.Files) != 2 || d.Source != "catalog:test-voice" {
		t.Fatalf("record = %+v", d)
	}
	if f := waitDL(t, s, d.ID, dlSucceeded, dlFailed); f.State != dlFailed {
		t.Fatalf("final = %+v", f)
	}
	if _, err := os.Stat(filepath.Join(s.assetsRoot, "voices", "t.onnx")); !os.IsNotExist(err) {
		t.Error("a half-installed voice must not remain")
	}
	noPartials(t, s)

	catalog[len(catalog)-1].Files[1].URL = src.URL + "/t.onnx.json"
	_, d = postDownload(t, s, `{"id":"test-voice"}`)
	if f := waitDL(t, s, d.ID, dlSucceeded, dlFailed); f.State != dlSucceeded {
		t.Fatalf("final = %+v", f)
	}
	for _, n := range []string{"t.onnx", "t.onnx.json"} {
		if _, err := os.Stat(filepath.Join(s.assetsRoot, "voices", n)); err != nil {
			t.Errorf("%s missing: %v", n, err)
		}
	}
}

func TestDownload_EventsStream(t *testing.T) {
	s := newDLServer(t)
	src := fileServer(t, strings.Repeat("y", 500))
	_, d := postDownload(t, s, fmt.Sprintf(`{"kind":"model","url":%q}`, src.URL+"/ev.safetensors"))
	waitDL(t, s, d.ID, dlSucceeded, dlFailed)

	// replay after the job finished
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	resp, err := http.Get(ts.URL + "/api/downloads/" + d.ID + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}
	var types []string
	var lastState string
	sc := bufio.NewScanner(resp.Body)
	cur := ""
	for sc.Scan() {
		line := sc.Text()
		if v, ok := strings.CutPrefix(line, "event: "); ok {
			cur = v
			types = append(types, v)
		}
		if v, ok := strings.CutPrefix(line, "data: "); ok && cur == "download-state" {
			var p struct{ State string }
			_ = json.Unmarshal([]byte(v), &p)
			lastState = p.State
		}
	}
	joined := strings.Join(types, ",")
	if !strings.HasPrefix(joined, "download-state") || !strings.Contains(joined, "progress") || !strings.HasSuffix(joined, "download-state,stream-end") {
		t.Errorf("event types = %s", joined)
	}
	if lastState != dlSucceeded {
		t.Errorf("last state = %q", lastState)
	}

	if rec := call(s.Handler(), "GET", "/api/downloads/nope/events", ""); rec.Code != 404 {
		t.Errorf("unknown events: %d", rec.Code)
	}
}

func TestDownload_EventsLiveTail(t *testing.T) {
	s := newDLServer(t)
	src, release, _ := gatedServer(t)
	_, d := postDownload(t, s, fmt.Sprintf(`{"kind":"model","url":%q}`, src.URL+"/live.safetensors"))
	waitDL(t, s, d.ID, dlRunning)

	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	resp, err := http.Get(ts.URL + "/api/downloads/" + d.ID + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	done := make(chan string, 1)
	go func() {
		var sb strings.Builder
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			sb.WriteString(sc.Text() + "\n")
		}
		done <- sb.String()
	}()
	time.Sleep(50 * time.Millisecond)
	close(release)
	select {
	case body := <-done:
		if !strings.Contains(body, `"state":"succeeded"`) || !strings.Contains(body, "event: stream-end") {
			t.Errorf("live tail body:\n%s", body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stream did not end")
	}
}

func TestDownload_GetAndList(t *testing.T) {
	s := newDLServer(t)
	if rec := call(s.Handler(), "GET", "/api/downloads/nope", ""); rec.Code != 404 {
		t.Errorf("unknown get: %d", rec.Code)
	}
	rec := call(s.Handler(), "GET", "/api/downloads", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"downloads":[]`) {
		t.Errorf("empty list: %d %s", rec.Code, rec.Body)
	}
}

func parseIP(t *testing.T, s string) net.IP {
	t.Helper()
	ip := net.ParseIP(s)
	if ip == nil {
		t.Fatalf("bad ip %q", s)
	}
	return ip
}

func TestDownload_HFAccessGuidance(t *testing.T) {
	status := new(atomic.Int32)
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(int(status.Load()))
	}))
	t.Cleanup(src.Close)

	run := func(t *testing.T, s *testServer, st int) Download {
		t.Helper()
		status.Store(int32(st))
		_, d := postDownload(t, s, fmt.Sprintf(`{"kind":"model","url":%q}`, src.URL+fmt.Sprintf("/m%d.safetensors", st)))
		return waitDL(t, s, d.ID, dlSucceeded, dlFailed)
	}

	t.Run("gated without a token", func(t *testing.T) {
		s := newDLServer(t)
		s.dm.isHF = func(string) bool { return true }
		fin := run(t, s, 401)
		for _, want := range []string{"licence", "read token", "HF_TOKEN", "does not exist"} {
			if !strings.Contains(fin.Error, want) {
				t.Errorf("message lacks %q: %s", want, fin.Error)
			}
		}
	})
	t.Run("token without access never leaks", func(t *testing.T) {
		s := newDLServer(t)
		s.dm.isHF = func(string) bool { return true }
		s.dm.hfToken = func() string { return "hf_supersecret" }
		fin := run(t, s, 403)
		if !strings.Contains(fin.Error, "that same account") || strings.Contains(fin.Error, "hf_supersecret") {
			t.Errorf("message = %s", fin.Error)
		}
		rec, _, _ := s.dm.get(fin.ID)
		if b, _ := json.Marshal(rec); strings.Contains(string(b), "hf_supersecret") {
			t.Error("token present in the record")
		}
	})
	t.Run("other hosts keep the plain status", func(t *testing.T) {
		s := newDLServer(t) // isHF stays the real predicate: httptest is not Hugging Face
		fin := run(t, s, 403)
		if !strings.Contains(fin.Error, "remote answered 403") || strings.Contains(fin.Error, "HF_TOKEN") {
			t.Errorf("message = %s", fin.Error)
		}
	})
	t.Run("404 stays plain even on Hugging Face", func(t *testing.T) {
		s := newDLServer(t)
		s.dm.isHF = func(string) bool { return true }
		fin := run(t, s, 404)
		if !strings.Contains(fin.Error, "remote answered 404") {
			t.Errorf("message = %s", fin.Error)
		}
	})
}

func TestDownload_CatalogInstallsOnlyMissingFiles(t *testing.T) {
	s := newDLServer(t)
	var hits atomic.Int32
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("payload-" + filepath.Base(r.URL.Path)))
	}))
	t.Cleanup(src.Close)
	saved := catalog
	t.Cleanup(func() { catalog = saved })
	shared := catalogFile{Name: "shared.safetensors", Role: "vae", URL: src.URL + "/shared.safetensors"}
	catalog = append(append([]catalogEntry{}, catalog...),
		catalogEntry{ID: "b1", Kind: "video", Name: "B1", Files: []catalogFile{{Name: "one.gguf", Role: "model", URL: src.URL + "/one.gguf"}, shared}},
		catalogEntry{ID: "b2", Kind: "video", Name: "B2", Files: []catalogFile{{Name: "two.gguf", Role: "model", URL: src.URL + "/two.gguf"}, shared}},
	)

	_, d := postDownload(t, s, `{"id":"b1"}`)
	if f := waitDL(t, s, d.ID, dlSucceeded, dlFailed); f.State != dlSucceeded {
		t.Fatalf("b1 = %+v", f)
	}
	if hits.Load() != 2 {
		t.Fatalf("b1 fetched %d files, want 2", hits.Load())
	}

	// b2 shares a file with b1: only its own file is downloaded
	rec, d2 := postDownload(t, s, `{"id":"b2"}`)
	if rec.Code != 202 {
		t.Fatalf("b2: %d %s", rec.Code, rec.Body)
	}
	if len(d2.Files) != 1 || d2.Files[0] != "two.gguf" {
		t.Errorf("b2 must download only its missing file, got %v", d2.Files)
	}
	if f := waitDL(t, s, d2.ID, dlSucceeded, dlFailed); f.State != dlSucceeded {
		t.Fatalf("b2 = %+v", f)
	}
	if hits.Load() != 3 {
		t.Errorf("total fetches = %d, want 3 (the shared file must not be fetched again)", hits.Load())
	}

	// everything present: 409, nothing fetched
	rec, _ = postDownload(t, s, `{"id":"b2"}`)
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), "already installed") {
		t.Errorf("already installed: %d %s", rec.Code, rec.Body)
	}
	if hits.Load() != 3 {
		t.Error("an already-installed entry must not fetch anything")
	}

	// overwrite replaces every file
	rec, d3 := postDownload(t, s, `{"id":"b2","overwrite":true}`)
	if rec.Code != 202 || len(d3.Files) != 2 {
		t.Fatalf("overwrite: %d files=%v", rec.Code, d3.Files)
	}
	waitDL(t, s, d3.ID, dlSucceeded, dlFailed)
}

// The DiffRhythm2 entry only downloads the raw checkpoint; the server then runs `diffrhythm2 install` on that folder
// (it fetches the rest and converts it), and a failure of the command fails the download.
func TestDownload_DiffRhythm2RunsTheInstall(t *testing.T) {
	s := newDLServer(t)
	src := fileServer(t, "payload")
	saved := catalog
	t.Cleanup(func() { catalog = saved })
	catalog = append(append([]catalogEntry{}, catalog...), catalogEntry{
		ID: "test-dr2", Kind: "diffrhythm2", Name: "Test DiffRhythm2",
		Files: []catalogFile{{Name: "config.json", URL: src.URL + "/config.json"}},
	})
	k, _ := FindKind("diffrhythm2")
	dir := s.KindDir(k)

	// without the .NET services the files arrive but the download fails and says why
	_, d := postDownload(t, s, `{"id":"test-dr2"}`)
	f := waitDL(t, s, d.ID, dlSucceeded, dlFailed)
	if f.State != dlFailed || !strings.Contains(f.Error, "services") {
		t.Fatalf("without the services: %+v", f)
	}

	exeDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(exeDir, "diffrhythm2"), 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(exeDir, "diffrhythm2", "diffrhythm2")
	if err := os.WriteFile(exe, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	s.services = &musicsvc.ServiceHost{Dir: exeDir}
	fr := runner.NewFakeRunner()
	s.mediaRunner = fr

	_, d = postDownload(t, s, `{"id":"test-dr2","overwrite":true}`)
	if f := waitDL(t, s, d.ID, dlSucceeded, dlFailed); f.State != dlSucceeded {
		t.Fatalf("with the services: %+v", f)
	}
	calls := fr.Calls()
	if len(calls) != 1 || calls[0].Name != exe {
		t.Fatalf("calls = %+v", calls)
	}
	want := []string{"install", "--model", dir, "--accept-licenses"}
	if strings.Join(calls[0].Args, " ") != strings.Join(want, " ") {
		t.Errorf("args = %v, want %v", calls[0].Args, want)
	}

	fr.Reset()
	fr.Script(runner.ScriptedResult{Err: errors.New("exit status 1: no space left")})
	_, d = postDownload(t, s, `{"id":"test-dr2","overwrite":true}`)
	if f := waitDL(t, s, d.ID, dlSucceeded, dlFailed); f.State != dlFailed || !strings.Contains(f.Error, "no space left") {
		t.Fatalf("a failing install fails the download: %+v", f)
	}
}

func TestDownload_AFullQueueIsRefused(t *testing.T) {
	s := newDLServer(t)
	src, release, _ := gatedServer(t)
	defer close(release)

	var first Download
	for i := 0; i <= maxQueuedDownloads; i++ { // one runs, maxQueuedDownloads wait
		rec, d := postDownload(t, s, fmt.Sprintf(`{"kind":"model","url":%q}`, fmt.Sprintf("%s/m%d.safetensors", src.URL, i)))
		if rec.Code != http.StatusAccepted && rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
			t.Fatalf("download %d: status %d (%s)", i, rec.Code, rec.Body.String())
		}
		if i == 0 {
			first = d
			waitDL(t, s, d.ID, dlRunning)
		}
	}
	rec, _ := postDownload(t, s, fmt.Sprintf(`{"kind":"model","url":%q}`, src.URL+"/one-too-many.safetensors"))
	if rec.Code != http.StatusTooManyRequests || !strings.Contains(rec.Body.String(), "queue_full") {
		t.Fatalf("status %d (%s), want 429 queue_full", rec.Code, rec.Body.String())
	}
	// Finishing the running one makes room.
	if c := call(s.Handler(), "POST", "/api/downloads/"+first.ID+"/cancel", ""); c.Code != 200 {
		t.Fatalf("cancel: %d", c.Code)
	}
}
