package musicmotion

// blockmenu.go: the blocks and components the script writer may name as layers over the song (spec script-block-layers). The writer
// sees a short menu of what can be drawn now; the server checks what it names and drops what does not pass.

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"veritmo/internal/music/hyperframes"
)

// Limits of the block layers a proposal may carry.
const (
	MaxBlockMenu       = 40
	MaxBlockLayers     = 2
	MaxBlockLayerItems = 8
)

// MenuVar is a variable of a block as the writer sees it.
type MenuVar struct {
	ID      string
	Type    string
	Default any
}

// MenuEntry is a block or component the writer may name.
type MenuEntry struct {
	Name     string
	Kind     string // "block" or "component"
	Title    string
	Family   string
	Tags     []string
	Duration float64
	Vars     []MenuVar
}

// BlockMenu builds the menu from the catalog of blocks: only those that pass their checks and can be drawn now (the libraries installed, and
// the upstream runtime for those that were checked with it), at most MaxBlockMenu, taken in turn from each family so that the list is varied.
// Empty when nothing is usable.
func BlockMenu() []MenuEntry {
	if len(hyperframes.Missing(hyperframes.LibsDir)) > 0 {
		return nil
	}
	all, err := BlockStore().List()
	if err != nil {
		return nil
	}
	_, haveRuntime := hyperframes.UpstreamRuntime()
	groups := map[string][]MenuEntry{}
	for _, b := range all {
		if hyperframes.StateOf(b) != "ok" || b.Checks == nil || b.Checks.Runtime == "upstream" && !haveRuntime {
			continue
		}
		kind := "block"
		if b.Kind == "component" {
			kind = "component"
		}
		e := MenuEntry{Name: b.Name, Kind: kind, Title: b.Title, Family: b.Family, Tags: b.Tags, Duration: b.Duration, Vars: menuVars(b.Variables)}
		key := b.Family
		if key == "" {
			key = "(" + kind + ")"
		}
		groups[key] = append(groups[key], e)
	}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
		sort.Slice(groups[k], func(i, j int) bool { return groups[k][i].Name < groups[k][j].Name })
	}
	sort.Strings(keys)
	var out []MenuEntry
	for round := 0; len(out) < MaxBlockMenu; round++ {
		added := false
		for _, k := range keys {
			if round < len(groups[k]) && len(out) < MaxBlockMenu {
				out = append(out, groups[k][round])
				added = true
			}
		}
		if !added {
			break
		}
	}
	return out
}

func menuVars(raw json.RawMessage) []MenuVar {
	var vs []struct {
		ID      string `json:"id"`
		Type    string `json:"type"`
		Role    string `json:"role"`
		Default any    `json:"default"`
	}
	if json.Unmarshal(raw, &vs) != nil {
		return nil
	}
	var out []MenuVar
	for _, v := range vs {
		if v.ID != "" && (v.Role == "content" || v.Role == "style" || v.Role == "") && len(out) < 3 {
			out = append(out, MenuVar{ID: v.ID, Type: v.Type, Default: v.Default})
		}
	}
	return out
}

// menuBlock is the text of the menu in the writer's request.
func menuBlock(menu []MenuEntry) string {
	if len(menu) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\nBlocks (optional; whole animated pieces drawn over the song, each as an item of a layer):\n")
	for _, e := range menu {
		fmt.Fprintf(&b, "- %s (%s", e.Name, e.Kind)
		if e.Family != "" {
			b.WriteString(", " + e.Family)
		}
		fmt.Fprintf(&b, ", %.1f s): %s", e.Duration, e.Title)
		if len(e.Vars) > 0 {
			var vs []string
			for _, v := range e.Vars {
				vs = append(vs, fmt.Sprintf("%s=%v", v.ID, v.Default))
			}
			b.WriteString(". Variables: " + strings.Join(vs, ", "))
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, `Add one or two block layers to the answer when a block fits the song (they are what lifts the video above plain typography): a top-level "layers" array next to "pieces", for example "layers": [{"id": "fx", "items": [{"block": "<name above>", "params": {"<variable>": <value>}, "start": <song seconds>, "end": <song seconds>}]}]. Up to %d layers and %d items per layer; items of a layer do not overlap; a block lasts about its own length; choose by the mood of the section (a title element over a verse, an effect or transition at a section change, a background where nothing is sung). Use only names from the list above. Leave "layers" out only if no block fits.`+"\n", MaxBlockLayers, MaxBlockLayerItems)
	return b.String()
}

// blockLayersFrom reads the block layers the writer proposed and keeps only what is valid: the block is in the menu, the range is inside the
// song and the items of a lane do not overlap; variables the block does not declare are dropped. What was dropped is named in the warnings.
func blockLayersFrom(layers []Layer, menu []MenuEntry, duration float64) ([]Layer, []string) {
	if len(menu) == 0 || len(layers) == 0 {
		return nil, nil
	}
	byName := map[string]MenuEntry{}
	for _, e := range menu {
		byName[e.Name] = e
	}
	var out []Layer
	var warns []string
	for li, l := range layers {
		if len(out) >= MaxBlockLayers {
			warns = append(warns, fmt.Sprintf("layer %q dropped: only %d block layers are kept", l.ID, MaxBlockLayers))
			continue
		}
		var items []LayerItem
		for _, it := range l.Items {
			e, ok := byName[it.Block]
			switch {
			case it.Block == "":
				continue // part layers are not the writer's to add
			case !ok:
				warns = append(warns, fmt.Sprintf("the block %q is not in the menu: item dropped", it.Block))
				continue
			case it.Start < 0 || it.End > duration+0.05 || it.End <= it.Start:
				warns = append(warns, fmt.Sprintf("the block %q has a range outside the song (%.1f to %.1f s): item dropped", it.Block, it.Start, it.End))
				continue
			case len(items) > 0 && it.Start < items[len(items)-1].End-coverTolerance:
				warns = append(warns, fmt.Sprintf("the block %q overlaps the one before it in its layer: item dropped", it.Block))
				continue
			case len(items) >= MaxBlockLayerItems:
				warns = append(warns, fmt.Sprintf("the block %q is over the %d items of a layer: item dropped", it.Block, MaxBlockLayerItems))
				continue
			}
			declared := map[string]bool{}
			for _, v := range e.Vars {
				declared[v.ID] = true
			}
			params := map[string]any{}
			for k, v := range it.Params {
				if declared[k] {
					params[k] = v
				}
			}
			if len(params) == 0 {
				params = nil
			}
			items = append(items, LayerItem{Block: it.Block, Params: params, Start: it.Start, End: it.End})
		}
		if len(items) > 0 {
			id := l.ID
			if !reID.MatchString(id) {
				id = fmt.Sprintf("fx%d", li+1)
			}
			out = append(out, Layer{ID: id, Items: items})
		}
	}
	return out, warns
}

// dropBlockLayers takes the layers made of blocks out of the script and says how many there were.
func dropBlockLayers(s *Script) int {
	var keep []Layer
	n := 0
	for _, l := range s.Layers {
		isBlock := len(l.Items) > 0
		for _, it := range l.Items {
			if it.Block == "" {
				isBlock = false
			}
		}
		if isBlock {
			n++
			continue
		}
		keep = append(keep, l)
	}
	s.Layers = keep
	return n
}
