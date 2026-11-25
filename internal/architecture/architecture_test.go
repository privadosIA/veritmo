// Package architecture holds the one test that keeps the layout of internal/ honest. It reads the imports of every
// Go file (no compilation) and checks the dependency rules written down in docs/DEVELOPMENT.md.
package architecture

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

const module = "veritmo/"

// rule says which internal folders a folder may import (besides itself). A folder that is not listed is free.
var rule = map[string][]string{
	"platform":  {},           // the base: knows nothing about the features
	"assistant": {"platform"}, // the language-model helpers use only the base
	"speech":    {"platform"}, // text to speech
	"models":    {"platform"}, // model conversion
}

// imports returns, for every non-test Go file under dir, its internal imports.
func imports(t *testing.T, dir string) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return err
		}
		f, perr := parser.ParseFile(token.NewFileSet(), p, nil, parser.ImportsOnly)
		if perr != nil {
			return perr
		}
		for _, im := range f.Imports {
			path := strings.Trim(im.Path.Value, `"`)
			if strings.HasPrefix(path, module+"internal/") {
				out[p] = append(out[p], strings.TrimPrefix(path, module+"internal/"))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func folder(importPath string) string {
	first, _, _ := strings.Cut(importPath, "/")
	return first
}

// TestFolderRules: the base and the small helper folders import only what they are allowed to.
func TestFolderRules(t *testing.T) {
	for dir, allowed := range rule {
		ok := map[string]bool{dir: true}
		for _, a := range allowed {
			ok[a] = true
		}
		for file, imps := range imports(t, filepath.Join("..", dir)) {
			for _, imp := range imps {
				if !ok[folder(imp)] {
					t.Errorf("%s imports internal/%s: internal/%s may only import %v", file, imp, dir, append([]string{dir}, allowed...))
				}
			}
		}
	}
}

// TestNothingImportsTheServer: internal/server is the top of the stack; only the program (cmd/) uses it.
func TestNothingImportsTheServer(t *testing.T) {
	entries, err := filepath.Glob(filepath.Join("..", "*"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if filepath.Base(e) == "server" || filepath.Base(e) == "architecture" {
			continue
		}
		for file, imps := range imports(t, e) {
			for _, imp := range imps {
				if folder(imp) == "server" {
					t.Errorf("%s imports internal/%s: only cmd/ may import internal/server", file, imp)
				}
			}
		}
	}
}

// TestServerPackages: internal/server is the composition root of its own sub-packages (one per family, plus
// api, the kernel). A sub-package never imports package server; api imports no other sub-package; a family
// may import api and, once the stack of families is settled, only the ones below it (listed in serverRule).
func TestServerPackages(t *testing.T) {
	serverRule := map[string][]string{ // sub-package -> sub-packages it may import (besides itself)
		"api":        {},
		"modelcat":   {},
		"assistant":  {"api"},
		"prompts":    {"api"},
		"workspace":  {"api", "modelcat"},
		"models":     {"api", "modelcat", "workspace"},
		"generation": {"api", "modelcat", "assistant", "workspace", "models"},
		"music":      {"api", "modelcat", "assistant", "workspace", "models", "generation"},
		"library3d":  {"api", "workspace"},
	}
	entries, err := filepath.Glob(filepath.Join("..", "server", "*"))
	if err != nil {
		t.Fatal(err)
	}
	const prefix = "server/"
	for _, e := range entries {
		name := filepath.Base(e)
		if fi, err := filepath.Glob(filepath.Join(e, "*.go")); err != nil || len(fi) == 0 {
			continue // not a package (servertest helpers, fixtures)
		}
		allowed, listed := serverRule[name]
		if !listed && name != "servertest" {
			t.Errorf("internal/server/%s is a package that serverRule does not list: say which sub-packages it may import", name)
			continue
		}
		ok := map[string]bool{name: true}
		for _, a := range allowed {
			ok[a] = true
		}
		for file, imps := range imports(t, e) {
			for _, imp := range imps {
				if imp == "server" {
					t.Errorf("%s imports package server: a sub-package never does", file)
				}
				if strings.HasPrefix(imp, prefix) {
					sub, _, _ := strings.Cut(strings.TrimPrefix(imp, prefix), "/")
					if name != "servertest" && !ok[sub] {
						t.Errorf("%s imports internal/%s: internal/server/%s may only import %v", file, imp, name, allowed)
					}
				}
			}
		}
	}
}
