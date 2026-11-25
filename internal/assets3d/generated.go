package assets3d

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"veritmo/internal/video/blender"
)

// GeneratedPackID is the pack of the models that an engine made (spec model-generation): created with the first one, removable like any pack.
const GeneratedPackID = "generated"

// GenMeta is what AddGenerated is told about a model.
type GenMeta struct {
	// Name is the plain name asked for ("" makes one from the prompt); a number is added when it is taken.
	Name string
	// Prompt is what the picture was made from, Picture the name of a picture the caller gave instead.
	Prompt, Picture               string
	Engine, EngineVersion, Detail string
	// Licence is the engine's; Attribution says whether it asks for its terms to be followed.
	Licence     string
	Attribution bool
	// Project and Entry say which project made the model and which of its models (see GeneratedInfo). A model with the same project and entry
	// that the library already holds is replaced (its file, its record and its thumbnail), keeping its name, so a project that is run again does
	// not fill the library with copies.
	Project, Entry string
	Adult          bool
}

var genMu sync.Mutex

var slugWords = regexp.MustCompile(`[a-z0-9]+`)

// SlugOf makes a plain name from a text: its first words, lower case, joined with dashes, at most 32 characters ("model" when it has none).
func SlugOf(text string) string {
	words := slugWords.FindAllString(strings.ToLower(asciiOf(text)), 5)
	s := strings.Join(words, "-")
	if len(s) > 32 {
		s = strings.TrimRight(s[:32], "-")
	}
	if s == "" {
		return "model"
	}
	return s
}

// asciiOf drops the accents of the letters it knows and the characters that are not ASCII.
func asciiOf(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r < 128:
			b.WriteRune(r)
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
			b.WriteByte(' ')
		}
	}
	return b.String()
}

// AddGenerated puts a model that an engine made in the pack "generated" (created when it is the first) and records what made it. The file must be
// a usable binary glTF within the limits of a scene. It returns the model as the catalog shows it.
func (l *Library) AddGenerated(glb []byte, meta GenMeta) (ModelView, error) {
	if meta.Engine == "" || meta.Licence == "" {
		return ModelView{}, fmt.Errorf("%w: a generated model has an engine and its licence", ErrInvalid)
	}
	m, err := blender.ParseModel(glb)
	if err != nil {
		return ModelView{}, fmt.Errorf("%w: the engine's model is not usable: %v", ErrInvalid, err)
	}
	if m.Triangles > blender.MaxModelTriangles {
		return ModelView{}, fmt.Errorf("%w: %d triangles, at most %d", ErrInvalid, m.Triangles, blender.MaxModelTriangles)
	}
	base := meta.Name
	if base == "" {
		base = SlugOf(firstNonEmpty(meta.Prompt, meta.Picture))
	}
	if !namePattern.MatchString(base) {
		return ModelView{}, fmt.Errorf("%w: the name must be a plain name (letters, digits, '.', '_' or '-')", ErrInvalid)
	}
	genMu.Lock()
	defer genMu.Unlock()
	dir := l.packDir(GeneratedPackID)
	rec, ok := l.record(GeneratedPackID)
	if !ok {
		rec = &Record{ID: GeneratedPackID, Name: "Generated models", Author: "generated here", Licence: "that of the engine that made each model", Source: "generated",
			Description: "Models made from a prompt or a picture by an engine of this server.", Tags: []string{"generated"}}
	}
	taken := map[string]bool{}
	for _, x := range rec.Models {
		taken[x.Name] = true
	}
	replace := -1
	if meta.Project != "" && meta.Entry != "" {
		for i, x := range rec.Models {
			if g := x.Generated; g != nil && g.Project == meta.Project && g.Entry == meta.Entry {
				replace = i
				break
			}
		}
	}
	name := base
	if replace >= 0 {
		name = rec.Models[replace].Name
	} else {
		for n := 2; taken[name]; n++ {
			name = fmt.Sprintf("%s-%d", base, n)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "files"), 0o755); err != nil {
		return ModelView{}, err
	}
	file := filepath.Join(dir, "files", name+blender.ModelExt)
	if err := writeAtomic(file, glb); err != nil {
		return ModelView{}, err
	}
	now := time.Now().UTC()
	tags := tagsOf(name, []string{"generated", meta.Engine})
	model := Model{Name: name, File: name + blender.ModelExt, Clips: []string{}, Triangles: m.Triangles, Needs: []string{}, Tags: tags,
		Generated: &GeneratedInfo{Prompt: meta.Prompt, Picture: meta.Picture, Engine: meta.Engine, EngineVersion: meta.EngineVersion, Detail: meta.Detail,
			Licence: meta.Licence, Attribution: meta.Attribution, GeneratedAt: now, Project: meta.Project, Entry: meta.Entry, Adult: meta.Adult}}
	at := len(rec.Models)
	if replace >= 0 {
		rec.Models[replace] = model
		at = replace
	} else {
		rec.Models = append(rec.Models, model)
	}
	rec.InstalledAt = now
	if err := writeRecordAtomic(dir, rec); err != nil {
		if replace < 0 {
			_ = os.Remove(file)
		}
		return ModelView{}, err
	}
	_ = os.Remove(filepath.Join(dir, "thumbs", name+".png")) // a model made again is drawn again
	return viewOfModel(rec, rec.Models[at]), nil
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

// writeAtomic writes a file through a temporary name, so a crash leaves the old file or the new one and never half of one.
func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".write-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil { // a temporary file is private; the files of the library are readable like the others
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// writeRecordAtomic writes pack.json the same way (the folder may not have one yet).
func writeRecordAtomic(dir string, r *Record) error {
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return writeAtomic(filepath.Join(dir, "pack.json"), append(raw, '\n'))
}
