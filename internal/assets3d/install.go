package assets3d

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

// ErrNoModels is a pack that holds no usable .glb.
var ErrNoModels = errors.New("the pack has no .glb model")

type job struct {
	running     bool
	err         string
	done, total atomic.Int64
	cancel      context.CancelFunc
}

// Install queues the download of a pack of the list. It returns ErrNotFound for a pack that is not in the list and ErrConflict
// for one that is installed or being installed.
func (l *Library) Install(id string) error {
	d, ok := l.def(id)
	if !ok {
		return fmt.Errorf("%w: %q is not a pack of the list", ErrNotFound, id)
	}
	if _, ok := l.record(id); ok {
		return fmt.Errorf("%w: %q is already installed", ErrConflict, id)
	}
	l.mu.Lock()
	if j := l.jobs[id]; j != nil && j.running {
		l.mu.Unlock()
		return fmt.Errorf("%w: %q is being installed", ErrConflict, id)
	}
	ctx, cancel := context.WithCancel(context.Background())
	j := &job{running: true, cancel: cancel}
	l.jobs[id] = j
	l.mu.Unlock()
	go l.runInstall(ctx, d, j)
	return nil
}

// Cancel stops an install; the pack goes back to available.
func (l *Library) Cancel(id string) error {
	l.mu.Lock()
	j := l.jobs[id]
	l.mu.Unlock()
	if j == nil || !j.running {
		return fmt.Errorf("%w: %q is not being installed", ErrConflict, id)
	}
	j.cancel()
	return nil
}

// Wait blocks until the install of a pack is over (for the tests and the commands).
func (l *Library) Wait(id string) {
	for {
		l.mu.Lock()
		j := l.jobs[id]
		running := j != nil && j.running
		l.mu.Unlock()
		if !running {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (l *Library) finish(id string, j *job, err error, cancelled bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	switch {
	case cancelled:
		delete(l.jobs, id)
	case err != nil:
		j.running, j.err = false, err.Error()
	default:
		delete(l.jobs, id)
	}
}

func (l *Library) runInstall(ctx context.Context, d PackDef, j *job) {
	select {
	case l.sem <- struct{}{}:
		defer func() { <-l.sem }()
	case <-ctx.Done():
		l.finish(d.ID, j, nil, true)
		return
	}
	tmp := l.packDir(d.ID) + ".partial"
	_ = os.RemoveAll(tmp)
	err := l.install(ctx, d, j, tmp)
	if err != nil {
		_ = os.RemoveAll(tmp)
	}
	l.finish(d.ID, j, err, ctx.Err() != nil)
}

func (l *Library) install(ctx context.Context, d PackDef, j *job, tmp string) error {
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return err
	}
	zipPath := filepath.Join(tmp, "pack.zip")
	f, err := os.Create(zipPath)
	if err != nil {
		return err
	}
	h := sha256.New()
	err = l.fetchZip(ctx, d, io.MultiWriter(f, h), j)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != d.SHA256 {
		return fmt.Errorf("the pack changed: its zip is not the version that was checked (expected %s…, got %s…): the list has to be updated by someone who reads the new version", d.SHA256[:12], got[:12])
	}
	files := filepath.Join(tmp, "files")
	if err := unzipSafe(zipPath, files); err != nil {
		return err
	}
	_ = os.Remove(zipPath)
	if err := checkLicence(files, d.LicenceWords); err != nil {
		return err
	}
	models, skipped := index(files, d.Tags)
	if len(models) == 0 {
		return ErrNoModels
	}
	rec := &Record{ID: d.ID, Name: d.Name, Author: d.Author, Licence: d.Licence, Source: "catalog", Page: d.Page, Version: d.Version, SHA256: d.SHA256,
		SizeBytes: d.SizeBytes, Description: d.Description, Tags: d.Tags, InstalledAt: time.Now().UTC(), Models: models, Skipped: skipped}
	return l.commit(tmp, rec)
}

// commit writes the record and moves the folder into place: a pack with a record is a complete one.
func (l *Library) commit(tmp string, rec *Record) error {
	if err := writeRecord(tmp, rec); err != nil {
		return err
	}
	if err := os.Rename(tmp, l.packDir(rec.ID)); err != nil {
		return err
	}
	return nil
}

// checkLicence asks that the licence text of the pack, a .txt or .md file named like a licence, has all the words.
func checkLicence(files string, words []string) error {
	var text strings.Builder
	_ = filepath.WalkDir(files, func(p string, e os.DirEntry, err error) error {
		if err != nil || !e.Type().IsRegular() {
			return nil
		}
		if n := strings.ToLower(e.Name()); strings.Contains(n, "licen") || strings.Contains(n, "copying") {
			b, _ := os.ReadFile(p)
			if len(b) > 1<<20 {
				b = b[:1<<20]
			}
			text.Write(b)
			text.WriteByte('\n')
		}
		return nil
	})
	lower := strings.ToLower(text.String())
	if lower == "" {
		return fmt.Errorf("the pack has no licence text: it is not the version that was checked")
	}
	for _, w := range words {
		if !strings.Contains(lower, strings.ToLower(w)) {
			return fmt.Errorf("the licence text of the pack does not say %q: it is not the version that was checked", w)
		}
	}
	return nil
}

// ImportMeta is what a person tells about a zip they bring.
type ImportMeta struct {
	ID, Name, Author, Licence, SourceURL string
}

// Import installs a pack from a zip the person has. The licence they declare is kept and shown; the server does not judge it.
func (l *Library) Import(ctx context.Context, m ImportMeta, zipBody io.Reader) (PackView, error) {
	switch {
	case !idPattern.MatchString(m.ID):
		return PackView{}, fmt.Errorf("%w: the id must be lower case letters, digits and dashes", ErrInvalid)
	case strings.TrimSpace(m.Licence) == "":
		return PackView{}, fmt.Errorf("%w: licence is required: say what the pack allows", ErrInvalid)
	}
	if m.ID == GeneratedPackID {
		return PackView{}, fmt.Errorf("%w: %q is the pack of the generated models", ErrConflict, m.ID)
	}
	if _, ok := l.def(m.ID); ok {
		return PackView{}, fmt.Errorf("%w: %q is a pack of the list", ErrConflict, m.ID)
	}
	if _, ok := l.record(m.ID); ok {
		return PackView{}, fmt.Errorf("%w: %q is already installed", ErrConflict, m.ID)
	}
	l.mu.Lock()
	if j := l.jobs[m.ID]; j != nil && j.running {
		l.mu.Unlock()
		return PackView{}, fmt.Errorf("%w: %q is being installed", ErrConflict, m.ID)
	}
	l.mu.Unlock()
	tmp := l.packDir(m.ID) + ".partial"
	_ = os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return PackView{}, err
	}
	done := false
	defer func() {
		if !done {
			_ = os.RemoveAll(tmp)
		}
	}()
	zipPath := filepath.Join(tmp, "pack.zip")
	f, err := os.Create(zipPath)
	if err != nil {
		return PackView{}, err
	}
	n, err := io.Copy(f, io.LimitReader(zipBody, MaxZipBytes+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	switch {
	case err != nil:
		return PackView{}, fmt.Errorf("%w: cannot read the upload: %v", ErrInvalid, err)
	case n == 0:
		return PackView{}, fmt.Errorf("%w: the file is empty", ErrInvalid)
	case n > MaxZipBytes:
		return PackView{}, fmt.Errorf("%w: the zip is larger than %d MB", ErrInvalid, MaxZipBytes>>20)
	}
	files := filepath.Join(tmp, "files")
	if err := unzipSafe(zipPath, files); err != nil {
		return PackView{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	_ = os.Remove(zipPath)
	models, skipped := index(files, nil)
	if len(models) == 0 {
		return PackView{}, ErrNoModels
	}
	name := strings.TrimSpace(m.Name)
	if name == "" {
		name = m.ID
	}
	author := strings.TrimSpace(m.Author)
	if author == "" {
		author = "unknown"
	}
	lic := strings.TrimSpace(m.Licence)
	low := strings.ToLower(lic)
	rec := &Record{ID: m.ID, Name: name, Author: author, Licence: lic, Attribution: !(strings.Contains(low, "cc0") || strings.Contains(low, "creative commons zero")),
		Source: "user", Page: strings.TrimSpace(m.SourceURL), SizeBytes: n, InstalledAt: time.Now().UTC(), Models: models, Skipped: skipped}
	if err := l.commit(tmp, rec); err != nil {
		return PackView{}, err
	}
	done = true
	return l.viewOf(m.ID), nil
}

// Remove deletes an installed or imported pack and its files; a pack of the list goes back to available.
func (l *Library) Remove(id string) error {
	l.mu.Lock()
	if j := l.jobs[id]; j != nil && j.running {
		l.mu.Unlock()
		return fmt.Errorf("%w: %q is being installed: cancel it first", ErrConflict, id)
	}
	delete(l.jobs, id)
	l.mu.Unlock()
	if _, ok := l.record(id); !ok {
		return fmt.Errorf("%w: %q is not installed", ErrNotFound, id)
	}
	return os.RemoveAll(l.packDir(id))
}
