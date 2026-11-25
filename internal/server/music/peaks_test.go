package music

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/platform/runner"
)

// f32 builds decoded mono audio at peaksRate: silence, a tone of the given
// amplitude and silence again.
func f32(lead, tone, tail float64, amp float32) []byte {
	n := func(sec float64) int { return int(sec * peaksRate) }
	var out []byte
	put := func(v float32) { out = binary.LittleEndian.AppendUint32(out, math.Float32bits(v)) }
	for i := 0; i < n(lead); i++ {
		put(0)
	}
	for i := 0; i < n(tone); i++ {
		put(amp * float32(math.Sin(2*math.Pi*440*float64(i)/peaksRate)))
	}
	for i := 0; i < n(tail); i++ {
		put(0)
	}
	return out
}

func fakePeaks(t *testing.T, s *testServer, audio []byte, fail error) *runner.FakeRunner {
	t.Helper()
	fr := runner.NewFakeRunner()
	fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
		if fail != nil {
			return runner.ScriptedResult{Err: fail}
		}
		return runner.ScriptedResult{Stdout: audio}
	})
	s.MediaRunner = fr
	s.Look = func(name string) (string, error) { return "/fake/" + name, nil }
	return fr
}

func getPeaks(t *testing.T, s *testServer, id, query string) (int, peaksAnswer, string) {
	t.Helper()
	rec := call(s.Handler(), http.MethodGet, "/api/projects/"+id+"/peaks?"+query, "")
	var a peaksAnswer
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &a); err != nil {
			t.Fatal(err)
		}
	}
	return rec.Code, a, rec.Body.String()
}

func TestComputePeaks(t *testing.T) {
	// 1 s silence, 2 s of a half-scale tone, 1 s silence
	raw := f32(1, 2, 1, 0.5)
	samples := make([]float32, len(raw)/4)
	for i := range samples {
		samples[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[i*4:]))
	}
	a := computePeaks(samples, peaksRate, 40)
	if a.Duration != 4 || a.Buckets != 40 || len(a.Peaks) != 40 {
		t.Fatalf("shape: %+v", a)
	}
	for i, p := range a.Peaks {
		if p < 0 || p > 1 {
			t.Fatalf("peak %d = %v out of range", i, p)
		}
	}
	if a.Peaks[2] != 0 || a.Peaks[37] != 0 {
		t.Fatalf("the silent edges must be 0: %v %v", a.Peaks[2], a.Peaks[37])
	}
	if a.Peaks[20] < 0.45 || a.Peaks[20] > 0.5 {
		t.Fatalf("the tone peaks near 0.5, got %v", a.Peaks[20])
	}
	if a.Active == nil || math.Abs(a.Active.Start-1) > 0.15 || math.Abs(a.Active.End-3) > 0.15 {
		t.Fatalf("active = %+v, want about 1..3", a.Active)
	}
	if got := computePeaks(make([]float32, 8000), peaksRate, 10); got.Active != nil {
		t.Fatalf("a silent file has no active range: %+v", got.Active)
	}
	if got := computePeaks(nil, peaksRate, 5); got.Duration != 0 || len(got.Peaks) != 5 || got.Active != nil {
		t.Fatalf("empty: %+v", got)
	}
	// clipping above full scale is reported as 1
	if got := computePeaks([]float32{3, -3, 3, -3}, 4, 2); got.Peaks[0] != 1 {
		t.Fatalf("peaks above full scale clamp to 1: %v", got.Peaks)
	}
}

func TestPeaks_ServesTheShapeOfASourceAndOfAnExport(t *testing.T) {
	s, projects := newProjectsServer(t)
	createProject(t, s, "songs")
	fr := fakePeaks(t, s, f32(1.7, 20, 4.3, 0.5), nil)
	putSource(t, s, "songs", "base.mp3", []byte("MP3"))
	os.MkdirAll(filepath.Join(projects, "songs", "out", "songs"), 0o755)
	os.WriteFile(filepath.Join(projects, "songs", "out", "songs", "voz.mp3"), []byte("MP3"), 0o644)

	code, a, body := getPeaks(t, s, "songs", "file=sources/base.mp3&buckets=400")
	if code != http.StatusOK || a.Buckets != 400 || len(a.Peaks) != 400 || a.Duration != 26 {
		t.Fatalf("%d %s", code, body)
	}
	if a.Active == nil || math.Abs(a.Active.Start-1.7) > 0.15 || math.Abs(a.Active.End-21.7) > 0.15 {
		t.Fatalf("the active range must skip the silent edges: %+v", a.Active)
	}
	last := fr.Calls()[len(fr.Calls())-1]
	if last.Name != "ffmpeg" || !strings.Contains(strings.Join(last.Args, " "), "-ac 1 -ar 8000 -f f32le -") {
		t.Fatalf("decoding call = %v", last)
	}
	if code, a, _ := getPeaks(t, s, "songs", "file=out/songs/voz.mp3"); code != http.StatusOK || a.Buckets != peaksDefaultBuckets || len(a.Peaks) != 800 {
		t.Fatalf("export / default buckets: %d %d", code, a.Buckets)
	}
}

func TestPeaks_RefusesOtherPlacesAndBadParameters(t *testing.T) {
	s, _ := newProjectsServer(t)
	createProject(t, s, "songs")
	fr := fakePeaks(t, s, f32(0, 1, 0, 0.5), nil)
	for _, q := range []string{
		"", "file=tmp/x.mp3", "file=../x.mp3", "file=/etc/passwd.mp3", "file=sources/sub/x.mp3", "file=sources/a.ogg",
		"file=out/x.mp3", "file=out/a/b/x.mp3", "file=scenes.yaml", "file=sources/..%2Fx.mp3", "file=sources/a%20b.mp3",
		"file=sources/a.mp3&buckets=0", "file=sources/a.mp3&buckets=4001", "file=sources/a.mp3&buckets=100000", "file=sources/a.mp3&buckets=abc",
		"file=sources/a.mp3&bpm=abc", "file=sources/a.mp3&bpm=10", "file=sources/a.mp3&bpm=500",
	} {
		if code, _, body := getPeaks(t, s, "songs", q); code != http.StatusBadRequest {
			t.Errorf("%q: %d %s, want 400", q, code, body)
		}
	}
	if len(fr.Calls()) != 0 {
		t.Fatalf("nothing may be read: %v", fr.Calls())
	}
	if code, _, _ := getPeaks(t, s, "nope", "file=sources/a.mp3"); code != http.StatusNotFound {
		t.Fatalf("unknown project: %d", code)
	}
	if code, _, _ := getPeaks(t, s, "songs", "file=sources/missing.mp3"); code != http.StatusNotFound {
		t.Fatalf("missing file: %d", code)
	}
}

func TestPeaks_SymlinkOutOfTheProjectIsRefused(t *testing.T) {
	s, projects := newProjectsServer(t)
	createProject(t, s, "songs")
	fakePeaks(t, s, f32(0, 1, 0, 0.5), nil)
	outside := filepath.Join(t.TempDir(), "secret.mp3")
	os.WriteFile(outside, []byte("X"), 0o644)
	os.MkdirAll(filepath.Join(projects, "songs", "sources"), 0o755)
	if err := os.Symlink(outside, filepath.Join(projects, "songs", "sources", "link.mp3")); err != nil {
		t.Skip("cannot make symlinks")
	}
	if code, _, _ := getPeaks(t, s, "songs", "file=sources/link.mp3"); code != http.StatusBadRequest {
		t.Fatalf("a symlink that leaves the project must be refused, got %d", code)
	}
}

func TestPeaks_FailuresAndSilentFile(t *testing.T) {
	s, _ := newProjectsServer(t)
	createProject(t, s, "songs")
	putSource(t, s, "songs", "a.mp3", []byte("MP3"))

	fakePeaks(t, s, nil, fmt.Errorf("Invalid data found when processing input"))
	if code, _, body := getPeaks(t, s, "songs", "file=sources/a.mp3"); code != http.StatusUnprocessableEntity || !strings.Contains(body, "Invalid data") {
		t.Fatalf("unreadable: %d %s", code, body)
	}
	fakePeaks(t, s, f32(0, 1, 0, 0.5), nil)
	s.Look = func(string) (string, error) { return "", fmt.Errorf("not found") }
	if code, _, body := getPeaks(t, s, "songs", "file=sources/a.mp3"); code != http.StatusServiceUnavailable || !strings.Contains(body, "ffmpeg") {
		t.Fatalf("no ffmpeg: %d %s", code, body)
	}
	fakePeaks(t, s, f32(3, 0, 0, 0), nil) // 3 s of pure silence
	code, a, _ := getPeaks(t, s, "songs", "file=sources/a.mp3")
	if code != http.StatusOK || a.Active != nil || a.Duration != 3 {
		t.Fatalf("silent file: %d %+v", code, a)
	}
}

func TestPeaks_AreCachedByContentAndRecomputedWhenTheFileChanges(t *testing.T) {
	s, projects := newProjectsServer(t)
	createProject(t, s, "songs")
	fr := fakePeaks(t, s, f32(0, 1, 0, 0.5), nil)
	putSource(t, s, "songs", "a.mp3", []byte("ONE"))
	getPeaks(t, s, "songs", "file=sources/a.mp3&buckets=50")
	getPeaks(t, s, "songs", "file=sources/a.mp3&buckets=50")
	if n := len(fr.Calls()); n != 1 {
		t.Fatalf("the second request must come from the cache, ffmpeg ran %d times", n)
	}
	getPeaks(t, s, "songs", "file=sources/a.mp3&buckets=60")
	if n := len(fr.Calls()); n != 2 {
		t.Fatalf("another bucket count is another answer, ffmpeg ran %d times", n)
	}
	os.WriteFile(filepath.Join(projects, "songs", "sources", "a.mp3"), []byte("A LONGER FILE"), 0o644)
	getPeaks(t, s, "songs", "file=sources/a.mp3&buckets=50")
	if n := len(fr.Calls()); n != 3 {
		t.Fatalf("a changed file must be read again, ffmpeg ran %d times", n)
	}
}

func TestPeaks_GivesWhereThePulseFallsOnlyWhenAskedWithATempo(t *testing.T) {
	s, _ := newProjectsServer(t)
	createProject(t, s, "songs")
	var audio []byte
	for _, v := range clicks(30, 120, 0.23) {
		audio = binary.LittleEndian.AppendUint32(audio, math.Float32bits(v))
	}
	fakePeaks(t, s, audio, nil)
	putSource(t, s, "songs", "beat.mp3", []byte("MP3"))

	if _, a, _ := getPeaks(t, s, "songs", "file=sources/beat.mp3"); a.BeatOffset != nil {
		t.Fatalf("without a tempo there is no pulse to report: %v", *a.BeatOffset)
	}
	code, a, body := getPeaks(t, s, "songs", "file=sources/beat.mp3&bpm=120")
	if code != http.StatusOK || a.BeatOffset == nil || math.Abs(*a.BeatOffset-0.23) > 0.02 {
		t.Fatalf("%d %s", code, body)
	}
	// another tempo is another answer (the cache is per tempo), and a wrong one finds no clear pulse
	if _, a, _ := getPeaks(t, s, "songs", "file=sources/beat.mp3&bpm=150"); a.BeatOffset != nil && math.Abs(*a.BeatOffset-0.23) < 0.02 {
		t.Logf("a wrong tempo still found a phase: %v", *a.BeatOffset)
	}
}

func TestLoudness_MeasuresACachedAudioAndSaysNullForSilence(t *testing.T) {
	s, _ := newProjectsServer(t)
	createProject(t, s, "songs")
	fr := fakePeaks(t, s, []byte("frame:0 pts:0 pts_time:0\nlavfi.r128.I=-18.5\nframe:1 pts:1 pts_time:0.1\nlavfi.r128.I=-17.2\n"), nil)
	putSource(t, s, "songs", "voz.mp3", []byte("MP3"))
	putSource(t, s, "songs", "mudo.mp3", []byte("MP3"))

	get := func(q string) (int, string) {
		rec := call(s.Handler(), http.MethodGet, "/api/projects/songs/loudness?"+q, "")
		return rec.Code, strings.TrimSpace(rec.Body.String())
	}
	if code, body := get("file=sources/voz.mp3"); code != http.StatusOK || body != `{"lufs":-17.2}` {
		t.Fatalf("%d %s", code, body)
	}
	before := len(fr.Calls())
	if code, _ := get("file=sources/voz.mp3"); code != http.StatusOK || len(fr.Calls()) != before {
		t.Fatalf("the second answer must come from the cache (%d calls before, %d after)", before, len(fr.Calls()))
	}
	fakePeaks(t, s, []byte("lavfi.r128.I=-inf\n"), nil)
	if code, body := get("file=sources/mudo.mp3"); code != http.StatusOK || body != `{"lufs":null}` {
		t.Fatalf("silence: %d %s", code, body)
	}
	for _, q := range []string{"", "file=tmp/x.mp3", "file=../x.mp3", "file=sources/a.ogg", "file=scenes.yaml"} {
		if code, body := get(q); code != http.StatusBadRequest {
			t.Errorf("%q: %d %s, want 400", q, code, body)
		}
	}
	if code, _ := get("file=sources/nope.mp3"); code != http.StatusNotFound {
		t.Errorf("missing: %d", code)
	}
}
