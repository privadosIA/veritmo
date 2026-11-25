package blender

// Real Blender tests of the models placed from files (spec blender-engine). Skipped without VERITMO_REAL_BLENDER=1. They use
// the Kenney character of testdata/blender-models (CC0), at a tiny size on the CPU.

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const modelSources = "../../../testdata/blender-models/sources"

// modelEnv is a project whose sources hold a copy of the example's characters (a copy: a test may remove them).
func modelEnv(t *testing.T) Env {
	t.Helper()
	dir := t.TempDir()
	for _, n := range []string{"character-female-a.glb", "character-female-c.glb", "colormap.png"} {
		src, err := os.Open(filepath.Join(modelSources, n))
		if err != nil {
			t.Fatal(err)
		}
		dst, err := os.Create(filepath.Join(dir, n))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(dst, src); err != nil {
			t.Fatal(err)
		}
		src.Close()
		dst.Close()
	}
	return testEnv(dir)
}

// oneModel is a description with one character standing at the origin, seen from 6 m with a dark world, whose members
// (the model's fields after "type") are given.
func oneModel(members string) string {
	return `{"version":1,"world":{"color":"#000000"},"camera":{"lens":50,"keys":[{"t":0,"location":[0,-6,0.9],"target":[0,0,0.9]}]},` +
		`"lights":[{"type":"sun","rotation":[60,0,20],"power":3},{"type":"area","location":[0,-4,3],"target":[0,0,1],"power":600,"size":3}],` +
		`"objects":[{"id":"ana","type":"model","file":"character-female-a.glb",` + members + `}]}`
}

// litRows is the first and the last row of the frame that have a pixel brighter than the dark world, as a fraction of the
// frame's height: how tall the figure looks.
func litRows(t *testing.T, path string) float64 {
	t.Helper()
	w, h, y := luminance(t, path)
	first, last := -1, -1
	for j := 0; j < h; j++ {
		for i := 0; i < w; i++ {
			if y[j*w+i] > 25 {
				if first < 0 {
					first = j
				}
				last = j
				break
			}
		}
	}
	if first < 0 {
		t.Fatalf("%s has nothing lit", path)
	}
	return float64(last-first+1) / float64(h)
}

func TestRealAModelIsScaledToItsSize(t *testing.T) {
	env := modelEnv(t)
	small := renderSpec(t, oneModel(`"size":1`), env, nil, 3, 320, 180)
	big := renderSpec(t, oneModel(`"size":2`), env, nil, 3, 320, 180)
	hs, hb := litRows(t, frameFile(small, 1)), litRows(t, frameFile(big, 1))
	// a lens of 50 mm on a sensor 36 mm wide sees 0.405 * 6 m = 2.43 m of height at 6 m
	if want := 1 / 2.43; hs < want-0.06 || hs > want+0.06 {
		t.Errorf("a model of size 1 fills %.2f of the frame, want about %.2f", hs, want)
	}
	if r := hb / hs; r < 1.8 || r > 2.2 {
		t.Errorf("size 2 is %.2f times as tall as size 1, want about 2", r)
	}
}

func TestRealAClipPlaysAndLoopsWithItsOwnDurationInSeconds(t *testing.T) {
	env := modelEnv(t)
	still := renderSpec(t, oneModel(`"size":1.7`), env, nil, 20, 160, 90)
	if s := ssim(t, frameFile(still, 1), frameFile(still, 8)); s < 0.9999 {
		t.Errorf("a model with no animation must not move: ssim %.5f", s)
	}
	run := renderSpec(t, oneModel(`"size":1.7,"animation":{"clip":"sprint"}`), env, nil, 20, 160, 90)
	// sprint lasts half a second in the file: 15 frames at 30 fps, so frame 16 is the pose of frame 1 and frame 8 is not
	if s := ssim(t, frameFile(run, 1), frameFile(run, 8)); s > 0.999 {
		t.Errorf("the clip does not move the figure: ssim of frames 1 and 8 is %.5f", s)
	}
	if s := ssim(t, frameFile(run, 1), frameFile(run, 16)); s < 0.999 {
		t.Errorf("a looped clip of half a second must repeat on frame 16: ssim %.5f", s)
	}
}

func TestRealOffsetAndSpeedAndNoLoop(t *testing.T) {
	env := modelEnv(t)
	run := renderSpec(t, oneModel(`"size":1.7,"animation":{"clip":"sprint"}`), env, nil, 20, 160, 90)
	late := renderSpec(t, oneModel(`"size":1.7,"animation":{"clip":"sprint","offset":0.2}`), env, nil, 20, 160, 90)
	if s := ssim(t, frameFile(run, 10), frameFile(late, 10)); s > 0.999 {
		t.Errorf("a clip delayed by 0.2 s must differ from the one that is not: ssim %.5f", s)
	}
	// delayed by exactly one frame count of the cycle (0.5 s) the pose is the same again
	phase := renderSpec(t, oneModel(`"size":1.7,"animation":{"clip":"sprint","offset":0.5}`), env, nil, 20, 160, 90)
	if s := ssim(t, frameFile(run, 10), frameFile(phase, 10)); s < 0.999 {
		t.Errorf("a looped clip delayed by its own length is the same clip: ssim %.5f", s)
	}
	fast := renderSpec(t, oneModel(`"size":1.7,"animation":{"clip":"sprint","speed":2}`), env, nil, 20, 160, 90)
	// at double speed the cycle takes 15/2 frames: frame 1 and frame 8.5 would match; frame 1 and frame 16 (15 later) is two cycles
	if s := ssim(t, frameFile(fast, 1), frameFile(fast, 16)); s < 0.999 {
		t.Errorf("at speed 2 frame 16 is two cycles after frame 1: ssim %.5f", s)
	}
	if s := ssim(t, frameFile(run, 8), frameFile(fast, 8)); s > 0.999 {
		t.Errorf("speed 2 must change the pose at frame 8: ssim %.5f", s)
	}
	// a clip that does not loop holds its first pose until the offset, plays once, and holds its last pose
	once := renderSpec(t, oneModel(`"size":1.7,"animation":{"clip":"jump","loop":false,"offset":0.3}`), env, nil, 40, 160, 90)
	if s := ssim(t, frameFile(once, 1), frameFile(once, 8)); s < 0.9999 {
		t.Errorf("before its offset the model keeps the first pose: ssim %.5f", s)
	}
	if s := ssim(t, frameFile(once, 36), frameFile(once, 40)); s < 0.9999 {
		t.Errorf("after the clip it keeps the last pose: ssim %.5f", s)
	}
	if s := ssim(t, frameFile(once, 8), frameFile(once, 16)); s > 0.9995 {
		t.Errorf("between its offset and its end it moves: ssim %.5f", s)
	}
}

func TestRealAKeptBlendOpensWithTheModelAndWithoutTheSources(t *testing.T) {
	info, r := realBlender(t)
	env := modelEnv(t)
	dir := renderSpec(t, oneModel(`"size":1.7,"animation":{"clip":"sprint"}`), env, nil, 3, 160, 90)
	if err := os.RemoveAll(env.Sources); err != nil { // the .blend must not need the sources
		t.Fatal(err)
	}
	lines := queryBlend(t, r, info.Path, filepath.Join(dir, "a.blend"), `import bpy
meshes = [o for o in bpy.data.objects if o.type == 'MESH']
arms = [o for o in bpy.data.objects if o.type == 'ARMATURE']
print('Q meshes', ','.join(sorted(o.name for o in meshes if o.visible_get())))
print('Q armatures', len(arms))
print('Q packed', all(i.packed_file is not None for i in bpy.data.images if i.source == 'FILE'))
print('Q clip', any(a.name.split('.')[0] == 'sprint' for a in bpy.data.actions))
print('Q empty', any(o.name == 'ana' and o.type == 'EMPTY' for o in bpy.data.objects))
`)
	got := strings.Join(lines, "|")
	for _, want := range []string{"meshes body-mesh,head-mesh", "armatures 1", "packed True", "clip True", "empty True"} {
		if !strings.Contains(got, want) {
			t.Errorf("the kept .blend says %q, want it to hold %q", got, want)
		}
	}
}
