package musicmotion

import (
	"strings"
	"testing"
)

func TestTheVerseLayoutIsValidatedAsAWhole(t *testing.T) {
	for _, l := range []VerseLayout{{}, {Position: "top"}, {Position: "center", Scale: 0.6, Align: "left"}, {Scale: 1.6, Align: "right"}, {Position: "bottom", Scale: 1}} {
		d := good()
		d.VerseLayout = &l
		if err := d.Validate(); err != nil {
			t.Errorf("%+v: %v", l, err)
		}
	}
	for _, c := range []struct {
		l    VerseLayout
		want string
	}{
		{VerseLayout{Position: "middle"}, "verse_layout.position"},
		{VerseLayout{Align: "justify"}, "verse_layout.align"},
		{VerseLayout{Scale: 0.5}, "verse_layout.scale"},
		{VerseLayout{Scale: 1.7}, "verse_layout.scale"},
		{VerseLayout{Scale: -1}, "verse_layout.scale"},
	} {
		d := good()
		d.VerseLayout = &c.l
		if err := d.Validate(); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: %v, want %q", c.l, err, c.want)
		}
	}
}

func TestAPiecesDesignOverrideReplacesTheVerseLayout(t *testing.T) {
	d := good()
	d.VerseLayout = &VerseLayout{Position: "top", Align: "left"}
	if got := d.With(nil); got.VerseLayout == nil || got.VerseLayout.Position != "top" {
		t.Fatal("no override: the design's")
	}
	over := &DesignOverride{VerseLayout: &VerseLayout{Position: "center", Scale: 1.2}}
	got := d.With(over)
	if got.VerseLayout.Position != "center" || got.VerseLayout.Scale != 1.2 || got.VerseLayout.Align != "" {
		t.Fatalf("the override is a layout of its own: %+v", got.VerseLayout)
	}
	if d.VerseLayout.Position != "top" {
		t.Fatal("the design itself is not changed")
	}
	// an override with a layout that is not valid is refused with the design
	bad := &DesignOverride{VerseLayout: &VerseLayout{Scale: 3}}
	if err := d.With(bad).Validate(); err == nil || !strings.Contains(err.Error(), "scale") {
		t.Fatalf("%v", err)
	}
}

func TestTheVerseLayoutIsInThePageAndTheDefaultIsNot(t *testing.T) {
	page := func(l *VerseLayout) string {
		sc := testScene()
		sc.Design.VerseLayout = l
		html, err := BuildPage(sc)
		if err != nil {
			t.Fatal(err)
		}
		return html
	}
	plain := page(nil)
	for _, l := range []*VerseLayout{{}, {Position: "bottom"}, {Scale: 1}, {Align: "center"}, {Position: "bottom", Scale: 1, Align: "center"}} {
		if page(l) != plain {
			t.Errorf("%+v is the default: the page is the same", l)
		}
	}
	top := page(&VerseLayout{Position: "top"})
	if !strings.Contains(top, "#verse{top:6%;bottom:auto}") || strings.Contains(top, "#verse{top:50%") {
		t.Errorf("top")
	}
	if c := page(&VerseLayout{Position: "center"}); !strings.Contains(c, "#verse{top:50%;bottom:auto;transform:translateY(-50%)}") {
		t.Errorf("center")
	}
	// 720 high: the size the frame gives is 44 px, a scale of 1.5 makes 66
	if big := page(&VerseLayout{Scale: 1.5}); !strings.Contains(big, "#verse{font-size:66px}") {
		t.Errorf("scale: %s", big[strings.Index(big, "#verse{font-size"):][:40])
	}
	if r := page(&VerseLayout{Align: "right"}); !strings.Contains(r, "#verse{text-align:right}") {
		t.Errorf("align")
	}
}

// With Chrome: where the verse is drawn is where the layout says, and it stays inside the frame.
func TestTheVerseIsDrawnWhereTheLayoutSays(t *testing.T) {
	browserOrSkip(t)
	box := func(l *VerseLayout) (top, bottom, left, right float64) {
		sc := testScene()
		sc.Design.VerseLayout = l
		s, _ := openPage(t, sc)
		var r struct{ T, B, L, R float64 }
		evalAt(t, s, 1.2, `(function(){var b=document.getElementById('verse').getBoundingClientRect();return {T:b.top,B:b.bottom,L:b.left,R:b.right}})()`, &r)
		return r.T, r.B, r.L, r.R
	}
	_, bottom, _, _ := box(nil)
	if bottom < 650 {
		t.Fatalf("by default the verse is at the bottom of a 720 frame: %v", bottom)
	}
	top, _, _, _ := box(&VerseLayout{Position: "top"})
	if top > 60 {
		t.Errorf("a verse at the top starts at %v", top)
	}
	cTop, cBottom, _, _ := box(&VerseLayout{Position: "center"})
	if mid := (cTop + cBottom) / 2; mid < 330 || mid > 390 {
		t.Errorf("a centred verse is in the middle of the frame: %v", mid)
	}
	for _, l := range []*VerseLayout{{Position: "top", Scale: 1.6}, {Position: "center", Scale: 1.6, Align: "right"}, {Scale: 0.6, Align: "left"}} {
		tp, bt, lf, rt := box(l)
		if tp < 0 || bt > 720 || lf < 0 || rt > 1280 {
			t.Errorf("%+v leaves the frame: %v %v %v %v", l, tp, bt, lf, rt)
		}
	}
}
