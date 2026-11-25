package importer

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"veritmo/internal/assets3d/generate"
	"veritmo/internal/project/config"
)

// models3dMarkerRe matches the marker lines of a 3D model entry (case-insensitive).
var models3dMarkerRe = regexp.MustCompile(`(?i)^(engine|detail|variants|seed|image|adult)\s*:\s*(.*)$`)

// maxModelIDRunes is the longest id an imported entry keeps (a longer one is cut, with a warning).
const maxModelIDRunes = 32

var nonIDRe = regexp.MustCompile(`[^a-z0-9]+`)

// MapModels3D converts dataset entries into a project of the mode models3d: one entry of `models` per accepted tuple. The lines of an entry's text
// before the first marker are the prompt (joined with spaces); `Engine:`, `Detail:`, `Variants:`, `Seed:`, `Image:` (a file of the project's sources) and
// `Adult:` set one field each. A line that is none of those after the first marker is ignored with a warning. The id is made plain (lower case, digits,
// dashes, up to 32 characters) and unique with a number when it is not, saying so; an empty id is made from the title. The title and lora_info are
// otherwise ignored. A bad entry is rejected with its reason and the others import.
func MapModels3D(entries []Entry, loaderRejected []Rejection, opts Options) (*Result, error) {
	res := &Result{Config: &config.Config{Project: opts.Project, Output: config.Output{Mode: config.ModeModels3D}}}
	for _, r := range loaderRejected {
		res.Reports = append(res.Reports, EntryReport{ID: r.ID, Rejected: r.Reason})
		res.Rejected++
	}
	taken := map[string]bool{}
	for _, e := range entries {
		model, report := mapModels3DEntry(e, taken)
		if report.Rejected != "" {
			res.Reports = append(res.Reports, report)
			res.Rejected++
			continue
		}
		taken[model.ID] = true
		res.Config.Models = append(res.Config.Models, model)
		res.Reports = append(res.Reports, report)
		res.Imported++
	}
	return res, nil
}

// plainModelID makes an id for an entry from a text: the text itself when it already is one, else its words in lower case joined with dashes.
func plainModelID(s string) string {
	if config.ValidModelID(s) && len([]rune(s)) <= maxModelIDRunes {
		return s
	}
	id := strings.Trim(nonIDRe.ReplaceAllString(strings.ToLower(asciiFold(s)), "-"), "-")
	if r := []rune(id); len(r) > maxModelIDRunes {
		id = strings.TrimRight(string(r[:maxModelIDRunes]), "-")
	}
	return id
}

// asciiFold drops the accents of the letters it knows, so an id made of Spanish words keeps its letters.
func asciiFold(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case strings.ContainsRune("áàäâ", r):
			b.WriteByte('a')
		case strings.ContainsRune("éèëê", r):
			b.WriteByte('e')
		case strings.ContainsRune("íìïî", r):
			b.WriteByte('i')
		case strings.ContainsRune("óòöô", r):
			b.WriteByte('o')
		case strings.ContainsRune("úùüû", r):
			b.WriteByte('u')
		case r == 'ñ':
			b.WriteByte('n')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// mapModels3DEntry reads one entry. A rejected entry comes back with only its report filled in.
func mapModels3DEntry(e Entry, taken map[string]bool) (config.ModelEntry, EntryReport) {
	report := EntryReport{ID: e.ID}
	reject := func(format string, args ...any) (config.ModelEntry, EntryReport) {
		report.Rejected = fmt.Sprintf(format, args...)
		return config.ModelEntry{}, report
	}
	note := func(field, v string) { report.Mapped = append(report.Mapped, fmt.Sprintf("%s ← %s", field, v)) }

	m := config.ModelEntry{}
	var prompt []string
	seenMarker := false
	given := map[string]bool{}
	for _, line := range strings.Split(strings.ReplaceAll(e.FullPrompt, "\r\n", "\n"), "\n") {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		mk := models3dMarkerRe.FindStringSubmatch(t)
		if mk == nil {
			if seenMarker {
				report.Discarded = append(report.Discarded, fmt.Sprintf("line %q is after the settings and is none of them: ignored", shortLine(t)))
			} else {
				prompt = append(prompt, t)
			}
			continue
		}
		seenMarker = true
		name, val := strings.ToLower(mk[1]), strings.TrimSpace(mk[2])
		if given[name] {
			report.Discarded = append(report.Discarded, fmt.Sprintf("%s was given twice: the last one counts", mk[1]))
		}
		given[name] = true
		switch name {
		case "engine":
			v := strings.ToLower(val)
			if _, ok := generate.Info(v); !ok {
				return reject("line %q: Engine must be one of %s", t, strings.Join(generate.Engines(), ", "))
			}
			m.Engine = v
			note("engine", v)
		case "detail":
			v := strings.ToLower(val)
			if !generate.ValidDetail(v) {
				return reject("line %q: Detail must be low, normal or high", t)
			}
			m.Detail = v
			note("detail", v)
		case "variants":
			n, err := strconv.Atoi(val)
			if err != nil || n < 1 || n > config.MaxModelVariants {
				return reject("line %q: Variants must be a whole number from 1 to %d", t, config.MaxModelVariants)
			}
			m.Variants = &n
			note("variants", val)
		case "seed":
			n, err := strconv.Atoi(val)
			if err != nil || n < 0 {
				return reject("line %q: Seed must be a whole number, 0 or more", t)
			}
			m.Seed = &n
			note("seed", val)
		case "image":
			if !config.ValidImageName(val) {
				return reject("line %q: Image must be the name of a .png, .jpg, .jpeg or .webp file of the project's sources", t)
			}
			m.Image = val
			note("image", val)
			report.Discarded = append(report.Discarded, fmt.Sprintf("Image %s has to be in the project's sources: a dataset cannot carry it (the confirmation refuses the project without it)", val))
		case "adult":
			switch strings.ToLower(val) {
			case "true", "yes":
				m.Adult = true
			case "false", "no":
			default:
				return reject("line %q: Adult must be true or false", t)
			}
			note("adult", strings.ToLower(val))
		}
	}
	m.Prompt = strings.Join(strings.Fields(strings.Join(prompt, " ")), " ")
	switch {
	case m.Prompt == "" && m.Image == "":
		return reject("entry has no prompt (the lines before the first setting) and no Image line")
	case m.Prompt != "" && m.Image != "":
		return reject("entry has a prompt and an Image line: the picture is made from the prompt, or it is the image, not both")
	case len([]rune(m.Prompt)) > generate.MaxPromptRunes:
		return reject("prompt is %d characters, the most is %d", len([]rune(m.Prompt)), generate.MaxPromptRunes)
	}
	if m.Prompt != "" {
		note("prompt", fmt.Sprintf("%d chars", len([]rune(m.Prompt))))
	}

	// the id: the tuple's, made plain, or the title's when the tuple has none; unique with a number
	want := plainModelID(strings.TrimSpace(e.ID))
	if want == "" {
		want = plainModelID(strings.TrimSpace(e.Title))
	}
	if want == "" {
		want = "model"
	}
	id := want
	for n := 2; taken[id]; n++ {
		suffix := fmt.Sprintf("-%d", n)
		base := want
		if r := []rune(base); len(r)+len(suffix) > maxModelIDRunes {
			base = strings.TrimRight(string(r[:maxModelIDRunes-len(suffix)]), "-")
		}
		id = base + suffix
	}
	m.ID = id
	if id != e.ID {
		report.Discarded = append(report.Discarded, fmt.Sprintf("id %q was changed to %q (a plain lower-case name that no other entry has)", e.ID, id))
	}
	if e.LoraInfo != "" {
		report.Discarded = append(report.Discarded, fmt.Sprintf("lora_info %q: a 3D model has no adapter, field ignored", e.LoraInfo))
	}
	return m, report
}

// shortLine cuts a long line for a message.
func shortLine(s string) string {
	if r := []rune(s); len(r) > 60 {
		return string(r[:60]) + "…"
	}
	return s
}

// validateModels3D checks the generated project with the real rules of the mode. The pictures the entries name are stubbed in a temporary sources folder
// (the import has no project yet): a failure here always means the entries are wrong, never that a picture is still to be uploaded.
func (r *Result) validateModels3D() error {
	if len(r.Config.Models) == 0 {
		return nil // nothing imported: nothing to validate (the caller reports it)
	}
	dir, err := os.MkdirTemp("", "vt-import-models3d")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	_ = os.MkdirAll(filepath.Join(dir, config.SourcesSubdir), 0o755)
	for _, e := range r.Config.Models {
		if e.Image != "" {
			_ = os.WriteFile(filepath.Join(dir, config.SourcesSubdir, e.Image), nil, 0o644)
		}
	}
	check := *r.Config
	return check.ValidateInDir(dir)
}

// emitModels3DYAML renders the generated scenes.yaml of a models3d project: the entries the dataset supplied, and commented placeholders for the image
// settings the pictures of the prompts are drawn with.
func (r *Result) emitModels3DYAML() []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "# Generated by `veritmo import --mode models3d`. Check the models below and set the\n")
	fmt.Fprintf(&b, "# image model (commented) that draws the picture of each prompt before running it.\n\n")
	fmt.Fprintf(&b, "project: %s\n", yamlScalar(r.Config.Project))
	r.writeAdult(&b)
	fmt.Fprintf(&b, "\n")
	fmt.Fprintf(&b, "output:\n  mode: models3d\n\n")
	fmt.Fprintf(&b, "defaults:\n")
	fmt.Fprintf(&b, "  # model: models/<checkpoint>.safetensors\n")
	fmt.Fprintf(&b, "  # resolution: 768x768\n")
	fmt.Fprintf(&b, "  # sampler: euler\n")
	fmt.Fprintf(&b, "  # steps: 20\n")
	fmt.Fprintf(&b, "  # models3d: {engine: triposr, detail: normal}\n\n")
	var doc strings.Builder
	enc := yaml.NewEncoder(&doc)
	enc.SetIndent(2)
	if err := enc.Encode(map[string]any{"models": r.Config.Models}); err == nil {
		_ = enc.Close()
		b.WriteString(doc.String())
	}
	return []byte(b.String())
}

// exportModels writes the entries of a models3d project as tuples. The engine and the detail are folded in (the dataset stands on its own) and written only
// when they are not the built-in ones, so the file is short and importing it makes the same entries.
func exportModels(cfg *config.Config, d *Dataset) [][]string {
	var out [][]string
	for _, e := range cfg.Models {
		var lines []string
		if p := oneLine(e.Prompt); p != "" {
			lines = append(lines, p)
		}
		if engine := cfg.EntryEngine(e); engine != generate.EngineTripoSR {
			lines = append(lines, "Engine: "+engine)
		}
		if detail := cfg.EntryDetail(e); detail != generate.DetailNormal {
			lines = append(lines, "Detail: "+detail)
		}
		if e.Variants != nil && *e.Variants != 1 {
			lines = append(lines, fmt.Sprintf("Variants: %d", *e.Variants))
		}
		if e.Seed != nil {
			lines = append(lines, fmt.Sprintf("Seed: %d", *e.Seed))
		}
		if e.Image != "" {
			lines = append(lines, "Image: "+e.Image)
			d.Notes = append(d.Notes, fmt.Sprintf("model %q uses its own picture %s from sources/, which a dataset cannot carry: copy it with the project package", e.ID, e.Image))
		}
		if e.Adult {
			lines = append(lines, "Adult: true")
		}
		out = append(out, []string{e.ID, e.ID, "", strings.Join(lines, "\n")})
	}
	d.Notes = append(d.Notes, "the engine and the detail of the project's defaults are folded into each entry; the image model that draws the pictures (and its size, sampler and steps), the library and the models themselves are not carried")
	return out
}
