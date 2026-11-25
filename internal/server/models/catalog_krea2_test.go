package models

import (
	"encoding/json"
	"strings"
	"testing"
	"veritmo/internal/server/modelcat"
)

const (
	idKrea = "krea2-turbo-q4"
	idIntr = "krea2-intorealism-v4-fp8"
)

func catalogEntryByID(t *testing.T, id string) catalogEntry {
	t.Helper()
	e, ok := findCatalog(id)
	if !ok {
		t.Fatalf("catalog has no %s", id)
	}
	return e
}

func rolesByName(e catalogEntry) map[string]string {
	m := map[string]string{}
	for _, f := range e.Files {
		m[f.Role] = f.Name
	}
	return m
}

// keyedIDs are the entries whose files need a Civitai key; the private edition's tests add theirs.
var keyedIDs = map[string]bool{idIntr: true}

func TestCatalogKrea2_OnlyIntoRealismRequiresAKey(t *testing.T) {
	for _, e := range catalog {
		want := ""
		if keyedIDs[e.ID] {
			want = "civitai"
		}
		if e.RequiresToken != want {
			t.Errorf("%s: requires_token = %q, want %q", e.ID, e.RequiresToken, want)
		}
	}
	s := newAssetServer(t)
	rec := call(s.Handler(), "GET", "/api/assets/catalog", "")
	var out struct{ Entries []map[string]any }
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	for _, e := range out.Entries {
		tok, has := e["requires_token"]
		if id, _ := e["id"].(string); keyedIDs[id] {
			if tok != "civitai" {
				t.Errorf("%s requires_token = %v", id, tok)
			}
		} else if has {
			t.Errorf("%v must omit requires_token, got %v", e["id"], tok)
		}
	}
}

func TestCatalogKrea2_BundleInstalledOnlyWithEveryFile(t *testing.T) {
	s := newAssetServer(t)
	e := catalogEntryByID(t, idKrea)
	installed := func() bool {
		rec := call(s.Handler(), "GET", "/api/assets/catalog", "")
		var out struct{ Entries []catalogEntry }
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		for _, x := range out.Entries {
			if x.ID == idKrea {
				return x.Installed
			}
		}
		t.Fatal("entry missing from the served catalog")
		return false
	}
	writeAsset(t, s, "model", rolesByName(e)["model"], 1)
	if installed() {
		t.Error("only the checkpoint exists: the bundle must not count as installed")
	}
	writeAsset(t, s, "model", rolesByName(e)["text_encoder"], 1)
	if installed() {
		t.Error("the VAE is still missing")
	}
	writeAsset(t, s, "model", rolesByName(e)["vae"], 1)
	if !installed() {
		t.Error("with all three files the bundle is installed")
	}
}

func TestCatalogPreset_OmittedWhenAbsentAndZeroCfgKept(t *testing.T) {
	b, err := json.Marshal(catalogEntryByID(t, "sd-1-5"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "preset") {
		t.Errorf("an entry without preset must not emit the field: %s", b)
	}
	zero := 0.0
	b, _ = json.Marshal(catalogEntry{Preset: &modelcat.Preset{CfgScale: &zero}})
	if !strings.Contains(string(b), `"preset":{"cfg_scale":0}`) {
		t.Errorf("cfg_scale 0 must be kept and the unset fields omitted: %s", b)
	}
}

func TestCatalogPreset_ServedByTheCatalogEndpoint(t *testing.T) {
	var out struct {
		Entries []struct {
			ID     string          `json:"id"`
			Preset json.RawMessage `json:"preset"`
		} `json:"entries"`
	}
	b, _ := json.Marshal(struct {
		Entries []catalogEntry `json:"entries"`
	}{catalog})
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, e := range out.Entries {
		got[e.ID] = string(e.Preset)
	}
	if got[idKrea] != `{"steps":8,"cfg_scale":1,"resolution":"1024x576"}` || got["sd-1-5"] != "" {
		t.Errorf("served presets: krea %q, sd-1-5 %q", got[idKrea], got["sd-1-5"])
	}
}

const (
	idJugg = "sdxl-juggernaut-xl-v9"
	idPony = "sdxl-pony-diffusion-v6"
)

func TestCatalogSDXL_JuggernautAndPonyAreOpenCheckpointsWithPreset(t *testing.T) {
	for _, id := range []string{idJugg, idPony} {
		e := catalogEntryByID(t, id)
		if e.Kind != "model" || e.RequiresToken != "" || e.VRAMMB <= 0 || e.SizeBytes <= 0 {
			t.Errorf("%s: kind %q token %q vram %d size %d", id, e.Kind, e.RequiresToken, e.VRAMMB, e.SizeBytes)
		}
		if len(e.Files) != 1 || e.Files[0].Role != "model" || !strings.HasPrefix(e.Files[0].URL, "https://huggingface.co/") {
			t.Errorf("%s: files = %+v, want one model file from Hugging Face", id, e.Files)
		}
		p := e.Preset
		if p == nil || p.Steps != 25 || p.CfgScale != nil || p.Resolution != "" {
			t.Errorf("%s: preset = %+v, want only 25 steps", id, p)
		}
	}
}

func TestCatalogPromptHint_OnlyPonyHasOneAndItIsOmittedOtherwise(t *testing.T) {
	for _, e := range catalog {
		has := e.PromptHint != ""
		if has != (e.ID == idPony) {
			t.Errorf("%s: prompt_hint = %q", e.ID, e.PromptHint)
		}
	}
	if !strings.Contains(catalogEntryByID(t, idPony).PromptHint, "score_9, score_8_up, score_7_up") {
		t.Error("the Pony hint must name the score prefixes")
	}
	b, _ := json.Marshal(catalogEntryByID(t, idKrea))
	if strings.Contains(string(b), "prompt_hint") {
		t.Errorf("an entry without a hint must not emit the field: %s", b)
	}
}

func TestCatalogSDXL_InstalledWhenTheCheckpointIsInModels(t *testing.T) {
	s := newAssetServer(t)
	installed := func(id string) bool {
		rec := call(s.Handler(), "GET", "/api/assets/catalog", "")
		var out struct{ Entries []catalogEntry }
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		for _, x := range out.Entries {
			if x.ID == id {
				return x.Installed
			}
		}
		t.Fatalf("%s missing from the served catalog", id)
		return false
	}
	if installed(idJugg) {
		t.Fatal("nothing is installed yet")
	}
	writeAsset(t, s, "model", catalogEntryByID(t, idJugg).Files[0].Name, 1)
	if !installed(idJugg) || installed(idPony) {
		t.Error("only Juggernaut must count as installed")
	}
}
