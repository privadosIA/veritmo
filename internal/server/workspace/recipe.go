package workspace

// recipe.go builds the "recipe" of a project: everything it uses (models,
// voice, LoRAs, animation engine) with the settings it runs with, the
// recommended settings (catalog preset) of its models and whether the project
// follows them, and where each file can be downloaded. It goes into the
// project package next to scenes.yaml, and the importing side reads it to say
// what is installed and what is missing (see specs/project-management).

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"veritmo/internal/server/modelcat"

	"gopkg.in/yaml.v3"

	"veritmo/internal/assets3d/generate"
	"veritmo/internal/project/config"
)

// recipeFormat is the version of recipe.json.
const recipeFormat = 1

// recipeFile is one file a project needs.
type recipeFile struct {
	// Key is the place in scenes.yaml (e.g. defaults.animate.lora) and Label says it in words.
	Key   string `json:"key"`
	Label string `json:"label"`
	// Path is the project-relative path as scenes.yaml writes it.
	Path string `json:"path"`
	Name string `json:"name"`
	// SizeBytes is the file's size (the catalog's when the catalog knows it, else the disk's).
	SizeBytes int64 `json:"size_bytes,omitempty"`
	// CatalogID, CatalogName and URL say where the file comes from when the catalog has it.
	CatalogID     string `json:"catalog_id,omitempty"`
	CatalogName   string `json:"catalog_name,omitempty"`
	URL           string `json:"url,omitempty"`
	RequiresToken string `json:"requires_token,omitempty"`
	// Installed is whether the file exists on the machine that built the recipe or checks it.
	Installed bool `json:"installed"`
	// Note says where a file that is not in the catalog comes from (the weights of a 3D engine are fetched by their own route).
	Note string `json:"note,omitempty"`
}

// recipePreset compares the project's settings with a catalog preset.
type recipePreset struct {
	For         string           `json:"for"`
	CatalogID   string           `json:"catalog_id"`
	CatalogName string           `json:"catalog_name"`
	Recommended *modelcat.Preset `json:"recommended"`
	Used        recipeUsed       `json:"used"`
	Matches     bool             `json:"matches"`
	Differences []string         `json:"differences,omitempty"`
}

type recipeUsed struct {
	Steps      int      `json:"steps,omitempty"`
	CfgScale   *float64 `json:"cfg_scale,omitempty"`
	Resolution string   `json:"resolution,omitempty"`
}

// recipeSettings is what the project runs with (defaults resolved).
type recipeSettings struct {
	Resolution string          `json:"resolution,omitempty"`
	Sampler    string          `json:"sampler,omitempty"`
	Steps      int             `json:"steps,omitempty"`
	CfgScale   *float64        `json:"cfg_scale,omitempty"`
	FPS        int             `json:"fps,omitempty"`
	Animate    *recipeAnimate  `json:"animate,omitempty"`
	Music      *recipeMusicSet `json:"music,omitempty"`
	// Models3D is what a models3d project makes its models with: the engines of its entries.
	Models3D *recipeModels3D `json:"models3d,omitempty"`
}

// recipeModels3D is the 3D engines a models3d project uses, with the licence each one's models come under.
type recipeModels3D struct {
	Engines []recipeEngine `json:"engines"`
}

type recipeEngine struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Licence string `json:"licence"`
	// Gated says the weights come from a repository whose licence the person's own Hugging Face account has to accept.
	Gated bool `json:"gated,omitempty"`
}

// models3DRecipe is the engines the entries of a models3d project use, in the order they first appear, and the weights each one reads.
func models3DRecipe(cfg *config.Config) (*recipeModels3D, []recipeFile) {
	if cfg.Output.Mode != config.ModeModels3D {
		return nil, nil
	}
	out := &recipeModels3D{}
	var files []recipeFile
	seen := map[string]bool{}
	for _, e := range cfg.Models {
		id := cfg.EntryEngine(e)
		info, ok := generate.Info(id)
		if !ok || seen[id] {
			continue
		}
		seen[id] = true
		out.Engines = append(out.Engines, recipeEngine{ID: id, Name: info.Name, Licence: info.Licence, Gated: info.Gated})
		for _, rel := range generate.Required(id) {
			p := path.Join(generate.WeightsDir, rel)
			files = append(files, recipeFile{Key: "models3d.engine." + id, Label: "3D · " + info.Name, Path: p, Name: path.Base(p),
				Note: "POST /api/models3d/engines/" + id + "/install"})
		}
	}
	return out, files
}

type recipeAnimate struct {
	Engine     string   `json:"engine"`
	Resolution string   `json:"resolution"`
	Frames     int      `json:"frames"`
	FPS        int      `json:"fps"`
	Steps      int      `json:"steps"`
	CfgScale   *float64 `json:"cfg_scale,omitempty"`
	Turbo      bool     `json:"turbo_lora,omitempty"`
}

type recipeMusicSet struct {
	InferenceSteps int    `json:"inference_steps,omitempty"`
	OutputFormat   string `json:"output_format,omitempty"`
}

// recipe is recipe.json.
type recipe struct {
	Format     int               `json:"format"`
	Project    string            `json:"project"`
	Mode       config.OutputMode `json:"mode"`
	ExportedAt string            `json:"exported_at"`
	Scenes     int               `json:"scenes,omitempty"`
	Tracks     int               `json:"tracks,omitempty"`
	Models     int               `json:"models,omitempty"`
	Settings   recipeSettings    `json:"settings"`
	Presets    []recipePreset    `json:"presets,omitempty"`
	Files      []recipeFile      `json:"files"`
}

// fileKeys are the scenes.yaml keys whose value is a file of the machine, with the label of each
// (Spanish, English). A key not listed is ignored.
var fileKeys = map[string][2]string{
	"model":                      {"Modelo de imagen", "Image model"},
	"diffusion_model":            {"Modelo de difusión", "Diffusion model"},
	"high_noise_diffusion_model": {"Modelo de ruido alto (A14B)", "High-noise model (A14B)"},
	"llm":                        {"Codificador de texto (LLM)", "Text encoder (LLM)"},
	"t5xxl":                      {"Codificador de texto (umt5)", "Text encoder (umt5)"},
	"vae":                        {"VAE", "VAE"},
	"audio_vae":                  {"VAE de audio", "Audio VAE"},
	"connectors":                 {"Conectores de embeddings (LTX)", "Embeddings connectors (LTX)"},
	"voice":                      {"Voz", "Voice"},
	"checkpoint":                 {"Modelo SD 1.5 (AnimateDiff)", "SD 1.5 model (AnimateDiff)"},
	"motion_module":              {"Módulo de movimiento (AnimateDiff)", "Motion module (AnimateDiff)"},
	"lora":                       {"LoRA", "LoRA"},
	"lm_model":                   {"Modelo de lenguaje (música)", "Language model (music)"},
	"synth_model":                {"Modelo de síntesis (música)", "Synthesis model (music)"},
}

// collectRecipeFiles walks scenes.yaml and returns every file it names, in the order of the file, without repeats.
// LoRAs of scenes are looked up in defaults.lora_model_dir; music models in defaults.music.models_dir.
func collectRecipeFiles(yamlData []byte, cfg *config.Config, lang string) []recipeFile {
	var root yaml.Node
	if err := yaml.Unmarshal(yamlData, &root); err != nil || len(root.Content) == 0 {
		return nil
	}
	idx := 0
	if lang == "en" {
		idx = 1
	}
	loraDir := cfg.Defaults.LoraModelDir
	if loraDir == "" {
		loraDir = "models/loras"
	}
	musicDir := ""
	if cfg.Defaults.Music != nil {
		musicDir = cfg.Defaults.Music.ModelsDir
	}
	var out []recipeFile
	seen := map[string]bool{}
	add := func(key, label, p string) {
		p = path.Clean(strings.TrimSpace(p))
		if p == "." || p == "" || seen[p] {
			return
		}
		seen[p] = true
		out = append(out, recipeFile{Key: key, Label: label, Path: p, Name: path.Base(p)})
	}
	var walk func(n *yaml.Node, trail []string)
	walk = func(n *yaml.Node, trail []string) {
		switch n.Kind {
		case yaml.MappingNode:
			for i := 0; i+1 < len(n.Content); i += 2 {
				k, v := n.Content[i].Value, n.Content[i+1]
				if v.Kind == yaml.ScalarNode && v.Value != "" {
					keyPath := strings.Join(append(append([]string{}, trail...), k), ".")
					labels, known := fileKeys[k]
					if !known {
						continue
					}
					val := v.Value
					switch {
					case k == "lora" && !strings.Contains(keyPath, "animate"):
						if !strings.HasPrefix(val, "models/") {
							val = path.Join(loraDir, val)
						}
						if path.Ext(val) == "" {
							val += ".safetensors"
						}
					case (k == "lm_model" || k == "synth_model") && musicDir != "":
						val = path.Join(musicDir, val)
					}
					label := labels[idx]
					if strings.Contains(keyPath, ".animate.") {
						label = [2]string{"Animación · ", "Animation · "}[idx] + label
					}
					add(keyPath, label, val)
				} else {
					walk(v, append(append([]string{}, trail...), k))
				}
			}
		case yaml.SequenceNode:
			for _, c := range n.Content {
				walk(c, trail)
			}
		case yaml.DocumentNode:
			for _, c := range n.Content {
				walk(c, trail)
			}
		}
	}
	walk(&root, nil)
	return out
}

// catalogFileFor finds the catalog entry that ships a file with this name. A file can be in several
// entries (a VAE shared by two bundles): the one that also ships the most of the other files wins, so a
// project's files point at the same bundle; a tie goes to the first entry of the catalog.
func catalogFileFor(cat []modelcat.Entry, name string, siblings ...string) (modelcat.Entry, modelcat.File, bool) {
	var best modelcat.Entry
	var bestFile modelcat.File
	bestScore := -1
	for _, e := range cat {
		for _, f := range e.Files {
			if f.Name != name {
				continue
			}
			score := 0
			for _, other := range siblings {
				for _, g := range e.Files {
					if g.Name == other {
						score++
					}
				}
			}
			if score > bestScore {
				best, bestFile, bestScore = e, f, score
			}
		}
	}
	return best, bestFile, bestScore >= 0
}

// annotateRecipeFiles fills what the catalog and the disk say about each file. assetsRoot is the
// directory that holds models/ and voices/ ("" is the working directory).
func annotateRecipeFiles(cat []modelcat.Entry, files []recipeFile, assetsRoot, lang string) {
	names := make([]string, len(files))
	for i, f := range files {
		names[i] = f.Name
	}
	for i := range files {
		f := &files[i]
		if e, cf, ok := catalogFileFor(cat, f.Name, names...); ok {
			f.CatalogID, f.CatalogName, f.URL, f.RequiresToken = e.ID, catalogName(e, lang), cf.URL, e.RequiresToken
		}
		if fi, err := os.Stat(filepath.Join(assetsRoot, filepath.FromSlash(f.Path))); err == nil && fi.Mode().IsRegular() {
			f.Installed = true
			f.SizeBytes = fi.Size()
		}
	}
}

func catalogName(e modelcat.Entry, lang string) string {
	if lang == "en" && e.NameEN != "" {
		return e.NameEN
	}
	return e.Name
}

// recipePresets compares the settings of the project with the preset of the catalog entry of its models.
func recipePresets(cat []modelcat.Entry, cfg *config.Config, files []recipeFile, lang string) []recipePreset {
	var out []recipePreset
	byKey := map[string]recipeFile{}
	for _, f := range files {
		byKey[f.Key] = f
	}
	pick := func(keys ...string) (modelcat.Entry, bool) {
		for _, k := range keys {
			if f, ok := byKey[k]; ok {
				if f.CatalogID != "" {
					if e, ok := findEntry(cat, f.CatalogID); ok && e.Preset != nil {
						return e, true
					}
				}
			}
		}
		return modelcat.Entry{}, false
	}
	tr := func(es, en string) string {
		if lang == "en" {
			return en
		}
		return es
	}
	d := cfg.Defaults
	if e, ok := pick("defaults.model_set.diffusion_model", "defaults.model"); ok && cfg.Output.Mode != config.ModeMusic {
		used := recipeUsed{Steps: d.Steps, Resolution: d.Resolution}
		if d.ModelSet != nil {
			used.CfgScale = d.ModelSet.CfgScale
		}
		out = append(out, comparePreset(tr("Modelo de imagen", "Image model"), e, used, lang))
	}
	if a := d.Animate; a != nil && cfg.Output.Mode != config.ModeMusic {
		r := a.Resolved()
		if e, ok := pick("defaults.animate.diffusion_model", "defaults.animate.motion_module"); ok {
			if r.Engine == config.EngineAnimateDiff {
				e, _ = pick("defaults.animate.motion_module")
			}
			if e.Preset != nil {
				used := recipeUsed{Steps: r.Steps, Resolution: r.Resolution, CfgScale: r.CfgScale}
				out = append(out, comparePreset(tr("Animación", "Animation"), e, used, lang))
			}
		}
	}
	return out
}

func comparePreset(what string, e modelcat.Entry, used recipeUsed, lang string) recipePreset {
	p := recipePreset{For: what, CatalogID: e.ID, CatalogName: catalogName(e, lang), Recommended: e.Preset, Used: used, Matches: true}
	rec := e.Preset
	if rec.Steps != 0 && used.Steps != 0 && rec.Steps != used.Steps {
		p.Matches = false
		p.Differences = append(p.Differences, fmt.Sprintf("steps %d → %d", rec.Steps, used.Steps))
	}
	if rec.CfgScale != nil && used.CfgScale != nil && *rec.CfgScale != *used.CfgScale {
		p.Matches = false
		p.Differences = append(p.Differences, fmt.Sprintf("cfg %v → %v", *rec.CfgScale, *used.CfgScale))
	}
	if rec.Resolution != "" && used.Resolution != "" && rec.Resolution != used.Resolution {
		p.Matches = false
		p.Differences = append(p.Differences, fmt.Sprintf("%s → %s", rec.Resolution, used.Resolution))
	}
	return p
}

func recipeSettingsOf(cfg *config.Config) recipeSettings {
	d := cfg.Defaults
	s := recipeSettings{}
	if cfg.Output.Mode != config.ModeMusic {
		s.Resolution, s.Sampler, s.Steps, s.FPS = d.Resolution, d.Sampler, d.Steps, d.FPS
		if d.ModelSet != nil {
			s.CfgScale = d.ModelSet.CfgScale
		}
		if a := d.Animate; a != nil {
			r := a.Resolved()
			engine := r.Engine
			if engine == "" {
				engine = config.EngineWan
			}
			s.Animate = &recipeAnimate{Engine: engine, Resolution: r.Resolution, Frames: r.Frames, FPS: r.FPS, Steps: r.Steps, Turbo: r.LoRA != ""}
			if r.CfgScale != nil {
				c := *r.CfgScale
				s.Animate.CfgScale = &c
			}
		}
	} else if m := d.Music; m != nil {
		s.Music = &recipeMusicSet{InferenceSteps: m.InferenceSteps, OutputFormat: m.OutputFormat}
	}
	return s
}

// buildRecipe describes a project. assetsRoot is the directory with models/ and voices/ ("" is the working directory).
func buildRecipe(cat []modelcat.Entry, yamlData []byte, cfg *config.Config, assetsRoot, lang string, now time.Time) *recipe {
	files := collectRecipeFiles(yamlData, cfg, lang)
	m3d, m3dFiles := models3DRecipe(cfg)
	seenPath := map[string]bool{}
	for _, f := range files {
		seenPath[f.Path] = true
	}
	for _, f := range m3dFiles {
		if !seenPath[f.Path] { // the cut-out model is shared by the two engines
			seenPath[f.Path] = true
			files = append(files, f)
		}
	}
	annotateRecipeFiles(cat, files, assetsRoot, lang)
	r := &recipe{
		Format:     recipeFormat,
		Project:    cfg.Project,
		Mode:       cfg.Output.Mode,
		ExportedAt: now.UTC().Format(time.RFC3339),
		Scenes:     len(cfg.Scenes),
		Tracks:     len(cfg.Tracks),
		Settings:   recipeSettingsOf(cfg),
		Presets:    recipePresets(cat, cfg, files, lang),
		Files:      files,
	}
	r.Settings.Models3D = m3d
	if m3d != nil {
		r.Models = len(cfg.Models)
	}
	return r
}

func (r *recipe) json() []byte {
	b, _ := json.MarshalIndent(r, "", "  ")
	return append(b, '\n')
}

// --- the readable version -----------------------------------------------------------------------

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(n)/(1<<20))
	case n > 0:
		return fmt.Sprintf("%d KB", (n+1023)/1024)
	}
	return "—"
}

// markdown is RECIPE.md: the same facts in words, for whoever opens the package.
func (r *recipe) markdown(lang string) []byte {
	tr := func(es, en string) string {
		if lang == "en" {
			return en
		}
		return es
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# %s: %s\n\n", tr("Receta del proyecto", "Project recipe"), r.Project)
	fmt.Fprintf(&b, "%s %s · %s\n\n", tr("Exportado el", "Exported on"), r.ExportedAt, tr("modo", "mode")+" `"+string(r.Mode)+"`")
	b.WriteString(tr(
		"Esto es lo que usa el proyecto. Para repetirlo en otro equipo: importa el `.zip` desde el panel (Proyectos > Importar proyecto completo); te dirá qué modelos tienes y cuáles faltan, y los instala con un clic. Después solo hay que lanzar la generación.\n\n",
		"This is what the project uses. To repeat it on another machine: import the `.zip` from the dashboard (Projects > Import full project); it tells you which models you have and which are missing, and installs them with one click. Then just launch the generation.\n\n"))

	b.WriteString("## " + tr("Ajustes con los que se genera", "Settings it generates with") + "\n\n")
	s := r.Settings
	row := func(k, v string) {
		if v != "" {
			fmt.Fprintf(&b, "- **%s**: %s\n", k, v)
		}
	}
	if m := s.Models3D; m != nil {
		for _, e := range m.Engines {
			gated := ""
			if e.Gated {
				gated = tr(" (los pesos piden aceptar su licencia con tu cuenta de Hugging Face)", " (the weights need your Hugging Face account to accept its licence)")
			}
			row(tr("Motor 3D", "3D engine"), fmt.Sprintf("%s · %s%s", e.Name, e.Licence, gated))
		}
	}
	if r.Mode != config.ModeMusic {
		row(tr("Resolución", "Resolution"), s.Resolution)
		row(tr("Muestreador", "Sampler"), s.Sampler)
		if s.Steps > 0 {
			row(tr("Pasos", "Steps"), fmt.Sprint(s.Steps))
		}
		if s.CfgScale != nil {
			row("cfg_scale", fmt.Sprint(*s.CfgScale))
		}
		if s.FPS > 0 {
			row("FPS", fmt.Sprint(s.FPS))
		}
		if a := s.Animate; a != nil {
			cfgs := ""
			if a.CfgScale != nil {
				cfgs = fmt.Sprintf(", cfg %v", *a.CfgScale)
			}
			turbo := ""
			if a.Turbo {
				turbo = tr(" (con LoRA turbo: borrador)", " (with the turbo LoRA: draft)")
			}
			row(tr("Animación", "Animation"), fmt.Sprintf("%s · %s · %d %s · %d fps · %d %s%s%s", a.Engine, a.Resolution, a.Frames, tr("fotogramas", "frames"), a.FPS, a.Steps, tr("pasos", "steps"), cfgs, turbo))
		}
	} else if m := s.Music; m != nil {
		if m.InferenceSteps > 0 {
			row(tr("Pasos de síntesis", "Synthesis steps"), fmt.Sprint(m.InferenceSteps))
		}
		row(tr("Formato", "Format"), m.OutputFormat)
	}
	b.WriteString("\n")

	if len(r.Presets) > 0 {
		b.WriteString("## " + tr("Ajustes recomendados del catálogo", "Catalog recommended settings") + "\n\n")
		for _, p := range r.Presets {
			rec := []string{}
			if p.Recommended.Steps != 0 {
				rec = append(rec, fmt.Sprintf("%d %s", p.Recommended.Steps, tr("pasos", "steps")))
			}
			if p.Recommended.CfgScale != nil {
				rec = append(rec, fmt.Sprintf("cfg %v", *p.Recommended.CfgScale))
			}
			if p.Recommended.Resolution != "" {
				rec = append(rec, p.Recommended.Resolution)
			}
			state := tr("el proyecto los usa tal cual", "the project uses them as they are")
			if !p.Matches {
				state = tr("el proyecto los cambió: ", "the project changed them: ") + strings.Join(p.Differences, ", ")
			}
			fmt.Fprintf(&b, "- **%s** (%s): %s — %s\n", p.For, p.CatalogName, strings.Join(rec, " · "), state)
		}
		b.WriteString("\n")
	}

	b.WriteString("## " + tr("Archivos que necesita", "Files it needs") + "\n\n")
	b.WriteString("| " + tr("Para qué", "What for") + " | " + tr("Archivo", "File") + " | " + tr("Tamaño", "Size") + " | " + tr("Dónde conseguirlo", "Where to get it") + " |\n|---|---|---|---|\n")
	for _, f := range r.Files {
		src := tr("no está en el catálogo: búscalo por su nombre", "not in the catalog: look it up by its name")
		if f.Note != "" {
			src = f.Note
		}
		if f.CatalogID != "" {
			src = fmt.Sprintf("%s (`%s`)", f.CatalogName, f.CatalogID)
		}
		fmt.Fprintf(&b, "| %s | `%s` | %s | %s |\n", f.Label, f.Path, humanBytes(f.SizeBytes), src)
	}
	b.WriteString("\n")
	return []byte(b.String())
}

// sortedFileNames is a helper for stable output in tests.
func sortedFileNames(files []recipeFile) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.Name)
	}
	sort.Strings(out)
	return out
}

// findEntry is the entry of the catalog with that identifier.
func findEntry(cat []modelcat.Entry, id string) (modelcat.Entry, bool) {
	for _, e := range cat {
		if e.ID == id {
			return e, true
		}
	}
	return modelcat.Entry{}, false
}
