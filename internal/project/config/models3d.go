package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"veritmo/internal/assets3d/generate"
)

// ModeModels3D is a project that makes 3D models (spec models3d-mode): a list of models in its scenes.yaml, each made from a prompt or a picture.
const ModeModels3D OutputMode = "models3d"

// MaxModelVariants bounds the models made from one entry with different seeds.
const MaxModelVariants = 4

// ModelEntry is one entry of the models: list of a models3d-mode project.
type ModelEntry struct {
	// ID names the entry and its file, out/<project>/<id>.glb.
	ID string `yaml:"id" json:"id"`
	// Prompt is what the picture is made from; Image is the name of a picture of the sources instead. One of the two, never both.
	Prompt string `yaml:"prompt,omitempty" json:"prompt,omitempty"`
	Image  string `yaml:"image,omitempty" json:"image,omitempty"`
	// Engine and Detail say how the model is made; empty takes defaults.models3d, then triposr and normal.
	Engine string `yaml:"engine,omitempty" json:"engine,omitempty"`
	Detail string `yaml:"detail,omitempty" json:"detail,omitempty"`
	// Variants is how many models are made with different seeds (1 to MaxModelVariants; nil is 1).
	Variants *int `yaml:"variants,omitempty" json:"variants,omitempty"`
	// Seed is the one of the picture (the first variant; each next one adds 1). nil takes one from the prompt.
	Seed *int `yaml:"seed,omitempty" json:"seed,omitempty"`
	// Adult marks the models made from this entry as for people of 18 or older.
	Adult bool `yaml:"adult,omitempty" json:"adult,omitempty"`
}

// Models3DDefaults are the defaults.models3d of a models3d-mode project.
type Models3DDefaults struct {
	Engine string `yaml:"engine,omitempty" json:"engine,omitempty"`
	Detail string `yaml:"detail,omitempty" json:"detail,omitempty"`
}

// EntryEngine is the engine an entry is made with: its own, else defaults.models3d, else triposr.
func (c *Config) EntryEngine(e ModelEntry) string {
	switch {
	case e.Engine != "":
		return e.Engine
	case c.Defaults.Models3D != nil && c.Defaults.Models3D.Engine != "":
		return c.Defaults.Models3D.Engine
	}
	return generate.EngineTripoSR
}

// EntryDetail is the detail an entry is made at: its own, else defaults.models3d, else normal.
func (c *Config) EntryDetail(e ModelEntry) string {
	switch {
	case e.Detail != "":
		return e.Detail
	case c.Defaults.Models3D != nil && c.Defaults.Models3D.Detail != "":
		return c.Defaults.Models3D.Detail
	}
	return generate.DetailNormal
}

// EntryVariants is how many models an entry makes: one when it has its own image (the seed only changes the picture).
func (e ModelEntry) EntryVariants() int {
	if e.Variants == nil || e.Image != "" {
		return 1
	}
	return *e.Variants
}

// ModelFileName is the file of variant i (0-based) of an entry that makes `variants`: <id>.glb for the first, <id>-<n>.glb after it.
func ModelFileName(id string, i int) string {
	if i == 0 {
		return id + ".glb"
	}
	return fmt.Sprintf("%s-%d.glb", id, i+1)
}

// ModelName is the name of variant i of an entry in the library and in the manifest: the id, with -<n> after the first.
func ModelName(id string, i int) string { return strings.TrimSuffix(ModelFileName(id, i), ".glb") }

// ValidModelID reports whether id can name an entry of models (and so a model made from it).
func ValidModelID(id string) bool { return plainModelID(id) }

// plainModelID reports whether an id can name an entry: lower case letters, digits, '.', '_' and '-', not starting with a symbol, because it becomes the
// name of a file and of a model of the library.
func plainModelID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for i, r := range id {
		ok := r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || (i > 0 && (r == '.' || r == '_' || r == '-'))
		if !ok {
			return false
		}
	}
	return true
}

// validateModels3D checks a models3d-mode project: its entries instead of scenes and tracks, the engines and the details, and the pictures of the sources.
func (c *Config) validateModels3D(statPath func(string) string) []error {
	var errs []error
	if len(c.Scenes) > 0 {
		errs = append(errs, fmt.Errorf("config: output.mode %q uses models, not scenes: move the %d scene(s) to models or change the mode", ModeModels3D, len(c.Scenes)))
	}
	if len(c.Tracks) > 0 {
		errs = append(errs, fmt.Errorf("config: output.mode %q uses models, not tracks: tracks belong to the mode %q", ModeModels3D, ModeMusic))
	}
	if len(c.Models) == 0 {
		errs = append(errs, fmt.Errorf("config: output.mode %q needs at least one entry in models", ModeModels3D))
	}
	if d := c.Defaults.Models3D; d != nil {
		if d.Engine != "" {
			if _, ok := generate.Info(d.Engine); !ok {
				errs = append(errs, fmt.Errorf("config: defaults.models3d.engine %q must be one of %s", d.Engine, strings.Join(generate.Engines(), ", ")))
			}
		}
		if d.Detail != "" && !generate.ValidDetail(d.Detail) {
			errs = append(errs, fmt.Errorf("config: defaults.models3d.detail %q must be low, normal or high", d.Detail))
		}
	}
	seen := map[string]int{}
	for i, e := range c.Models {
		bad := func(format string, a ...any) {
			errs = append(errs, fmt.Errorf("config: model %q: "+format, append([]any{e.ID}, a...)...))
		}
		switch {
		case !plainModelID(e.ID):
			errs = append(errs, fmt.Errorf("config: model %d: id %q must be a plain lower-case name (letters, digits, '.', '_' or '-', up to 64 characters)", i, e.ID))
			continue
		default:
			if prev, dup := seen[e.ID]; dup {
				bad("the id is repeated (first at index %d, again at index %d)", prev, i)
			}
			seen[e.ID] = i
		}
		prompt := strings.TrimSpace(e.Prompt)
		switch {
		case prompt != "" && e.Image != "":
			bad("prompt and image cannot both be set: the picture is made from the prompt, or it is the image")
		case prompt == "" && e.Image == "":
			bad("needs a prompt or an image")
		case len([]rune(prompt)) > generate.MaxPromptRunes:
			bad("prompt is longer than %d characters", generate.MaxPromptRunes)
		}
		if e.Image != "" {
			if !ValidImageName(e.Image) {
				bad("image %q must be the name of a .png, .jpg, .jpeg or .webp file, not a path", e.Image)
			} else if _, err := os.Stat(statPath(filepath.Join(SourcesSubdir, e.Image))); err != nil {
				bad("image %q not found in %s", e.Image, SourcesSubdir)
			}
		}
		if e.Engine != "" {
			if _, ok := generate.Info(e.Engine); !ok {
				bad("engine %q must be one of %s", e.Engine, strings.Join(generate.Engines(), ", "))
			}
		}
		if e.Detail != "" && !generate.ValidDetail(e.Detail) {
			bad("detail %q must be low, normal or high", e.Detail)
		}
		if e.Variants != nil && (*e.Variants < 1 || *e.Variants > MaxModelVariants) {
			bad("variants must be between 1 and %d, got %d", MaxModelVariants, *e.Variants)
		}
		if e.Image != "" && e.Variants != nil && *e.Variants > 1 {
			c.warn("model %q: variants is ignored because the entry has its own image (the seed only changes the picture): one model is made", e.ID)
		}
	}
	// The files the entries make must not collide (an id "a-2" and the second variant of "a").
	files := map[string]string{}
	for _, e := range c.Models {
		if !plainModelID(e.ID) {
			continue
		}
		for i := 0; i < e.EntryVariants(); i++ {
			f := ModelFileName(e.ID, i)
			if other, dup := files[f]; dup && other != e.ID {
				errs = append(errs, fmt.Errorf("config: models %q and %q both make the file %q", other, e.ID, f))
			}
			files[f] = e.ID
		}
	}
	return errs
}
