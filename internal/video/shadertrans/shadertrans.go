// Package shadertrans draws the shader transitions of HyperFrames (@hyperframes/shader-transitions, Apache-2.0) between two clips: the last
// seconds of one and the first of the next go to a page with a WebGL canvas, a fragment shader mixes them frame by frame, and the frames are
// encoded to a video that takes the place of the crossfade of the join (see spec shader-transitions).
package shadertrans

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"

	"veritmo/internal/video/motion"
)

//go:embed shaders.json
var shadersJSON []byte

var (
	loadOnce sync.Once
	vertex   string
	frags    map[string]string
)

func load() {
	loadOnce.Do(func() {
		var d struct {
			Vertex  string            `json:"vertex"`
			Shaders map[string]string `json:"shaders"`
		}
		if err := json.Unmarshal(shadersJSON, &d); err != nil {
			panic("shadertrans: shaders.json: " + err.Error())
		}
		vertex, frags = d.Vertex, d.Shaders
	})
}

// Prefix starts the name of a transition kind that is one of these shaders (a scene's `transition: shader:domain-warp`).
const Prefix = "shader:"

// Names are the shaders, sorted.
func Names() []string {
	load()
	out := make([]string, 0, len(frags))
	for n := range frags {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Fragment is the fragment shader of a transition, or false.
func Fragment(name string) (string, bool) {
	load()
	f, ok := frags[name]
	return f, ok
}

// ShaderOf is the shader a transition kind names ("shader:domain-warp" gives "domain-warp"), or false when the kind is not a shader one.
func ShaderOf(kind string) (string, bool) {
	name, ok := strings.CutPrefix(kind, Prefix)
	if !ok {
		return "", false
	}
	_, known := Fragment(name)
	return name, known
}

// DefaultAccent is the colour the shaders glow with when the job names none (HyperFrames' own default orange).
const DefaultAccent = "#ff6b2b"

// accentColors are the three colours the shaders read (u_accent, u_accent_dark, u_accent_bright), derived from one as HyperFrames does.
func accentColors(hex string) ([3]float64, [3]float64, [3]float64, error) {
	var z [3]float64
	h := strings.TrimPrefix(strings.TrimSpace(hex), "#")
	if len(h) == 3 {
		h = string([]byte{h[0], h[0], h[1], h[1], h[2], h[2]})
	}
	if len(h) != 6 {
		return z, z, z, fmt.Errorf("%q is not a colour of the form #rrggbb", hex)
	}
	var c [3]float64
	for i := 0; i < 3; i++ {
		v, err := strconv.ParseUint(h[2*i:2*i+2], 16, 8)
		if err != nil {
			return z, z, z, fmt.Errorf("%q is not a colour of the form #rrggbb", hex)
		}
		c[i] = float64(v) / 255
	}
	var dark, bright [3]float64
	for i := range c {
		dark[i] = c[i] * 0.35
		bright[i] = math.Min(1, c[i]*1.5+0.2)
	}
	return c, dark, bright, nil
}

// Job is one transition to draw.
type Job struct {
	Browser string
	// FFmpeg is the ffmpeg program; empty means "ffmpeg".
	FFmpeg string
	Shader string
	// From and To are the frames (PNG files' content) of the end of the first clip and the start of the second, the same number of each; the
	// transition has as many frames as there are pairs, from the first clip's alone to the second's alone.
	From, To      [][]byte
	Width, Height int
	FPS           int
	// Accent is the glow colour, "#rrggbb"; empty is DefaultAccent.
	Accent string
	// Out is the video to write (no sound).
	Out string
}

const pageHTML = `<!doctype html><html><head><meta charset="utf-8"><style>html,body{margin:0;background:#000;overflow:hidden}canvas{display:block}</style></head>
<body><canvas id="c" width="%d" height="%d"></canvas><script>
(function(){
  var N = %d, FPS = %d;
  var canvas = document.getElementById('c');
  var gl = canvas.getContext('webgl', {preserveDrawingBuffer: true, antialias: false});
  if (!gl) { throw new Error('WebGL is not available'); }
  function shader(type, src){ var s = gl.createShader(type); gl.shaderSource(s, src); gl.compileShader(s); if (!gl.getShaderParameter(s, gl.COMPILE_STATUS)) throw new Error('shader: ' + gl.getShaderInfoLog(s)); return s; }
  var prog = gl.createProgram();
  gl.attachShader(prog, shader(gl.VERTEX_SHADER, %s));
  gl.attachShader(prog, shader(gl.FRAGMENT_SHADER, %s));
  gl.linkProgram(prog);
  if (!gl.getProgramParameter(prog, gl.LINK_STATUS)) throw new Error('program: ' + gl.getProgramInfoLog(prog));
  gl.useProgram(prog);
  var buf = gl.createBuffer(); gl.bindBuffer(gl.ARRAY_BUFFER, buf);
  gl.bufferData(gl.ARRAY_BUFFER, new Float32Array([-1,-1, 1,-1, -1,1, 1,1]), gl.STATIC_DRAW);
  var loc = gl.getAttribLocation(prog, 'a_pos'); gl.enableVertexAttribArray(loc); gl.vertexAttribPointer(loc, 2, gl.FLOAT, false, 0, 0);
  function tex(unit){ var t = gl.createTexture(); gl.activeTexture(gl.TEXTURE0 + unit); gl.bindTexture(gl.TEXTURE_2D, t);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.LINEAR); gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.LINEAR);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, gl.CLAMP_TO_EDGE); gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.CLAMP_TO_EDGE); return t; }
  tex(0); tex(1);
  gl.uniform1i(gl.getUniformLocation(prog, 'u_from'), 0); gl.uniform1i(gl.getUniformLocation(prog, 'u_to'), 1);
  gl.uniform2f(gl.getUniformLocation(prog, 'u_resolution'), canvas.width, canvas.height);
  gl.uniform3f(gl.getUniformLocation(prog, 'u_accent'), %f, %f, %f);
  gl.uniform3f(gl.getUniformLocation(prog, 'u_accent_dark'), %f, %f, %f);
  gl.uniform3f(gl.getUniformLocation(prog, 'u_accent_bright'), %f, %f, %f);
  var uP = gl.getUniformLocation(prog, 'u_progress');
  function frame(kind, i, unit){ return fetch('/f/' + kind + '-' + i + '.png').then(function(r){ return r.blob(); }).then(createImageBitmap).then(function(bmp){
    gl.activeTexture(gl.TEXTURE0 + unit); gl.texImage2D(gl.TEXTURE_2D, 0, gl.RGBA, gl.RGBA, gl.UNSIGNED_BYTE, bmp); bmp.close(); }); }
  window.__seek = function(t){
    var i = Math.max(0, Math.min(N - 1, Math.round(t * FPS)));
    return Promise.all([frame('from', i, 0), frame('to', i, 1)]).then(function(){
      gl.uniform1f(uP, N > 1 ? i / (N - 1) : 1);
      gl.viewport(0, 0, canvas.width, canvas.height); gl.drawArrays(gl.TRIANGLE_STRIP, 0, 4);
      var px = new Uint8Array(4); gl.readPixels(0, 0, 1, 1, gl.RGBA, gl.UNSIGNED_BYTE, px); // waits for the draw
    });
  };
})();
</script></body></html>`

// Render draws the transition: one frame for each pair of From and To, mixed by the shader, as a video of Out.
func Render(ctx context.Context, j Job) (int, []string, error) {
	frag, ok := Fragment(j.Shader)
	if !ok {
		return 0, nil, fmt.Errorf("%q is not a shader transition (known: %s)", j.Shader, strings.Join(Names(), ", "))
	}
	if len(j.From) == 0 || len(j.From) != len(j.To) {
		return 0, nil, errors.New("the transition needs the same number of frames, at least one, from each clip")
	}
	if j.Width <= 0 || j.Height <= 0 || j.FPS <= 0 {
		return 0, nil, errors.New("the transition needs a size and a frame rate")
	}
	acc := j.Accent
	if acc == "" {
		acc = DefaultAccent
	}
	a, d, b, err := accentColors(acc)
	if err != nil {
		return 0, nil, err
	}
	n := len(j.From)
	vj, _ := json.Marshal(vertex)
	fj, _ := json.Marshal(frag)
	html := fmt.Sprintf(pageHTML, j.Width, j.Height, n, j.FPS, vj, fj, a[0], a[1], a[2], d[0], d[1], d[2], b[0], b[1], b[2])
	frames := map[string][]byte{}
	for i := 0; i < n; i++ {
		frames[fmt.Sprintf("/f/from-%d.png", i)] = j.From[i]
		frames[fmt.Sprintf("/f/to-%d.png", i)] = j.To[i]
	}
	page := motion.Page{
		HTML: html, Width: j.Width, Height: j.Height, Duration: float64(n) / float64(j.FPS), GPU: true,
		Resource: func(u string) ([]byte, string, bool) {
			p := strings.TrimPrefix(u, "http://motion.invalid")
			if b, ok := frames[p]; ok {
				return b, "image/png", true
			}
			return nil, "", false
		},
	}
	return motion.Render(ctx, motion.Job{Page: page, FPS: j.FPS, Browser: j.Browser, FFmpeg: j.FFmpeg, Out: j.Out})
}
