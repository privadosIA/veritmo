package workspace

// resultavail.go says whether a run still has something to show. A run stays in the history
// and in the statistics after its project or its files are gone, but no screen may link a
// gallery, a studio or a video that no longer exists.

import (
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"veritmo/internal/project/config"
)

// RunView is a run as the API shows it.
type RunView struct {
	Run
	// ResultAvailable is true while the project still holds something this run's mode produced.
	ResultAvailable bool `json:"result_available"`
}

// resultCheck answers resultAvailable for many runs, reading each project once.
type resultCheck struct {
	h     *Handler
	modes map[string]config.OutputMode // project -> mode ("" when the project does not exist)
}

func (h *Handler) newResultCheck() *resultCheck {
	return &resultCheck{h: h, modes: map[string]config.OutputMode{}}
}

func (c *resultCheck) mode(project string) config.OutputMode {
	if m, ok := c.modes[project]; ok {
		return m
	}
	var m config.OutputMode
	if data, err := c.h.ProjectDefinition(project); err == nil {
		var probe struct {
			Output struct {
				Mode config.OutputMode `yaml:"mode"`
			} `yaml:"output"`
		}
		if yaml.Unmarshal(data, &probe) == nil {
			m = probe.Output.Mode
			if m == "" {
				m = config.ModeVideo
			}
		}
	}
	c.modes[project] = m
	return m
}

var (
	pictureExts = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".webp": true}
	songExts    = map[string]bool{".mp3": true, ".wav": true, ".flac": true, ".ogg": true}
	modelExts   = map[string]bool{".glb": true}
)

// available says whether the run's project still holds a result of the run's kind: the final
// video or a scene clip (video mode), a picture (images mode) a song (music mode) or a model (models3d mode).
func (c *resultCheck) available(r Run) bool {
	mode := c.mode(r.Project)
	if mode == "" || r.State != "succeeded" || r.Artifact == "" {
		return false
	}
	// A run that ended in an MP4 made a video, whatever the project is set to produce now: a project
	// switched to images afterwards still holds the video that run made.
	if strings.EqualFold(filepath.Ext(r.Artifact), ".mp4") {
		mode = config.ModeVideo
	}
	dir := c.h.ProjectDir(r.Project)
	switch mode {
	case config.ModeVideo:
		if fi, err := os.Stat(filepath.Join(dir, filepath.FromSlash(r.Artifact))); err == nil && fi.Mode().IsRegular() && outputPathOf(r.Artifact) != "" {
			return true
		}
		return hasFileWith(filepath.Join(dir, "out", "clips"), map[string]bool{".mp4": true})
	case config.ModeImages:
		return hasFileWith(outputDirOf(dir, r.Artifact), pictureExts)
	case config.ModeMusic:
		return hasFileWith(outputDirOf(dir, r.Artifact), songExts)
	case config.ModeModels3D:
		return hasFileWith(outputDirOf(dir, r.Artifact), modelExts)
	}
	return false
}

// outputDirOf is the out/<name> folder of a manifest artifact ("" when it has none).
func outputDirOf(dir, artifact string) string {
	p := outputPathOf(artifact)
	if p == "" || strings.Count(artifact, "/") < 2 {
		return ""
	}
	return filepath.Join(dir, filepath.FromSlash(p))
}

func hasFileWith(dir string, exts map[string]bool) bool {
	if dir == "" {
		return false
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.Type().IsRegular() && exts[strings.ToLower(filepath.Ext(e.Name()))] && !strings.HasPrefix(e.Name(), ".") {
			return true
		}
	}
	return false
}

// viewsOf adds result_available to a list of runs.
func (h *Handler) viewsOf(runs []Run) []RunView {
	c := h.newResultCheck()
	out := make([]RunView, 0, len(runs))
	for _, r := range runs {
		v := RunView{Run: r, ResultAvailable: c.available(r)}
		if IsAnimationProject(r.Project) {
			v.Mode = ModeAnimation // the run's project is not listed, so the run says what it is
		}
		out = append(out, v)
	}
	return out
}
