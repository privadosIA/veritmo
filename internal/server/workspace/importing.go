package workspace

// importing.go implements the project-import capability: a report-first
// preview that drives the existing internal/project/importer logic (never a
// reimplementation) and an explicit confirm step that creates the project
// (see specs/project-import).

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"gopkg.in/yaml.v3"
	"veritmo/internal/project/config"
	"veritmo/internal/project/importer"
	"veritmo/internal/project/pipeline"
	"veritmo/internal/server/api"
)

type importEntryReport struct {
	ID        string   `json:"id"`
	Mapped    []string `json:"mapped"`
	Discarded []string `json:"discarded"`
	Rejected  string   `json:"rejected,omitempty"`
}

// POST /api/import/preview  {"content": "...", "project": "...", "silence_seconds": 2.0, "mode": "video|motion|music|models3d"}
func (h *Handler) handleImportPreview(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Content        string  `json:"content"`
		Project        string  `json:"project"`
		SilenceSeconds float64 `json:"silence_seconds"`
		Mode           string  `json:"mode"`
		// OutputMode is the mode of the project the scenes become: video (the default) or images.
		OutputMode config.OutputMode `json:"output_mode"`
		// Descriptions are the files of the 3D scenes the entries name with a Blender line, by name.
		Descriptions map[string]json.RawMessage `json:"descriptions"`
	}
	if !api.DecodeJSONLimited(w, r, &req, api.MaxImportBody) {
		return
	}
	if strings.TrimSpace(req.Content) == "" {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "content is empty")
		return
	}
	if req.Mode != "" && req.Mode != "video" && req.Mode != "music" && req.Mode != "motion" && req.Mode != "models3d" {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("mode must be video, motion, music or models3d, got %q", req.Mode))
		return
	}
	if err := checkOutputMode(req.OutputMode, req.Mode); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, err.Error())
		return
	}
	descriptions := importDescriptions{}
	for name, raw := range req.Descriptions {
		descriptions[name] = raw
	}
	if err := checkImportShape(descriptions); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, err.Error())
		return
	}
	if req.Project == "" {
		req.Project = "imported"
	}
	if req.SilenceSeconds == 0 {
		req.SilenceSeconds = 2.0 // the import subcommand's --silence-seconds default
	}

	entries, rejected, err := importer.LoadBytes([]byte(req.Content))
	if err != nil {
		api.WriteError(w, http.StatusUnprocessableEntity, api.CodeValidation, err.Error())
		return
	}
	mapper := importer.Map
	switch req.Mode {
	case "music":
		mapper = importer.MapMusic
	case "motion":
		mapper = importer.MapMotion
	case "models3d":
		mapper = importer.MapModels3D
	}
	res, err := mapper(entries, rejected, importer.Options{
		Project:        req.Project,
		SilenceSeconds: req.SilenceSeconds,
		Parts:          h.Parts(),
	})
	if err != nil {
		api.WriteError(w, http.StatusUnprocessableEntity, api.CodeValidation, err.Error())
		return
	}

	if req.Mode == "models3d" && refuseAdultModels(w, h.host, res.Config) {
		return // a server that hides adult content does not take a dataset that asks for it
	}
	reports := make([]importEntryReport, 0, len(res.Reports))
	for _, rep := range res.Reports {
		reports = append(reports, importEntryReport{
			ID:        rep.ID,
			Mapped:    rep.Mapped,
			Discarded: rep.Discarded,
			Rejected:  rep.Rejected,
		})
	}

	out := h.fillImportPlaceholders(res.EmitYAML())
	if req.OutputMode == config.ModeImages {
		out = bytes.Replace(out, []byte("\n  mode: video\n"), []byte("\n  mode: images\n"), 1)
	}
	warnings := []string{}
	if req.Mode == "models3d" {
		for _, rep := range res.Reports {
			if rep.Rejected == "" {
				for _, d := range rep.Discarded {
					warnings = append(warnings, fmt.Sprintf("%s: %s", rep.ID, d))
				}
			}
		}
	}
	if req.Mode != "music" && req.Mode != "models3d" {
		broken, unused, missing := descriptions.problems(res.Config, pipeline.BlenderEnv(res.Config, ""))
		for _, name := range missing {
			warnings = append(warnings, fmt.Sprintf("a scene names the 3D description %s, which is not in descriptions: the project cannot be created without it", name))
		}
		for _, name := range unused {
			warnings = append(warnings, fmt.Sprintf("the description %s is not named by any scene: the confirmation refuses it", name))
		}
		for _, b := range broken {
			warnings = append(warnings, "the 3D description does not pass the checks: "+b)
		}
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{
		"warnings":     warnings,
		"yaml":         string(out),
		"report":       res.Report(),
		"entries":      reports,
		"imported":     res.Imported,
		"silent":       res.Silent,
		"instrumental": res.Instrumental,
		"rejected":     res.Rejected,
	})
}

// fillImportPlaceholders turns the generator's commented placeholders
// into working values the server actually knows: output.file and the
// standard defaults are uncommented, and model/voice are filled from the
// shared inventory when one exists. The result is a project that passes
// validation as-is instead of a file the user must edit first.
func (h *Handler) fillImportPlaceholders(y []byte) []byte {
	out := string(y)
	for _, unc := range []string{"file: out/", "resolution: ", "sampler: ", "steps: ", "fps: "} {
		out = strings.Replace(out, "\n  # "+unc, "\n  "+unc, 1)
	}
	if model := h.FirstAsset("models"); model != "" {
		out = strings.Replace(out, "\n  # model: models/<checkpoint>.safetensors", "\n  model: "+model, 1)
	}
	if voice := h.FirstAsset("voices"); voice != "" {
		out = strings.Replace(out, "\n  # voice: voices/<voice>.onnx", "\n  voice: "+voice, 1)
	}
	return []byte(out)
}

// POST /api/import/confirm  {"id": "...", "yaml": "..."}
func (h *Handler) handleImportConfirm(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID   string `json:"id"`
		YAML string `json:"yaml"`
		// OutputMode, when given, replaces the output.mode of the YAML: video or images.
		OutputMode config.OutputMode `json:"output_mode"`
		// Descriptions are the files of the 3D scenes the YAML names with blender.build, by name; they are written to the
		// project's sources before it is validated.
		Descriptions map[string]json.RawMessage `json:"descriptions"`
	}
	if !api.DecodeJSONLimited(w, r, &req, api.MaxConfigBody+maxImportDescriptionBytes) {
		return
	}
	if !api.ProjectIDPattern.MatchString(req.ID) {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid project id %q", req.ID))
		return
	}
	if h.ProjectExists(req.ID) || api.DirExists(h.ProjectDir(req.ID)) {
		api.WriteError(w, http.StatusConflict, api.CodeConflict, fmt.Sprintf("project %q already exists", req.ID))
		return
	}
	if err := checkOutputMode(req.OutputMode, ""); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, err.Error())
		return
	}
	if req.OutputMode != "" {
		y, err := setOutputMode([]byte(req.YAML), req.OutputMode)
		if err != nil {
			api.WriteError(w, http.StatusUnprocessableEntity, api.CodeValidation, err.Error())
			return
		}
		req.YAML = string(y)
	}
	cfg, err := config.LoadBytes([]byte(req.YAML))
	if err != nil {
		api.WriteError(w, http.StatusUnprocessableEntity, api.CodeValidation, err.Error())
		return
	}
	if h.host.RefuseAdultConfig(w, cfg.Adult) {
		return
	}
	if refuseAdultModels(w, h.host, cfg) {
		return
	}
	if len(req.YAML) > api.MaxConfigBody {
		api.WriteError(w, http.StatusRequestEntityTooLarge, api.CodeInvalid, fmt.Sprintf("the yaml is larger than %d MB", api.MaxConfigBody>>20))
		return
	}
	descriptions := importDescriptions{}
	for name, raw := range req.Descriptions {
		descriptions[name] = raw
	}
	if err := checkImportShape(descriptions); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, err.Error())
		return
	}
	// The files of the 3D scenes are checked as a hand-written one is, and no file may be there that no scene names; a scene
	// whose file is not given is refused by the validation below, which looks for it in the sources.
	broken, unused, _ := descriptions.problems(cfg, pipeline.BlenderEnv(cfg, ""))
	if len(broken)+len(unused) > 0 {
		msgs := append([]string{}, broken...)
		for _, name := range unused {
			msgs = append(msgs, fmt.Sprintf("%s: no scene names this description", name))
		}
		api.WriteError(w, http.StatusUnprocessableEntity, api.CodeValidation, "descriptions: "+strings.Join(msgs, "; "))
		return
	}
	// Materialize first (the asset symlinks must exist for the
	// file-existence checks), then validate in place; roll back on
	// failure so no invalid project is left behind.
	if err := h.materializeProject(req.ID, []byte(req.YAML)); err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	for name, raw := range descriptions {
		if err := h.storeDescription(req.ID, name, indented(raw)); err != nil {
			_ = h.removeProjectFiles(req.ID)
			api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, fmt.Sprintf("cannot save the description %s: %v", name, err))
			return
		}
	}
	if err := h.validateInDir(cfg, h.ProjectDir(req.ID)); err != nil {
		_ = h.removeProjectFiles(req.ID)
		api.WriteError(w, http.StatusUnprocessableEntity, api.CodeValidation, err.Error())
		return
	}
	api.WriteJSON(w, http.StatusCreated, map[string]string{"id": req.ID})
}

// refuseAdultModels answers the 422 of an import that would make 3D models marked for adults on a server that does not keep them, and says whether it did.
// An import of the other modes is refused by its project's `adult` label; the entries of a models3d project carry their own.
func refuseAdultModels(w http.ResponseWriter, host Host, cfg *config.Config) bool {
	if cfg == nil || cfg.Output.Mode != config.ModeModels3D {
		return false
	}
	for _, m := range cfg.Models {
		if m.Adult {
			yes := true
			return host.RefuseAdultConfig(w, &yes)
		}
	}
	return false
}

// checkOutputMode accepts "", video and images; images only makes sense for a scenes dataset (not music, motion or models3d).
func checkOutputMode(outputMode config.OutputMode, importMode string) error {
	switch outputMode {
	case "", config.ModeVideo:
		return nil
	case config.ModeImages:
		if importMode == "music" || importMode == "motion" || importMode == "models3d" {
			return fmt.Errorf("output_mode images only applies to a scenes dataset, not to mode %q", importMode)
		}
		return nil
	}
	return fmt.Errorf("output_mode must be video or images, got %q", outputMode)
}

// setOutputMode sets output.mode in a project file keeping the rest of it (comments included).
func setOutputMode(src []byte, mode config.OutputMode) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return nil, err
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("the project file is not a YAML mapping")
	}
	get := func(m *yaml.Node, key string) *yaml.Node {
		for i := 0; i+1 < len(m.Content); i += 2 {
			if m.Content[i].Value == key {
				return m.Content[i+1]
			}
		}
		return nil
	}
	root := doc.Content[0]
	out := get(root, "output")
	if out == nil {
		out = &yaml.Node{Kind: yaml.MappingNode}
		root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: "output"}, out)
	}
	if out.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("output must be a mapping")
	}
	if m := get(out, "mode"); m != nil {
		m.Kind, m.Tag, m.Value = yaml.ScalarNode, "!!str", string(mode)
	} else {
		out.Content = append(out.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: "mode"}, &yaml.Node{Kind: yaml.ScalarNode, Value: string(mode)})
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
