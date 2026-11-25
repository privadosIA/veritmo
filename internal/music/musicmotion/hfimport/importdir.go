package hfimport

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"veritmo/internal/music/musicmotion"
)

// Options of Import.
type Options struct {
	// Dir holds the blocks as HyperFrames installed them: compositions/<name>.html (or the files straight in it), or as the registry
	// keeps them, <name>/<name>.html with its registry-item.json.
	Dir string
	// Only limits the import to these blocks (by name); empty means all of them.
	Only []string
	// DryRun checks everything and writes nothing.
	DryRun bool
	// Browser is the Chrome that runs the part checks.
	Browser string
	// Catalog is the catalog the passing blocks are written to (the program gives the one on its database). Without it, UserDir is the folder
	// of the user's parts they are written to.
	Catalog *musicmotion.Catalog
	UserDir string
	// Version is the version of HyperFrames the blocks come from, kept in each part's origin (the registry item's own version wins).
	Version string
}

// Line is what became of one block.
type Line struct {
	Block string `json:"block"`
	Part  string `json:"part,omitempty"`
	// Written says the part was written (with DryRun: it would be).
	Written bool `json:"written"`
	// Refused is why it was not, one sentence; Problems are all the reasons.
	Refused  string   `json:"refused,omitempty"`
	Problems []string `json:"problems,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
	// Pictures are the files of assets/ embedded in the part.
	Pictures []Picture `json:"pictures,omitempty"`
}

// HyperFramesURL is the address kept in the origin of the parts.
const HyperFramesURL = "https://github.com/heygen-com/hyperframes"

type blockFile struct {
	name string
	path string
	item string
}

// find lists the blocks in a folder, by name.
func find(dir string) ([]blockFile, error) {
	root := dir
	if st, err := os.Stat(filepath.Join(dir, "compositions")); err == nil && st.IsDir() {
		root = filepath.Join(dir, "compositions")
	}
	ents, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var out []blockFile
	for _, e := range ents {
		name := e.Name()
		switch {
		case e.IsDir():
			p := filepath.Join(root, name, name+".html")
			if _, err := os.Stat(p); err == nil {
				out = append(out, blockFile{name: name, path: p, item: filepath.Join(root, name, "registry-item.json")})
			}
		case strings.HasSuffix(strings.ToLower(name), ".html"):
			out = append(out, blockFile{name: strings.TrimSuffix(name, filepath.Ext(name)), path: filepath.Join(root, name)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out, nil
}

// Import converts the blocks of a folder, checks each as a part (musicmotion.CheckPart) and writes the ones that pass into the user's parts
// (spec music-motion-parts). It returns one line per block; an error is only for what stops the whole import (the folder, no browser).
func Import(ctx context.Context, o Options) ([]Line, error) {
	files, err := find(o.Dir)
	if err != nil {
		return nil, err
	}
	if len(o.Only) > 0 {
		want := map[string]bool{}
		for _, n := range o.Only {
			want[strings.TrimSpace(n)] = true
		}
		var kept []blockFile
		for _, f := range files {
			if want[f.name] {
				kept = append(kept, f)
				delete(want, f.name)
			}
		}
		if len(want) > 0 {
			var missing []string
			for n := range want {
				missing = append(missing, n)
			}
			sort.Strings(missing)
			return nil, fmt.Errorf("no such block in %s: %s", o.Dir, strings.Join(missing, ", "))
		}
		files = kept
	}
	cat := musicmotion.Catalog{Builtin: musicmotion.BuiltinParts(), UserDir: o.UserDir}
	if o.Catalog != nil {
		cat = *o.Catalog
	}
	var lines []Line
	for _, f := range files {
		b, err := os.ReadFile(f.path)
		if err != nil {
			lines = append(lines, Line{Block: f.name, Refused: err.Error(), Problems: []string{err.Error()}})
			continue
		}
		var item []byte
		if f.item != "" {
			item, _ = os.ReadFile(f.item)
		}
		r := ConvertWith(f.name, b, item, sourceFor(o.Dir, f))
		line := Line{Block: f.name, Part: r.Name, Warnings: r.Warnings, Pictures: r.Pictures}
		refuse := func(probs []string) {
			line.Problems = probs
			line.Refused = probs[0]
			if len(probs) > 1 {
				line.Refused += fmt.Sprintf(" (and %d more)", len(probs)-1)
			}
		}
		if len(r.Problems) > 0 {
			refuse(r.Problems)
			lines = append(lines, line)
			continue
		}
		if _, taken := cat.Get(r.Name); taken {
			refuse([]string{fmt.Sprintf("the part %q already exists", r.Name)})
			lines = append(lines, line)
			continue
		}
		part, probs := musicmotion.ParsePart(r.ManifestJSON(), r.Fragment)
		if len(probs) == 0 {
			probs, err = musicmotion.CheckPart(ctx, o.Browser, part, musicmotion.CheckOptions{})
			if err != nil {
				return lines, fmt.Errorf("checking %s: %w", f.name, err)
			}
		}
		if len(probs) > 0 {
			refuse(probs)
			lines = append(lines, line)
			continue
		}
		if !o.DryRun {
			version := o.Version
			if v := itemVersion(item); v != "" {
				version = v
			}
			origin := &musicmotion.PartOrigin{Source: "hyperframes", Block: f.name, Version: version, Licence: "Apache-2.0", URL: HyperFramesURL, ApprovedAt: time.Now().UTC(), Files: originFiles(r.Pictures)}
			if err := cat.CreatePart(r.ManifestJSON(), r.Fragment, origin); err != nil {
				refuse([]string{err.Error()})
				lines = append(lines, line)
				continue
			}
		}
		line.Written = true
		lines = append(lines, line)
	}
	return lines, nil
}

func itemVersion(item []byte) string {
	var m Meta
	if len(item) == 0 {
		return ""
	}
	_ = json.Unmarshal(item, &m)
	return m.Version
}

func originFiles(ps []Picture) []musicmotion.OriginFile {
	var out []musicmotion.OriginFile
	for _, p := range ps {
		out = append(out, musicmotion.OriginFile{Name: p.Name, Kind: p.Kind, Bytes: p.Bytes})
	}
	return out
}

// sourceFor reads the files a block refers to: first from the block's own folder (the registry's layout), then from the folder of
// the blocks, with and without its compositions/ (the layout that shares one assets/). A path never leaves those folders.
func sourceFor(dir string, f blockFile) Source {
	roots := []string{}
	if f.item != "" {
		roots = append(roots, filepath.Dir(f.path))
	}
	roots = append(roots, dir)
	if st, err := os.Stat(filepath.Join(dir, "compositions")); err == nil && st.IsDir() {
		roots = append(roots, filepath.Join(dir, "compositions"))
	}
	return func(rel string) ([]byte, error) {
		if rel != filepath.ToSlash(filepath.Clean(rel)) || strings.HasPrefix(rel, "/") || strings.Contains(rel, "..") {
			return nil, os.ErrNotExist
		}
		for _, r := range roots {
			p := filepath.Join(r, filepath.FromSlash(rel))
			if st, err := os.Lstat(p); err == nil && st.Mode().IsRegular() {
				return os.ReadFile(p)
			}
		}
		return nil, os.ErrNotExist
	}
}
