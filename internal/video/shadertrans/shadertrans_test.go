package shadertrans

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/project/config"
	"veritmo/internal/video/motion"
)

func TestTheShadersAreTheFourteenOfHyperFrames(t *testing.T) {
	names := Names()
	if len(names) != 14 {
		t.Fatalf("%d shaders: %v", len(names), names)
	}
	for _, n := range []string{"domain-warp", "glitch", "light-leak", "whip-pan"} {
		f, ok := Fragment(n)
		if !ok || !strings.Contains(f, "u_progress") || !strings.Contains(f, "gl_FragColor") {
			t.Errorf("%s: %v", n, ok)
		}
	}
	if n, ok := ShaderOf("shader:glitch"); !ok || n != "glitch" {
		t.Fatal("shader:glitch")
	}
	for _, k := range []string{"fade", "shader:nope", "glitch", ""} {
		if _, ok := ShaderOf(k); ok {
			t.Errorf("%q is not a shader kind", k)
		}
	}
}

func TestAccentColorsAreDerivedAsHyperFramesDoes(t *testing.T) {
	a, d, b, err := accentColors("#ff8000")
	if err != nil || a[0] != 1 || d[0] != 0.35 || b[0] != 1 || b[2] != 0.2 {
		t.Fatalf("%v %v %v %v", a, d, b, err)
	}
	if _, _, _, err := accentColors("orange"); err == nil {
		t.Fatal("a colour that is not #rrggbb is refused")
	}
}

func solid(c color.Color, w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return b.Bytes()
}

func TestRenderMixesTheTwoClipsFromTheFirstToTheSecond(t *testing.T) {
	br, err := motion.Find("")
	if err != nil {
		t.Skip("no Chrome or Chromium")
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg")
	}
	const n = 8
	red, blue := solid(color.RGBA{255, 0, 0, 255}, 160, 90), solid(color.RGBA{0, 0, 255, 255}, 160, 90)
	var from, to [][]byte
	for i := 0; i < n; i++ {
		from, to = append(from, red), append(to, blue)
	}
	out := filepath.Join(t.TempDir(), "t.mp4")
	frames, _, err := Render(context.Background(), Job{Browser: br.Path, Shader: "cross-warp-morph", From: from, To: to, Width: 160, Height: 90, FPS: 8, Out: out})
	if err != nil || frames != n {
		t.Fatalf("%d %v", frames, err)
	}
	pixel := func(frame int) [3]int {
		p := filepath.Join(t.TempDir(), "f.png")
		cmd := exec.Command("ffmpeg", "-y", "-v", "error", "-i", out, "-vf", "select=eq(n\\,"+string(rune('0'+frame))+")", "-frames:v", "1", p)
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, b)
		}
		f, err := os.Open(p)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		img, err := png.Decode(f)
		if err != nil {
			t.Fatal(err)
		}
		r, g, b, _ := img.At(80, 45).RGBA()
		return [3]int{int(r >> 8), int(g >> 8), int(b >> 8)}
	}
	first, last := pixel(0), pixel(n-1)
	if first[0] < 150 || first[2] > 100 {
		t.Errorf("the first frame is the first clip's (red): %v", first)
	}
	if last[2] < 150 || last[0] > 100 {
		t.Errorf("the last frame is the second clip's (blue): %v", last)
	}
}

func TestTheConfigKnowsTheSameShaders(t *testing.T) {
	names := Names()
	if len(config.ShaderTransitions) != len(names) {
		t.Fatalf("config has %d, the package %d", len(config.ShaderTransitions), len(names))
	}
	for _, n := range names {
		if !config.IsTransition(Prefix + n) {
			t.Errorf("config does not accept shader:%s", n)
		}
	}
	if config.IsTransition("shader:nope") || !config.IsTransition("wipeleft") {
		t.Error("only the known shaders and the xfade names are transitions")
	}
}

func TestTheAccentColoursTheGlowOfAShader(t *testing.T) {
	br, err := motion.Find("")
	if err != nil {
		t.Skip("no Chrome or Chromium")
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg")
	}
	const n = 9
	grey := solid(color.RGBA{60, 60, 60, 255}, 160, 90)
	var from, to [][]byte
	for i := 0; i < n; i++ {
		from, to = append(from, grey), append(to, grey)
	}
	mid := func(accent string) [3]int {
		out := filepath.Join(t.TempDir(), "t.mp4")
		if _, _, err := Render(context.Background(), Job{Browser: br.Path, Shader: "light-leak", From: from, To: to, Width: 160, Height: 90, FPS: 9, Accent: accent, Out: out}); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(t.TempDir(), "f.png")
		if b, err := exec.Command("ffmpeg", "-y", "-v", "error", "-i", out, "-vf", "select=eq(n\\,3)", "-frames:v", "1", p).CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, b)
		}
		f, _ := os.Open(p)
		defer f.Close()
		img, _ := png.Decode(f)
		var sum [3]int
		for y := 0; y < 90; y += 5 {
			for x := 0; x < 160; x += 5 {
				r, g, b, _ := img.At(x, y).RGBA()
				sum[0], sum[1], sum[2] = sum[0]+int(r>>8), sum[1]+int(g>>8), sum[2]+int(b>>8)
			}
		}
		return sum
	}
	green, blue := mid("#00ff40"), mid("#2040ff")
	if green[1] <= green[2] || blue[2] <= blue[1] {
		t.Errorf("the glow follows the accent: green %v, blue %v", green, blue)
	}
}
