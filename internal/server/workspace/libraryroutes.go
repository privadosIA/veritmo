package workspace

// libraryroutes.go: the routes of the song library besides GET /api/library and PUT /api/library/meta (library.go): the songs in pages and one by
// one, a rating, the playlists one by one and the rescan (change move-song-library-to-database).

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"gorm.io/gorm"

	"veritmo/internal/platform/store"
	"veritmo/internal/project/config"
	"veritmo/internal/server/api"
)

// SongsListSpec says what the list of songs can be sorted and searched by: the newest first unless asked otherwise.
var SongsListSpec = store.ListSpec{
	Sortable:   map[string]string{"created_at": "songs.created_at", "title": "songs.title", "rating": "songs.rating", "duration": "songs.duration"},
	Searchable: []string{"songs.title", "songs.caption", "songs.lyrics"},
	Default:    "-created_at",
	Tie:        "songs.song_key",
}

// songView is a song as the routes of one song and of the list of songs answer it: the item of GET /api/library and its stars (absent when none).
type songView struct {
	librarySong
	Rating int `json:"rating,omitempty"`
}

// viewOfSong is a song as the routes of the library answer it: the item of the listing and its stars.
func viewOfSong(row SongRow) songView { return songView{librarySong: songOf(row), Rating: row.Rating} }

// songVisible says whether a song is one the server shows: its project is visible in the content mode.
func (h *Handler) songVisible(key string) bool { return h.host.ProjectVisible(songKeyProject(key)) }

// GET /api/library/songs[?limit=&offset=&sort=&q=&project=&client=&managed=&min_rating=&favorite=true&playlist=&generator=]: the songs of the library in pages.
func (h *Handler) handleLibrarySongs(w http.ResponseWriter, r *http.Request) {
	filter, ok := listFilterOf(w, r)
	if !ok {
		return
	}
	params, err := store.ParseList(r, SongsListSpec)
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, err.Error())
		return
	}
	q := r.URL.Query()
	minRating := 0
	if v := q.Get("min_rating"); v != "" {
		if minRating, err = strconv.Atoi(v); err != nil || minRating < 1 || minRating > 5 {
			api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "min_rating must be a whole number from 1 to 5")
			return
		}
	}
	if v := q.Get("favorite"); v != "" && v != "true" {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "favorite is only accepted as true")
		return
	}
	generator := q.Get("generator")
	switch generator {
	case "", config.GeneratorACEStep, config.GeneratorHeartMuLa, config.GeneratorDiffRhythm2:
	default:
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("generator must be %s, %s or %s", config.GeneratorACEStep, config.GeneratorHeartMuLa, config.GeneratorDiffRhythm2))
		return
	}
	playlist := q.Get("playlist")
	if playlist != "" {
		if _, err := h.songs.Playlist(playlist); errors.Is(err, ErrNoPlaylist) {
			api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("playlist %q does not exist", playlist))
			return
		}
	}
	scope, err := h.songScope(filter)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	spec := SongsListSpec
	base := scope(h.songs.db.DB.Model(&SongRow{}))
	if project := q.Get("project"); project != "" {
		base = base.Where("songs.project_id = ?", project)
	}
	if minRating > 0 {
		base = base.Where("songs.rating >= ?", minRating)
	}
	if generator != "" {
		base = base.Where("songs.model = ?", generator)
	}
	if q.Get("favorite") == "true" {
		base = base.Where("songs.favorite = ?", true)
	}
	if playlist != "" {
		base = base.Joins("JOIN playlist_songs ON playlist_songs.song_key = songs.song_key AND playlist_songs.playlist_id = ?", playlist)
		if params.Sort == "" {
			spec.Default, spec.Tie = "", "playlist_songs.position" // the order of the playlist
		}
	}
	base = store.Filter(base, params, spec)
	var total int64
	if err := base.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	page := store.Order(base.Select("songs.*"), params, spec)
	if params.Limit > 0 {
		page = page.Limit(params.Limit)
	}
	if params.Offset > 0 {
		page = page.Offset(params.Offset)
	}
	var rows []SongRow
	if err := page.Find(&rows).Error; err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	out := make([]songView, 0, len(rows))
	for _, row := range rows {
		out = append(out, viewOfSong(row))
	}
	store.WriteListHeaders(w, r, total, params)
	api.WriteJSON(w, http.StatusOK, out)
}

// songFromPath reads the key of the path (its slashes sent as %2F) and the song; it writes the 404 itself.
func (h *Handler) songFromPath(w http.ResponseWriter, r *http.Request) (SongRow, bool) {
	key := r.PathValue("key")
	row, err := h.songs.Get(key)
	if errors.Is(err, ErrNoSong) || (err == nil && !h.songVisible(key)) {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("song %q does not exist", key))
		return SongRow{}, false
	}
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return SongRow{}, false
	}
	return row, true
}

// GET /api/library/songs/{key}: one song (the slashes of its key as %2F).
func (h *Handler) handleLibrarySong(w http.ResponseWriter, r *http.Request) {
	row, ok := h.songFromPath(w, r)
	if !ok {
		return
	}
	api.WriteJSON(w, http.StatusOK, viewOfSong(row))
}

// PUT /api/library/songs/{key}/rating  {"stars": 0..5}: sets the stars of one song, 0 removes them.
func (h *Handler) handleLibrarySongRating(w http.ResponseWriter, r *http.Request) {
	row, ok := h.songFromPath(w, r)
	if !ok {
		return
	}
	var in struct {
		Stars *int `json:"stars"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&in); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
		return
	}
	if in.Stars == nil || *in.Stars < 0 || *in.Stars > 5 {
		api.WriteError(w, http.StatusUnprocessableEntity, api.CodeValidation, "stars must be a whole number from 1 to 5, or 0 to remove them")
		return
	}
	if err := h.songs.SetRating(row.Key, *in.Stars); err != nil {
		if errors.Is(err, ErrNoSong) {
			api.WriteError(w, http.StatusNotFound, api.CodeNotFound, err.Error())
		} else {
			api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		}
		return
	}
	row.Rating = *in.Stars
	api.WriteJSON(w, http.StatusOK, viewOfSong(row))
}

// POST /api/library/rescan: reads what every project exports again, for audio put or removed by hand, and says how many songs that added, updated
// and removed.
func (h *Handler) handleLibraryRescan(w http.ResponseWriter, r *http.Request) {
	ids := map[string]bool{}
	for _, id := range h.projectIDs() {
		ids[id] = true
	}
	withSongs, err := h.songs.Projects() // a project that is gone still has rows to remove
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	for id := range withSongs {
		ids[id] = true
	}
	var total SyncReport
	for id := range ids {
		if !h.host.ProjectVisible(id) {
			continue
		}
		rep, err := h.SyncLibrary(r.Context(), id)
		if err != nil {
			api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, fmt.Sprintf("cannot read the songs of %q: %v", id, err))
			return
		}
		total.add(rep)
	}
	api.WriteJSON(w, http.StatusOK, map[string]int{"added": total.Added, "updated": total.Updated, "removed": total.Removed})
}

// --- playlists --------------------------------------------------------------------------------------------

// playlistSummary is a playlist in the list of playlists: how many songs it holds, not the songs.
type playlistSummary struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Songs int    `json:"songs"`
}

// GET /api/library/playlists: the playlists in order.
func (h *Handler) handleLibraryPlaylists(w http.ResponseWriter, r *http.Request) {
	meta, err := h.songs.Meta()
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	out := []playlistSummary{}
	for _, p := range meta.Playlists {
		out = append(out, playlistSummary{ID: p.ID, Name: p.Name, Songs: len(p.Songs) - len(h.hiddenKeys(p.Songs))})
	}
	api.WriteJSON(w, http.StatusOK, out)
}

// playlistBody is what a playlist is made or replaced from.
type playlistBody struct {
	ID    string   `json:"id"`
	Name  string   `json:"name"`
	Songs []string `json:"songs"`
}

// readPlaylistBody reads and checks a playlist (the limits of PUT /api/library/meta); it writes the 400 or 422 itself. The songs are checked later,
// against the library, by the store.
func readPlaylistBody(w http.ResponseWriter, r *http.Request) (playlistBody, bool) {
	var in playlistBody
	api.RaiseBodyLimit(w, r, 1<<20)
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
		return in, false
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len([]rune(in.Name)) > maxPlaylistName {
		api.WriteError(w, http.StatusUnprocessableEntity, api.CodeValidation, fmt.Sprintf("a playlist needs a name of up to %d characters", maxPlaylistName))
		return in, false
	}
	if len(in.Songs) > maxPlaylistSongs {
		api.WriteError(w, http.StatusUnprocessableEntity, api.CodeValidation, fmt.Sprintf("a playlist has too many songs (max %d)", maxPlaylistSongs))
		return in, false
	}
	if in.Songs == nil {
		in.Songs = []string{}
	}
	return in, true
}

// playlistID is the id a name gives: lowercase letters and digits joined by hyphens, as long as the pattern allows; "" when the name gives none.
func playlistID(name string) string {
	fold := strings.NewReplacer("á", "a", "à", "a", "ä", "a", "â", "a", "é", "e", "è", "e", "ë", "e", "ê", "e", "í", "i", "ì", "i", "ï", "i", "î", "i",
		"ó", "o", "ò", "o", "ö", "o", "ô", "o", "ú", "u", "ù", "u", "ü", "u", "û", "u", "ñ", "n", "ç", "c")
	var b strings.Builder
	hyphen := true // a leading hyphen is not written
	for _, c := range fold.Replace(strings.ToLower(name)) {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			b.WriteRune(c)
			hyphen = false
		} else if !hyphen {
			b.WriteByte('-')
			hyphen = true
		}
	}
	id := strings.TrimRight(b.String(), "-")
	if len(id) > 40 {
		id = strings.TrimRight(id[:40], "-")
	}
	return id
}

// writePlaylistError answers what the store said about a playlist: 409 for an id that exists, 404 for one that does not and 422 naming the
// first key that is not a song.
func writePlaylistError(w http.ResponseWriter, err error) {
	var unknown *ErrUnknownSongs
	switch {
	case errors.Is(err, ErrPlaylistExists):
		api.WriteError(w, http.StatusConflict, api.CodeConflict, err.Error())
	case errors.Is(err, ErrNoPlaylist):
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, err.Error())
	case errors.As(err, &unknown):
		api.WriteError(w, http.StatusUnprocessableEntity, api.CodeValidation, err.Error())
	default:
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
	}
}

// hiddenKeys are the keys among keys that belong to a project the server hides.
func (h *Handler) hiddenKeys(keys []string) []string {
	var out []string
	for _, k := range keys {
		if !h.songVisible(k) {
			out = append(out, k)
		}
	}
	return out
}

// POST /api/library/playlists  {id?, name, songs?}: makes a playlist; 201 with it.
func (h *Handler) handleLibraryPlaylistCreate(w http.ResponseWriter, r *http.Request) {
	in, ok := readPlaylistBody(w, r)
	if !ok {
		return
	}
	if in.ID == "" {
		if in.ID = playlistID(in.Name); in.ID == "" {
			api.WriteError(w, http.StatusUnprocessableEntity, api.CodeValidation, "the name gives no id: send an id of lowercase letters, digits and hyphens")
			return
		}
	} else if !playlistIDPattern.MatchString(in.ID) {
		api.WriteError(w, http.StatusUnprocessableEntity, api.CodeValidation, fmt.Sprintf("playlist id %q is invalid: lowercase letters, digits and hyphens", in.ID))
		return
	}
	if n, err := h.songs.PlaylistCount(); err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	} else if n >= maxPlaylists {
		api.WriteError(w, http.StatusUnprocessableEntity, api.CodeValidation, fmt.Sprintf("too many playlists (max %d)", maxPlaylists))
		return
	}
	if hidden := h.hiddenKeys(in.Songs); len(hidden) > 0 {
		writePlaylistError(w, &ErrUnknownSongs{Keys: hidden})
		return
	}
	p := libraryPlaylist(in)
	if err := h.songs.CreatePlaylist(p); err != nil {
		writePlaylistError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, p)
}

// playlistFromPath reads a playlist of the path as the client may see it (the songs of hidden projects left out); it writes the 404 itself.
func (h *Handler) playlistFromPath(w http.ResponseWriter, r *http.Request) (libraryPlaylist, bool) {
	p, err := h.songs.Playlist(r.PathValue("id"))
	if err != nil {
		writePlaylistError(w, fmt.Errorf("%w: %q", err, r.PathValue("id")))
		return p, false
	}
	return p, true
}

// GET /api/library/playlists/{id}: a playlist with its song keys in order.
func (h *Handler) handleLibraryPlaylistGet(w http.ResponseWriter, r *http.Request) {
	p, ok := h.playlistFromPath(w, r)
	if !ok {
		return
	}
	kept := []string{}
	for _, k := range p.Songs {
		if h.songVisible(k) {
			kept = append(kept, k)
		}
	}
	p.Songs = kept
	api.WriteJSON(w, http.StatusOK, p)
}

// PUT /api/library/playlists/{id}  {name, songs}: replaces the name and the songs of a playlist. What the content mode hides stays in it, at the end.
func (h *Handler) handleLibraryPlaylistPut(w http.ResponseWriter, r *http.Request) {
	stored, ok := h.playlistFromPath(w, r)
	if !ok {
		return
	}
	in, ok := readPlaylistBody(w, r)
	if !ok {
		return
	}
	if hidden := h.hiddenKeys(in.Songs); len(hidden) > 0 {
		writePlaylistError(w, &ErrUnknownSongs{Keys: hidden})
		return
	}
	songs := append([]string{}, in.Songs...)
	songs = append(songs, h.hiddenKeys(stored.Songs)...)
	if err := h.songs.ReplacePlaylist(stored.ID, in.Name, songs); err != nil {
		writePlaylistError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, libraryPlaylist{ID: stored.ID, Name: in.Name, Songs: in.Songs})
}

// DELETE /api/library/playlists/{id}
func (h *Handler) handleLibraryPlaylistDelete(w http.ResponseWriter, r *http.Request) {
	if err := h.songs.DeletePlaylist(r.PathValue("id")); err != nil {
		writePlaylistError(w, fmt.Errorf("%w: %q", err, r.PathValue("id")))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
