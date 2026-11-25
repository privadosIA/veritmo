package modelcat

import "testing"

func TestModelPage(t *testing.T) {
	for in, want := range map[string]string{
		"https://huggingface.co/QuantStack/FLUX.1-Kontext-dev-GGUF/resolve/main/flux1-kontext-dev-Q4_K_M.gguf": "https://huggingface.co/QuantStack/FLUX.1-Kontext-dev-GGUF",
		"https://github.com/danielgatis/rembg/releases/download/v0.0.0/u2net_human_seg.onnx":                   "https://github.com/danielgatis/rembg",
		"https://civitai.com/api/download/models/1":                                                            "",
		"http://huggingface.co/a/b/resolve/main/f":                                                             "",
		"https://huggingface.co/onlyone":                                                                       "",
		"not a url":                                                                                            "",
	} {
		if got := ModelPage(in); got != want {
			t.Errorf("ModelPage(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFillLicenceOnlyForALicenceWithConditions(t *testing.T) {
	file := []File{{Name: "m.gguf", URL: "https://huggingface.co/owner/repo/resolve/main/m.gguf"}}

	nc := Entry{ID: "nc", License: "FLUX.1 [dev] Non-Commercial", Commercial: "no", CommercialNote: "No comercial", CommercialNoteEN: "Non-commercial.", Files: file}
	nc.FillLicence()
	if nc.Licence == nil || nc.Licence.Name != "FLUX.1 [dev] Non-Commercial" || nc.Licence.Commercial != "no" || nc.Licence.Summary != "Non-commercial." ||
		nc.Licence.URL != "https://huggingface.co/owner/repo" {
		t.Errorf("non-commercial: %+v", nc.Licence)
	}

	// without the English note the Spanish one is the summary
	es := Entry{ID: "es", License: "X", Commercial: "conditions", CommercialNote: "Con condiciones.", Files: file}
	es.FillLicence()
	if es.Licence == nil || es.Licence.Summary != "Con condiciones." {
		t.Errorf("spanish note: %+v", es.Licence)
	}

	for name, e := range map[string]Entry{
		"permissive":    {License: "Apache-2.0", Commercial: "yes", Files: file},
		"no license":    {Commercial: "conditions", Files: file},
		"no commercial": {License: "MIT"},
		"stale pointer": {License: "MIT", Commercial: "yes", Licence: nil},
	} {
		e.FillLicence()
		if e.Licence != nil {
			t.Errorf("%s: should have no terms, got %+v", name, e.Licence)
		}
	}
}
