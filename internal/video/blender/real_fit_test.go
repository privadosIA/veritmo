package blender

// Real Blender tests of the fit of a model (spec blender-engine, "A model can be sized by its largest dimension"). Skipped without
// VERITMO_REAL_BLENDER=1. The flat model is built here, so the test does not depend on a pack.

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// flatGLB is a box of a binary glTF: width x height x depth in glTF axes (Y is up, which Blender turns into Z), so a flat one has
// a small height.
func flatGLB(w, h, d float32) []byte {
	var pos []byte
	for _, v := range [][3]float32{{-w / 2, 0, -d / 2}, {w / 2, 0, -d / 2}, {w / 2, h, -d / 2}, {-w / 2, h, -d / 2}, {-w / 2, 0, d / 2}, {w / 2, 0, d / 2}, {w / 2, h, d / 2}, {-w / 2, h, d / 2}} {
		for _, f := range v {
			pos = binary.LittleEndian.AppendUint32(pos, math.Float32bits(f))
		}
	}
	var idx []byte
	for _, i := range []uint16{0, 2, 1, 0, 3, 2, 4, 5, 6, 4, 6, 7, 0, 1, 5, 0, 5, 4, 3, 7, 6, 3, 6, 2, 1, 2, 6, 1, 6, 5, 0, 4, 7, 0, 7, 3} {
		idx = binary.LittleEndian.AppendUint16(idx, i)
	}
	bin := append(pos, idx...)
	js := `{"asset":{"version":"2.0"},"scene":0,"scenes":[{"nodes":[0]}],"nodes":[{"mesh":0}],"meshes":[{"primitives":[{"attributes":{"POSITION":0},"indices":1}]}],` +
		`"buffers":[{"byteLength":168}],"bufferViews":[{"buffer":0,"byteOffset":0,"byteLength":96,"target":34962},{"buffer":0,"byteOffset":96,"byteLength":72,"target":34963}],` +
		`"accessors":[{"bufferView":0,"componentType":5126,"count":8,"type":"VEC3","min":[-1,0,-1],"max":[1,1,1]},{"bufferView":1,"componentType":5123,"count":36,"type":"SCALAR"}]}`
	for len(js)%4 != 0 {
		js += " "
	}
	out := []byte("glTF")
	out = binary.LittleEndian.AppendUint32(out, 2)
	out = binary.LittleEndian.AppendUint32(out, uint32(12+8+len(js)+8+len(bin)))
	out = binary.LittleEndian.AppendUint32(out, uint32(len(js)))
	out = binary.LittleEndian.AppendUint32(out, 0x4E4F534A)
	out = append(out, js...)
	out = binary.LittleEndian.AppendUint32(out, uint32(len(bin)))
	out = binary.LittleEndian.AppendUint32(out, 0x004E4942)
	return append(out, bin...)
}

// litCols is how many columns of the frame have a pixel brighter than the dark world, as a fraction of the frame's width.
func litCols(t *testing.T, path string) float64 {
	t.Helper()
	w, h, y := luminance(t, path)
	n := 0
	for i := 0; i < w; i++ {
		for j := 0; j < h; j++ {
			if y[j*w+i] > 25 {
				n++
				break
			}
		}
	}
	if n == 0 {
		t.Fatalf("%s has nothing lit", path)
	}
	return float64(n) / float64(w)
}

func TestRealAFlatModelIsSizedByItsLargestDimension(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "plate.glb"), flatGLB(0.3, 0.03, 0.3), 0o644); err != nil {
		t.Fatal(err)
	}
	env := testEnv(dir)
	doc := func(members string) string {
		return `{"version":1,"world":{"color":"#000000"},"camera":{"lens":50,"keys":[{"t":0,"location":[0,-6,0.6],"target":[0,0,0.6]}]},` +
			`"lights":[{"type":"sun","rotation":[60,0,20],"power":4},{"type":"area","location":[0,-4,3],"target":[0,0,0.5],"power":800,"size":3}],` +
			`"objects":[{"id":"plate","type":"model","file":"plate.glb",` + members + `}]}`
	}
	width := func(members string) float64 {
		return litCols(t, frameFile(renderSpec(t, doc(members), env, nil, 3, 640, 360), 1))
	}
	// a lens of 50 mm on a sensor 36 mm wide sees 0.72 * 6 m = 4.32 m of width at 6 m
	largest := width(`"size":0.3,"fit":"largest"`)
	if want := 0.3 / 4.32; largest < want*0.7 || largest > want*1.3 {
		t.Errorf("a plate sized by its largest dimension fills %.3f of the width, want about %.3f (30 cm)", largest, want)
	}
	byHeight := width(`"size":0.3`)
	// ten times in size; a little more in the picture because the front of a 3 m plate is nearer the camera
	if r := byHeight / largest; r < 9 || r > 15 {
		t.Errorf("sized by its height (3 cm) the same size is %.1f times wider, want about 10", r)
	}
	if explicit := width(`"size":0.3,"fit":"height"`); math.Abs(explicit-byHeight) > 0.01 {
		t.Errorf("fit height is what no fit means: %.3f against %.3f", explicit, byHeight)
	}
	scaled := width(`"size":0.3,"fit":"largest","keys":[{"t":0,"scale":2}]`)
	if r := scaled / largest; r < 1.8 || r > 2.2 {
		t.Errorf("a scale key still multiplies the size: %.2f times, want 2", r)
	}
}
