package hyperframes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/url"
	"os"
	"path"
	"regexp"
	"strings"

	"veritmo/internal/video/motion"
)

// pageHost is the address blocks are served under: the page is index.html and its relative files (assets/x.js, lib/y.js)
// are the files of the block.
const pageHost = "motion.invalid"

// Drawer draws blocks in a headless browser. A page can only get the files of its block and the pinned libraries; any other
// request fails and is reported as a problem.
type Drawer struct {
	Store   Store
	Browser string
	// LibsDir is where the libraries are kept (LibsDir of the package when empty).
	LibsDir string
	// Transparent leaves the page's background transparent (a block that draws only on a part of the frame, such as a lower
	// third or snow, is not hidden by the white of the page). The checks use it; thumbnails and previews do not.
	Transparent bool
	// Runtime says which runtime draws the page: "upstream" (HyperFrames' own, built by scripts/get-hyperframes-runtime.sh), "builtin"
	// (this package's small one) or empty for upstream when it is installed and the builtin one when it is not.
	Runtime string
	// Limits for the browser.
	Limits motion.Limits
}

// ErrLibs is returned when a block needs libraries that are not installed.
var ErrLibs = errors.New("the HyperFrames libraries are not installed (bash scripts/get-hyperframes-libs.sh)")

// Open starts a session that draws the block at the size and length it declares. The caller closes it. The block is drawn as
// stored; html, when not empty, replaces the stored main file (the preview of an edit).
func (d Drawer) Open(ctx context.Context, name, html string) (*motion.Session, Block, error) {
	blk, pg, err := d.Page(name, html, 0, nil)
	if err != nil {
		return nil, blk, err
	}
	sess, err := motion.Open(ctx, d.Browser, pg, d.Limits)
	return sess, blk, err
}

// UsesUpstream says whether the page with this main file is drawn with the upstream runtime. Forced by Runtime; left empty, it is the
// one that fits the page: the upstream runtime for a page that mounts sub-compositions (data-composition-src), which only it can mount, when
// it is installed, and the small builtin one for every other (the 177 blocks that pass with it are the ones checked; the upstream one
// makes some GPU shaders drift between passes).
func (d Drawer) UsesUpstream(html string) bool {
	switch d.Runtime {
	case "builtin":
		return false
	case "upstream":
		return true
	}
	if !strings.Contains(html, "data-composition-src") {
		return false
	}
	_, ok := UpstreamRuntime()
	return ok
}

// Page is what a browser needs to draw a block: its page, size, the runtime with the variables (the defaults the block declares
// with vars laid over them) and the resolver of what it asks for. seconds is the length it is drawn for (0: the block's own).
func (d Drawer) Page(name, html string, seconds float64, vars map[string]any) (Block, motion.Page, error) {
	blk, err := d.Store.Get(name)
	if err != nil {
		return blk, motion.Page{}, err
	}
	if html == "" {
		b, err := d.Store.Read(name, blk.HTML)
		if err != nil {
			return blk, motion.Page{}, fmt.Errorf("the main file of %s: %w", name, err)
		}
		html = string(b)
	}
	if blk.Width <= 0 || blk.Height <= 0 {
		return blk, motion.Page{}, fmt.Errorf("%s declares no size", name)
	}
	libs := d.LibsDir
	if libs == "" {
		libs = LibsDir
	}
	dur := seconds
	if dur <= 0 {
		dur = blk.Duration
	}
	if dur <= 0 {
		dur = 1
	}
	all := Defaults(blk.Variables)
	for k, v := range vars {
		all[k] = v
	}
	pg := motion.Page{
		HTML: html, Width: blk.Width, Height: blk.Height, Duration: dur, GPU: true, Transparent: d.Transparent,
		Resource: d.resource(name, blk, libs), Init: Runtime(all, dur),
	}
	if d.UsesUpstream(html) {
		// the variables are the block's own defaults with the item's over them, as the runtime reads them from the page
		vj, _ := json.Marshal(all)
		up, _ := UpstreamInit(false)
		pg.Init = SeedScript + "\nwindow.__hfVariables = " + string(vj) + ";\n" + up
	}
	return blk, pg, nil
}

// RenderClip plays a block to a video file of the given length and frame rate, with no song: the frames come from the block's own
// timeline. vars overrides the defaults of the variables the block declares. It gives the number of frames and the problems the page
// reported; the video is written even then.
func (d Drawer) RenderClip(ctx context.Context, name string, seconds float64, fps int, vars map[string]any, out string) (int, []string, error) {
	_, pg, err := d.Page(name, "", seconds, vars)
	if err != nil {
		return 0, nil, err
	}
	return motion.Render(ctx, motion.Job{Page: pg, FPS: fps, Browser: d.Browser, Out: out, Limits: d.Limits})
}

// resource answers what the page of one block asks for besides itself.
func (d Drawer) resource(name string, blk Block, libs string) func(string) ([]byte, string, bool) {
	return d.resolver(libs, func(rel string) (string, string) { return name, rel })
}

// resolver answers what a page asks for: the files of a block (which block, and the path in it, comes from split, given the path
// under the page's own address), the pinned libraries and the Google Fonts stylesheets kept. Anything else is refused.
func (d Drawer) resolver(libs string, split func(rel string) (name, file string)) func(string) ([]byte, string, bool) {
	return func(raw string) ([]byte, string, bool) {
		u, err := url.Parse(raw)
		if err != nil {
			return nil, "", false
		}
		if u.Host == pageHost {
			rel := strings.TrimPrefix(u.Path, "/")
			if rel == "" || rel == "index.html" {
				return nil, "", false
			}
			name, file := split(rel)
			if name == "" || file == "" {
				return nil, "", false
			}
			b, err := d.Store.Read(name, file)
			if err != nil {
				return nil, "", false
			}
			return b, contentType(file), true
		}
		if u.Host == "fonts.googleapis.com" {
			if css, ok := FontCSS(raw); ok {
				return css, "text/css; charset=utf-8", true
			}
			return nil, "", false
		}
		p, ok := Lookup(libs, raw)
		if !ok {
			return nil, "", false
		}
		b, err := readFile(p)
		if err != nil {
			return nil, "", false
		}
		return b, contentType(u.Path), true
	}
}

// BlockBase is the address the files of a block are served under when the block sits inside another page (an iframe), the <base> of
// its document.
func BlockBase(name string) string { return "http://" + pageHost + "/hf/" + name + "/" }

var reBlockRef = regexp.MustCompile(`data-hf-block="([a-z0-9][a-z0-9-]{0,80})"`)

// Extend gives a page that embeds blocks (an iframe marked data-hf-block, see musicmotion) what they need to be drawn: the
// resolver of their files and of the pinned libraries, the HyperFrames runtime in every document (the blocks' own) and software
// WebGL and WebGPU. A page with no block is left as it is. The blocks and libraries are the server's own folders.
func Extend(pg *motion.Page) {
	if !reBlockRef.MatchString(pg.HTML) {
		return
	}
	d := Drawer{Store: Store{Dir: ResolveDir(os.Getenv)}}
	blocks := d.resolver(LibsDir, func(rel string) (string, string) {
		if rest, ok := strings.CutPrefix(rel, "hf/"); ok {
			if name, file, ok := strings.Cut(rest, "/"); ok {
				return name, file
			}
		}
		return "", ""
	})
	prev := pg.Resource
	pg.Resource = func(u string) ([]byte, string, bool) {
		if b, ct, ok := blocks(u); ok {
			return b, ct, true
		}
		if prev != nil {
			return prev(u)
		}
		return nil, "", false
	}
	pg.GPU = true
	pg.Init = pg.Init + "\n" + Runtime(nil, pg.Duration)
	if strings.Contains(pg.HTML, "data-composition-src") {
		// a block that mounts sub-compositions (the components) needs the runtime that mounts them, in the iframes only
		if up, ok := UpstreamInit(true); ok {
			pg.Init += "\n" + up
		}
	}
}

func contentType(p string) string {
	switch strings.ToLower(path.Ext(p)) {
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8"
	case ".json":
		return "application/json"
	case ".wasm":
		return "application/wasm"
	case ".woff2":
		return "font/woff2"
	case ".woff":
		return "font/woff"
	case ".ttf":
		return "font/ttf"
	case ".css":
		return "text/css; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	}
	if t := mime.TypeByExtension(path.Ext(p)); t != "" {
		return t
	}
	if strings.HasSuffix(p, "/+esm") {
		return "text/javascript; charset=utf-8"
	}
	return "application/octet-stream"
}
