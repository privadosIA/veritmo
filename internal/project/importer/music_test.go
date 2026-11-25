package importer

import (
	"strings"
	"testing"

	"veritmo/internal/project/config"
)

func songEntry(id, prompt string) Entry { return Entry{ID: id, Title: "t", FullPrompt: prompt} }

func mapSongs(t *testing.T, entries ...Entry) *Result {
	t.Helper()
	res, err := MapMusic(entries, nil, Options{Project: "songs"})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestMapMusic_StyleAndLyricsKeepTheirLineBreaks(t *testing.T) {
	res := mapSongs(t, songEntry("balada", "Spanish pop ballad with piano\nand warm male vocals\nLyrics:\n[Verse]\nCamino por la calle\n\n[Chorus]\nVuelvo a casa"))
	if res.Imported != 1 || res.Rejected != 0 {
		t.Fatalf("%+v", res.Reports)
	}
	tr := res.Config.Tracks[0]
	if tr.Prompt != "Spanish pop ballad with piano and warm male vocals" {
		t.Errorf("prompt = %q", tr.Prompt)
	}
	if tr.Lyrics != "[Verse]\nCamino por la calle\n\n[Chorus]\nVuelvo a casa" {
		t.Errorf("lyrics = %q", tr.Lyrics)
	}
	if res.Config.Output.Mode != config.ModeMusic || len(res.Config.Scenes) != 0 {
		t.Errorf("config = %+v", res.Config)
	}
}

func TestMapMusic_LyricsMayStartOnTheMarkerLine(t *testing.T) {
	res := mapSongs(t, songEntry("a", "Folk\nLyrics: [Verse]\nla la"))
	if got := res.Config.Tracks[0].Lyrics; got != "[Verse]\nla la" {
		t.Fatalf("lyrics = %q", got)
	}
}

func TestMapMusic_MetadataMarkersSetTheirFields(t *testing.T) {
	res := mapSongs(t, songEntry("a", "Folk guitar\nLanguage: ES\nBPM: 90\nDuration: 120\nKey: C minor\nTime signature: 3\nSeed: 7\nVariants: 3"))
	if res.Imported != 1 {
		t.Fatalf("%+v", res.Reports)
	}
	tr := res.Config.Tracks[0]
	if tr.VocalLanguage != "es" || *tr.BPM != 90 || *tr.DurationSeconds != 120 || tr.KeyScale != "C minor" || tr.TimeSignature != "3" || *tr.Seed != 7 || *tr.Variants != 3 {
		t.Fatalf("%+v", tr)
	}
}

func TestMapMusic_MarkersAreCaseInsensitiveAndDurationMayEndInS(t *testing.T) {
	res := mapSongs(t, songEntry("a", "Folk\nlyrics:\nla\nbpm: 80\nDURATION: 45s\ntime signature: 4\ntimesignature: 4"))
	tr := res.Config.Tracks[0]
	if tr.Lyrics != "la" || *tr.BPM != 80 || *tr.DurationSeconds != 45 || tr.TimeSignature != "4" {
		t.Fatalf("%+v", tr)
	}
}

func TestMapMusic_ASeedOfZeroIsKept(t *testing.T) {
	res := mapSongs(t, songEntry("a", "Folk\nSeed: 0"))
	if s := res.Config.Tracks[0].Seed; s == nil || *s != 0 {
		t.Fatalf("seed = %v", s)
	}
}

func TestMapMusic_Instrumental(t *testing.T) {
	for name, prompt := range map[string]string{
		"bare line":          "Lo-fi hip hop\nInstrumental",
		"bracketed lyrics":   "Lo-fi hip hop\nLyrics: [Instrumental]",
		"lowercase and dots": "Lo-fi hip hop\ninstrumental.",
	} {
		res := mapSongs(t, songEntry("a", prompt))
		tr := res.Config.Tracks[0]
		if !tr.Instrumental || tr.Lyrics != "" || res.Instrumental != 1 {
			t.Errorf("%s: %+v (instrumental %d)", name, tr, res.Instrumental)
		}
	}
}

func TestMapMusic_InstrumentalDiscardsLyricsWithANote(t *testing.T) {
	res := mapSongs(t, songEntry("a", "Lo-fi\nInstrumental\nLyrics:\n[Verse]\nla\nla"))
	tr := res.Config.Tracks[0]
	if !tr.Instrumental || tr.Lyrics != "" {
		t.Fatalf("%+v", tr)
	}
	if got := strings.Join(res.Reports[0].Discarded, "|"); !strings.Contains(got, "lyrics (3 lines): the track is instrumental") {
		t.Fatalf("discarded = %q", got)
	}
}

func TestMapMusic_SectionTagsInsideLyricsAreNotMarkers(t *testing.T) {
	res := mapSongs(t, songEntry("a", "Folk\nLyrics:\n[Verse]\nla\n[Instrumental]\n[Outro]\nbye"))
	tr := res.Config.Tracks[0]
	if tr.Instrumental || !strings.Contains(tr.Lyrics, "[Instrumental]") || !strings.Contains(tr.Lyrics, "[Outro]") {
		t.Fatalf("a [Instrumental] section tag is lyrics, not the instrumental marker: %+v", tr)
	}
}

func TestMapMusic_LoraInfoIsDiscardedAndTitleIgnored(t *testing.T) {
	e := Entry{ID: "a", Title: "A title", LoraInfo: "eurobeat", FullPrompt: "Dance"}
	res := mapSongs(t, e)
	if got := strings.Join(res.Reports[0].Discarded, "|"); !strings.Contains(got, `lora_info "eurobeat"`) || !strings.Contains(got, "adapters are chosen in the studio") {
		t.Fatalf("discarded = %q", got)
	}
	if res.Config.Tracks[0].Adapter != "" {
		t.Fatal("an adapter must not be guessed from lora_info")
	}
}

func TestMapMusic_RejectsOnlyTheBadEntries(t *testing.T) {
	cases := map[string]string{
		"BPM: fast":           "Folk\nBPM: fast",
		"BPM: 0":              "Folk\nBPM: 0",
		"Duration: soon":      "Folk\nDuration: soon",
		"Duration: -5":        "Folk\nDuration: -5",
		"Seed: x":             "Folk\nSeed: x",
		"Variants: 0":         "Folk\nVariants: 0",
		"no style":            "Lyrics:\nla",
		"empty":               "",
		"second Lyrics block": "Folk\nLyrics:\nla\nLyrics:\nlo",
	}
	for name, prompt := range cases {
		res := mapSongs(t, songEntry("bad", prompt), songEntry("good", "Folk"))
		if res.Imported != 1 || res.Rejected != 1 || res.Config.Tracks[0].ID != "good" {
			t.Errorf("%s: imported %d rejected %d tracks %+v", name, res.Imported, res.Rejected, res.Config.Tracks)
			continue
		}
		if res.Reports[0].Rejected == "" {
			t.Errorf("%s: the reason must be reported", name)
		}
	}
}

func TestMapMusic_ATrackIdThatCannotBeAFileNameIsRejected(t *testing.T) {
	res := mapSongs(t, songEntry("my song!", "Folk"), songEntry("../x", "Folk"), songEntry("ok-1", "Folk"))
	if res.Imported != 1 || res.Rejected != 2 {
		t.Fatalf("imported %d rejected %d", res.Imported, res.Rejected)
	}
	if !strings.Contains(res.Reports[0].Rejected, "cannot be a track id") {
		t.Fatalf("%q", res.Reports[0].Rejected)
	}
}

func TestMapMusic_DuplicateIdsAbortTheWholeImport(t *testing.T) {
	_, err := MapMusic([]Entry{songEntry("a", "Folk"), songEntry("a", "Jazz")}, nil, Options{Project: "p"})
	if err == nil || !strings.Contains(err.Error(), `duplicate entry id "a"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestMapMusic_KeepsLoaderRejections(t *testing.T) {
	res, err := MapMusic([]Entry{songEntry("a", "Folk")}, []Rejection{{ID: "x", Reason: "unresolved placeholder"}}, Options{Project: "p"})
	if err != nil || res.Rejected != 1 || res.Imported != 1 || res.Reports[0].ID != "x" {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestMapMusic_TheGeneratedProjectLoadsAndPassesTheRealRules(t *testing.T) {
	res := mapSongs(t,
		songEntry("balada", "Spanish pop ballad\nLanguage: es\nBPM: 90\nLyrics:\n[Verse]\nhola\n\n[Chorus]\nadiós"),
		songEntry("lofi", "Lo-fi\nInstrumental\nDuration: 60"),
	)
	if err := res.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	cfg, err := config.LoadBytes(res.EmitYAML())
	if err != nil {
		t.Fatalf("the YAML must load: %v\n%s", err, res.EmitYAML())
	}
	if cfg.Output.Mode != "music" || len(cfg.Tracks) != 2 || len(cfg.Scenes) != 0 {
		t.Fatalf("%+v", cfg)
	}
	b := cfg.Tracks[0]
	if b.ID != "balada" || b.Lyrics != "[Verse]\nhola\n\n[Chorus]\nadiós" || b.VocalLanguage != "es" || *b.BPM != 90 {
		t.Fatalf("round trip: %+v", b)
	}
	if !cfg.Tracks[1].Instrumental || *cfg.Tracks[1].DurationSeconds != 60 {
		t.Fatalf("round trip: %+v", cfg.Tracks[1])
	}
}

func TestMapMusic_ValidationDoesNotNeedTheModelsInstalled(t *testing.T) {
	res := mapSongs(t, songEntry("a", "Folk"))
	if err := res.Validate(); err != nil {
		t.Fatalf("a missing model must not fail the import: %v", err)
	}
}

func TestMapMusic_ReportAndSummary(t *testing.T) {
	res := mapSongs(t, songEntry("a", "Folk\nBPM: 90\nLyrics:\nla"), songEntry("b", "Lo-fi\nInstrumental"), songEntry("c", "Jazz\nBPM: x"))
	rep := res.Report()
	for _, want := range []string{"[a]", "mapped: bpm ← 90", "mapped: lyrics ← 1 lines", "[b]", "mapped: instrumental ← true", "[c]", "rejected: BPM \"x\"", "imported 2, instrumental 1, rejected 1"} {
		if !strings.Contains(rep, want) {
			t.Errorf("report must contain %q:\n%s", want, rep)
		}
	}
}

func TestMapMusic_VideoImportIsUntouched(t *testing.T) {
	res, err := Map([]Entry{{ID: "s", FullPrompt: "A cafe\nDialogue: \"hola\""}}, nil, Options{Project: "p", SilenceSeconds: 2})
	if err != nil || res.Config.Output.Mode == config.ModeMusic || len(res.Config.Scenes) != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if !strings.Contains(res.Report(), "imported 1, silent 0, rejected 0") {
		t.Fatalf("video summary changed: %s", res.Report())
	}
}

func TestMapMusicReadsTagsAndExportWritesThem(t *testing.T) {
	res, err := MapMusic([]Entry{{ID: "s1", FullPrompt: "Warm piano ballad\nLyrics:\n[Verse]\nla la\nTags: Piano, Happy, piano"}}, nil, Options{Project: "p"})
	if err != nil || res.Imported != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if got := res.Config.Tracks[0].Tags; got != "piano,happy" {
		t.Fatalf("tags = %q", got)
	}
	if got := res.Config.Tracks[0].Lyrics; strings.Contains(got, "Tags") {
		t.Fatalf("the Tags line must not end up in the lyrics: %q", got)
	}
}
