package pipeline

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/platform/events"
	"veritmo/internal/platform/runner"
	"veritmo/internal/project/config"
)

func sdCalls(fr *runner.FakeRunner) []runner.Invocation {
	var out []runner.Invocation
	for _, c := range fr.Calls() {
		if c.Name == "sd" {
			out = append(out, c)
		}
	}
	return out
}

func coverConfigured(t *testing.T, env *musicEnv, tracks ...config.Track) *config.Config {
	t.Helper()
	cfg := musicConfig(tracks...)
	cfg.Project = "songs"
	model := filepath.Join("models", "img.safetensors")
	os.MkdirAll(filepath.Join(env.dir, "models"), 0o755)
	os.WriteFile(filepath.Join(env.dir, model), []byte("stub"), 0o644)
	cfg.Defaults.Model = model
	if cfg.Defaults.Music == nil {
		cfg.Defaults.Music = &config.Music{}
	}
	cfg.Defaults.Music.Cover = config.CoverAuto
	return cfg
}

func TestMusicTags_TheExportedMp3CarriesTitleArtistAlbumLyricsAndStyle(t *testing.T) {
	env := newMusicEnv(t)
	cfg := musicConfig(config.Track{ID: "night_drive", Prompt: "lofi beat", Lyrics: "[Verse]\nhola"})
	cfg.Project = "Songs"
	cfg.Defaults.Music = &config.Music{Artist: "Yo"}
	if _, _, err := env.run(t, cfg, Options{}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(env.dir, "out", "Songs", "night_drive.mp3"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b[:3]) != "ID3" {
		t.Fatalf("the mp3 has no tag: %q", b[:min(8, len(b))])
	}
	for _, id := range []string{"TIT2", "TPE1", "TALB", "TRCK", "COMM", "USLT"} {
		if !bytes.Contains(b, []byte(id)) {
			t.Errorf("frame %s missing", id)
		}
	}
	if !bytes.Contains(b, []byte{'n', 0, 'i', 0, 'g', 0, 'h', 0, 't', 0, ' ', 0, 'd', 0, 'r', 0}) {
		t.Error("the title is the id with spaces (night drive) and it is missing")
	}
}

func TestMusicTags_ManyVersionsGetTheirNumberInTheTitle(t *testing.T) {
	env := newMusicEnv(t)
	two := 2
	cfg := musicConfig(config.Track{ID: "a", Prompt: "p", Lyrics: "x", Variants: &two})
	if _, _, err := env.run(t, cfg, Options{}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(env.dir, "out", "songs", "a_v2.mp3"))
	if !bytes.Contains(b, []byte{'(', 0, 'v', 0, '2', 0, ')', 0}) {
		t.Errorf("version 2 does not say so in its title")
	}
}

func TestMusicCover_AutoMakesOnePicturePerTrackEmbedsItAndCachesIt(t *testing.T) {
	env := newMusicEnv(t)
	cfg := coverConfigured(t, env, config.Track{ID: "a", Prompt: "lofi beat", Lyrics: "x"})
	_, evs, err := env.run(t, cfg, Options{})
	if err != nil {
		t.Fatal(err)
	}
	calls := sdCalls(env.fr)
	if len(calls) != 1 {
		t.Fatalf("sd calls = %d, want 1", len(calls))
	}
	args := strings.Join(calls[0].Args, " ")
	if !strings.Contains(args, "Music album artwork, lofi beat") || !strings.Contains(args, "-W 768 -H 768") || !strings.Contains(args, "-s ") {
		t.Errorf("sd args = %s", args)
	}
	if got := strings.Join(phases(evs, "a"), ","); !strings.Contains(got, "phase_start:cover,phase_end:cover") {
		t.Errorf("events = %s", got)
	}
	song, _ := os.ReadFile(filepath.Join(env.dir, "out", "songs", "a.mp3"))
	if !bytes.Contains(song, []byte("APIC")) || !bytes.Contains(song, []byte("\x89PNGcover")) {
		t.Error("the cover is not inside the mp3")
	}
	if _, err := os.Stat(filepath.Join(env.dir, "out", "songs", "a.png")); err != nil {
		t.Errorf("the cover is not exported next to the song: %v", err)
	}
	m := readMusicManifest(t, env.dir)
	if len(m.Tracks) != 1 || m.Tracks[0].Cover != filepath.Join("out", "songs", "a.png") || m.Tracks[0].Title != "a" {
		t.Errorf("manifest entry = %+v", m.Tracks)
	}
	// the same project again: the cover comes from the cache
	env.fr.Reset()
	_, evs, err = env.run(t, cfg, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(sdCalls(env.fr)) != 0 || !strings.Contains(strings.Join(phases(evs, "a"), ","), "skipped:cover") {
		t.Errorf("a cached cover must not run sd: calls %d, events %s", len(sdCalls(env.fr)), strings.Join(phases(evs, "a"), ","))
	}
}

func TestMusicCover_TheTrackOwnPictureIsUsedAsItIsAndOffIsRespected(t *testing.T) {
	env := newMusicEnv(t)
	cfg := coverConfigured(t, env, config.Track{ID: "a", Prompt: "p", Lyrics: "x", Cover: "mine.jpg"}, config.Track{ID: "b", Prompt: "p", Lyrics: "x", Cover: config.CoverOff})
	os.MkdirAll(filepath.Join(env.dir, "sources"), 0o755)
	os.WriteFile(filepath.Join(env.dir, "sources", "mine.jpg"), []byte("JPEGDATA"), 0o644)
	if _, _, err := env.run(t, cfg, Options{}); err != nil {
		t.Fatal(err)
	}
	if n := len(sdCalls(env.fr)); n != 0 {
		t.Errorf("sd ran %d times: an own picture and off make none", n)
	}
	a, _ := os.ReadFile(filepath.Join(env.dir, "out", "songs", "a.mp3"))
	if !bytes.Contains(a, []byte("image/jpeg")) || !bytes.Contains(a, []byte("JPEGDATA")) {
		t.Error("the own picture is not embedded")
	}
	b, _ := os.ReadFile(filepath.Join(env.dir, "out", "songs", "b.mp3"))
	if bytes.Contains(b, []byte("APIC")) {
		t.Error("a track with cover off has a picture")
	}
	if _, err := os.Stat(filepath.Join(env.dir, "out", "songs", "a.jpg")); err != nil {
		t.Errorf("the own picture is not copied next to the song: %v", err)
	}
}

func TestMusicCover_WhenThePictureFailsTheSongIsStillMade(t *testing.T) {
	env := newMusicEnv(t)
	cfg := coverConfigured(t, env, config.Track{ID: "a", Prompt: "p", Lyrics: "x"})
	*env.failSD = true
	_, evs, err := env.run(t, cfg, Options{})
	if err != nil {
		t.Fatalf("a failed cover must not fail the run: %v", err)
	}
	var warned bool
	for _, e := range evs {
		if e.Type == events.TypeWarn && strings.Contains(e.Message, "cover skipped") {
			warned = true
		}
	}
	if !warned {
		t.Error("no warning says the cover was skipped")
	}
	song, _ := os.ReadFile(filepath.Join(env.dir, "out", "songs", "a.mp3"))
	if len(song) == 0 || bytes.Contains(song, []byte("APIC")) {
		t.Error("the song must exist without a cover")
	}
}

func TestMusicTags_WavHasNoTags(t *testing.T) {
	env := newMusicEnv(t)
	cfg := musicConfig(config.Track{ID: "a", Prompt: "p", Lyrics: "x"})
	cfg.Defaults.Music = &config.Music{OutputFormat: config.MusicFormatWAV}
	if _, _, err := env.run(t, cfg, Options{}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(env.dir, "out", "songs", "a.wav"))
	if len(b) == 0 || string(b[:3]) == "ID3" {
		t.Error("a wav must be left as it was made")
	}
}
