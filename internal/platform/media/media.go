// Package media keeps whole projects in one directory, possibly on another disk (spec media-directory).
//
// With a media directory a project is the folder <media>/<id>/ holding config/ (scenes.yaml, its backups,
// sources/, .reviews/, motion/), out/ and tmp/. The project folder under the projects directory keeps its
// layout (scenes.yaml, sources, .reviews, motion, out, tmp) as symbolic links into it, so every project-relative
// path stored in manifests, runs and the API stays valid and the rest of the program does not know.
package media

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"veritmo/internal/platform/fsutil"
)

// Part is one thing of a project that lives in the media folder.
type Part struct {
	Name   string // name inside the project folder
	Target string // path inside <media>/<id>/
	File   bool   // a file (scenes.yaml); the others are folders
}

// Parts are the pieces of a project that live in the media directory; scenes.yaml.* (backups) go to config/ as
// plain files.
var Parts = []Part{
	{Name: "scenes.yaml", Target: "config/scenes.yaml", File: true},
	{Name: "sources", Target: "config/sources"},
	{Name: ".reviews", Target: "config/.reviews"},
	{Name: "motion", Target: "config/motion"},
	{Name: "out", Target: "out"},
	{Name: "tmp", Target: "tmp"},
}

const migratedSuffix = ".migrated"

var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// ValidID reports whether id can name a project folder (it is joined to a path, so no separators or dots-only).
func ValidID(id string) bool {
	return idPattern.MatchString(id) && id != "." && id != ".."
}

// Check makes sure dir exists and can be written, and returns its absolute path.
func Check(dir string) (string, error) {
	if strings.TrimSpace(dir) == "" {
		return "", errors.New("the media directory is empty")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("media directory %s: %w", dir, err)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return "", fmt.Errorf("media directory %s cannot be created: %w", abs, err)
	}
	f, err := os.CreateTemp(abs, ".write-check-*")
	if err != nil {
		return "", fmt.Errorf("media directory %s cannot be written: %w", abs, err)
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return abs, nil
}

// Folder is <media>/<id>.
func Folder(mediaDir, id string) string { return filepath.Join(mediaDir, id) }

// Available reports whether the media directory is there and can be written (a disk that is unmounted is not).
func Available(mediaDir string) error {
	fi, err := os.Stat(mediaDir)
	if err != nil || !fi.IsDir() {
		return fmt.Errorf("media directory %s is not there", mediaDir)
	}
	f, err := os.CreateTemp(mediaDir, ".write-check-*")
	if err != nil {
		return fmt.Errorf("media directory %s cannot be written: %w", mediaDir, err)
	}
	name := f.Name()
	_ = f.Close()
	return os.Remove(name)
}

// Real resolves a symbolic link at path (scenes.yaml is one) so that a writer puts its temporary and backup files
// next to the real file. A path that does not exist or is not a link is returned as it is.
func Real(path string) string {
	if r, err := filepath.EvalSymlinks(path); err == nil {
		return r
	}
	return path
}

// IsLinked reports whether the part of the project is a symbolic link into <media>/<id>.
func IsLinked(projectDir, mediaDir, id string, p Part) bool {
	link := filepath.Join(projectDir, p.Name)
	fi, err := os.Lstat(link)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		return false
	}
	dest, err := os.Readlink(link)
	return err == nil && dest == filepath.Join(Folder(mediaDir, id), filepath.FromSlash(p.Target))
}

// Linked reports whether the whole project is kept in the media directory (its scenes.yaml is a link into it).
func Linked(projectDir, mediaDir, id string) bool {
	return IsLinked(projectDir, mediaDir, id, Parts[0])
}

// Pending lists the parts of the project that are still real files or folders, not links into the media directory.
func Pending(projectDir, mediaDir, id string) []string {
	var out []string
	for _, p := range Parts {
		fi, err := os.Lstat(filepath.Join(projectDir, p.Name))
		if err != nil {
			continue
		}
		if fi.Mode()&os.ModeSymlink == 0 {
			out = append(out, p.Name)
		}
	}
	out = append(out, backups(projectDir)...)
	return out
}

// backups lists the scenes.yaml.* files directly in the project folder.
func backups(projectDir string) []string {
	m, _ := filepath.Glob(filepath.Join(projectDir, "scenes.yaml.*"))
	var out []string
	for _, f := range m {
		if fi, err := os.Lstat(f); err == nil && fi.Mode().IsRegular() {
			out = append(out, filepath.Base(f))
		}
	}
	sort.Strings(out)
	return out
}

// Result is what Adopt did (or, in a dry run, would do).
type Result struct {
	ID    string   `json:"id"`
	Moved []string `json:"moved"`
	Bytes int64    `json:"bytes"`
	Links []string `json:"links"`
}

// Needed is the number of bytes Adopt would copy for the project.
func Needed(projectDir string) int64 {
	var n int64
	for _, p := range Parts {
		n += treeSize(filepath.Join(projectDir, p.Name))
	}
	for _, b := range backups(projectDir) {
		n += treeSize(filepath.Join(projectDir, b))
	}
	return n
}

// treeSize adds up the regular files under path when path is real; a link counts for nothing.
func treeSize(path string) int64 {
	fi, err := os.Lstat(path)
	if err != nil || fi.Mode()&os.ModeSymlink != 0 {
		return 0
	}
	if fi.Mode().IsRegular() {
		return fi.Size()
	}
	var n int64
	_ = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if i, e := d.Info(); e == nil {
				n += i.Size()
			}
		}
		return nil
	})
	return n
}

// Adopt keeps the project in the media directory: it makes <media>/<id>/{config,out,tmp}, moves into it what the
// project still has as real files or folders (copy, check, and only then replace by a link), and creates the
// links that are missing. It is safe to run again. With dry set it only reports.
func Adopt(projectDir, mediaDir, id string, dry bool) (Result, error) {
	res := Result{ID: id}
	if !ValidID(id) {
		return res, fmt.Errorf("invalid project id %q", id)
	}
	folder := Folder(mediaDir, id)
	if !dry {
		for _, sub := range []string{"config", "out", "tmp"} {
			if err := os.MkdirAll(filepath.Join(folder, sub), 0o755); err != nil {
				return res, fmt.Errorf("cannot create %s: %w", filepath.Join(folder, sub), err)
			}
		}
	}
	for _, p := range Parts {
		src := filepath.Join(projectDir, p.Name)
		dst := filepath.Join(folder, filepath.FromSlash(p.Target))
		fi, err := os.Lstat(src)
		switch {
		case err == nil && fi.Mode()&os.ModeSymlink != 0:
			if IsLinked(projectDir, mediaDir, id, p) {
				cleanLeftover(src)
				continue
			}
			// A link to somewhere else (models/voices are not parts; any other is left alone).
			continue
		case err == nil:
			n := treeSize(src)
			res.Moved = append(res.Moved, p.Name)
			res.Bytes += n
			if dry {
				continue
			}
			if err := moveInto(src, dst, p.File); err != nil {
				return res, fmt.Errorf("%s of project %s: %w", p.Name, id, err)
			}
			res.Links = append(res.Links, p.Name)
		case errors.Is(err, fs.ErrNotExist):
			if p.File {
				continue // no scenes.yaml: not a project (yet)
			}
			res.Links = append(res.Links, p.Name)
			if dry {
				continue
			}
			if err := os.MkdirAll(dst, 0o755); err != nil {
				return res, err
			}
			if err := os.Symlink(dst, src); err != nil {
				return res, err
			}
		default:
			return res, err
		}
	}
	for _, b := range backups(projectDir) {
		src := filepath.Join(projectDir, b)
		res.Moved = append(res.Moved, b)
		res.Bytes += treeSize(src)
		if dry {
			continue
		}
		if err := moveInto(src, filepath.Join(folder, "config", b), true); err != nil {
			return res, fmt.Errorf("%s of project %s: %w", b, id, err)
		}
	}
	return res, nil
}

// cleanLeftover removes the <name>.migrated left by a migration that stopped after switching to the link (the
// copy had been verified before the switch).
func cleanLeftover(link string) {
	if _, err := os.Lstat(link + migratedSuffix); err == nil {
		_ = os.RemoveAll(link + migratedSuffix)
	}
}

// moveInto copies src (a file or a folder) to dst, checks that every file arrived with its size, renames src out of
// the way, puts a link at src, and then deletes the old copy. Nothing is deleted before the check passes.
func moveInto(src, dst string, isFile bool) error {
	if isFile {
		if err := fsutil.CopyFile(src, dst); err != nil {
			return err
		}
	} else {
		if err := os.MkdirAll(dst, 0o755); err != nil {
			return err
		}
		if err := copyTree(src, dst); err != nil {
			return err
		}
	}
	if err := verifyCopy(src, dst); err != nil {
		return fmt.Errorf("the copy does not match the original, nothing was removed: %w", err)
	}
	old := src + migratedSuffix
	_ = os.RemoveAll(old)
	if strings.HasPrefix(filepath.Base(src), "scenes.yaml.") {
		return os.Remove(src) // a backup: it now lives in config/ with no link
	}
	if err := os.Rename(src, old); err != nil {
		return err
	}
	if err := os.Symlink(dst, src); err != nil {
		_ = os.Rename(old, src)
		return err
	}
	return os.RemoveAll(old)
}

// copyTree copies the regular files, folders and symbolic links under src into the existing folder dst.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if rel == "." {
			return nil
		}
		to := filepath.Join(dst, rel)
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			_ = os.Remove(to)
			return os.Symlink(target, to)
		case d.IsDir():
			return os.MkdirAll(to, 0o755)
		case d.Type().IsRegular():
			return fsutil.CopyFile(p, to)
		}
		return nil // sockets, devices: nothing to keep
	})
}

// verifyCopy checks that every regular file and link under src exists under dst with the same size.
func verifyCopy(src, dst string) error {
	si, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if si.Mode().IsRegular() {
		di, err := os.Stat(dst)
		if err != nil || di.Size() != si.Size() {
			return fmt.Errorf("%s is missing or has another size", dst)
		}
		return nil
	}
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		to := filepath.Join(dst, rel)
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			if _, err := os.Lstat(to); err != nil {
				return fmt.Errorf("%s is missing", to)
			}
		case d.Type().IsRegular():
			a, e1 := d.Info()
			b, e2 := os.Stat(to)
			if e1 != nil || e2 != nil || a.Size() != b.Size() {
				return fmt.Errorf("%s is missing or has another size", to)
			}
		}
		return nil
	})
}

// RemoveProject deletes <media>/<id> (the folder of a project that is being deleted).
func RemoveProject(mediaDir, id string) error {
	if !ValidID(id) {
		return fmt.Errorf("invalid project id %q", id)
	}
	return os.RemoveAll(Folder(mediaDir, id))
}

// RemoveTree deletes path. When path is a symbolic link to a folder (tmp/ of a project in the media directory) the
// link stays and what is inside the folder goes: removing the link would leave the next run to recreate the folder
// on the local disk.
func RemoveTree(path string) error {
	fi, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		return os.RemoveAll(path)
	}
	target, err := os.Stat(path)
	if err != nil || !target.IsDir() {
		return os.Remove(path)
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(path, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// FreeBytes is the free space of the disk that holds dir.
func FreeBytes(dir string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil
}

// Projects lists the ids of the folders of projectsDir that hold a scenes.yaml (a file or a link).
func Projects(projectsDir string) []string {
	entries, _ := os.ReadDir(projectsDir)
	var ids []string
	for _, e := range entries {
		if !e.IsDir() || !ValidID(e.Name()) {
			continue
		}
		if _, err := os.Lstat(filepath.Join(projectsDir, e.Name(), "scenes.yaml")); err == nil {
			ids = append(ids, e.Name())
		}
	}
	return ids
}

// Report is the outcome of Migrate.
type Report struct {
	Projects []Result `json:"projects"`
	Skipped  []string `json:"skipped,omitempty"`
	Needed   int64    `json:"needed_bytes"`
	Free     int64    `json:"free_bytes"`
}

// Migrate moves every project of projectsDir into the media directory. busy says a project has a run (it is
// skipped). Nothing is moved when the media disk lacks the space; dry only reports.
func Migrate(projectsDir, mediaDir string, dry bool, busy func(id string) bool) (Report, error) {
	var rep Report
	abs, err := Check(mediaDir)
	if err != nil {
		return rep, err
	}
	free, err := FreeBytes(abs)
	if err != nil {
		return rep, err
	}
	rep.Free = free
	var todo []string
	for _, id := range Projects(projectsDir) {
		if busy != nil && busy(id) {
			rep.Skipped = append(rep.Skipped, id)
			continue
		}
		dir := filepath.Join(projectsDir, id)
		if len(Pending(dir, abs, id)) == 0 && Linked(dir, abs, id) && allLinked(dir, abs, id) {
			continue
		}
		rep.Needed += Needed(dir)
		todo = append(todo, id)
	}
	if rep.Needed > rep.Free {
		return rep, fmt.Errorf("the projects need %d bytes and the media disk has %d free: nothing was moved", rep.Needed, rep.Free)
	}
	for _, id := range todo {
		r, err := Adopt(filepath.Join(projectsDir, id), abs, id, dry)
		rep.Projects = append(rep.Projects, r)
		if err != nil {
			return rep, err
		}
	}
	return rep, nil
}

// allLinked is true when every folder part of the project is a link into the media directory.
func allLinked(projectDir, mediaDir, id string) bool {
	for _, p := range Parts {
		if !IsLinked(projectDir, mediaDir, id, p) {
			if _, err := os.Lstat(filepath.Join(projectDir, p.Name)); err != nil && p.File {
				continue
			}
			return false
		}
	}
	return true
}
