package assets3d

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// The limits of an unpacked pack.
const (
	MaxZipBytes      = 200 << 20
	MaxEntries       = 5000
	MaxUnpackedBytes = 1 << 30
)

// keptExt are the files of a pack a library uses: the models, the pictures they name and the previews, and the texts (the
// licence). The .fbx and .obj copies are left out: they double the size and nothing reads them.
var keptExt = map[string]bool{".glb": true, ".png": true, ".jpg": true, ".jpeg": true, ".webp": true, ".txt": true, ".md": true}

// ErrUnsafeZip is a zip that tries something an archive must not: it is refused whole.
var ErrUnsafeZip = errors.New("unsafe zip")

// maxUnpacked is MaxUnpackedBytes; a variable so that a test can lower it.
var maxUnpacked int64 = MaxUnpackedBytes

// unzipSafe unpacks the zip at zipPath into dir (which must not exist yet) and keeps only what a library uses. An entry that is
// a link, an absolute path or leaves the folder, a pack of more than MaxEntries entries or more than MaxUnpackedBytes once
// unpacked (counted while copying, not from what the header says) refuses the whole pack, leaving nothing in dir.
func unzipSafe(zipPath, dir string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("%w: not a readable zip (%v)", ErrUnsafeZip, err)
	}
	defer zr.Close()
	if len(zr.File) > MaxEntries {
		return fmt.Errorf("%w: more than %d entries", ErrUnsafeZip, MaxEntries)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	var total int64
	ok := false
	defer func() {
		if !ok {
			_ = os.RemoveAll(dir)
		}
	}()
	for _, f := range zr.File {
		name := strings.ReplaceAll(f.Name, "\\", "/")
		clean := path.Clean(name)
		switch {
		case f.Mode()&os.ModeSymlink != 0:
			return fmt.Errorf("%w: %q is a link", ErrUnsafeZip, f.Name)
		case strings.HasPrefix(name, "/") || clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") || filepath.IsAbs(clean):
			return fmt.Errorf("%w: %q leaves the folder", ErrUnsafeZip, f.Name)
		case strings.ContainsRune(clean, ':') || strings.ContainsRune(clean, 0):
			return fmt.Errorf("%w: %q is not a plain path", ErrUnsafeZip, f.Name)
		}
		if f.FileInfo().IsDir() || !f.Mode().IsRegular() {
			continue
		}
		if !keptExt[strings.ToLower(path.Ext(clean))] {
			continue
		}
		dst := filepath.Join(dir, filepath.FromSlash(clean))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("%w: %q cannot be read (%v)", ErrUnsafeZip, f.Name, err)
		}
		out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			rc.Close()
			return fmt.Errorf("%w: %q appears twice or cannot be written (%v)", ErrUnsafeZip, f.Name, err)
		}
		n, cerr := io.Copy(out, io.LimitReader(rc, maxUnpacked-total+1))
		rc.Close()
		if e := out.Close(); cerr == nil {
			cerr = e
		}
		total += n
		if total > maxUnpacked {
			return fmt.Errorf("%w: more than %d MB once unpacked", ErrUnsafeZip, MaxUnpackedBytes>>20)
		}
		if cerr != nil {
			return fmt.Errorf("%w: %q cannot be unpacked (%v)", ErrUnsafeZip, f.Name, cerr)
		}
	}
	ok = true
	return nil
}
