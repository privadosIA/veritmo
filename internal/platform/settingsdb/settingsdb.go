// Package settingsdb keeps the settings of the server in the database (change move-settings-and-catalogs-to-database): it is the settings.Source the
// server, and the commands that open the database, use. Each setting is a row of the table settings (its key and the JSON of that field of
// settings.File) and each provider a row of the table providers, in the order the user gave them. No secret is in a row: the tokens of HeartMuLa and
// DiffRhythm2 and the secrets of the monitoring tools are in the private keys file next to settings.json (ids heartmula, diffrhythm2 and
// monitoring:<tool>), put there on Save and read back on Load, so that the code that reads File.HeartMulaToken does not change.
package settingsdb

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"veritmo/internal/platform/settings"
	"veritmo/internal/platform/store"
)

// The ids the secrets of the settings are kept under in the keys file.
const (
	KeyHeartMula   = "heartmula"
	KeyDiffRhythm2 = "diffrhythm2"
	keyMonitoring  = "monitoring:"
)

// MonitoringKey is the id the secret of a monitoring tool is kept under in the keys file.
func MonitoringKey(tool string) string { return keyMonitoring + tool }

// Setting is one setting of the database: the key is the name the field has in settings.json.
type Setting struct {
	Key       string `gorm:"column:setting_key;primaryKey;size:64"`
	Value     store.Text
	UpdatedAt time.Time `gorm:"autoUpdateTime:false"`
}

// TableName is the table of the settings.
func (Setting) TableName() string { return "settings" }

// Provider is a model provider as the database keeps it.
type Provider struct {
	ID        string `gorm:"primaryKey;size:64"`
	Position  int
	Name      string    `gorm:"size:255"`
	Kind      string    `gorm:"size:32"`
	UpdatedAt time.Time `gorm:"autoUpdateTime:false"`
	Doc       store.JSON[settings.Provider]
}

// TableName is the table of the providers.
func (Provider) TableName() string { return "providers" }

// RegisterStore tells a registry what the settings own: their tables and the migration that creates them (server.DataRegistry calls it).
func RegisterStore(r *store.Registry) {
	r.AddModels(&Setting{}, &Provider{})
	r.AddMigration(settingsMigration())
	r.AddImporter(Importer{})
}

// settingsMigration creates the tables of the settings and of the providers (the structs are copied here: it does the same whatever the models become).
func settingsMigration() *store.Migration {
	return &store.Migration{
		ID: "202611191700_settings",
		Migrate: func(tx *gorm.DB) error {
			type setting struct {
				Key       string `gorm:"column:setting_key;primaryKey;size:64"`
				Value     store.Text
				UpdatedAt time.Time
			}
			type provider struct {
				ID        string `gorm:"primaryKey;size:64"`
				Position  int
				Name      string `gorm:"size:255"`
				Kind      string `gorm:"size:32"`
				UpdatedAt time.Time
				Doc       store.Text
			}
			for table, model := range map[string]any{"settings": &setting{}, "providers": &provider{}} {
				if tx.Migrator().HasTable(table) {
					continue
				}
				if err := tx.Table(table).Migrator().CreateTable(model); err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			for _, t := range []string{"providers", "settings"} {
				if err := tx.Migrator().DropTable(t); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

// Source is the settings of a database.
type Source struct{ st *store.Store }

// New makes the source of the settings of an open, migrated store.
func New(st *store.Store) *Source { return &Source{st: st} }

// field is one setting: the key it is stored under and what it holds in a File, with the secrets taken out; nil means the setting is not set.
type field struct {
	key string
	get func(f settings.File) any
}

var fields = []field{
	{"ollama_host", func(f settings.File) any { return nonEmpty(f.OllamaHost) }},
	{"browser", func(f settings.File) any { return nonEmpty(f.Browser) }},
	{"motion_model", func(f settings.File) any { return nonEmpty(f.MotionModel) }},
	{"routing", func(f settings.File) any { return nonEmptyMap(f.Routing) }},
	{"consent", func(f settings.File) any { return nonEmptyMap(f.Consent) }},
	{"licence_acceptances", func(f settings.File) any {
		if len(f.LicenceAcceptances) == 0 {
			return nil
		}
		return f.LicenceAcceptances
	}},
	{"client_colors", func(f settings.File) any { return nonEmptyMap(f.ClientColors) }},
	{"remote", func(f settings.File) any {
		if f.Remote == nil || f.Remote.MaxCalls == 0 {
			return nil
		}
		return f.Remote
	}},
	{"heartmula_host", func(f settings.File) any { return nonEmpty(f.HeartMulaHost) }},
	{"diffrhythm2_host", func(f settings.File) any { return nonEmpty(f.DiffRhythm2Host) }},
	{"heartmula_backend", func(f settings.File) any { return nonEmpty(f.HeartMulaBackend) }},
	{"diffrhythm2_backend", func(f settings.File) any { return nonEmpty(f.DiffRhythm2Backend) }},
	{"model3d_backend", func(f settings.File) any { return nonEmpty(f.Model3DBackend) }},
	{"monitoring", func(f settings.File) any {
		if len(f.Monitoring) == 0 {
			return nil
		}
		out := make(map[string]settings.Endpoint, len(f.Monitoring))
		for id, e := range f.Monitoring {
			e.Secret = "" // never in the database
			out[id] = e
		}
		return out
	}},
	{"cors_origins", func(f settings.File) any {
		if len(f.CORSOrigins) == 0 {
			return nil
		}
		return f.CORSOrigins
	}},
	{"cors_networks", func(f settings.File) any {
		if len(f.CORSNetworks) == 0 {
			return nil
		}
		return f.CORSNetworks
	}},
	{"music_motion_review_drafts", func(f settings.File) any {
		if !f.ReviewDrafts {
			return nil
		}
		return true
	}},
	{"ctxpress", func(f settings.File) any {
		if f.Ctxpress == nil {
			return nil
		}
		return f.Ctxpress
	}},
}

// nonEmpty is the value of a text setting, or nil (not set) when it is empty.
func nonEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// nonEmptyMap is the value of a map setting, or nil (not set) when it has no entry.
func nonEmptyMap(m map[string]string) any {
	if len(m) == 0 {
		return nil
	}
	return m
}

// values is the JSON of every setting that is set.
func values(f settings.File) (map[string]string, error) {
	out := map[string]string{}
	for _, fd := range fields {
		v := fd.get(f)
		if v == nil {
			continue
		}
		raw, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		out[fd.key] = string(raw)
	}
	return out, nil
}

// secrets are the secrets a File carries, by the id they are kept under in the keys file. A tool or a token that is not set is there with an
// empty value, so that SyncKeys removes what is left of it.
func secrets(f settings.File, stored map[string]settings.Endpoint) map[string]string {
	out := map[string]string{KeyHeartMula: f.HeartMulaToken, KeyDiffRhythm2: f.DiffRhythm2Token}
	for id := range stored {
		out[MonitoringKey(id)] = ""
	}
	for id, e := range f.Monitoring {
		out[MonitoringKey(id)] = e.Secret
	}
	return out
}

// Load reads every setting and every provider (two queries) and fills the secrets from the keys file. It keeps no copy: a change made by another
// process is seen at once.
func (s *Source) Load() (settings.File, error) {
	var rows []Setting
	if err := s.st.DB.Find(&rows).Error; err != nil {
		return settings.File{}, err
	}
	doc := map[string]json.RawMessage{}
	for _, r := range rows {
		doc[r.Key] = json.RawMessage(r.Value)
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return settings.File{}, err
	}
	var f settings.File
	if err := json.Unmarshal(raw, &f); err != nil {
		return settings.File{}, fmt.Errorf("the settings in the database cannot be read: %w", err)
	}
	var ps []Provider
	if err := s.st.DB.Order("position").Order("id").Find(&ps).Error; err != nil {
		return settings.File{}, err
	}
	for _, p := range ps {
		f.Providers = append(f.Providers, p.Doc.V)
	}
	f.HeartMulaToken = settings.Key(KeyHeartMula)
	f.DiffRhythm2Token = settings.Key(KeyDiffRhythm2)
	for id, e := range f.Monitoring {
		e.Secret = settings.Key(MonitoringKey(id))
		f.Monitoring[id] = e
	}
	return f, nil
}

// Save writes the settings in one transaction, only the rows that changed, and the secrets to the keys file. The database settings and the server
// name are not its business (they stay in settings.json, see settings.UseSource).
func (s *Source) Save(f settings.File) error {
	want, err := values(f)
	if err != nil {
		return err
	}
	var stored settings.File
	var keep []Setting
	err = s.st.Tx(func(tx *store.Store) error {
		if err := tx.DB.Find(&keep).Error; err != nil {
			return err
		}
		have := map[string]string{}
		for _, r := range keep {
			have[r.Key] = string(r.Value)
		}
		if m, ok := have["monitoring"]; ok {
			_ = json.Unmarshal([]byte(m), &stored.Monitoring)
		}
		now := time.Now().UTC()
		for _, fd := range fields {
			v, set := want[fd.key]
			old, had := have[fd.key]
			switch {
			case !set && had:
				if err := tx.DB.Where("setting_key = ?", fd.key).Delete(&Setting{}).Error; err != nil {
					return err
				}
			case set && (!had || old != v):
				if err := tx.DB.Clauses(clause.OnConflict{UpdateAll: true}).Create(&Setting{Key: fd.key, Value: store.Text(v), UpdatedAt: now}).Error; err != nil {
					return err
				}
			}
		}
		return saveProviders(tx, f.Providers, now)
	})
	if err != nil {
		return err
	}
	return settings.SyncKeys(secrets(f, stored.Monitoring))
}

// saveProviders makes the rows of the providers the list given, in its order: a row that changed is written, one that is gone is deleted.
func saveProviders(tx *store.Store, list []settings.Provider, now time.Time) error {
	var rows []Provider
	if err := tx.DB.Find(&rows).Error; err != nil {
		return err
	}
	have := map[string]Provider{}
	for _, r := range rows {
		have[r.ID] = r
	}
	keep := map[string]bool{}
	for i, p := range list {
		keep[p.ID] = true
		row := Provider{ID: p.ID, Position: i, Name: p.Name, Kind: p.Type, UpdatedAt: now, Doc: store.JSON[settings.Provider]{V: p}}
		if old, ok := have[p.ID]; ok && old.Position == i && old.Doc.V == p {
			continue
		}
		if err := tx.DB.Clauses(clause.OnConflict{UpdateAll: true}).Create(&row).Error; err != nil {
			return err
		}
	}
	var gone []string
	for id := range have {
		if !keep[id] {
			gone = append(gone, id)
		}
	}
	sort.Strings(gone)
	if len(gone) == 0 {
		return nil
	}
	return tx.DB.Where("id IN ?", gone).Delete(&Provider{}).Error
}
