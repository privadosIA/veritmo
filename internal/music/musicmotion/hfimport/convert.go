// Package hfimport turns the blocks of HyperFrames (HeyGen, Apache-2.0) into parts of the music motion catalog: a block is a standalone
// HTML composition that builds one paused GSAP timeline and registers it in window.__timelines, which is the contract the parts speak
// too (spec music-motion-parts, "HyperFrames blocks are imported as parts").
//
// Convert is pure text work: it reads the block, refuses what a part cannot carry (media, the network, nested compositions, a canvas,
// a timing expression, a vertical canvas) and writes the manifest and the fragment. The caller runs the part checks and decides.
package hfimport

import (
	"encoding/json"
	"fmt"
	"html"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"veritmo/internal/music/musicmotion"
)

// Prefix is what every imported part's name starts with.
const Prefix = "hf-"

// Meta is what the registry item that sits beside a block says about it (registry-item.json); every field may be empty.
type Meta struct {
	Title       string  `json:"title"`
	Description string  `json:"description"`
	Version     string  `json:"version"`
	Duration    float64 `json:"duration"`
	Dimensions  struct {
		Width  int `json:"width"`
		Height int `json:"height"`
	} `json:"dimensions"`
}

// Result is the conversion of one block.
type Result struct {
	// Name is the name of the part (hf-<block>).
	Name     string
	Manifest musicmotion.Manifest
	Fragment string
	// Problems say why the block is refused (empty: it can be checked as a part). Warnings are things kept as they were that a person may care about.
	Problems []string
	Warnings []string
	// Pictures are the files of assets/ embedded in the fragment as data: URLs.
	Pictures []Picture
}

// ManifestJSON is the manifest as manifest.json holds it.
func (r Result) ManifestJSON() []byte {
	b, _ := json.MarshalIndent(r.Manifest, "", "  ")
	return b
}

var (
	reTemplate = regexp.MustCompile(`(?is)<template[^>]*>(.*)</template>`)
	reStyle    = regexp.MustCompile(`(?is)<style[^>]*>(.*?)</style>`)
	reScript   = regexp.MustCompile(`(?is)<script([^>]*)>(.*?)</script>`)
	reSrcAttr  = regexp.MustCompile(`(?is)\bsrc\s*=\s*["']?([^"'\s>]+)`)
	reBody     = regexp.MustCompile(`(?is)<body[^>]*>(.*)</body>`)
	reHead     = regexp.MustCompile(`(?is)<head[^>]*>.*?</head>`)
	reTitle    = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	reLink     = regexp.MustCompile(`(?is)<link\b[^>]*>`)
	reMeta     = regexp.MustCompile(`(?is)<meta\b[^>]*>`)
	reDoctype  = regexp.MustCompile(`(?is)<!doctype[^>]*>|</?html[^>]*>`)
	reComment  = regexp.MustCompile(`(?s)<!--.*?-->`)
	reCSSCmt   = regexp.MustCompile(`(?s)/\*.*?\*/`)
	reAtImport = regexp.MustCompile(`(?i)@(import|charset)[^;]*;`)

	reRootTag  = regexp.MustCompile(`(?is)<[a-z][^>]*\bdata-composition-id\b[^>]*>`)
	reDataW    = regexp.MustCompile(`(?i)\bdata-width\s*=\s*["']?(\d+)`)
	reDataH    = regexp.MustCompile(`(?i)\bdata-height\s*=\s*["']?(\d+)`)
	reVariable = regexp.MustCompile(`(?is)\bdata-(?:composition-)?variables\s*=\s*(?:'([^']*)'|"([^"]*)")`)
	reVarText  = regexp.MustCompile(`(?is)<([a-z][a-z0-9]*)\b([^>]*?)\sdata-var-text\s*=\s*["']([^"']+)["']([^>]*)>`)
	reStartVal = regexp.MustCompile(`(?i)\bdata-(start|duration)\s*=\s*["']([^"']*)["']`)

	reMedia    = regexp.MustCompile(`(?i)<(audio|video|source|track|picture)\b`)
	reCanvas   = regexp.MustCompile(`(?i)<canvas\b|getContext\s*\(\s*["'](2d|webgl2?|experimental-webgl)["']`)
	reFrame    = regexp.MustCompile(`(?i)<(iframe|object|embed)\b`)
	reMount    = regexp.MustCompile(`(?i)\bdata-composition-src\b|\bdata-variable-values\b`)
	reNetwork  = regexp.MustCompile(`\b(fetch|XMLHttpRequest|WebSocket|importScripts)\s*\(|\bimport\s*\(|(^|[;\n])\s*import\s+[\w{*]`)
	reAssets   = regexp.MustCompile(`["'(]\s*(\./)?assets/`)
	reVarSrc   = regexp.MustCompile(`(?i)\bdata-var-src\b`)
	reHFShim   = regexp.MustCompile(`__hyperframes`)
	reTimeline = regexp.MustCompile(`__timelines`)

	reColour     = regexp.MustCompile(`#[0-9a-fA-F]{3,8}\b|\brgba?\(|\bhsla?\(`)
	reFontFamily = regexp.MustCompile(`(?i)font-family\s*:\s*([^;}]*)`)
	reFontInDQ   = regexp.MustCompile(`(?i)font-family\s*:\s*([^;"]*)`)
	reFontInSQ   = regexp.MustCompile(`(?i)font-family\s*:\s*([^;']*)`)
	reFontWeight = regexp.MustCompile(`(?i)font-weight\s*:\s*(\d{3}|bold|bolder)`)
	reStyleAttr  = regexp.MustCompile(`(?is)\bstyle\s*=\s*("[^"]*"|'[^']*')`)
	reHeavyFace  = regexp.MustCompile(`(?i)archivo black|league gothic|bebas|anton|impact|fraunces|playfair|oswald|black`)
	reValidName  = regexp.MustCompile(`[^a-z0-9]+`)
)

// Convert converts one block that reads no files. name is the block's own name (the file's name without .html); item is its
// registry-item.json, or nil.
func Convert(name string, block []byte, item []byte) Result {
	return ConvertWith(name, block, item, nil)
}

// ConvertWith is Convert for a block whose pictures can be read from assets/ with read (nil: it finds none).
func ConvertWith(name string, block []byte, item []byte, read Source) Result {
	res := Result{Name: PartName(name)}
	var meta Meta
	if len(item) > 0 {
		_ = json.Unmarshal(item, &meta) // a registry item that does not read is only less information
	}
	doc := string(block)
	title := ""
	if m := reTitle.FindStringSubmatch(doc); m != nil {
		title = strings.TrimSpace(html.UnescapeString(m[1]))
	}
	if m := reTemplate.FindStringSubmatch(doc); m != nil {
		doc = m[1]
	}
	doc = reComment.ReplaceAllString(doc, "")

	// the styles, in order
	var css strings.Builder
	for _, m := range reStyle.FindAllStringSubmatch(doc, -1) {
		css.WriteString(m[1])
		css.WriteString("\n")
	}
	doc = reStyle.ReplaceAllString(doc, "")

	// the scripts: the GSAP of the CDN is dropped (the page embeds its own), any other file is refused, the inline ones are kept in order
	var scripts []string
	var refused []string
	for _, m := range reScript.FindAllStringSubmatch(doc, -1) {
		if sm := reSrcAttr.FindStringSubmatch(m[1]); sm != nil {
			if strings.Contains(strings.ToLower(sm[1]), "gsap") {
				continue
			}
			refused = append(refused, "it loads the script "+sm[1]+" from outside: a part is one inline script")
			continue
		}
		if s := strings.TrimSpace(m[2]); s != "" {
			scripts = append(scripts, s)
		}
	}
	doc = reScript.ReplaceAllString(doc, "")
	if links := len(reLink.FindAllString(doc, -1)); links > 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf("%d <link> tag(s) dropped (fonts and styles are inline)", links))
	}
	doc = reLink.ReplaceAllString(doc, "")
	doc = reMeta.ReplaceAllString(doc, "")
	var body string
	if m := reBody.FindStringSubmatch(doc); m != nil {
		body = m[1]
	} else {
		body = reHead.ReplaceAllString(doc, "")
	}
	body = strings.TrimSpace(reDoctype.ReplaceAllString(body, ""))

	// the style: no rule of the page (html, body, *, :root) reaches outside the stage, fonts are the design's (and their @font-face
	// is gone before anything is judged, so a font file under assets/ refuses nothing), colours are counted
	cssText, warn := rewriteCSS(reAtImport.ReplaceAllString(reCSSCmt.ReplaceAllString(css.String(), ""), ""))
	res.Warnings = append(res.Warnings, warn...)
	body, fontWarn := mapInlineFonts(body)
	res.Warnings = append(res.Warnings, fontWarn...)
	if n := len(reColour.FindAllString(cssText+" "+body, -1)); n > 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf("%d colour literal(s) kept as the block wrote them (not the design's variables)", n))
	}
	// the small pictures the block reads from assets/ travel inside the part
	var picProblems, picWarnings []string
	cssText, body, res.Pictures, picProblems, picWarnings = embedPictures(cssText, body, read)
	res.Warnings = append(res.Warnings, picWarnings...)

	all := cssText + "\n" + body + "\n" + strings.Join(scripts, "\n")
	res.Problems = append(res.Problems, refused...)
	res.Problems = append(res.Problems, picProblems...)
	if m := mediaProblem(all); m != "" {
		res.Problems = append(res.Problems, m)
	}
	if reCanvas.MatchString(all) {
		res.Problems = append(res.Problems, "it draws on a canvas (2D or WebGL), which parts do not run")
	}
	if m := reFrame.FindStringSubmatch(all); m != nil {
		res.Problems = append(res.Problems, "it embeds a <"+strings.ToLower(m[1])+">")
	}
	if m := reMount.FindString(all); m != "" {
		res.Problems = append(res.Problems, "it mounts another composition ("+strings.ToLower(m)+"): only standalone blocks are imported")
	}
	if reNetwork.MatchString(all) {
		res.Problems = append(res.Problems, "it uses the network or imports a module (fetch, XMLHttpRequest, import): a part is one inline script")
	}
	if reAssets.MatchString(all) {
		res.Problems = append(res.Problems, "it reads files from assets/ that cannot be embedded (only small pictures can): parts carry no files")
	}
	if reVarSrc.MatchString(all) {
		res.Problems = append(res.Problems, "a variable sets a media source (data-var-src): parts carry no media")
	}
	for _, m := range reStartVal.FindAllStringSubmatch(body, -1) {
		if _, err := strconv.ParseFloat(strings.TrimSpace(m[2]), 64); err != nil {
			res.Problems = append(res.Problems, fmt.Sprintf("its timing is an expression (data-%s=%q): only seconds are accepted", m[1], m[2]))
			break
		}
	}
	if !reTimeline.MatchString(strings.Join(scripts, "\n")) {
		res.Problems = append(res.Problems, "it registers no GSAP timeline (window.__timelines): there is nothing to seek")
	}

	// the canvas the block was made for
	w, h := meta.Dimensions.Width, meta.Dimensions.Height
	if tag := reRootTag.FindString(body); tag != "" {
		if m := reDataW.FindStringSubmatch(tag); m != nil {
			w, _ = strconv.Atoi(m[1])
		}
		if m := reDataH.FindStringSubmatch(tag); m != nil {
			h, _ = strconv.Atoi(m[1])
		}
	}
	if w <= 0 || h <= 0 {
		w, h = 1920, 1080
	}
	if h > w {
		res.Problems = append(res.Problems, fmt.Sprintf("its canvas is vertical (%d x %d): the frame is landscape", w, h))
	}

	// the variables become parameters
	params, bindings, perr := variables(doc + "\n" + all)
	res.Problems = append(res.Problems, perr...)
	body = bindText(body, bindings)
	if reHFShim.MatchString(strings.Join(scripts, "\n")) {
		// the helper of HyperFrames' runtime that gives a script its variables: the page's parameters are those values
		scripts = append([]string{"window.__hyperframes = window.__hyperframes || {getVariables: function(){ return window.__params || {}; }};"}, scripts...)
	}

	// the stage: the block's canvas in pixels, scaled to the frame and centred
	var vars strings.Builder
	for _, b := range bindings {
		fmt.Fprintf(&vars, "--%s:{{%s}};", b.id, b.name)
	}
	var frag strings.Builder
	frag.WriteString("<style>" + strings.TrimSpace(cssText) + "\n.hf-stage{position:absolute;left:0;top:0;width:" + strconv.Itoa(w) + "px;height:" + strconv.Itoa(h) + "px;overflow:hidden;transform-origin:0 0}</style>\n")
	frag.WriteString(`<div class="hf-stage" style="` + vars.String() + `">` + "\n" + body + "\n</div>\n")
	frag.WriteString(fmt.Sprintf(`<script>(function(){var s=document.currentScript.previousElementSibling,W=%d,H=%d,k=Math.min(innerWidth/W,innerHeight/H);s.style.transform='translate('+((innerWidth-W*k)/2)+'px,'+((innerHeight-H*k)/2)+'px) scale('+k+')';})();</script>`+"\n", w, h))
	for _, s := range scripts {
		frag.WriteString("<script>" + strings.ReplaceAll(s, "</script", `<\/script`) + "</script>\n")
	}
	res.Fragment = frag.String()
	if len(res.Fragment) > maxPartHTML {
		res.Problems = append(res.Problems, fmt.Sprintf("the part would weigh %d KB, over the %d KB limit", len(res.Fragment)>>10, maxPartHTML>>10))
	}

	desc := strings.TrimSpace(meta.Description)
	if desc == "" {
		desc = title
	}
	if desc == "" {
		desc = "HyperFrames block " + name
	}
	if t := strings.TrimSpace(meta.Title); t != "" && !strings.Contains(desc, t) {
		desc = t + ": " + desc
	}
	res.Manifest = musicmotion.Manifest{Name: res.Name, Description: desc, Version: 1, Levels: []string{musicmotion.LevelSimple, musicmotion.LevelMedium, musicmotion.LevelAdvanced}, Params: params, Pictures: len(res.Pictures)}
	if res.Manifest.Params == nil {
		res.Manifest.Params = []musicmotion.Param{}
	}
	if len(res.Problems) == 0 {
		// the rules of any part: its manifest and its fragment
		if _, probs := musicmotion.ParsePart(res.ManifestJSON(), res.Fragment); len(probs) > 0 {
			res.Problems = append(res.Problems, probs...)
		}
	}
	return res
}

// PartName is the name of the part a block makes.
func PartName(block string) string {
	n := strings.Trim(reValidName.ReplaceAllString(strings.ToLower(block), "-"), "-")
	if name := Prefix + n; len(name) <= maxName {
		return name
	}
	// a part's name has 32 characters at most: the block's name is cut (at a dash when there is one near the end)
	return strings.TrimRight((Prefix + n)[:maxName], "-")
}

// maxName is the longest name a part may have.
const maxName = 32

// binding says which parameter a variable of the block is.
type binding struct{ id, name string }

type hfVariable struct {
	ID      string   `json:"id"`
	Type    string   `json:"type"`
	Label   string   `json:"label"`
	Default any      `json:"default"`
	Min     *float64 `json:"min"`
	Max     *float64 `json:"max"`
	Options []any    `json:"options"`
}

var reParamName = regexp.MustCompile(`[^a-z0-9_]+`)

// colourDefault is the variable of the design a colour variable stands for, from its name.
func colourDefault(id string) string {
	l := strings.ToLower(id)
	switch {
	case strings.Contains(l, "background") || l == "bg" || strings.HasPrefix(l, "bg-") || strings.HasPrefix(l, "bg_"):
		return "var(--bg)"
	case strings.Contains(l, "text") || strings.Contains(l, "foreground") || l == "fg":
		return "var(--fg)"
	}
	return "var(--accent)"
}

// variables reads the variables a block declares (data-composition-variables or data-variables, a JSON list) as parameters.
func variables(doc string) ([]musicmotion.Param, []binding, []string) {
	m := reVariable.FindStringSubmatch(doc)
	if m == nil {
		return nil, nil, nil
	}
	raw := m[1]
	if raw == "" {
		raw = m[2]
	}
	var vs []hfVariable
	if err := json.Unmarshal([]byte(html.UnescapeString(raw)), &vs); err != nil {
		return nil, nil, []string{"its variables are not a JSON list: " + err.Error()}
	}
	var params []musicmotion.Param
	var binds []binding
	var probs []string
	used := map[string]bool{}
	for _, v := range vs {
		name := strings.Trim(reParamName.ReplaceAllString(strings.ToLower(v.ID), "_"), "_")
		if name == "" || name[0] < 'a' || name[0] > 'z' || used[name] {
			probs = append(probs, fmt.Sprintf("the variable %q has no usable name", v.ID))
			continue
		}
		used[name] = true
		p := musicmotion.Param{Name: name, Description: v.Label}
		switch v.Type {
		case "string", "":
			s, _ := v.Default.(string)
			p.Type, p.Default, p.MaxLength = "string", s, max(80, len([]rune(s)))
		case "number":
			f, _ := v.Default.(float64)
			p.Type, p.Default, p.Min, p.Max = "number", f, v.Min, v.Max
		case "boolean", "bool":
			b, _ := v.Default.(bool)
			p.Type, p.Default = "bool", b
		case "color", "colour":
			p.Type, p.Default, p.MaxLength = "string", colourDefault(v.ID), 80
		case "select", "enum":
			for _, o := range v.Options {
				switch ov := o.(type) {
				case string:
					p.Options = append(p.Options, ov)
				case map[string]any:
					if s, ok := ov["value"].(string); ok {
						p.Options = append(p.Options, s)
					}
				}
			}
			s, _ := v.Default.(string)
			if s == "" && len(p.Options) > 0 {
				s = p.Options[0]
			}
			p.Type, p.Default = "enum", s
		default:
			probs = append(probs, fmt.Sprintf("the variable %q has the type %q: parts carry no media or addresses", v.ID, v.Type))
			continue
		}
		if p.Type == "number" && p.Min != nil && p.Max != nil && *p.Min > *p.Max {
			p.Min, p.Max = nil, nil
		}
		params = append(params, p)
		binds = append(binds, binding{id: v.ID, name: name})
	}
	return params, binds, probs
}

// bindText makes the elements a variable fills with data-var-text read their parameter: the text becomes the slot {{name}}.
func bindText(body string, binds []binding) string {
	if len(binds) == 0 {
		return body
	}
	names := map[string]string{}
	for _, b := range binds {
		names[b.id] = b.name
	}
	for from := 0; ; {
		loc := reVarText.FindStringSubmatchIndex(body[from:])
		if loc == nil {
			return body
		}
		for i := range loc {
			if loc[i] >= 0 {
				loc[i] += from
			}
		}
		tag, before, id, after := body[loc[2]:loc[3]], body[loc[4]:loc[5]], body[loc[6]:loc[7]], body[loc[8]:loc[9]]
		name, ok := names[id]
		closeAt := strings.Index(strings.ToLower(body[loc[1]:]), "</"+strings.ToLower(tag)+">")
		if !ok || closeAt < 0 {
			from = loc[1]
			continue
		}
		open := "<" + tag + before + after + ">"
		repl := open + "{{" + name + "}}"
		body = body[:loc[0]] + repl + body[loc[1]+closeAt:]
		from = loc[0] + len(repl)
	}
}

// rewriteCSS keeps the rules of a block off the page that holds it: html and body rules go (the page has its own), * and :root apply to
// its stage, the fonts become the design's, @font-face goes.
func rewriteCSS(css string) (string, []string) {
	var warns []string
	faces := 0
	fonts := map[string]bool{}
	out := rewriteRules(css, &faces, fonts)
	if faces > 0 {
		warns = append(warns, fmt.Sprintf("%d @font-face dropped (the design's fonts are used)", faces))
	}
	if len(fonts) > 0 {
		var names []string
		for f := range fonts {
			names = append(names, f)
		}
		sort.Strings(names)
		warns = append(warns, "fonts mapped to the design's: "+strings.Join(names, "; "))
	}
	return out, warns
}

func rewriteRules(css string, faces *int, fonts map[string]bool) string {
	var out strings.Builder
	for i := 0; i < len(css); {
		open := strings.IndexByte(css[i:], '{')
		if open < 0 {
			break
		}
		open += i
		depth, j := 1, open+1
		for j < len(css) && depth > 0 {
			switch css[j] {
			case '{':
				depth++
			case '}':
				depth--
			}
			j++
		}
		prelude, inner := strings.TrimSpace(css[i:open]), css[open+1:max(open+1, j-1)]
		i = j
		lp := strings.ToLower(prelude)
		switch {
		case strings.HasPrefix(lp, "@font-face"):
			*faces++
		case strings.HasPrefix(lp, "@media"), strings.HasPrefix(lp, "@supports"), strings.HasPrefix(lp, "@layer"):
			out.WriteString(prelude + "{" + rewriteRules(inner, faces, fonts) + "}\n")
		case strings.HasPrefix(lp, "@"):
			out.WriteString(prelude + "{" + inner + "}\n")
		default:
			var sels []string
			for _, s := range splitSelectors(prelude) {
				if s = stageSelector(s); s != "" {
					sels = append(sels, s)
				}
			}
			if len(sels) == 0 {
				continue
			}
			out.WriteString(strings.Join(sels, ", ") + "{" + mapFontFamilies(inner, fonts) + "}\n")
		}
	}
	return out.String()
}

// splitSelectors splits a selector list at the commas that are not inside parentheses or brackets.
func splitSelectors(s string) []string {
	var out []string
	depth, start := 0, 0
	for i, c := range s {
		switch c {
		case '(', '[':
			depth++
		case ')', ']':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, strings.TrimSpace(s[start:i]))
				start = i + 1
			}
		}
	}
	return append(out, strings.TrimSpace(s[start:]))
}

// stageSelector says what a selector of the block means inside the page: "" when it is about the page itself.
func stageSelector(s string) string {
	l := strings.ToLower(s)
	switch {
	case l == "html" || l == "body" || l == "html body" || l == "":
		return ""
	case l == "*":
		return ".hf-stage, .hf-stage *"
	case l == ":root":
		return ".hf-stage"
	case strings.HasPrefix(l, "html body "):
		return ".hf-stage " + strings.TrimSpace(s[len("html body "):])
	case strings.HasPrefix(l, "body ") || strings.HasPrefix(l, "html "):
		_, rest, _ := strings.Cut(s, " ")
		return ".hf-stage " + strings.TrimSpace(rest)
	}
	return s
}

// mapFontFamilies sets the font of every declaration to the design's display face (a heavy weight or a display family) or its body face.
func mapFontFamilies(block string, fonts map[string]bool) string {
	return mapFonts(block, fonts, reFontFamily)
}

// mapFonts is mapFontFamilies with the pattern that finds a declaration (inside a quoted attribute it must stop at the quote).
func mapFonts(block string, fonts map[string]bool, re *regexp.Regexp) string {
	heavy := false
	if m := reFontWeight.FindStringSubmatch(block); m != nil {
		w := strings.ToLower(m[1])
		n, _ := strconv.Atoi(w)
		heavy = w == "bold" || w == "bolder" || n >= 700
	}
	return re.ReplaceAllStringFunc(block, func(decl string) string {
		fam := strings.TrimSpace(re.FindStringSubmatch(decl)[1])
		if strings.HasPrefix(strings.ToLower(fam), "var(") {
			return decl
		}
		to := "var(--body)"
		if heavy || reHeavyFace.MatchString(fam) {
			to = "var(--display)"
		}
		fonts[strings.Trim(strings.SplitN(fam, ",", 2)[0], `"' `)+" -> "+to] = true
		return "font-family:" + to
	})
}

// mapInlineFonts does the same inside style="" attributes of the markup.
func mapInlineFonts(body string) (string, []string) {
	fonts := map[string]bool{}
	out := reStyleAttr.ReplaceAllStringFunc(body, func(attr string) string {
		if strings.HasPrefix(attr[strings.IndexAny(attr, `"'`):], `"`) {
			return mapFonts(attr, fonts, reFontInDQ)
		}
		return mapFonts(attr, fonts, reFontInSQ)
	})
	if len(fonts) == 0 {
		return out, nil
	}
	var names []string
	for f := range fonts {
		names = append(names, f)
	}
	sort.Strings(names)
	return out, []string{"inline fonts mapped to the design's: " + strings.Join(names, "; ")}
}
