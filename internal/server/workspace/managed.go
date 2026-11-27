package workspace

// managed.go: the projects the server creates for the generation API, the image edits, the video-edit tools and the
// music-motion pieces. They have a reserved name and are not a user's project: the project list, the storage and the
// statistics by project leave them out. The families that create them (generation, music) use the functions here.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"gorm.io/gorm/clause"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"veritmo/internal/platform/store"
	"veritmo/internal/project/config"
	"veritmo/internal/server/api"
)

// Reserved prefixes of the projects the server creates for the generation API.
const (
	GenPrefix     = "api-gen-"
	EditPrefix    = "api-edit-"
	VideoPrefix   = "api-vid-"
	ManagedRecord = "generation.json"
	// PiecePrefix is the prefix of the projects of the music-motion pieces.
	PiecePrefix = "api-mm-"
	// ModelGenPrefix is the prefix of the projects of the 3D model generation API.
	ModelGenPrefix = "api-m3d-"
)

// IsManagedProject says whether a project belongs to the generation API: it is not a user's project.
func IsManagedProject(id string) bool {
	return strings.HasPrefix(id, GenPrefix) || strings.HasPrefix(id, EditPrefix) || strings.HasPrefix(id, VideoPrefix) || strings.HasPrefix(id, PiecePrefix) || strings.HasPrefix(id, ModelGenPrefix)
}

// ModeAnimation is the mode the runs and the statistics report for the runs of the music-motion pieces (their projects
// are the ones with PiecePrefix); it is not an output mode of a project.
const ModeAnimation config.OutputMode = "animation"

// IsAnimationProject says whether a project is the one of a music-motion piece.
func IsAnimationProject(id string) bool { return strings.HasPrefix(id, PiecePrefix) }

// ManagedKind is what a managed project is for.
type ManagedKind string

const (
	// KindGeneration is a project made by the generation API (a video from a text).
	KindGeneration ManagedKind = "generation"
	// KindImageEdit is a project made by the picture-edit API.
	KindImageEdit ManagedKind = "image_edit"
	// KindVideoEdit is a project made by the video-edit API.
	KindVideoEdit ManagedKind = "video_edit"
	// KindModelGeneration is a project made by the 3D model generation API (a model from a prompt or a picture).
	KindModelGeneration ManagedKind = "model_generation"
	// KindMusicMotion is the project of an animation of the music mode (a piece). It is listed only with `managed=all`.
	KindMusicMotion ManagedKind = "music_motion"
)

// ManagedInfo is generation.json: what the server remembers of a generation or an edit besides its scenes.yaml.
type ManagedInfo struct {
	ID          string          `json:"id"`
	Kind        ManagedKind     `json:"kind"`
	RunID       string          `json:"run_id"`
	CreatedAt   time.Time       `json:"created_at"`
	Request     json.RawMessage `json:"request"`
	ClipSeconds float64         `json:"clip_seconds,omitempty"`
	Estimate    int             `json:"estimate,omitempty"`
	// Inputs are the files besides the start picture the generation used, in order: kind and name in sources/.
	Inputs []ManagedInput `json:"inputs,omitempty"`
}

// ReadManaged reads the record of a managed project; false when there is none or it does not belong to id.
func (h *Handler) ReadManaged(id string) (*ManagedInfo, bool) {
	var row ManagedRow
	if err := h.projects.db.DB.Where("id = ?", id).Take(&row).Error; err != nil {
		return nil, false
	}
	var info ManagedInfo
	if json.Unmarshal([]byte(row.Doc), &info) != nil || info.ID != id {
		return nil, false
	}
	return &info, true
}

// managedRowOf is the row that keeps a record: its columns are what the lists filter and sort by, the document is the record as it is answered.
func managedRowOf(info *ManagedInfo) (ManagedRow, error) {
	doc, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return ManagedRow{}, err
	}
	var req struct {
		Client string `json:"client"`
	}
	_ = json.Unmarshal(info.Request, &req)
	return ManagedRow{ID: info.ID, Kind: string(info.Kind), RunID: info.RunID, Client: req.Client, CreatedAt: info.CreatedAt.UTC(), Doc: store.Text(doc)}, nil
}

// PutManaged keeps the record of a managed project (the project's client follows the one the request names when its definition names none).
func (h *Handler) PutManaged(info *ManagedInfo) error {
	row, err := managedRowOf(info)
	if err != nil {
		return err
	}
	if err := h.projects.db.DB.Clauses(clause.OnConflict{UpdateAll: true}).Create(&row).Error; err != nil {
		return err
	}
	_ = h.projects.TouchClient(info.ID)
	return nil
}

// NewManagedID makes an id for a managed project: the prefix of its kind, the time and three random bytes.
func NewManagedID(prefix string) string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return prefix + time.Now().UTC().Format("20060102-150405") + "-" + hex.EncodeToString(b)
}

// ManagedList lists the managed projects whose id starts with prefix (one kind), newest first.
func (h *Handler) ManagedList(prefix string) []*ManagedInfo {
	var rows []ManagedRow
	if err := h.projects.db.DB.Where("id LIKE ?", prefix+"%").Order("created_at DESC, id DESC").Find(&rows).Error; err != nil {
		return nil
	}
	return infosOf(rows)
}

// infosOf reads the records the rows hold (a row whose document is not a record is left out).
func infosOf(rows []ManagedRow) []*ManagedInfo {
	var out []*ManagedInfo
	for _, row := range rows {
		var info ManagedInfo
		if json.Unmarshal([]byte(row.Doc), &info) == nil && info.ID == row.ID {
			out = append(out, &info)
		}
	}
	return out
}

// CreateManaged writes the project of a generation or an edit, stores the start picture, launches its run and
// answers with the run. When anything fails nothing is left behind.
func (h *Handler) CreateManaged(info *ManagedInfo, scenes []byte, picture []byte, pictureName string) (Run, *LaunchError) {
	return h.CreateManagedStaged(info, scenes, func(sources string) error {
		if len(picture) == 0 {
			return nil
		}
		return os.WriteFile(filepath.Join(sources, pictureName), picture, 0o644)
	})
}

// CreateManagedStaged is CreateManaged with the sources written by stage (called with the sources folder, which
// exists), for the requests that bring a video as well as a picture.
func (h *Handler) CreateManagedStaged(info *ManagedInfo, scenes []byte, stage func(sources string) error) (Run, *LaunchError) {
	cleanup := func() { _ = h.removeProjectFiles(info.ID) } // the rows and the folder
	if err := h.materializeProject(info.ID, scenes); err != nil {
		cleanup()
		return Run{}, &LaunchError{Status: http.StatusInternalServerError, Code: api.CodeInternal, Msg: err.Error()}
	}
	sources := h.SourcesDir(info.ID)
	if err := os.MkdirAll(sources, 0o755); err != nil {
		cleanup()
		return Run{}, &LaunchError{Status: http.StatusInternalServerError, Code: api.CodeInternal, Msg: err.Error()}
	}
	if err := stage(sources); err != nil {
		cleanup()
		return Run{}, &LaunchError{Status: http.StatusInternalServerError, Code: api.CodeInternal, Msg: err.Error()}
	}
	rec, _, lerr := h.rm.launch(info.ID, RunRequest{GPU: h.GPUForRun(info.ID, RunRequest{})})
	if lerr != nil {
		cleanup()
		return Run{}, lerr
	}
	info.RunID = rec.ID
	if err := h.PutManaged(info); err != nil {
		_, _ = h.rm.cancelRun(rec.ID)
		cleanup()
		return Run{}, &LaunchError{Status: http.StatusInternalServerError, Code: api.CodeInternal, Msg: err.Error()}
	}
	return rec, nil
}

// RemoveManaged cancels the run of a generation or an edit and deletes it with its files and run history.
func (h *Handler) RemoveManaged(id string) *LaunchError {
	if runID, busy := h.ProjectBusy(id); busy {
		if _, lerr := h.rm.cancelRun(runID); lerr != nil && lerr.Status != http.StatusConflict {
			return lerr
		}
		deadline := time.Now().Add(15 * time.Second)
		for {
			if _, still := h.ProjectBusy(id); !still {
				break
			}
			if time.Now().After(deadline) {
				return &LaunchError{Status: http.StatusConflict, Code: api.CodeConflict, Msg: "the run did not stop in time; try again"}
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	if err := h.removeProjectFiles(id); err != nil {
		return &LaunchError{Status: http.StatusInternalServerError, Code: api.CodeInternal, Msg: err.Error()}
	}
	f := runDeleteFilter{Project: id, States: terminalStates}
	if _, _, _, err := h.rm.deleteRuns(nil, &f, false); err != nil {
		slog.Warn("cannot delete the run history", "project", id, "err", err)
	}
	return nil
}

// ManagedInput is what a generation remembers of an input: its kind and its name in sources/, in order.
type ManagedInput struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}
