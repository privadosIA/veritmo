package models

import (
	"path/filepath"
	"testing"

	"veritmo/internal/video/videovision"
)

func TestCatalogVision_TheTwoOnnxModelsAreOneEntryThatMatchesWhatTheServiceReads(t *testing.T) {
	var found []catalogEntry
	for _, e := range catalog {
		if e.Kind == "vision" {
			found = append(found, e)
		}
	}
	if len(found) != 1 {
		t.Fatalf("expected one entry, got %d", len(found))
	}
	e := found[0]
	k, ok := FindKind(e.Kind)
	if !ok {
		t.Fatalf("kind %q is not an asset kind", e.Kind)
	}
	if k.Dir != filepath.FromSlash(videovision.WeightsDir) {
		t.Errorf("the models go to %s and the service reads %s", k.Dir, videovision.WeightsDir)
	}
	got := map[string]bool{}
	for _, f := range e.Files {
		if err := ValidateAssetName(k, f.Name); err != nil {
			t.Errorf("%s is refused by its own kind: %v", f.Name, err)
		}
		got[f.Name] = true
	}
	for _, want := range []string{videovision.DepthFile, videovision.MaskFile} {
		if !got[want] {
			t.Errorf("the entry does not install %s, which the service needs", want)
		}
	}
	if len(e.Files) != 2 {
		t.Errorf("files: %+v", e.Files)
	}
	const sum = 99060839 + 175997641 // depth-anything-v2-small.onnx + u2net_human_seg.onnx
	if e.SizeBytes != sum {
		t.Errorf("size %d is not the sum of the two files, %d", e.SizeBytes, sum)
	}
	if e.License != "Apache-2.0" || e.Commercial == "" || e.CommercialNote == "" || e.NameEN == "" || e.DescriptionEN == "" {
		t.Errorf("the entry lacks its licence or its English text: %+v", e)
	}
}

// The video kind lists .safetensors and .gguf only, so the two models are not offered as video models, and the
// vision kind lists only the .onnx files of the same folder.
func TestCatalogVision_TheVideoKindDoesNotListTheOnnxFiles(t *testing.T) {
	video, _ := FindKind("video")
	vision, _ := FindKind("vision")
	for _, name := range []string{videovision.DepthFile, videovision.MaskFile} {
		if ValidateAssetName(video, name) == nil {
			t.Errorf("%s would be taken for a video model", name)
		}
		if err := ValidateAssetName(vision, name); err != nil {
			t.Errorf("%s is not accepted as a vision model: %v", name, err)
		}
	}
	if ValidateAssetName(vision, "Wan2.2-TI2V-5B-Q4_K_S.gguf") == nil {
		t.Error("a video model would be taken for a vision one")
	}
}
