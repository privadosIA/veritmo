package servertest

// catalogs.go: fixture catalogs of models for the tests of the generation family and of the server. They mirror
// web/src/app/editor/auto-plan.spec.ts.

import "veritmo/internal/server/modelcat"

// FixtureTEntry fills what a fixture entry leaves out: the kind "model" and the name taken from the id.
func FixtureTEntry(e modelcat.Entry) modelcat.Entry {
	if e.Kind == "" {
		e.Kind = "model"
	}
	if e.Name == "" {
		e.Name = e.ID
	}
	return e
}

// FixtureFiles returns the files of a fixture entry from role, name pairs (a role "" is a single-file entry).
func FixtureFiles(pairs ...string) []modelcat.File {
	var out []modelcat.File
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, modelcat.File{Role: pairs[i], Name: pairs[i+1]})
	}
	return out
}

// FixtureTInto is an installed IntoRealism-like picture model (14 GB, 8 steps, timed).
func FixtureTInto() modelcat.Entry {
	return FixtureTEntry(modelcat.Entry{ID: "into", Name: "IntoRealism", Installed: true, Styles: []string{"photo"}, StyleRank: 1, VRAMMB: 14336,
		Preset: &modelcat.Preset{Steps: 8, Resolution: "1024x576"}, Timing: &modelcat.Timing{Seconds: 19, Resolution: "1024x576", Steps: 8, Measured: true},
		Files: FixtureFiles("model", "into.safetensors", "text_encoder", "qwen.gguf", "vae", "vae.safetensors")})
}

// FixtureTSD15 is an installed Stable Diffusion 1.5-like picture model (4 GB, one file).
func FixtureTSD15() modelcat.Entry {
	return FixtureTEntry(modelcat.Entry{ID: "sd-1-5", Installed: true, Styles: []string{"general"}, StyleRank: 2, VRAMMB: 4096,
		Timing: &modelcat.Timing{Seconds: 4, Resolution: "512x512", Steps: 20}, Files: FixtureFiles("", "sd15.safetensors")})
}

// FixtureTWan is an installed Wan video bundle with the given quality, time per clip, steps and memory; hi adds the
// high-noise expert of the two-expert variant.
func FixtureTWan(id, quality string, seconds, steps, vram int, hi bool) modelcat.Entry {
	f := FixtureFiles("model", id+".gguf", "text_encoder", "t5.gguf", "vae", "wan.vae")
	if hi {
		f = append(f, modelcat.File{Role: "high_noise_model", Name: id + "-hi.gguf"})
	}
	return FixtureTEntry(modelcat.Entry{ID: id, Kind: "video", Installed: true, Quality: quality, VRAMMB: vram,
		Timing: &modelcat.Timing{Seconds: seconds, Resolution: "832x480", Steps: steps, Frames: 33, Measured: true}, Files: f})
}

// FixtureTH3 is an installed MiniMax H3 video bundle with its turbo LoRA.
func FixtureTH3() modelcat.Entry {
	return FixtureTEntry(modelcat.Entry{ID: "h3", Name: "MiniMax H3", Kind: "video", Installed: true, VRAMMB: 14336, MinRAMGB: 24,
		Preset: &modelcat.Preset{Steps: 20, Resolution: "864x480"},
		Timing: &modelcat.Timing{Seconds: 258, Resolution: "864x480", Steps: 20, Frames: 56, TurboSeconds: 96, TurboSteps: 4, Measured: true},
		Files:  FixtureFiles("model", "dit.gguf", "text_encoder", "enc.gguf", "vae", "v.safetensors", "audio_vae", "a.safetensors", "lora", "turbo.safetensors")})
}

// FixtureTH3FF is the MiniMax H3 first-frame variant: the FL2VA model, the 8-step LoRA and a low-memory way to run it.
func FixtureTH3FF() modelcat.Entry {
	e := FixtureTH3()
	e.ID, e.Name = "h3-ff", "MiniMax H3 first frame"
	e.Files = FixtureFiles("model", "minimax_h3_fl2va.gguf", "text_encoder", "enc.gguf", "vae", "v.safetensors", "audio_vae", "a.safetensors", "lora", "turbo8.safetensors")
	e.Timing = &modelcat.Timing{Seconds: 290, Resolution: "864x480", Steps: 20, Frames: 56, TurboSeconds: 168, TurboSteps: 8, Measured: true}
	e.LowVRAM = &modelcat.LowVRAM{VRAMMB: 11520, Resolution: "768x416"}
	return e
}

// FixtureTLTX is an installed LTX 2.3 video bundle (with audio and connectors).
func FixtureTLTX() modelcat.Entry {
	return FixtureTEntry(modelcat.Entry{ID: "ltx", Name: "LTX 2.3", Kind: "video", Installed: true, VRAMMB: 11520, MinRAMGB: 24,
		Preset: &modelcat.Preset{Steps: 8, Resolution: "768x416"}, Timing: &modelcat.Timing{Seconds: 145, Resolution: "768x416", Steps: 8, Frames: 49, Measured: true},
		Files: FixtureFiles("model", "ltx.gguf", "text_encoder", "gemma.gguf", "vae", "lv.safetensors", "audio_vae", "la.safetensors", "connectors", "conn.safetensors")})
}

// FixtureTAD is an installed AnimateDiff bundle: its motion module on top of an SD 1.5 model.
func FixtureTAD() modelcat.Entry {
	return FixtureTEntry(modelcat.Entry{ID: "ad", Name: "AnimateDiff", Kind: "video", Installed: true, VRAMMB: 6144,
		Preset: &modelcat.Preset{Steps: 25, Resolution: "512x512"}, Timing: &modelcat.Timing{Seconds: 70, Resolution: "512x512", Steps: 25, Frames: 16, Measured: true},
		Files: FixtureFiles("motion_module", "mm_sd15_v3.safetensors")})
}

// FixtureTKontext is an installed FLUX.1 Kontext picture-editing model.
func FixtureTKontext() modelcat.Entry {
	return FixtureTEntry(modelcat.Entry{ID: "kontext", Name: "FLUX.1 Kontext", Installed: true, Edit: true, VRAMMB: 8192, MinRAMGB: 16,
		Preset: &modelcat.Preset{Steps: 20, Resolution: "1024x576"}, Files: FixtureFiles("model", "k.gguf", "clip_l", "cl.safetensors", "t5xxl", "t5.safetensors", "vae", "ae.safetensors")})
}

// FixtureTQwen is an installed Qwen-Image-Edit picture-editing model.
func FixtureTQwen() modelcat.Entry {
	return FixtureTEntry(modelcat.Entry{ID: "qwen", Name: "Qwen-Image-Edit", Installed: true, Edit: true, VRAMMB: 11500, MinRAMGB: 24,
		Preset: &modelcat.Preset{Steps: 20, Resolution: "1024x576"}, Files: FixtureFiles("model", "q.gguf", "llm", "l.gguf", "llm_vision", "mm.gguf", "vae", "qv.safetensors")})
}

// BaseCatalog is the usual catalog of the tests: IntoRealism, SD 1.5, three Wan bundles and MiniMax H3.
func BaseCatalog() []modelcat.Entry {
	return []modelcat.Entry{FixtureTInto(), FixtureTSD15(), FixtureTWan("wan5q4", "low", 85, 20, 6144, false), FixtureTWan("wan5q8", "medium", 85, 20, 8192, false),
		FixtureTWan("wan14", "high", 680, 10, 11264, true), FixtureTH3()}
}

// FullCatalog is every engine installed, so every option can be tried.
func FullCatalog() []modelcat.Entry {
	return append(BaseCatalog(), FixtureTLTX(), FixtureTAD(), FixtureTKontext(), FixtureTQwen())
}
