package workspace

// songstore.go: the songs, their stars and the playlists in the database (change move-song-library-to-database). A song is a row written when a
// run of its project ends (SyncProject reads what the run exported: the manifest, the definition and the audio), with what the listing says
// about it and the length of its audio, so the library is one query and never opens a folder or runs ffmpeg. The audio, the cover and the
// manifest stay files. The stars are a column of the song; the playlists are two tables.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"sync"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"veritmo/internal/platform/runner"
	"veritmo/internal/platform/store"
	"veritmo/internal/project/config"
)

// ErrNoSong is a song that is not in the library; ErrNoPlaylist a playlist that is not; ErrPlaylistExists one that already is.
var (
	ErrNoSong         = errors.New("the song does not exist")
	ErrNoPlaylist     = errors.New("the playlist does not exist")
	ErrPlaylistExists = errors.New("the playlist already exists")
)

// SongRow is a song as the database keeps it. The column of the key is song_key (a bare key is a reserved word of MySQL).
type SongRow struct {
	Key       string `gorm:"column:song_key;primaryKey;size:191"`
	ProjectID string `gorm:"size:191;index"`
	TrackID   string `gorm:"size:191"`
	Variant   int
	Seed      int64
	Title     string `gorm:"size:255"`
	Caption   store.Text
	Lyrics    store.Text
	BPM       int
	// MusicKey is the musical key of the song ("C major"), apart from Key, the identity.
	MusicKey string `gorm:"size:32"`
	// Duration is the length of the audio as the song map reads it, in seconds; zero when it could not be measured.
	Duration float64
	File     string `gorm:"size:512"`
	Cover    string `gorm:"size:512"`
	// Model is the generator that made the song ("ace-step", "heartmula" or "diffrhythm2").
	Model    string `gorm:"size:32"`
	Client   string `gorm:"size:64;index"`
	Favorite bool
	// Rating is the stars (1 to 5); 0 is none.
	Rating int `gorm:"index"`
	// FileSize and FileMTime (unix nanoseconds) are what the audio was when Duration was measured: a duration is measured again only when they change.
	FileSize  int64
	FileMTime int64 `gorm:"column:file_mtime"`
	// CreatedAt is when the run that exported the song says it ran (generated_at of the manifest), the sort key of the library; UpdatedAt when the row was written.
	CreatedAt time.Time `gorm:"index;autoCreateTime:false"`
	UpdatedAt time.Time `gorm:"autoUpdateTime:false"`
	// Doc is the item the listing answers, so that the body of GET /api/library is exactly the one it always was.
	Doc store.JSON[librarySong]
}

// TableName is the table of the songs.
func (SongRow) TableName() string { return "songs" }

// PlaylistRow is a playlist: its id, its name and its place among the playlists.
type PlaylistRow struct {
	ID        string `gorm:"primaryKey;size:64"`
	Name      string `gorm:"size:255"`
	Position  int
	CreatedAt time.Time `gorm:"autoCreateTime:false"`
	UpdatedAt time.Time `gorm:"autoUpdateTime:false"`
}

// TableName is the table of the playlists.
func (PlaylistRow) TableName() string { return "playlists" }

// PlaylistSong is a place of a playlist: the song that is at a position. SongKey is not a constraint: a playlist and its songs are changed together.
type PlaylistSong struct {
	PlaylistID string `gorm:"primaryKey;size:64"`
	Position   int    `gorm:"primaryKey;autoIncrement:false"`
	SongKey    string `gorm:"size:191;index"`
}

// TableName is the table of the places.
func (PlaylistSong) TableName() string { return "playlist_songs" }

// songsMigration creates the tables of the library (the structs are copied here: the migration does the same whatever the models become).
func songsMigration() *store.Migration {
	return &store.Migration{
		ID: "202611191400_songs",
		Migrate: func(tx *gorm.DB) error {
			type songRow struct {
				Key       string `gorm:"column:song_key;primaryKey;size:191"`
				ProjectID string `gorm:"size:191;index"`
				TrackID   string `gorm:"size:191"`
				Variant   int
				Seed      int64
				Title     string `gorm:"size:255"`
				Caption   store.Text
				Lyrics    store.Text
				BPM       int
				MusicKey  string `gorm:"size:32"`
				Duration  float64
				File      string `gorm:"size:512"`
				Cover     string `gorm:"size:512"`
				Model     string `gorm:"size:32"`
				Client    string `gorm:"size:64;index"`
				Favorite  bool
				Rating    int `gorm:"index"`
				FileSize  int64
				FileMTime int64     `gorm:"column:file_mtime"`
				CreatedAt time.Time `gorm:"index"`
				UpdatedAt time.Time
				Doc       store.Text
			}
			type playlistRow struct {
				ID        string `gorm:"primaryKey;size:64"`
				Name      string `gorm:"size:255"`
				Position  int
				CreatedAt time.Time
				UpdatedAt time.Time
			}
			type playlistSong struct {
				PlaylistID string `gorm:"primaryKey;size:64"`
				Position   int    `gorm:"primaryKey;autoIncrement:false"`
				SongKey    string `gorm:"size:191;index"`
			}
			for table, model := range map[string]any{"songs": &songRow{}, "playlists": &playlistRow{}, "playlist_songs": &playlistSong{}} {
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
			for _, t := range []string{"playlist_songs", "playlists", "songs"} {
				if err := tx.Migrator().DropTable(t); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

// songStore is the library of the database.
type songStore struct {
	db *store.Store
	// mu makes the writes take turns (a sync, a change of the stars, a change of a playlist).
	mu sync.Mutex
	// measure is the length of an audio file in seconds, 0 when it cannot be told; nil measures nothing.
	measure func(ctx context.Context, path string) float64
}

// newSongStore is the library of a database, with nothing to measure an audio with (the caller sets measure).
func newSongStore(db *store.Store) *songStore { return &songStore{db: db} }

// SyncReport says what a sync did.
type SyncReport struct {
	Added, Updated, Removed int
}

// add counts what another sync did.
func (r *SyncReport) add(o SyncReport) {
	r.Added += o.Added
	r.Updated += o.Updated
	r.Removed += o.Removed
}

// songSource is a song as the disk says it, before the row is made.
type songSource struct {
	song      librarySong
	path      string // absolute path of the audio
	createdAt time.Time
}

// rowOf makes the row of a song, the duration and the stat of the audio filled by the caller.
func rowOf(s librarySong, createdAt time.Time) SongRow {
	return SongRow{
		Key: s.Key, ProjectID: s.Project, TrackID: s.TrackID, Variant: s.Variant, Seed: s.Seed, Title: s.Title,
		Caption: store.Text(s.Caption), Lyrics: store.Text(s.Lyrics), BPM: s.BPM, MusicKey: s.KeyScale, Duration: s.Duration,
		File: s.File, Cover: s.Cover, Model: s.Generator, Client: s.Client, Favorite: s.Favorite, CreatedAt: createdAt,
		Doc: store.JSON[librarySong]{V: s},
	}
}

// syncColumns are the columns a sync rewrites for a song that already is: all but the stars.
var syncColumns = []string{
	"project_id", "track_id", "variant", "seed", "title", "caption", "lyrics", "bpm", "music_key", "duration", "file", "cover", "model", "client",
	"favorite", "file_size", "file_mtime", "created_at", "updated_at", "doc",
}

// SyncProject makes the rows of a project the songs its manifest exports: a new song is added, one that is there is updated (its stars stay),
// one that is no longer exported is removed (and leaves the playlists, which close the gap). A song whose audio did not change since its row was
// written is not measured again. An empty list (a project that is not music, has no manifest or is gone) removes every row of the project.
func (s *songStore) SyncProject(ctx context.Context, project string, found []songSource) (SyncReport, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var existing []SongRow
	if err := s.db.DB.Where("project_id = ?", project).Find(&existing).Error; err != nil {
		return SyncReport{}, err
	}
	have := map[string]SongRow{}
	for _, r := range existing {
		have[r.Key] = r
	}
	now := time.Now().UTC()
	rows := make([]SongRow, 0, len(found))
	wanted := map[string]bool{}
	for _, f := range found {
		if wanted[f.song.Key] {
			continue // two entries of the manifest with the same key: the first one is the song
		}
		wanted[f.song.Key] = true
		var size, mtime int64
		if st, err := os.Stat(f.path); err == nil {
			size, mtime = st.Size(), st.ModTime().UnixNano()
		}
		song := f.song
		if old, ok := have[f.song.Key]; ok && old.Duration > 0 && old.FileSize == size && old.FileMTime == mtime && size > 0 {
			song.Duration = old.Duration
		} else if s.measure != nil {
			song.Duration = s.measure(ctx, f.path)
		}
		row := rowOf(song, f.createdAt.UTC())
		row.FileSize, row.FileMTime, row.UpdatedAt = size, mtime, now
		rows = append(rows, row)
	}
	var rep SyncReport
	err := s.db.Tx(func(tx *store.Store) error {
		for _, row := range rows {
			old, ok := have[row.Key]
			switch {
			case !ok:
				rep.Added++
			case !sameSong(old, row):
				rep.Updated++
			}
			if err := tx.DB.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "song_key"}}, DoUpdates: clause.AssignmentColumns(syncColumns)}).Create(&row).Error; err != nil {
				return err
			}
		}
		var gone []string
		for _, r := range existing {
			if !wanted[r.Key] {
				gone = append(gone, r.Key)
			}
		}
		rep.Removed = len(gone)
		return removeSongs(tx, gone)
	})
	if err != nil {
		return SyncReport{}, err
	}
	return rep, nil
}

// syncFromProject is SyncProject for a project of the database: the songs are what its manifest, read in its folder dir, exports under its saved
// definition. A project that is gone, invalid or not music has none, so its rows are removed.
func (s *songStore) syncFromProject(ctx context.Context, ps *projectStore, id, dir string) (SyncReport, error) {
	var found []songSource
	if row, err := ps.Get(id); err == nil {
		if cfg, err := config.LoadBytes([]byte(row.Definition)); err == nil {
			found = songsOf(id, dir, cfg, row.Client)
		}
	}
	return s.SyncProject(ctx, id, found)
}

// execMeasure measures an audio file with the ffmpeg of the machine, for what has no server to ask (the importer and the command line).
func execMeasure(ctx context.Context, path string) float64 {
	return decodedSeconds(ctx, exec.LookPath, runner.NewExecRunner(), path)
}

// sameSong says whether a sync changes nothing of a row: the item the listing answers and the file it was measured from.
func sameSong(old, now SongRow) bool {
	return old.Doc.V == now.Doc.V && old.FileSize == now.FileSize && old.FileMTime == now.FileMTime && old.CreatedAt.Equal(now.CreatedAt)
}

// removeSongs deletes the rows of some songs and their places in the playlists, closing the gaps.
func removeSongs(tx *store.Store, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	for start := 0; start < len(keys); start += 500 {
		end := min(start+500, len(keys))
		if err := tx.DB.Where("song_key IN ?", keys[start:end]).Delete(&SongRow{}).Error; err != nil {
			return err
		}
	}
	var playlists []string
	for start := 0; start < len(keys); start += 500 {
		end := min(start+500, len(keys))
		var part []string
		if err := tx.DB.Model(&PlaylistSong{}).Where("song_key IN ?", keys[start:end]).Distinct().Pluck("playlist_id", &part).Error; err != nil {
			return err
		}
		playlists = append(playlists, part...)
	}
	sort.Strings(playlists)
	for i, id := range playlists {
		if i > 0 && playlists[i-1] == id {
			continue
		}
		var places []PlaylistSong
		if err := tx.DB.Where("playlist_id = ?", id).Order("position").Find(&places).Error; err != nil {
			return err
		}
		drop := map[string]bool{}
		for _, k := range keys {
			drop[k] = true
		}
		var kept []string
		for _, p := range places {
			if !drop[p.SongKey] {
				kept = append(kept, p.SongKey)
			}
		}
		if err := writePlaces(tx, id, kept); err != nil {
			return err
		}
	}
	return nil
}

// writePlaces replaces the places of a playlist with the songs given, in order, from position 0.
func writePlaces(tx *store.Store, id string, keys []string) error {
	if err := tx.DB.Where("playlist_id = ?", id).Delete(&PlaylistSong{}).Error; err != nil {
		return err
	}
	if len(keys) == 0 {
		return nil
	}
	places := make([]PlaylistSong, len(keys))
	for i, k := range keys {
		places[i] = PlaylistSong{PlaylistID: id, Position: i, SongKey: k}
	}
	return tx.DB.CreateInBatches(places, 200).Error
}

// deleteProject removes the songs of a project and their places in the playlists, in the transaction of the delete of the project.
func (s *songStore) deleteProject(tx *store.Store, project string) error {
	var keys []string
	if err := tx.DB.Model(&SongRow{}).Where("project_id = ?", project).Pluck("song_key", &keys).Error; err != nil {
		return err
	}
	return removeSongs(tx, keys)
}

// Get reads a song; ErrNoSong when there is none.
func (s *songStore) Get(key string) (SongRow, error) {
	var r SongRow
	if err := s.db.DB.Where("song_key = ?", key).Take(&r).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return SongRow{}, ErrNoSong
		}
		return SongRow{}, err
	}
	return r, nil
}

// SetRating sets the stars of a song (0 removes them); ErrNoSong when there is no such song.
func (s *songStore) SetRating(key string, stars int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	res := s.db.DB.Model(&SongRow{}).Where("song_key = ?", key).Update("rating", stars)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNoSong
	}
	return nil
}

// Projects lists the projects that have songs and the client of each.
func (s *songStore) Projects() (map[string]string, error) {
	var rows []SongRow
	if err := s.db.DB.Select("project_id", "client").Group("project_id, client").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, r := range rows {
		out[r.ProjectID] = r.Client
	}
	return out, nil
}

// All reads every song, newest first (the key breaks a tie), the ones a scope keeps.
func (s *songStore) All(scopes ...func(*gorm.DB) *gorm.DB) ([]SongRow, error) {
	q := s.db.DB.Model(&SongRow{})
	for _, sc := range scopes {
		q = sc(q)
	}
	var rows []SongRow
	err := q.Order("created_at DESC").Order("song_key ASC").Find(&rows).Error
	return rows, err
}

// Meta is every star and every playlist the database holds, whatever the projects the songs belong to.
func (s *songStore) Meta() (libraryMeta, error) {
	m := libraryMeta{Ratings: map[string]int{}, Playlists: []libraryPlaylist{}}
	var rated []SongRow
	if err := s.db.DB.Select("song_key", "rating").Where("rating > 0").Find(&rated).Error; err != nil {
		return m, err
	}
	for _, r := range rated {
		m.Ratings[r.Key] = r.Rating
	}
	var lists []PlaylistRow
	if err := s.db.DB.Order("position").Order("id").Find(&lists).Error; err != nil {
		return m, err
	}
	var places []PlaylistSong
	if err := s.db.DB.Order("playlist_id").Order("position").Find(&places).Error; err != nil {
		return m, err
	}
	byList := map[string][]string{}
	for _, p := range places {
		byList[p.PlaylistID] = append(byList[p.PlaylistID], p.SongKey)
	}
	for _, l := range lists {
		songs := byList[l.ID]
		if songs == nil {
			songs = []string{}
		}
		m.Playlists = append(m.Playlists, libraryPlaylist{ID: l.ID, Name: l.Name, Songs: songs})
	}
	return m, nil
}

// ReplaceMeta makes the stars and the playlists of a library what m says, in one transaction: a song that is not in m has no stars. What m says
// of a song that is not in the library (a star, a place in a playlist) is left out, so a client that read the library before a song went away
// can still save. It returns what was kept.
func (s *songStore) ReplaceMeta(m libraryMeta) (libraryMeta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var kept libraryMeta
	err := s.db.Tx(func(tx *store.Store) error {
		var keys []string
		if err := tx.DB.Model(&SongRow{}).Pluck("song_key", &keys).Error; err != nil {
			return err
		}
		exists := make(map[string]bool, len(keys))
		for _, k := range keys {
			exists[k] = true
		}
		kept = libraryMeta{Ratings: map[string]int{}, Playlists: []libraryPlaylist{}}
		if err := tx.DB.Model(&SongRow{}).Where("rating <> 0").Update("rating", 0).Error; err != nil {
			return err
		}
		byStars := map[int][]string{}
		for k, v := range m.Ratings {
			if exists[k] {
				byStars[v] = append(byStars[v], k)
				kept.Ratings[k] = v
			}
		}
		for stars, ks := range byStars {
			for start := 0; start < len(ks); start += 500 {
				end := min(start+500, len(ks))
				if err := tx.DB.Model(&SongRow{}).Where("song_key IN ?", ks[start:end]).Update("rating", stars).Error; err != nil {
					return err
				}
			}
		}
		if err := tx.DB.Where("1 = 1").Delete(&PlaylistSong{}).Error; err != nil {
			return err
		}
		if err := tx.DB.Where("1 = 1").Delete(&PlaylistRow{}).Error; err != nil {
			return err
		}
		now := time.Now().UTC()
		for i, p := range m.Playlists {
			if err := tx.DB.Create(&PlaylistRow{ID: p.ID, Name: p.Name, Position: i, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
				return err
			}
			songs := []string{}
			for _, k := range p.Songs {
				if exists[k] {
					songs = append(songs, k)
				}
			}
			if err := writePlaces(tx, p.ID, songs); err != nil {
				return err
			}
			kept.Playlists = append(kept.Playlists, libraryPlaylist{ID: p.ID, Name: p.Name, Songs: songs})
		}
		return nil
	})
	if err != nil {
		return libraryMeta{}, err
	}
	return kept, nil
}

// --- the playlists one by one --------------------------------------------------------------------

// Playlist reads one playlist with its song keys in order.
func (s *songStore) Playlist(id string) (libraryPlaylist, error) {
	var row PlaylistRow
	if err := s.db.DB.Where("id = ?", id).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return libraryPlaylist{}, ErrNoPlaylist
		}
		return libraryPlaylist{}, err
	}
	var keys []string
	if err := s.db.DB.Model(&PlaylistSong{}).Where("playlist_id = ?", id).Order("position").Pluck("song_key", &keys).Error; err != nil {
		return libraryPlaylist{}, err
	}
	if keys == nil {
		keys = []string{}
	}
	return libraryPlaylist{ID: row.ID, Name: row.Name, Songs: keys}, nil
}

// ErrUnknownSongs is the keys of a playlist that are not songs of the library.
type ErrUnknownSongs struct{ Keys []string }

// Error names the first key that is not a song.
func (e *ErrUnknownSongs) Error() string {
	return fmt.Sprintf("song %q is not a song of the library", e.Keys[0])
}

// unknownSongs are the keys among keys that are not songs, in the order given.
func unknownSongs(tx *store.Store, keys []string) ([]string, error) {
	have := map[string]bool{}
	uniq := map[string]bool{}
	var list []string
	for _, k := range keys {
		if !uniq[k] {
			uniq[k] = true
			list = append(list, k)
		}
	}
	for start := 0; start < len(list); start += 500 {
		end := min(start+500, len(list))
		var found []string
		if err := tx.DB.Model(&SongRow{}).Where("song_key IN ?", list[start:end]).Pluck("song_key", &found).Error; err != nil {
			return nil, err
		}
		for _, k := range found {
			have[k] = true
		}
	}
	var missing []string
	for _, k := range keys {
		if !have[k] {
			missing = append(missing, k)
		}
	}
	return missing, nil
}

// CreatePlaylist makes a playlist at the end of the others. ErrPlaylistExists when the id is taken, *ErrUnknownSongs when a key is not a song.
func (s *songStore) CreatePlaylist(p libraryPlaylist) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Tx(func(tx *store.Store) error {
		var n int64
		if err := tx.DB.Model(&PlaylistRow{}).Where("id = ?", p.ID).Count(&n).Error; err != nil {
			return err
		}
		if n > 0 {
			return ErrPlaylistExists
		}
		if missing, err := unknownSongs(tx, p.Songs); err != nil {
			return err
		} else if len(missing) > 0 {
			return &ErrUnknownSongs{Keys: missing}
		}
		var last int
		if err := tx.DB.Model(&PlaylistRow{}).Select("COALESCE(MAX(position), -1)").Scan(&last).Error; err != nil {
			return err
		}
		now := time.Now().UTC()
		if err := tx.DB.Create(&PlaylistRow{ID: p.ID, Name: p.Name, Position: last + 1, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
			return err
		}
		return writePlaces(tx, p.ID, p.Songs)
	})
}

// ReplacePlaylist replaces the name and the songs of a playlist. ErrNoPlaylist when there is none, *ErrUnknownSongs when a key is not a song.
func (s *songStore) ReplacePlaylist(id, name string, songs []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Tx(func(tx *store.Store) error {
		var n int64
		if err := tx.DB.Model(&PlaylistRow{}).Where("id = ?", id).Count(&n).Error; err != nil {
			return err
		}
		if n == 0 {
			return ErrNoPlaylist
		}
		if missing, err := unknownSongs(tx, songs); err != nil {
			return err
		} else if len(missing) > 0 {
			return &ErrUnknownSongs{Keys: missing}
		}
		if err := tx.DB.Model(&PlaylistRow{}).Where("id = ?", id).Updates(map[string]any{"name": name, "updated_at": time.Now().UTC()}).Error; err != nil {
			return err
		}
		return writePlaces(tx, id, songs)
	})
}

// DeletePlaylist deletes a playlist; ErrNoPlaylist when there is none.
func (s *songStore) DeletePlaylist(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Tx(func(tx *store.Store) error {
		res := tx.DB.Where("id = ?", id).Delete(&PlaylistRow{})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrNoPlaylist
		}
		if err := tx.DB.Where("playlist_id = ?", id).Delete(&PlaylistSong{}).Error; err != nil {
			return err
		}
		// the others keep their order; close the gap in positions so a new playlist goes last
		var rest []PlaylistRow
		if err := tx.DB.Order("position").Order("id").Find(&rest).Error; err != nil {
			return err
		}
		for i, r := range rest {
			if r.Position != i {
				if err := tx.DB.Model(&PlaylistRow{}).Where("id = ?", r.ID).Update("position", i).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// PlaylistCount is how many playlists there are.
func (s *songStore) PlaylistCount() (int64, error) {
	var n int64
	err := s.db.DB.Model(&PlaylistRow{}).Count(&n).Error
	return n, err
}
