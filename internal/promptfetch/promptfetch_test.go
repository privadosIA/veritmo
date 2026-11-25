package promptfetch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	pstore "veritmo/internal/platform/store"
	store "veritmo/internal/prompts"
)

// marker is text that sits in a column no source names: it must never reach any output.
const marker = "LYRICS-MARKER-NEVER"

func bp(b bool) *bool { return &b }

func card(licence ...string) Card {
	var c Card
	b, _ := json.Marshal(licence)
	if len(licence) == 1 {
		b, _ = json.Marshal(licence[0])
	}
	c.CardData.License = b
	c.SHA = "abc123"
	return c
}

// ---- sources ----

func TestDefaultSources(t *testing.T) {
	byID := map[string]Source{}
	for _, s := range DefaultSources() {
		byID[s.ID] = s
	}
	if !byID["diffusiondb"].On() || !byID["rapidata-text2video"].On() {
		t.Fatal("the two public sources are on")
	}
	suno := byID["suno-descriptions"]
	if suno.On() || !strings.Contains(suno.Check, "strictly local use") {
		t.Fatalf("the Suno source is off, with its reason: %+v", suno)
	}
	for _, s := range byID {
		for _, c := range s.ColumnsRead() {
			if c == "prompt_letras" {
				t.Fatalf("%s names the lyrics column", s.ID)
			}
		}
	}
	if err := byID["diffusiondb"].Validate(); err != nil {
		t.Fatal(err)
	}
	if got := byID["diffusiondb"].ColumnsRead(); !reflect.DeepEqual(got, []string{"prompt", "image_name", "prompt_nsfw", "image_nsfw"}) {
		t.Fatalf("%v", got)
	}
}

func TestSourceValidationAndParsing(t *testing.T) {
	good := Source{ID: "x", Repo: "o/n", Files: []string{"a/b.csv"}, Format: "csv", Columns: Columns{Prompt: "p"}, ExpectedLicence: "mit", MaxRows: 5}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mod := range map[string]func(*Source){
		"repo":  func(s *Source) { s.Repo = "../etc" },
		"file":  func(s *Source) { s.Files = []string{"../../x"} },
		"abs":   func(s *Source) { s.Files = []string{"/etc/passwd"} },
		"fmt":   func(s *Source) { s.Format = "xml" },
		"max":   func(s *Source) { s.MaxRows = 0 },
		"lic":   func(s *Source) { s.ExpectedLicence = "" },
		"col":   func(s *Source) { s.Columns.Prompt = "" },
		"files": func(s *Source) { s.Files = nil },
	} {
		s := good
		mod(&s)
		if s.Validate() == nil {
			t.Errorf("%s is accepted", name)
		}
	}
	if _, err := ParseSources([]byte(`{"version":2,"sources":[]}`)); err == nil {
		t.Error("an unknown version is accepted")
	}
	if _, err := ParseSources([]byte(`{"version":1,"sources":[{"id":"a"},{"id":"a"}]}`)); err == nil {
		t.Error("a repeated id is accepted")
	}
}

// ---- the licence gate ----

func TestGate(t *testing.T) {
	src := Source{ExpectedLicence: "cc0-1.0"}
	cases := []struct {
		name string
		card Card
		ok   bool
		want string
	}{
		{"cc0", card("cc0-1.0"), true, ""},
		{"list with the expected one", card("cc0-1.0", "mit"), true, ""},
		{"allowed but not expected", card("apache-2.0"), false, "expects cc0-1.0"},
		{"non commercial", card("cc-by-nc-4.0"), false, "not in the allow-list"},
		{"one of the list not allowed", card("cc0-1.0", "gpl-3.0"), false, "not in the allow-list"},
		{"unknown", Card{}, false, "unknown"},
		{"upper case", card("CC0-1.0"), true, ""},
	}
	for _, c := range cases {
		_, ok, why := Gate(c.card, src)
		if ok != c.ok || (c.want != "" && !strings.Contains(why, c.want)) {
			t.Errorf("%s: %v %q", c.name, ok, why)
		}
	}
	var c Card
	_ = json.Unmarshal([]byte(`{"sha":"s","cardData":{"license":["apache-2.0"]}}`), &c)
	if l := c.Licences(); len(l) != 1 || l[0] != "apache-2.0" || c.SHA != "s" {
		t.Fatalf("%v", l)
	}
	_ = json.Unmarshal([]byte(`{"license":"mit"}`), &c)
	c.CardData.License = nil
	if l := c.Licences(); l[0] != "mit" {
		t.Fatalf("the top-level license is the fallback: %v", l)
	}
}

// ---- readers ----

func TestReadCSV_OnlyNamedColumnsAndQuotes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.csv")
	csv := "\ufeffid,title,prompt_letras,gpt_description_prompt,tags,is_instrumental\n" +
		"1,\"Song, one\",\"" + marker + " line one\nline \"\"two\"\"\",\"A calm, slow\nballad\",\"pop, calm\",True\n" +
		"\n" +
		"2,T2," + marker + " x,Upbeat dance,,False"
	if err := os.WriteFile(path, []byte(csv), 0o644); err != nil {
		t.Fatal(err)
	}
	var rows []Row
	if err := ReadCSV(path, []string{"id", "gpt_description_prompt", "tags", "is_instrumental"}, func(r Row) error { rows = append(rows, r); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0]["gpt_description_prompt"] != "A calm, slow\nballad" || rows[0]["tags"] != "pop, calm" || rows[0]["is_instrumental"] != "True" || rows[1]["id"] != "2" || rows[1]["tags"] != "" {
		t.Fatalf("%q", rows)
	}
	for _, r := range rows {
		if len(r) != 4 {
			t.Fatalf("only the named columns: %v", r)
		}
		for _, v := range r {
			if strings.Contains(fmt.Sprint(v), marker) {
				t.Fatal("the lyrics column was read")
			}
		}
	}
	if err := ReadCSV(path, []string{"nope"}, func(Row) error { return nil }); err == nil {
		t.Fatal("a missing column is an error")
	}
}

func TestReadJSON(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		_ = os.WriteFile(p, []byte(body), 0o644)
		return p
	}
	for name, body := range map[string]string{
		"list.json":  `[{"Prompt":"a","Other":"` + marker + `","n":1},{"Prompt":"b"}]`,
		"obj.json":   `{"data":[{"Prompt":"a","Other":"x","n":1},{"Prompt":"b"}]}`,
		"lines.json": "{\"Prompt\":\"a\",\"Other\":\"x\",\"n\":1}\n{\"Prompt\":\"b\"}\n",
	} {
		var rows []Row
		if err := ReadJSON(write(name, body), []string{"Prompt", "n"}, func(r Row) error { rows = append(rows, r); return nil }); err != nil {
			t.Fatal(name, err)
		}
		if len(rows) != 2 || rows[0]["Prompt"] != "a" || rows[0]["n"] != float64(1) || rows[0]["Other"] != nil || rows[1]["Prompt"] != "b" {
			t.Fatalf("%s: %v", name, rows)
		}
	}
}

// ---- cleaning and mapping ----

func TestMappingHelpers(t *testing.T) {
	if got := TitleOf("A castle, oil painting"); got != "A castle" {
		t.Error(got)
	}
	if got := TitleOf(strings.Repeat("x", 80)); got != strings.Repeat("x", 57)+"…" {
		t.Error(got)
	}
	if got := TitleOf("Hello world!!, more"); got != "Hello world" {
		t.Error(got)
	}
	if got := TagsOf("a cat, (highly detailed:1.3), 4k, trending on artstation, a cat", []string{"Fantasy Art", "x"}); !reflect.DeepEqual(got, []string{"highly detailed", "4k", "trending on artstation", "a cat", "fantasy art"}) {
		t.Errorf("%v", got)
	}
	many := "s" + strings.Repeat(", tag", 20)
	if got := TagsOf(many, nil); len(got) != 1 {
		t.Errorf("duplicates collapse: %v", got)
	}
	if LanguageOf("el gato come con la niña en la casa de los vecinos", "en") != "es" || LanguageOf("a cat on a roof at night", "en") != "en" || LanguageOf("el gato", "en") != "en" {
		t.Error("language")
	}
	if Normalise("Hello,  World!!") != "hello world" {
		t.Error("normalise")
	}
	if camelWords("CamelCaseCategory") != "camel case category" {
		t.Error("camel")
	}
	for prompt, want := range map[string]string{
		"tiny!": "too_short", strings.Repeat("a", 2001): "too_long", "https://example.com/some/long/path.png": "url_only",
		"(123456:1.3) 99999": "weights_only", "a naked statue in a museum": "blocklist", "a castle on a hill": "",
	} {
		if got := Refusal(prompt, Blocklist); got != want {
			t.Errorf("%q: %q, want %q", prompt, got, want)
		}
	}
	if Refusal("a naked statue in a museum", nil) != "" {
		t.Error("a source with scores has no blocklist")
	}
}

func dirtyRows() []diffusionRow {
	return []diffusionRow{
		{ImageName: "a.png", Prompt: "a castle on a hill, oil painting, highly detailed", Lyrics: marker},
		{ImageName: "b.png", Prompt: "A  Castle on a hill, OIL painting; highly detailed!", Lyrics: marker}, // the same once normalised
		{ImageName: "c.png", Prompt: "tiny!"},
		{ImageName: "d.png", Prompt: "https://example.com/x.png"},
		{ImageName: "e.png", Prompt: "a naked statue in a museum, marble", PromptNSFW: 0.9},
		{ImageName: "f.png", Prompt: "  "},
		{ImageName: "g.png", Prompt: "(123456:1.3) 99999"},
		{ImageName: "h.png", Prompt: "a naked statue in a museum, marble light", PromptNSFW: 0.1}, // its own score says fine
		{ImageName: "i.png", Prompt: "a lighthouse in a storm, dramatic sky", PromptNSFW: 0.1, ImageNSFW: 0.7},
		{ImageName: "", Prompt: "a quiet lake at dawn, mist over the water"},
	}
}

func diffusionSource() Source {
	return Source{ID: "ddb", Repo: "owner/ddb", Files: []string{"metadata.parquet"}, Format: "parquet", Kind: "image", Language: "en",
		Columns: Columns{Prompt: "prompt", SourceID: "image_name"}, ExpectedLicence: "cc0-1.0", MaxRows: 100,
		NSFW: []NSFWScore{{Column: "prompt_nsfw", Max: 0.2}, {Column: "image_nsfw", Max: 0.5}}}
}

func TestCleaner_CountsEveryRule(t *testing.T) {
	src := diffusionSource()
	c := NewCleaner(src)
	if err := ReadParquet(writeParquet(t, dirtyRows()), src.ColumnsRead(), func(r Row) error { c.Add(r); return nil }); err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"duplicate": 1, "too_short": 1, "url_only": 1, "nsfw": 2, "empty": 1, "weights_only": 1}
	if !reflect.DeepEqual(c.Counts.Dropped, want) || c.Counts.Read != 10 || c.Counts.Kept != 3 {
		t.Fatalf("%+v", c.Counts)
	}
	first := c.Items[0]
	if first.Title != "a castle on a hill" || first.SourceID != "a.png" || first.Kind != "image" || first.Source != "owner/ddb" || !first.HasScore ||
		!reflect.DeepEqual(first.Tags, []string{"oil painting", "highly detailed"}) {
		t.Fatalf("%+v", first)
	}
	if got := c.Items[2].SourceID; len(got) != 12 {
		t.Fatalf("a row with no id gets a hash of the prompt: %q", got)
	}
}

func TestOwnTagsAndFlags(t *testing.T) {
	src := Source{ID: "m", Repo: "o/m", Kind: "music", Engine: "ace-step", Columns: Columns{Prompt: "d", Tags: []TagColumn{{Column: "tags", Split: ","}, {Column: "Category", Camel: true}}, Flags: []FlagColumn{{Column: "inst", Tag: "instrumental"}}}}
	c := NewCleaner(src)
	c.Add(Row{"d": "A calm slow ballad with piano", "tags": "Pop, Calm", "Category": "SlowMusic", "inst": "True"})
	c.Add(Row{"d": "Another quiet ballad with strings", "inst": "False"})
	if len(c.Items) != 2 || !reflect.DeepEqual(c.Items[0].Tags, []string{"pop", "calm", "slow music", "instrumental"}) || len(c.Items[1].Tags) != 0 || c.Items[0].Engine != "ace-step" || c.Items[0].HasScore {
		t.Fatalf("%+v", c.Items)
	}
}

// ---- sampling ----

func TestSample_IsDeterministicAndMatchesTheScript(t *testing.T) {
	r := &rng{a: 42}
	for i, want := range []float64{0.6011037519201636, 0.44829055899754167, 0.8524657934904099, 0.6697340414393693} {
		if got := r.next(); got != want {
			t.Fatalf("%d: %v, want %v", i, got, want)
		}
	}
	seq := func(n int) []int {
		s := make([]int, n)
		for i := range s {
			s[i] = i
		}
		return s
	}
	if got := Sample(seq(30), 7, 42); !reflect.DeepEqual(got, []int{0, 8, 12, 14, 18, 21, 25}) {
		t.Fatalf("%v", got)
	}
	if got := Sample(seq(1000), 5, 7); !reflect.DeepEqual(got, []int{11, 62, 523, 699, 976}) {
		t.Fatalf("%v", got)
	}
	if got := Sample(seq(3), 10, 1); len(got) != 3 {
		t.Fatal("fewer than the cap are all kept")
	}
	if !reflect.DeepEqual(Sample(seq(500), 20, 9), Sample(seq(500), 20, 9)) || reflect.DeepEqual(Sample(seq(500), 20, 9), Sample(seq(500), 20, 10)) {
		t.Fatal("same seed, same list; another seed, another")
	}
}

// ---- the run ----

type fakeNet struct {
	cards     map[string]Card
	files     map[string]string // repo/file -> a file on disk to copy
	downloads []string
	cardCalls []string
}

func (f *fakeNet) Card(_ context.Context, repo string) (Card, error) {
	f.cardCalls = append(f.cardCalls, repo)
	c, ok := f.cards[repo]
	if !ok {
		return Card{}, fmt.Errorf("no card for %s", repo)
	}
	return c, nil
}

func (f *fakeNet) Download(_ context.Context, repo, file, dest string) error {
	if _, err := os.Stat(dest); err == nil {
		return nil
	}
	f.downloads = append(f.downloads, repo+"/"+file)
	b, err := os.ReadFile(f.files[repo+"/"+file])
	if err != nil {
		return err
	}
	return writeFile(dest, b)
}

func runFixture(t *testing.T) (*fakeNet, []Source, string) {
	t.Helper()
	dir := t.TempDir()
	csvPath := filepath.Join(dir, "suno.csv")
	_ = os.WriteFile(csvPath, []byte("id,prompt_letras,gpt_description_prompt,tags,is_instrumental\n1,"+marker+" la la,A calm slow ballad with piano,\"pop, calm\",False\n"), 0o644)
	jsonPath := filepath.Join(dir, "rapidata.json")
	_ = os.WriteFile(jsonPath, []byte(`[{"Prompt":"A drone flight over a snowy mountain ridge","Category":"NatureScenes","video_name":"v1","Other":"`+marker+`"},{"Prompt":"A drone flight over a snowy mountain ridge!","Category":"NatureScenes","video_name":"v2"}]`), 0o644)
	net := &fakeNet{
		cards: map[string]Card{"owner/ddb": card("cc0-1.0"), "owner/rapidata": card("apache-2.0"), "owner/suno": card("cc-by-4.0"), "owner/nc": card("cc-by-nc-4.0"), "owner/unk": {}},
		files: map[string]string{"owner/ddb/metadata.parquet": writeParquet(t, dirtyRows()), "owner/rapidata/data.json": jsonPath, "owner/suno/metadata.csv": csvPath},
	}
	sources := []Source{
		diffusionSource(),
		{ID: "rapidata", Repo: "owner/rapidata", Files: []string{"data.json"}, Format: "json", Kind: "video", Language: "en", Columns: Columns{Prompt: "Prompt", SourceID: "video_name", Tags: []TagColumn{{Column: "Category", Camel: true}}}, ExpectedLicence: "apache-2.0", MaxRows: 50},
		{ID: "suno", Enabled: bp(false), Repo: "owner/suno", Files: []string{"metadata.csv"}, Format: "csv", Kind: "music", Engine: "ace-step", Columns: Columns{Prompt: "gpt_description_prompt", SourceID: "id", Tags: []TagColumn{{Column: "tags", Split: ","}}, Flags: []FlagColumn{{Column: "is_instrumental", Tag: "instrumental"}}}, ExpectedLicence: "cc-by-4.0", MaxRows: 50, Check: "off: strictly local use"},
		{ID: "nc", Repo: "owner/nc", Files: []string{"x.json"}, Format: "json", Kind: "image", Columns: Columns{Prompt: "p"}, ExpectedLicence: "cc0-1.0", MaxRows: 5},
		{ID: "unk", Repo: "owner/unk", Files: []string{"x.json"}, Format: "json", Kind: "image", Columns: Columns{Prompt: "p"}, ExpectedLicence: "cc0-1.0", MaxRows: 5},
	}
	return net, sources, filepath.Join(dir, "prompts")
}

// newPresets is a store of presets on a database in memory.
func newPresets(t *testing.T) store.Store {
	t.Helper()
	reg := pstore.NewRegistry()
	store.RegisterStore(reg, t.TempDir())
	st, err := pstore.OpenMemory(reg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	return store.NewDB(st)
}

// kept is every preset of a store, as JSON, to look for a text in what reached it.
func kept(t *testing.T, presets store.Store) (all []*store.Preset, text string) {
	t.Helper()
	all, _, err := presets.List(store.Filter{}, pstore.ListParams{})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(all)
	return all, string(b)
}

func allText(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && !strings.Contains(p, store.CacheDir+string(filepath.Separator)+"owner") {
			x, _ := os.ReadFile(p)
			b.Write(x)
		}
		return nil
	})
	return b.String()
}

func TestRun_WritesTheStoreRefusesBadLicencesAndNeverReadsTheLyrics(t *testing.T) {
	net, sources, dir := runFixture(t)
	presets := newPresets(t)
	opts := Options{Sources: sources, Dir: dir, Presets: presets, Seed: 42, Net: net, Now: func() time.Time { return time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC) }}
	res, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if res.Written != 4 || res.Already != 0 || len(res.Used) != 2 || len(res.Refused) != 2 || len(res.Off) != 1 {
		t.Fatalf("%+v %d", res, res.Written)
	}
	if res.Refused[0].Source.ID != "nc" || res.Refused[0].Licences[0] != "cc-by-nc-4.0" || res.Refused[1].Licences[0] != "unknown" {
		t.Fatalf("%+v", res.Refused)
	}
	for _, d := range net.downloads {
		if strings.Contains(d, "owner/suno") || strings.Contains(d, "owner/nc") || strings.Contains(d, "owner/unk") {
			t.Fatalf("a source that is off or refused was downloaded: %v", net.downloads)
		}
	}
	for _, c := range net.cardCalls {
		if c == "owner/suno" {
			t.Fatal("a source that is off is not even asked about")
		}
	}
	// what is in the store
	got, storeText := kept(t, presets)
	if len(got) != 4 {
		t.Fatalf("%d presets", len(got))
	}
	for _, p := range got {
		if p.Own || p.Licence == "" || p.Source == "" || p.CreatedAt == "" || !store.ValidKind(p.Kind) {
			t.Fatalf("%+v", p)
		}
	}
	if text := allText(t, dir) + storeText; strings.Contains(text, marker) {
		t.Fatal("a column no source names reached the store or the report")
	}
	for _, want := range []string{"cc0-1.0", "abc123", "Rows read: 10", "Dropped (nsfw): 2", "Dropped (duplicate): 1", "cc-by-nc-4.0", "unknown", "suno: off: strictly local use"} {
		if !strings.Contains(res.Report, want) {
			t.Errorf("the report lacks %q:\n%s", want, res.Report)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, store.CacheDir, "report.md")); err != nil {
		t.Error("the report is saved next to the cache")
	}

	// a second run adds nothing and downloads nothing again
	before := len(net.downloads)
	res2, err := Run(context.Background(), opts)
	if err != nil || res2.Written != 0 || res2.Already != 4 || len(net.downloads) != before {
		t.Fatalf("%v %+v %d", err, res2, len(net.downloads)-before)
	}
}

func TestRun_BundleDryRunAndOnly(t *testing.T) {
	net, sources, dir := runFixture(t)
	out := filepath.Join(t.TempDir(), "bundle.json")
	res, err := Run(context.Background(), Options{Sources: sources, Dir: dir, Seed: 42, Out: out, Net: net})
	if err != nil {
		t.Fatal(err)
	}
	var b Bundle
	raw, _ := os.ReadFile(out)
	if json.Unmarshal(raw, &b) != nil || b.Format != "prompt-presets" || b.Version != 1 || len(b.Items) != 4 || len(b.Meta.Sources) != 2 || b.Meta.Seed != 42 {
		t.Fatalf("%s", raw)
	}
	if strings.Contains(string(raw), marker) {
		t.Fatal("the lyrics are in the bundle")
	}
	if b.Items[0].Kind != "image" || b.Items[len(b.Items)-1].Kind != "video" || b.Items[0].NSFW == nil || b.Items[len(b.Items)-1].NSFW != nil {
		t.Fatalf("sorted by kind; nsfw only where the source scores: %+v", b.Items)
	}
	if _, err := os.Stat(filepath.Join(dir, "image")); err == nil {
		t.Error("--out writes no store")
	}
	if res.Written != 0 {
		t.Error("nothing was written to a store")
	}

	net2, sources2, dir2 := runFixture(t)
	if _, err := Run(context.Background(), Options{Sources: sources2, Dir: dir2, Seed: 1, DryRun: true, Net: net2}); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(dir2); len(entries) > 1 {
		t.Errorf("a dry run writes no presets and no report: %v", entries)
	}

	// --source names one; a source that is off runs when named
	net3, sources3, dir3 := runFixture(t)
	presets3 := newPresets(t)
	res3, err := Run(context.Background(), Options{Sources: sources3, Dir: dir3, Presets: presets3, Seed: 42, Only: "suno", Net: net3})
	if err != nil || len(res3.Used) != 1 || res3.Written != 1 {
		t.Fatalf("%v %+v", err, res3)
	}
	got3, text3 := kept(t, presets3)
	p := got3[0]
	if p.Engine != "ace-step" || !reflect.DeepEqual(p.Tags, []string{"pop", "calm"}) || strings.Contains(allText(t, dir3)+text3, marker) {
		t.Fatalf("%+v", p)
	}
	if _, err := Run(context.Background(), Options{Sources: sources3, Dir: dir3, Only: "nope", Net: net3}); err == nil || !strings.Contains(err.Error(), "ddb") {
		t.Fatalf("an unknown source names the ones there are: %v", err)
	}
}

func TestRun_MaxAndSeed(t *testing.T) {
	net, sources, dir := runFixture(t)
	a, _ := Run(context.Background(), Options{Sources: sources, Dir: dir, Seed: 3, Max: 1, DryRun: true, Net: net})
	b, _ := Run(context.Background(), Options{Sources: sources, Dir: dir, Seed: 3, Max: 1, DryRun: true, Net: net})
	if len(a.Bundle.Items) != 2 || !reflect.DeepEqual(a.Bundle.Items, b.Bundle.Items) {
		t.Fatalf("%d items; the same seed chooses the same", len(a.Bundle.Items))
	}
}

func TestRun_ACardThatCannotBeReadStopsTheRun(t *testing.T) {
	net, sources, dir := runFixture(t)
	net.cards = map[string]Card{}
	if _, err := Run(context.Background(), Options{Sources: sources, Dir: dir, Presets: newPresets(t), Net: net}); err == nil {
		t.Fatal("no card, no run")
	}
}
