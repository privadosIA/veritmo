package server

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"veritmo/internal/server/models"

	"gopkg.in/yaml.v3"

	"veritmo/internal/music/clap"
	"veritmo/internal/music/hyperframes"
	"veritmo/internal/music/motionref"
	"veritmo/internal/music/musicmotion"
	"veritmo/internal/music/songmap"
	"veritmo/internal/platform/musicsvc"
	"veritmo/internal/platform/store"
	"veritmo/internal/server/api"
	"veritmo/internal/server/assistant"
	"veritmo/internal/server/music"
	"veritmo/internal/server/workspace"
	"veritmo/internal/video/videoedit"
)

// The description (openapi.yaml) and the Go structures that build the answers must agree on which fields exist and
// which are always sent (spec: api-documentation, "The description follows the structures that build the answers").
// A field is required exactly when its structure never omits it (no `omitempty`); a pointer that is always present can
// be null, so its schema says `nullable`. The comparison is by reflection, so it needs no sample data and cannot take
// a sparse sample for proof that a field is optional.

// structCase ties a schema (a name of components.schemas, or a path inside one) to the Go value that builds it.
type structCase struct {
	schema string // "Run", or "Routing/properties/routing/additionalProperties"
	value  any
	// allOptional marks a request body whose fields may all be left out: OpenAPI 3.0 has no empty `required`, so the
	// description says it in words and must carry no `required` at all.
	allOptional bool
}

// monitoringTools is the answer of GET /api/settings/monitoring, which the handler builds as a map.
type monitoringTools struct {
	Tools []monitoringView `json:"tools"`
}

var structCases = []structCase{
	{schema: "ProjectSummary", value: projectSummary{}},
	{schema: "VideoEditRequest", value: videoEditRequest{}},
	{schema: "VideoEdit", value: videoEditView{}},
	{schema: "VideoEditResult", value: videoEditResult{}},
	{schema: "ModelGeneration", value: modelGenerationView{}},
	{schema: "ModelGenerationRequest", value: modelGenerationRequest{}, allOptional: true},
	{schema: "VideoEditTool", value: videoEditToolView{}},
	{schema: "VideoEditSetting", value: videoedit.Setting{}},
	{schema: "Motion", value: libraryMotionView{}},
	{schema: "MotionRequest", value: libraryMotionRequest{}, allOptional: true},
	{schema: "MusicPiecePatch", value: music.PiecePatch{}, allOptional: true},
	{schema: "DataStatus", value: store.Status{}},
	{schema: "DataMigration", value: store.AppliedMigration{}},
	{schema: "DataTableRows", value: store.TableRows{}},
	{schema: "DataImportRecord", value: store.ImportRecord{}},
	{schema: "DataLegacyFiles", value: store.LegacyStatus{}},
	{schema: "DataImportRequest", value: workspace.DataImportRequest{}, allOptional: true},
	{schema: "DataCleanRequest", value: workspace.DataCleanRequest{}},
	{schema: "MusicMotionCatalog", value: pieceCatalog{}},
	{schema: "MusicPart", value: catalogPart{}},
	{schema: "MusicPartOrigin", value: musicmotion.PartOrigin{}},
	{schema: "PartDraftReview", value: musicmotion.Review{}, allOptional: true},
	{schema: "MusicTheme", value: musicmotion.Theme{}, allOptional: true},
	{schema: "MusicThemeHud", value: musicmotion.HUD{}, allOptional: true},
	{schema: "MusicParam", value: catalogParam{}},
	{schema: "MusicCharacter", value: catalogCharacter{}},
	{schema: "MusicBackground", value: catalogBackground{}},
	{schema: "MusicPieceRequest", value: pieceRequest{}},
	{schema: "MotionReference", value: motionref.Reference{}},
	{schema: "MotionPreset", value: musicmotion.Preset{}},
	{schema: "PartDraft", value: musicmotion.Draft{}},
	{schema: "PartDraftRequest", value: draftRequest{}, allOptional: true},
	{schema: "Learning", value: learningView{}},
	{schema: "LearnedItem", value: learnedView{}},
	{schema: "LearnRequest", value: learnRequest{}, allOptional: true},
	{schema: "CtxpressSettings", value: ctxpressView{}},
	{schema: "CtxpressSettingsRequest", value: ctxpressPutRequest{}, allOptional: true},
	{schema: "CtxpressTestRequest", value: ctxpressTestRequest{}, allOptional: true},
	{schema: "CtxpressTestResult", value: ctxpressTestResult{}},
	{schema: "HFBlock", value: music.HFBlock{}},
	{schema: "HFOrigin", value: hyperframes.Origin{}},
	{schema: "HFBlockList", value: music.HFBlockList{}},
	{schema: "HFImportBody", value: music.HFImportBody{}},
	{schema: "HFRenderBody", value: music.HFRenderBody{}, allOptional: true},
	{schema: "HFClip", value: music.HFClip{}},
	{schema: "LearnSimilar", value: musicmotion.Similar{}},
	{schema: "LearnDiscarded", value: motionref.Discarded{}},
	{schema: "LearnSkipped", value: motionref.Skipped{}},
	{schema: "PartDraftPut", value: draftPut{}},
	{schema: "MotionPresetRequest", value: presetRequest{}, allOptional: true},
	{schema: "MotionPresetPut", value: presetPut{}},
	{schema: "MotionAnalysis", value: motionref.Analysis{}},
	{schema: "MotionColour", value: motionref.Colour{}},
	{schema: "MotionReferenceRequest", value: assistant.ReferenceRequest{}, allOptional: true},
	{schema: "MotionReferencePatch", value: assistant.ReferencePatch{}, allOptional: true},
	{schema: "MotionBreakdownRequest", value: assistant.BreakdownRequest{}, allOptional: true},
	{schema: "MusicPiece", value: pieceView{}},
	{schema: "MusicPiece/properties/source", value: pieceSource{}},
	{schema: "MusicEstimate", value: musicmotion.Estimate{}},
	{schema: "MusicDesign", value: musicmotion.Design{}},
	{schema: "MusicDesignStored", value: musicmotion.Stored{}},
	{schema: "MusicScript", value: musicmotion.Script{}},
	{schema: "MusicScriptPiece", value: musicmotion.Piece{}},
	{schema: "MusicScriptLine", value: musicmotion.ScriptLine{}, allOptional: true},
	{schema: "MusicPartRef", value: musicmotion.PartRef{}},
	{schema: "MusicDesignOverride", value: musicmotion.DesignOverride{}, allOptional: true},
	{schema: "MusicVerseLayout", value: musicmotion.VerseLayout{}, allOptional: true},
	{schema: "MusicVideoItem", value: musicmotion.VideoItem{}},
	{schema: "MusicScriptHit", value: musicmotion.ScriptHit{}},
	{schema: "MusicStage", value: musicmotion.Stage{}, allOptional: true},
	{schema: "MusicStageCharacter", value: musicmotion.StageCharacter{}},
	{schema: "SongMap", value: songmap.Map{}},
	{schema: "SongMap/properties/sections/items", value: songmap.Section{}},
	{schema: "SongMap/properties/words/items", value: songmap.Word{}},
	{schema: "MusicProposeRequest", value: proposeRequest{}},
	{schema: "MusicProposeAnswer", value: proposeAnswer{}},
	{schema: "MusicProposal", value: musicProposal{}},
	{schema: "MusicProposeAnswer/properties/replacements/items", value: musicmotion.Replacement{}},
	{schema: "MusicRenderAnswer", value: renderAnswer{}},
	{schema: "Run", value: runView{}},
	{schema: "Review", value: Review{}},
	{schema: "Finding", value: findingJSON{}},
	{schema: "Provider", value: assistant.ProviderView{}},
	{schema: "Routing", value: routingAnswer{}},
	{schema: "Routing/properties/routing/additionalProperties", value: routedTask{}},
	{schema: "Error", value: api.APIError{}},
	{schema: "Error/properties/error", value: api.APIErrorBody{}},
	{schema: "Download", value: models.Download{}},
	{schema: "ContainerState", value: musicsvc.ContainerState{}},
	{schema: "DatabaseView", value: databaseView{}},
	{schema: "DatabaseSettings", value: databaseAnswer{}},
	{schema: "SoundTag", value: clap.Tag{}},
	{schema: "MonitoringRequest", value: monitoringRequest{}, allOptional: true},
	{schema: "MonitoringTools", value: monitoringTools{}},
	{schema: "MonitoringTools/properties/tools/items", value: monitoringView{}},
	{schema: "GenerationRequest", value: generationRequest{}, allOptional: true},
	{schema: "Generation", value: generationView{}},
	{schema: "GenerationInput", value: generationInput{}},
	{schema: "DurationRange", value: durationRange{}},
	{schema: "VideoOption", value: videoOptionView{}},
	{schema: "GenerationInputs", value: generationInputs{}},
	{schema: "InputLimit", value: inputLimit{}},
	{schema: "ImageModel", value: imageModelView{}},
	{schema: "GenerationOptions", value: generationOptions{}},
	{schema: "PhaseDuration", value: phaseDurationView{}},
	{schema: "EstimateRequest", value: estimateRequest{}},
	{schema: "Estimate", value: estimateView{}},
	{schema: "ImageEditRequest", value: imageEditRequest{}, allOptional: true},
	{schema: "ImageEditResult", value: imageEditResult{}},
	{schema: "ImageEdit", value: imageEditView{}},
}

type jsonField struct {
	omitEmpty bool
	pointer   bool // a pointer: always present when not omitted, and can be null
}

// jsonFields lists the JSON fields a value is encoded with, as encoding/json sees them: embedded structs without a name
// are flattened, `json:"-"` and unexported fields are skipped.
func jsonFields(t reflect.Type) map[string]jsonField {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	out := map[string]jsonField{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, opts, _ := strings.Cut(tag, ",")
		if f.Anonymous && name == "" {
			et := f.Type
			for et.Kind() == reflect.Pointer {
				et = et.Elem()
			}
			if et.Kind() == reflect.Struct {
				for k, v := range jsonFields(et) {
					out[k] = v
				}
				continue
			}
		}
		if !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		out[name] = jsonField{omitEmpty: strings.Contains(","+opts+",", ",omitempty,"), pointer: f.Type.Kind() == reflect.Pointer}
	}
	return out
}

// schemaAt follows a path such as "Routing/properties/routing/additionalProperties" from components.schemas.
func schemaAt(c *contractChecker, path string) (map[string]any, bool) {
	node := c.Doc["components"].(map[string]any)["schemas"]
	for _, part := range strings.Split(path, "/") {
		m, ok := c.Resolve(node).(map[string]any)
		if !ok {
			return nil, false
		}
		node = m[part]
	}
	s, ok := c.Resolve(node).(map[string]any)
	return s, ok
}

func compareWithStructure(c *contractChecker, tc structCase) []string {
	schema, ok := schemaAt(c, tc.schema)
	if !ok {
		return []string{tc.schema + ": the description has no such schema"}
	}
	props, _ := schema["properties"].(map[string]any)
	required := map[string]bool{}
	if list, ok := schema["required"].([]any); ok {
		for _, r := range list {
			required[r.(string)] = true
		}
	}
	fields := jsonFields(reflect.TypeOf(tc.value))
	var problems []string
	fail := func(field, format string, args ...any) {
		problems = append(problems, fmt.Sprintf("%s.%s: %s", tc.schema, field, fmt.Sprintf(format, args...)))
	}
	names := make([]string, 0, len(fields))
	for n := range fields {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		f := fields[n]
		prop, declared := props[n]
		switch {
		case !declared:
			fail(n, "the structure sends it and the description does not declare it")
		case tc.allOptional:
		case !f.omitEmpty && !required[n]:
			fail(n, "the structure always sends it and the description does not mark it as required")
		case f.omitEmpty && required[n]:
			fail(n, "the structure may omit it (omitempty) and the description marks it as required")
		}
		if declared && f.pointer && !f.omitEmpty && !tc.allOptional {
			// A $ref cannot carry `nullable` next to it in OpenAPI 3.0: only inline schemas are checked.
			if m, ok := prop.(map[string]any); ok && m["$ref"] == nil {
				if nullable, _ := m["nullable"].(bool); !nullable {
					fail(n, "is a pointer the structure always sends and can be null, and the description does not say nullable")
				}
			}
		}
	}
	for n := range props {
		if _, ok := fields[n]; !ok {
			fail(n, "the description declares it and the structure has no such field")
		}
	}
	for n := range required {
		if _, ok := props[n]; !ok {
			fail(n, "is required and not declared")
		}
	}
	if tc.allOptional && len(required) > 0 {
		problems = append(problems, tc.schema+": every field may be left out, so the description must carry no `required`")
	}
	sort.Strings(problems)
	return problems
}

func TestContract_TheDescriptionFollowsTheStructures(t *testing.T) {
	c := newContractChecker(t)
	var problems []string
	for _, tc := range structCases {
		problems = append(problems, compareWithStructure(c, tc)...)
	}
	if len(problems) > 0 {
		t.Errorf("the description and the structures disagree:\n  %s", strings.Join(problems, "\n  "))
	}
}

// A schema that lists fields and says nothing about which are required leaves a client to guess: it is a failure,
// unless it is a request whose fields are all optional (and the table above says so).
func TestContract_EverySchemaWithFieldsSaysWhichAreRequired(t *testing.T) {
	c := newContractChecker(t)
	allOptional := map[string]bool{}
	for _, tc := range structCases {
		if tc.allOptional {
			allOptional[tc.schema] = true
		}
	}
	schemas := c.Doc["components"].(map[string]any)["schemas"].(map[string]any)
	var missing []string
	for name, node := range schemas {
		s, _ := node.(map[string]any)
		if props, _ := s["properties"].(map[string]any); len(props) > 0 && s["required"] == nil && !allOptional[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("these schemas declare fields and no `required` (add it, or mark the request as all-optional in structCases): %s", strings.Join(missing, ", "))
	}
}

func TestContract_TheComparisonCatchesWhatDiffers(t *testing.T) {
	c := newContractChecker(t)
	type sent struct {
		Always string  `json:"always"`
		Maybe  string  `json:"maybe,omitempty"`
		Null   *string `json:"null"`
		Hidden string  `json:"-"`
	}
	set := func(schema map[string]any) {
		c.Doc["components"].(map[string]any)["schemas"].(map[string]any)["Probe"] = schema
	}
	prop := map[string]any{"type": "string"}
	good := map[string]any{"type": "object", "required": []any{"always", "null"}, "properties": map[string]any{"always": prop, "maybe": prop, "null": map[string]any{"type": "string", "nullable": true}}}
	set(good)
	if p := compareWithStructure(c, structCase{schema: "Probe", value: sent{}}); len(p) != 0 {
		t.Fatalf("the reference schema must fit: %v", p)
	}
	cases := []struct {
		name string
		edit func(m map[string]any)
		want string
	}{
		{"a field the structure sends and the description lacks", func(m map[string]any) { delete(m["properties"].(map[string]any), "maybe") }, "Probe.maybe: the structure sends it"},
		{"a field the description has and the structure lacks", func(m map[string]any) { m["properties"].(map[string]any)["ghost"] = prop }, "Probe.ghost: the description declares it"},
		{"an always-sent field not marked required", func(m map[string]any) { m["required"] = []any{"null"} }, "Probe.always: the structure always sends it"},
		{"an optional field marked required", func(m map[string]any) { m["required"] = []any{"always", "null", "maybe"} }, "Probe.maybe: the structure may omit it"},
		{"a nullable pointer not saying so", func(m map[string]any) { m["properties"].(map[string]any)["null"] = prop }, "Probe.null: is a pointer"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := map[string]any{"type": "object", "required": []any{"always", "null"}, "properties": map[string]any{"always": prop, "maybe": prop, "null": map[string]any{"type": "string", "nullable": true}}}
			tc.edit(m)
			set(m)
			p := strings.Join(compareWithStructure(c, structCase{schema: "Probe", value: sent{}}), "\n")
			if !strings.Contains(p, tc.want) {
				t.Errorf("must report %q, got:\n%s", tc.want, p)
			}
		})
	}
	t.Run("an all-optional request that carries required", func(t *testing.T) {
		set(good)
		p := strings.Join(compareWithStructure(c, structCase{schema: "Probe", value: sent{}, allOptional: true}), "\n")
		if !strings.Contains(p, "must carry no `required`") {
			t.Errorf("got %s", p)
		}
	})
}

// codeConstants are the string constants named code… (or Code…, in the packages of the families) of this folder and its
// sub-packages: the error codes the server can answer.
func codeConstants(t *testing.T) []string {
	t.Helper()
	fset := token.NewFileSet()
	var names []string
	err := filepath.WalkDir(".", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go") {
			names = append(names, p)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	var codes []string
	for _, name := range names {
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, ident := range vs.Names {
					if !(strings.HasPrefix(ident.Name, "code") || strings.HasPrefix(ident.Name, "Code")) || len(ident.Name) < 5 || i >= len(vs.Values) {
						continue
					}
					if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
						v, _ := strconv.Unquote(lit.Value)
						codes = append(codes, v)
					}
				}
			}
		}
	}
	sort.Strings(codes)
	return codes
}

func TestContract_EveryErrorCodeTheServerUsesIsListed(t *testing.T) {
	c := newContractChecker(t)
	body, ok := schemaAt(c, "Error/properties/error/properties/code")
	if !ok {
		t.Fatal("the description has no Error.error.code")
	}
	listed := map[string]bool{}
	enum, _ := body["enum"].([]any)
	for _, v := range enum {
		listed[v.(string)] = true
	}
	codes := codeConstants(t)
	if len(codes) < 10 {
		t.Fatalf("found only %d code constants: the scan is not finding them (%v)", len(codes), codes)
	}
	defined := map[string]bool{}
	for _, code := range codes {
		defined[code] = true
		if !listed[code] {
			t.Errorf("the server defines the error code %q and the description does not list it", code)
		}
	}
	for code := range listed {
		if !defined[code] {
			t.Errorf("the description lists the error code %q and the server does not define it", code)
		}
	}
}

// openapiSchemaKeywords is the vocabulary of an OpenAPI 3.0 schema object (and the `x-` extensions).
var openapiSchemaKeywords = map[string]bool{
	"$ref": true, "type": true, "properties": true, "required": true, "items": true, "additionalProperties": true,
	"nullable": true, "enum": true, "description": true, "format": true, "example": true, "default": true, "title": true,
	"allOf": true, "oneOf": true, "anyOf": true, "not": true, "discriminator": true,
	"minimum": true, "maximum": true, "exclusiveMinimum": true, "exclusiveMaximum": true, "multipleOf": true,
	"minLength": true, "maxLength": true, "pattern": true, "minItems": true, "maxItems": true, "uniqueItems": true,
	"minProperties": true, "maxProperties": true, "readOnly": true, "writeOnly": true, "deprecated": true, "xml": true, "externalDocs": true,
}

func isSecurityScheme(typ string, n map[string]any) bool {
	switch typ {
	case "http", "apiKey", "oauth2", "openIdConnect":
		return n["scheme"] != nil || n["in"] != nil // an http scheme has a scheme, an apiKey says where it goes
	}
	return false
}

// unknownSchemaKeywords walks a description and names every schema that has a key OpenAPI does not know. A YAML flow
// mapping such as `{ type: integer, description: about 9,5 GB }` breaks at the comma and turns the rest of the text
// into a key; this is how that, and a misspelt keyword, are caught for every schema, walked by a test or not.
func unknownSchemaKeywords(node any, path string) []string {
	var found []string
	switch n := node.(type) {
	case map[string]any:
		_, hasProperties := n["properties"].(map[string]any)
		if typ, ok := n["type"].(string); ok || hasProperties {
			// A security scheme has a `type` too, but it is not a schema.
			if !isSecurityScheme(typ, n) {
				for k := range n {
					if !openapiSchemaKeywords[k] && !strings.HasPrefix(k, "x-") {
						found = append(found, fmt.Sprintf("%s: %q is not a keyword of a schema", path, k))
					}
				}
			}
		}
		for k, v := range n {
			switch k {
			case "example", "examples", "default", "enum":
				continue // values, not schemas
			}
			found = append(found, unknownSchemaKeywords(v, path+"/"+k)...)
		}
	case []any:
		for i, v := range n {
			found = append(found, unknownSchemaKeywords(v, fmt.Sprintf("%s[%d]", path, i))...)
		}
	}
	return found
}

func TestContract_EverySchemaUsesOnlyKeywordsOfOpenAPI(t *testing.T) {
	c := newContractChecker(t)
	found := unknownSchemaKeywords(c.Doc["components"], "components")
	found = append(found, unknownSchemaKeywords(c.Doc["paths"], "paths")...)
	sort.Strings(found)
	if len(found) > 0 {
		t.Errorf("the description has keys no schema can have (an unquoted comma in a one-line description splits it):\n  %s", strings.Join(found, "\n  "))
	}
}

func TestContract_ACommaInAnUnquotedDescriptionIsCaught(t *testing.T) {
	var doc map[string]any
	broken := "type: object\nproperties:\n  w: { type: integer, description: Lo que ocuparán los pesos (unos 9,5 GB). }\n"
	if err := yaml.Unmarshal([]byte(broken), &doc); err != nil {
		t.Fatal(err)
	}
	found := unknownSchemaKeywords(doc, "Probe")
	if len(found) != 1 || !strings.Contains(found[0], `"5 GB)."`) {
		t.Errorf("must report the key the comma made, got %v", found)
	}
	fixed := "type: object\nproperties:\n  w: { type: integer, description: \"Lo que ocuparán los pesos (unos 9,5 GB).\" }\n"
	doc = nil
	if err := yaml.Unmarshal([]byte(fixed), &doc); err != nil {
		t.Fatal(err)
	}
	if found := unknownSchemaKeywords(doc, "Probe"); len(found) != 0 {
		t.Errorf("a quoted description is fine, got %v", found)
	}
}
