package workspace

// database.go: the settings of the database the run history is kept in (SQLite, MySQL or Postgres), a test of a
// connection and the copy of the SQLite history into the server. The engine is read once, when the server starts:
// saving a change asks for a restart. See specs/database-choice.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"veritmo/internal/platform/settings"
	"veritmo/internal/platform/store"
	"veritmo/internal/server/api"
)

// DatabaseView is a database as the page sees it: never the password.
type DatabaseView struct {
	Driver      string `json:"driver"`
	Host        string `json:"host,omitempty"`
	Port        int    `json:"port,omitempty"`
	Name        string `json:"name,omitempty"`
	User        string `json:"user,omitempty"`
	PasswordSet bool   `json:"password_set"`
}

func databaseViewOf(d settings.Database) DatabaseView {
	if !d.IsRemote() {
		return DatabaseView{Driver: settings.DBSQLite}
	}
	return DatabaseView{Driver: d.Driver, Host: d.Host, Port: d.Port, Name: d.Name, User: d.User, PasswordSet: d.Password != ""}
}

// DatabaseAnswer is GET /api/settings/database.
type DatabaseAnswer struct {
	// Saved is what is saved; Active what this server started with. They differ until it is restarted.
	Saved           DatabaseView `json:"saved"`
	Active          DatabaseView `json:"active"`
	RestartRequired bool         `json:"restart_required"`
	// Source says where `saved` comes from: setting (the settings file), env (the VERITMO_DB_* variables, used while nothing is saved) or default (the SQLite file).
	Source string `json:"source"`
	// ServerName is how this server is called in the history; Servers are the names the history knows.
	ServerName string   `json:"server_name"`
	Servers    []string `json:"servers"`
}

func (h *Handler) databaseAnswer() DatabaseAnswer {
	saved, active := settings.GetDatabase(), h.cfg.Database
	a := DatabaseAnswer{Saved: databaseViewOf(saved), Active: databaseViewOf(active), Source: settings.DatabaseSource(), ServerName: settings.ServerName(), Servers: []string{}}
	a.RestartRequired = a.Saved != a.Active
	var names []string
	if err := h.rm.hist.db.Model(&Run{}).Distinct().Where("server <> ''").Order("server").Pluck("server", &names).Error; err == nil && len(names) > 0 {
		a.Servers = names
	}
	if a.ServerName != h.cfg.ServerName && h.cfg.ServerName != "" {
		a.RestartRequired = true
	}
	return a
}

// GET /api/settings/database
//
//	-> {saved: {driver, host?, port?, name?, user?, password_set}, active, restart_required, server_name, servers}
func (h *Handler) handleDatabaseGet(w http.ResponseWriter, r *http.Request) {
	api.WriteJSON(w, http.StatusOK, h.databaseAnswer())
}

type databaseRequest struct {
	Driver     string  `json:"driver"`
	Host       string  `json:"host"`
	Port       int     `json:"port"`
	Name       string  `json:"name"`
	User       string  `json:"user"`
	Password   *string `json:"password"`
	ServerName *string `json:"server_name"`
}

// resolve turns a request into a database: the password left out is the saved one when the rest names the same server.
func (q databaseRequest) resolve() (settings.Database, error) {
	d, err := settings.Database{Driver: q.Driver, Host: q.Host, Port: q.Port, Name: q.Name, User: q.User}.Validate()
	if err != nil || !d.IsRemote() {
		return d, err
	}
	if q.Password != nil {
		d.Password = *q.Password
	} else if old := settings.SavedDatabase(); old.Driver == d.Driver && old.Host == d.Host && old.Port == d.Port && old.User == d.User {
		d.Password = old.Password
	}
	return d, nil
}

func readDatabaseRequest(w http.ResponseWriter, r *http.Request) (databaseRequest, bool) {
	var req databaseRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil && err != io.EOF {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
		return req, false
	}
	return req, true
}

// PUT /api/settings/database {driver, host, port?, name, user, password?, server_name?} -> the same as GET
//
// Saves where the history is kept and, if given, the name of this server. It connects to nothing (see /test) and
// takes effect when the server restarts. An empty `driver` or `sqlite` goes back to the file.
func (h *Handler) handleDatabasePut(w http.ResponseWriter, r *http.Request) {
	req, ok := readDatabaseRequest(w, r)
	if !ok {
		return
	}
	d, err := req.resolve()
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, err.Error())
		return
	}
	if req.ServerName != nil {
		if len([]rune(*req.ServerName)) > 100 {
			api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "the server name is longer than 100 characters")
			return
		}
		if err := settings.SetServerName(*req.ServerName); err != nil {
			api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, "could not save the settings: "+err.Error())
			return
		}
	}
	if err := settings.SetDatabase(d); err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, "could not save the settings: "+err.Error())
		return
	}
	api.WriteJSON(w, http.StatusOK, h.databaseAnswer())
}

// databaseTestResult is the answer of the test: a database nobody answers on is a normal answer (ok false).
type databaseTestResult struct {
	OK      bool   `json:"ok"`
	Version string `json:"version,omitempty"`
	// Empty says the database has no tables of the history yet (it is created when the server starts with it).
	Empty bool   `json:"empty,omitempty"`
	Error string `json:"error,omitempty"`
}

// POST /api/settings/database/test {driver, host, ...} -> {ok, version?, empty?, error?}
//
// Opens the connection with what the form says (the saved password when it is left out) and asks its version. It
// creates and changes nothing.
func (h *Handler) handleDatabaseTest(w http.ResponseWriter, r *http.Request) {
	req, ok := readDatabaseRequest(w, r)
	if !ok {
		return
	}
	d, err := req.resolve()
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, err.Error())
		return
	}
	if !d.IsRemote() {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "choose MySQL or Postgres to test a connection: SQLite is a file and needs none")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	api.WriteJSON(w, http.StatusOK, testDatabase(ctx, d))
}

func testDatabase(ctx context.Context, d settings.Database) databaseTestResult {
	st, err := store.Open("", d, nil)
	if err != nil {
		return databaseTestResult{Error: err.Error()}
	}
	defer func() { _ = st.Close() }()
	db := st.DB
	var version string
	if err := db.WithContext(ctx).Raw("SELECT VERSION()").Scan(&version).Error; err != nil {
		return databaseTestResult{Error: err.Error()}
	}
	return databaseTestResult{OK: true, Version: version, Empty: !db.Migrator().HasTable(&Run{})}
}

// databaseCopyResult is how many rows of each table were copied.
type databaseCopyResult struct {
	// Copied is how many rows each table received; Existing how many were already in the destination and were left as they are.
	Copied   map[string]int `json:"copied"`
	Existing map[string]int `json:"existing"`
}

// POST /api/settings/database/copy -> {copied: {runs, run_events, ...}}
//
// Copies the history of the SQLite file into the saved MySQL or Postgres database, creating its tables if they are
// not there. Rows that are already there (by id) are left as they are, so it can be repeated; the rows of the
// file are this server's. The server keeps using what it started with until it is restarted.
func (h *Handler) handleDatabaseCopy(w http.ResponseWriter, r *http.Request) {
	d := settings.GetDatabase()
	if !d.IsRemote() {
		api.WriteError(w, http.StatusConflict, api.CodeConflict, "save a MySQL or Postgres database first: there is nowhere to copy to")
		return
	}
	if h.cfg.Database.IsRemote() {
		// The file is no longer written; copying it would bring back what the server was started to leave behind.
		api.WriteError(w, http.StatusConflict, api.CodeConflict, "this server already keeps its history in the database: the file is not copied again")
		return
	}
	if _, busy := h.rm.anyBusy(); busy {
		api.WriteError(w, http.StatusConflict, api.CodeConflict, "a run is queued or running: wait for it to end, or its history would be copied half way")
		return
	}
	res, err := copyHistory(h.cfg.Store, d, settings.ServerName())
	if err != nil {
		api.WriteError(w, http.StatusBadGateway, api.CodeInternal, err.Error())
		return
	}
	api.WriteJSON(w, http.StatusOK, res)
}

// copyBatch is how many rows are inserted at once.
const copyBatch = 500

// copyHistory copies every table of the data layer from the open database src into the database d, creating its tables through the
// migrations.
func copyHistory(src *store.Store, d settings.Database, server string) (*databaseCopyResult, error) {
	dst, err := store.Open("", d, src.Registry())
	if err != nil {
		return nil, err
	}
	defer func() { _ = dst.Close() }()
	if err := dst.Migrate(); err != nil {
		return nil, fmt.Errorf("cannot create the tables in the database: %w", err)
	}
	return copyStores(src, dst, server)
}

// historyTables are the tables copyStores copies with rules of their own: the rows of the file are this server's and a numbered row
// takes a new number in the destination, which another server may have used for another row.
var historyTables = map[string]bool{"runs": true, "run_events": true, "run_samples": true, "reviews": true, "findings": true, "calibration_samples": true}

// copyStores copies the history and then every other registered table from src to dst, all in one transaction: when something fails
// nothing is changed. Rows whose key is already in dst are left as they are, so it can be repeated.
func copyStores(src, dst *store.Store, server string) (*databaseCopyResult, error) {
	out := &databaseCopyResult{Copied: map[string]int{}, Existing: map[string]int{}}
	err := dst.Tx(func(tx *store.Store) error {
		if err := copyHistoryRows(src.DB, tx.DB, server, out); err != nil {
			return err
		}
		others, err := store.CopyTables(src, tx, historyTables)
		if err != nil {
			return err
		}
		for name, c := range others {
			out.Copied[name], out.Existing[name] = c.Copied, c.Existing
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("the copy failed and nothing was changed: %w", err)
	}
	return out, nil
}

func copyHistoryRows(src, tx *gorm.DB, server string, out *databaseCopyResult) error {
	var runs []Run
	if err := src.Order("created_at, id").Find(&runs).Error; err != nil {
		return err
	}
	for i := range runs {
		if runs[i].Server == "" {
			runs[i].Server = server
		}
	}
	if err := insertAll(tx, &runs, out, "runs"); err != nil {
		return err
	}
	var events []RunEvent
	if err := src.Order("id").Find(&events).Error; err != nil {
		return err
	}
	for i := range events {
		events[i].ID = 0 // the database numbers them again
	}
	if err := insertAll(tx, &events, out, "run_events"); err != nil {
		return err
	}
	var samples []RunSample
	if err := src.Order("id").Find(&samples).Error; err != nil {
		return err
	}
	for i := range samples {
		samples[i].ID = 0
	}
	if err := insertAll(tx, &samples, out, "run_samples"); err != nil {
		return err
	}
	var reviews []Review
	if err := src.Find(&reviews).Error; err != nil {
		return err
	}
	for i := range reviews {
		if reviews[i].Server == "" {
			reviews[i].Server = server
		}
	}
	if err := insertAll(tx, &reviews, out, "reviews"); err != nil {
		return err
	}
	var findings []Finding
	if err := src.Find(&findings).Error; err != nil {
		return err
	}
	if err := insertAll(tx, &findings, out, "findings"); err != nil {
		return err
	}
	var cal []CalibrationSample
	if err := src.Order("id").Find(&cal).Error; err != nil {
		return err
	}
	for i := range cal {
		cal[i].ID = 0
		if cal[i].Server == "" {
			cal[i].Server = server
		}
	}
	return insertAll(tx, &cal, out, "calibration_samples")
}

// insertAll inserts the rows in batches, leaving alone those whose key is already there, and counts what it added.
func insertAll[T any](tx *gorm.DB, rows *[]T, out *databaseCopyResult, name string) error {
	out.Copied[name], out.Existing[name] = 0, 0
	defer func() { out.Existing[name] = len(*rows) - out.Copied[name] }()
	for i := 0; i < len(*rows); i += copyBatch {
		end := min(i+copyBatch, len(*rows))
		batch := (*rows)[i:end]
		res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&batch)
		if res.Error != nil {
			return fmt.Errorf("%s: %w", name, res.Error)
		}
		out.Copied[name] += int(res.RowsAffected)
	}
	return nil
}
