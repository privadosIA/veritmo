package cache

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"veritmo/internal/platform/fsutil"
)

// Store keeps the artifacts that a phase made under the hash of its inputs, apart from the scene that made them, so
// another scene (or the same one renamed, or the project made again) with the same inputs reuses the file instead of
// running the phase. It is a directory of `<hash>.<ext>` files (the narration `.wav`, the picture `.png`).
//
// Files go in and out by copy, never by link: the programs that write an artifact (piper, sd) rewrite the file where it
// is, and a hard link would let a later run damage the stored copy.
type Store struct {
	// Dir is the folder of the store (it is created when the first artifact is put).
	Dir string
	// MaxBytes is the most the store may hold; when a Put goes over it the oldest files are removed. 0 means no limit.
	MaxBytes int64
}

// DefaultStoreMaxBytes is the limit of a store when nothing else says (2 GiB).
const DefaultStoreMaxBytes int64 = 2 << 30

// StoreMaxBytes reads the limit from the environment variable ORCHESTRATOR_STORE_MB: a number of megabytes, 0 for
// no store at all (ok is false then). Unset or unreadable gives the default.
func StoreMaxBytes(env string) (limit int64, ok bool) {
	if strings.TrimSpace(env) == "" {
		return DefaultStoreMaxBytes, true
	}
	mb, err := strconv.ParseInt(strings.TrimSpace(env), 10, 64)
	if err != nil || mb < 0 {
		return DefaultStoreMaxBytes, true
	}
	if mb == 0 {
		return 0, false
	}
	return mb << 20, true
}

// path is where the artifact of a hash lives.
func (s *Store) path(hash, ext string) string { return filepath.Join(s.Dir, hash+ext) }

// Has reports whether the store holds a usable file for the hash: present, a regular file and not empty.
func (s *Store) Has(hash, ext string) bool {
	if s == nil || hash == "" {
		return false
	}
	fi, err := os.Stat(s.path(hash, ext))
	return err == nil && fi.Mode().IsRegular() && fi.Size() > 0
}

// Get copies the stored artifact of the hash to dst, replacing what is there, and reports whether it did. A missing or
// empty stored file is not a hit (and is removed, so the phase that runs replaces it).
func (s *Store) Get(hash, ext, dst string) bool {
	if s == nil || hash == "" {
		return false
	}
	if !s.Has(hash, ext) {
		_ = os.Remove(s.path(hash, ext)) // an empty or odd file must not stay as an entry
		return false
	}
	if err := fsutil.CopyFile(s.path(hash, ext), dst); err != nil {
		return false
	}
	// A touched file is a used file: the oldest ones are the first to go.
	now := time.Now()
	_ = os.Chtimes(s.path(hash, ext), now, now)
	return true
}

// Put copies the artifact at src into the store under the hash, replacing an older one, and then removes the oldest
// files while the store is over its limit. An empty or missing src is not stored. Errors are returned for the caller to
// report as a warning: the store is an optimisation and never stops a run.
func (s *Store) Put(hash, ext, src string) error {
	if s == nil || hash == "" {
		return nil
	}
	fi, err := os.Stat(src)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() == 0 {
		return nil
	}
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return fmt.Errorf("store: %w", err)
	}
	if err := fsutil.CopyFile(src, s.path(hash, ext)); err != nil {
		return fmt.Errorf("store: %w", err)
	}
	s.prune(s.path(hash, ext))
	return nil
}

// prune removes the oldest files while the store holds more than MaxBytes; keep is never removed (it was just put).
func (s *Store) prune(keep string) {
	if s.MaxBytes <= 0 {
		return
	}
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return
	}
	type item struct {
		path string
		size int64
		mod  time.Time
	}
	var items []item
	var total int64
	for _, e := range entries {
		fi, err := e.Info()
		if err != nil || !fi.Mode().IsRegular() || strings.HasSuffix(e.Name(), ".tmp") {
			continue
		}
		items = append(items, item{filepath.Join(s.Dir, e.Name()), fi.Size(), fi.ModTime()})
		total += fi.Size()
	}
	sort.Slice(items, func(i, j int) bool { return items[i].mod.Before(items[j].mod) })
	for _, it := range items {
		if total <= s.MaxBytes {
			return
		}
		if it.path == keep {
			continue
		}
		if os.Remove(it.path) == nil {
			total -= it.size
		}
	}
}
