package assets3d

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"veritmo/internal/video/blender"
)

// CopyResult says what a copy of a model made.
type CopyResult struct {
	// Name is the .glb in the sources; Pictures are the pictures it needs, which are there too.
	Name     string   `json:"name"`
	Pictures []string `json:"pictures"`
	// Already is true when the model was there with the same content and nothing was written.
	Already bool `json:"-"`
}

// CopyModel copies the model from ("<pack>/<name>") into the sources folder of a project or a piece: the .glb as name (or as
// <model>.glb when name is empty) and each picture it needs as <pack>--<picture>, with the addresses inside the copy rewritten to
// match, so that the models of two packs that call their atlas the same never collide. What is already there with the same content
// is reused; another file of the same name is ErrConflict and nothing is written.
func (l *Library) CopyModel(from, sources, name string) (CopyResult, error) {
	pack, mname, ok := ParseID(from)
	if !ok {
		return CopyResult{}, fmt.Errorf("%w: from must be <pack>/<model>", ErrInvalid)
	}
	_, m, err := l.find(pack, mname)
	if err != nil {
		return CopyResult{}, err
	}
	if name == "" {
		name = m.Name + blender.ModelExt
	}
	if !strings.EqualFold(filepath.Ext(name), blender.ModelExt) || !namePattern.MatchString(strings.TrimSuffix(name, filepath.Ext(name))) {
		return CopyResult{}, fmt.Errorf("%w: the name must be a plain file name ending in .glb", ErrInvalid)
	}
	files := l.FilesDir(pack)
	glbPath := filepath.Join(files, filepath.FromSlash(m.File))
	data, err := os.ReadFile(glbPath)
	if err != nil {
		return CopyResult{}, fmt.Errorf("%w: the file of the model is gone: %v", ErrNotFound, err)
	}
	model, err := blender.ParseModel(data)
	if err != nil {
		return CopyResult{}, fmt.Errorf("%w: the model is not usable: %v", ErrInvalid, err)
	}
	type picture struct {
		target string
		data   []byte
	}
	var pictures []picture
	rename := func(r blender.ImageRef) string { return pack + "--" + r.Name }
	for _, ref := range model.Refs {
		target := rename(ref)
		if !namePattern.MatchString(target) {
			return CopyResult{}, fmt.Errorf("%w: the picture %q has no plain name", ErrInvalid, ref.Name)
		}
		src, err := insideFiles(files, filepath.Join(filepath.Dir(glbPath), filepath.FromSlash(ref.Path)))
		if err != nil {
			return CopyResult{}, fmt.Errorf("%w: the picture %q of the model: %v", ErrNotFound, ref.Path, err)
		}
		b, err := os.ReadFile(src)
		if err != nil {
			return CopyResult{}, fmt.Errorf("%w: the picture %q of the model: %v", ErrNotFound, ref.Path, err)
		}
		pictures = append(pictures, picture{target, b})
	}
	out := data
	if len(model.Refs) > 0 {
		if out, err = blender.RewriteImages(data, rename); err != nil {
			return CopyResult{}, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
	}
	if err := os.MkdirAll(sources, 0o755); err != nil {
		return CopyResult{}, err
	}
	// everything that is there is checked before anything is written
	res := CopyResult{Name: name}
	glbThere := false
	if cur, err := os.ReadFile(filepath.Join(sources, name)); err == nil {
		if !bytes.Equal(cur, out) {
			return CopyResult{}, fmt.Errorf("%w: %q is already in the sources with other content: choose another name", ErrConflict, name)
		}
		glbThere = true
	}
	var toWrite []picture
	for _, p := range pictures {
		res.Pictures = append(res.Pictures, p.target)
		cur, err := os.ReadFile(filepath.Join(sources, p.target))
		switch {
		case err == nil && !bytes.Equal(cur, p.data):
			return CopyResult{}, fmt.Errorf("%w: %q is already in the sources with other content", ErrConflict, p.target)
		case err != nil:
			toWrite = append(toWrite, p)
		}
	}
	if res.Pictures == nil {
		res.Pictures = []string{}
	}
	for _, p := range toWrite {
		if err := writeNew(sources, p.target, p.data); err != nil {
			return CopyResult{}, err
		}
	}
	if glbThere {
		res.Already = len(toWrite) == 0
		return res, nil
	}
	if err := writeNew(sources, name, out); err != nil {
		return CopyResult{}, err
	}
	return res, nil
}

// insideFiles is the real path of p, which must be a regular file inside the files folder of a pack.
func insideFiles(files, p string) (string, error) {
	root, err := filepath.EvalSymlinks(files)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", err
	}
	if rel, err := filepath.Rel(root, real); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("it leaves the pack")
	}
	if fi, err := os.Stat(real); err != nil || !fi.Mode().IsRegular() {
		return "", fmt.Errorf("it is not a file")
	}
	return real, nil
}

// writeNew writes dir/name without ever replacing a file: it goes to a temporary name and is renamed.
func writeNew(dir, name string, data []byte) error {
	dst := filepath.Join(dir, name)
	if _, err := os.Lstat(dst); err == nil {
		return fmt.Errorf("%w: %q is already there", ErrConflict, name)
	}
	tmp, err := os.CreateTemp(dir, ".copy-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dst)
}
