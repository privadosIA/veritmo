package importer

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
	"veritmo/internal/project/config"
)

// Options configure one import run.
type Options struct {
	// Project names the generated project (scenes.yaml `project:` field).
	Project string
	// SilenceSeconds is assigned to entries without a dialogue line.
	SilenceSeconds float64
	// Parts is the catalog the parts of the scenes are read and checked against; nil does not read the parameters of a
	// part (an entry that gives some is rejected) and does not check the parts.
	Parts config.PartCatalog
}

// EntryReport records what happened to one dataset entry.
type EntryReport struct {
	ID        string
	Mapped    []string // human-readable "field ← source" lines
	Discarded []string // discarded items with their content or reason
	Rejected  string   // non-empty when the entry was rejected
}

// Result is a completed mapping: the in-memory config plus the per-entry
// reports and totals.
type Result struct {
	Config   *config.Config
	Reports  []EntryReport
	Imported int
	Silent   int
	Rejected int
	// Instrumental counts the tracks of a music import without lyrics.
	Instrumental int
	// motion is set by an animated graphics import.
	motion bool
	// parts is the catalog the entries were read against (Options.Parts).
	parts config.PartCatalog
}

// music reports whether the result is a music project.
func (r *Result) music() bool { return r.Config.Output.Mode == config.ModeMusic }

// models3d reports whether the result is a project that makes 3D models.
func (r *Result) models3d() bool { return r.Config.Output.Mode == config.ModeModels3D }

var (
	dialogueRe = regexp.MustCompile(`^Dialogue(\s*\([^)]*\))?\s*:`)
	quotedRe   = regexp.MustCompile(`"([^"]*)"`)
	// sceneMarkerRe matches the optional one-line settings of a scene entry
	// (case-insensitive): what moves, the seed, the narration speed, the length
	// of a silent scene and the number of variants.
	// The graphic and overlay lines belong to the animated graphics (see MapMotion); Duration is read only there.
	sceneMarkerRe = regexp.MustCompile(`(?i)^(motion|seed|speed|silence|variants|adult|graphic|duration|part|text|bpm|design|blender|param\s+[a-z_][a-z0-9_]*|overlay(?:\s+(?:position|start|end))?)\s*:\s*(.*)$`)
)

// sceneSettings are the values of the optional marker lines of one entry.
type sceneSettings struct {
	motion   string
	seed     *int
	speed    float64
	silence  float64
	variants *int
	// adult is the project's label as one entry states it (Adult: yes|no).
	adult *bool
	// graphic describes an animated graphic; overlay and its range and position put a title over the scene.
	graphic      string
	overlay      string
	overlayPos   string
	overlayStart *float64
	overlayEnd   *float64
	overlayGiven []string // the overlay lines that were set, for the report when there is no text
	// The animated graphics made from parts of the catalog: the part (or auto), its parameters as text, the line of text, the
	// tempo and the words of style the project's palette is chosen from.
	part   string
	params map[string]string
	text   string
	bpm    *float64
	design string
	// blender is the description of a 3D scene (a .scene.json of the sources) the scene is rendered from.
	blender string
}

// set applies one marker line, or says why it cannot.
func (s *sceneSettings) set(name, val string, report *EntryReport) error {
	note := func(field, v string) { report.Mapped = append(report.Mapped, fmt.Sprintf("%s ← %s", field, v)) }
	name = strings.Join(strings.Fields(strings.ToLower(name)), " ")
	if strings.HasPrefix(name, "param ") {
		pn := strings.TrimPrefix(name, "param ")
		if s.params == nil {
			s.params = map[string]string{}
		}
		if _, dup := s.params[pn]; dup {
			return fmt.Errorf("Param %s is given twice", pn)
		}
		s.params[pn] = val
		note("motion_params."+pn, val)
		return nil
	}
	switch name {
	case "graphic":
		if val == "" {
			return fmt.Errorf("Graphic line without text")
		}
		s.graphic = val
	case "part":
		if val == "" {
			return fmt.Errorf("Part line without a name")
		}
		s.part = strings.ToLower(val) // reported once, where the scene is built
	case "text":
		if val == "" {
			return fmt.Errorf("Text line without text")
		}
		s.text = val
		note("motion_text", fmt.Sprintf("%q", val))
	case "bpm":
		f, err := strconv.ParseFloat(val, 64)
		if err != nil || f < config.MinMotionBPM || f > config.MaxMotionBPM {
			return fmt.Errorf("BPM %q must be a number between %v and %v", val, config.MinMotionBPM, config.MaxMotionBPM)
		}
		s.bpm = &f
		note("motion_bpm", val)
	case "design":
		if val == "" {
			return fmt.Errorf("Design line without words")
		}
		s.design = val
	case "overlay":
		if val == "" {
			return fmt.Errorf("Overlay line without text")
		}
		s.overlay = val
		note("overlay.text", fmt.Sprintf("%q", val))
	case "overlay position":
		switch strings.ToLower(val) {
		case config.OverlayTop, config.OverlayCenter, config.OverlayBottom:
		default:
			return fmt.Errorf("Overlay position %q must be top, center or bottom", val)
		}
		s.overlayPos = strings.ToLower(val)
		s.overlayGiven = append(s.overlayGiven, "Overlay position "+val)
		note("overlay.position", s.overlayPos)
	case "overlay start", "overlay end":
		f, err := strconv.ParseFloat(strings.TrimSuffix(strings.ToLower(val), "s"), 64)
		if err != nil || f < 0 || (name == "overlay end" && f <= 0) {
			return fmt.Errorf("%s %q is not a number of seconds", strings.ToUpper(name[:1])+name[1:], val)
		}
		if name == "overlay start" {
			s.overlayStart = &f
		} else {
			s.overlayEnd = &f
		}
		s.overlayGiven = append(s.overlayGiven, strings.ToUpper(name[:1])+name[1:]+" "+val)
		note("overlay."+strings.TrimPrefix(name, "overlay "), val)
	case "motion":
		if val == "" {
			return fmt.Errorf("Motion line without text")
		}
		s.motion = val
		note("motion_prompt", fmt.Sprintf("%q (animate ← true)", val))
	case "blender":
		if s.blender != "" {
			return fmt.Errorf("a second Blender line (at most one per entry)")
		}
		if !config.ValidSceneSpecName(val) {
			return fmt.Errorf("Blender %q must be the name of a %s file, not a path", val, config.SceneSpecSuffix)
		}
		s.blender = val
		note("blender.build", val)
	case "adult":
		switch strings.ToLower(val) {
		case "yes", "true":
			t := true
			s.adult = &t
		case "no", "false":
			f := false
			s.adult = &f
		default:
			return fmt.Errorf("Adult %q must be yes or no", val)
		}
		note("adult", strings.ToLower(val))
	case "seed":
		n, err := strconv.Atoi(val)
		if err != nil || n < 0 {
			return fmt.Errorf("Seed %q is not a non-negative integer", val)
		}
		s.seed = &n
		note("seed", val)
	case "speed":
		f, err := strconv.ParseFloat(val, 64)
		if err != nil || f <= 0 {
			return fmt.Errorf("Speed %q is not a number > 0", val)
		}
		s.speed = f
		note("speed", val)
	case "silence":
		f, err := strconv.ParseFloat(val, 64)
		if err != nil || f <= 0 {
			return fmt.Errorf("Silence %q is not a number of seconds > 0", val)
		}
		s.silence = f
		note("silence_seconds", val)
	case "variants":
		n, err := strconv.Atoi(val)
		if err != nil || n < 1 {
			return fmt.Errorf("Variants %q is not an integer >= 1", val)
		}
		s.variants = &n
		note("variants", val)
	}
	return nil
}

// Map converts loaded entries (plus loader-side rejections) into a Result
// per the scene-import spec: dialogue → tts_text, no dialogue →
// silence_seconds, shared negative → defaults, camera/style discarded,
// lora_info warned. Duplicate ids abort the whole import.
func Map(entries []Entry, loaderRejected []Rejection, opts Options) (*Result, error) {
	return mapScenes(entries, loaderRejected, opts, false)
}

// MapMotion converts the entries into animated graphics (see specs/scene-import): a video project whose scenes
// are all `motion_graphic`, each described by the lines before the first marker or by a Graphic line.
func MapMotion(entries []Entry, loaderRejected []Rejection, opts Options) (*Result, error) {
	return mapScenes(entries, loaderRejected, opts, true)
}

func mapScenes(entries []Entry, loaderRejected []Rejection, opts Options, motion bool) (*Result, error) {
	if opts.SilenceSeconds <= 0 {
		return nil, fmt.Errorf("import: silence-seconds must be > 0, got %v", opts.SilenceSeconds)
	}

	// Duplicate ids abort: they are cache keys and --only targets.
	seen := map[string]int{}
	for i, e := range entries {
		if prev, dup := seen[e.ID]; dup {
			return nil, fmt.Errorf("import: duplicate entry id %q (entries %d and %d)", e.ID, prev, i)
		}
		seen[e.ID] = i
	}

	res := &Result{Config: &config.Config{Project: opts.Project, Parts: opts.Parts}, motion: motion, parts: opts.Parts}
	for _, r := range loaderRejected {
		res.Reports = append(res.Reports, EntryReport{ID: r.ID, Rejected: r.Reason})
		res.Rejected++
	}

	type parsed struct {
		entry    Entry
		report   EntryReport
		scene    config.Scene
		negative string
		adult    *bool
	}
	var ok []parsed

	for _, e := range entries {
		report := EntryReport{ID: e.ID}
		var promptLines []string
		var dialogue *string
		var negative string
		var settings sceneSettings
		rejected := ""

		for _, line := range strings.Split(e.FullPrompt, "\n") {
			trimmed := strings.TrimSpace(line)
			switch {
			case trimmed == "":
			case dialogueRe.MatchString(trimmed):
				if dialogue != nil {
					rejected = "second Dialogue line (at most one per entry)"
					break
				}
				m := quotedRe.FindStringSubmatch(trimmed)
				if m == nil {
					rejected = "Dialogue line without quoted text"
					break
				}
				d := m[1]
				dialogue = &d
				report.Mapped = append(report.Mapped, fmt.Sprintf("tts_text ← dialogue %q", d))
			case sceneMarkerRe.MatchString(trimmed):
				m := sceneMarkerRe.FindStringSubmatch(trimmed)
				name := m[1]
				if strings.EqualFold(name, "duration") {
					if !motion {
						promptLines = append(promptLines, trimmed) // only the graphics read it
						break
					}
					name = "silence"
				}
				if !motion && isMotionPartMarker(name) {
					promptLines = append(promptLines, trimmed) // only the graphics read them
					break
				}
				if err := settings.set(name, strings.TrimSpace(m[2]), &report); err != nil {
					rejected = err.Error()
				}
			case strings.HasPrefix(trimmed, "Negative:"):
				negative = strings.TrimSpace(strings.TrimPrefix(trimmed, "Negative:"))
			case strings.HasPrefix(trimmed, "Camera/style:"):
				report.Discarded = append(report.Discarded,
					"Camera/style (not parsed into motion/look): "+strings.TrimSpace(strings.TrimPrefix(trimmed, "Camera/style:")))
			default:
				promptLines = append(promptLines, trimmed)
			}
			if rejected != "" {
				break
			}
		}

		prompt := strings.Join(strings.Fields(strings.Join(promptLines, " ")), " ")
		graphic := ""
		// A 3D scene is described by its file: what describes a picture or a graphic beside it is not applied.
		if settings.blender != "" {
			if settings.graphic != "" {
				report.Discarded = append(report.Discarded, fmt.Sprintf("Graphic (%d chars): the scene is a 3D scene (Blender)", len(settings.graphic)))
				settings.graphic = ""
			}
			if motion {
				if prompt != "" {
					report.Discarded = append(report.Discarded, fmt.Sprintf("text before the markers (%d chars): the scene is a 3D scene (Blender)", len(prompt)))
					prompt = ""
				}
				if settings.part != "" || len(settings.params) > 0 || settings.text != "" || settings.bpm != nil || settings.design != "" {
					report.Discarded = append(report.Discarded, "Part, Param, Text, BPM and Design lines: the scene is a 3D scene (Blender), not made from a part")
					settings.part, settings.params, settings.text, settings.bpm, settings.design = "", nil, "", nil, ""
				}
			}
		}
		named := motion && settings.part != "" && settings.part != "auto" // a part named: no description is needed
		if rejected == "" && motion {
			rejected = checkPartMarkers(&settings)
		}
		switch {
		case settings.graphic != "" || motion:
			// A graphic is described by its Graphic line, or (graphics mode) by the lines before the markers.
			graphic = settings.graphic
			if graphic == "" {
				graphic = prompt
			} else if prompt != "" {
				report.Discarded = append(report.Discarded, fmt.Sprintf("text before the markers (%d chars): the scene is a graphic described by its Graphic line", len(prompt)))
			}
			prompt = ""
			if named {
				if graphic != "" {
					report.Discarded = append(report.Discarded, fmt.Sprintf("description (%d chars): the scene is made from the part %q, which needs none", len(graphic), settings.part))
				}
				graphic = ""
			} else if rejected == "" && graphic == "" && settings.blender == "" {
				rejected = "entry has no description of the graphic"
			}
		case rejected == "" && prompt == "" && settings.blender == "":
			rejected = "entry yields an empty prompt"
		}
		if rejected != "" {
			report.Rejected = rejected
			res.Reports = append(res.Reports, report)
			res.Rejected++
			continue
		}

		scene := config.Scene{ID: e.ID, Prompt: prompt, MotionGraphic: graphic}
		if motion && settings.part != "" {
			if r := applyPart(res.parts, &scene, &settings); r != "" {
				report.Rejected = r
				res.Reports = append(res.Reports, report)
				res.Rejected++
				continue
			}
			if settings.design != "" {
				if res.Config.Defaults.SubjectPrompt == "" {
					res.Config.Defaults.SubjectPrompt = settings.design
					report.Mapped = append(report.Mapped, fmt.Sprintf("subject_prompt ← %q (the palette of the project is chosen from these words)", settings.design))
				} else {
					report.Discarded = append(report.Discarded, "Design: the project's palette is set by the first entry that gives one")
				}
			}
		}
		if scene.MotionPart != "" {
			report.Mapped = append(report.Mapped, fmt.Sprintf("motion_part ← %s", scene.MotionPart))
		}
		if graphic != "" {
			report.Mapped = append(report.Mapped, fmt.Sprintf("motion_graphic ← %d chars", len(graphic)))
		} else if scene.MotionPart == "" && prompt != "" {
			report.Mapped = append(report.Mapped, fmt.Sprintf("prompt ← %d chars", len(prompt)))
		}
		if dialogue != nil {
			scene.TTSText = *dialogue
			if settings.silence > 0 {
				report.Discarded = append(report.Discarded, fmt.Sprintf("Silence %v: the scene has a dialogue, so its length is the narration's", settings.silence))
			}
		} else {
			s := opts.SilenceSeconds
			if settings.silence > 0 {
				s = settings.silence
				report.Mapped = append(report.Mapped, fmt.Sprintf("silence_seconds ← %v (Silence line)", s))
			} else {
				report.Mapped = append(report.Mapped, fmt.Sprintf("silence_seconds ← %v (no dialogue line)", s))
			}
			scene.SilenceSeconds = &s
			res.Silent++
			if settings.speed > 0 {
				report.Discarded = append(report.Discarded, fmt.Sprintf("Speed %v: the scene is silent", settings.speed))
				settings.speed = 0
			}
		}
		if settings.speed > 0 {
			scene.Speed = settings.speed
		}
		if settings.blender != "" {
			scene.Blender = &config.SceneBlender{Build: settings.blender}
			if settings.motion != "" {
				report.Discarded = append(report.Discarded, fmt.Sprintf("Motion %q: the scene is rendered by Blender, it has nothing to animate", settings.motion))
				settings.motion = ""
			}
		}
		if settings.motion != "" {
			if graphic != "" {
				report.Discarded = append(report.Discarded, fmt.Sprintf("Motion %q: a graphic is an animated page, it has nothing to animate", settings.motion))
			} else {
				scene.Animate, scene.MotionPrompt = true, settings.motion
			}
		}
		if settings.overlay != "" {
			o := &config.Overlay{Text: settings.overlay, Position: settings.overlayPos, Start: settings.overlayStart, End: settings.overlayEnd}
			scene.Overlay = o
		} else if len(settings.overlayGiven) > 0 {
			report.Discarded = append(report.Discarded, strings.Join(settings.overlayGiven, ", ")+": there is no Overlay line with the text")
		}
		if graphic != "" && settings.variants != nil {
			report.Discarded = append(report.Discarded, fmt.Sprintf("Variants %d: a graphic is made once", *settings.variants))
			settings.variants = nil
		}
		scene.Seed, scene.Variants = settings.seed, settings.variants
		if e.LoraInfo != "" {
			report.Discarded = append(report.Discarded,
				fmt.Sprintf("lora_info %q: LoRA is not active in v1, field ignored", e.LoraInfo))
		}
		ok = append(ok, parsed{entry: e, report: report, scene: scene, negative: negative, adult: settings.adult})
	}

	// The project's label: for adults when any entry says so, reviewed when some say no and none says yes.
	for _, p := range ok {
		if p.adult == nil {
			continue
		}
		if *p.adult {
			t := true
			res.Config.Adult = &t
			break
		}
		f := false
		res.Config.Adult = &f
	}

	// Negative placement: defaults only when every imported entry carries
	// the identical text; otherwise each scene keeps its own and defaults
	// stays empty (the pipeline concatenates defaults after scene-level
	// negatives, so mixed placement would double-apply the shared text).
	allSame := len(ok) > 0
	for _, p := range ok {
		if p.negative == "" || p.negative != ok[0].negative {
			allSame = false
			break
		}
	}
	for i := range ok {
		if allSame {
			ok[i].report.Mapped = append(ok[i].report.Mapped, "negative ← defaults.negative_prompt (shared by all entries)")
		} else if ok[i].negative != "" {
			ok[i].scene.NegativePrompt = ok[i].negative
			ok[i].report.Mapped = append(ok[i].report.Mapped, "negative ← scene.negative_prompt")
		}
	}
	if allSame {
		res.Config.Defaults.NegativePrompt = ok[0].negative
	}

	for _, p := range ok {
		res.Config.Scenes = append(res.Config.Scenes, p.scene)
		res.Reports = append(res.Reports, p.report)
		res.Imported++
	}
	return res, nil
}

// Validate runs the structural subset of the orchestrator's own validation
// over the generated config: the placeholder fields the dataset cannot
// supply (model, voice, output.file) are stubbed for the check only, so a
// failure here always means the scenes themselves are invalid.
func (r *Result) Validate() error {
	if r.music() {
		return r.validateMusic()
	}
	if r.models3d() {
		return r.validateModels3D()
	}
	check := *r.Config
	check.SkipBrowserCheck = true // the graphics are drawn where the project runs, not where it is imported
	check.SkipBlenderCheck = true
	check.Output = config.Output{Mode: config.ModeVideo, File: "out/placeholder.mp4"}
	check.Defaults.Model = ""
	check.Defaults.ModelSet = nil
	check.Defaults.Voice = ""
	// The Wan models are setup, like the checkpoint: the animated scenes are checked as scenes.
	check.Scenes = append([]config.Scene(nil), r.Config.Scenes...)
	for i := range check.Scenes {
		check.Scenes[i].Animate, check.Scenes[i].MotionPrompt = false, ""
	}
	// A 3D scene names a description of the sources, which the import does not have: an empty file stands for each one,
	// so the check says whether the scenes are valid and not whether the files came with them (the confirmation says that).
	dir, err := os.MkdirTemp("", "import-check-*")
	if err != nil {
		return check.Validate()
	}
	defer os.RemoveAll(dir)
	_ = os.MkdirAll(filepath.Join(dir, config.SourcesSubdir), 0o755)
	for _, sc := range check.Scenes {
		if sc.Blender != nil && sc.Blender.Build != "" && config.ValidSceneSpecName(sc.Blender.Build) {
			_ = os.WriteFile(filepath.Join(dir, config.SourcesSubdir, sc.Blender.Build), nil, 0o644)
		}
	}
	return check.ValidateInDir(dir)
}

// yamlScalar renders s as a safe single-line YAML scalar.
func yamlScalar(s string) string {
	b, _ := yaml.Marshal(s)
	return strings.TrimRight(string(b), "\n")
}

// writeAdult writes the project's label when the dataset gave one.
func (r *Result) writeAdult(b *strings.Builder) {
	if r.Config.Adult != nil {
		fmt.Fprintf(b, "adult: %t\n", *r.Config.Adult)
	}
}

// EmitYAML renders the generated scenes.yaml: real values for everything
// the dataset supplied, commented placeholders for the fields the user
// must fill in (model, voice, resolution, sampler, steps, fps, output.file).
func (r *Result) EmitYAML() []byte {
	if r.music() {
		return r.emitMusicYAML()
	}
	if r.models3d() {
		return r.emitModels3DYAML()
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Generated by `veritmo import`. Fill in the commented placeholders\n")
	fmt.Fprintf(&b, "# below (model, voice, resolution, sampler, steps, fps, output.file)\n")
	fmt.Fprintf(&b, "# before running the pipeline.\n\n")
	fmt.Fprintf(&b, "project: %s\n", yamlScalar(r.Config.Project))
	r.writeAdult(&b)
	fmt.Fprintf(&b, "\n")
	fmt.Fprintf(&b, "output:\n")
	fmt.Fprintf(&b, "  mode: video\n")
	fmt.Fprintf(&b, "  # file: out/%s.mp4\n\n", r.Config.Project)
	fmt.Fprintf(&b, "defaults:\n")
	fmt.Fprintf(&b, "  # model: models/<checkpoint>.safetensors\n")
	fmt.Fprintf(&b, "  # voice: voices/<voice>.onnx\n")
	fmt.Fprintf(&b, "  # resolution: 1280x720\n")
	fmt.Fprintf(&b, "  # sampler: euler\n")
	fmt.Fprintf(&b, "  # steps: 30\n")
	fmt.Fprintf(&b, "  # fps: 30\n")
	if r.Config.Defaults.NegativePrompt != "" {
		fmt.Fprintf(&b, "  negative_prompt: %s\n", yamlScalar(r.Config.Defaults.NegativePrompt))
	}
	if r.motion && r.Config.Defaults.SubjectPrompt != "" {
		fmt.Fprintf(&b, "  subject_prompt: %s\n", yamlScalar(r.Config.Defaults.SubjectPrompt))
	}
	for _, s := range r.Config.Scenes {
		if s.Animate {
			// The Wan models belong to the project, not to the dataset: name them in the file.
			fmt.Fprintf(&b, "  # animate:                    # needed by the scenes with animate: true\n")
			fmt.Fprintf(&b, "  #   diffusion_model: models/video/Wan2.2-TI2V-5B-Q8_0.gguf\n")
			fmt.Fprintf(&b, "  #   t5xxl: models/video/umt5-xxl-encoder-Q8_0.gguf\n")
			fmt.Fprintf(&b, "  #   vae: models/video/wan2.2_vae.safetensors\n")
			fmt.Fprintf(&b, "  #   frames: 33\n")
			break
		}
	}
	fmt.Fprintf(&b, "\nscenes:\n")
	for _, s := range r.Config.Scenes {
		fmt.Fprintf(&b, "  - id: %s\n", yamlScalar(s.ID))
		if s.MotionGraphic != "" {
			fmt.Fprintf(&b, "    motion_graphic: %s\n", yamlScalar(s.MotionGraphic))
		} else if s.MotionPart == "" && (s.Prompt != "" || s.Blender == nil) {
			fmt.Fprintf(&b, "    prompt: %s\n", yamlScalar(s.Prompt))
		}
		if s.Blender != nil {
			fmt.Fprintf(&b, "    blender:\n      build: %s\n", yamlScalar(s.Blender.Build))
		}
		if s.MotionPart != "" {
			fmt.Fprintf(&b, "    motion_part: %s\n", yamlScalar(s.MotionPart))
			if len(s.MotionParams) > 0 {
				fmt.Fprintf(&b, "    motion_params:\n")
				names := make([]string, 0, len(s.MotionParams))
				for k := range s.MotionParams {
					names = append(names, k)
				}
				sort.Strings(names)
				for _, k := range names {
					fmt.Fprintf(&b, "      %s: %s\n", k, paramScalar(s.MotionParams[k]))
				}
			}
			if s.MotionText != "" {
				fmt.Fprintf(&b, "    motion_text: %s\n", yamlScalar(s.MotionText))
			}
			if s.MotionBPM != nil {
				fmt.Fprintf(&b, "    motion_bpm: %v\n", *s.MotionBPM)
			}
		}
		if s.NegativePrompt != "" {
			fmt.Fprintf(&b, "    negative_prompt: %s\n", yamlScalar(s.NegativePrompt))
		}
		if s.Seed != nil {
			fmt.Fprintf(&b, "    seed: %d\n", *s.Seed)
		}
		if s.Variants != nil {
			fmt.Fprintf(&b, "    variants: %d\n", *s.Variants)
		}
		if s.SilenceSeconds != nil {
			fmt.Fprintf(&b, "    silence_seconds: %v\n", *s.SilenceSeconds)
		} else {
			fmt.Fprintf(&b, "    tts_text: %s\n", yamlScalar(s.TTSText))
			speed := "1.0"
			if s.Speed > 0 && s.Speed != 1 {
				speed = strconv.FormatFloat(s.Speed, 'f', -1, 64)
			}
			fmt.Fprintf(&b, "    speed: %s\n", speed)
		}
		if s.Animate {
			fmt.Fprintf(&b, "    animate: true\n")
			fmt.Fprintf(&b, "    motion_prompt: %s\n", yamlScalar(s.MotionPrompt))
		}
		if o := s.Overlay; o != nil {
			fmt.Fprintf(&b, "    overlay:\n      text: %s\n", yamlScalar(o.Text))
			if o.Position != "" {
				fmt.Fprintf(&b, "      position: %s\n", o.Position)
			}
			if o.Start != nil {
				fmt.Fprintf(&b, "      start: %v\n", *o.Start)
			}
			if o.End != nil {
				fmt.Fprintf(&b, "      end: %v\n", *o.End)
			}
		}
	}
	return []byte(b.String())
}

// WriteYAML validates and atomically writes the generated scenes.yaml,
// refusing to overwrite an existing file unless forceOverwrite is set.
// Nothing is written when validation fails.
func (r *Result) WriteYAML(path string, forceOverwrite bool) error {
	if err := r.Validate(); err != nil {
		return fmt.Errorf("import: generated config is invalid: %w", err)
	}
	if !forceOverwrite {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("import: output %s already exists (use --force-overwrite to replace it)", path)
		}
	}
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("import: mkdir %s: %w", dir, err)
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, r.EmitYAML(), 0o644); err != nil {
		return fmt.Errorf("import: write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("import: rename %s → %s: %w", tmp, path, err)
	}
	return nil
}

// Report renders the per-entry report plus the summary line, in dataset
// order.
func (r *Result) Report() string {
	var b strings.Builder
	for _, rep := range r.Reports {
		fmt.Fprintf(&b, "[%s]\n", rep.ID)
		if rep.Rejected != "" {
			fmt.Fprintf(&b, "  rejected: %s\n", rep.Rejected)
			continue
		}
		for _, m := range rep.Mapped {
			fmt.Fprintf(&b, "  mapped: %s\n", m)
		}
		for _, d := range rep.Discarded {
			fmt.Fprintf(&b, "  discarded: %s\n", d)
		}
	}
	if r.models3d() {
		fmt.Fprintf(&b, "imported %d models, rejected %d\n", r.Imported, r.Rejected)
	} else if r.music() {
		fmt.Fprintf(&b, "imported %d, instrumental %d, rejected %d\n", r.Imported, r.Instrumental, r.Rejected)
	} else if r.motion {
		fmt.Fprintf(&b, "imported %d graphics, silent %d, rejected %d\n", r.Imported, r.Silent, r.Rejected)
	} else {
		fmt.Fprintf(&b, "imported %d, silent %d, rejected %d\n", r.Imported, r.Silent, r.Rejected)
	}
	return b.String()
}

// isMotionPartMarker says whether a marker name belongs to the graphics made from parts.
func isMotionPartMarker(name string) bool {
	n := strings.Join(strings.Fields(strings.ToLower(name)), " ")
	return n == "part" || n == "text" || n == "bpm" || n == "design" || strings.HasPrefix(n, "param ")
}

// checkPartMarkers says why the part markers of an entry cannot be: parameters, text or tempo with no part.
func checkPartMarkers(s *sceneSettings) string {
	if s.part == "" {
		switch {
		case len(s.params) > 0:
			return "Param lines need a Part line"
		case s.text != "":
			return "Text needs a Part line"
		case s.bpm != nil:
			return "BPM needs a Part line"
		}
		return ""
	}
	if s.part == "auto" && len(s.params) > 0 {
		return "Param lines cannot be given with Part: auto: the model fills them in"
	}
	return ""
}

// applyPart puts the part of an entry in the scene, typing its parameters as the part declares them and checking the part
// and its limits as a scene is checked. It returns why the entry cannot be, or "".
func applyPart(parts config.PartCatalog, scene *config.Scene, s *sceneSettings) string {
	scene.MotionPart, scene.MotionText, scene.MotionBPM = s.part, s.text, s.bpm
	if len([]rune(s.text)) > config.MaxMotionText {
		return fmt.Sprintf("Text has %d characters, the most is %d", len([]rune(s.text)), config.MaxMotionText)
	}
	if s.part == "auto" {
		return ""
	}
	if len(s.params) > 0 {
		if parts == nil {
			return "this program cannot read the parameters of a part (the catalog of parts is not linked in)"
		}
		params, err := parts.Parse(s.part, s.params)
		if err != nil {
			return err.Error()
		}
		scene.MotionParams = params
	}
	if parts != nil {
		if err := parts.Check(s.part, scene.MotionParams); err != nil {
			return fmt.Sprintf("Part %q: %v", s.part, err)
		}
	}
	return ""
}

// paramScalar writes the value of a parameter as a YAML scalar.
func paramScalar(v any) string {
	switch t := v.(type) {
	case string:
		return yamlScalar(t)
	default:
		return fmt.Sprint(t)
	}
}
