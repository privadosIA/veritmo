package models

// downloads.go implements the download half of the model-management
// capability: an in-memory, single-worker queue of background transfers
// into the asset directories, written to .partial files and renamed only
// after every file of the job arrived, with SSE progress that reuses the
// run event log (see specs/model-management and design D1-D7).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"veritmo/internal/server/workspace"

	"veritmo/internal/models/convrot"
	"veritmo/internal/platform/edition"
	"veritmo/internal/server/api"
)

// Download states.
const (
	dlQueued    = "queued"
	dlRunning   = "running"
	dlSucceeded = "succeeded"
	dlFailed    = "failed"
	dlCancelled = "cancelled"
)

const (
	maxDownloadHistory = 50
	maxRedirects       = 10
	progressInterval   = 250 * time.Millisecond
)

// Download is the public record of one download job.
type Download struct {
	ID         string     `json:"id"`
	State      string     `json:"state"`
	Kind       string     `json:"kind"`
	Name       string     `json:"name"`
	Files      []string   `json:"files"`
	Source     string     `json:"source"`
	Bytes      int64      `json:"bytes"`
	Total      int64      `json:"total,omitempty"`
	Error      string     `json:"error,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

type dlFile struct {
	name string
	url  string
	path string // final destination
	// convert is the catalogFile.Convert of the file: what is downloaded is turned into the installed file.
	convert string
}

type dlJob struct {
	rec        Download
	files      []dlFile
	overwrite  bool
	log        *api.EventLog
	cancel     context.CancelFunc
	userCancel bool
}

type downloadManager struct {
	mu     sync.Mutex
	jobs   map[string]*dlJob
	order  []string // creation order, oldest first
	queue  []*dlJob
	active *dlJob

	counter      int
	rootCtx      context.Context
	rootCancel   context.CancelFunc
	shuttingDown bool
	wg           sync.WaitGroup

	client *http.Client
	// allowPrivate lets tests reach httptest servers on loopback.
	allowPrivate bool
	hfToken      func() string
	// civitaiToken returns the Civitai API key ("" when none); read on every
	// transfer so a replaced key file takes effect without a restart.
	civitaiToken func() string
	// isCivitai decides whether a request host is Civitai (token, access
	// guidance, name hint); a seam so tests can point it at httptest.
	isCivitai func(host string) bool
	// civitaiAPI is the base address of the Civitai API; a seam so tests can
	// point it at httptest.
	civitaiAPI string
	// isHF decides whether a request host gets Hugging Face treatment
	// (token, access guidance); a seam so tests can point it at httptest.
	isHF      func(host string) bool
	freeBytes func(dir string) (uint64, error)
	now       func() time.Time
}

func newDownloadManager(hfKey func() string) *downloadManager {
	ctx, cancel := context.WithCancel(context.Background())
	m := &downloadManager{
		jobs:       make(map[string]*dlJob),
		rootCtx:    ctx,
		rootCancel: cancel,
		hfToken:    hfKey,
		civitaiToken: func() string {
			return readCivitaiToken(os.Getenv, CivitaiTokenFile())
		},
		isCivitai:  isCivitaiHost,
		civitaiAPI: civitaiAPIBase,
		isHF:       isHFHost,
		freeBytes:  workspace.FreeDiskBytes,
		now:        time.Now,
	}
	m.client = &http.Client{
		Transport: &http.Transport{
			Proxy:                 nil, // dial-time address checks must see the real target
			DialContext:           m.dialContext,
			TLSHandshakeTimeout:   15 * time.Second,
			ResponseHeaderTimeout: 60 * time.Second,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return fmt.Errorf("stopped after %d redirects", maxRedirects)
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return fmt.Errorf("redirect to unsupported scheme %q", req.URL.Scheme)
			}
			// A Civitai download that needs an account redirects to the
			// login page: following it would save HTML as the model.
			if m.isCivitai(req.URL.Hostname()) && strings.HasPrefix(req.URL.Path, "/login") {
				return errCivitaiLogin
			}
			return nil
		},
	}
	return m
}

// blockedIP reports addresses a download must never reach: loopback,
// private, link-local, multicast, unspecified and carrier-grade NAT.
func blockedIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}
	if v4 := ip.To4(); v4 != nil && v4[0] == 100 && v4[1]&0xC0 == 64 { // 100.64.0.0/10
		return true
	}
	return false
}

// dialContext resolves the host itself, refuses non-public addresses and
// dials the resolved IP, so redirects and DNS tricks cannot reach internal
// services.
func (m *downloadManager) dialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("no addresses for %s", host)
	}
	if !m.allowPrivate {
		for _, ip := range ips {
			if blockedIP(ip.IP) {
				return nil, fmt.Errorf("refusing to connect to non-public address %s (%s)", ip.IP, host)
			}
		}
	}
	var d net.Dialer
	var lastErr error
	for _, ip := range ips {
		c, err := d.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
		if err == nil {
			return c, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// ---------------------------------------------------------------------------
// requests
// ---------------------------------------------------------------------------

type downloadRequest struct {
	Kind      string `json:"kind"`
	ID        string `json:"id"` // catalog entry id
	URL       string `json:"url"`
	Repo      string `json:"repo"`
	File      string `json:"file"`
	Revision  string `json:"revision"`
	Name      string `json:"name"`
	Overwrite bool   `json:"overwrite"`
}

type dlError struct {
	status int
	code   string
	msg    string
}

func (e *dlError) Error() string { return e.msg }

var (
	hfRepoRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*/[A-Za-z0-9][A-Za-z0-9._-]*$`)
	hfRevRe  = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)
)

// resolve turns a request into a validated job description.
func (h *Handler) resolveDownload(req downloadRequest) (kind AssetKind, source, display string, files []dlFile, derr *dlError) {
	bad := func(status int, code, format string, a ...any) *dlError {
		return &dlError{status, code, fmt.Sprintf(format, a...)}
	}

	if req.ID != "" {
		entry, ok := h.findCatalog(req.ID)
		if !ok {
			return kind, "", "", nil, bad(http.StatusNotFound, api.CodeNotFound, "unknown catalog entry %q", req.ID)
		}
		k, _ := FindKind(entry.Kind)
		for _, f := range entry.Files {
			p, err := h.assetPath(k, f.Name)
			if err != nil {
				return kind, "", "", nil, bad(http.StatusInternalServerError, api.CodeInternal, "catalog entry %q is invalid: %v", req.ID, err)
			}
			files = append(files, dlFile{name: f.Name, url: f.URL, path: p, convert: f.Convert})
		}
		return k, "catalog:" + entry.ID, entry.Name, files, nil
	}

	k, ok := FindKind(req.Kind)
	if !ok {
		return kind, "", "", nil, bad(http.StatusBadRequest, api.CodeInvalid, "kind must be one of model, voice, lora, video, music, adapter")
	}

	var rawURL, defName, urlHost string
	switch {
	case req.URL != "" && (req.Repo != "" || req.File != ""):
		return k, "", "", nil, bad(http.StatusBadRequest, api.CodeInvalid, "give either url or repo+file, not both")
	case req.URL != "":
		u, err := url.Parse(strings.TrimSpace(req.URL))
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return k, "", "", nil, bad(http.StatusBadRequest, api.CodeInvalid, "url must be an absolute http(s) URL")
		}
		u.Fragment = ""
		rawURL = u.String()
		urlHost = u.Hostname()
		if !edition.CivitaiEnabled() && isCivitaiHost(urlHost) {
			return k, "", "", nil, bad(http.StatusBadRequest, api.CodeInvalid, "this edition does not download from Civitai")
		}
		defName = path.Base(u.Path)
		source = rawURL
		if u.User != nil {
			source = strings.Replace(source, u.User.String()+"@", "", 1) // never echo credentials
			rawURL = u.String()
		}
	case req.Repo != "" && req.File != "":
		if !hfRepoRe.MatchString(req.Repo) {
			return k, "", "", nil, bad(http.StatusBadRequest, api.CodeInvalid, "repo must look like org/name")
		}
		file := path.Clean(strings.ReplaceAll(req.File, `\`, "/"))
		if file == "." || strings.HasPrefix(file, "/") || file == ".." || strings.HasPrefix(file, "../") {
			return k, "", "", nil, bad(http.StatusBadRequest, api.CodeInvalid, "file must be a relative path inside the repository")
		}
		rev := req.Revision
		if rev == "" {
			rev = "main"
		}
		if !hfRevRe.MatchString(rev) || strings.Contains(rev, "..") {
			return k, "", "", nil, bad(http.StatusBadRequest, api.CodeInvalid, "invalid revision")
		}
		segs := strings.Split(file, "/")
		for i := range segs {
			segs[i] = url.PathEscape(segs[i])
		}
		rawURL = "https://huggingface.co/" + req.Repo + "/resolve/" + rev + "/" + strings.Join(segs, "/")
		defName = path.Base(file)
		source = "hf:" + req.Repo + "/" + file
	default:
		return k, "", "", nil, bad(http.StatusBadRequest, api.CodeInvalid, "give a url, or repo and file, or a catalog id")
	}

	name := req.Name
	if name == "" {
		name, _ = url.PathUnescape(defName)
	}
	p, err := h.assetPath(k, name)
	if err != nil {
		if req.Name == "" && urlHost != "" && h.dm.isCivitai(urlHost) {
			return k, "", "", nil, bad(http.StatusUnprocessableEntity, api.CodeValidation,
				"Civitai download URLs carry no file name: give \"name\", for example \"modelo.safetensors\" (%v)", err)
		}
		return k, "", "", nil, bad(http.StatusUnprocessableEntity, api.CodeValidation, "%v", err)
	}
	return k, source, name, []dlFile{{name: name, url: rawURL, path: p}}, nil
}

// start validates and enqueues a download.
func (h *Handler) startDownload(req downloadRequest) (Download, *dlError) {
	if err := h.host.ModelsIssue(); err != nil {
		return Download{}, &dlError{status: http.StatusServiceUnavailable, code: api.CodeInternal, msg: err.Error()}
	}
	k, source, display, files, derr := h.resolveDownload(req)
	if derr != nil {
		return Download{}, derr
	}
	// Catalog bundles share files (e.g. a VAE): install only what is
	// missing, and say so when nothing is.
	if req.ID != "" && !req.Overwrite {
		var missing []dlFile
		for _, f := range files {
			if _, err := os.Lstat(f.path); err != nil {
				missing = append(missing, f)
			}
		}
		if len(missing) == 0 {
			return Download{}, &dlError{http.StatusConflict, api.CodeConflict, fmt.Sprintf("%s is already installed", display)}
		}
		files = missing
	}
	m := h.dm
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.shuttingDown {
		return Download{}, &dlError{http.StatusServiceUnavailable, api.CodeInternal, "server is shutting down"}
	}
	for _, f := range files {
		if !req.Overwrite {
			if _, err := os.Lstat(f.path); err == nil {
				return Download{}, &dlError{http.StatusConflict, api.CodeConflict, fmt.Sprintf("%s already exists (set overwrite to replace it)", f.name)}
			}
		}
		for _, j := range m.jobs {
			if j.rec.State != dlQueued && j.rec.State != dlRunning {
				continue
			}
			for _, jf := range j.files {
				if jf.path == f.path {
					return Download{}, &dlError{http.StatusConflict, api.CodeConflict, fmt.Sprintf("%s is already being downloaded (%s)", f.name, j.rec.ID)}
				}
			}
		}
	}

	if len(m.queue) >= maxQueuedDownloads {
		return Download{}, &dlError{http.StatusTooManyRequests, api.CodeQueueFull, fmt.Sprintf("the download queue is full (%d waiting): wait for one to finish or cancel one", maxQueuedDownloads)}
	}
	m.counter++
	id := fmt.Sprintf("dl-%d-%d", m.now().Unix(), m.counter)
	names := make([]string, len(files))
	for i, f := range files {
		names[i] = f.name
	}
	j := &dlJob{
		rec: Download{ID: id, State: dlQueued, Kind: k.Kind, Name: display, Files: names,
			Source: source, CreatedAt: m.now().UTC()},
		files:     files,
		overwrite: req.Overwrite,
		log:       api.NewEventLog(nil),
	}
	m.jobs[id] = j
	m.order = append(m.order, id)
	m.queue = append(m.queue, j)
	m.emitStateLocked(j)
	m.trimLocked()
	m.scheduleLocked(h)
	return j.rec, nil
}

// ---------------------------------------------------------------------------
// maxQueuedDownloads is how many downloads may wait for the worker; one more is refused with 429 queue_full.
const maxQueuedDownloads = 20

// queue and worker
// ---------------------------------------------------------------------------

func (m *downloadManager) scheduleLocked(h *Handler) {
	if m.active != nil || len(m.queue) == 0 || m.shuttingDown {
		return
	}
	j := m.queue[0]
	m.queue = m.queue[1:]
	ctx, cancel := context.WithCancel(m.rootCtx)
	j.cancel = cancel
	j.rec.State = dlRunning
	m.active = j
	m.emitStateLocked(j)
	m.wg.Add(1)
	go m.work(ctx, h, j)
}

func (m *downloadManager) work(ctx context.Context, h *Handler, j *dlJob) {
	defer m.wg.Done()
	err := m.transferAll(ctx, h, j)

	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now().UTC()
	j.rec.FinishedAt = &now
	switch {
	case err == nil:
		j.rec.State = dlSucceeded
	case j.userCancel || m.shuttingDown || errors.Is(ctx.Err(), context.Canceled):
		j.rec.State = dlCancelled
	default:
		j.rec.State = dlFailed
		j.rec.Error = err.Error()
	}
	j.cancel()
	m.active = nil
	m.emitStateLocked(j)
	j.log.Close()
	m.trimLocked()
	m.scheduleLocked(h)
}

// emitStateLocked appends a download-state event to the job's log.
func (m *downloadManager) emitStateLocked(j *dlJob) {
	m.emitLocked(j, "download-state", map[string]any{"id": j.rec.ID, "state": j.rec.State, "error": j.rec.Error})
}

func (m *downloadManager) emitLocked(j *dlJob, typ string, data any) {
	b, _ := json.Marshal(map[string]any{"type": typ, "data": data})
	_, _ = j.log.Write(b)
}

// trimLocked forgets the oldest terminal jobs beyond the history cap.
func (m *downloadManager) trimLocked() {
	terminal := 0
	for _, id := range m.order {
		if st := m.jobs[id].rec.State; st != dlQueued && st != dlRunning {
			terminal++
		}
	}
	for terminal > maxDownloadHistory && len(m.order) > 0 {
		for i, id := range m.order {
			if st := m.jobs[id].rec.State; st != dlQueued && st != dlRunning {
				delete(m.jobs, id)
				m.order = append(m.order[:i], m.order[i+1:]...)
				terminal--
				break
			}
		}
	}
}

func (m *downloadManager) setProgress(j *dlJob, bytes, total int64) {
	m.mu.Lock()
	j.rec.Bytes, j.rec.Total = bytes, total
	m.emitLocked(j, "progress", map[string]any{"id": j.rec.ID, "bytes": bytes, "total": total})
	m.mu.Unlock()
}

// transferAll downloads every file to .partial and renames them all only
// when the whole job succeeded, so a multi-file asset is never half
// installed. A file with a conversion is downloaded as it is published and
// converted once everything has arrived.
func (m *downloadManager) transferAll(ctx context.Context, h *Handler, j *dlJob) (err error) {
	var temps []string
	defer func() {
		if err != nil {
			for _, p := range temps {
				_ = os.Remove(p)
			}
		}
	}()

	parts := make([]string, len(j.files))
	var doneBytes, doneTotal int64
	for i, f := range j.files {
		parts[i] = f.path + ".partial"
		download := parts[i]
		if f.convert != "" {
			download = f.path + ".src.partial"
		}
		if err := os.MkdirAll(filepath.Dir(f.path), 0o755); err != nil {
			return fmt.Errorf("cannot create destination: %w", err)
		}
		temps = append(temps, parts[i], download)
		n, total, terr := m.transfer(ctx, j, f, download, doneBytes, doneTotal)
		if terr != nil {
			return terr
		}
		doneBytes += n
		doneTotal += total
	}
	for i, f := range j.files {
		if f.convert == "" {
			continue
		}
		if err := m.convertFile(ctx, h, f, f.path+".src.partial", parts[i], &temps); err != nil {
			return fmt.Errorf("cannot convert %s: %w", f.name, err)
		}
	}
	for _, f := range j.files {
		if !j.overwrite {
			if _, err := os.Lstat(f.path); err == nil {
				return fmt.Errorf("%s already exists", f.name)
			}
		}
	}
	for i, f := range j.files {
		if err := os.Rename(parts[i], f.path); err != nil {
			return fmt.Errorf("cannot install %s: %w", f.name, err)
		}
	}
	temps = nil
	m.setProgress(j, doneBytes, doneTotal)
	if j.rec.Kind == "diffrhythm2" {
		k, _ := FindKind(j.rec.Kind)
		return m.installDiffRhythm2(ctx, h, h.KindDir(k))
	}
	return nil
}

// installDiffRhythm2 finishes the DiffRhythm2 entry: the catalog downloads only the raw checkpoint, and the
// `diffrhythm2 install` command of the service fetches the MuQ and XLM-RoBERTa models, verifies them and converts
// everything into the files the service reads (no Python). Pressing install on the entry is the acceptance of the
// licences it shows (MuQ-MuLan is CC-BY-NC), which the command asks for with --accept-licenses.
func (m *downloadManager) installDiffRhythm2(ctx context.Context, h *Handler, dir string) error {
	exe, down := h.host.DiffRhythm2CLI()
	if down != "" {
		return fmt.Errorf("the checkpoint is downloaded but cannot be completed: %s", down)
	}
	if _, err := h.host.Runner().Run(ctx, exe, []string{"install", "--model", dir, "--accept-licenses"}); err != nil {
		return fmt.Errorf("diffrhythm2 install failed (installing the entry again resumes it): %s", api.LastLines(err.Error(), 3))
	}
	return nil
}

// convertFile turns the downloaded int8 convrot checkpoint src into the GGUF out: it undoes the rotation into a BF16
// file (about twice the size of src) and has sd quantise that. Every temporary is added to temps so a failed job
// removes it, and the downloaded original is removed once the result exists.
func (m *downloadManager) convertFile(ctx context.Context, h *Handler, f dlFile, src, out string, temps *[]string) error {
	typ, ok := strings.CutPrefix(f.convert, "int8-convrot:")
	if !ok || (typ != "q8_0" && typ != "q4_K") {
		return fmt.Errorf("unknown conversion %q", f.convert)
	}
	if !convrot.IsInt8Convrot(src) {
		return errors.New("the downloaded file is not an int8 convrot checkpoint (the model may have changed on its source)")
	}
	st, err := os.Stat(src)
	if err != nil {
		return err
	}
	// While the result is written the BF16 intermediate (~1.9x the source) and the result (at most the size of the
	// source) exist next to the downloaded file.
	need := uint64(st.Size()) * 29 / 10
	if free, ferr := m.freeBytes(filepath.Dir(f.path)); ferr == nil && need > free {
		return fmt.Errorf("not enough free disk space: the conversion needs about %d GB more besides the download", need/1_000_000_000+1)
	}
	sd, err := h.host.LookPath("sd")
	if err != nil {
		return errors.New("the sd binary is not installed (bash scripts/build-sd.sh)")
	}
	bf16 := f.path + ".bf16.partial" // sd reads and writes by content, not by extension
	gguf := f.path + ".convert.partial"
	*temps = append(*temps, bf16, gguf)
	if err := convrot.Derotate(ctx, src, bf16, nil); err != nil {
		return err
	}
	if msg, err := exec.CommandContext(ctx, sd, "-M", "convert", "-m", bf16, "-o", gguf, "--type", typ).CombinedOutput(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("sd convert failed: %w: %s", err, tail(string(msg), 300))
	}
	if err := os.Rename(gguf, out); err != nil {
		return err
	}
	_ = os.Remove(bf16)
	_ = os.Remove(src)
	return nil
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

func isHFHost(host string) bool {
	host = strings.ToLower(host)
	return host == "huggingface.co" || strings.HasSuffix(host, ".huggingface.co")
}

// transfer fetches one file into part. baseBytes/baseTotal are the sizes
// of the job's previously completed files, so progress is cumulative.
func (m *downloadManager) transfer(ctx context.Context, j *dlJob, f dlFile, part string, baseBytes, baseTotal int64) (written, total int64, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.url, nil)
	if err != nil {
		return 0, 0, err
	}
	req.Header.Set("User-Agent", "veritmo-model-manager")
	// Go drops Authorization on redirects to other domains, so the token
	// only ever travels to huggingface.co itself.
	if tok := m.hfToken(); tok != "" && m.isHF(req.URL.Hostname()) {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	// Same for Civitai: the key goes in the header (never the URL) and only
	// to the initial Civitai host; Go does not forward it to the storage
	// domain Civitai redirects to.
	civitaiHost := m.isCivitai(req.URL.Hostname())
	civitaiKey := ""
	if civitaiHost {
		civitaiKey = m.civitaiToken()
		if civitaiKey != "" {
			req.Header.Set("Authorization", "Bearer "+civitaiKey)
		}
	}
	resp, err := m.client.Do(req)
	if err != nil {
		if errors.Is(err, errCivitaiLogin) {
			return 0, 0, errors.New(civitaiAccessMessage(http.StatusUnauthorized, civitaiKey != ""))
		}
		return 0, 0, redactErr(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		if (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden) && m.isHF(req.URL.Hostname()) {
			return 0, 0, errors.New(hfAccessMessage(resp.StatusCode, m.hfToken() != ""))
		}
		if (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden) && civitaiHost {
			return 0, 0, errors.New(civitaiAccessMessage(resp.StatusCode, civitaiKey != ""))
		}
		return 0, 0, fmt.Errorf("remote answered %s for %s", resp.Status, f.name)
	}
	total = resp.ContentLength // -1 when unknown
	if total > 0 {
		if free, ferr := m.freeBytes(filepath.Dir(f.path)); ferr == nil && uint64(total) > free {
			return 0, 0, fmt.Errorf("insufficient disk space for %s: need %d MB, %d MB free", f.name, total>>20, free>>20)
		}
	}

	out, err := os.OpenFile(part, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return 0, 0, fmt.Errorf("cannot write %s: %w", filepath.Base(part), err)
	}
	closed := false
	defer func() {
		if !closed {
			out.Close()
		}
	}()

	report := func(n int64) {
		t := baseTotal
		if total > 0 {
			t += total
		}
		m.setProgress(j, baseBytes+n, t)
	}
	buf := make([]byte, 256<<10)
	last := time.Now()
	report(0)
	for {
		nr, rerr := resp.Body.Read(buf)
		if nr > 0 {
			if _, werr := out.Write(buf[:nr]); werr != nil {
				return written, total, fmt.Errorf("cannot write %s: %w", f.name, werr)
			}
			written += int64(nr)
			if time.Since(last) >= progressInterval {
				last = time.Now()
				report(written)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			if ctx.Err() != nil {
				return written, total, ctx.Err()
			}
			return written, total, fmt.Errorf("transfer of %s interrupted: %w", f.name, rerr)
		}
	}
	if total > 0 && written != total {
		return written, total, fmt.Errorf("%s truncated: got %d of %d bytes", f.name, written, total)
	}
	if err := out.Close(); err != nil {
		closed = true
		return written, total, err
	}
	closed = true
	if total < 0 {
		total = written
	}
	return written, total, nil
}

// hfAccessMessage tells the user how to get past a refused Hugging Face
// request. Hugging Face answers 401 for private and non-existent repos as
// well as gated ones, so the text lists all three. Only whether a token is
// configured is known here; its value never reaches the message.
func hfAccessMessage(status int, haveToken bool) string {
	if haveToken {
		return fmt.Sprintf("Hugging Face refused access (HTTP %d) with the configured token: that token's account has no access to this repository, or the repository does not exist. "+
			"Accept the licence on the model's page at huggingface.co with that same account, or change the token in the Models tab (or in HF_TOKEN).", status)
	}
	return fmt.Sprintf("Hugging Face refused access (HTTP %d): the repository is private, gated or does not exist. "+
		"If it exists and is gated: accept its licence on the model's page at huggingface.co, create a read token (Settings → Access Tokens) and save it in the Models tab (or in the HF_TOKEN variable).", status)
}

// errCivitaiLogin is the redirect-policy error for a Civitai redirect to its
// login page.
var errCivitaiLogin = errors.New("civitai redirected to its login page")

// civitaiAPIBase is the address of Civitai's API (the lookup itself exists only in the private edition).
const civitaiAPIBase = "https://civitai.com"

// isCivitaiHost reports civitai.com and its subdomains; look-alikes such as
// evilcivitai.com or civitai.com.evil.com do not match.
func isCivitaiHost(host string) bool {
	host = strings.ToLower(host)
	return host == "civitai.com" || strings.HasSuffix(host, ".civitai.com")
}

// CivitaiTokenFile is where the Civitai key may be stored when CIVITAI_TOKEN is not
// set; empty when there is no home directory.
func CivitaiTokenFile() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "civitai-token")
}

// readCivitaiToken returns the Civitai API key: the environment variable when
// non-empty, otherwise the trimmed contents of file, otherwise "".
func readCivitaiToken(getenv func(string) string, file string) string {
	if tok := strings.TrimSpace(getenv("CIVITAI_TOKEN")); tok != "" {
		return tok
	}
	if file == "" {
		return ""
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// civitaiAccessMessage tells the user how to get past a refused Civitai
// request. Only whether a key is configured is known here; its value never
// reaches the message.
func civitaiAccessMessage(status int, haveKey bool) string {
	if haveKey {
		return fmt.Sprintf("Civitai refused access (HTTP %d) with the configured key: that key's account has no access to this file "+
			"(for example, a paid or early-access model). Check on civitai.com that the account can download it, or change the key in the Models tab.", status)
	}
	return fmt.Sprintf("Civitai requires an account key for this file (HTTP %d). "+
		"Create an API key on civitai.com (Account settings → API Keys) and save it in the Models tab (or in the CIVITAI_TOKEN variable, or in the file ~/.config/civitai-token).", status)
}

// redactErr unwraps url.Error so a failing request never echoes the full
// URL (which may carry credentials or signed query strings).
func redactErr(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("request failed: %w", ue.Err)
	}
	return err
}

// ---------------------------------------------------------------------------
// queries, cancel, shutdown
// ---------------------------------------------------------------------------

func (m *downloadManager) list() []Download {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Download, 0, len(m.order))
	for i := len(m.order) - 1; i >= 0; i-- { // newest first
		out = append(out, m.jobs[m.order[i]].rec)
	}
	return out
}

func (m *downloadManager) get(id string) (Download, *dlJob, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return Download{}, nil, false
	}
	return j.rec, j, true
}

func (m *downloadManager) cancelJob(id string) (Download, *dlError) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return Download{}, &dlError{http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("download %s not found", id)}
	}
	switch j.rec.State {
	case dlQueued:
		for i, q := range m.queue {
			if q == j {
				m.queue = append(m.queue[:i], m.queue[i+1:]...)
				break
			}
		}
		now := m.now().UTC()
		j.rec.State, j.rec.FinishedAt = dlCancelled, &now
		m.emitStateLocked(j)
		j.log.Close()
		return j.rec, nil
	case dlRunning:
		j.userCancel = true
		j.cancel() // the worker records the cancelled state and removes partials
		return j.rec, nil
	default:
		return Download{}, &dlError{http.StatusConflict, api.CodeConflict, fmt.Sprintf("download %s is already %s", id, j.rec.State)}
	}
}

// shutdown cancels the running download (partials removed by the worker)
// and drops the queue.
func (m *downloadManager) shutdown() {
	m.mu.Lock()
	m.shuttingDown = true
	now := m.now().UTC()
	for _, j := range m.queue {
		j.rec.State, j.rec.FinishedAt = dlCancelled, &now
		m.emitStateLocked(j)
		j.log.Close()
	}
	m.queue = nil
	m.mu.Unlock()
	m.rootCancel()
	m.wg.Wait()
}

// sweepPartials removes leftovers of interrupted downloads.
func (h *Handler) sweepPartials() {
	for _, k := range assetKinds {
		entries, err := os.ReadDir(h.KindDir(k))
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".partial") {
				_ = os.Remove(filepath.Join(h.KindDir(k), e.Name()))
			}
			// a kind of units downloads into folders: sweep one level down too
			if e.IsDir() && k.Units {
				sub, _ := os.ReadDir(filepath.Join(h.KindDir(k), e.Name()))
				for _, f := range sub {
					if !f.IsDir() && strings.HasSuffix(f.Name(), ".partial") {
						_ = os.Remove(filepath.Join(h.KindDir(k), e.Name(), f.Name()))
					}
				}
			}
		}
	}
}

// ---------------------------------------------------------------------------
// HTTP handlers
// ---------------------------------------------------------------------------

// POST /api/downloads
func (h *Handler) handleDownloadCreate(w http.ResponseWriter, r *http.Request) {
	var req downloadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
		return
	}
	rec, derr := h.startDownload(req)
	if derr != nil {
		api.WriteError(w, derr.status, derr.code, derr.msg)
		return
	}
	api.WriteJSON(w, http.StatusAccepted, map[string]any{"download": rec})
}

// GET /api/downloads
func (h *Handler) handleDownloadsList(w http.ResponseWriter, r *http.Request) {
	api.WriteJSON(w, http.StatusOK, map[string]any{"downloads": h.dm.list()})
}

// GET /api/downloads/{id}
func (h *Handler) handleDownloadGet(w http.ResponseWriter, r *http.Request) {
	rec, _, ok := h.dm.get(r.PathValue("id"))
	if !ok {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("download %s not found", r.PathValue("id")))
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"download": rec})
}

// POST /api/downloads/{id}/cancel
func (h *Handler) handleDownloadCancel(w http.ResponseWriter, r *http.Request) {
	rec, derr := h.dm.cancelJob(r.PathValue("id"))
	if derr != nil {
		api.WriteError(w, derr.status, derr.code, derr.msg)
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"download": rec})
}

// GET /api/downloads/{id}/events — replay of the job's events, then a live
// tail until it reaches a terminal state, closed by stream-end.
func (h *Handler) handleDownloadEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	_, j, ok := h.dm.get(id)
	if !ok {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("download %s not found", id))
		return
	}
	sw, ok := api.NewSSEWriter(w)
	if !ok {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, "response writer does not support streaming")
		return
	}
	ctx := r.Context()
	j.log.WatchCancel(ctx)
	for i := 0; ; i++ {
		line, over := j.log.Next(ctx, i)
		if line != nil {
			var env struct {
				Type string          `json:"type"`
				Data json.RawMessage `json:"data"`
			}
			if json.Unmarshal(line, &env) == nil && env.Type != "" {
				sw.Named(env.Type, env.Data)
			}
			continue
		}
		if over {
			if ctx.Err() != nil {
				return // client went away; no stream-end
			}
			break
		}
	}
	sw.Notify("stream-end", map[string]string{"id": id})
}
