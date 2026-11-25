package workspace

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/platform/runner"
	"veritmo/internal/server/api"
)

func TestParseYouTubeURL(t *testing.T) {
	const want = "https://www.youtube.com/watch?v=dQw4w9WgXcQ"
	ok := []string{
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ",
		"https://youtube.com/watch?v=dQw4w9WgXcQ&list=PLx&t=42",
		"https://m.youtube.com/watch?v=dQw4w9WgXcQ",
		"https://music.youtube.com/watch?v=dQw4w9WgXcQ",
		"https://youtu.be/dQw4w9WgXcQ?si=abc",
		"https://www.youtube.com/shorts/dQw4w9WgXcQ",
		"https://www.youtube.com/live/dQw4w9WgXcQ",
		"https://www.youtube.com/embed/dQw4w9WgXcQ",
		"  https://YOUTU.BE/dQw4w9WgXcQ  ",
		"https://www.youtube.com:443/watch?v=dQw4w9WgXcQ",
	}
	for _, raw := range ok {
		got, err := api.ParseYouTubeURL(raw)
		if err != nil || got != want {
			t.Errorf("%q: got %q, %v", raw, got, err)
		}
	}
	bad := []string{
		"", "dQw4w9WgXcQ", "http://www.youtube.com/watch?v=dQw4w9WgXcQ",
		"https://example.com/a.mp3", "https://www.youtube.com.evil.com/watch?v=dQw4w9WgXcQ",
		"https://evil.com/youtube.com/watch?v=dQw4w9WgXcQ", "https://notyoutube.com/watch?v=dQw4w9WgXcQ",
		"https://user:pw@www.youtube.com/watch?v=dQw4w9WgXcQ", "https://www.youtube.com:8080/watch?v=dQw4w9WgXcQ",
		"https://www.youtube.com/playlist?list=PLx", "https://www.youtube.com/watch", "https://www.youtube.com/watch?v=short",
		"https://www.youtube.com/watch?v=--output=/etc/x", "https://youtu.be/", "https://youtu.be/dQw4w9WgXcQ/extra",
		"https://www.youtube.com/@canal", "file:///etc/passwd", "https://localhost/watch?v=dQw4w9WgXcQ",
		"https://127.0.0.1/watch?v=dQw4w9WgXcQ",
	}
	for _, raw := range bad {
		if got, err := api.ParseYouTubeURL(raw); err == nil {
			t.Errorf("%q must be refused, got %q", raw, got)
		}
	}
}

func TestSourceBaseNameAndUniqueness(t *testing.T) {
	cases := map[string]string{
		"Mi Canción Número 1 (Official Video)": "mi-cancion-numero-1-official-video",
		"  ¡Hola, señor! ":                     "hola-senor",
		"日本語のタイトル":                             "youtube",
		"":                                     "youtube",
		strings.Repeat("a", 100):               strings.Repeat("a", 60),
	}
	for in, want := range cases {
		if got := sourceBaseName(in); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "song.mp3"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "song-2.mp3"), []byte("x"), 0o644)
	if got := uniqueSourceName(dir, "song"); got != "song-3.mp3" {
		t.Fatalf("got %q", got)
	}
	if got := uniqueSourceName(dir, "other"); got != "other.mp3" {
		t.Fatalf("got %q", got)
	}
}

const goodURL = "https://youtu.be/dQw4w9WgXcQ"

// fakeYtDlp scripts yt-dlp: the metadata call prints duration and title; the
// download call writes the mp3 where -o says. It returns the runner for asserts.
func fakeYtDlp(t *testing.T, s *testServer, duration, title string, failMeta, failDownload error) *runner.FakeRunner {
	t.Helper()
	fr := runner.NewFakeRunner()
	fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
		joined := strings.Join(inv.Args, " ")
		if strings.Contains(joined, "--print") {
			if failMeta != nil {
				return runner.ScriptedResult{Err: failMeta}
			}
			return runner.ScriptedResult{Stdout: []byte(duration + "\n" + title + "\n")}
		}
		if failDownload != nil {
			return runner.ScriptedResult{Err: failDownload}
		}
		for i, a := range inv.Args {
			if a == "-o" {
				os.WriteFile(strings.Replace(inv.Args[i+1], "%(ext)s", "mp3", 1), []byte("AUDIO"), 0o644)
			}
		}
		return runner.ScriptedResult{}
	})
	s.mediaRunner = fr
	s.look = func(name string) (string, error) { return "/fake/bin/" + name, nil }
	return fr
}

func postFromURL(t *testing.T, s *testServer, id, body string) (int, string) {
	t.Helper()
	rec := call(s.Handler(), http.MethodPost, "/api/projects/"+id+"/sources/from-url", body)
	return rec.Code, rec.Body.String()
}

func sourcesEntries(t *testing.T, projects, id string) []string {
	t.Helper()
	entries, _ := os.ReadDir(filepath.Join(projects, id, "sources"))
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestSourceFromURL_AddsTheAudioWithoutAccountsOrCookies(t *testing.T) {
	s, projects := newProjectsServer(t)
	createProject(t, s, "songs")
	fr := fakeYtDlp(t, s, "215.4", "Mi Canción (Official)", nil, nil)
	code, body := postFromURL(t, s, "songs", `{"url":"`+goodURL+`"}`)
	if code != http.StatusCreated || !strings.Contains(body, `"mi-cancion-official.mp3"`) {
		t.Fatalf("%d %s", code, body)
	}
	b, err := os.ReadFile(filepath.Join(projects, "songs", "sources", "mi-cancion-official.mp3"))
	if err != nil || string(b) != "AUDIO" {
		t.Fatalf("%v %q", err, b)
	}
	if got := strings.Join(sourcesEntries(t, projects, "songs"), ","); got != "mi-cancion-official.mp3" {
		t.Fatalf("sources = %s (no temporary may stay)", got)
	}
	calls := fr.Calls()
	if len(calls) != 2 {
		t.Fatalf("calls = %d", len(calls))
	}
	for _, c := range calls {
		if c.Name != "yt-dlp" || c.Args[0] != "--ignore-config" || c.Args[len(c.Args)-2] != "--" ||
			c.Args[len(c.Args)-1] != "https://www.youtube.com/watch?v=dQw4w9WgXcQ" {
			t.Errorf("the address must be the last argument after --: %v", c.Args)
		}
		for _, a := range c.Args {
			if strings.Contains(a, "cookies") || a == "--username" || a == "--password" {
				t.Errorf("no account or cookie option is allowed: %v", c.Args)
			}
		}
	}
	dl := strings.Join(calls[1].Args, " ")
	for _, want := range []string{"--no-playlist", "-x", "--audio-format mp3", "--max-filesize"} {
		if !strings.Contains(dl, want) {
			t.Errorf("download call misses %q: %s", want, dl)
		}
	}
	// a second video with the same title gets a numeric suffix
	if code, body := postFromURL(t, s, "songs", `{"url":"`+goodURL+`"}`); code != http.StatusCreated || !strings.Contains(body, `"mi-cancion-official-2.mp3"`) {
		t.Fatalf("%d %s", code, body)
	}
}

func TestSourceFromURL_GivenNameAndConflicts(t *testing.T) {
	s, projects := newProjectsServer(t)
	createProject(t, s, "songs")
	fakeYtDlp(t, s, "100", "t", nil, nil)
	if code, body := postFromURL(t, s, "songs", `{"url":"`+goodURL+`","name":"ref.mp3"}`); code != http.StatusCreated || !strings.Contains(body, `"ref.mp3"`) {
		t.Fatalf("%d %s", code, body)
	}
	if code, _ := postFromURL(t, s, "songs", `{"url":"`+goodURL+`","name":"ref.mp3"}`); code != http.StatusConflict {
		t.Fatalf("an existing name must answer 409, got %d", code)
	}
	for _, name := range []string{"ref.wav", "../x.mp3", "a b.mp3", "ref"} {
		if code, _ := postFromURL(t, s, "songs", `{"url":"`+goodURL+`","name":"`+name+`"}`); code != http.StatusBadRequest {
			t.Errorf("name %q: %d, want 400", name, code)
		}
	}
	if got := strings.Join(sourcesEntries(t, projects, "songs"), ","); got != "ref.mp3" {
		t.Fatalf("sources = %s", got)
	}
}

func TestSourceFromURL_RefusesWhatIsNotYouTubeBeforeAnyDownload(t *testing.T) {
	s, _ := newProjectsServer(t)
	createProject(t, s, "songs")
	fr := fakeYtDlp(t, s, "10", "t", nil, nil)
	for _, u := range []string{"https://example.com/a.mp3", "http://youtube.com/watch?v=dQw4w9WgXcQ", "", "https://www.youtube.com/playlist?list=PLx"} {
		code, body := postFromURL(t, s, "songs", `{"url":"`+u+`"}`)
		if code != http.StatusBadRequest || !strings.Contains(body, "YouTube") {
			t.Errorf("%q: %d %s", u, code, body)
		}
	}
	if code, _ := postFromURL(t, s, "songs", `not json`); code != http.StatusBadRequest {
		t.Fatalf("bad JSON: %d", code)
	}
	if code, _ := postFromURL(t, s, "nope", `{"url":"`+goodURL+`"}`); code != http.StatusNotFound {
		t.Fatalf("unknown project: %d", code)
	}
	if len(fr.Calls()) != 0 {
		t.Fatalf("nothing may run: %v", fr.Calls())
	}
}

func TestSourceFromURL_TooLongLiveOrOversizedLeavesNothing(t *testing.T) {
	s, projects := newProjectsServer(t)
	createProject(t, s, "songs")
	fakeYtDlp(t, s, "1201", "long", nil, nil)
	if code, body := postFromURL(t, s, "songs", `{"url":"`+goodURL+`"}`); code != http.StatusUnprocessableEntity || !strings.Contains(body, "limit is 20") {
		t.Fatalf("%d %s", code, body)
	}
	fakeYtDlp(t, s, "NA", "live", nil, nil)
	if code, body := postFromURL(t, s, "songs", `{"url":"`+goodURL+`"}`); code != http.StatusUnprocessableEntity || !strings.Contains(body, "live") {
		t.Fatalf("%d %s", code, body)
	}
	if got := sourcesEntries(t, projects, "songs"); len(got) != 0 {
		t.Fatalf("nothing may stay: %v", got)
	}
}

func TestSourceFromURL_SignInAndOtherFailuresAre422WithTheReason(t *testing.T) {
	s, projects := newProjectsServer(t)
	createProject(t, s, "songs")
	fakeYtDlp(t, s, "", "", fmt.Errorf("ERROR: [youtube] x: Sign in to confirm you’re not a bot"), nil)
	code, body := postFromURL(t, s, "songs", `{"url":"`+goodURL+`"}`)
	if code != http.StatusUnprocessableEntity || !strings.Contains(body, "does not use accounts or cookies") {
		t.Fatalf("%d %s", code, body)
	}
	fakeYtDlp(t, s, "10", "t", nil, fmt.Errorf("ERROR: Video unavailable"))
	code, body = postFromURL(t, s, "songs", `{"url":"`+goodURL+`"}`)
	if code != http.StatusUnprocessableEntity || !strings.Contains(body, "Video unavailable") || !strings.Contains(body, "get-ytdlp.sh") {
		t.Fatalf("%d %s", code, body)
	}
	if got := sourcesEntries(t, projects, "songs"); len(got) != 0 {
		t.Fatalf("nothing may stay: %v", got)
	}
}

func TestSourceFromURL_MissingProgramsAre503WithTheFix(t *testing.T) {
	s, _ := newProjectsServer(t)
	createProject(t, s, "songs")
	fakeYtDlp(t, s, "10", "t", nil, nil)
	s.look = func(name string) (string, error) {
		if name == "yt-dlp" {
			return "", fmt.Errorf("not found")
		}
		return "/fake/" + name, nil
	}
	if code, body := postFromURL(t, s, "songs", `{"url":"`+goodURL+`"}`); code != http.StatusServiceUnavailable || !strings.Contains(body, "scripts/get-ytdlp.sh") {
		t.Fatalf("%d %s", code, body)
	}
	s.look = func(name string) (string, error) {
		if name == "ffmpeg" {
			return "", fmt.Errorf("not found")
		}
		return "/fake/" + name, nil
	}
	if code, body := postFromURL(t, s, "songs", `{"url":"`+goodURL+`"}`); code != http.StatusServiceUnavailable || !strings.Contains(body, "ffmpeg") {
		t.Fatalf("%d %s", code, body)
	}
}

func TestSourceFromURL_CancellingStopsTheDownloadAndLeavesNothing(t *testing.T) {
	s, projects := newProjectsServer(t)
	createProject(t, s, "songs")
	fr := runner.NewFakeRunner()
	fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
		if strings.Contains(strings.Join(inv.Args, " "), "--print") {
			return runner.ScriptedResult{Stdout: []byte("10\nt\n")}
		}
		// the download is "running" when the client goes away
		os.WriteFile(filepath.Join(filepath.Dir(inv.Args[indexOf(inv.Args, "-o")+1]), "audio.part"), []byte("half"), 0o644)
		return runner.ScriptedResult{Err: context.Canceled}
	})
	s.mediaRunner = fr
	s.look = func(name string) (string, error) { return "/fake/" + name, nil }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "/api/projects/songs/sources/from-url", strings.NewReader(`{"url":"`+goodURL+`"}`))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if got := sourcesEntries(t, projects, "songs"); len(got) != 0 {
		t.Fatalf("a cancelled download must leave nothing: %v", got)
	}
}

func indexOf(a []string, s string) int {
	for i, x := range a {
		if x == s {
			return i
		}
	}
	return -1
}
