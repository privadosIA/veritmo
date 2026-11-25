package blender

// model.go: a 3D model of the sources (contract v1, object type "model"). A model is a binary glTF (.glb): one file that
// holds the meshes, the rig, the clips and the textures. It is read here as DATA before Blender starts (the JSON chunk,
// never the binary one), so a description that names a file or a clip it does not have is refused by its place in the
// JSON, the writer can be told which clips a file has, and the sources listing can show them. Nothing in the file is
// evaluated, and a file that points to other files by address is refused: Blender would read a path of the file's choosing.

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// The limits of a model file.
const (
	MaxModelBytes     = 32 << 20
	MaxModelTriangles = 500000
	MaxModels         = 16
)

// ModelExt is the only extension of a model: binary glTF is self-contained, which .gltf, .fbx and .obj are not.
const ModelExt = ".glb"

const (
	glbMagic     = 0x46546C67 // "glTF"
	glbJSONChunk = 0x4E4F534A // "JSON"
)

// Model is what a .glb says about itself.
type Model struct {
	// Clips are the names of its animations, sorted and without repeats (an animation with no name cannot be asked for).
	Clips []string `json:"clips"`
	// Triangles is the number of triangles of its meshes, each mesh counted once however many nodes use it.
	Triangles int `json:"triangles"`
	// Bytes is the size of the file.
	Bytes int64 `json:"bytes"`
	// Needs are the names of the pictures of the sources the file refers to (the last name of each external image, unique and
	// sorted): Kenney's characters all name Textures/colormap.png, so they need colormap.png.
	Needs []string `json:"needs"`
	// Refs are the external images as the file names them, decoded: the relative path each one has to have next to the
	// file when it is imported.
	Refs []ImageRef `json:"-"`
}

// ImageRef is one external image of a model: Path as the file writes it (Textures/colormap.png) and Name its last name.
type ImageRef struct {
	Path string
	Name string
}

// HasClip reports whether the file has a clip of that name.
func (m *Model) HasClip(name string) bool {
	for _, c := range m.Clips {
		if c == name {
			return true
		}
	}
	return false
}

// ReadModel reads a .glb file. The error says why it is not a valid binary glTF.
func ReadModel(path string) (*Model, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxModelBytes+1))
	if err != nil {
		return nil, err
	}
	return ParseModel(data)
}

// glTF is the few fields of the JSON chunk that are read.
type glTF struct {
	Animations []struct {
		Name string `json:"name"`
	} `json:"animations"`
	Accessors []struct {
		Count int `json:"count"`
	} `json:"accessors"`
	Meshes []struct {
		Primitives []struct {
			Attributes map[string]int `json:"attributes"`
			Indices    *int           `json:"indices"`
			Mode       *int           `json:"mode"`
		} `json:"primitives"`
	} `json:"meshes"`
	Buffers []struct {
		URI string `json:"uri"`
	} `json:"buffers"`
	Images []struct {
		URI string `json:"uri"`
	} `json:"images"`
}

// imageRef reads the address of an external image: a relative path of plain names, so that nothing outside the folder it
// is placed in can be reached. An address with a scheme, a query, an absolute path, a backslash or a .. is refused.
func imageRef(uri string) (ImageRef, error) {
	bad := fmt.Errorf("the file refers to another file (%q): a picture of a model must be a relative path of plain names", uri)
	dec, err := url.PathUnescape(uri)
	if err != nil || dec == "" || strings.ContainsAny(dec, "\\:?#\x00") || strings.HasPrefix(dec, "/") {
		return ImageRef{}, bad
	}
	for _, seg := range strings.Split(dec, "/") {
		if seg == "" || seg == ".." || seg == "." {
			return ImageRef{}, bad
		}
	}
	return ImageRef{Path: path.Clean(dec), Name: path.Base(dec)}, nil
}

// ParseModel reads the bytes of a .glb.
func ParseModel(data []byte) (*Model, error) {
	if len(data) > MaxModelBytes {
		return nil, fmt.Errorf("the file is larger than %d MiB", MaxModelBytes>>20)
	}
	if len(data) < 20 {
		return nil, errors.New("not a valid binary glTF (.glb): the file is too short")
	}
	if binary.LittleEndian.Uint32(data[0:4]) != glbMagic {
		return nil, errors.New("not a valid binary glTF (.glb): it does not start with the glTF header")
	}
	if v := binary.LittleEndian.Uint32(data[4:8]); v != 2 {
		return nil, fmt.Errorf("not a valid binary glTF (.glb): version %d, only 2 is read", v)
	}
	if total := binary.LittleEndian.Uint32(data[8:12]); int64(total) > int64(len(data)) {
		return nil, errors.New("not a valid binary glTF (.glb): the file is cut short")
	}
	size := binary.LittleEndian.Uint32(data[12:16])
	if binary.LittleEndian.Uint32(data[16:20]) != glbJSONChunk {
		return nil, errors.New("not a valid binary glTF (.glb): the first chunk is not JSON")
	}
	if int64(size) > int64(len(data)-20) {
		return nil, errors.New("not a valid binary glTF (.glb): the JSON chunk is cut short")
	}
	var g glTF
	if err := json.NewDecoder(bytes.NewReader(data[20 : 20+int(size)])).Decode(&g); err != nil {
		return nil, fmt.Errorf("not a valid binary glTF (.glb): the JSON chunk cannot be read (%v)", err)
	}
	for _, b := range g.Buffers {
		if b.URI != "" && !strings.HasPrefix(b.URI, "data:") {
			return nil, fmt.Errorf("the file keeps its data in another file (%q): a model keeps its meshes and clips inside", b.URI)
		}
	}
	m := &Model{Bytes: int64(len(data))}
	seenImage, seenName := map[string]bool{}, map[string]bool{}
	for _, im := range g.Images {
		if im.URI == "" || strings.HasPrefix(im.URI, "data:") {
			continue
		}
		ref, err := imageRef(im.URI)
		if err != nil {
			return nil, err
		}
		if !seenImage[ref.Path] {
			seenImage[ref.Path] = true
			m.Refs = append(m.Refs, ref)
		}
		if !seenName[ref.Name] {
			seenName[ref.Name] = true
			m.Needs = append(m.Needs, ref.Name)
		}
	}
	sort.Strings(m.Needs)
	seen := map[string]bool{}
	for _, a := range g.Animations {
		if a.Name != "" && !seen[a.Name] {
			seen[a.Name] = true
			m.Clips = append(m.Clips, a.Name)
		}
	}
	sort.Strings(m.Clips)
	count := func(i int) int {
		if i < 0 || i >= len(g.Accessors) {
			return 0
		}
		return g.Accessors[i].Count
	}
	for _, me := range g.Meshes {
		for _, p := range me.Primitives {
			mode := 4
			if p.Mode != nil {
				mode = *p.Mode
			}
			n := 0
			switch {
			case p.Indices != nil:
				n = count(*p.Indices)
			default:
				if pos, ok := p.Attributes["POSITION"]; ok {
					n = count(pos)
				}
			}
			switch mode {
			case 4:
				m.Triangles += n / 3
			case 5, 6:
				if n > 2 {
					m.Triangles += n - 2
				}
			}
		}
	}
	return m, nil
}

// ModelsDir is the folder, inside the frames', where the models that need pictures are staged.
const ModelsDir = "_models"

// StageModels prepares the model files of a description for the import. A model that needs no picture is imported from the
// sources as it is. One that does is copied to <dst>/<n>/ together with the pictures it names, each at the relative path the
// file gives it (Textures/colormap.png), so that Blender finds them where the file expects them and nothing outside the
// sources is ever read. It returns, by file name, the path to import from.
func StageModels(spec *Spec, sources, dst string) (map[string]string, error) {
	out := map[string]string{}
	for i, name := range spec.Models() {
		file, err := resolveInSources(sources, name, "the model")
		if err != nil {
			return nil, err
		}
		m, err := ReadModel(file)
		if err != nil {
			return nil, fmt.Errorf("the model %q: %w", name, err)
		}
		if len(m.Refs) == 0 {
			out[name] = file
			continue
		}
		dir := filepath.Join(dst, fmt.Sprint(i))
		if err := copyFile(file, filepath.Join(dir, name)); err != nil {
			return nil, err
		}
		for _, ref := range m.Refs {
			pic, err := resolveInSources(sources, ref.Name, "the picture")
			if err != nil {
				return nil, fmt.Errorf("the model %q needs the picture %s, which is not in the sources", name, ref.Name)
			}
			if err := copyFile(pic, filepath.Join(dir, filepath.FromSlash(ref.Path))); err != nil {
				return nil, err
			}
		}
		out[name] = filepath.Join(dir, name)
	}
	return out, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// ProjectModel is a model of a project's sources that a description can use.
type ProjectModel struct {
	Name string
	Model
}

// maxListedModels bounds how many models the writer is told about.
const maxListedModels = 24

// ProjectModels lists the .glb files of the sources that a description could use now (valid, small enough, with every
// picture they need present), sorted by name. A scene with no project has none.
func ProjectModels(env Env) []ProjectModel {
	if env.NoFiles || env.Sources == "" {
		return nil
	}
	entries, err := os.ReadDir(env.Sources)
	if err != nil {
		return nil
	}
	var out []ProjectModel
	for _, e := range entries {
		name := e.Name()
		if !e.Type().IsRegular() || strings.ToLower(filepath.Ext(name)) != ModelExt || strings.HasPrefix(name, ".") {
			continue
		}
		m, err := readSourceModel(env.Sources, name)
		if err != nil || m.Triangles > MaxModelTriangles {
			continue
		}
		usable := true
		for _, need := range m.Needs {
			if needPicture(env.Sources, name, need) != nil {
				usable = false
			}
		}
		if usable {
			out = append(out, ProjectModel{Name: name, Model: *m})
		}
		if len(out) == maxListedModels {
			break
		}
	}
	return out
}

// RewriteImages gives the external pictures of a .glb other addresses: rename is called with each one (see Model.Refs) and
// returns the plain name it must have in the copy. Only the images' addresses in the JSON chunk change: the binary chunk and
// every other field are kept as they were, and the file is made again with the lengths of its header and chunks right.
func RewriteImages(data []byte, rename func(ImageRef) string) ([]byte, error) {
	if _, err := ParseModel(data); err != nil {
		return nil, err
	}
	size := int(binary.LittleEndian.Uint32(data[12:16]))
	dec := json.NewDecoder(bytes.NewReader(data[20 : 20+size]))
	dec.UseNumber()
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("the JSON chunk cannot be read (%v)", err)
	}
	if images, ok := doc["images"].([]any); ok {
		for _, im := range images {
			m, ok := im.(map[string]any)
			if !ok {
				continue
			}
			uri, _ := m["uri"].(string)
			if uri == "" || strings.HasPrefix(uri, "data:") {
				continue
			}
			ref, err := imageRef(uri)
			if err != nil {
				return nil, err
			}
			m["uri"] = rename(ref)
		}
	}
	js, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	for len(js)%4 != 0 {
		js = append(js, ' ')
	}
	rest := data[20+size:] // the chunks after the JSON one, untouched
	out := make([]byte, 0, 20+len(js)+len(rest))
	out = binary.LittleEndian.AppendUint32(out, glbMagic)
	out = binary.LittleEndian.AppendUint32(out, 2)
	out = binary.LittleEndian.AppendUint32(out, uint32(20+len(js)+len(rest)))
	out = binary.LittleEndian.AppendUint32(out, uint32(len(js)))
	out = binary.LittleEndian.AppendUint32(out, glbJSONChunk)
	out = append(out, js...)
	return append(out, rest...), nil
}

// UsesSource says whether the description places the model file name, or a model that needs the picture name: what keeps a file of the
// sources from being removed from under a scene.
func (s *Spec) UsesSource(sourcesDir, name string) bool {
	for _, file := range s.Models() {
		if file == name {
			return true
		}
		if m, err := ReadModel(filepath.Join(sourcesDir, file)); err == nil {
			for _, need := range m.Needs {
				if need == name {
					return true
				}
			}
		}
	}
	return false
}
