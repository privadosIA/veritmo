package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"time"

	"veritmo/internal/assets3d"
	"veritmo/internal/assets3d/generate"
	"veritmo/internal/platform/cache"
	"veritmo/internal/platform/events"
	"veritmo/internal/platform/musicsvc"
	"veritmo/internal/platform/runner"
	"veritmo/internal/project/config"
	"veritmo/internal/project/export"
	"veritmo/internal/video/render"
)

// Models3DServices is what a models3d-mode run asks of the machine besides the image engine: the 3D service, and the library the models are added to.
type Models3DServices struct {
	// Service opens the 3D service for an engine (a *generate.Service); a run without it fails saying so.
	Service generate.Opener
	// Library receives each model in its pack "generated", with the project and the entry that made it; nil keeps the models only in out/.
	Library *assets3d.Library
}

// defaultModelsResolution is the size of the picture of an entry when the project says none: square, so that the object stands in the middle.
const defaultModelsResolution = "768x768"

// modelPlan is one model a run may make: variant i of an entry.
type modelPlan struct {
	entry   config.ModelEntry
	variant int
	// name is the name of the model (the id, with -<n> after the first variant) and file its path in the project.
	name, file string
	project    string
	engine     string
	detail     string
	seed       int64
	// scene is the picture's scene (a prompt entry); source is the picture of the sources (an entry with its own image).
	scene  config.Scene
	source string
	// pictureHash says what the picture is made from (the hash of its inputs, or of the file of the entry's own image).
	pictureHash string
	key         string
	hit         bool
	// prev is the entry of the manifest of an earlier run, when it is the same model.
	prev export.Models3DEntry
}

// entrySeed is the seed of the first variant of an entry: its own, or one from its prompt (a prompt gives the same seed every time, so a run that
// changes nothing changes no cache key).
func entrySeed(e config.ModelEntry) int64 {
	if e.Seed != nil {
		return int64(*e.Seed)
	}
	return int64(crc32.ChecksumIEEE([]byte(e.Prompt)))
}

// modelKey is the hash of what decides a model: the picture, the engine and its version, the detail asked and the seed.
func modelKey(pictureHash, engine, version, detail string, seed int64) string {
	h := sha256.New()
	fmt.Fprintf(h, "picture=%s\x00engine=%s\x00version=%s\x00detail=%s\x00seed=%d\x00", pictureHash, engine, version, detail, seed)
	return hex.EncodeToString(h.Sum(nil))
}

// runModels3D makes the models of a models3d-mode project, one entry (and variant) after the other: the picture with the project's image engine
// (unless the entry brings its own), the model with the 3D service, the file in out/<project>/, its place in the library and the manifest. The
// service holds its card, so there is nothing to gain from making two at once.
func runModels3D(ctx context.Context, cfg *config.Config, emitter *events.Emitter, execRunner runner.CommandRunner, manifest *cache.Manifest, onlyFilter cache.OnlyFilter, force bool, renderEnv []string, base string, services *Models3DServices) (string, error) {
	if services == nil || services.Service == nil {
		return "", fail(emitter, "_global", "this run has no 3D service to make the models with")
	}
	exportDir := cfg.ExportDir()
	manifestPath := filepath.Join(exportDir, "manifest.json")
	if err := os.MkdirAll(joinDir(base, exportDir), 0o755); err != nil {
		return "", fail(emitter, "_global", fmt.Sprintf("cannot create %s: %v", exportDir, err))
	}
	// The picture of a prompt is made with the project's image settings; a project that sets no size gets a square one.
	pic := *cfg
	if pic.Defaults.Resolution == "" {
		pic.Defaults.Resolution = defaultModelsResolution
	}

	// What an earlier run made, to tell what is unchanged.
	prevBy := map[string]export.Models3DEntry{}
	if prev, err := export.ReadModels3DManifest(joinDir(base, manifestPath)); err == nil {
		for _, e := range prev.Models {
			prevBy[e.File] = e
		}
	}

	// The engines first: a service that cannot be used refuses the run before anything is made.
	versions := map[string]string{}
	clients := map[string]*generate.Client{}
	for _, e := range cfg.Models {
		if !onlyFilter.ShouldProcess(e.ID) {
			continue
		}
		engine := cfg.EntryEngine(e)
		if _, done := clients[engine]; done {
			continue
		}
		c, closeFn, err := services.Service.Open(ctx, engine)
		if err != nil {
			var ue *musicsvc.UnavailableError
			if errors.As(err, &ue) {
				return "", fail(emitter, "_global", fmt.Sprintf("the 3D engine %s cannot be used (%s): %s", engine, ue.Reason, ue.Detail))
			}
			return "", fail(emitter, "_global", fmt.Sprintf("the 3D engine %s cannot be used: %v", engine, err))
		}
		defer closeFn()
		clients[engine] = c
		v, err := c.EngineVersion(ctx, engine)
		if err != nil {
			return "", fail(emitter, "_global", fmt.Sprintf("the 3D engine %s cannot be used: %v", engine, err))
		}
		versions[engine] = v
	}

	// What each entry made, by id: the manifest lists the entries in the order of the project. The entries that this run does not touch (under
	// --only) keep what the earlier run said of them.
	done := map[string][]export.Models3DEntry{}
	for _, e := range cfg.Models {
		if onlyFilter.ShouldProcess(e.ID) {
			continue
		}
		for i := 0; i < e.EntryVariants(); i++ {
			if p, ok := prevBy[filepath.Join(exportDir, config.ModelFileName(e.ID, i))]; ok {
				done[e.ID] = append(done[e.ID], p)
			}
		}
	}
	// The manifest is written after every entry, so that a run that is cancelled or fails leaves what it made recorded.
	flush := func() error {
		m := export.Models3DManifest{Project: cfg.Project, Mode: export.Models3DMode, GeneratedAt: time.Now().UTC().Format(time.RFC3339)}
		for _, e := range cfg.Models {
			m.Models = append(m.Models, done[e.ID]...)
		}
		return export.WriteModels3DManifest(joinDir(base, manifestPath), m)
	}

	for _, e := range cfg.Models {
		if !onlyFilter.ShouldProcess(e.ID) {
			continue
		}
		if err := ctx.Err(); err != nil {
			_ = flush()
			return "", err
		}
		plans := planEntry(&pic, e, base, exportDir, versions, prevBy, force)
		lines, err := makeEntry(ctx, &pic, emitter, execRunner, manifest, renderEnv, base, services, clients, plans)
		done[e.ID] = lines
		if err != nil {
			_ = flush()
			return "", err
		}
		if err := flush(); err != nil {
			return "", fail(emitter, "_global", fmt.Sprintf("export failed: %v", err))
		}
	}
	if err := cache.SaveManifest(joinDir(base, cache.ManifestPath()), manifest); err != nil {
		emitter.Warn("_global", fmt.Sprintf("cannot save cache manifest: %v", err))
	}
	emitter.PhaseStart("_global", "export")
	if err := flush(); err != nil {
		return "", fail(emitter, "_global", fmt.Sprintf("export failed: %v", err))
	}
	emitter.PhaseEnd("_global", "export", manifestPath)
	return manifestPath, nil
}

// planEntry lays out the variants of an entry: their files, their seeds, what decides each model, and whether the one of an earlier run is still good.
func planEntry(cfg *config.Config, e config.ModelEntry, base, exportDir string, versions map[string]string, prevBy map[string]export.Models3DEntry, force bool) []*modelPlan {
	n := e.EntryVariants()
	plans := make([]*modelPlan, n)
	for i := 0; i < n; i++ {
		p := &modelPlan{entry: e, variant: i, project: cfg.Project, name: config.ModelName(e.ID, i), engine: cfg.EntryEngine(e), detail: cfg.EntryDetail(e), seed: entrySeed(e) + int64(i)}
		p.file = filepath.Join(exportDir, config.ModelFileName(e.ID, i))
		if e.Image != "" {
			p.source = filepath.Join(config.SourcesSubdir, e.Image)
			if sha, err := cache.FileSHA256(joinDir(base, p.source)); err == nil {
				p.pictureHash = "file:" + sha
			}
		} else {
			p.scene = config.Scene{ID: p.name, Prompt: generate.CompletePrompt(e.Prompt), NegativePrompt: generate.NegativePrompt}
			p.pictureHash = cache.ImageHash(SceneImageParams(cfg, p.scene, p.seed, 0))
		}
		p.key = modelKey(p.pictureHash, p.engine, versions[p.engine], p.detail, p.seed)
		if prev, ok := prevBy[p.file]; ok && prev.Key == p.key && p.pictureHash != "" && !force {
			if fi, err := os.Stat(joinDir(base, p.file)); err == nil && fi.Mode().IsRegular() {
				p.hit, p.prev = true, prev
			}
		}
		plans[i] = p
	}
	return plans
}

// makeEntry makes what an entry needs and returns the lines of its variants, in their order: the pictures (one phase for the entry), then the models
// (another). When it fails it returns the lines of the variants that were made or already good.
func makeEntry(ctx context.Context, cfg *config.Config, emitter *events.Emitter, execRunner runner.CommandRunner, manifest *cache.Manifest, renderEnv []string, base string, services *Models3DServices, clients map[string]*generate.Client, plans []*modelPlan) ([]export.Models3DEntry, error) {
	e := plans[0].entry
	var todo []*modelPlan
	for _, p := range plans {
		if !p.hit {
			todo = append(todo, p)
		}
	}
	prompted := e.Image == ""
	lines := make([]export.Models3DEntry, len(plans))
	// The variants that were already good keep their line of the manifest (and their mark, when the entry's changed).
	for i, p := range plans {
		if !p.hit {
			continue
		}
		lines[i] = p.prev
		if lines[i].Adult != e.Adult {
			lines[i].Adult = e.Adult
			if err := addToLibrary(base, services, p, lines[i]); err != nil {
				emitter.Warn(e.ID, fmt.Sprintf("the mark of %s could not be changed in the library: %v", p.name, err))
			}
		}
	}
	made := func() []export.Models3DEntry {
		var out []export.Models3DEntry
		for _, l := range lines {
			if l.File != "" {
				out = append(out, l)
			}
		}
		return out
	}
	if len(todo) == 0 {
		if prompted {
			emitter.Skipped(e.ID, "render")
		}
		emitter.Skipped(e.ID, "model3d")
		return made(), nil
	}
	pngs := map[string]string{}
	if prompted {
		if err := makePictures(ctx, cfg, emitter, execRunner, manifest, renderEnv, todo, pngs); err != nil {
			return made(), err
		}
	}
	emitter.PhaseStart(e.ID, "model3d")
	var failed error
	for _, p := range todo {
		if failed = ctx.Err(); failed != nil {
			break
		}
		line, err := makeModel(ctx, emitter, base, services, clients[p.engine], p, pngs[p.name])
		if err != nil {
			failed = err
			break
		}
		lines[p.variant] = line
	}
	if failed != nil {
		return made(), failed // what was made before a failure or a cancel stays recorded
	}
	emitter.PhaseEnd(e.ID, "model3d", todo[len(todo)-1].file)
	return made(), nil
}

// makePictures draws the picture of each variant that has to be made, as the images mode does for a scene (its cache and its sd call), under one
// "render" phase of the entry.
func makePictures(ctx context.Context, cfg *config.Config, emitter *events.Emitter, execRunner runner.CommandRunner, manifest *cache.Manifest, renderEnv []string, todo []*modelPlan, pngs map[string]string) error {
	e := todo[0].entry
	emitter.PhaseStart(e.ID, "render")
	loadMs := 0
	last := ""
	for _, p := range todo {
		key := p.scene.ID
		_, png, _ := cache.ArtifactPaths(key)
		if !cache.DecideImages(manifest, key, p.pictureHash, false) {
			res, err := render.RunVariant(ctx, execRunner, p.scene, cfg.Defaults, 0, p.seed, png, renderEnv)
			loadMs += res.LoadMs
			if err != nil {
				return fail(emitter, e.ID, fmt.Sprintf("render failed: %v", err))
			}
			ent := manifest.Scenes[key]
			ent.ImageHash, ent.PngPath = p.pictureHash, png
			manifest.UpdateEntry(key, ent)
		}
		pngs[p.name] = png
		last = png
	}
	emitter.PhaseEndLoad(e.ID, "render", last, loadMs)
	return nil
}

// makeModel sends the picture of a variant to the service, writes the model where the project keeps it, adds it to the library and records it.
func makeModel(ctx context.Context, emitter *events.Emitter, base string, services *Models3DServices, client *generate.Client, p *modelPlan, png string) (export.Models3DEntry, error) {
	src := png
	if p.source != "" {
		src = p.source
	}
	image, err := os.ReadFile(joinDir(base, src))
	if err != nil {
		return export.Models3DEntry{}, fail(emitter, p.entry.ID, fmt.Sprintf("the picture of the model cannot be read: %v", err))
	}
	start := time.Now()
	res, err := client.Generate(ctx, image, p.engine, p.detail, nil)
	if err != nil {
		if ctx.Err() != nil {
			return export.Models3DEntry{}, ctx.Err()
		}
		return export.Models3DEntry{}, fail(emitter, p.entry.ID, fmt.Sprintf("the model %s could not be made: %v", p.name, err))
	}
	dst := joinDir(base, p.file)
	if err := writeFileAtomic(dst, res.GLB); err != nil {
		return export.Models3DEntry{}, fail(emitter, p.entry.ID, fmt.Sprintf("export failed: %v", err))
	}
	line := export.Models3DEntry{Entry: p.entry.ID, Variant: p.variant, Name: p.name, File: p.file, Prompt: p.entry.Prompt, Picture: p.entry.Image, Engine: p.engine,
		EngineVersion: res.EngineVersion, Detail: res.DetailUsed, Seed: p.seed, Triangles: res.Triangles, Seconds: time.Since(start).Seconds(), Adult: p.entry.Adult, Key: p.key}
	if err := addToLibrary(base, services, p, line); err != nil {
		emitter.Warn(p.entry.ID, fmt.Sprintf("the model %s could not be added to the library: %v", p.name, err))
	}
	return line, nil
}

// addToLibrary puts the model of a manifest line in the library (replacing the one this project and entry made before).
func addToLibrary(base string, services *Models3DServices, p *modelPlan, line export.Models3DEntry) error {
	if services == nil || services.Library == nil {
		return nil
	}
	glb, err := os.ReadFile(joinDir(base, line.File))
	if err != nil {
		return err
	}
	info, _ := generate.Info(line.Engine)
	_, err = services.Library.AddGenerated(glb, assets3d.GenMeta{Name: line.Name, Prompt: line.Prompt, Picture: line.Picture, Engine: line.Engine,
		EngineVersion: line.EngineVersion, Detail: line.Detail, Licence: info.Licence, Attribution: info.Attribution, Project: p.project, Entry: line.Name, Adult: line.Adult})
	return err
}

// writeFileAtomic writes a file through a temporary name, so that a model is never half of one.
func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
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
	if err := os.Chmod(tmp.Name(), 0o644); err != nil { // a temporary file is private; the outputs of a project are readable like the others
		return err
	}
	return os.Rename(tmp.Name(), path)
}
