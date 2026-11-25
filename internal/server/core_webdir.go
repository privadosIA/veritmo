package server

import (
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path"
	"regexp"
	"strings"
)

// hashedName matches the files Angular names after their content (chunk-0i58r8Jb.js): they never change under that name.
var hashedName = regexp.MustCompile(`-[A-Za-z0-9_-]{8}\.(js|css)$`)

// webDirHandler serves the static files of a front end from dir, whatever front end it is. The directory is read on
// every request, so a new build shows up without restarting the server.
func webDirHandler(dir string) http.Handler {
	root := os.DirFS(dir)
	fileServer := http.FileServerFS(root)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Cleaning a rooted path drops every ".." before it reaches the file system.
		p := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if p == "" {
			p = "index.html"
		}
		info, err := fs.Stat(root, p)
		if err != nil || info.IsDir() {
			// SPA fallback: an unknown non-API path loads the app shell so its router resolves the route.
			w.Header().Set("Cache-Control", "no-cache")
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/"
			fileServer.ServeHTTP(w, r2)
			return
		}
		// The app shell and the files with a fixed name are checked again on every visit, so a phone never keeps
		// showing the screens of a previous build; the hashed files can be kept for good.
		if hashedName.MatchString(p) {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		if p == "manifest.webmanifest" {
			// Not every system's MIME table knows this extension.
			w.Header().Set("Content-Type", "application/manifest+json")
		}
		fileServer.ServeHTTP(w, r)
	})
}

// noFrontEndHandler answers the root when the operator gave no --web-dir: a plain-text note, so a browser pointed at
// the server says what to do instead of showing an error.
func noFrontEndHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(w, "veritmo API is running, but this server has no front end.")
		fmt.Fprintln(w, "Serve a built one with: veritmo serve --web-dir <folder>")
		fmt.Fprintln(w, "The API lives under /api/ (see docs/http-api.md).")
	})
}
