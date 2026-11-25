package generation

import (
	"encoding/json"
	"strings"
	"testing"

	"veritmo/internal/project/config"
)

func TestReferenceLimits_FollowTheMemoryOfTheCard(t *testing.T) {
	for _, tc := range []struct {
		name   string
		vramMB int
		images int
		videos int
		vsecs  float64
		audios int
		asecs  float64
	}{
		{"RTX 4070, 12 GB", 12282, 2, 1, 2, 1, 3},
		{"a card just under 15 GB", 15359, 2, 1, 2, 1, 3},
		{"15 GB", 15360, 6, 2, 3, 3, 4},
		{"RTX 5060 Ti, 16 GB", 16311, 6, 2, 3, 3, 4},
		{"a 24 GB card gets what was measured, no more", 24576, 6, 2, 3, 3, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			imgs, vids, auds := referenceLimitsFor(tc.vramMB)
			if imgs.Min != 1 || imgs.Max != tc.images || vids.Max != tc.videos || vids.MaxSeconds != tc.vsecs || auds.Max != tc.audios || auds.MaxSeconds != tc.asecs {
				t.Errorf("limits = %+v %+v %+v", imgs, vids, auds)
			}
		})
	}
	// 9 pictures and 3 videos, what the tool the dashboard copies offers, were not measured and are not offered (3 audios were)
	imgs, vids, auds := referenceLimitsFor(80 * 1024)
	if imgs.Max >= 9 || vids.Max >= 3 || auds.Max > 3 {
		t.Errorf("limits above the measured ones: %+v %+v %+v", imgs, vids, auds)
	}
}

func TestInputsOf_WhatEachWayOfMakingAVideoTakes(t *testing.T) {
	m16, m12 := machine(16311, 64), machine(12282, 64)
	for _, tc := range []struct {
		name  string
		c     videoChoice
		m     planMachine
		start bool
		end   bool
		refs  bool
	}{
		{"H3 with the first-and-last-frame bundle", videoChoice{Engine: config.EngineMiniMaxH3, FirstFrame: true}, m16, true, true, false},
		{"H3 with only the Ref2VA bundle used as a picture reference", videoChoice{Engine: config.EngineMiniMaxH3}, m16, true, false, false},
		{"H3 made for references on a 16 GB card", videoChoice{Engine: config.EngineMiniMaxH3, References: true}, m16, false, false, true},
		{"H3 made for references on a 12 GB card", videoChoice{Engine: config.EngineMiniMaxH3, References: true}, m12, false, false, true},
		{"LTX", videoChoice{Engine: config.EngineLTX}, m16, true, true, false},
		{"Wan", videoChoice{Engine: config.EngineWan}, m16, true, false, false},
		{"AnimateDiff", videoChoice{Engine: config.EngineAnimateDiff}, m16, true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := inputsOf(tc.c, tc.m)
			if in.StartImage != tc.start || in.EndImage != tc.end || (in.ReferenceImages != nil) != tc.refs || (in.ReferenceVideos != nil) != tc.refs || (in.ReferenceAudios != nil) != tc.refs {
				t.Errorf("inputs = %+v", in)
			}
		})
	}
}

func TestInputs_AreSentAsTheContractSaysAndAnAbsentKindIsNotThere(t *testing.T) {
	raw, _ := json.Marshal(inputsOf(videoChoice{Engine: config.EngineWan}, machine(16311, 64)))
	if string(raw) != `{"start_image":true,"end_image":false}` {
		t.Errorf("an option with only a first picture: %s", raw)
	}
	raw, _ = json.Marshal(inputsOf(videoChoice{Engine: config.EngineMiniMaxH3, References: true}, machine(12282, 64)))
	for _, want := range []string{`"start_image":false`, `"end_image":false`, `"reference_images":{"min":1,"max":2}`, `"reference_videos":{"max":1,"max_seconds":2}`, `"reference_audios":{"max":1,"max_seconds":3}`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("%s lacks %s", raw, want)
		}
	}
}

func TestCatalog_TheReferencesBundleHasALowMemoryEntryMeasured(t *testing.T) {
	e, ok := findCatalog("video-minimax-h3-ref2va-q4")
	if !ok || e.LowVRAM == nil {
		t.Fatalf("entry = %+v", e)
	}
	if e.LowVRAM.VRAMMB != 11520 || e.LowVRAM.Resolution != referenceResolution {
		t.Errorf("low vram = %+v, want 11520 MB at %s (the light case peaked at 11.3 GB)", e.LowVRAM, referenceResolution)
	}
}

func tH3Ref() catalogEntry {
	e := tH3()
	e.LowVRAM = &catalogLowVRAM{VRAMMB: 11520, Resolution: "768x416"}
	return e
}

func optionsByID(cat []catalogEntry, m planMachine) map[string]videoOptionView {
	out := map[string]videoOptionView{}
	for _, o := range buildVideoOptions(cat, m, "") {
		out[o.ID] = o
	}
	return out
}

func TestOptionsAnnounceTheirInputs(t *testing.T) {
	cat := append(baseCatalog()[:5:5], tH3Ref(), tH3FF(), tLTX(), tAD())
	opts := optionsByID(cat, machine(16384, 32))
	for _, id := range []string{"h3-full", "h3-draft", "ltx"} {
		if in := opts[id].Inputs; in == nil || !in.StartImage || !in.EndImage || in.ReferenceImages != nil {
			t.Errorf("%s inputs = %+v, want start and end picture", id, in)
		}
	}
	for _, id := range []string{"wan-high", "wan-medium", "wan-low", "ad-high", "ad-medium", "ad-low"} {
		if in := opts[id].Inputs; in == nil || !in.StartImage || in.EndImage || in.ReferenceImages != nil {
			t.Errorf("%s inputs = %+v, want the start picture alone", id, in)
		}
	}
	for _, id := range []string{"h3-ref", "h3-ref-draft"} {
		o := opts[id]
		if !o.Available || o.Inputs == nil || o.Inputs.StartImage || o.Inputs.EndImage || o.Inputs.ReferenceImages == nil ||
			o.Inputs.ReferenceImages.Max != 6 || o.Inputs.ReferenceVideos.MaxSeconds != 3 || o.Inputs.ReferenceAudios.Max != 3 {
			t.Errorf("%s = %+v / %+v", id, o, o.Inputs)
		}
	}
	if opts["h3-full"].Default != true || opts["h3-ref"].Default {
		t.Errorf("the default must stay h3-full: %v %v", opts["h3-full"].Default, opts["h3-ref"].Default)
	}
}

func TestOnlyTheFirstFrameBundleTakesALastPictureOnH3(t *testing.T) {
	opts := optionsByID(append(baseCatalog()[:5:5], tH3Ref()), machine(16384, 32))
	if in := opts["h3-full"].Inputs; in == nil || in.EndImage {
		t.Errorf("with only the references bundle h3-full has no last picture: %+v", in)
	}
}

func TestReferencesFollowTheCardAndNeedItsMemory(t *testing.T) {
	cat := append(baseCatalog()[:5:5], tH3Ref())
	small := optionsByID(cat, machine(12288, 32))["h3-ref"]
	if !small.Available || small.Inputs.ReferenceImages.Max != 2 || small.Inputs.ReferenceVideos.MaxSeconds != 2 || small.Inputs.ReferenceAudios.MaxSeconds != 3 {
		t.Errorf("12 GB: %+v / %+v", small, small.Inputs)
	}
	tiny := optionsByID(cat, machine(8192, 32))["h3-ref"]
	if tiny.Available || tiny.Reason == "" || !strings.Contains(tiny.Reason, "11 GB") {
		t.Errorf("8 GB: %+v", tiny)
	}
	if _, ok := findChoice(videoChoices(cat, machine(12288, 32)), "h3-ref"); !ok {
		t.Error("h3-ref is not a choice")
	}
}
