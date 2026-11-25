package importer

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"veritmo/internal/project/config"
	"veritmo/internal/video/render"
)

// Dataset is a project written in the dataset format `veritmo import`
// reads: a JSON array of [id, title, lora_info, full_prompt] tuples.
type Dataset struct {
	// JSON is the file: one tuple per line.
	JSON []byte
	// Exported counts the scenes or tracks written.
	Exported int
	// Notes say what the format cannot carry, so nobody is surprised by what
	// an import brings back. They are not errors.
	Notes []string
}

// ExportDataset writes the scenes (video or images mode), the tracks (music
// mode) or the models (models3d mode) of a project as a dataset that Map or MapMusic read back: what each
// entry says is put in the lines the importer understands (dialogue, negative,
// motion, seed, speed, silence and variants for scenes, plus graphic and overlay lines for the animated graphics; lyrics, language, bpm,
// duration, key, time signature, seed and variants for songs). What the project
// shares (subject prompt, negative prompt, style prefix and voice) is folded
// into each entry, so the dataset stands on its own.
//
// The format carries content, not setup: models, voice, resolution, sampler,
// steps, animation models, camera and look presets, per-scene render settings,
// own pictures, mixes and favorites stay in the project. A project package
// (scenes.yaml with its sources) keeps all of it.
func ExportDataset(cfg *config.Config) (*Dataset, error) {
	if cfg == nil {
		return nil, fmt.Errorf("export: no project")
	}
	d := &Dataset{}
	var tuples [][]string
	if cfg.Output.Mode == config.ModeMusic {
		tuples = exportTracks(cfg, d)
	} else if cfg.Output.Mode == config.ModeModels3D {
		tuples = exportModels(cfg, d)
	} else {
		tuples = exportScenes(cfg, d)
	}
	if len(tuples) == 0 {
		return nil, fmt.Errorf("export: the project has nothing to export (no scenes, songs or models)")
	}
	var b strings.Builder
	b.WriteString("[\n")
	for i, t := range tuples {
		line, err := json.Marshal(t)
		if err != nil {
			return nil, err
		}
		b.WriteString("  ")
		b.Write(line)
		if i < len(tuples)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	b.WriteString("]\n")
	d.JSON = []byte(b.String())
	d.Exported = len(tuples)
	return d, nil
}

// oneLine collapses a text to a single line: the dataset is read line by line.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func exportScenes(cfg *config.Config, d *Dataset) [][]string {
	var out [][]string
	var notCarried = map[string]bool{}
	pages := false
	labelled, designed := false, false
	var descriptions []string // the 3D scenes' description files: only the package carries them
	for _, s := range cfg.Scenes {
		prompt := render.ComposePositive(oneLine(cfg.Defaults.SubjectPrompt), oneLine(s.Prompt))
		build := ""
		if s.Blender != nil {
			if s.Blender.Build == "" {
				d.Notes = append(d.Notes, fmt.Sprintf("scene %q is rendered from a .blend file (%s), which a dataset cannot carry: it was not exported", s.ID, s.Blender.File))
				continue
			}
			build = s.Blender.Build
			descriptions = append(descriptions, build)
			prompt = oneLine(s.Prompt) // the project's subject is for pictures, not for a 3D scene
		}
		if build != "" {
			// A 3D scene: its description is a file of the sources, named by the Blender line.
		} else if s.MotionPart != "" {
			// A graphic made from a part of the catalog: its description (for auto) and the part with what it is given.
			pages = true
			prompt = ""
			if s.MotionGraphic != "" {
				prompt = "Graphic: " + oneLine(s.MotionGraphic)
			}
		} else if s.MotionGraphic != "" {
			prompt = "Graphic: " + oneLine(s.MotionGraphic)
			pages = true
		} else if oneLine(s.Prompt) == "" {
			switch {
			case s.Image != "":
				prompt = fmt.Sprintf("Own picture %s (not carried: copy it with the project package)", s.Image)
				d.Notes = append(d.Notes, fmt.Sprintf("scene %q uses its own picture %s from sources/, which a dataset cannot carry", s.ID, s.Image))
			case s.Clip != "":
				prompt = fmt.Sprintf("Own clip %s (not carried: copy it with the project package)", s.Clip)
				d.Notes = append(d.Notes, fmt.Sprintf("scene %q uses its own clip %s from sources/, which a dataset cannot carry", s.ID, s.Clip))
			default:
				d.Notes = append(d.Notes, fmt.Sprintf("scene %q has no prompt and was not exported", s.ID))
				continue
			}
		}
		lines := []string{strings.TrimSpace(prompt)}
		if build != "" {
			lines = lines[:0]
			if t := strings.TrimSpace(prompt); t != "" {
				lines = append(lines, t)
			}
			lines = append(lines, "Blender: "+build)
		}
		if s.MotionPart != "" {
			lines = append(lines, "Part: "+s.MotionPart)
			names := make([]string, 0, len(s.MotionParams))
			for k := range s.MotionParams {
				names = append(names, k)
			}
			sort.Strings(names)
			for _, k := range names {
				lines = append(lines, fmt.Sprintf("Param %s: %s", k, oneLine(fmt.Sprint(s.MotionParams[k]))))
			}
			if s.MotionText != "" {
				lines = append(lines, "Text: "+oneLine(s.MotionText))
			}
			if s.MotionBPM != nil {
				lines = append(lines, fmt.Sprintf("BPM: %v", *s.MotionBPM))
			}
			if !designed && oneLine(cfg.Defaults.SubjectPrompt) != "" { // the palette belongs to the project: the first entry carries it
				designed = true
				lines = append(lines, "Design: "+oneLine(cfg.Defaults.SubjectPrompt))
			}
		}
		if s.IsSilent() {
			lines = append(lines, fmt.Sprintf("Silence: %v", s.SilenceDuration()))
		} else if text := oneLine(s.TTSText); text != "" {
			if strings.Contains(text, `"`) {
				d.Notes = append(d.Notes, fmt.Sprintf("scene %q: double quotes in the narration were written as single quotes, which is how the format delimits it", s.ID))
				text = strings.ReplaceAll(text, `"`, `'`)
			}
			lines = append(lines, fmt.Sprintf("Dialogue: %q", text))
			if s.Speed > 0 && s.Speed != 1 {
				lines = append(lines, fmt.Sprintf("Speed: %v", s.Speed))
			}
		}
		if o := s.Overlay; o != nil && oneLine(o.Text) != "" {
			pages = true
			lines = append(lines, "Overlay: "+oneLine(o.Text))
			if o.Position != "" {
				lines = append(lines, "Overlay position: "+o.Position)
			}
			if o.Start != nil {
				lines = append(lines, fmt.Sprintf("Overlay start: %v", *o.Start))
			}
			if o.End != nil {
				lines = append(lines, fmt.Sprintf("Overlay end: %v", *o.End))
			}
		}
		if !labelled && cfg.Adult != nil { // the label belongs to the project: the first entry carries it
			labelled = true
			lines = append(lines, "Adult: "+map[bool]string{true: "yes", false: "no"}[*cfg.Adult])
		}
		if s.IsMotionGraphic() {
			// A graphic is an animated page: it has no seed, variants, negative prompt or animation of its own.
			out = append(out, []string{s.ID, s.ID, "", strings.Join(lines, "\n")})
			continue
		}
		if s.Seed != nil {
			lines = append(lines, fmt.Sprintf("Seed: %d", *s.Seed))
		}
		if s.Variants != nil && *s.Variants != 1 {
			lines = append(lines, fmt.Sprintf("Variants: %d", *s.Variants))
		}
		if neg := render.ComposeNegative(oneLine(s.NegativePrompt), oneLine(cfg.Defaults.NegativePrompt)); neg != "" {
			lines = append(lines, "Negative: "+neg)
		}
		if s.Animate {
			motion := oneLine(s.MotionPrompt)
			if motion == "" {
				motion = oneLine(s.Prompt)
			}
			lines = append(lines, "Motion: "+motion)
		}
		out = append(out, []string{s.ID, s.ID, "", strings.Join(lines, "\n")})
		for what, set := range map[string]bool{
			"camera motion": s.Motion != nil, "look": s.Look != nil, "lora": s.Lora != "",
			"init_image": s.InitImage != "", "per-scene render/animation settings": s.Render != nil || s.Animation != nil,
		} {
			if set {
				notCarried[what] = true
			}
		}
	}
	for _, what := range []string{"camera motion", "look", "lora", "init_image", "per-scene render/animation settings"} {
		if notCarried[what] {
			d.Notes = append(d.Notes, what+" of some scenes is not carried by the dataset format")
		}
	}
	if len(descriptions) > 0 {
		d.Notes = append(d.Notes, fmt.Sprintf("the dataset keeps the Blender line of the 3D scenes, not their description files (%s): they travel in the project package, or the assistant writes them again", strings.Join(descriptions, ", ")))
	}
	if pages {
		d.Notes = append(d.Notes, "the pages of the animated graphics and titles are not carried: the model writes them again when the dataset is imported (the full project keeps them)")
	}
	d.Notes = append(d.Notes, "the models, voice, resolution, steps and animation models are not carried: they belong to the project that imports it")
	return out
}

func exportTracks(cfg *config.Config, d *Dataset) [][]string {
	var out [][]string
	m := cfg.Defaults.Music
	for _, t := range cfg.Tracks {
		if t.Task != "" {
			d.Notes = append(d.Notes, fmt.Sprintf("track %q (%s) starts from a song and was not exported", t.ID, t.Task))
			continue
		}
		lines := []string{oneLine(m.CaptionFor(t))}
		if t.Instrumental {
			lines = append(lines, "Instrumental")
		} else if strings.TrimSpace(t.Lyrics) != "" {
			lines = append(lines, "Lyrics:", strings.TrimSpace(t.Lyrics))
		}
		lang := t.VocalLanguage
		if lang == "" && m != nil {
			lang = m.VocalLanguage
		}
		if lang != "" && !t.Instrumental {
			lines = append(lines, "Language: "+lang)
		}
		if t.BPM != nil {
			lines = append(lines, fmt.Sprintf("BPM: %d", *t.BPM))
		}
		if t.DurationSeconds != nil {
			lines = append(lines, fmt.Sprintf("Duration: %v", *t.DurationSeconds))
		}
		if t.KeyScale != "" {
			lines = append(lines, "Key: "+t.KeyScale)
		}
		if t.TimeSignature != "" {
			lines = append(lines, "Time signature: "+t.TimeSignature)
		}
		if t.Seed != nil {
			lines = append(lines, fmt.Sprintf("Seed: %d", *t.Seed))
		}
		if t.Tags != "" {
			lines = append(lines, "Tags: "+string(t.Tags))
		}
		if t.Variants != nil && *t.Variants != 1 {
			lines = append(lines, fmt.Sprintf("Variants: %d", *t.Variants))
		}
		out = append(out, []string{t.ID, t.ID, "", strings.Join(lines, "\n")})
	}
	d.Notes = append(d.Notes, "the style prefix and the voice are folded into each style; the engine settings (and the generator), adapters, mixes, favorites and songs made from other songs are not carried")
	return out
}

// packageReadme goes inside a project package.
const packageReadme = `A Veritmo project package: scenes.yaml (every setting of the project),
the files of its sources/ folder, and the recipe (RECIPE.md to read, recipe.json
for the program): the models, voice and LoRAs it uses, the settings it runs with,
the catalog's recommended settings and where each file can be downloaded.

To run it elsewhere, the easy way: in the dashboard, Projects > Import full
project, and choose this .zip. It says which models are installed and which are
missing (and installs the missing ones with one click). Then launch the generation.

By hand: unpack it into a folder, install the models listed in RECIPE.md and run
  veritmo --keep-tmp scenes.yaml
or put the folder in the projects directory of an 'veritmo serve'.
Models and voices are not in the package, they belong to the machine; neither is
what was generated (out/, tmp/).
`

// motionSubdir is the project's folder of motion-graphic pages (internal/video/motion.Dir).
const motionSubdir = "motion"

// IsMotionFile says whether a file name is one a project keeps in its motion folder: a page or its details.
func IsMotionFile(name string) bool {
	return !strings.HasPrefix(name, ".") && (strings.HasSuffix(name, ".html") || strings.HasSuffix(name, ".meta.json"))
}

// PackageFile is an extra file for a project package.
type PackageFile struct {
	Name string
	Data []byte
}

// WriteProject writes a project package to w: a zip with scenes.yaml, the files
// of the project's sources/ folder, a short README and the extra files (the
// recipe). Models, voices and what the project generated (out/, tmp/) stay out:
// they belong to the machine and can be made again. projectDir is the folder
// that holds scenes.yaml.
func WriteProject(w io.Writer, projectDir string, extra ...PackageFile) error {
	zw := zip.NewWriter(w)
	add := func(name string, data []byte) error {
		f, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: time.Now()})
		if err != nil {
			return err
		}
		_, err = f.Write(data)
		return err
	}
	yamlData, err := os.ReadFile(filepath.Join(projectDir, "scenes.yaml"))
	if err != nil {
		return fmt.Errorf("export: cannot read scenes.yaml: %w", err)
	}
	if err := add("scenes.yaml", yamlData); err != nil {
		return err
	}
	if err := add("README.txt", []byte(packageReadme)); err != nil {
		return err
	}
	for _, e := range extra {
		if err := add(e.Name, e.Data); err != nil {
			return err
		}
	}
	entries, _ := os.ReadDir(filepath.Join(projectDir, config.SourcesSubdir))
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		src, err := os.Open(filepath.Join(projectDir, config.SourcesSubdir, e.Name()))
		if err != nil {
			return err
		}
		// Pictures and audio are already compressed: store them as they are.
		f, err := zw.CreateHeader(&zip.FileHeader{Name: config.SourcesSubdir + "/" + e.Name(), Method: zip.Store, Modified: time.Now()})
		if err == nil {
			_, err = io.Copy(f, src)
		}
		src.Close()
		if err != nil {
			return err
		}
	}
	// The pages of motion graphics and overlays (and the details of who wrote them) are part of the project.
	pages, _ := os.ReadDir(filepath.Join(projectDir, motionSubdir))
	for _, e := range pages {
		if !e.Type().IsRegular() || !IsMotionFile(e.Name()) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(projectDir, motionSubdir, e.Name()))
		if err != nil {
			return err
		}
		if err := add(motionSubdir+"/"+e.Name(), data); err != nil {
			return err
		}
	}
	return zw.Close()
}
