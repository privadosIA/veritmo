package musicmotion

// page.go: the page of a scene of a piece, built from a part, the design and the two layers of text. The model
// does not write it: a part is a fragment that draws and animates from the beat clock (window.__beat), and this
// file wraps it with the colours of the design and with the lower layer (the verse that is being sung, its words
// appearing when they are sung, the keyword in the accent colour) and the upper one (a word hit at full screen).

import (
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"math"
	"regexp"
	"strings"
)

// Word is a sung word of a line, in the time of the scene. Heard says the time is the one of a word that was heard in the
// song and not one made up between its neighbours (or moved by hand): a part can show an unsure word differently.
type Word struct {
	Text  string  `json:"t"`
	Start float64 `json:"s"`
	End   float64 `json:"e"`
	Heard bool    `json:"heard"`
}

// Line is a verse of the scene. Keyword is the index of the word painted in the accent colour (-1 for none). A
// line whose words have no time (Words empty) is shown whole from Start, with Text.
type Line struct {
	Text    string  `json:"text"`
	Start   float64 `json:"start"`
	End     float64 `json:"end"`
	Words   []Word  `json:"words"`
	Keyword int     `json:"keyword"`
	// Groups split a long line into runs of words shown one at a time (see LineGroups); empty for a line shown whole.
	Groups []Group `json:"groups,omitempty"`
}

// Hit is a word shown at full screen from At for Length seconds.
type Hit struct {
	Word   string  `json:"word"`
	At     float64 `json:"at"`
	Length float64 `json:"length"`
}

// Scene is what a page is made from. Times are in the scene's own time (0 is its start).
type Scene struct {
	Part   Part
	Params map[string]any // already resolved (Part.ResolveParams)
	// Animate changes parameters of the main part over the scene (see animate.go).
	Animate  Animate
	Design   Design
	Mode     string
	Duration float64
	Width    int
	Height   int
	Lines    []Line
	Hit      *Hit
	// SongStart is where the piece starts in the song and SongLength how long the song is, in seconds: a part can draw the
	// position of the song and not only of its own scene (window.__scene.song_start, song_length). Zero when there is no song.
	SongStart  float64
	SongLength float64
	// Extras are parts drawn together with the main one, over it, in the order given (see MaxExtras).
	Extras []ScenePart
	// Layers are the items of the script's layers that show in this scene, in drawing order (see layers.go).
	Layers []SceneLayer
	// Backdrop is the SVG of the background of an illustrated piece, and Actors its characters (see stage.go).
	Backdrop string
	Actors   []Actor
	// Background, when set, makes the page one that goes over a video: it paints nothing over the whole frame (the design's
	// colour is not laid under it) and the scrim goes under its parts. Nil for a page of its own, which is what every piece had
	// before pieces could have a video behind them.
	Background *VideoBackground
}

// VideoBackground is what a page over a video needs to know about it.
type VideoBackground struct {
	// Scrim is how much of the design's --bg colour is laid over the video so the verse reads over it, from 0 to MaxScrim.
	Scrim float64
}

// Limits of the scrim of a video behind a piece.
const (
	DefaultScrim = 0.3
	MaxScrim     = 0.8
)

// transparentCSS is added to the page's own stylesheet for a page over a video: the page and its body paint nothing, and the
// scrim is the only thing the page lays over the video itself.
func transparentCSS(b VideoBackground) string {
	return fmt.Sprintf("\nhtml,body{background:transparent}\n#scrim{position:absolute;left:0;top:0;width:100%%;height:100%%;background:color-mix(in srgb,var(--bg) %g%%,transparent)}\n", math.Round(b.Scrim*1000)/10) + verseHaloCSS
}

// verseHalo is the shadow of the design's background colour a word of the verse has over a video: a tight one that gives the glyphs an
// edge and a soft one that darkens (or lightens, in a light mode) what is right behind them. It never changes a word's colour.
const verseHalo = "0 0 .08em color-mix(in srgb,var(--bg) 90%,transparent),0 0 .3em color-mix(in srgb,var(--bg) 70%,transparent)"

// verseHaloCSS gives every word of the verse the halo, whatever the verse style. The glow style has shadows of its own, so it keeps
// them and the halo goes after them.
const verseHaloCSS = "#verse .w{text-shadow:" + verseHalo + "}\n" +
	"#verse.verse-glow .w{text-shadow:0 0 .1em var(--fg),0 0 .45em color-mix(in srgb,var(--fg) 75%,transparent)," + verseHalo + "}\n" +
	"#verse.verse-glow .w.k{text-shadow:0 0 .1em var(--accent),0 0 .45em color-mix(in srgb,var(--accent) 75%,transparent)," + verseHalo + "}\n"

// MaxExtras is how many parts a piece may carry besides its main one.
const MaxExtras = 3

// ScenePart is a part of a scene with its parameters, already resolved.
type ScenePart struct {
	Part    Part
	Params  map[string]any
	Animate Animate
}

var reSlot = regexp.MustCompile(`\{\{\s*(json:)?([a-z][a-z0-9_]*)\s*\}\}`)

// fill replaces {{name}} with the parameter as text safe in HTML and {{json:name}} with it as a JSON value that is
// safe inside a script.
func fill(frag string, params map[string]any) (string, error) {
	var bad error
	out := reSlot.ReplaceAllStringFunc(frag, func(m string) string {
		sub := reSlot.FindStringSubmatch(m)
		v, ok := params[sub[2]]
		if !ok {
			bad = fmt.Errorf("the fragment uses {{%s}}, which is not a parameter of the part", sub[2])
			return ""
		}
		if sub[1] != "" {
			raw, _ := json.Marshal(v)
			return scriptSafe(string(raw))
		}
		return html.EscapeString(fmt.Sprint(v))
	})
	return out, bad
}

// scriptSafe keeps JSON from closing the script it sits in.
func scriptSafe(s string) string {
	s = strings.ReplaceAll(s, "<", `<`)
	s = strings.ReplaceAll(s, " ", ` `)
	return strings.ReplaceAll(s, " ", ` `)
}

// Check says what is wrong with a scene before a page is made of it.
func (s Scene) Check() error {
	if s.Duration <= 0 {
		return errors.New("the scene has no duration")
	}
	if s.Width <= 0 || s.Height <= 0 {
		return errors.New("the scene needs a width and a height")
	}
	if len(s.Extras) > MaxExtras {
		return fmt.Errorf("a piece may carry up to %d extra parts, it has %d", MaxExtras, len(s.Extras))
	}
	seen := map[string]bool{s.Part.Name: true}
	for _, e := range s.Extras {
		if seen[e.Part.Name] {
			return fmt.Errorf("the part %q is in the piece twice: two fragments of one part would share their element ids", e.Part.Name)
		}
		seen[e.Part.Name] = true
	}
	if b := s.Background; b != nil && (b.Scrim < 0 || b.Scrim > MaxScrim) {
		return fmt.Errorf("the scrim is %v: it goes from 0 to %v", b.Scrim, MaxScrim)
	}
	if s.Mode != ModeDark && s.Mode != ModeLight {
		return fmt.Errorf("the mode %q is neither %q nor %q", s.Mode, ModeDark, ModeLight)
	}
	if err := s.Design.Validate(); err != nil {
		return err
	}
	for i, l := range s.Lines {
		if l.Keyword >= len(l.Words) && len(l.Words) > 0 {
			return fmt.Errorf("line %d: the keyword is word %d but the line has %d words", i+1, l.Keyword+1, len(l.Words))
		}
	}
	if s.Hit != nil {
		if strings.TrimSpace(s.Hit.Word) == "" || s.Hit.Length <= 0 || s.Hit.At < 0 || s.Hit.At >= s.Duration {
			return errors.New("the word hit needs a word, a length above 0 and a start inside the scene")
		}
		if strings.ContainsAny(strings.TrimSpace(s.Hit.Word), " \t\n") {
			return errors.New("the word hit must be one word")
		}
	}
	return nil
}

const pageCSS = `
html,body{margin:0;padding:0;width:%dpx;height:%dpx;overflow:hidden;background:var(--bg);color:var(--fg);font-family:var(--body)}
#stage{position:absolute;left:0;top:0;width:100%%;height:100%%;overflow:hidden}
#backdrop{position:absolute;left:0;top:0;width:100%%;height:100%%}
#backdrop svg{width:100%%;height:100%%;display:block}
#actors{position:absolute;left:0;top:0;width:100%%;height:100%%;overflow:hidden;z-index:3}
#actors .actor{position:absolute;transform-origin:50%% 100%%}
#actors .actor svg{display:block;width:100%%;height:100%%;overflow:visible}
#verse{position:absolute;left:7%%;right:7%%;bottom:6%%;text-align:center;font-family:var(--body);font-weight:700;font-size:%dpx;line-height:1.2;z-index:5}
#verse .w{display:inline-block;margin:0 .14em;opacity:0}
#verse .k{color:var(--accent)}
#verse.verse-highlight .w.sung{background:var(--accent);color:var(--bg);border-radius:.18em;padding:0 .18em}
#verse.verse-karaoke .w.sung{color:var(--accent)}
#verse.verse-glow .w{color:var(--fg);text-shadow:0 0 .1em var(--fg),0 0 .45em color-mix(in srgb,var(--fg) 75%%,transparent)}
#verse.verse-glow .w.k{color:var(--accent);text-shadow:0 0 .1em var(--accent),0 0 .45em color-mix(in srgb,var(--accent) 75%%,transparent)}
#hitbg{position:absolute;left:0;top:0;width:100%%;height:100%%;background:var(--bg);opacity:0;visibility:hidden;z-index:8}
#hit.hit-metal .ch,#hit.hit-glow .ch{display:inline-block;opacity:0}
#hit.hit-metal .ch{background-image:linear-gradient(180deg,#f4f6fa 0%%,#9aa1ad 48%%,#e3e7ee 52%%,#7b818c 100%%);-webkit-background-clip:text;background-clip:text;color:transparent;-webkit-text-fill-color:transparent}
#hit.hit-glow .ch{color:var(--accent);text-shadow:0 0 .12em var(--accent),0 0 .35em color-mix(in srgb,var(--accent) 65%%,transparent)}
#hit{position:absolute;left:3%%;right:3%%;top:0;bottom:0;display:flex;align-items:center;justify-content:center;white-space:nowrap;text-align:center;font-family:var(--display);font-weight:900;text-transform:uppercase;letter-spacing:-.01em;color:var(--fg);z-index:9;visibility:hidden}
`

// layerScript runs the two layers. window.__scene holds the lines and the hit; window.__partSeek(t, beat) is the
// part's own drawing, called first.
const layerScript = `(function(){
  var S = window.__scene, verse = document.getElementById('verse'), hit = document.getElementById('hit'), hitbg = document.getElementById('hitbg');
  var lineEls = [];
  function el(tag, cls, text){ var e = document.createElement(tag); if (cls) e.className = cls; if (text != null) e.textContent = text; return e; }
  if (verse) S.lines.forEach(function(l){
    var box = el('div', 'line'); box.style.display = 'none';
    box._w = [];
    if (l.words && l.words.length) {
      if (l.groups && l.groups.length) {
        // a long line: its words in groups, one group on the page at a time (opened and removed at the times the page worked out)
        box._g = [];
        l.groups.forEach(function(g){
          var ge = el('span', 'group'); ge.style.display = 'none';
          for (var i = g.from; i < g.to; i++) { var s = el('span', 'w' + (i === l.keyword ? ' k' : ''), l.words[i].t); ge.appendChild(s); box._w.push(s); }
          box.appendChild(ge); box._g.push(ge);
        });
      } else l.words.forEach(function(w, i){ var s = el('span', 'w' + (i === l.keyword ? ' k' : ''), w.t); box.appendChild(s); box._w.push(s); });
    } else { var s = el('span', 'w' + (l.keyword === 0 ? ' k' : ''), l.text); s.style.opacity = '0'; box.appendChild(s); box._w.push(s); }
    verse.appendChild(box); lineEls.push(box);
  });
  // In the metal and glow looks the word is made of letters that enter one after another; plain stays one piece of text.
  var letters = null;
  if (S.hit && hit && (S.hit_style === 'metal' || S.hit_style === 'glow')) {
    hit.className = 'hit-' + S.hit_style; hit.textContent = ''; letters = [];
    Array.from(S.hit.word).forEach(function(c){ var sp = el('span', 'ch', c); hit.appendChild(sp); letters.push(sp); });
  }
  // The hit is 60 % of the height of the frame, or smaller so that its whole width is 90 % of the frame's: measured on the text itself
  // (a range over its content, whatever overflows a centred box), once the fonts are loaded, in the one-piece and the per-letter looks.
  function fitHit(){
    if (!(S.hit && hit)) return;
    // measured at rest: the entrance scales the word and its letters, and a width taken in the middle of it would be of the scaled word
    var size = window.innerHeight * 0.6, keep = hit.style.transform, kept = (letters || []).map(function(c){ return c.style.transform; });
    hit.style.fontSize = size + 'px'; hit.style.transform = 'none'; (letters || []).forEach(function(c){ c.style.transform = 'none'; });
    var range = document.createRange(); range.selectNodeContents(hit);
    var w = range.getBoundingClientRect().width, avail = window.innerWidth * 0.9;
    if (w > avail) hit.style.fontSize = Math.floor(size * avail / w) + 'px';
    hit.style.transform = keep; (letters || []).forEach(function(c, i){ c.style.transform = kept[i]; });
  }
  // The display face may not be in when the page is built (the measure would be of the fallback face and never run again), so the face is
  // requested at once, the hit is fitted again on every seek until the document reports its fonts loaded, and once more after that.
  var fitted = false;
  function refit(){
    if (!(S.hit && hit)) return;
    if (document.fonts && document.fonts.status !== 'loaded') { fitHit(); fitted = false; }
    else if (!fitted) { fitHit(); fitted = true; }
  }
  if (S.hit && hit) {
    if (!letters) hit.textContent = S.hit.word;
    try { if (document.fonts && document.fonts.load) document.fonts.load('900 10px ' + getComputedStyle(hit).fontFamily, S.hit.word).then(refit, function(){}); } catch (e) {}
    fitHit(); if (document.fonts && document.fonts.ready) document.fonts.ready.then(fitHit);
  }
  function ease(p){ p = Math.min(1, Math.max(0, p)); return 1 - Math.pow(1 - p, 3); }
  function layers(t){
    refit();
    var cur = -1;
    for (var i = 0; i < S.lines.length; i++) { if (t >= S.lines[i].start - 1e-9) cur = i; }
    lineEls.forEach(function(box, i){
      var show = i === cur; box.style.display = show ? 'block' : 'none';
      if (!show) return;
      var l = S.lines[i], spans = box._w;
      if (box._g) for (var gi = 0; gi < box._g.length; gi++) box._g[gi].style.display = (t >= l.groups[gi].open - 1e-9 && t < l.groups[gi].close) ? '' : 'none';
      if (l.words && l.words.length) {
        for (var k = 0; k < spans.length; k++) {
          var p = ease((t - l.words[k].s) / 0.12); spans[k].style.opacity = String(p); spans[k].style.transform = 'translateY(' + ((1 - p) * 14) + 'px)';
          // sung: its time has come; now: it is the word being sung (until the next one starts)
          var sung = t >= l.words[k].s - 1e-9, now = sung && (k === spans.length - 1 || t < l.words[k + 1].s);
          spans[k].classList.toggle('sung', sung); spans[k].classList.toggle('now', now);
        }
      } else { var q = ease((t - l.start) / 0.2); spans[0].style.opacity = String(q); spans[0].classList.toggle('sung', t >= l.start - 1e-9); spans[0].classList.toggle('now', t >= l.start - 1e-9); }
    });
    if (S.hit && hit) {
      var on = t >= S.hit.at - 1e-9 && t < S.hit.at + S.hit.length;
      hit.style.visibility = on ? 'visible' : 'hidden'; hitbg.style.visibility = on ? 'visible' : 'hidden';
      if (on) {
        var a = ease((t - S.hit.at) / 0.15), out = Math.min(1, Math.max(0, (S.hit.at + S.hit.length - t) / 0.1));
        hit.style.opacity = String(Math.min(a, out)); hit.style.transform = 'scale(' + (0.85 + 0.15 * a) + ')'; hitbg.style.opacity = String(0.6 * Math.min(a, out));
        if (letters) {
          var n = letters.length, step = Math.min(0.4, S.hit.length * 0.4) / n, prog = (t - S.hit.at) / S.hit.length;
          letters.forEach(function(ch, i){
            var p = ease((t - S.hit.at - i * step) / 0.12);
            ch.style.opacity = String(p); ch.style.transform = 'translateY(' + ((1 - p) * 0.25) + 'em) scale(' + (0.8 + 0.2 * p) + ')';
            if (S.hit_style === 'metal') { var shine = Math.max(0, 1 - Math.abs(prog * (n + 2) - (i + 1))); ch.style.filter = 'brightness(' + (1 + 0.7 * shine) + ')'; }
          });
        }
      }
    }
  }
  // sung says which word and which line are being sung at t, in the time of the scene: the word is {text, index (in its line),
  // line (in window.__scene.lines), start, end, position (0 at its start, 1 at its end)} and the line {index, text, start, end,
  // position}; each is null when none is being sung (an instrumental break, or the silence between two words).
  function sung(t){
    var out = {word: null, line: null};
    for (var i = 0; i < S.lines.length; i++) {
      var l = S.lines[i];
      if (!(t >= l.start - 1e-9 && t < l.end)) continue;
      out.line = {index: i, text: l.text, start: l.start, end: l.end, position: l.end > l.start ? (t - l.start) / (l.end - l.start) : 0};
      var ws = l.words || [];
      for (var k = 0; k < ws.length; k++) {
        if (t >= ws[k].s - 1e-9 && t < ws[k].e) { out.word = {text: ws[k].t, index: k, line: i, start: ws[k].s, end: ws[k].e, position: ws[k].e > ws[k].s ? (t - ws[k].s) / (ws[k].e - ws[k].s) : 0}; break; }
      }
      break;
    }
    return out;
  }
  window.__seek = function(t){
    var base = window.__beat || {number: 0, position: 0, section: '', sectionIndex: -1, bpm: 0, time: t}, now = sung(t), beat = {};
    for (var key in base) beat[key] = base[key];
    beat.word = now.word; beat.line = now.line;
    if (typeof window.__stageSeek === 'function') window.__stageSeek(t, beat);
    var seeks = window.__partSeeks || [];
    for (var i = 0; i < seeks.length; i++) seeks[i](t, beat);
    layers(t);
    if (typeof window.__afterSeek === 'function') window.__afterSeek(t, beat); // after the page's own layers: the layout repair of a scene measures here
    return Promise.all((window.__pendingSeeks || []).splice(0));
  };
  window.__seek(0);
})();`

// animationScript is added to the page, before the layer script, only when a part animates.
const animationScript = `(function(){` + animateValue + `
  window.__setAnimated = function(t){` + animateSeek + `
  };
})();`

// stageScript places the actors and moves them from the beat clock. window.__scene.actors holds where each goes.
const stageScript = `(function(){
  var S = window.__scene, W = window.innerWidth, H = window.innerHeight, els = [];
  (S.actors || []).forEach(function(a){
    var el = document.getElementById('actor-' + a.id), h = a.size / 100 * H, w = h * a.aspect;
    el.style.height = h + 'px'; el.style.width = w + 'px';
    el.style.left = (a.x / 100 * W) + 'px'; el.style.top = (a.y / 100 * H) + 'px';
    els.push({el: el, a: a, w: w, h: h});
  });
  function ease(p){ p = Math.min(1, Math.max(0, p)); return 1 - Math.pow(1 - p, 3); }
  window.__stageSeek = function(t, b){
    var n = b.number < 0 ? 0 : b.number, pos = b.number < 0 ? 0 : b.position, period = b.bpm > 0 ? 60 / b.bpm : 0.5, D = S.duration;
    els.forEach(function(o){
      var a = o.a, dx = 0, dy = 0, rot = 0, sx = 1, sy = 1;
      var side = a.x < 50 ? -1 : 1, away = side < 0 ? (a.x / 100 * W + o.w) : ((100 - a.x) / 100 * W + o.w);
      if (a.action === 'enter') { dx = side * away * (1 - ease(t / Math.max(0.3, period))); }
      else if (a.action === 'exit') { var d = Math.max(0.5, period), p = (t - (D - d)) / d; dx = side * away * (p <= 0 ? 0 : ease(p) ); }
      else if (a.action === 'bounce') { dy = -o.h * 0.07 * Math.sin(Math.PI * pos); sy = 1 + 0.04 * Math.sin(Math.PI * pos); sx = 1 - 0.02 * Math.sin(Math.PI * pos); }
      else if (a.action === 'look') { rot = 6 * Math.sin(Math.PI * (n + pos)); }
      else if (a.action === 'wave') { rot = 7 * Math.sin(4 * Math.PI * (n + pos)); }
      else if (a.action === 'slide') { dx = 0.10 * W * Math.sin(Math.PI * (n + pos) / 2); }
      o.el.style.transform = 'translate(' + (-a.anchor_x * 100) + '%, ' + (-a.anchor_y * 100) + '%) translate(' + dx + 'px,' + dy + 'px) rotate(' + rot + 'deg) scale(' + sx + ',' + sy + ')';
      o.el.style.transformOrigin = (a.anchor_x * 100) + '% ' + (a.anchor_y * 100) + '%';
    });
  };
})();`

// partBlock is the fragment of a part as it goes in the page: before it a script gives it its own parameters (it reads
// them while it loads), after it a script moves the seek function it defined into the list the layer script runs in
// order, and clears it for the next fragment.
func partBlock(p Part, params map[string]any, index int, moves bool, slot *layerSlot) (string, error) {
	frag, err := fill(p.Fragment, params)
	if err != nil {
		return "", fmt.Errorf("part %s: %w", p.Name, err)
	}
	pj, _ := json.Marshal(params)
	// A part that animates hands its own parameters object to the page, by its place, so the page can move them.
	hand := ""
	if moves {
		hand = fmt.Sprintf("window.__partParams=window.__partParams||{};window.__partParams[%d]=window.__params;", index)
	}
	if slot != nil {
		// A layer item registers its seek apart: the page calls it with the item's own time and only inside its bounds.
		hf := ""
		if usesTimelines(p.Fragment) {
			hf = ",hf:window.__hf.claim({tls:[],clips:[]})"
		}
		return "<script>window.__params=" + scriptSafe(string(pj)) + ";</script>" + frag +
			fmt.Sprintf("<script>(function(){%swindow.__layers=window.__layers||[];window.__layers.push({el:document.getElementById('layer-%d'),from:%v,to:%v,seek:typeof window.__partSeek==='function'?window.__partSeek:null%s});window.__partSeek=null;})();</script>", hand, slot.N, slot.From, slot.To, hf), nil
	}
	if usesTimelines(p.Fragment) {
		hand += "window.__hf.claim(window.__hf.main);"
	}
	return "<script>window.__params=" + scriptSafe(string(pj)) + ";</script>" + frag +
		"<script>(function(){" + hand + "window.__partSeeks=window.__partSeeks||[];if(typeof window.__partSeek==='function'){window.__partSeeks.push(window.__partSeek);}window.__partSeek=null;})();</script>", nil
}

// BuildPage makes the page of a scene: a complete document with nothing loaded from outside.
func BuildPage(s Scene) (string, error) {
	if err := s.Check(); err != nil {
		return "", err
	}
	s = s.withAssets()
	hfUse, gsapUse := s.timelineUse()
	var gsapJS string
	if gsapUse {
		var gerr error
		if gsapJS, gerr = gsapSource(); gerr != nil {
			return "", fmt.Errorf("a part uses GSAP: %w", gerr)
		}
	}
	stage, err := partBlock(s.Part, s.Params, 0, len(s.Animate) > 0, nil)
	if err != nil {
		return "", err
	}
	moving := []animated{{Part: s.Part, Animate: s.Animate}}
	for i, e := range s.Extras {
		block, err := partBlock(e.Part, e.Params, i+1, len(e.Animate) > 0, nil)
		if err != nil {
			return "", err
		}
		stage += block
		moving = append(moving, animated{Part: e.Part, Animate: e.Animate})
	}
	// The items of the layers go after the extras, each in a wrapper the page's seek shows between its bounds.
	var layerBlocks strings.Builder
	for i, ly := range s.Layers {
		idx := 1 + len(s.Extras) + i
		if ly.Block != nil {
			fmt.Fprintf(&layerBlocks, "<div class=\"layer\" id=\"layer-%d\" style=\"display:none\">%s</div>", i, blockLayerHTML(ly.Block, layerSlot{N: i, From: ly.From, To: ly.To}, s.Width, s.Height))
			continue
		}
		block, err := partBlock(ly.Part, ly.Params, idx, len(ly.Animate) > 0, &layerSlot{N: i, From: ly.From, To: ly.To})
		if err != nil {
			return "", err
		}
		from := ly.From
		fmt.Fprintf(&layerBlocks, "<div class=\"layer\" id=\"layer-%d\" style=\"display:none\">%s</div>", i, block)
		moving = append(moving, animated{Part: ly.Part, Animate: ly.Animate, Off: &from})
	}
	stage += layerBlocks.String()
	animation := animateScript(moving)
	bg, fg, accent := s.Design.Colours(s.Mode)
	lines := s.Lines
	if lines == nil {
		lines = []Line{}
	}
	actors := s.Actors
	if actors == nil {
		actors = []Actor{}
	}
	theme := map[string]any{"assets": map[string]string{}, "hud": HUD{}}
	if t := s.Design.Theme; t != nil {
		if t.Assets != nil {
			theme["assets"] = t.Assets
		}
		if t.HUD != nil {
			theme["hud"] = *t.HUD
		}
	}
	themeJSON, _ := json.Marshal(theme)
	scene := map[string]any{"duration": s.Duration, "lines": lines, "hit": s.Hit, "mode": s.Mode, "actors": actors, "hit_style": s.Design.HitStyle, "song_start": s.SongStart, "song_length": s.SongLength}
	sceneJSON, _ := json.Marshal(scene)
	paramsJSON, _ := json.Marshal(s.Params)
	vars := fmt.Sprintf(":root{--bg:%s;--fg:%s;--accent:%s;--dark:%s;--light:%s;--display:%s;--body:%s}",
		bg, fg, accent, s.Design.Dark, s.Design.Light, Fonts[s.Design.Display], Fonts[s.Design.Body])
	size := s.Height * 62 / 1000
	var b strings.Builder
	b.WriteString("<!doctype html><html><head><meta charset=\"utf-8\"><style>")
	b.WriteString(vars)
	fmt.Fprintf(&b, pageCSS, s.Width, s.Height, size)
	if l := s.Design.VerseLayout; !l.IsDefault() {
		b.WriteString(verseLayoutCSS(*l, size))
	}
	if s.Background != nil {
		b.WriteString(transparentCSS(*s.Background))
	}
	b.WriteString(FontFaceCSS(s.Design.Display, s.Design.Body))
	b.WriteString("</style>")
	if t := s.Design.Theme; t != nil && t.CSS != "" {
		b.WriteString("<style data-theme>" + t.CSS + "</style>") // after the page's own stylesheet, so it wins over its defaults
	}
	b.WriteString("</head><body class=\"mode-" + s.Mode + "\">")
	if s.Background != nil && s.Background.Scrim > 0 {
		b.WriteString("<div id=\"scrim\"></div>") // first in the page: everything else is drawn over it
	}
	// The data goes first: the script of the part runs while the page loads and may read it.
	b.WriteString("<script>window.__scene=" + scriptSafe(string(sceneJSON)) + ";window.__theme=" + scriptSafe(string(themeJSON)) + ";window.__params=" + scriptSafe(string(paramsJSON)) + ";</script>")
	if hfUse {
		// The library first when a part needs it (the fragments build their timelines while the page loads), then the runtime that
		// collects what each fragment registers and seeks it.
		if gsapUse {
			b.WriteString("<script>" + gsapJS + "</script>")
		}
		b.WriteString("<style>" + timelineCSS + "</style><script>" + timelineRuntime + "</script>")
	}
	if s.Backdrop != "" {
		b.WriteString("<div id=\"backdrop\">" + s.Backdrop + "</div>")
	}
	b.WriteString("<div id=\"stage\">" + stage + "<script>window.__params=" + scriptSafe(string(paramsJSON)) + ";</script></div>")
	if len(s.Actors) > 0 {
		b.WriteString("<style>")
		for _, a := range s.Actors {
			fmt.Fprintf(&b, "#actor-%s .expr{display:none !important}#actor-%s .expr-%s{display:inline !important}", a.ID, a.ID, a.Expression)
		}
		b.WriteString("</style><div id=\"actors\">")
		for _, a := range s.Actors {
			b.WriteString("<div class=\"actor " + a.Scope + "\" id=\"actor-" + a.ID + "\">" + a.SVG + "</div>")
		}
		b.WriteString("</div>")
	}
	verseClass := ""
	if s.Design.VerseStyle == VerseStyleHighlight || s.Design.VerseStyle == VerseStyleKaraoke || s.Design.VerseStyle == VerseStyleGlow {
		verseClass = " class=\"verse-" + s.Design.VerseStyle + "\""
	}
	// A main part that draws the lyric (verse: own) or the word hit (hit: own) leaves the page's own layer out.
	if !s.Part.OwnsVerse() {
		b.WriteString("<div id=\"verse\"" + verseClass + "></div>")
	}
	if !s.Part.OwnsHit() {
		b.WriteString("<div id=\"hitbg\"></div><div id=\"hit\"></div>")
	}
	b.WriteString("<script>" + stageScript + "</script>")
	layers := layerScript
	seekLayers := layerSeek
	if hfUse {
		// After each part's own seek: its timelines and clips, at the part's time (an item of a layer, at the seconds from its start).
		layers = strings.Replace(layers, "for (var i = 0; i < seeks.length; i++) seeks[i](t, beat);", "for (var i = 0; i < seeks.length; i++) seeks[i](t, beat);\n    window.__hf.seek(window.__hf.main, t);", 1)
		seekLayers = strings.Replace(seekLayers, "if (on && LY[li].seek) LY[li].seek(abs - LY[li].from, beat);", "if (on && LY[li].seek) LY[li].seek(abs - LY[li].from, beat);\n      if (on && LY[li].hf) window.__hf.seek(LY[li].hf, abs - LY[li].from);", 1)
	}
	if len(s.Layers) > 0 {
		layers = strings.Replace(layers, "for (var i = 0; i < seeks.length; i++) seeks[i](t, beat);", "for (var i = 0; i < seeks.length; i++) seeks[i](t, beat);"+seekLayers, 1)
	}
	if animation != "" {
		// Only a page that animates carries the evaluator and the call in its seek; any other page is what it always was.
		b.WriteString(animation + "<script>" + animationScript + "</script>")
		layers = strings.Replace(layers, "window.__seek = function(t){\n", "window.__seek = function(t){\n    window.__setAnimated(t);\n", 1)
	}
	b.WriteString("<script>" + layers + "</script></body></html>")
	return b.String(), nil
}

// withAssets is the scene with the theme's drawings put in the {{asset:name}} slots of its parts' fragments (the scene's own
// parts are not changed).
func (s Scene) withAssets() Scene {
	t := s.Design.Theme
	s.Part.Fragment = fillAssets(s.Part.Fragment, t)
	extras := make([]ScenePart, len(s.Extras))
	for i, e := range s.Extras {
		e.Part.Fragment = fillAssets(e.Part.Fragment, t)
		extras[i] = e
	}
	s.Extras = extras
	layers := make([]SceneLayer, len(s.Layers))
	for i, l := range s.Layers {
		l.Part.Fragment = fillAssets(l.Part.Fragment, t)
		layers[i] = l
	}
	s.Layers = layers
	return s
}

// verseLayoutCSS is added to the page's own stylesheet when the design places the verse somewhere other than the bottom, in another size or
// aligned otherwise: it overrides the default of #verse and nothing else, so a design with no layout makes the page it always made. size is the
// font size the frame gives (in pixels), which the scale multiplies.
func verseLayoutCSS(l VerseLayout, size int) string {
	var b strings.Builder
	b.WriteString("\n")
	switch l.Position {
	case VersePositionTop:
		b.WriteString("#verse{top:6%;bottom:auto}\n")
	case VersePositionCenter:
		b.WriteString("#verse{top:50%;bottom:auto;transform:translateY(-50%)}\n")
	}
	if l.Scale != 0 && l.Scale != 1 {
		fmt.Fprintf(&b, "#verse{font-size:%dpx}\n", int(math.Round(float64(size)*l.Scale)))
	}
	if l.Align != "" && l.Align != VerseAlignCenter {
		fmt.Fprintf(&b, "#verse{text-align:%s}\n", l.Align)
	}
	return b.String()
}
