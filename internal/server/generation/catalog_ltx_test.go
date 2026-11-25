package generation

import (
	"strings"
	"testing"

	"veritmo/internal/project/config"
)

const idLTX = "video-ltx-2-3-distilled-q4"

func TestCatalogLTX_FiveFilesPresetAndTiming(t *testing.T) {
	e := catalogEntryByID(t, idLTX)
	if e.Kind != "video" || len(e.Files) != 5 {
		t.Fatalf("kind %q files %d", e.Kind, len(e.Files))
	}
	roles := rolesByName(e)
	for _, want := range []string{"model", "text_encoder", "vae", "audio_vae", "connectors"} {
		if roles[want] == "" {
			t.Errorf("no %s file", want)
		}
	}
	if e.MinRAMGB != 24 || e.VRAMMB == 0 {
		t.Errorf("min_ram_gb = %d, vram_mb = %d", e.MinRAMGB, e.VRAMMB)
	}
	if p := e.Preset; p == nil || p.Steps != 8 || p.CfgScale == nil || *p.CfgScale != 1 || p.Resolution != "768x416" {
		t.Errorf("preset = %+v", p)
	}
	if tm := e.Timing; tm == nil || tm.Seconds == 0 || tm.Frames != 49 || tm.Steps != 8 || tm.Resolution != "768x416" || !tm.Measured {
		t.Errorf("timing = %+v", tm)
	}
	if !strings.Contains(e.Description, "--mmap") || !strings.Contains(e.DescriptionEN, "--mmap") {
		t.Error("the description must say the weights are read with --mmap")
	}
	if e.Quality != "" || e.RequiresToken != "" {
		t.Errorf("quality %q, requires_token %q: it must be outside the quality scale and public", e.Quality, e.RequiresToken)
	}
	for _, f := range e.Files {
		if !strings.HasPrefix(f.URL, "https://huggingface.co/unsloth/") {
			t.Errorf("%s must come from an open unsloth repository: %s", f.Name, f.URL)
		}
	}
}

func TestCatalogLTX_InstalledOnlyWithEveryFileInModelsVideo(t *testing.T) {
	s := newAssetServer(t)
	for i, f := range catalogEntryByID(t, idLTX).Files {
		if catalogEntryInstalled(t, s, idLTX) {
			t.Fatalf("installed after only %d of 5 files", i)
		}
		writeAsset(t, s, "video", f.Name, 1)
	}
	if !catalogEntryInstalled(t, s, idLTX) {
		t.Error("with the five files the bundle is installed")
	}
}

func TestCatalogLTX_ShareNoFileWithMiniMax(t *testing.T) {
	h3 := map[string]bool{}
	for _, f := range catalogEntryByID(t, idMiniMax).Files {
		h3[f.Name] = true
	}
	for _, f := range catalogEntryByID(t, idLTX).Files {
		if h3[f.Name] {
			t.Errorf("%s is also in the MiniMax H3 bundle", f.Name)
		}
	}
}

func TestExpectedClip_LTXUsesItsOwnEntryNotH3(t *testing.T) {
	ltx := &config.Config{Defaults: config.Defaults{Animate: &config.Animate{Engine: config.EngineLTX, DiffusionModel: "d", VAE: "v", AudioVAE: "a", LLM: "l", Connectors: "c"}}}
	closeTo(t, "LTX at its defaults", expectedClip(ltx, config.Scene{}), float64(catalogEntryByID(t, idLTX).Timing.Seconds))
	h3 := &config.Config{Defaults: config.Defaults{Animate: &config.Animate{Engine: config.EngineMiniMaxH3, DiffusionModel: "d", VAE: "v", AudioVAE: "a", LLM: "l"}}}
	closeTo(t, "H3 still finds its own entry", expectedClip(h3, config.Scene{}), 258)
}
