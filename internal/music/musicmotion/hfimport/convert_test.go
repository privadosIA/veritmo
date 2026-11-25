package hfimport

import (
	"encoding/json"
	"strings"
	"testing"

	"veritmo/internal/music/musicmotion"
)

// A kinetic-type block as HyperFrames installs it: a complete document that loads GSAP from a CDN, sets its page up in px,
// declares variables on its root and builds one paused timeline.
const kineticBlock = `<!doctype html>
<html lang="en">
  <head>
    <meta charset="UTF-8" />
    <title>Kinetic headline</title>
    <script src="https://cdn.jsdelivr.net/npm/gsap@3.14.2/dist/gsap.min.js"></script>
    <style>
      * { margin: 0; padding: 0; box-sizing: border-box; }
      html, body { background: transparent; overflow: hidden; }
      body { width: 1920px; height: 1080px; }
      @font-face { font-family: "Archivo Black"; src: url(fonts/archivo.woff2); }
      #root .k-wrap { position: absolute; left: 160px; top: 380px; }
      #root .k-head { font-family: "Archivo Black", sans-serif; font-size: 160px; color: var(--ink); text-transform: uppercase; }
      #root .k-sub { font-family: 'Space Mono', monospace; font-size: 40px; color: #ffd23f; }
    </style>
  </head>
  <body>
    <div id="root" data-composition-id="kinetic" data-width="1920" data-height="1080" data-start="0" data-duration="4"
      data-composition-variables='[{"id":"headline","type":"string","label":"Headline","default":"Hello"},{"id":"ink","type":"color","label":"Ink","default":"#ffffff"},{"id":"speed","type":"number","label":"Speed","default":1,"min":0.5,"max":3}]'>
      <div id="k-clip" class="clip" data-start="0" data-duration="4">
        <div class="k-wrap">
          <div id="k-head" class="k-head" data-var-text="headline">Hello</div>
          <div id="k-sub" class="k-sub" style="font-family: 'Inter', sans-serif">sub line</div>
        </div>
      </div>
    </div>
    <script>
      window.__timelines = window.__timelines || {};
      var tl = gsap.timeline({ paused: true });
      tl.from('#k-head', { y: 80, opacity: 0, duration: 0.6, ease: 'power3.out' }, 0.1);
      tl.from('#k-sub', { x: -60, opacity: 0, duration: 0.5, ease: 'power3.out' }, 0.5);
      window.__timelines['kinetic'] = tl;
    </script>
  </body>
</html>`

func convert(t *testing.T, name, block string) Result {
	t.Helper()
	return Convert(name, []byte(block), nil)
}

func TestACleanKineticBlockBecomesAPartWithItsParameters(t *testing.T) {
	r := convert(t, "kinetic", kineticBlock)
	if len(r.Problems) > 0 {
		t.Fatalf("a clean block is refused: %v", r.Problems)
	}
	if r.Name != "hf-kinetic" || r.Manifest.Name != "hf-kinetic" || r.Manifest.Description != "Kinetic headline" {
		t.Fatalf("name or description: %+v", r.Manifest)
	}
	if len(r.Manifest.Params) != 3 {
		t.Fatalf("want 3 parameters, got %+v", r.Manifest.Params)
	}
	byName := map[string]musicmotion.Param{}
	for _, p := range r.Manifest.Params {
		byName[p.Name] = p
	}
	if p := byName["headline"]; p.Type != "string" || p.Default != "Hello" || p.MaxLength != 80 {
		t.Errorf("headline: %+v", p)
	}
	if p := byName["ink"]; p.Type != "string" || p.Default != "var(--fg)" && p.Default != "var(--accent)" {
		t.Errorf("a colour is a string that defaults to a variable of the design: %+v", p)
	}
	if p := byName["speed"]; p.Type != "number" || p.Min == nil || *p.Min != 0.5 || *p.Max != 3 {
		t.Errorf("speed: %+v", p)
	}
	f := r.Fragment
	for _, bad := range []string{"cdn.jsdelivr.net", "html, body", "@font-face", "<html", "<body", "Archivo Black", "'Space Mono'", "'Inter'"} {
		if strings.Contains(f, bad) {
			t.Errorf("the fragment must not carry %q:\n%s", bad, f)
		}
	}
	for _, want := range []string{`class="hf-stage"`, "var(--display)", "var(--body)", "{{headline}}", "--ink:{{ink}}", "width:1920px;height:1080px", "window.__timelines['kinetic']", "scale("} {
		if !strings.Contains(f, want) {
			t.Errorf("the fragment must carry %q:\n%s", want, f)
		}
	}
	joined := strings.Join(r.Warnings, " | ")
	if !strings.Contains(joined, "colour literal") || !strings.Contains(joined, "fonts mapped") || !strings.Contains(joined, "@font-face dropped") {
		t.Errorf("warnings: %v", r.Warnings)
	}
	// the result is a part the catalog accepts as it stands
	if _, probs := musicmotion.ParsePart(r.ManifestJSON(), r.Fragment); len(probs) > 0 {
		t.Fatalf("ParsePart refuses it: %v", probs)
	}
	var back musicmotion.Manifest
	if err := json.Unmarshal(r.ManifestJSON(), &back); err != nil || back.Name != r.Name {
		t.Fatalf("manifest.json: %v", err)
	}
}

func TestABlockWithMediaIsRefusedSayingPartsCarryNoMedia(t *testing.T) {
	r := convert(t, "clip", strings.Replace(kineticBlock, `<div id="k-sub"`, `<video id="v" src="clip.mp4" muted></video><div id="k-sub"`, 1))
	if len(r.Problems) == 0 || !strings.Contains(strings.Join(r.Problems, " "), "parts carry no media") {
		t.Fatalf("want the media refused, got %v", r.Problems)
	}
	for _, c := range []struct{ what, in, want string }{
		{"an image", `<img src="a.png">`, "(<img>)"},
		{"a canvas", `<canvas id="c"></canvas>`, "canvas"},
		{"a file in assets", `<div style="background:url(assets/bg.png)"></div>`, "assets/"},
		{"the network", `<script>fetch('/x')</script>`, "network"},
	} {
		r := convert(t, "x", strings.Replace(kineticBlock, `<div id="k-sub"`, c.in+`<div id="k-sub"`, 1))
		if !strings.Contains(strings.Join(r.Problems, " "), c.want) {
			t.Errorf("%s: want %q in %v", c.what, c.want, r.Problems)
		}
	}
}

func TestANestedCompositionAndATimingExpressionAreRefusedWithTheirLine(t *testing.T) {
	r := convert(t, "nest", strings.Replace(kineticBlock, `<div id="k-sub"`, `<div data-composition-src="compositions/inner.html" data-start="0"></div><div id="k-sub"`, 1))
	if !strings.Contains(strings.Join(r.Problems, " "), "data-composition-src") {
		t.Fatalf("a nested mount: %v", r.Problems)
	}
	r = convert(t, "expr", strings.Replace(kineticBlock, `data-start="0" data-duration="4">
        <div class="k-wrap">`, `data-start="intro + 0.5" data-duration="4">
        <div class="k-wrap">`, 1))
	if !strings.Contains(strings.Join(r.Problems, " "), `data-start="intro + 0.5"`) {
		t.Fatalf("a timing expression: %v", r.Problems)
	}
}

func TestASelectVariableBecomesAnEnumAndAnImageVariableRefusesTheBlock(t *testing.T) {
	sel := strings.Replace(kineticBlock, `{"id":"speed","type":"number","label":"Speed","default":1,"min":0.5,"max":3}`,
		`{"id":"look","type":"select","label":"Look","default":"bold","options":["bold","thin",{"value":"mono"}]}`, 1)
	r := convert(t, "sel", sel)
	if len(r.Problems) > 0 {
		t.Fatalf("a select is kept: %v", r.Problems)
	}
	var look musicmotion.Param
	for _, p := range r.Manifest.Params {
		if p.Name == "look" {
			look = p
		}
	}
	if look.Type != "enum" || look.Default != "bold" || len(look.Options) != 3 || look.Options[2] != "mono" {
		t.Fatalf("look: %+v", look)
	}
	img := strings.Replace(kineticBlock, `{"id":"speed","type":"number","label":"Speed","default":1,"min":0.5,"max":3}`, `{"id":"photo","type":"image","label":"Photo","default":""}`, 1)
	r = convert(t, "img", img)
	if !strings.Contains(strings.Join(r.Problems, " "), `"photo"`) {
		t.Fatalf("an image variable refuses the block: %v", r.Problems)
	}
}

func TestAVerticalBlockAndOneWithoutATimelineAreRefused(t *testing.T) {
	r := convert(t, "tall", strings.Replace(strings.Replace(kineticBlock, `data-width="1920"`, `data-width="1080"`, 1), `data-height="1080"`, `data-height="1920"`, 1))
	if !strings.Contains(strings.Join(r.Problems, " "), "vertical") {
		t.Fatalf("a vertical canvas: %v", r.Problems)
	}
	r = convert(t, "still", strings.Replace(kineticBlock, "window.__timelines['kinetic'] = tl;", "", 1))
	_ = r
	r = convert(t, "still", strings.ReplaceAll(kineticBlock, "__timelines", "__nothing"))
	if !strings.Contains(strings.Join(r.Problems, " "), "no GSAP timeline") {
		t.Fatalf("no timeline: %v", r.Problems)
	}
}

func TestABlockInATemplateAndOneThatReadsTheHyperframesHelperAreConverted(t *testing.T) {
	tmpl := `<template>
<style>#root .t { position:absolute; left:100px; top:100px; font-family:"Inter"; font-size:60px }</style>
<div id="root" data-composition-id="t" data-width="1920" data-height="1080"><div class="clip" data-start="0" data-duration="3"><div class="t" id="t">hi</div></div></div>
<script>window.__timelines = window.__timelines || {}; var v = window.__hyperframes.getVariables(); var tl = gsap.timeline({paused:true}); tl.to('#t',{x:200,duration:2},0); window.__timelines['t'] = tl;</script>
</template>`
	r := convert(t, "Tmpl_Block", tmpl)
	if len(r.Problems) > 0 {
		t.Fatalf("%v", r.Problems)
	}
	if r.Name != "hf-tmpl-block" {
		t.Fatalf("the name is lower case with dashes: %q", r.Name)
	}
	if !strings.Contains(r.Fragment, "window.__hyperframes = window.__hyperframes ||") || strings.Contains(r.Fragment, "<template") {
		t.Fatalf("the helper shim and no <template>:\n%s", r.Fragment)
	}
}

func TestTheRegistryItemGivesTheDescription(t *testing.T) {
	item := `{"name":"kinetic","title":"Lower Third","description":"A bar wipes in and the name slams up","dimensions":{"width":1920,"height":1080},"duration":4.8}`
	r := Convert("kinetic", []byte(kineticBlock), []byte(item))
	if r.Manifest.Description != "Lower Third: A bar wipes in and the name slams up" {
		t.Fatalf("%q", r.Manifest.Description)
	}
}

func TestALongBlockNameIsCutToWhatAPartMayHave(t *testing.T) {
	if got := PartName("code-snippet-apple-terminal-pro"); len(got) > 32 || !strings.HasPrefix(got, "hf-code-snippet") || strings.HasSuffix(got, "-") {
		t.Fatalf("%q", got)
	}
	long := strings.Repeat("x", 300)
	r := convert(t, "long", strings.Replace(kineticBlock, `"default":"Hello"`, `"default":"`+long+`"`, 1))
	if len(r.Problems) > 0 {
		t.Fatalf("a long default text is not a reason to refuse: %v", r.Problems)
	}
}
