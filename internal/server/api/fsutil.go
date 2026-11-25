package api

import (
	"fmt"
	"os"

	"veritmo/internal/platform/media"
)

// AtomicWrite writes data to path via a temp file + rename so a reader
// never observes a partially written file.
func AtomicWrite(path string, data []byte) error {
	path = media.Real(path) // a link into the media directory: write next to the real file, keep the link
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("cannot write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("cannot rename %s: %w", tmp, err)
	}
	return nil
}

// DirExists says whether path is a directory.
func DirExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}
