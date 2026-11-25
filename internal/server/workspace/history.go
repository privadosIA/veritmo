package workspace

// history.go implements the durable half of the run-control capability:
// GORM models for runs and their verbatim event lines, stored in SQLite
// through the pure-Go driver (design D4 — no CGO, swappable database), or in
// a MySQL or Postgres server that several servers can share (see
// specs/database-choice).

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"veritmo/internal/platform/store"
	"veritmo/internal/project/config"
)

// RunState is where a run is in its life. It is written in the API and in the history as its text.
type RunState string

// The states of a run.
const (
	// StateQueued is a run waiting for its turn.
	StateQueued RunState = "queued"
	// StateRunning is a run the pipeline is working on.
	StateRunning RunState = "running"
	// StateSucceeded is a run that finished and made its result.
	StateSucceeded RunState = "succeeded"
	// StateFailed is a run that stopped with an error.
	StateFailed RunState = "failed"
	// StateCancelled is a run somebody stopped.
	StateCancelled RunState = "cancelled"
	// StateInterrupted is a run that was queued or running when the server stopped.
	StateInterrupted RunState = "interrupted"
)

// Run is one pipeline run: its options, lifecycle timestamps and outcome.
type Run struct {
	ID      string   `gorm:"primaryKey;size:64" json:"id"`
	Project string   `gorm:"size:191;index" json:"project"`
	State   RunState `gorm:"size:32;index" json:"state"`
	// Server is the server that made the run, for a history that several share (empty before it was recorded).
	Server     string `gorm:"size:100;index" json:"server,omitempty"`
	GPU        string `gorm:"size:191" json:"gpu"`
	KeepTmp    bool   `json:"keep_tmp"`
	Force      bool   `json:"force"`
	Only       string `gorm:"size:191" json:"only,omitempty"`
	VRAMWarnMB int    `json:"vram_warn_mb"`
	// Overlap is the look-ahead of the overlap option of this run (0: one phase after another).
	Overlap int               `json:"overlap,omitempty"`
	Mode    config.OutputMode `gorm:"size:32" json:"mode,omitempty"`
	// Client is the dashboard that asked for the run through the generation API, copied from the scenes.yaml of
	// its project (empty for ordinary projects and for what was made before clients existed; see clientOf).
	Client     string     `gorm:"size:32;index" json:"client,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	Artifact   string     `gorm:"size:255" json:"artifact,omitempty"`
	Error      string     `json:"error,omitempty"`
}

// RunEvent is one pipeline event exactly as emitted: Line holds the
// verbatim JSON object from the --json stream (no decomposition, so
// replay fidelity is byte-for-byte; see specs/run-event-streaming).
type RunEvent struct {
	ID    uint   `gorm:"primaryKey" json:"-"`
	RunID string `gorm:"size:64;index:idx_run_seq,unique,priority:1" json:"run_id"`
	Seq   int    `gorm:"index:idx_run_seq,unique,priority:2" json:"seq"`
	Line  string `json:"line"`
}

// terminalStates are the states a run can be deleted in.
var terminalStates = []RunState{StateSucceeded, StateFailed, StateCancelled, StateInterrupted}

// joinStates writes the states as a list of words.
func joinStates(states []RunState) string {
	words := make([]string, len(states))
	for i, s := range states {
		words[i] = string(s)
	}
	return strings.Join(words, ", ")
}

func isTerminalState(s RunState) bool {
	for _, t := range terminalStates {
		if s == t {
			return true
		}
	}
	return false
}

// historyStore wraps the GORM handle with the queries the server needs.
type historyStore struct {
	// visible, when set, says whether the runs of a project are shown (the content mode); nil shows all.
	visible func(project string) bool
	// modeOf says the kind of a project (video, images or music); empty when it cannot be read. The statistics
	// group and filter the runs with it.
	modeOf func(project string) string
	db     *gorm.DB
	// server is how this server is called in the history (see Run.Server); driver is the engine it is kept in.
	server, driver string
	// afterEventsDeleted is a test seam: it runs inside the delete
	// transaction, after the events and samples are gone and before the runs go, so a
	// test can prove that a failure leaves nothing half deleted.
	afterEventsDeleted func() error
}

// historyModels are the tables of the history, which RegisterStore gives to the data layer (the export and the copy to another engine walk
// them); the migration that creates them is in history_migration.go.
func historyModels() []any {
	return []any{&Run{}, &RunEvent{}, &RunSample{}, &Review{}, &Finding{}, &CalibrationSample{}}
}

// RegisterStore tells a registry what the run history owns: its tables and the migration that creates them. The program calls it when it
// builds the registry of its database (see server.DataRegistry); a test of this package builds its own.
func RegisterStore(r *store.Registry) {
	r.AddModels(historyModels()...)
	r.AddMigration(historyMigration())
	r.AddModels(&ProjectRow{}, &ProjectVersion{}, &ProjectPage{}, &ManagedRow{})
	r.AddMigration(projectsMigration())
	r.AddMigration(managedRecordsMigration())
	r.AddModels(&SongRow{}, &PlaylistRow{}, &PlaylistSong{})
	r.AddMigration(songsMigration())
}

// openHistoryOn builds the history on a database the caller has opened and migrated, as the server called server (see Run.Server),
// and marks any run left queued or running by a previous process as interrupted (startup recovery).
func openHistoryOn(st *store.Store, server string) (*historyStore, error) {
	h := &historyStore{db: st.DB, server: server, driver: st.Driver}
	if err := h.adoptUnowned(); err != nil {
		return nil, err
	}
	if err := h.recoverInterrupted(); err != nil {
		return nil, err
	}
	return h, nil
}

// adoptUnowned gives this server the rows recorded before the history knew which server made them: they can only
// be this one's (the history was a file of its own until now).
func (h *historyStore) adoptUnowned() error {
	if h.server == "" {
		return nil
	}
	for _, m := range []any{&Run{}, &Review{}, &CalibrationSample{}} {
		if err := h.db.Model(m).Where("server = ? OR server IS NULL", "").Update("server", h.server).Error; err != nil {
			return fmt.Errorf("cannot record which server made the history: %w", err)
		}
	}
	return nil
}

// newInstanceSuffix is chosen at the start of a server that keeps its history in a database other servers use: it ends
// the ids of runs and reviews so that two servers never make the same one (the counters start at 1 in each). A server
// with a history of its own has none.
func newInstanceSuffix(shared bool) string {
	if !shared {
		return ""
	}
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return "-" + hex.EncodeToString(b)
}

// recoverInterrupted marks stale non-terminal runs as interrupted.
func (h *historyStore) recoverInterrupted() error {
	now := time.Now().UTC()
	// Only this server's: in a history that several share, another server's running runs are running.
	res := h.db.Model(&Run{}).
		Where("state IN ? AND (server = ? OR server IS NULL OR server = '')", []RunState{StateQueued, StateRunning}, h.server).
		Updates(map[string]any{"state": StateInterrupted, "finished_at": &now})
	if res.Error != nil {
		return fmt.Errorf("cannot recover interrupted runs: %w", res.Error)
	}
	return nil
}

func (h *historyStore) createRun(r *Run) error {
	if r.Server == "" {
		r.Server = h.server
	}
	return h.db.Create(r).Error
}

func (h *historyStore) saveRun(r *Run) error {
	return h.db.Save(r).Error
}

func (h *historyStore) appendEvent(runID string, seq int, line string) error {
	return h.db.Create(&RunEvent{RunID: runID, Seq: seq, Line: line}).Error
}

func (h *historyStore) getRun(id string) (*Run, error) {
	var r Run
	if err := h.db.First(&r, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &r, nil
}

// listRuns returns runs newest-first, optionally filtered by project.
func (h *historyStore) listRuns(project string, limit int) ([]Run, error) {
	q := h.db.Order("created_at DESC, id DESC")
	if project != "" {
		q = q.Where("project = ?", project)
	}
	if limit > 0 && h.visible == nil {
		q = q.Limit(limit)
	}
	var runs []Run
	if err := q.Find(&runs).Error; err != nil {
		return nil, err
	}
	if h.visible != nil { // the limit counts the runs that are shown
		runs = h.onlyVisible(runs)
		if limit > 0 && len(runs) > limit {
			runs = runs[:limit]
		}
	}
	return runs, nil
}

// onlyVisible leaves out the runs of the projects the server does not show.
func (h *historyStore) onlyVisible(runs []Run) []Run {
	seen := map[string]bool{}
	out := runs[:0:0]
	for _, r := range runs {
		v, ok := seen[r.Project]
		if !ok {
			v = h.visible(r.Project)
			seen[r.Project] = v
		}
		if v {
			out = append(out, r)
		}
	}
	return out
}

// listEvents returns a run's stored event lines in emission order.
func (h *historyStore) listEvents(runID string) ([]string, error) {
	var evs []RunEvent
	if err := h.db.Where("run_id = ?", runID).Order("seq ASC").Find(&evs).Error; err != nil {
		return nil, err
	}
	lines := make([]string, len(evs))
	for i, e := range evs {
		lines[i] = e.Line
	}
	return lines, nil
}

// runDeleteFilter selects terminal runs to delete; zero values mean "any".
type runDeleteFilter struct {
	Project string
	States  []RunState // terminal states only; empty means every terminal state
	Before  *time.Time
}

// deleteChunk keeps each IN list well under SQLite's variable limit.
const deleteChunk = 500

// runStates returns the state of each existing run among ids (unknown ids are
// simply absent).
func (h *historyStore) runStates(ids []string) (map[string]string, error) {
	out := make(map[string]string, len(ids))
	for i := 0; i < len(ids); i += deleteChunk {
		end := min(i+deleteChunk, len(ids))
		var rows []Run
		if err := h.db.Select("id", "state").Where("id IN ?", ids[i:end]).Find(&rows).Error; err != nil {
			return nil, err
		}
		for _, r := range rows {
			out[r.ID] = string(r.State)
		}
	}
	return out, nil
}

// terminalRunIDs returns the ids of the terminal runs a filter selects.
func (h *historyStore) terminalRunIDs(f runDeleteFilter) ([]string, error) {
	states := f.States
	if len(states) == 0 {
		states = terminalStates
	}
	q := h.db.Model(&Run{}).Where("state IN ?", states)
	if f.Project != "" {
		q = q.Where("project = ?", f.Project)
	}
	if f.Before != nil {
		q = q.Where("created_at < ?", f.Before.UTC())
	}
	var ids []string
	if err := q.Pluck("id", &ids).Error; err != nil {
		return nil, err
	}
	return ids, nil
}

// deleteRuns removes the runs and all their events in one transaction: either
// everything goes or nothing does. It never looks at a run's state; callers
// pass only runs they have checked are finished.
func (h *historyStore) deleteRuns(ids []string) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	deleted := 0
	err := h.db.Transaction(func(tx *gorm.DB) error {
		for i := 0; i < len(ids); i += deleteChunk {
			end := min(i+deleteChunk, len(ids))
			if err := tx.Where("run_id IN ?", ids[i:end]).Delete(&RunEvent{}).Error; err != nil {
				return err
			}
		}
		for i := 0; i < len(ids); i += deleteChunk {
			end := min(i+deleteChunk, len(ids))
			if err := tx.Where("run_id IN ?", ids[i:end]).Delete(&RunSample{}).Error; err != nil {
				return err
			}
		}
		if h.afterEventsDeleted != nil {
			if err := h.afterEventsDeleted(); err != nil {
				return err
			}
		}
		for i := 0; i < len(ids); i += deleteChunk {
			end := min(i+deleteChunk, len(ids))
			res := tx.Where("id IN ?", ids[i:end]).Delete(&Run{})
			if res.Error != nil {
				return res.Error
			}
			deleted += int(res.RowsAffected)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return deleted, nil
}
