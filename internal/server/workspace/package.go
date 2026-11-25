package workspace

// package.go is the full project package: what the export writes (scenes.yaml,
// sources/ and the recipe) and the import that reads it back, tells what the
// machine has and lacks, and creates the project so that only the generation
// is left to launch (see specs/project-management).

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
	"veritmo/internal/server/modelcat"

	"veritmo/internal/platform/media"
	"veritmo/internal/project/config"
	"veritmo/internal/project/importer"
	"veritmo/internal/server/api"
	"veritmo/internal/video/motion"
)

// WriteProjectPackage writes the package of the project in projectDir: its
// scenes.yaml, sources/, a README and the recipe (RECIPE.md and recipe.json).
// assetsRoot is the directory holding models/ and voices/, used to say which
// files exist on this machine; lang is "es" or "en" (the language of RECIPE.md).
func WriteProjectPackage(cat []modelcat.Entry, w io.Writer, projectDir, assetsRoot, lang string) error {
	yamlData, err := os.ReadFile(filepath.Join(projectDir, "scenes.yaml"))
	if err != nil {
		return fmt.Errorf("export: cannot read scenes.yaml: %w", err)
	}
	cfg, err := config.LoadBytes(yamlData)
	if err != nil {
		return err
	}
	r := buildRecipe(cat, yamlData, cfg, assetsRoot, normLang(lang), time.Now())
	return importer.WriteProject(w, projectDir,
		importer.PackageFile{Name: "RECIPE.md", Data: r.markdown(normLang(lang))},
		importer.PackageFile{Name: "recipe.json", Data: r.json()},
	)
}

func normLang(lang string) string {
	if lang == "es" {
		return "es"
	}
	return "en"
}

// --- reading a package ---------------------------------------------------------------------------

const (
	maxPackageBytes   = 1 << 30 // the upload, and the sum of what it unpacks to
	maxPackageEntries = 2000
)

// Limits of what is read of a package. The sources are not read here: they stay in the zip and are copied to disk one by one.
const (
	maxPackageYAML   = api.MaxConfigBody // scenes.yaml
	maxPackageRecipe = 4 << 20           // recipe.json
	maxPackageMotion = 8 << 20           // all the pages of motion graphics together
)

// packageContent is what a package holds. The small files (scenes.yaml, the recipe, the motion pages) are read into
// memory; the sources are references to entries of the zip, which stays open (the caller closes the file it reads).
type packageContent struct {
	yaml    []byte
	sources map[string]*zip.File
	// motion holds the pages of motion graphics and overlays, and their details, by file name.
	motion map[string][]byte
	recipe *recipe
}

// readLimited reads an entry of the zip, refusing one that unpacks to more than limit bytes.
func readLimited(f *zip.File, limit int64) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%q is larger than %d MB", f.Name, limit>>20)
	}
	return b, nil
}

// copySource writes the source name of the package to dst (through a temporary file next to it, so a failure leaves
// nothing half written), never more than the size the entry declares.
func (pc *packageContent) copySource(name, dst string) error {
	f := pc.sources[name]
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	dst = media.Real(dst)
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".package-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	n, err := io.Copy(tmp, io.LimitReader(rc, int64(f.UncompressedSize64)+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if n > int64(f.UncompressedSize64) {
		return fmt.Errorf("%q is larger than it says", name)
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dst)
}

// readPackage reads a project package of size bytes. Only scenes.yaml, recipe.json and the files directly under
// sources/ and the motion folder are taken, whatever else the zip holds is ignored, and no entry may name a path
// outside them. What the entries unpack to, added up, is limited to maxPackageBytes.
func readPackage(ra io.ReaderAt, size int64) (*packageContent, error) {
	zr, err := zip.NewReader(ra, size)
	if err != nil {
		return nil, fmt.Errorf("not a zip file: %w", err)
	}
	if len(zr.File) > maxPackageEntries {
		return nil, fmt.Errorf("the package has too many files (%d)", len(zr.File))
	}
	pc := &packageContent{sources: map[string]*zip.File{}, motion: map[string][]byte{}}
	var total, motionTotal int64
	for _, f := range zr.File {
		name := path.Clean(strings.ReplaceAll(f.Name, "\\", "/"))
		if strings.HasPrefix(name, "/") || strings.HasPrefix(name, "../") || name == ".." {
			return nil, fmt.Errorf("the package has an unsafe path %q", f.Name)
		}
		if f.FileInfo().IsDir() {
			continue
		}
		want := name == "scenes.yaml" || name == "recipe.json" ||
			(strings.HasPrefix(name, config.SourcesSubdir+"/") && path.Dir(name) == config.SourcesSubdir) ||
			(path.Dir(name) == motion.Dir && importer.IsMotionFile(path.Base(name)))
		if !want {
			continue
		}
		total += int64(f.UncompressedSize64)
		if total > maxPackageBytes {
			return nil, fmt.Errorf("the package is too big (over %d MB unpacked)", maxPackageBytes>>20)
		}
		switch {
		case name == "scenes.yaml":
			if pc.yaml, err = readLimited(f, maxPackageYAML); err != nil {
				return nil, err
			}
		case name == "recipe.json":
			b, err := readLimited(f, maxPackageRecipe)
			if err != nil {
				return nil, err
			}
			var r recipe
			if json.Unmarshal(b, &r) == nil && r.Format == recipeFormat {
				pc.recipe = &r
			}
		case path.Dir(name) == motion.Dir:
			motionTotal += int64(f.UncompressedSize64)
			if motionTotal > maxPackageMotion {
				return nil, fmt.Errorf("the motion pages of the package are larger than %d MB", maxPackageMotion>>20)
			}
			b, err := readLimited(f, maxPackageMotion)
			if err != nil {
				return nil, err
			}
			pc.motion[path.Base(name)] = b
		default:
			base := path.Base(name)
			if base != "." && base != ".." && !strings.ContainsAny(base, `/\`) {
				pc.sources[base] = f
			}
		}
	}
	if pc.yaml == nil {
		return nil, fmt.Errorf("the package has no scenes.yaml")
	}
	return pc, nil
}

// --- the report of an import ---------------------------------------------------------------------

// installGroup is a catalog entry that has files missing on this machine.
type installGroup struct {
	CatalogID string   `json:"catalog_id"`
	Name      string   `json:"name"`
	SizeBytes int64    `json:"size_bytes"`
	Missing   []string `json:"missing"`
	// TotalFiles is how many files the entry has: when fewer are missing, the size of the entry is not what is left to download.
	TotalFiles    int    `json:"total_files"`
	RequiresToken string `json:"requires_token,omitempty"`
}

type packageReport struct {
	Project     string            `json:"project"`
	SuggestedID string            `json:"suggested_id"`
	IDTaken     bool              `json:"id_taken"`
	Mode        config.OutputMode `json:"mode"`
	Scenes      int               `json:"scenes"`
	Tracks      int               `json:"tracks"`
	Sources     int               `json:"sources"`
	Recipe      *recipe           `json:"recipe"`
	Install     []installGroup    `json:"install"`
	// Unknown lists the missing files the catalog does not know: they have to be found by name.
	Unknown []string `json:"unknown"`
	// Problems are what the project's own validation says once its sources are in place (a clip or picture the
	// package does not carry, an invalid value...), apart from the models and voices counted above.
	Problems []string `json:"problems"`
	// Ready is true when nothing is missing: the project can be generated as soon as it is created.
	Ready bool `json:"ready"`
}

func (h *Handler) packageReport(pc *packageContent, lang string) (*packageReport, *config.Config, error) {
	cfg, err := config.LoadBytes(pc.yaml)
	if err != nil {
		return nil, nil, err
	}
	// The recipe is rebuilt from scenes.yaml here: it is the truth about this package, and a package
	// from an older version has no recipe.json. What the package's own recipe adds is kept below.
	r := buildRecipe(h.host.Catalog(), pc.yaml, cfg, h.host.AssetsRoot(), lang, time.Now())
	if pc.recipe != nil {
		r.ExportedAt = pc.recipe.ExportedAt
	}
	rep := &packageReport{Project: cfg.Project, Mode: cfg.Output.Mode, Scenes: len(cfg.Scenes), Tracks: len(cfg.Tracks), Sources: len(pc.sources), Recipe: r}
	rep.SuggestedID = suggestProjectID(cfg.Project)
	rep.IDTaken = h.ProjectExists(rep.SuggestedID) || api.DirExists(h.ProjectDir(rep.SuggestedID))
	groups := map[string]*installGroup{}
	var order []string
	for _, f := range r.Files {
		if f.Installed {
			continue
		}
		if f.CatalogID == "" {
			rep.Unknown = append(rep.Unknown, f.Path)
			continue
		}
		g := groups[f.CatalogID]
		if g == nil {
			e, _ := findEntry(h.host.Catalog(), f.CatalogID)
			g = &installGroup{CatalogID: f.CatalogID, Name: f.CatalogName, SizeBytes: e.SizeBytes, TotalFiles: len(e.Files), RequiresToken: e.RequiresToken}
			groups[f.CatalogID] = g
			order = append(order, f.CatalogID)
		}
		g.Missing = append(g.Missing, f.Name)
	}
	for _, id := range order {
		rep.Install = append(rep.Install, *groups[id])
	}
	// What the project's validation says with its sources in place. The models and voices that are missing
	// were already counted, so those messages are not repeated.
	if tmp, err := os.MkdirTemp("", "package-check-"); err == nil {
		defer os.RemoveAll(tmp)
		if os.WriteFile(filepath.Join(tmp, "scenes.yaml"), pc.yaml, 0o644) == nil {
			_ = os.MkdirAll(filepath.Join(tmp, config.SourcesSubdir), 0o755)
			for name := range pc.sources {
				// Validation only looks for the sources: an empty file stands for each one, so the report costs no disk.
				_ = os.WriteFile(filepath.Join(tmp, config.SourcesSubdir, name), nil, 0o644)
			}
			h.linkSharedAssets(tmp)
			if verr := h.validateInDir(cfg, tmp); verr != nil {
				for _, line := range strings.Split(verr.Error(), "\n") {
					line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "config: "))
					if line != "" && !mentionsMissingAsset(line, r.Files) {
						rep.Problems = append(rep.Problems, line)
					}
				}
			}
		}
	}
	rep.Ready = len(rep.Install) == 0 && len(rep.Unknown) == 0 && len(rep.Problems) == 0
	return rep, cfg, nil
}

// mentionsMissingAsset says whether a validation message is only about a model or voice file the report already
// lists as missing (matched by file name).
func mentionsMissingAsset(msg string, files []recipeFile) bool {
	for _, f := range files {
		if !f.Installed && strings.Contains(msg, f.Name) {
			return true
		}
	}
	return false
}

// suggestProjectID turns the project's name into a valid project id.
func suggestProjectID(name string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		case r == ' ':
			b.WriteRune('-')
		}
	}
	id := strings.TrimLeft(b.String(), "._-")
	if len(id) > 64 {
		id = id[:64]
	}
	if id == "" {
		id = "imported"
	}
	return id
}

// spoolBody saves the uploaded package to a temporary file, so it is never held whole in memory. The caller closes the
// returned file and removes it with cleanup.
func spoolBody(w http.ResponseWriter, r *http.Request) (f *os.File, size int64, cleanup func(), ok bool) {
	api.RaiseBodyLimit(w, r, maxPackageBytes)
	tmp, err := os.CreateTemp("", "package-upload-*.zip")
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return nil, 0, nil, false
	}
	cleanup = func() { tmp.Close(); os.Remove(tmp.Name()) }
	n, err := io.Copy(tmp, r.Body)
	if err != nil {
		cleanup()
		api.WriteError(w, http.StatusRequestEntityTooLarge, api.CodeInvalid, fmt.Sprintf("cannot read the package: %v", err))
		return nil, 0, nil, false
	}
	return tmp, n, cleanup, true
}

// POST /api/import/package/preview?lang=es|en  (body: the .zip) — what the package brings and what this machine lacks.
func (h *Handler) handlePackagePreview(w http.ResponseWriter, r *http.Request) {
	zf, size, cleanup, ok := spoolBody(w, r)
	if !ok {
		return
	}
	defer cleanup()
	pc, err := readPackage(zf, size)
	if err != nil {
		api.WriteError(w, http.StatusUnprocessableEntity, api.CodeValidation, err.Error())
		return
	}
	if pcfg, perr := config.LoadBytes(pc.yaml); perr == nil && h.host.RefuseAdultConfig(w, pcfg.Adult) {
		return
	}
	rep, _, err := h.packageReport(pc, normLang(r.URL.Query().Get("lang")))
	if err != nil {
		api.WriteError(w, http.StatusUnprocessableEntity, api.CodeValidation, err.Error())
		return
	}
	api.WriteJSON(w, http.StatusOK, rep)
}

// POST /api/import/package/confirm?id=<project id>  (body: the .zip) — creates the project. Models that are
// missing do not stop it: the report says what to install, and the project is complete as soon as it is.
func (h *Handler) handlePackageConfirm(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if !api.ProjectIDPattern.MatchString(id) {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid project id %q", id))
		return
	}
	if h.ProjectExists(id) || api.DirExists(h.ProjectDir(id)) {
		api.WriteError(w, http.StatusConflict, api.CodeConflict, fmt.Sprintf("project %q already exists", id))
		return
	}
	zf, size, cleanup, ok := spoolBody(w, r)
	if !ok {
		return
	}
	defer cleanup()
	pc, err := readPackage(zf, size)
	if err != nil {
		api.WriteError(w, http.StatusUnprocessableEntity, api.CodeValidation, err.Error())
		return
	}
	if pcfg, perr := config.LoadBytes(pc.yaml); perr == nil && h.host.RefuseAdultConfig(w, pcfg.Adult) {
		return
	}
	rep, _, err := h.packageReport(pc, normLang(r.URL.Query().Get("lang")))
	if err != nil {
		api.WriteError(w, http.StatusUnprocessableEntity, api.CodeValidation, err.Error())
		return
	}
	if err := h.materializeProject(id, pc.yaml); err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	if len(pc.sources) > 0 {
		dir := filepath.Join(h.ProjectDir(id), config.SourcesSubdir)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			_ = h.removeProjectFiles(id)
			api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
			return
		}
		for name := range pc.sources {
			if err := pc.copySource(name, filepath.Join(dir, name)); err != nil {
				_ = h.removeProjectFiles(id)
				api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
				return
			}
		}
	}
	if len(pc.motion) > 0 {
		dir := filepath.Join(h.ProjectDir(id), motion.Dir)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			_ = h.removeProjectFiles(id)
			api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
			return
		}
		for name, b := range pc.motion {
			if err := api.AtomicWrite(filepath.Join(dir, name), b); err != nil {
				_ = h.removeProjectFiles(id)
				api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
				return
			}
		}
	}
	api.WriteJSON(w, http.StatusCreated, map[string]any{"id": id, "ready": rep.Ready, "install": rep.Install, "unknown": rep.Unknown, "problems": rep.Problems})
}
