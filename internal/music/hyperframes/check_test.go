package hyperframes

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
	"time"

	"veritmo/internal/video/motion"
)

func chromeDrawer(t *testing.T, html string) (Drawer, string) {
	t.Helper()
	br, err := motion.Find("")
	if err != nil {
		t.Skip("no Chrome or Chromium")
	}
	reg := t.TempDir()
	blk := filepath.Join(reg, "demo")
	if err := os.MkdirAll(blk, 0o755); err != nil {
		t.Fatal(err)
	}
	must := func(p, s string) {
		if err := os.WriteFile(filepath.Join(blk, p), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("registry-item.json", `{"name":"demo","title":"Demo","dimensions":{"width":320,"height":180},"duration":2,"files":[{"path":"demo.html","type":"hyperframes:composition"}]}`)
	must("demo.html", html)
	st := Store{Dir: t.TempDir()}
	if res, err := st.Import(Source{Dir: reg}, nil, false); err != nil || !res[0].Written {
		t.Fatalf("%+v %v", res, err)
	}
	return Drawer{Store: st, Browser: br.Path, Limits: motion.Limits{Load: 20 * time.Second, Frame: 10 * time.Second}}, "demo"
}

const movingBlock = `<!doctype html><html><body><div id="b" style="width:50px;height:50px;background:red"></div>
<script>window.__timelines={}; var tl={seek:function(t){document.getElementById('b').style.marginLeft=(t*100)+'px'},pause:function(){}}; window.__timelines.demo=tl;</script></body></html>`

func TestCheckPassesAMovingBlock(t *testing.T) {
	d, name := chromeDrawer(t, movingBlock)
	res, err := d.Check(context.Background(), name)
	if err != nil || !res.OK {
		t.Fatalf("%+v %v", res, err)
	}
	if b, _ := d.Store.Get(name); b.Checks == nil || !b.Checks.OK {
		t.Fatalf("the result was not kept: %+v", b.Checks)
	}
}

func TestCheckRefusesWhatAsksForTheNetworkAndWarnsOfAStillOne(t *testing.T) {
	d, name := chromeDrawer(t, `<!doctype html><html><body><p>still</p><script src="https://example.com/x.js"></script></body></html>`)
	res, err := d.Check(context.Background(), name)
	if err != nil || res.OK {
		t.Fatalf("%+v %v", res, err)
	}
	all := strings.Join(res.Problems, "|")
	if !strings.Contains(all, "outside itself") {
		t.Fatalf("%s", all)
	}
	if strings.Contains(all, "nothing") || len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "static") {
		t.Fatalf("a still block is a warning, not a problem: %v / %v", res.Problems, res.Warnings)
	}
}

func TestThumbnailIsDrawnOnceAndKept(t *testing.T) {
	d, name := chromeDrawer(t, movingBlock)
	a, err := d.Thumbnail(context.Background(), name)
	if err != nil || len(a) < 100 || a[0] != 0xFF || a[1] != 0xD8 {
		t.Fatalf("%d bytes, %v", len(a), err)
	}
	if kept, ok := d.Store.Thumb(name); !ok || len(kept) != len(a) {
		t.Fatal("the thumbnail was not kept")
	}
	if err := d.Store.WriteHTML(name, movingBlock+"<!-- edit -->", CheckResult{OK: true}); err != nil {
		t.Fatal(err)
	}
	if _, ok := d.Store.Thumb(name); ok {
		t.Fatal("an edit must drop the thumbnail")
	}
	if b, _ := d.Store.Get(name); !b.Edited {
		t.Fatal("the block is not marked as edited")
	}
}

func TestRenderClipFollowsTheTimelineAndTakesVariables(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg")
	}
	html := `<!doctype html><html><body><div id="v" style="font-size:40px"></div><div id="b" style="width:20px;height:20px;background:red"></div>
<script>var vars=window.__hyperframes.getVariables(); document.getElementById('v').textContent=vars.word||'default';
window.__timelines={}; window.__timelines.demo={pause:function(){},seek:function(t){document.getElementById('b').style.marginLeft=(t*60)+'px'}};</script></body></html>`
	d, name := chromeDrawer(t, html)
	out := filepath.Join(t.TempDir(), "clip.mp4")
	n, probs, err := d.RenderClip(context.Background(), name, 1, 10, map[string]any{"word": "hello"}, out)
	if err != nil || n != 10 || len(probs) != 0 {
		t.Fatalf("%d %v %v", n, probs, err)
	}
	if fi, err := os.Stat(out); err != nil || fi.Size() < 500 {
		t.Fatalf("%v %v", fi, err)
	}
}

func TestSameFrameToleratesAFewPixelsButNotAChange(t *testing.T) {
	mk := func(edit func(img *image.RGBA)) []byte {
		img := image.NewRGBA(image.Rect(0, 0, 100, 100))
		for i := range img.Pix {
			img.Pix[i] = 200
		}
		edit(img)
		var b bytes.Buffer
		_ = png.Encode(&b, img)
		return b.Bytes()
	}
	base := mk(func(*image.RGBA) {})
	few := mk(func(img *image.RGBA) {
		img.SetRGBA(3, 3, color.RGBA{0, 0, 0, 255})
		img.SetRGBA(50, 50, color.RGBA{0, 0, 0, 255})
	}) // 2 of 10000 pixels
	soft := mk(func(img *image.RGBA) {
		for i := range img.Pix {
			img.Pix[i] = 205
		}
	}) // everything a little different: under the threshold of a level
	many := mk(func(img *image.RGBA) {
		for x := 0; x < 100; x++ {
			for y := 0; y < 10; y++ {
				img.SetRGBA(x, y, color.RGBA{0, 0, 0, 255})
			}
		}
	})
	if !SameFrame(base, base) || !SameFrame(base, few) || !SameFrame(base, soft) {
		t.Fatal("a few pixels, or a small change of level, are the same picture")
	}
	if SameFrame(base, many) || SameFrame(base, []byte("not a png")) {
		t.Fatal("a tenth of the picture changed is not the same")
	}
}

func TestABlockThatDrawsAtRandomLooksTheSameOnEveryPass(t *testing.T) {
	html := `<!doctype html><html><body><div id="b" style="width:50px;height:50px;background:red"></div>
<script>var k = Math.random() * 100; window.__timelines={}; window.__timelines.demo={pause:function(){},seek:function(t){document.getElementById('b').style.marginLeft=(k + t*20)+'px'}};</script></body></html>`
	d, name := chromeDrawer(t, html)
	res, err := d.Check(context.Background(), name)
	if err != nil || !res.OK {
		t.Fatalf("a seeded Math.random is the same on every load: %+v %v", res, err)
	}
}
