package music

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"veritmo/internal/assistant/assist"
	"veritmo/internal/project/config"
	"veritmo/internal/project/importer"
	"veritmo/internal/server/api"
	"veritmo/internal/server/assistant"
	"veritmo/internal/video/motion"
)

// DatasetRequest is the body of POST /api/assist/dataset.
type DatasetRequest struct {
	Mode     string `json:"mode"`
	Text     string `json:"text"`
	Language string `json:"language,omitempty"`
	// Engine is the video generator the scenes are for; Negative is written as the shared Negative: line of every scene;
	// Look is a phrase of style added to every picture; Images.Variants adds Variants: to every scene.
	Engine   string `json:"engine,omitempty"`
	Negative string `json:"negative,omitempty"`
	Look     string `json:"look,omitempty"`
	Images   *struct {
		Variants int `json:"variants"`
	} `json:"images,omitempty"`
	// Generator is the music generator whose vocabulary the tracks' tags use.
	Generator string           `json:"generator,omitempty"`
	Examples  []assist.Example `json:"examples,omitempty"`
	Model     string           `json:"model,omitempty"`
	Provider  string           `json:"provider,omitempty"`
	// Picture says how the pictures of a storyboard are made: "blender" makes every scene a 3D scene whose description the
	// server writes (video mode only). Project, with it, is an existing project whose design, tempo and frame the
	// descriptions are written for.
	Picture string `json:"picture,omitempty"`
	Project string `json:"project,omitempty"`
}

// DatasetAnswer is the answer of POST /api/assist/dataset: tuples in the shape POST /api/import/preview reads.
type DatasetAnswer struct {
	Entries  [][4]string `json:"entries"`
	Mode     string      `json:"mode"`
	Warnings []string    `json:"warnings"`
	// Descriptions are the 3D scenes the entries with a Blender line name, by file name: the import takes them in its own
	// descriptions.
	Descriptions map[string]json.RawMessage `json:"descriptions,omitempty"`
}

// maxDatasetTracks bounds the music mode: each track is one more call of the model (its lyrics and tags).
const maxDatasetTracks = 6

// maxDatasetScenes bounds the 3D scenes of a storyboard: each is up to three more calls of the model and a check.
const maxDatasetScenes = 6

// pictureBlender is the value of `picture` that makes the scenes 3D.
const pictureBlender = "blender"

var (
	datasetIDRe     = regexp.MustCompile(`[^a-z0-9_-]+`)
	datasetMarkerRe = regexp.MustCompile(`(?i)^(dialogue(\s*\([^)]*\))?|motion|negative|variants|seed|speed|silence|duration|adult|graphic|part|text|bpm|design|blender|param\s+\S+|overlay.*)\s*:`)
	// datasetLabelRe is a label the model puts before the picture line ("Picture: ...", "**Image:** ..."): it is not part of the prompt.
	datasetLabelRe  = regexp.MustCompile(`(?i)^[\s*_]*(picture|image|prompt|scene|shot|visual|description)[\s*_]*:[\s*_]*`)
	datasetSharedRe = regexp.MustCompile(`(?i)^(negative|variants)\s*:`)
	// datasetBlenderRe are the lines a 3D scene does not have: the server writes its own Blender line, and the scene's
	// description says how it moves.
	datasetBlenderRe = regexp.MustCompile(`(?i)^(motion|blender)\s*:`)
	// models3dMarkerRe are the settings lines of the import of 3D models.
	models3dMarkerRe = regexp.MustCompile(`(?i)^(engine|detail|variants|seed|image|adult)\s*:`)
	datasetSilenceRe = regexp.MustCompile(`(?i)^(?:silence|duration)\s*:\s*([0-9.]+)`)
	datasetDialogue  = regexp.MustCompile(`(?i)^(dialogue(?:\s*\([^)]*\))?)\s*:\s*(.*)$`)
)

// POST /api/assist/dataset {"mode", "text", ...} -> DatasetAnswer
//
// A dataset of several entries from one sentence, written in the lines POST /api/import/preview reads: a storyboard
// (video), animated graphics from the catalog's parts (motion) or songs (music). The model writes the entries; the
// shared lines come from the request; every entry is checked with the import's own mapping before it is returned, and
// one that fails is left out with a warning. It has the guards of the plan: routing and consent, the wait for a run in
// progress, the memory rule, the model's own refusal.
func (h *Handler) handleAssistDataset(w http.ResponseWriter, r *http.Request) {
	var req DatasetRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
		return
	}
	bad := func(msg string) { api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, msg) }
	switch {
	case req.Mode != assist.DatasetVideo && req.Mode != assist.DatasetMotion && req.Mode != assist.DatasetMusic && req.Mode != assist.DatasetModels3D:
		bad(fmt.Sprintf("mode must be video, motion, music or models3d, got %q", req.Mode))
		return
	case req.Mode == assist.DatasetModels3D && (req.Picture != "" || req.Project != ""):
		bad("picture and project do not apply to mode models3d: a 3D model is made from the prompt of one object")
		return
	case req.Language != "" && !api.LanguagePattern.MatchString(req.Language):
		bad("language must be an ISO 639-1 code such as es or en")
		return
	case len(req.Negative) > 500 || len(req.Look) > 200 || len(req.Engine) > 40 || len(req.Generator) > 40:
		bad("negative is limited to 500 characters, look to 200, engine and generator to 40")
		return
	case req.Images != nil && (req.Images.Variants < 1 || req.Images.Variants > 16):
		bad("images.variants must be between 1 and 16")
		return
	case req.Picture != "" && req.Picture != pictureBlender:
		bad(fmt.Sprintf("picture must be %q, got %q", pictureBlender, req.Picture))
		return
	case req.Picture == pictureBlender && req.Mode == assist.DatasetMusic:
		bad("picture blender needs mode video or motion")
		return
	case req.Project != "" && (req.Picture != pictureBlender || !api.ProjectIDPattern.MatchString(req.Project)):
		bad("project is the id of an existing project and goes with picture blender")
		return
	}
	if req.Project != "" && !h.ws.ProjectExists(req.Project) {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("project %q not found", req.Project))
		return
	}
	if err := assist.ValidateText(req.Text); err != nil {
		bad(err.Error())
		return
	}
	if err := assist.ValidateExamples(req.Examples); err != nil {
		bad(err.Error())
		return
	}
	ctx, ok := h.assistant.Gate(w, r, strings.TrimSpace(req.Provider))
	if !ok {
		return
	}
	ctx = assist.WithExamples(ctx, req.Examples)
	// The model that writes the 3D descriptions is taken before the storyboard is asked for, so a refusal costs no call.
	var sceneCtx context.Context
	var sceneWriter motion.Writer
	if req.Picture == pictureBlender {
		var ok bool
		if sceneCtx, sceneWriter, _, ok = h.ws.SceneWriter(w, r, strings.TrimSpace(req.Provider)); !ok {
			return
		}
	}

	in := assist.DatasetInput{Mode: req.Mode, Text: strings.TrimSpace(req.Text), Language: req.Language,
		Engine: strings.TrimSpace(req.Engine), Look: strings.TrimSpace(req.Look), Picture: req.Picture}
	if req.Mode == assist.DatasetMotion && req.Picture != pictureBlender { // 3D graphics name no part
		for _, p := range h.mmCatalog().List() {
			in.Parts = append(in.Parts, assist.PlanPart{Name: p.Name, Description: p.Description})
		}
	}
	run := h.assistant.AssistDatasetRun
	if run == nil {
		run = assistant.DefaultAssistDataset
	}
	written, err := run(ctx, in, req.Model)
	if err != nil {
		api.WriteAssistError(w, err)
		return
	}

	ans := DatasetAnswer{Entries: [][4]string{}, Mode: req.Mode, Warnings: []string{}}
	warn := func(format string, a ...any) { ans.Warnings = append(ans.Warnings, fmt.Sprintf(format, a...)) }
	if len(written) > assist.MaxDatasetEntries {
		warn("the model wrote %d entries: only the first %d are kept", len(written), assist.MaxDatasetEntries)
		written = written[:assist.MaxDatasetEntries]
	}
	if req.Picture == pictureBlender && len(written) > maxDatasetScenes {
		warn("%d 3D scenes were written: only the first %d are kept", len(written), maxDatasetScenes)
		written = written[:maxDatasetScenes]
	}
	if req.Mode == assist.DatasetMusic && len(written) > maxDatasetTracks {
		warn("%d tracks were asked: only the first %d are written", len(written), maxDatasetTracks)
		written = written[:maxDatasetTracks]
	}

	seen := map[string]bool{}
	descriptions := map[string]json.RawMessage{}
	var cand [][4]string
	for i, e := range written {
		id := datasetIDRe.ReplaceAllString(strings.ToLower(strings.TrimSpace(e.ID)), "")
		if len(id) > 24 {
			id = id[:24]
		}
		if id == "" || seen[id] {
			if id != "" {
				warn("entry %d repeated the id %q: it was renamed", i+1, id)
			}
			id = fmt.Sprintf("s%02d", i+1)
			for seen[id] {
				id += "x"
			}
		}
		seen[id] = true
		title := strings.TrimSpace(e.Title)
		if r := []rune(title); len(r) > 80 {
			title = string(r[:80])
		}
		body, ok := h.datasetBody(ctx, &req, e, i, warn)
		if !ok {
			if ctx.Err() != nil {
				return
			}
			continue
		}
		if req.Picture == pictureBlender {
			name := id + config.SceneSpecSuffix
			raw, ok := h.datasetScene(sceneCtx, sceneWriter, &req, id, body, warn)
			if !ok {
				if sceneCtx.Err() != nil {
					return
				}
				continue
			}
			descriptions[name] = raw
			body += "\nBlender: " + name
		}
		cand = append(cand, [4]string{id, title, "", body})
	}

	// What comes back must preview: the import's own mapping decides which entries are usable.
	raw, _ := json.Marshal(cand)
	entries, rej, err := importer.LoadBytes(raw)
	if err != nil {
		api.WriteError(w, http.StatusUnprocessableEntity, api.CodeValidation, err.Error())
		return
	}
	mapper := importer.Map
	switch req.Mode {
	case assist.DatasetMotion:
		mapper = importer.MapMotion
	case assist.DatasetMusic:
		mapper = importer.MapMusic
	case assist.DatasetModels3D:
		mapper = importer.MapModels3D
	}
	res, err := mapper(entries, rej, importer.Options{Project: "dataset", SilenceSeconds: 2, Parts: h.ws.Parts()})
	if err != nil {
		api.WriteError(w, http.StatusUnprocessableEntity, api.CodeValidation, err.Error())
		return
	}
	refused := map[string]string{}
	for _, rep := range res.Reports {
		if rep.Rejected != "" {
			refused[rep.ID] = rep.Rejected
		}
	}
	for _, c := range cand {
		if why, no := refused[c[0]]; no {
			warn("entry %q was left out: %s", c[0], why)
			continue
		}
		ans.Entries = append(ans.Entries, c)
		if raw, ok := descriptions[c[0]+config.SceneSpecSuffix]; ok {
			if ans.Descriptions == nil {
				ans.Descriptions = map[string]json.RawMessage{}
			}
			ans.Descriptions[c[0]+config.SceneSpecSuffix] = raw
		}
	}
	if len(ans.Entries) == 0 {
		api.WriteError(w, http.StatusUnprocessableEntity, api.CodeValidation, "the model wrote no usable entry: "+strings.Join(ans.Warnings, "; "))
		return
	}
	api.WriteJSON(w, http.StatusOK, ans)
}

// datasetBody is the text of one entry: what the model wrote plus the lines the request fixes. ok is false when the
// entry could not be completed (a warning says why).
func (h *Handler) datasetBody(ctx context.Context, req *DatasetRequest, e assist.DatasetEntry, i int, warn func(string, ...any)) (string, bool) {
	var lines []string
	for _, l := range strings.Split(e.Body, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	switch req.Mode {
	case assist.DatasetMusic:
		return h.datasetTrack(ctx, req, lines, i, warn)
	case assist.DatasetModels3D:
		return h.datasetModel(ctx, req, lines, i, warn)
	case assist.DatasetMotion:
		if req.Picture == pictureBlender {
			return h.datasetGraphic3D(lines, i, warn)
		}
		return strings.Join(lines, "\n"), true
	}
	// video: the request's lines replace any the model wrote; the dialogue is quoted; the look ends the picture line.
	var out []string
	last := -1
	blender := req.Picture == pictureBlender
	for _, l := range lines {
		if datasetSharedRe.MatchString(l) || (blender && datasetBlenderRe.MatchString(l)) {
			continue
		}
		if m := datasetDialogue.FindStringSubmatch(l); m != nil {
			v := strings.TrimSpace(m[2])
			if !strings.HasPrefix(v, "\"") {
				v = "\"" + strings.Trim(v, "\"“”") + "\""
			}
			l = m[1] + ": " + v
		}
		if !datasetMarkerRe.MatchString(l) {
			if c := strings.TrimSpace(datasetLabelRe.ReplaceAllString(l, "")); c != "" {
				l = c
			}
			last = len(out)
		}
		out = append(out, l)
	}
	if last < 0 {
		warn("scene %d has no picture line: it was left out", i+1)
		return "", false
	}
	if look := strings.TrimSpace(req.Look); look != "" && !strings.Contains(strings.ToLower(out[last]), strings.ToLower(look)) {
		out[last] = strings.TrimRight(out[last], " .,") + ", " + look
	}
	if blender {
		return strings.Join(out, "\n"), true // a 3D scene has no negative prompt and no variants
	}
	if n := strings.TrimSpace(req.Negative); n != "" {
		out = append(out, "Negative: "+n)
	}
	if req.Images != nil {
		out = append(out, fmt.Sprintf("Variants: %d", req.Images.Variants))
	}
	return strings.Join(out, "\n"), true
}

// datasetGraphic3D is the entry of a 3D graphic of the motion mode: the line that says what is built and its Duration. The lines of a
// graphic made from a part, and a Blender line the model wrote, are not kept (the server writes its own).
func (h *Handler) datasetGraphic3D(lines []string, i int, warn func(string, ...any)) (string, bool) {
	var out []string
	idea, duration := "", ""
	for _, l := range lines {
		switch {
		case datasetBlenderRe.MatchString(l) || regexp.MustCompile(`(?i)^(part|graphic|text|bpm|design|param\s+\S+)\s*:`).MatchString(l):
		case datasetSilenceRe.MatchString(l):
			duration = l
		case !datasetMarkerRe.MatchString(l):
			idea = strings.TrimSpace(datasetLabelRe.ReplaceAllString(l, ""))
		}
	}
	if idea == "" {
		warn("graphic %d has no line that says what is built: it was left out", i+1)
		return "", false
	}
	out = append(out, idea)
	if duration != "" {
		out = append(out, duration)
	}
	return strings.Join(out, "\n"), true
}

// The seconds of a 3D scene whose length the entry does not give: five, or what its narration takes at about 2.5 words a
// second, between three and twelve.
const (
	datasetSceneDefault = 5.0
	datasetSceneMin     = 3.0
	datasetSceneMax     = 12.0
)

// datasetScene writes the 3D description of one entry from its picture line, with the writer and checks of the
// descriptions of a project. ok is false when it could not (a warning says why).
func (h *Handler) datasetScene(ctx context.Context, wr motion.Writer, req *DatasetRequest, id, body string, warn func(string, ...any)) (json.RawMessage, bool) {
	idea, seconds := "", datasetSceneDefault
	for _, l := range strings.Split(body, "\n") {
		if m := datasetSilenceRe.FindStringSubmatch(l); m != nil {
			if f, err := strconv.ParseFloat(m[1], 64); err == nil && f > 0 {
				seconds = math.Min(math.Max(f, 0.5), 30) // the length the entry says is the one it is written for
			}
		} else if m := datasetDialogue.FindStringSubmatch(l); m != nil {
			seconds = math.Min(math.Max(float64(len(strings.Fields(m[2])))/2.5, datasetSceneMin), datasetSceneMax)
		} else if !datasetMarkerRe.MatchString(l) {
			idea = l
		}
	}
	if idea == "" {
		warn("scene %q has no picture line to write its 3D description from: it was left out", id)
		return nil, false
	}
	res, err := h.ws.DescribeSceneFor(ctx, wr, req.Project, idea, seconds)
	if err != nil {
		warn("scene %q was left out: its 3D description was not written: %v", id, err)
		return nil, false
	}
	return json.RawMessage(bytes.TrimSpace(res.Raw)), true
}

// notOneObjectRe finds what a prompt of one object alone does not have: people, a crowd, a landscape or a scene. It is a cheap guard of the obvious cases; the
// engine's result is what says whether a prompt makes a good object.
var notOneObjectRe = regexp.MustCompile(`(?i)\b(people|person|persons|man|men|woman|women|boy|girl|child|children|kids?|crowd|audience|family|couple|spectators?|landscape|cityscape|scene)\b`)

// datasetModelMarkerRe are the settings lines of a 3D model entry the writer may keep: the others are the request's or the person's to give.
var datasetModelMarkerRe = regexp.MustCompile(`(?i)^(engine|detail|variants)\s*:\s*\S`)

// datasetModel is the entry of a 3D model: the prompt of one object alone and the few settings the request asked for. A prompt that describes a scene or
// people is written again once, asking for one object; if it still does, the entry is left out.
func (h *Handler) datasetModel(ctx context.Context, req *DatasetRequest, lines []string, i int, warn func(string, ...any)) (string, bool) {
	var prompt []string
	var settings []string
	for _, l := range lines {
		switch {
		case datasetModelMarkerRe.MatchString(l):
			settings = append(settings, l)
		case datasetMarkerRe.MatchString(l) || models3dMarkerRe.MatchString(l):
			// a line of another mode, or a setting that is the person's (Seed, Image, Adult): not the writer's to give
		default:
			if c := strings.TrimSpace(datasetLabelRe.ReplaceAllString(l, "")); c != "" {
				prompt = append(prompt, c)
			}
		}
	}
	text := strings.Join(strings.Fields(strings.Join(prompt, " ")), " ")
	if text == "" {
		warn("model %d has no prompt: it was left out", i+1)
		return "", false
	}
	if bad := notOneObjectRe.FindString(text); bad != "" {
		run := h.assistant.AssistRun
		if run == nil {
			run = assistant.DefaultAssistRun
		}
		again, err := run(ctx, assist.TaskModel3D, "One object alone, not a scene and with no people. It was written as: "+text, req.Model)
		again = strings.Join(strings.Fields(again), " ")
		switch {
		case err != nil:
			if ctx.Err() == nil {
				warn("model %d described %q and could not be written again as one object: it was left out", i+1, bad)
			}
			return "", false
		case again == "" || notOneObjectRe.MatchString(again):
			warn("model %d is not one object alone (it still names %q after writing it again): it was left out", i+1, notOneObjectRe.FindString(text+" "+again))
			return "", false
		}
		warn("model %d described %q: it was written again as one object", i+1, bad)
		text = again
	}
	return strings.Join(append([]string{text}, settings...), "\n"), true
}

// datasetTrack turns the brief of a song into its entry with the music task: caption, lyrics and the generator's tags.
func (h *Handler) datasetTrack(ctx context.Context, req *DatasetRequest, lines []string, i int, warn func(string, ...any)) (string, bool) {
	instrumental := false
	var brief []string
	for _, l := range lines {
		if m := regexp.MustCompile(`(?i)^instrumental\s*:\s*(\w+)`).FindStringSubmatch(l); m != nil {
			instrumental = strings.HasPrefix(strings.ToLower(m[1]), "y")
			continue
		}
		brief = append(brief, l)
	}
	if len(brief) == 0 {
		warn("track %d has no description: it was left out", i+1)
		return "", false
	}
	text := strings.Join(brief, " ")
	musicRun := h.assistant.AssistMusicRun
	if musicRun == nil {
		musicRun = assistant.DefaultAssistMusic
	}
	out, err := musicRun(ctx, text, assist.MusicOptions{Instrumental: instrumental, Engine: req.Generator}, req.Model)
	if err != nil {
		if ctx.Err() == nil {
			warn("track %d could not be written: %s", i+1, err)
		}
		return "", false
	}
	b := []string{strings.TrimSpace(out.Caption)}
	if b[0] == "" {
		b[0] = text
	}
	if instrumental {
		b = append(b, "Lyrics: [Instrumental]")
	} else if l := strings.TrimSpace(out.Lyrics); l != "" {
		b = append(b, "Lyrics:\n"+l)
	}
	if tags := string(config.NormalizeTags(strings.Trim(strings.TrimSpace(out.Tags), ".,"))); tags != "" {
		b = append(b, "Tags: "+tags)
	}
	return strings.Join(b, "\n"), true
}
