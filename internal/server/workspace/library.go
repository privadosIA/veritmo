package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"veritmo/internal/music/audiomeasure"
	"veritmo/internal/music/music"
	"veritmo/internal/platform/runner"
	"veritmo/internal/project/config"
	"veritmo/internal/project/export"
	"veritmo/internal/server/api"
)

// The song library: every song of every music project in one list, with the
// stars and playlists the user adds. The songs, the stars and the playlists
// are rows of the database (songstore.go); the stars and playlists used to
// live in one small file next to the projects, which the importer reads.

const libraryMetaFile = ".library.json"

// Limits of the library metadata.
const (
	maxPlaylists      = 200
	maxPlaylistSongs  = 2000
	maxPlaylistName   = 80
	maxLibraryRatings = 20000
)

var playlistIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

// librarySong is one exported song of a music project. Key identifies it
// across regenerations the way favorites do: project, track and seed.
type librarySong struct {
	Key          string  `json:"key"`
	Project      string  `json:"project"`
	TrackID      string  `json:"track_id"`
	Variant      int     `json:"variant"`
	Seed         int64   `json:"seed"`
	Title        string  `json:"title"`
	Caption      string  `json:"caption,omitempty"`
	Lyrics       string  `json:"lyrics,omitempty"`
	Instrumental bool    `json:"instrumental,omitempty"`
	BPM          int     `json:"bpm,omitempty"`
	KeyScale     string  `json:"keyscale,omitempty"`
	Language     string  `json:"vocal_language,omitempty"`
	Seconds      float64 `json:"audio_seconds,omitempty"`
	File         string  `json:"file"`
	Cover        string  `json:"cover,omitempty"`
	Favorite     bool    `json:"favorite,omitempty"`
	// Generator is what made the song: "ace-step", "heartmula" or "diffrhythm2" (read from the synthesis model the manifest records).
	Generator   string `json:"generator"`
	GeneratedAt string `json:"generated_at,omitempty"`
	// Client is who made the project the song belongs to (its scenes.yaml, else embedded or unknown).
	// Duration is the length of the audio as the song map reads it (the decoded samples), to the millisecond; absent when it cannot be measured.
	Duration float64 `json:"duration,omitempty"`
	Client   string  `json:"client"`
}

type libraryPlaylist struct {
	ID    string   `json:"id"`
	Name  string   `json:"name"`
	Songs []string `json:"songs"`
}

// libraryMeta is what the user adds to the songs: stars (1 to 5; no entry
// means unrated) and playlists (ordered lists of song keys).
type libraryMeta struct {
	Ratings   map[string]int    `json:"ratings"`
	Playlists []libraryPlaylist `json:"playlists"`
}

// LibraryResponse is the answer of GET /api/library: every song of the projects and the playlists kept besides them.
type LibraryResponse struct {
	Songs []librarySong `json:"songs"`
	libraryMeta
}

// libraryKey is the identity of a song.
func libraryKey(project, track string, seed int64) string {
	return project + "/" + track + "/" + strconv.FormatInt(seed, 10)
}

// GET /api/library: the songs of the database (the ones the filter and the content mode show) with the stars and playlists. It reads no folder.
func (h *Handler) handleLibraryGet(w http.ResponseWriter, r *http.Request) {
	filter, ok := listFilterOf(w, r)
	if !ok {
		return
	}
	scope, err := h.songScope(filter)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	rows, err := h.songs.All(scope)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	songs := make([]librarySong, 0, len(rows))
	for _, row := range rows {
		songs = append(songs, songOf(row))
	}
	meta, err := h.songs.Meta()
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	api.WriteJSON(w, http.StatusOK, LibraryResponse{Songs: songs, libraryMeta: h.visibleMeta(meta)})
}

// songOf is the item the listing answers for a row.
func songOf(row SongRow) librarySong {
	s := row.Doc.V
	s.Client, s.Favorite = row.Client, row.Favorite
	return s
}

// songScope keeps the songs of the projects a request may see: the ones the filter lists and the content mode of the server shows.
func (h *Handler) songScope(filter listFilter) (func(*gorm.DB) *gorm.DB, error) {
	clients, err := h.songs.Projects()
	if err != nil {
		return nil, err
	}
	var allowed []string
	for project, client := range clients {
		if filter.shows(project, client) && h.host.ProjectVisible(project) {
			allowed = append(allowed, project)
		}
	}
	return func(db *gorm.DB) *gorm.DB {
		if len(allowed) == 0 {
			return db.Where("1 = 0")
		}
		return db.Where("project_id IN ?", allowed)
	}, nil
}

// songsOf lists the songs of a music project that are still on disk and whose track is still in the project (the manifest keeps the entries of a
// track that was deleted afterwards), as its manifest says: dir is the folder of the project and client is who made it. A project that is not a
// music project, or has no manifest, has none. The duration is left for the caller to measure, which does it only when the audio changed.
func songsOf(id, dir string, cfg *config.Config, client string) []songSource {
	if cfg.Output.Mode != config.ModeMusic {
		return nil
	}
	m, err := export.ReadMusicManifest(filepath.Join(dir, cfg.ExportDir(), "manifest.json"))
	if err != nil {
		return nil
	}
	favorite := map[string]bool{}
	for _, f := range cfg.Favorites {
		favorite[libraryKey(id, f.Track, f.Seed)] = true
	}
	inProject := map[string]bool{}
	for _, t := range cfg.Tracks {
		inProject[t.ID] = true
	}
	createdAt, _ := time.Parse(time.RFC3339, m.GeneratedAt)
	var out []songSource
	for _, e := range m.Tracks {
		if !inProject[e.TrackID] {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, e.File)); err != nil {
			continue
		}
		cover := e.Cover
		if cover != "" {
			if _, err := os.Stat(filepath.Join(dir, cover)); err != nil {
				cover = ""
			}
		}
		title := e.Title
		if title == "" {
			title = e.TrackID
		}
		key := libraryKey(id, e.TrackID, e.EffectiveSeed)
		out = append(out, songSource{path: filepath.Join(dir, e.File), createdAt: createdAt, song: librarySong{
			Key: key, Project: id, TrackID: e.TrackID, Variant: e.Variant, Seed: e.EffectiveSeed, Title: title,
			Caption: e.Caption, Lyrics: e.LyricsSung, Instrumental: e.Instrumental, BPM: e.BPM, KeyScale: e.KeyScale,
			Language: e.VocalLanguage, Seconds: e.AudioSeconds, File: e.File, Cover: cover, Favorite: favorite[key], Generator: songGenerator(e.SynthModel),
			GeneratedAt: m.GeneratedAt, Client: client,
		}})
	}
	return out
}

// SyncLibrary makes the songs of the database of a project what its manifest exports now (see songStore.SyncProject). The server calls it when a
// run of the project ends, after its definition is saved and after a song is deleted; the programs that export songs without the server call it
// through the command line.
func (h *Handler) SyncLibrary(ctx context.Context, id string) (SyncReport, error) {
	return h.songs.syncFromProject(ctx, h.projects, id, h.ProjectDir(id))
}

// syncLibraryQuietly is SyncLibrary where a failure is only a line of the log: what called it has done what it was asked to.
func (h *Handler) syncLibraryQuietly(id string) {
	if _, err := h.SyncLibrary(context.Background(), id); err != nil {
		slog.Warn("cannot update the song library of a project", "project", id, "err", err)
	}
}

// songGenerator tells the generator from the synthesis model a song recorded: the services record their own
// name, ACE-Step records the model file.
func songGenerator(synthModel string) string {
	switch synthModel {
	case music.HeartMulaSynthModel:
		return config.GeneratorHeartMuLa
	case music.DiffRhythm2SynthModel:
		return config.GeneratorDiffRhythm2
	}
	return "ace-step"
}

// PUT /api/library/meta  {"ratings": {...}, "playlists": [...]}
func (h *Handler) handleLibraryMetaPut(w http.ResponseWriter, r *http.Request) {
	var in libraryMeta
	api.RaiseBodyLimit(w, r, 8<<20)
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20))
	if err := dec.Decode(&in); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
		return
	}
	if err := validateLibraryMeta(&in); err != nil {
		api.WriteError(w, http.StatusUnprocessableEntity, api.CodeValidation, err.Error())
		return
	}
	stored, err := h.songs.Meta()
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	in = h.keepHiddenMeta(in, stored)
	kept, err := h.songs.ReplaceMeta(in)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	api.WriteJSON(w, http.StatusOK, kept)
}

func validateLibraryMeta(m *libraryMeta) error {
	if m.Ratings == nil {
		m.Ratings = map[string]int{}
	}
	if m.Playlists == nil {
		m.Playlists = []libraryPlaylist{}
	}
	if len(m.Ratings) > maxLibraryRatings {
		return fmt.Errorf("too many ratings (max %d)", maxLibraryRatings)
	}
	for k, v := range m.Ratings {
		if strings.Count(k, "/") != 2 {
			return fmt.Errorf("rating key %q is not project/track/seed", k)
		}
		if v < 1 || v > 5 {
			return fmt.Errorf("rating of %q must be 1 to 5, got %d (leave a song unrated by omitting it)", k, v)
		}
	}
	if len(m.Playlists) > maxPlaylists {
		return fmt.Errorf("too many playlists (max %d)", maxPlaylists)
	}
	seen := map[string]bool{}
	for i := range m.Playlists {
		p := &m.Playlists[i]
		p.Name = strings.TrimSpace(p.Name)
		if !playlistIDPattern.MatchString(p.ID) {
			return fmt.Errorf("playlist id %q is invalid: lowercase letters, digits and hyphens", p.ID)
		}
		if seen[p.ID] {
			return fmt.Errorf("playlist id %q is repeated", p.ID)
		}
		seen[p.ID] = true
		if p.Name == "" || len([]rune(p.Name)) > maxPlaylistName {
			return fmt.Errorf("playlist %q needs a name of up to %d characters", p.ID, maxPlaylistName)
		}
		if p.Songs == nil {
			p.Songs = []string{}
		}
		if len(p.Songs) > maxPlaylistSongs {
			return fmt.Errorf("playlist %q has too many songs (max %d)", p.ID, maxPlaylistSongs)
		}
		for _, k := range p.Songs {
			if strings.Count(k, "/") != 2 {
				return fmt.Errorf("song key %q in playlist %q is not project/track/seed", k, p.ID)
			}
		}
	}
	return nil
}

// songKeyProject is the project of a song key ("project/track/seed").
func songKeyProject(key string) string {
	if i := strings.IndexByte(key, '/'); i >= 0 {
		return key[:i]
	}
	return key
}

// visibleMeta leaves out the ratings and playlist songs of the projects the server hides.
func (h *Handler) visibleMeta(m libraryMeta) libraryMeta {
	if h.host.ShowsAdult() {
		return m
	}
	out := libraryMeta{Ratings: map[string]int{}, Playlists: []libraryPlaylist{}}
	for k, v := range m.Ratings {
		if h.host.ProjectVisible(songKeyProject(k)) {
			out.Ratings[k] = v
		}
	}
	for _, p := range m.Playlists {
		q := libraryPlaylist{ID: p.ID, Name: p.Name, Songs: []string{}}
		for _, k := range p.Songs {
			if h.host.ProjectVisible(songKeyProject(k)) {
				q.Songs = append(q.Songs, k)
			}
		}
		out.Playlists = append(out.Playlists, q)
	}
	return out
}

// keepHiddenMeta puts back what the stored metadata holds for hidden projects: the client
// only ever saw the rest, so saving its version must not erase them.
func (h *Handler) keepHiddenMeta(in, stored libraryMeta) libraryMeta {
	if h.host.ShowsAdult() {
		return in
	}
	for k, v := range stored.Ratings {
		if !h.host.ProjectVisible(songKeyProject(k)) {
			in.Ratings[k] = v
		}
	}
	byID := map[string]int{}
	for i, p := range in.Playlists {
		byID[p.ID] = i
	}
	for _, p := range stored.Playlists {
		i, ok := byID[p.ID]
		if !ok {
			continue
		}
		for _, k := range p.Songs {
			if !h.host.ProjectVisible(songKeyProject(k)) {
				in.Playlists[i].Songs = append(in.Playlists[i].Songs, k)
			}
		}
	}
	return in
}

// measureSong is the length a song map will give the song: see decodedSeconds.
func (h *Handler) measureSong(ctx context.Context, path string) float64 {
	return decodedSeconds(ctx, h.host.LookPath, h.host.Runner(), path)
}

// decodedSeconds is the length the song map will give an audio file: the decoded samples over the rate, the same decode as the map's, which is
// what a script has to cover to the hundredth (ffprobe differs by tens of milliseconds on an mp3). Zero when it cannot be measured (no ffmpeg, an
// unreadable file). The library measures it once, when it writes the row of the song (see SyncProject).
func decodedSeconds(ctx context.Context, look func(string) (string, error), run runner.CommandRunner, path string) float64 {
	if _, err := look("ffmpeg"); err != nil {
		return 0
	}
	raw, err := run.Run(ctx, "ffmpeg", []string{"-v", "error", "-i", path, "-vn", "-ac", "1", "-ar", fmt.Sprint(audiomeasure.Rate), "-f", "f32le", "-"})
	if err != nil || len(raw) == 0 {
		return 0
	}
	return math.Round(float64(len(raw)/4)/float64(audiomeasure.Rate)*1000) / 1000
}
