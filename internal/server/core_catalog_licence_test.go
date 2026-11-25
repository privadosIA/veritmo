package server

import "testing"

// Every catalog entry whose licence is not plainly permissive carries the terms a client shows before the download: name, commercial use and
// a page to read the text.
func TestCatalogEntriesWithConditionsCarryTheirLicenceTerms(t *testing.T) {
	var withTerms int
	for _, e := range Catalog() {
		wantTerms := e.License != "" && e.Commercial != "" && e.Commercial != "yes"
		if (e.Licence != nil) != wantTerms {
			t.Errorf("%s: license %q commercial %q but terms %+v", e.ID, e.License, e.Commercial, e.Licence)
			continue
		}
		if e.Licence == nil {
			continue
		}
		withTerms++
		if e.Licence.Name != e.License || e.Licence.Commercial != e.Commercial {
			t.Errorf("%s: terms %+v do not follow the entry", e.ID, e.Licence)
		}
	}
	if withTerms == 0 {
		t.Fatal("no entry has terms: the catalog has models with conditions")
	}
	for _, e := range Catalog() {
		if e.ID == "image-edit-flux-kontext-q4" {
			if e.Licence == nil || e.Licence.Commercial != "no" || e.Licence.URL != "https://huggingface.co/QuantStack/FLUX.1-Kontext-dev-GGUF" || e.Licence.Summary == "" {
				t.Errorf("FLUX.1 Kontext: %+v", e.Licence)
			}
		}
	}
}
