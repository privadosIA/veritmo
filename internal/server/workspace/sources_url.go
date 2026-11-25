package workspace

// sources_url.go adds a project source from a YouTube address: yt-dlp takes
// the audio of one video and it is stored as sources/<name>.mp3. No account,
// cookie or option from the request is ever used (see specs/project-management).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"veritmo/internal/project/config"
	"veritmo/internal/server/api"
)

const (
	// maxURLSourceSeconds bounds the length of a video that is downloaded.
	maxURLSourceSeconds = 20 * 60
	// maxSourceNameLen is the longest base name taken from a video title.
	maxSourceNameLen = 60
)

var asciiFold = strings.NewReplacer(
	"á", "a", "à", "a", "ä", "a", "â", "a", "ã", "a", "å", "a", "é", "e", "è", "e", "ë", "e", "ê", "e",
	"í", "i", "ì", "i", "ï", "i", "î", "i", "ó", "o", "ò", "o", "ö", "o", "ô", "o", "õ", "o", "ø", "o",
	"ú", "u", "ù", "u", "ü", "u", "û", "u", "ñ", "n", "ç", "c", "ß", "ss", "æ", "ae", "œ", "oe",
)

var nonNameChar = regexp.MustCompile(`[^a-z0-9._-]+`)

// sourceBaseName reduces a video title to a file-name base: lower case,
// accents folded, anything else a hyphen, at most maxSourceNameLen characters.
func sourceBaseName(title string) string {
	s := asciiFold.Replace(strings.ToLower(strings.TrimSpace(title)))
	s = strings.Trim(nonNameChar.ReplaceAllString(s, "-"), "-._")
	if len(s) > maxSourceNameLen {
		s = strings.Trim(s[:maxSourceNameLen], "-._")
	}
	if s == "" {
		return "youtube"
	}
	return s
}

// uniqueSourceName appends -2, -3… until sources/<base>.mp3 is free.
func uniqueSourceName(dir, base string) string {
	name := base + ".mp3"
	for i := 2; ; i++ {
		if _, err := os.Lstat(filepath.Join(dir, name)); err != nil {
			return name
		}
		name = fmt.Sprintf("%s-%d.mp3", base, i)
	}
}

// POST /api/projects/{id}/sources/from-url  {"url": "...", "name": "optional.mp3"}
func (h *Handler) handleSourceFromURL(w http.ResponseWriter, r *http.Request) {
	id, ok := h.RequireProject(w, r)
	if !ok {
		return
	}
	var req struct {
		URL  string `json:"url"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
		return
	}
	target, err := api.ParseYouTubeURL(req.URL)
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, err.Error())
		return
	}
	if req.Name != "" {
		if !sourceName(w, req.Name) {
			return
		}
		if strings.ToLower(filepath.Ext(req.Name)) != ".mp3" {
			api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "the audio is saved as an mp3: the name must end in .mp3")
			return
		}
	}
	if _, err := h.host.LookPath("yt-dlp"); err != nil {
		api.WriteError(w, http.StatusServiceUnavailable, api.CodeUnavailable, "yt-dlp is not installed: run `bash scripts/get-ytdlp.sh`")
		return
	}
	if _, err := h.host.LookPath("ffmpeg"); err != nil {
		api.WriteError(w, http.StatusServiceUnavailable, api.CodeUnavailable, "ffmpeg is not installed: it is needed to take the audio out of a video")
		return
	}
	dir := h.SourcesDir(id)
	if req.Name != "" {
		if _, err := os.Lstat(filepath.Join(dir, req.Name)); err == nil {
			api.WriteError(w, http.StatusConflict, api.CodeConflict, fmt.Sprintf("source %q already exists: choose another name", req.Name))
			return
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	work, err := os.MkdirTemp(dir, ".url-*")
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	defer os.RemoveAll(work)

	ctx, cancel := context.WithTimeout(r.Context(), api.URLSourceTimeout)
	defer cancel()
	// --ignore-config keeps any config file (cookies, accounts, extra
	// options) out of the call; the address goes after -- as one argument.
	meta, err := h.host.Runner().Run(ctx, "yt-dlp", []string{"--ignore-config", "--no-playlist", "--no-warnings",
		"--print", "%(duration)s\n%(title)s", "--", target})
	if err != nil {
		if r.Context().Err() != nil {
			return // the client went away: nothing to answer
		}
		if ctx.Err() != nil {
			api.WriteError(w, http.StatusGatewayTimeout, api.CodeUnavailable, "the download took more than 5 minutes and was stopped")
			return
		}
		api.YtdlpFailure(w, err)
		return
	}
	lines := strings.SplitN(strings.TrimSpace(string(meta)), "\n", 2)
	secs, perr := strconv.ParseFloat(strings.TrimSpace(lines[0]), 64)
	if perr != nil || secs <= 0 {
		api.WriteError(w, http.StatusUnprocessableEntity, api.CodeValidation, "cannot tell how long this video is (a live stream?): only finished videos are accepted")
		return
	}
	if secs > maxURLSourceSeconds {
		api.WriteError(w, http.StatusUnprocessableEntity, api.CodeValidation, fmt.Sprintf("the video lasts %d minutes: the limit is %d", int(secs/60), maxURLSourceSeconds/60))
		return
	}
	title := ""
	if len(lines) > 1 {
		title = lines[1]
	}

	if _, err := h.host.Runner().Run(ctx, "yt-dlp", []string{"--ignore-config", "--no-playlist", "--no-warnings",
		"-x", "--audio-format", "mp3", "--audio-quality", "2",
		"--max-filesize", strconv.FormatInt(config.MaxSourceBytes, 10),
		"-o", filepath.Join(work, "audio.%(ext)s"), "--", target}); err != nil {
		if r.Context().Err() != nil {
			return
		}
		if ctx.Err() != nil {
			api.WriteError(w, http.StatusGatewayTimeout, api.CodeUnavailable, "the download took more than 5 minutes and was stopped")
			return
		}
		api.YtdlpFailure(w, err)
		return
	}
	audio := filepath.Join(work, "audio.mp3")
	fi, err := os.Stat(audio)
	if err != nil || fi.Size() == 0 {
		api.WriteError(w, http.StatusUnprocessableEntity, api.CodeValidation, "the download produced no audio")
		return
	}
	if fi.Size() > config.MaxSourceBytes {
		api.WriteError(w, http.StatusUnprocessableEntity, api.CodeValidation, fmt.Sprintf("the audio is larger than %d MB", config.MaxSourceBytes>>20))
		return
	}
	name := req.Name
	if name == "" {
		name = uniqueSourceName(dir, sourceBaseName(title))
	}
	dst := filepath.Join(dir, name)
	if _, err := os.Lstat(dst); err == nil {
		api.WriteError(w, http.StatusConflict, api.CodeConflict, fmt.Sprintf("source %q already exists: choose another name", name))
		return
	}
	if err := os.Rename(audio, dst); err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	api.WriteJSON(w, http.StatusCreated, map[string]string{"name": name})
}
