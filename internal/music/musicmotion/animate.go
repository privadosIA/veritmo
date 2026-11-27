package musicmotion

// animate.go: parameters that change over a piece (spec music-motion, "A parameter can change over a piece"). A script
// piece and each of its extras may carry animate, a map from a parameter name to keyframes; the page evaluates them on
// every frame and writes the result into the part's own parameters before it calls the part.

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Features are what this server supports beyond the base script format; the catalog lists them. "svg" and "plan" are not
// script features: they say the server can write an SVG from a description (POST /api/assist/svg) and a plan from one
// sentence (POST /api/assist/plan). "word-timing" says the words can be timed on the separated voice (`separate` on the piece
// and its transcribe route), carry heard and confidence, and can be moved by hand (PATCH .../words).
var Features = []string{"animate", "layers", "svg", "plan", "dataset", "blender", "blender-motion", "blender-models", "examples", "scene", "theme", "review", "reference-breakdown", "word-timing", "video-background", "video-track", "verse-legibility"}

// Eases are the names a keyframe may give for how the value travels towards the next one.
var Eases = []string{"linear", "in", "out", "in-out", "step"}

// Keyframe is a value at a second of the piece (0 = its start).
type Keyframe struct {
	T     float64 `json:"t"`
	Value any     `json:"value"`
	// Ease is how the value travels from this keyframe to the next; empty means linear.
	Ease string `json:"ease,omitempty"`
}

// Animate maps a parameter name to its keyframes.
type Animate map[string][]Keyframe

func validEase(e string) bool {
	if e == "" {
		return true
	}
	for _, x := range Eases {
		if x == e {
			return true
		}
	}
	return false
}

// checkAnimate checks the animate of a part of a piece (n names the piece): every parameter must exist and be
// animatable, with at least one keyframe, t inside the piece and strictly increasing, a known ease and a value the
// parameter would take as a static value.
func (p Part) checkAnimate(n string, a Animate, duration float64) error {
	names := make([]string, 0, len(a))
	for k := range a {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, pn := range names {
		var pr *Param
		for i := range p.Params {
			if p.Params[i].Name == pn {
				pr = &p.Params[i]
			}
		}
		if pr == nil {
			return fmt.Errorf("%s: the part %q has no parameter %q", n, p.Name, pn)
		}
		if !pr.Animatable {
			return fmt.Errorf("%s: the parameter %q of %q cannot be animated", n, pn, p.Name)
		}
		kfs := a[pn]
		if len(kfs) == 0 {
			return fmt.Errorf("%s: the animation of %q has no keyframe", n, pn)
		}
		for i, k := range kfs {
			if k.T < 0 || k.T > duration+coverTolerance {
				return fmt.Errorf("%s: keyframe #%d of %q is at %.2f s, outside the piece (0 s to %.2f s)", n, i+1, pn, k.T, duration)
			}
			if i > 0 && k.T <= kfs[i-1].T {
				return fmt.Errorf("%s: keyframe #%d of %q is not after the one before it (%.2f s)", n, i+1, pn, kfs[i-1].T)
			}
			if !validEase(k.Ease) {
				return fmt.Errorf("%s: keyframe #%d of %q has the ease %q; use %s", n, i+1, pn, k.Ease, strings.Join(Eases, ", "))
			}
			if _, err := checkParam(*pr, k.Value); err != nil {
				return fmt.Errorf("%s: keyframe #%d of %q %w", n, i+1, pn, err)
			}
		}
	}
	return nil
}

// animated is a part of a page with what moves its parameters.
type animated struct {
	Part    Part
	Animate Animate
	// Off is, for a layer item, the song second its own time starts from: its keyframes are read at the song time minus Off.
	Off *float64
}

// animateScript is the data the page needs: for each part, in the order of the page, its keyframes (null when none).
// It is empty when no part animates, so a page without animation is the page it always was.
func animateScript(parts []animated) string {
	moves := false
	for _, a := range parts {
		if len(a.Animate) > 0 {
			moves = true
		}
	}
	if !moves {
		return ""
	}
	type track struct {
		K   []Keyframe `json:"k"`
		Int bool       `json:"int,omitempty"`
	}
	list := make([]any, len(parts))
	for i, a := range parts {
		if len(a.Animate) == 0 {
			continue
		}
		m := map[string]track{}
		for name, kfs := range a.Animate {
			tr := track{K: kfs}
			for _, pr := range a.Part.Params {
				if pr.Name == name && pr.Type == "int" {
					tr.Int = true
				}
			}
			m[name] = tr
		}
		list[i] = m
	}
	raw, _ := json.Marshal(list)
	out := "<script>window.__animate=" + scriptSafe(string(raw)) + ";</script>"
	offs := map[string]float64{}
	for i, a := range parts {
		if a.Off != nil && len(a.Animate) > 0 {
			offs[fmt.Sprint(i)] = *a.Off
		}
	}
	if len(offs) > 0 {
		o, _ := json.Marshal(offs)
		out += "<script>window.__animateOff=" + scriptSafe(string(o)) + ";</script>"
	}
	return out
}

// animateSeek is the part of the page's seek that sets the animated parameters: window.__partParams[i] is the
// object the i-th part holds (it reads it inside __partSeek), window.__animate[i] its keyframes.
const animateSeek = `
    var AN = window.__animate, PP = window.__partParams || [], AO = window.__animateOff || {};
    if (AN) for (var ai = 0; ai < AN.length; ai++) { if (AN[ai] && PP[ai]) { var at = AO[ai] === undefined ? t : ((window.__scene && window.__scene.song_start) || 0) + t - AO[ai]; for (var pn in AN[ai]) { var v = window.__animateValue(AN[ai][pn].k, at); PP[ai][pn] = (AN[ai][pn].int && typeof v === 'number') ? Math.round(v) : v; } } }`

// animateValue evaluates keyframes at t. Numbers are interpolated with the ease of the earlier keyframe; any other
// value, and every value under step, is held; before the first and after the last keyframe it is the nearest one.
const animateValue = `
  window.__animateValue = function(kfs, t){
    var n = kfs.length;
    if (t <= kfs[0].t) return kfs[0].value;
    if (t >= kfs[n - 1].t) return kfs[n - 1].value;
    var i = 0; while (i < n - 1 && t >= kfs[i + 1].t) i++;
    var a = kfs[i], b = kfs[i + 1], e = a.ease || 'linear';
    if (typeof a.value !== 'number' || typeof b.value !== 'number' || e === 'step') return a.value;
    var p = (t - a.t) / (b.t - a.t);
    if (e === 'in') p = p * p * p; else if (e === 'out') p = 1 - Math.pow(1 - p, 3);
    else if (e === 'in-out') p = p < 0.5 ? 4 * p * p * p : 1 - Math.pow(-2 * p + 2, 3) / 2;
    return a.value + (b.value - a.value) * p;
  };`
