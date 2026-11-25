package hfimport

// assets.go: the small pictures a block reads from assets/ travel inside the part as data: URLs (spec music-motion-parts,
// "HyperFrames blocks are imported as parts"). A part stays two files, the pages' CSP already allows img-src data:, and the
// frame stays deterministic. What cannot be made self-contained (video, audio, GIF, Lottie, 3D) refuses the block.

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"path"
	"regexp"
	"strings"

	"veritmo/internal/music/musicmotion"
)

const (
	maxSVGBytes    = 50 << 10
	maxRasterBytes = 200 << 10
	// maxPictureBytes is what all the pictures of a part may weigh once embedded (base64 included).
	maxPictureBytes = 600 << 10
	// maxPartHTML is the hard stop for the fragment of an imported part.
	maxPartHTML = 1 << 20
)

// Source reads a file the block refers to, by its path from the block's folder or from the folder of the blocks
// ("assets/stroke.svg"). The error says the file is not there.
type Source func(rel string) ([]byte, error)

// Picture is a file embedded in a part.
type Picture struct {
	Name  string `json:"name"`
	Kind  string `json:"kind"` // svg, png, jpeg or webp
	Bytes int    `json:"bytes"`
}

var (
	reImgSrc   = regexp.MustCompile(`(?is)(<img\b[^>]*?\bsrc\s*=\s*)(["'])([^"']*)(["'])`)
	reImageRef = regexp.MustCompile(`(?is)(<image\b[^>]*?\b(?:xlink:)?href\s*=\s*)(["'])([^"']*)(["'])`)
	reCSSURL   = regexp.MustCompile(`(?i)url\(\s*(["']?)\s*((?:\./)?assets/[^)"']*?)\s*(["']?)\s*\)`)
	reAnyImg   = regexp.MustCompile(`(?is)<img\b[^>]*>`)
	reSrcset   = regexp.MustCompile(`(?i)\b(srcset|poster)\s*=`)
	reSVGHref  = regexp.MustCompile(`(?i)(?:xlink:)?href\s*=\s*["']\s*([^"'#][^"']*)`)
)

// embedder embeds the pictures of one block, counting what they weigh and what it found wrong.
type embedder struct {
	read     Source
	pictures []Picture
	seen     map[string]bool
	weight   int
	problems []string
	warnings []string
}

func (e *embedder) problem(format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	for _, p := range e.problems {
		if p == msg {
			return
		}
	}
	e.problems = append(e.problems, msg)
}

// isAssetRef says whether a reference points at the assets/ folder.
func isAssetRef(ref string) bool {
	r := strings.TrimPrefix(strings.TrimSpace(ref), "./")
	return strings.HasPrefix(r, "assets/")
}

// dataURL returns the data: URL of the file a reference names, or the placeholder after recording why it cannot be embedded.
func (e *embedder) dataURL(ref string) string {
	url := e.embed(ref)
	if url == "" {
		// the block is refused (a problem was recorded); the placeholder keeps the generic checks from saying the same thing again
		return failedPicture
	}
	return url
}

// failedPicture stands for a reference that could not be embedded: the block is refused, so it is never written.
const failedPicture = "data:image/png;base64,"

func (e *embedder) embed(ref string) string {
	ref = strings.TrimPrefix(strings.TrimSpace(ref), "./")
	if strings.Contains(ref, "..") || path.Clean(ref) != ref || !strings.HasPrefix(ref, "assets/") {
		e.problem("it reads %s, which is not a clean path under assets/", ref)
		return ""
	}
	ext := strings.ToLower(path.Ext(ref))
	switch ext {
	case ".svg", ".png", ".jpg", ".jpeg", ".webp":
	case ".gif":
		e.problem("it reads %s: a GIF (animated or not) is refused, parts carry no media", ref)
		return ""
	case ".json", ".lottie", ".glb", ".gltf", ".mp4", ".webm", ".mov", ".mp3", ".wav", ".ogg":
		e.problem("it reads %s: parts carry no media nor runtimes", ref)
		return ""
	default:
		e.problem("it reads %s: parts carry no files other than small pictures", ref)
		return ""
	}
	if e.read == nil {
		e.problem("it reads %s, and no folder was given to find it in", ref)
		return ""
	}
	data, err := e.read(ref)
	if err != nil {
		e.problem("the file %s is not there", ref)
		return ""
	}
	kind, mime := "", ""
	switch ext {
	case ".svg":
		kind, mime = "svg", "image/svg+xml"
		if len(data) > maxSVGBytes {
			e.problem("%s is %d KB, over the %d KB limit for a drawing", ref, (len(data)+1023)>>10, maxSVGBytes>>10)
			return ""
		}
		if p := musicmotion.SVGProblem(string(data)); p != "" {
			e.problem("%s: %s", ref, p)
			return ""
		}
		if m := reSVGHref.FindStringSubmatch(string(data)); m != nil && !strings.HasPrefix(strings.ToLower(strings.TrimSpace(m[1])), "data:") {
			e.problem("%s refers to another file (%s)", ref, strings.TrimSpace(m[1]))
			return ""
		}
	default:
		if len(data) > maxRasterBytes {
			e.problem("%s is %d KB, over the %d KB limit for a picture", ref, (len(data)+1023)>>10, maxRasterBytes>>10)
			return ""
		}
		mime = http.DetectContentType(data)
		want := map[string]string{".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".webp": "image/webp"}[ext]
		if mime != want {
			e.problem("%s is not a %s picture", ref, strings.ToUpper(strings.TrimPrefix(ext, ".")))
			return ""
		}
		kind = strings.TrimPrefix(mime, "image/")
		if !e.seen[ref] {
			e.warnings = append(e.warnings, fmt.Sprintf("%s (%d KB) is a picture that keeps its own colours: the design does not recolour it", ref, (len(data)+1023)>>10))
		}
	}
	url := "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)
	e.weight += len(url)
	if e.weight > maxPictureBytes {
		e.problem("its pictures weigh over %d KB once embedded", maxPictureBytes>>10)
		return ""
	}
	if !e.seen[ref] {
		e.seen[ref] = true
		e.pictures = append(e.pictures, Picture{Name: path.Base(ref), Kind: kind, Bytes: len(data)})
	}
	return url
}

// attr replaces the value of the references of one kind of attribute that point at assets/.
func (e *embedder) attr(text string, re *regexp.Regexp) string {
	return re.ReplaceAllStringFunc(text, func(m string) string {
		sub := re.FindStringSubmatch(m)
		if !isAssetRef(sub[3]) {
			return m
		}
		return sub[1] + sub[2] + e.dataURL(sub[3]) + sub[4]
	})
}

// css replaces the url() that point at assets/.
func (e *embedder) css(text string) string {
	return reCSSURL.ReplaceAllStringFunc(text, func(m string) string {
		sub := reCSSURL.FindStringSubmatch(m)
		return `url("` + e.dataURL(sub[2]) + `")`
	})
}

// embedPictures embeds what the CSS and the markup of a block read from assets/. It returns the rewritten text, the files embedded
// and what it found wrong or worth saying. Other references to assets/ are left in place for the caller's check to refuse.
func embedPictures(cssText, body string, read Source) (string, string, []Picture, []string, []string) {
	e := &embedder{read: read, seen: map[string]bool{}}
	cssText = e.css(cssText)
	body = e.attr(body, reImgSrc)
	body = e.attr(body, reImageRef)
	body = reStyleAttr.ReplaceAllStringFunc(body, e.css)
	return cssText, body, e.pictures, e.problems, e.warnings
}

// mediaProblem says what in the text is media a part cannot carry: video, audio, a <picture> with sources, a srcset or poster, or an
// <img> whose source is not an embedded picture.
func mediaProblem(text string) string {
	if m := reMedia.FindStringSubmatch(text); m != nil {
		return "it mounts media (<" + strings.ToLower(m[1]) + ">): parts carry no media"
	}
	if m := reSrcset.FindStringSubmatch(text); m != nil {
		return "it uses " + strings.ToLower(m[1]) + ": parts carry no media"
	}
	for _, tag := range reAnyImg.FindAllString(text, -1) {
		m := reSrcAttr.FindStringSubmatch(tag)
		if m == nil || !strings.HasPrefix(strings.ToLower(m[1]), "data:image/") {
			return "it mounts media (<img>): parts carry no media (only pictures read from assets/ are embedded, and this source is not one)"
		}
	}
	return ""
}
