package music

// piecestore.go: where the pieces of the music motion mode are kept (spec music-motion, "A piece is kept in the database"): a row for the piece
// and one row for each of its documents (design, script, song map, proposal, scene batch and review). The song, the vocal stem, the frames and
// what a render writes stay in the folder of the piece.

import (
	"encoding/json"
	"log/slog"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"veritmo/internal/music/musicmotion"
	"veritmo/internal/platform/store"
)

// pieceRow is a piece as the database keeps it: what a list filters or sorts by as columns, and the whole of piece.json as the document the
// piece is read from.
type pieceRow struct {
	ID           string `gorm:"primaryKey;size:191"`
	Title        string `gorm:"size:255"`
	Project      string `gorm:"size:191"`
	TrackID      string `gorm:"size:191"`
	Variant      int
	Style        string `gorm:"size:32"`
	Level        string `gorm:"size:32"`
	FPS          int
	Width        int
	Height       int
	Client       string `gorm:"size:64;index"`
	Preset       string `gorm:"size:191"`
	Reference    string `gorm:"size:191"`
	ScriptSource string `gorm:"size:32"`
	RunID        string `gorm:"size:64"`
	Renders      int
	Recorded     bool
	SizeBytes    *int64
	CreatedAt    time.Time `gorm:"index"`
	UpdatedAt    time.Time
	Doc          store.JSON[pieceInfo]
}

// TableName is the table of the pieces.
func (pieceRow) TableName() string { return "mm_pieces" }

// pieceDocRow is a document of a piece: the JSON of its design, its script, the map of its song, its proposal, its scene batch or its review.
type pieceDocRow struct {
	PieceID   string `gorm:"primaryKey;size:191"`
	Kind      string `gorm:"primaryKey;size:32"`
	Body      store.Text
	UpdatedAt time.Time
}

// TableName is the table of the documents of the pieces.
func (pieceDocRow) TableName() string { return "mm_piece_documents" }

func piecesMigration() *store.Migration {
	return &store.Migration{
		ID: "202611191100_mm_pieces",
		Migrate: func(tx *gorm.DB) error {
			// The structs are copied here: the migration must do the same whatever the rows become.
			type pieceRow struct {
				ID           string `gorm:"primaryKey;size:191"`
				Title        string `gorm:"size:255"`
				Project      string `gorm:"size:191"`
				TrackID      string `gorm:"size:191"`
				Variant      int
				Style        string `gorm:"size:32"`
				Level        string `gorm:"size:32"`
				FPS          int
				Width        int
				Height       int
				Client       string `gorm:"size:64;index"`
				Preset       string `gorm:"size:191"`
				Reference    string `gorm:"size:191"`
				ScriptSource string `gorm:"size:32"`
				RunID        string `gorm:"size:64"`
				Renders      int
				Recorded     bool
				SizeBytes    *int64
				CreatedAt    time.Time `gorm:"index"`
				UpdatedAt    time.Time
				Doc          store.Text
			}
			type pieceDocRow struct {
				PieceID   string `gorm:"primaryKey;size:191"`
				Kind      string `gorm:"primaryKey;size:32"`
				Body      store.Text
				UpdatedAt time.Time
			}
			m := tx.Migrator()
			if !m.HasTable("mm_pieces") {
				if err := tx.Table("mm_pieces").Migrator().CreateTable(&pieceRow{}); err != nil {
					return err
				}
			}
			if !m.HasTable("mm_piece_documents") {
				if err := tx.Table("mm_piece_documents").Migrator().CreateTable(&pieceDocRow{}); err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			if err := tx.Migrator().DropTable("mm_piece_documents"); err != nil {
				return err
			}
			return tx.Migrator().DropTable("mm_pieces")
		},
	}
}

// RegisterStore tells a registry what the pieces own in the database. The server calls it when it builds the registry of its database
// (server.DataRegistry).
func RegisterStore(r *store.Registry, projectsDir string) {
	r.AddModels(&pieceRow{}, &pieceDocRow{})
	r.AddMigration(piecesMigration())
	r.AddImporter(pieceImporter{projects: projectsDir})
}

// pieceStore reads and writes the rows of the pieces.
type pieceStore struct{ st *store.Store }

func rowOfPiece(info *pieceInfo) pieceRow {
	return pieceRow{ID: info.ID, Title: info.Title, Project: info.Project, TrackID: info.TrackID, Variant: info.Variant, Style: info.Style, Level: info.Level,
		FPS: info.FPS, Width: info.Width, Height: info.Height, Client: info.Client, Preset: info.Preset, Reference: info.Reference,
		ScriptSource: info.ScriptSource, RunID: info.RunID, Renders: info.Renders, Recorded: info.Recorded, SizeBytes: info.SizeBytes,
		CreatedAt: info.CreatedAt, Doc: store.JSON[pieceInfo]{V: *info}}
}

func (p pieceStore) get(id string) (*pieceInfo, bool) {
	var r pieceRow
	if err := p.st.DB.Where("id = ?", id).Take(&r).Error; err != nil {
		return nil, false
	}
	info := r.Doc.V
	if info.ID != id {
		return nil, false
	}
	return &info, true
}

// put keeps a piece, replacing the one with its id.
func (p pieceStore) put(info *pieceInfo) error {
	r := rowOfPiece(info)
	return p.st.DB.Save(&r).Error
}

func putDocs(tx *gorm.DB, id string, docs map[string][]byte) error {
	for kind, body := range docs {
		d := pieceDocRow{PieceID: id, Kind: kind, Body: store.Text(body)}
		if err := tx.Save(&d).Error; err != nil {
			return err
		}
	}
	return nil
}

// create keeps a new piece with its first documents, all together or none.
func (p pieceStore) create(info *pieceInfo, docs map[string][]byte) error {
	r := rowOfPiece(info)
	return p.st.Tx(func(tx *store.Store) error {
		if err := tx.DB.Create(&r).Error; err != nil {
			return err
		}
		return putDocs(tx.DB, info.ID, docs)
	})
}

// delete removes a piece and its documents, all together or none.
func (p pieceStore) delete(id string) error {
	return p.st.Tx(func(tx *store.Store) error {
		if err := tx.DB.Where("piece_id = ?", id).Delete(&pieceDocRow{}).Error; err != nil {
			return err
		}
		return tx.DB.Where("id = ?", id).Delete(&pieceRow{}).Error
	})
}

// ids lists the ids of the pieces, newest first.
func (p pieceStore) ids() []string {
	var ids []string
	_ = p.st.DB.Model(&pieceRow{}).Order("created_at DESC, id DESC").Pluck("id", &ids).Error
	return ids
}

func (p pieceStore) doc(id, kind string) ([]byte, bool) {
	var d pieceDocRow
	if err := p.st.DB.Where("piece_id = ? AND kind = ?", id, kind).Take(&d).Error; err != nil {
		return nil, false
	}
	return []byte(d.Body), true
}

func (p pieceStore) putDoc(id, kind string, body []byte) error {
	d := pieceDocRow{PieceID: id, Kind: kind, Body: store.Text(body)}
	return p.st.DB.Clauses(clause.OnConflict{UpdateAll: true}).Create(&d).Error
}

// dbPlace is where the documents of one piece are kept: the database.
type dbPlace struct {
	ps pieceStore
	id string
}

func (d dbPlace) ReadDoc(kind string) ([]byte, bool) { return d.ps.doc(d.id, kind) }

func (d dbPlace) WriteDoc(kind string, body []byte) error { return d.ps.putDoc(d.id, kind, body) }

var _ musicmotion.Place = dbPlace{}

// pieces is the store of the pieces of this server.
func (h *Handler) pieces() pieceStore { return pieceStore{st: h.ws.Store()} }

// place is where the documents of a piece are kept.
func (h *Handler) place(id string) musicmotion.Place { return dbPlace{ps: h.pieces(), id: id} }

// deletePieceRows removes a piece, its documents and the scene parts written for it from the database in one transaction: all of it or
// none. The folder of the piece is removed by the caller (see RemoveManaged), not here.
func (h *Handler) deletePieceRows(id string) {
	st := h.ws.Store()
	err := st.Tx(func(tx *store.Store) error {
		if err := (pieceStore{st: tx}).delete(id); err != nil {
			return err
		}
		_, err := musicmotion.NewDBParts(tx).DeleteScenes(id)
		return err
	})
	if err != nil {
		slog.Warn("music-motion: the rows of a piece could not be deleted", "piece", id, "err", err)
	}
	// a catalog that keeps the parts in a folder (a test, an embedder) has its scenes removed through the catalog
	if h.mmCatalog().UserDir != "" {
		h.mmCatalog().DeleteScenes(id)
	}
}

// MarkInterrupted marks as failed the proposals and the scene batches that were being written when the server stopped: nothing is running
// after a start, so what says it is running is wrong. It runs when the handler is made, before the first request, and writes the answer
// into the documents (a read also reports an unfinished job as failed, which a handler that was just made cannot tell apart).
func (h *Handler) MarkInterrupted() {
	var rows []pieceDocRow
	if err := h.ws.Store().DB.Where("kind IN ?", []string{musicmotion.DocProposal, musicmotion.DocScenes}).Find(&rows).Error; err != nil {
		slog.Warn("music-motion: the unfinished proposals and batches could not be read", "err", err)
		return
	}
	now := time.Now().UTC()
	for _, r := range rows {
		switch r.Kind {
		case musicmotion.DocProposal:
			var p Proposal
			if json.Unmarshal([]byte(r.Body), &p) == nil && (p.State == ProposalWaiting || p.State == ProposalWriting) {
				p.State, p.FinishedAt, p.Error = ProposalFailed, &now, "the proposal was interrupted by a restart of the server"
				h.saveProposal(r.PieceID, p)
			}
		case musicmotion.DocScenes:
			var b SceneBatch
			if json.Unmarshal([]byte(r.Body), &b) == nil && b.State == SceneBatchWriting {
				b.State, b.FinishedAt, b.Error = SceneBatchFailed, &now, "the batch was interrupted by a restart of the server"
				h.saveSceneBatch(r.PieceID, b)
			}
		}
	}
}
