package generation

import (
	"testing"

	"veritmo/internal/video/videoedit"
)

func TestCatalogVace_TheMotionControlModelIsThreeFilesTheToolReadsAndNoGenerationOption(t *testing.T) {
	e, ok := findCatalog("video-wan-vace-1-3b-motion-control")
	if !ok {
		t.Fatal("the entry is not in the catalog")
	}
	k, ok := findKind(e.Kind)
	if !ok {
		t.Fatalf("kind %q is not an asset kind", e.Kind)
	}
	roles := map[string]string{}
	for _, f := range e.Files {
		roles[f.Role] = f.Name
		if err := validateAssetName(k, f.Name); err != nil {
			t.Errorf("%s is refused by its own kind: %v", f.Name, err)
		}
	}
	// the tool reads exactly these three files in models/video
	if roles["model"] != videoedit.VaceModel || roles["vae"] != videoedit.VaceVAE || roles["text_encoder"] != videoedit.VaceEncoder || len(e.Files) != 3 {
		t.Errorf("roles = %v", roles)
	}
	if k.Dir != "models/video" || videoedit.MotionModelsDir != "models/video" {
		t.Errorf("the model goes to %s and the tool reads %s", k.Dir, videoedit.MotionModelsDir)
	}
	// 4309519800 + 6043068256 + 253815318: the files as Hugging Face and the machine have them
	if e.SizeBytes != 4309519800+6043068256+253815318 {
		t.Errorf("size %d is not the sum of the three files", e.SizeBytes)
	}
	if e.License != "Apache-2.0" || e.NameEN == "" || e.DescriptionEN == "" || e.VRAMMB < videoedit.MinVRAMMB {
		t.Errorf("entry = %+v", e)
	}
	// the files it shares with the Wan bundles are the same ones, so they are downloaded once
	for _, shared := range []string{videoedit.VaceVAE, videoedit.VaceEncoder} {
		var urls = map[string]bool{}
		for _, other := range catalog {
			for _, f := range other.Files {
				if f.Name == shared {
					urls[f.URL] = true
				}
			}
		}
		if len(urls) != 1 {
			t.Errorf("%s has %d different sources in the catalog: %v", shared, len(urls), urls)
		}
	}
	// it has no quality level, no first frame, no audio: the generation options pick none of it up
	if e.Quality != "" || hasRole(e, "audio_vae") || hasRole(e, "connectors") || hasRole(e, "motion_module") {
		t.Errorf("it would be taken for a generation model: %+v", e)
	}
	installed := e
	installed.Installed = true
	cat := append(baseCatalog(), tH3FF(), tLTX(), installed)
	for _, id := range videoOrder {
		without, _ := findChoice(videoChoices(append(baseCatalog(), tH3FF(), tLTX()), machine(16384, 32)), id)
		with, _ := findChoice(videoChoices(cat, machine(16384, 32)), id)
		if without.Available != with.Available || without.Reason != with.Reason || (without.Entry == nil) != (with.Entry == nil) || (without.Entry != nil && without.Entry.ID != with.Entry.ID) {
			t.Errorf("option %s changed when the motion control model was installed: %+v -> %+v", id, without, with)
		}
	}
}
