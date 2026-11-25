package promptfetch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	store "veritmo/internal/prompts"
)

// Version is the version of the rules; it goes in the bundle's meta.
const Version = 1

// Options are what one run is told.
type Options struct {
	Sources []Source
	// Only names the one source to run (a source that is off runs when named); empty runs every enabled source.
	Only string
	// Max caps each source (0: the source's own max_rows). Seed is the sampler's seed.
	Max  int
	Seed int
	// Dir is the folder the downloads and the report go under (Dir/.cache): <media dir>/prompts, where the presets were kept as files.
	Dir string
	// Presets is where the presets are written (the database the server uses). It is needed unless Out or DryRun is set.
	Presets store.Store
	// Out, when set, is where the bundle is written (as a file) instead of the store. DryRun writes neither.
	Out    string
	DryRun bool
	Net    Net
	Now    func() time.Time
	Log    func(string)
}

// Used is a source that was read.
type Used struct {
	Source   Source
	SHA      string
	Licences []string
	Counts   Counts
	Items    []Item
	// Written and Already are what the store did with them (not set with Out or DryRun).
	Written, Already, Refused int
}

// Refused is a source whose licence the gate did not accept.
type Refused struct {
	Source   Source
	Licences []string
	Reason   string
}

// Result is what a run did.
type Result struct {
	Date     string
	Seed     int
	Used     []Used
	Refused  []Refused
	Off      []Source
	Bundle   *Bundle
	Report   string
	Written  int
	Already  int
	StoreDir string
}

// BundleItem is a preset in a bundle.
type BundleItem struct {
	Kind     string   `json:"kind"`
	Title    string   `json:"title"`
	Prompt   string   `json:"prompt"`
	Tags     []string `json:"tags"`
	Engine   string   `json:"engine,omitempty"`
	Language string   `json:"language"`
	Source   string   `json:"source"`
	SourceID string   `json:"source_id"`
	Licence  string   `json:"licence"`
	NSFW     *bool    `json:"nsfw,omitempty"`
}

// Bundle is the file `POST /api/prompts/import` takes.
type Bundle struct {
	Format  string       `json:"format"`
	Version int          `json:"version"`
	Meta    BundleMeta   `json:"meta"`
	Items   []BundleItem `json:"items"`
}

// BundleMeta says how a bundle was made.
type BundleMeta struct {
	Version int          `json:"fetch_version"`
	Date    string       `json:"date"`
	Seed    int          `json:"seed"`
	Sources []MetaSource `json:"sources"`
}

// MetaSource is a source of a bundle.
type MetaSource struct {
	ID      string `json:"id"`
	Repo    string `json:"repo"`
	SHA     string `json:"sha"`
	Licence string `json:"licence"`
	Items   int    `json:"items"`
}

func (o Options) log(format string, a ...any) {
	if o.Log != nil {
		o.Log(fmt.Sprintf(format, a...))
	}
}

// CacheDir is where the downloads and the report are kept.
func CacheDir(dir string) string { return filepath.Join(dir, store.CacheDir) }

// Run reads the sources, applies the licence gate, cleans and samples, and writes the presets (or the bundle) and the report.
func Run(ctx context.Context, o Options) (*Result, error) {
	now := o.Now
	if now == nil {
		now = time.Now
	}
	res := &Result{Date: now().UTC().Format(time.RFC3339), Seed: o.Seed, StoreDir: o.Dir}
	var chosen []Source
	for _, s := range o.Sources {
		if o.Only != "" && s.ID != o.Only {
			continue
		}
		chosen = append(chosen, s)
	}
	if o.Only != "" && len(chosen) == 0 {
		var ids []string
		for _, s := range o.Sources {
			ids = append(ids, s.ID)
		}
		return nil, fmt.Errorf("no source called %s: %s", o.Only, strings.Join(ids, ", "))
	}
	for _, s := range chosen {
		if !s.On() && o.Only == "" {
			res.Off = append(res.Off, s)
			continue
		}
		if err := s.Validate(); err != nil {
			return nil, err
		}
		if !s.On() {
			o.log("%s is off in the sources file: %s", s.ID, orDefault(s.Check, "no note"))
		}
		card, err := o.Net.Card(ctx, s.Repo)
		if err != nil {
			return nil, err
		}
		licences, ok, why := Gate(card, s)
		if !ok {
			res.Refused = append(res.Refused, Refused{Source: s, Licences: licences, Reason: why})
			o.log("refused %s: %s", s.ID, why)
			continue
		}
		cleaner := NewCleaner(s)
		for _, file := range s.Files {
			dest := filepath.Join(CacheDir(o.Dir), filepath.FromSlash(s.Repo), filepath.FromSlash(file))
			if err := o.Net.Download(ctx, s.Repo, file, dest); err != nil {
				return nil, err
			}
			if err := ReadFile(s.Format, dest, s.ColumnsRead(), func(r Row) error {
				if err := ctx.Err(); err != nil {
					return err
				}
				cleaner.Add(r)
				return nil
			}); err != nil {
				return nil, err
			}
		}
		capRows := s.MaxRows
		if o.Max > 0 {
			capRows = o.Max
		}
		u := Used{Source: s, SHA: card.SHA, Licences: licences, Counts: cleaner.Counts, Items: Sample(cleaner.Items, capRows, o.Seed)}
		u.Source.MaxRows = capRows
		res.Used = append(res.Used, u)
		o.log("%s: %d read, %d kept, %d chosen", s.ID, u.Counts.Read, u.Counts.Kept, len(u.Items))
	}
	res.Bundle = bundleOf(res)
	if !o.DryRun {
		switch {
		case o.Out != "":
			b, err := json.Marshal(res.Bundle)
			if err != nil {
				return nil, err
			}
			if err := writeFile(o.Out, append(b, '\n')); err != nil {
				return nil, err
			}
		default:
			if o.Presets == nil {
				return nil, errors.New("promptfetch: no store of presets to write to")
			}
			if err := writeStore(o.Presets, res); err != nil {
				return nil, err
			}
		}
	}
	res.Report = reportOf(res, o)
	if !o.DryRun {
		if err := writeFile(filepath.Join(CacheDir(o.Dir), "report.md"), []byte(res.Report)); err != nil {
			return nil, err
		}
	}
	return res, nil
}

func bundleOf(r *Result) *Bundle {
	b := &Bundle{Format: "prompt-presets", Version: 1, Items: []BundleItem{}, Meta: BundleMeta{Version: Version, Date: r.Date, Seed: r.Seed}}
	for _, u := range r.Used {
		lic := strings.Join(u.Licences, ", ")
		for _, it := range u.Items {
			bi := BundleItem{Kind: it.Kind, Title: it.Title, Prompt: it.Prompt, Tags: it.Tags, Engine: it.Engine, Language: it.Language, Source: it.Source, SourceID: it.SourceID, Licence: lic}
			if it.HasScore {
				f := false
				bi.NSFW = &f
			}
			b.Items = append(b.Items, bi)
		}
		b.Meta.Sources = append(b.Meta.Sources, MetaSource{ID: u.Source.ID, Repo: u.Source.Repo, SHA: u.SHA, Licence: lic, Items: len(u.Items)})
	}
	sort.SliceStable(b.Items, func(i, j int) bool {
		x, y := b.Items[i], b.Items[j]
		if x.Kind != y.Kind {
			return x.Kind < y.Kind
		}
		if x.Source != y.Source {
			return x.Source < y.Source
		}
		return x.SourceID < y.SourceID
	})
	return b
}

// writeStore writes the presets as bundled ones into the store, skipping what is already there, as one change.
func writeStore(presets store.Store, r *Result) error {
	have, err := presets.Keys()
	if err != nil {
		return err
	}
	created := time.Now().UTC().Format(time.RFC3339)
	var fresh []*store.Preset
	owner := map[*store.Preset]*Used{}
	for ui := range r.Used {
		u := &r.Used[ui]
		lic := strings.Join(u.Licences, ", ")
		for _, it := range u.Items {
			p := &store.Preset{Kind: it.Kind, Title: it.Title, Prompt: it.Prompt, Tags: it.Tags, Engine: it.Engine, Language: it.Language,
				Source: it.Source, SourceID: it.SourceID, Licence: lic}
			if k := store.Key(p); k != "" {
				if _, dup := have[k]; dup {
					u.Already++
					r.Already++
					continue
				}
			}
			if why := store.Clean(p); why != "" {
				u.Refused++
				continue
			}
			p.Own, p.CreatedAt = false, created
			if k := store.Key(p); k != "" {
				have[k] = ""
			}
			fresh = append(fresh, p)
			owner[p] = u
		}
	}
	ids, err := presets.FreshIDs(len(fresh))
	if err != nil {
		return err
	}
	for i, p := range fresh {
		p.ID = ids[i]
	}
	if err := presets.Save(fresh); err != nil {
		return err
	}
	for _, p := range fresh {
		owner[p].Written++
		r.Written++
	}
	return nil
}

func reportOf(r *Result, o Options) string {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }
	w("# Prompt fetch report\n")
	w("- Date: %s", r.Date)
	w("- Sample seed: %d", r.Seed)
	w("- Rules version: %d", Version)
	switch {
	case o.DryRun:
		w("- Output: none (dry run)")
	case o.Out != "":
		w("- Bundle: `%s`", o.Out)
	default:
		w("- Store: `%s`", o.Dir)
	}
	w("- Allow-list of licences: %s\n", strings.Join(AllowedLicences, ", "))
	w("## Sources used\n")
	if len(r.Used) == 0 {
		w("None.\n")
	}
	for _, u := range r.Used {
		w("### %s (%s)\n", u.Source.ID, u.Source.Repo)
		w("- Licence read from the card: %s", strings.Join(u.Licences, ", "))
		w("- Commit of the dataset: %s", orDefault(u.SHA, "unknown"))
		w("- Rows read: %d", u.Counts.Read)
		for _, rule := range Rules {
			if n := u.Counts.Dropped[rule]; n > 0 {
				w("- Dropped (%s): %d", strings.ReplaceAll(rule, "_", " "), n)
			}
		}
		w("- Kept after cleaning: %d", u.Counts.Kept)
		w("- Chosen (a sample of at most %d): %d", u.Source.MaxRows, len(u.Items))
		if o.Out == "" && !o.DryRun {
			w("- Written to the store: %d (already there: %d, refused by the store: %d)", u.Written, u.Already, u.Refused)
		}
		w("")
	}
	w("## Sources refused\n")
	if len(r.Refused) == 0 {
		w("None.\n")
	}
	for _, x := range r.Refused {
		w("- %s (%s): licence read %s. %s", x.Source.ID, x.Source.Repo, strings.Join(x.Licences, ", "), x.Reason)
	}
	if len(r.Refused) > 0 {
		w("")
	}
	w("## Sources off\n")
	if len(r.Off) == 0 {
		w("None.")
	}
	for _, s := range r.Off {
		w("- %s: %s", s.ID, orDefault(s.Check, "off in the sources file"))
	}
	return b.String()
}
