package media

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
	"veritmo/internal/platform/fsutil"
	"veritmo/internal/platform/legacyname"
)

// ModelsSettingFile is the user file that names the models directory when neither the flag nor MODELS_DIR does.
const ModelsSettingFile = "models-dir"

// ResolveModelsDir gives the models directory: the flag, else MODELS_DIR, else the first line of
// <config dir>/veritmo/models-dir. "" means none (models/ stays where it is); MODELS_DIR=off ignores the file.
func ResolveModelsDir(flagValue string, getenv func(string) string, configDir string) string {
	if v := strings.TrimSpace(flagValue); v != "" {
		return v
	}
	if v, ok := lookup(getenv, "MODELS_DIR"); ok {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "off", "none", "0":
			return ""
		}
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	if configDir == "" {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(legacyname.Dir(configDir), ModelsSettingFile))
	if err != nil {
		return ""
	}
	line, _, _ := strings.Cut(string(b), "\n")
	return strings.TrimSpace(line)
}

func lookup(getenv func(string) string, key string) (string, bool) {
	v := getenv(key)
	return v, v != ""
}

// UserConfigDir is $XDG_CONFIG_HOME or ~/.config.
func UserConfigDir(getenv func(string) string) string {
	if v := getenv("XDG_CONFIG_HOME"); v != "" {
		return v
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".config")
	}
	return ""
}

// ModelsState says how the models directory stands against the repository's models/.
type ModelsState struct {
	Dir       string `json:"dir"`
	Available bool   `json:"available"`
	// Linked is true when root/models is a link that leads to Dir.
	Linked bool `json:"linked"`
	// Reason says why it is not available, or what is pending.
	Reason string `json:"reason,omitempty"`
}

// CheckModels looks at the configured models directory without creating anything.
func CheckModels(root, dir string) ModelsState {
	st := ModelsState{Dir: dir}
	if err := Available(dir); err != nil {
		st.Reason = err.Error()
		return st
	}
	st.Available = true
	link := filepath.Join(root, "models")
	fi, err := os.Lstat(link)
	switch {
	case err != nil:
		st.Reason = "models/ does not exist: run `veritmo models migrate`"
	case fi.Mode()&os.ModeSymlink == 0:
		st.Reason = "models/ is still a real folder: run `veritmo models migrate`"
	default:
		a, e1 := filepath.EvalSymlinks(link)
		b, e2 := filepath.EvalSymlinks(dir)
		if e1 == nil && e2 == nil && a == b {
			st.Linked = true
		} else {
			st.Reason = "models/ leads somewhere other than the models directory: run `veritmo models migrate`"
		}
	}
	return st
}

// ModelsReport is the outcome of MigrateModels.
type ModelsReport struct {
	Files   int   `json:"files"`
	Copied  int   `json:"copied"`
	Skipped int   `json:"skipped"`
	Needed  int64 `json:"needed_bytes"`
	Free    int64 `json:"free_bytes"`
	Linked  bool  `json:"linked"`
	// Done is true when models/ already was the link to dir and there was nothing to do.
	Done bool `json:"done"`
}

// MigrateModels moves root/models to dir: copy (skipping what is there with the same size and time, so a cut run
// resumes), check names and sizes, rename the original to models.migrated, link models to dir, and only then delete
// the old copy. With dry set it only reports. progress, when not nil, gets one line per big file.
func MigrateModels(root, dir string, dry bool, progress io.Writer) (ModelsReport, error) {
	var rep ModelsReport
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return rep, err
	}
	if !dry {
		if absDir, err = Check(absDir); err != nil {
			return rep, err
		}
	} else if err := Available(absDir); err != nil {
		// A dry run may precede the creation of the directory: it only needs its parent to report the space.
		if _, e := os.Stat(filepath.Dir(absDir)); e != nil {
			return rep, err
		}
	}
	src := filepath.Join(root, "models")
	fi, err := os.Lstat(src)
	if err == nil && fi.Mode()&os.ModeSymlink != 0 {
		a, e1 := filepath.EvalSymlinks(src)
		b, e2 := filepath.EvalSymlinks(absDir)
		if e1 == nil && e2 == nil && a == b {
			rep.Done, rep.Linked = true, true
			return rep, nil
		}
		return rep, fmt.Errorf("models is a link that leads elsewhere (%s): not touching it", a)
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return rep, err
	}
	free, err := FreeBytes(nearestExisting(absDir))
	if err != nil {
		return rep, err
	}
	rep.Free = free
	files, err := listFiles(src)
	if err != nil {
		return rep, err
	}
	rep.Files = len(files)
	for _, f := range files {
		if sameFile(f, filepath.Join(absDir, f.rel)) {
			rep.Skipped++
		} else {
			rep.Copied++
			rep.Needed += f.size
		}
	}
	if rep.Needed > rep.Free {
		return rep, fmt.Errorf("the models need %d bytes more and the models directory has %d free: nothing was copied", rep.Needed, rep.Free)
	}
	if dry {
		return rep, nil // Copied and Skipped say what a real run would do
	}
	rep.Copied, rep.Skipped = 0, 0
	start := time.Now()
	for _, f := range files {
		dst := filepath.Join(absDir, f.rel)
		if sameFile(f, dst) {
			rep.Skipped++
			continue
		}
		if err := copyPartial(f.abs, dst); err != nil {
			return rep, fmt.Errorf("%s: %w", f.rel, err)
		}
		rep.Copied++
		if progress != nil && f.size >= 100<<20 {
			fmt.Fprintf(progress, "copied %s (%d MB, %d/%d files, %s)\n", f.rel, f.size>>20, rep.Copied+rep.Skipped, rep.Files, time.Since(start).Round(time.Second))
		}
	}
	if fi == nil {
		return rep, os.Symlink(absDir, src)
	}
	if err := mkdirs(src, absDir); err != nil {
		return rep, err
	}
	if err := verifyCopy(src, absDir); err != nil {
		return rep, fmt.Errorf("the copy does not match the original, nothing was removed: %w", err)
	}
	old := src + migratedSuffix
	if _, err := os.Lstat(old); err == nil {
		return rep, fmt.Errorf("%s is already there from an earlier run: check it and remove it, nothing was changed", old)
	}
	if err := os.Rename(src, old); err != nil {
		return rep, err
	}
	if err := os.Symlink(absDir, src); err != nil {
		_ = os.Rename(old, src)
		return rep, err
	}
	rep.Linked = true
	return rep, os.RemoveAll(old)
}

// mkdirs makes in dst every folder src has, the empty ones too.
func mkdirs(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Type()&fs.ModeSymlink == 0 {
			rel, _ := filepath.Rel(src, p)
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		return nil
	})
}

type modelFile struct {
	rel, abs string
	size     int64
	mtime    time.Time
	link     string // non-empty for a symbolic link
}

// listFiles lists the regular files and the symbolic links under dir (nothing is followed). A missing dir is empty.
func listFiles(dir string) ([]modelFile, error) {
	var out []modelFile
	if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			t, err := os.Readlink(p)
			if err != nil {
				return err
			}
			out = append(out, modelFile{rel: rel, abs: p, link: t})
		case d.Type().IsRegular():
			i, err := d.Info()
			if err != nil {
				return err
			}
			out = append(out, modelFile{rel: rel, abs: p, size: i.Size(), mtime: i.ModTime()})
		}
		return nil
	})
	return out, err
}

// sameFile is true when dst already holds f: a link to the same place, or a file of the same size and time.
func sameFile(f modelFile, dst string) bool {
	if f.link != "" {
		t, err := os.Readlink(dst)
		return err == nil && t == f.link
	}
	i, err := os.Lstat(dst)
	if err != nil || !i.Mode().IsRegular() || i.Size() != f.size {
		return false
	}
	d := i.ModTime().Sub(f.mtime)
	return d > -2*time.Second && d < 2*time.Second
}

// copyPartial copies src to dst through a .partial name so a cut copy never leaves a short file under the real name.
func copyPartial(src, dst string) error {
	i, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if i.Mode()&os.ModeSymlink != 0 {
		t, err := os.Readlink(src)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		_ = os.Remove(dst)
		return os.Symlink(t, dst)
	}
	part := dst + ".partial"
	if err := fsutil.CopyFile(src, part); err != nil {
		_ = os.Remove(part)
		return err
	}
	return os.Rename(part, dst)
}

// nearestExisting is path, or its closest parent that exists (the disk a new directory will be on).
func nearestExisting(p string) string {
	for {
		if _, err := os.Stat(p); err == nil {
			return p
		}
		parent := filepath.Dir(p)
		if parent == p {
			return p
		}
		p = parent
	}
}
