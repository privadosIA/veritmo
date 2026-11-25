package musicmotion

// register.go: what the music motion engine gives the packages that cannot import it. `config` checks the parts and the
// design a scene names, and `pipeline` builds the page of a scene from a part, but both are imported by this package, so
// they ask for an interface (config.PartCatalog, pipeline.MotionParts) that Parts implements. Every program that runs
// projects (the command and the server) makes one and hands it to them.

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"

	"veritmo/internal/project/config"
	"veritmo/internal/project/pipeline"
	"veritmo/internal/video/motion"
)

// Parts is the engine as the catalog of parts the configuration checks against (config.PartCatalog) and the builder of the
// pages of the scenes made from them (pipeline.MotionParts). It is given its catalog (NewParts, NewLazyParts): there is no default one
// in the package. The zero value has the parts that come with the program and none of the user's.
type Parts struct {
	cat  *Catalog
	lazy *lazyCatalog
}

// lazyCatalog opens the catalog when a part is first asked for, so that a command that never looks one up opens no database.
type lazyCatalog struct {
	once sync.Once
	open func() (Catalog, error)
	cat  Catalog
}

// NewParts is the engine on a catalog.
func NewParts(cat Catalog) Parts { return Parts{cat: &cat} }

// NewLazyParts is the engine on a catalog that open makes the first time a part is asked for. When it cannot (no database), the engine has
// the parts that come with the program and says why in the log, once.
func NewLazyParts(open func() (Catalog, error)) Parts { return Parts{lazy: &lazyCatalog{open: open}} }

// catalog is the catalog the engine reads.
func (p Parts) catalog() Catalog {
	switch {
	case p.cat != nil:
		return *p.cat
	case p.lazy != nil:
		p.lazy.once.Do(func() {
			c, err := p.lazy.open()
			if err != nil {
				slog.Warn("music-motion: the user's parts cannot be read, only the parts that come with the program are used", "err", err)
				c = Catalog{Builtin: BuiltinParts()}
			}
			p.lazy.cat = c
		})
		return p.lazy.cat
	}
	return Catalog{Builtin: BuiltinParts()}
}

var (
	_ config.PartCatalog   = Parts{}
	_ pipeline.MotionParts = Parts{}
)

// Check says whether a scene may name the part with these parameters (see CheckScenePart).
func (p Parts) Check(part string, params map[string]any) error {
	return CheckScenePart(p.catalog(), part, params)
}

// Parse reads the parameters of a part given as text (see ParseSceneParams).
func (p Parts) Parse(part string, raw map[string]string) (map[string]any, error) {
	return ParseSceneParams(p.catalog(), part, raw)
}

// CheckDesign validates the design of a project (see CheckMotionDesign).
func (Parts) CheckDesign(d config.MotionDesign) error { return CheckMotionDesign(d) }

// Page builds the page and the beat clock of a scene made from a part (see BuildScenePage).
func (p Parts) Page(r pipeline.PartRequest) (pipeline.PartPage, error) {
	return BuildScenePage(p.catalog(), r)
}

// Choose asks the model to choose a part of the catalog for a scene (see ChoosePart).
func (p Parts) Choose(ctx context.Context, w motion.Writer, r pipeline.PartChoiceRequest) (pipeline.PartChoice, error) {
	return ChoosePart(ctx, w, p.catalog(), r)
}

// ParseSceneParams reads the parameters of a part given as text, as the part's manifest declares their types: a whole
// number, a number, true or false, or text. A part that is not in the catalog, or a parameter that is not one of its own, is
// refused naming it.
func ParseSceneParams(cat Catalog, name string, raw map[string]string) (map[string]any, error) {
	part, ok := cat.Get(name)
	if !ok {
		return nil, fmt.Errorf("the part %q is not in the catalog", name)
	}
	byName := map[string]Param{}
	for _, p := range part.Params {
		byName[p.Name] = p
	}
	out := map[string]any{}
	for k, v := range raw {
		p, ok := byName[k]
		if !ok {
			return nil, fmt.Errorf("the part %q has no parameter %q", name, k)
		}
		switch p.Type {
		case "int":
			n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
			if err != nil {
				return nil, fmt.Errorf("the parameter %q of the part %q must be a whole number, got %q", k, name, v)
			}
			out[k] = n
		case "number":
			f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
			if err != nil {
				return nil, fmt.Errorf("the parameter %q of the part %q must be a number, got %q", k, name, v)
			}
			out[k] = f
		case "bool":
			b, err := strconv.ParseBool(strings.ToLower(strings.TrimSpace(v)))
			if err != nil {
				return nil, fmt.Errorf("the parameter %q of the part %q must be true or false, got %q", k, name, v)
			}
			out[k] = b
		default:
			out[k] = v
		}
	}
	return out, nil
}

// CheckScenePart says whether a scene may name the part with these parameters: the part is in the catalog and every
// parameter is one of its own and inside its limits.
func CheckScenePart(cat Catalog, name string, params map[string]any) error {
	part, ok := cat.Get(name)
	if !ok {
		var names []string
		for _, p := range cat.List() {
			if !p.Manual || p.Name == "blank" {
				names = append(names, p.Name)
			}
		}
		return fmt.Errorf("the part is not in the catalog (the parts are %s)", strings.Join(names, ", "))
	}
	_, err := part.ResolveParams(params)
	return err
}

// DesignFromConfig is the design a project's defaults.motion.design describes.
func DesignFromConfig(d config.MotionDesign) Design {
	return Design{Dark: d.Dark, Light: d.Light, Accent: d.Accent, Display: d.Display, Body: d.Body, DefaultMode: d.DefaultMode,
		Modes: d.Modes, Concept: d.Concept, VerseStyle: d.VerseStyle, HitStyle: d.HitStyle}
}

// ToConfig is the design as a project's defaults.motion.design writes it.
func (d Design) ToConfig() *config.MotionDesign {
	return &config.MotionDesign{Dark: d.Dark, Light: d.Light, Accent: d.Accent, Display: d.Display, Body: d.Body, DefaultMode: d.DefaultMode,
		Modes: d.Modes, Concept: d.Concept, VerseStyle: d.VerseStyle, HitStyle: d.HitStyle}
}

// CheckMotionDesign validates the design of a project as a piece's design is validated.
func CheckMotionDesign(d config.MotionDesign) error { return DesignFromConfig(d).Validate() }
