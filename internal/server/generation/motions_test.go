package generation

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"veritmo/internal/platform/runner"
	"veritmo/internal/platform/settings"
	"veritmo/internal/video/motionlib"
	"veritmo/internal/video/videoedit"
)

// libraryServer is a server whose library is in a folder of its own and whose vision service is the fake.
func libraryServer(t *testing.T, fv *fakeVision) *testServer {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	s, _ := newProjectsServer(t)
	t.Cleanup(s.Workspace.Shutdown)
	s.Stack.VideoEdit.SetVision(fv)
	return s
}

// motionClip is a video of the given seconds (16 frames per second, 320x180).
func motionClip(t *testing.T, seconds string) []byte {
	t.Helper()
	path := filepath.Join(t.TempDir(), "m.mp4")
	if b, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=16:duration="+seconds,
		"-c:v", "libx264", "-pix_fmt", "yuv420p", path).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	raw, _ := os.ReadFile(path)
	return raw
}

func postMotion(t *testing.T, s *testServer, req any, video []byte) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	raw, _ := json.Marshal(req)
	_ = mw.WriteField("request", string(raw))
	if video != nil {
		fw, _ := mw.CreateFormFile("video", "baile.mp4")
		_, _ = fw.Write(video)
	}
	_ = mw.Close()
	r := httptest.NewRequest(http.MethodPost, "/api/motions", &buf)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, r)
	return rec
}

func decodeMotion(t *testing.T, rec *httptest.ResponseRecorder) libraryMotionView {
	t.Helper()
	var v libraryMotionView
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("not a motion: %v: %s", err, rec.Body)
	}
	return v
}

func waitMotion(t *testing.T, s *testServer, id, want string) libraryMotionView {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	var last libraryMotionView
	for time.Now().Before(deadline) {
		if rec := doJSON(t, s.Handler(), http.MethodGet, "/api/motions/"+id, nil); rec.Code == http.StatusOK {
			if last = decodeMotion(t, rec); last.State == want {
				return last
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("motion %s never reached %s (last %+v)", id, want, last)
	return last
}

func TestMotions_AnEmptyLibraryListsNothing(t *testing.T) {
	s := libraryServer(t, &fakeVision{ready: true})
	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/motions", nil)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Errorf("%d %s", rec.Code, rec.Body)
	}
	if rec := doJSON(t, s.Handler(), http.MethodGet, "/api/motions/mot-20260101-000000-abcdef", nil); rec.Code != http.StatusNotFound {
		t.Errorf("unknown: %d", rec.Code)
	}
	for _, id := range []string{"nope", "mot-1", "mot-20260101-000000-abcdef"} {
		if rec := doJSON(t, s.Handler(), http.MethodGet, "/api/motions/"+id+"/preview", nil); rec.Code != http.StatusNotFound {
			t.Errorf("%s preview: %d", id, rec.Code)
		}
	}
}

func TestMotions_AddingOneGivesProcessingThenReadyWithItsFilesAndNoSource(t *testing.T) {
	fv := &fakeVision{ready: true}
	s := libraryServer(t, fv)
	rec := postMotion(t, s, map[string]any{"title": "Baile", "tags": []string{"dance", "solo"}}, motionClip(t, "4"))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("add: %d %s", rec.Code, rec.Body)
	}
	added := decodeMotion(t, rec)
	if added.State != "processing" || added.Title != "Baile" || len(added.Tags) != 2 || !strings.HasPrefix(added.ID, "mot-") || added.Preview != "/api/motions/"+added.ID+"/preview" {
		t.Fatalf("added = %+v", added)
	}
	ready := waitMotion(t, s, added.ID, "ready")
	if ready.Duration < 3.9 || ready.Duration > 4.1 || ready.People != 2 || ready.Error != "" {
		t.Errorf("ready = %+v", ready)
	}
	// the preview is served as a video
	prev := doJSON(t, s.Handler(), http.MethodGet, ready.Preview, nil)
	if prev.Code != http.StatusOK || prev.Header().Get("Content-Type") != "video/mp4" || prev.Body.Len() < 1000 {
		t.Errorf("preview: %d %s, %d bytes", prev.Code, prev.Header().Get("Content-Type"), prev.Body.Len())
	}
	// 4 seconds at 16 per second of three-channel PNG in the shape of the video, and nothing of the source video
	dir := s.libraryStore().Path(added.ID)
	control, _ := filepath.Glob(filepath.Join(dir, "control", "*.png"))
	if len(control) != 64 {
		t.Errorf("control frames = %d, want 64", len(control))
	}
	var kept []string
	_ = filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() && !strings.HasSuffix(p, ".png") {
			kept = append(kept, filepath.Base(p))
		}
		return nil
	})
	if strings.Join(kept, ",") != "preview.mp4" {
		t.Errorf("the motion keeps %v: its record is a row of the database, and the video and its frames are not kept", kept)
	}
	m, _ := s.libraryStore().Get(added.ID)
	if m.Frames != 64 || m.Width != 832 || m.Height != 480 {
		t.Errorf("stored = %+v: a landscape video gives 832x480", m)
	}
	// the list has it
	var list []libraryMotionView
	_ = json.Unmarshal(doJSON(t, s.Handler(), http.MethodGet, "/api/motions", nil).Body.Bytes(), &list)
	if len(list) != 1 || list[0].ID != added.ID {
		t.Errorf("list = %+v", list)
	}
	// the temporary files of the upload are gone
	if left, _ := filepath.Glob(filepath.Join(os.TempDir(), "motion-*", "origen.*")); len(left) != 0 {
		t.Errorf("temporary videos left: %v", left)
	}
}

func TestMotions_EveryRefusalKeepsNothing(t *testing.T) {
	fv := &fakeVision{ready: true}
	s := libraryServer(t, fv)
	long, short, ok := motionClip(t, "31"), motionClip(t, "1"), motionClip(t, "3")
	for _, tc := range []struct {
		name  string
		req   any
		video []byte
		setup func()
		code  int
		want  string
	}{
		{"not a video", map[string]any{}, []byte("just text"), nil, 422, "not a video"},
		{"too long", map[string]any{}, long, nil, 422, "at most 30"},
		{"too short", map[string]any{}, short, nil, 422, "at least 2.1"},
		{"nobody in it", map[string]any{}, ok, func() { fv.nobody = true }, 422, "no person was found"},
		{"no video and no address", map[string]any{"title": "x"}, nil, nil, 422, "the video is needed"},
		{"a video and an address", map[string]any{"source": "https://youtu.be/abcdefghijk"}, ok, nil, 422, "not both"},
		{"an address that is not accepted", map[string]any{"source": "http://example.com/v.mp4"}, nil, nil, 422, "YouTube"},
		{"a long title", map[string]any{"title": strings.Repeat("a", 121)}, ok, nil, 422, "title has 121"},
		{"too many tags", map[string]any{"tags": []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11"}}, ok, nil, 422, "tags has 11"},
		{"the vision service stopped", map[string]any{}, ok, func() { fv.ready = false }, 503, "vision service"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fv.nobody, fv.ready = false, true
			if tc.setup != nil {
				tc.setup()
			}
			rec := postMotion(t, s, tc.req, tc.video)
			if rec.Code != tc.code || !strings.Contains(rec.Body.String(), tc.want) {
				t.Fatalf("got %d %s, want %d with %q", rec.Code, rec.Body, tc.code, tc.want)
			}
			if got := s.libraryStore().List(); len(got) != 0 {
				t.Errorf("a refused request left motions: %+v", got)
			}
		})
	}
	// a body that is not multipart nor JSON
	r := httptest.NewRequest(http.MethodPost, "/api/motions", strings.NewReader("nope"))
	r.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, r)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bad JSON: %d %s", rec.Code, rec.Body)
	}
}

func TestMotions_APreparationThatFailsLeavesTheReasonAndNoControl(t *testing.T) {
	fv := &fakeVision{ready: true, controlErr: os.ErrPermission}
	s := libraryServer(t, fv)
	rec := postMotion(t, s, map[string]any{"title": "x"}, motionClip(t, "3"))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("add: %d %s", rec.Code, rec.Body)
	}
	id := decodeMotion(t, rec).ID
	failed := waitMotion(t, s, id, "failed")
	if !strings.Contains(failed.Error, "permission") {
		t.Errorf("failed = %+v", failed)
	}
	if got, _ := filepath.Glob(filepath.Join(s.libraryStore().Path(id), "control", "*")); len(got) != 0 {
		t.Errorf("a failed motion kept its control: %v", got)
	}
	// it is listed, cannot be used and can be removed
	if rec := doJSON(t, s.Handler(), http.MethodDelete, "/api/motions/"+id, nil); rec.Code != http.StatusNoContent {
		t.Errorf("delete: %d", rec.Code)
	}
}

func TestMotions_RemovingStopsThePreparationAndRemovesTheFiles(t *testing.T) {
	fv := &fakeVision{ready: true, block: true}
	s := libraryServer(t, fv)
	rec := postMotion(t, s, map[string]any{"title": "x"}, motionClip(t, "3"))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("add: %d %s", rec.Code, rec.Body)
	}
	id := decodeMotion(t, rec).ID
	deadline := time.Now().Add(10 * time.Second)
	for fv.calls() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if fv.calls() == 0 {
		t.Fatal("the preparation never reached the vision service")
	}
	if rec := doJSON(t, s.Handler(), http.MethodDelete, "/api/motions/"+id, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	s.waitLibrary()
	if _, err := os.Stat(s.libraryStore().Path(id)); !os.IsNotExist(err) {
		t.Errorf("the motion came back or was not removed: %v", err)
	}
	if rec := doJSON(t, s.Handler(), http.MethodGet, "/api/motions/"+id, nil); rec.Code != http.StatusNotFound {
		t.Errorf("get after delete: %d", rec.Code)
	}
	if rec := doJSON(t, s.Handler(), http.MethodDelete, "/api/motions/"+id, nil); rec.Code != http.StatusNotFound {
		t.Errorf("second delete: %d", rec.Code)
	}
}

func TestMotions_TheListIsNewestFirst(t *testing.T) {
	s := libraryServer(t, &fakeVision{ready: true})
	clip := motionClip(t, "3")
	a := decodeMotion(t, postMotion(t, s, map[string]any{"title": "first"}, clip))
	waitMotion(t, s, a.ID, "ready")
	b := decodeMotion(t, postMotion(t, s, map[string]any{"title": "second"}, clip))
	waitMotion(t, s, b.ID, "ready")
	var list []libraryMotionView
	_ = json.Unmarshal(doJSON(t, s.Handler(), http.MethodGet, "/api/motions", nil).Body.Bytes(), &list)
	if len(list) != 2 || list[0].ID != b.ID || list[1].ID != a.ID {
		t.Errorf("list = %+v", list)
	}
}

func TestMotions_TheServersStopLeavesNoMotionHalfMade(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	s, _ := newProjectsServer(t)
	t.Cleanup(s.Workspace.Shutdown)
	// a motion that was being prepared when the previous server stopped: its record is in the database the server uses
	st := motionlib.NewStoreDB(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "veritmo", "motions"), s.Workspace.Store())
	m, _ := st.Create("x", nil)
	if v := s.libraryStore(); v == nil {
		t.Fatal("no store")
	}
	got, _ := st.Get(m.ID)
	if got.State != motionlib.StateFailed || !strings.Contains(got.Error, "interrupted") {
		t.Errorf("got = %+v", got)
	}
	_ = context.Background()
}

// A request that names a motion of the library: the control is the one prepared when it was added, so the vision service
// does nothing, and what is not usable is refused before anything is created.
func TestVideoEditCreate_AMotionOfTheLibraryIsUsedByItsIdentifier(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	s := videoEditServer(t)
	fv := &fakeVision{ready: true}
	s.Stack.VideoEdit.SetVision(fv)
	ran := 0
	s.Stack.VideoEdit.Register(videoedit.MotionControl, videoedit.Impl{Run: func(_ context.Context, job videoedit.Job) (videoedit.Result, error) {
		ran++
		if job.Motion == "" || job.Video != "" {
			t.Errorf("the job must carry the motion and no video: %+v", job)
		}
		if err := os.WriteFile(filepath.Join(job.OutDir, "result.mp4"), []byte("mp4"), 0o644); err != nil {
			return videoedit.Result{}, err
		}
		return videoedit.Result{File: "result.mp4", Format: "mp4"}, nil
	}})

	st := s.libraryStore()
	mk := func(state, errText string, frames int) string {
		m, err := st.Create("m", nil)
		if err != nil {
			t.Fatal(err)
		}
		m.State, m.Error, m.Frames, m.Duration = state, errText, frames, float64(frames)/16
		if err := st.Save(m); err != nil {
			t.Fatal(err)
		}
		return m.ID
	}
	ready := mk(motionlib.StateReady, "", 160) // 10 s
	processing := mk(motionlib.StateProcessing, "", 0)
	failed := mk(motionlib.StateFailed, "no person", 0)
	picture := pngBytes(480, 866)

	for _, tc := range []struct {
		name string
		req  map[string]any
		code int
		want string
	}{
		{"a ready motion", map[string]any{"tool": "motion-control", "motion": ready}, 202, ""},
		{"a window of a long motion", map[string]any{"tool": "motion-control", "motion": ready, "settings": map[string]any{"start": 4}}, 202, ""},
		{"the last window that fits", map[string]any{"tool": "motion-control", "motion": ready, "settings": map[string]any{"start": 7.9}}, 202, ""},
		{"a window past the end", map[string]any{"tool": "motion-control", "motion": ready, "settings": map[string]any{"start": 8}}, 422, "does not fit"},
		{"an unknown motion", map[string]any{"tool": "motion-control", "motion": "mot-20260101-000000-abcdef"}, 422, "does not exist"},
		{"a motion still processing", map[string]any{"tool": "motion-control", "motion": processing}, 422, "being prepared"},
		{"a motion that failed", map[string]any{"tool": "motion-control", "motion": failed}, 422, "preparation failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := postVideoEdit(t, s, tc.req, nil, picture)
			if rec.Code != tc.code || !strings.Contains(rec.Body.String(), tc.want) {
				t.Fatalf("got %d %s, want %d with %q", rec.Code, rec.Body, tc.code, tc.want)
			}
			if tc.code == 202 {
				v := decodeVideoEdit(t, rec)
				waitVideoEdit(t, s, v.ID, GenDone)
				if v.Request.Motion != tc.req["motion"] {
					t.Errorf("request = %+v", v.Request)
				}
			}
		})
	}
	if fv.calls() != 0 {
		t.Errorf("the vision service made %d controls: a motion that is ready needs none", fv.calls())
	}
	if ran != 3 {
		t.Errorf("the tool ran %d times, want 3", ran)
	}
	// the three refused requests created nothing: 3 projects, those of the requests that were accepted
	if entries, _ := os.ReadDir(s.ProjectsDir); len(managedOf(entries, videoPrefix)) != 3 {
		t.Errorf("projects: %v", managedOf(entries, videoPrefix))
	}
	// both a video and a motion, or neither, were already refused
	if rec := postVideoEdit(t, s, map[string]any{"tool": "motion-control", "motion": ready}, clip, picture); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("both: %d %s", rec.Code, rec.Body)
	}
	if rec := postVideoEdit(t, s, map[string]any{"tool": "motion-control"}, nil, picture); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("neither: %d %s", rec.Code, rec.Body)
	}
}

// fakeYtdlp answers the two calls of yt-dlp that adding a motion from an address makes: the length and the title, and the
// download (it writes the clip where -o says).
func fakeYtdlp(clip []byte) runner.CommandRunner {
	fr := runner.NewFakeRunner()
	fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
		if inv.Name != "yt-dlp" {
			return runner.ScriptedResult{}
		}
		for i, a := range inv.Args {
			switch a {
			case "--print":
				return runner.ScriptedResult{Stdout: []byte("4\nA dancer on a roof\n")}
			case "-o":
				out := strings.Replace(inv.Args[i+1], "%(ext)s", "mp4", 1)
				if err := os.WriteFile(out, clip, 0o644); err != nil {
					return runner.ScriptedResult{Err: err}
				}
				return runner.ScriptedResult{}
			}
		}
		return runner.ScriptedResult{}
	})
	return fr
}

func TestMotions_TheAddressItCameFromIsKeptAndShownButNotTheVideo(t *testing.T) {
	fv := &fakeVision{ready: true}
	s := libraryServer(t, fv)
	s.MediaRunner = fakeYtdlp(motionClip(t, "4"))
	s.Look = func(name string) (string, error) { return "/fake/" + name, nil }

	// From an address: the list, the read and the file of the motion say where it came from.
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/motions", map[string]any{"source": "https://youtu.be/abcdefghijk"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("add: %d %s", rec.Code, rec.Body)
	}
	added := decodeMotion(t, rec)
	if added.Source != "https://www.youtube.com/watch?v=abcdefghijk" {
		t.Fatalf("the address, in its canonical form, is in the first answer: %+v", added)
	}
	ready := waitMotion(t, s, added.ID, "ready")
	if ready.Source != added.Source || ready.Title != "A dancer on a roof" {
		t.Fatalf("it stays when the motion is ready: %+v", ready)
	}
	var list []libraryMotionView
	_ = json.Unmarshal(doJSON(t, s.Handler(), http.MethodGet, "/api/motions", nil).Body.Bytes(), &list)
	if len(list) != 1 || list[0].Source != added.Source {
		t.Fatalf("the list says where it came from: %+v", list)
	}
	var doc string
	s.Workspace.Store().DB.Raw("SELECT doc FROM motions WHERE id = ?", added.ID).Scan(&doc)
	if !strings.Contains(doc, `"source":"https://www.youtube.com/watch?v=abcdefghijk"`) {
		t.Fatalf("the record in the database: %s", doc)
	}
	if _, err := os.Stat(filepath.Join(settings.Dir(), "motions", added.ID, "motion.json")); err == nil {
		t.Fatal("a motion.json was written: the record is a row of the database")
	}
	// Not the video: the folder of the motion has no video file.
	if files, _ := filepath.Glob(filepath.Join(filepath.Join(settings.Dir(), "motions"), added.ID, "video*")); len(files) != 0 {
		t.Fatalf("the video is not kept: %v", files)
	}

	// A file that is uploaded has no address.
	up := decodeMotion(t, postMotion(t, s, map[string]any{"title": "Uploaded"}, motionClip(t, "4")))
	if got := waitMotion(t, s, up.ID, "ready"); got.Source != "" {
		t.Fatalf("an uploaded file has no source: %+v", got)
	}
}

// makeMotions puts motions in the library with the titles, tags and states given, the first the oldest.
func makeMotions(t *testing.T, s *testServer, specs ...struct {
	title, state string
	tags         []string
}) []string {
	t.Helper()
	st := s.libraryStore()
	var ids []string
	for i, sp := range specs {
		m, err := st.Create(sp.title, sp.tags)
		if err != nil {
			t.Fatal(err)
		}
		m.State, m.CreatedAt = sp.state, time.Date(2026, 11, 1, 10, i, 0, 0, time.UTC)
		if err := st.Save(m); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, m.ID)
	}
	return ids
}

func TestMotions_TheListIsPagedSortedSearchedAndFilteredByState(t *testing.T) {
	s := libraryServer(t, &fakeVision{ready: true})
	type sp = struct {
		title, state string
		tags         []string
	}
	makeMotions(t, s, sp{"Slow walk", motionlib.StateReady, []string{"walk"}}, sp{"Dance", motionlib.StateReady, []string{"party"}},
		sp{"Wave", motionlib.StateFailed, nil}, sp{"Another walk", motionlib.StateReady, nil})
	get := func(query string) ([]libraryMotionView, string) {
		rec := doJSON(t, s.Handler(), http.MethodGet, "/api/motions"+query, nil)
		var l []libraryMotionView
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &l) != nil {
			t.Fatalf("%s: %d %s", query, rec.Code, rec.Body)
		}
		return l, rec.Header().Get("X-Total-Count")
	}
	names := func(l []libraryMotionView) string {
		var out []string
		for _, m := range l {
			out = append(out, m.Title)
		}
		return strings.Join(out, ",")
	}
	if l, total := get(""); names(l) != "Another walk,Wave,Dance,Slow walk" || total != "" {
		t.Fatalf("the most recent first and no header, as it always was: %s %q", names(l), total)
	}
	if l, total := get("?limit=2&offset=1"); names(l) != "Wave,Dance" || total != "4" {
		t.Fatalf("a page: %s %q", names(l), total)
	}
	if l, _ := get("?sort=title"); names(l) != "Another walk,Dance,Slow walk,Wave" {
		t.Fatalf("%s", names(l))
	}
	if l, total := get("?q=WALK"); names(l) != "Another walk,Slow walk" || total != "2" {
		t.Fatalf("%s %q", names(l), total)
	}
	if l, _ := get("?q=party"); names(l) != "Dance" {
		t.Fatalf("a tag: %s", names(l))
	}
	if l, total := get("?state=ready&limit=10"); names(l) != "Another walk,Dance,Slow walk" || total != "3" {
		t.Fatalf("%s %q", names(l), total)
	}
	for _, bad := range []string{"?sort=state", "?state=done", "?limit=0", "?offset=-1"} {
		if rec := doJSON(t, s.Handler(), http.MethodGet, "/api/motions"+bad, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", bad, rec.Code)
		}
	}
}

func TestMotions_ANewTitleAndNewTagsAreChangedByThemselves(t *testing.T) {
	s := libraryServer(t, &fakeVision{ready: true})
	type sp = struct {
		title, state string
		tags         []string
	}
	id := makeMotions(t, s, sp{"Walk", motionlib.StateReady, []string{"slow", "walk"}})[0]
	patch := func(id string, body any) (*httptest.ResponseRecorder, libraryMotionView) {
		rec := doJSON(t, s.Handler(), http.MethodPatch, "/api/motions/"+id, body)
		var v libraryMotionView
		_ = json.Unmarshal(rec.Body.Bytes(), &v)
		return rec, v
	}
	// a new title: the tags are unchanged
	if rec, v := patch(id, map[string]any{"title": "Slow walk"}); rec.Code != http.StatusOK || v.Title != "Slow walk" || strings.Join(v.Tags, ",") != "slow,walk" || v.State != motionlib.StateReady {
		t.Fatalf("%d %+v", rec.Code, v)
	}
	// new tags: the title is unchanged; the tags go through the checks a new motion's do
	if rec, v := patch(id, map[string]any{"tags": []string{" dance ", "Dance", "", "party"}}); rec.Code != http.StatusOK || v.Title != "Slow walk" || strings.Join(v.Tags, ",") != "dance,party" {
		t.Fatalf("%d %+v", rec.Code, v)
	}
	if got, _ := s.libraryStore().Get(id); got.Title != "Slow walk" || len(got.Tags) != 2 {
		t.Fatalf("the change is kept: %+v", got)
	}
	// an empty title is "motion", as when a motion is added
	if rec, v := patch(id, map[string]any{"title": "  "}); rec.Code != http.StatusOK || v.Title != "motion" {
		t.Fatalf("%d %+v", rec.Code, v)
	}
	// the rest of the motion is not changed
	for name, body := range map[string]any{
		"another field": map[string]any{"state": "failed"}, "the id": map[string]any{"id": "x"}, "a title and a field": map[string]any{"title": "x", "frames": 3},
		"a long title": map[string]any{"title": strings.Repeat("a", motionlib.MaxTitle+1)}, "too many tags": map[string]any{"tags": make([]string, motionlib.MaxTags+1)},
	} {
		if name == "too many tags" {
			tags := []string{}
			for i := 0; i <= motionlib.MaxTags; i++ {
				tags = append(tags, fmt.Sprint("t", i))
			}
			body = map[string]any{"tags": tags}
		}
		if rec, _ := patch(id, body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if got, _ := s.libraryStore().Get(id); got.State != motionlib.StateReady || got.Title != "motion" {
		t.Fatalf("a refused change left a mark: %+v", got)
	}
	if rec, _ := patch("mot-20260101-000000-abcdef", map[string]any{"title": "x"}); rec.Code != http.StatusNotFound {
		t.Fatalf("%d", rec.Code)
	}
	if rec := doJSON(t, s.Handler(), http.MethodPatch, "/api/motions/"+id, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("no body: %d", rec.Code)
	}
}
