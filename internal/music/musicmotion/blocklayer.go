package musicmotion

// blocklayer.go: a HyperFrames block as an item of a layer over the pieces (spec hyperframes-catalog, "A block can be a layer
// over a verse"). The block is drawn as it is stored, inside an iframe of the scene's page: the page seeks it with the seconds
// from the item's start, and whatever draws the page (the render, the frame route) serves the block's files and the pinned
// libraries to it (hyperframes.Extend), so that nothing is fetched from outside.

import (
	"encoding/json"
	"fmt"
	"html"
	"math"
	"os"
	"regexp"
	"strings"

	"veritmo/internal/music/hyperframes"
)

// BlockStore is the catalog of blocks the layers read (the server's own folder); a test seam.
var BlockStore = func() hyperframes.Store { return hyperframes.Store{Dir: hyperframes.ResolveDir(os.Getenv)} }

// SceneBlock is a block as a scene draws it.
type SceneBlock struct {
	Name          string
	HTML          string
	Width, Height int
	// Vars are the defaults the block declares with the item's parameters laid over them.
	Vars map[string]any
}

// checkBlockItem says whether a block can be an item of a layer: it is in the catalog, did not fail its checks and the libraries
// it loads are installed. It does not look at the parameters (the block declares its own variables).
func checkBlockItem(name string) error {
	if !hyperframes.ValidName(name) {
		return fmt.Errorf("%q is not a block name", name)
	}
	b, err := BlockStore().Get(name)
	if err != nil {
		return fmt.Errorf("the block %q is not in the catalog of blocks", name)
	}
	if st := hyperframes.StateOf(b); st == "failed" || st == "needs-files" || st == "needs-gpu" {
		why := ""
		if b.Checks != nil && len(b.Checks.Problems) > 0 {
			why = ": " + b.Checks.Problems[0]
		}
		return fmt.Errorf("the block %q cannot be used (%s)%s", name, st, why)
	}
	if n := len(hyperframes.Missing(hyperframes.LibsDir)); n > 0 {
		return fmt.Errorf("the block %q needs the HyperFrames libraries (%d are not installed: bash scripts/get-hyperframes-libs.sh)", name, n)
	}
	return nil
}

// loadSceneBlock reads a block for a scene, with the item's parameters laid over its variables' defaults.
func loadSceneBlock(name string, params map[string]any) (*SceneBlock, error) {
	if err := checkBlockItem(name); err != nil {
		return nil, err
	}
	st := BlockStore()
	b, err := st.Get(name)
	if err != nil {
		return nil, err
	}
	src, err := st.Read(name, b.HTML)
	if err != nil {
		return nil, err
	}
	vars := hyperframes.Defaults(b.Variables)
	for k, v := range params {
		vars[k] = v
	}
	return &SceneBlock{Name: name, HTML: string(src), Width: b.Width, Height: b.Height, Vars: vars}, nil
}

var reHead = regexp.MustCompile(`(?i)<head[^>]*>`)

// srcdocOf is the block's page with a <base> that sends its relative files to the folder of the block.
func srcdocOf(b *SceneBlock) string {
	base := fmt.Sprintf(`<base href="%s">`, hyperframes.BlockBase(b.Name))
	if loc := reHead.FindStringIndex(b.HTML); loc != nil {
		return b.HTML[:loc[1]] + base + b.HTML[loc[1]:]
	}
	return "<head>" + base + "</head>" + b.HTML
}

// blockLayerHTML is what goes in the layer's wrapper for a block: the iframe, scaled from the block's own size to the scene's, and the
// script that registers its seek in the page's list of layers.
func blockLayerHTML(b *SceneBlock, slot layerSlot, sceneW, sceneH int) string {
	scale := math.Min(float64(sceneW)/float64(b.Width), float64(sceneH)/float64(b.Height))
	dx := (float64(sceneW) - float64(b.Width)*scale) / 2
	dy := (float64(sceneH) - float64(b.Height)*scale) / 2
	vars, _ := json.Marshal(b.Vars)
	name := hyperframes.VarsPrefix + string(vars)
	return fmt.Sprintf(`<div data-hf-block="%s" style="position:absolute;left:%.3fpx;top:%.3fpx;width:%dpx;height:%dpx;transform-origin:0 0;transform:scale(%.5f);overflow:hidden">`+
		`<iframe id="hf-frame-%d" name="%s" srcdoc="%s" style="width:100%%;height:100%%;border:0;background:transparent" scrolling="no"></iframe></div>`+
		`<script>(function(){var f=document.getElementById('hf-frame-%d');window.__layers=window.__layers||[];`+
		`window.__layers.push({el:document.getElementById('layer-%d'),from:%v,to:%v,seek:function(t){var w=f.contentWindow;if(w&&typeof w.__seek==='function'){var p=w.__seek(t);if(p&&p.then){(window.__pendingSeeks=window.__pendingSeeks||[]).push(p);}}}});})();</script>`,
		b.Name, dx, dy, b.Width, b.Height, scale, slot.N, html.EscapeString(name), html.EscapeString(srcdocOf(b)), slot.N, slot.N, slot.From, slot.To)
}

// blockItemKey is how a block item counts against the parts a page draws.
func blockItemKey(name string) string { return "block " + strings.TrimSpace(name) }
