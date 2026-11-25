// Package modelcat holds the shape of an entry of the model catalog (catalog.json): what a model is, which files it
// is made of, how long it takes and what settings suit it. The data itself is embedded by package server; the
// families that read it (workspace, models, generation) share only these types.
package modelcat

import (
	"net/url"
	"strings"

	"veritmo/internal/platform/licence"
)

// File is one file of an entry: its name, its role in a bundle of several files, where it is downloaded from and, when
// the download is not the file that gets installed, how it is converted.
type File struct {
	Name string `json:"name"`
	// Role says what the file is in a multi-file bundle (model,
	// high_noise_model, text_encoder, clip_l, t5xxl, vae); empty for single-file entries.
	Role string `json:"role,omitempty"`
	URL  string `json:"url"`
	// Convert, when set, says the downloaded file is not the one installed: "int8-convrot:<type>" turns a ComfyUI
	// int8 convrot checkpoint (the only form some Civitai models come in) into a plain GGUF of that quantisation
	// (q8_0, q4_K) before it is put in place. Name is the converted file.
	Convert string `json:"convert,omitempty"`
}

// Entry is one model, or one bundle of models, of the catalog: what it is, where its files come from, how long it takes,
// what settings suit it and how the pages show it.
type Entry struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// NameEN, DescriptionEN and PromptHintEN are the English texts of an entry whose name,
	// description or prompt hint is written in Spanish; the dashboard shows them in English.
	NameEN        string `json:"name_en,omitempty"`
	DescriptionEN string `json:"description_en,omitempty"`
	PromptHintEN  string `json:"prompt_hint_en,omitempty"`
	// Commercial says whether the model's license allows commercial use: "yes", "conditions" (read the
	// note), "no" or "unknown" (could not be confirmed). License names the license and CommercialNote
	// (and its English twin) spells out the conditions; the note is omitted when there are none.
	Commercial       string `json:"commercial,omitempty"`
	License          string `json:"license,omitempty"`
	CommercialNote   string `json:"commercial_note,omitempty"`
	CommercialNoteEN string `json:"commercial_note_en,omitempty"`
	// Licence is the same, as the terms a client shows before the person downloads the entry: set for every entry whose license is not plainly
	// permissive (see FillLicence). URL is the page of the model, where its license text is.
	Licence *licence.Terms `json:"licence,omitempty"`
	// Starter marks a sensible first install for newcomers.
	Starter   bool  `json:"starter,omitempty"`
	SizeBytes int64 `json:"size_bytes"`
	// VRAMMB is a rough minimum GPU memory (MB) to render with a model
	// (an estimate, or the measured peak for a video bundle). Omitted for
	// kinds that do not render.
	VRAMMB int `json:"vram_mb,omitempty"`
	// MinRAMGB is the system RAM (GB) the bundle needs to run: sd copies the
	// whole model into RAM as well as VRAM. Omitted when it is no constraint.
	MinRAMGB int `json:"min_ram_gb,omitempty"`
	// LowVRAM is how the bundle runs on a smaller card: at that resolution it peaks at VRAMMB (measured).
	LowVRAM *LowVRAM `json:"low_vram,omitempty"`
	// RequiresToken names the service whose account key the files need
	// ("civitai"); omitted when none is needed. It is static: whether a key
	// is configured is not reported.
	RequiresToken string `json:"requires_token,omitempty"`
	// Preset is the generation settings measured to work with the model;
	// omitted when the entry makes no recommendation.
	Preset *Preset `json:"preset,omitempty"`
	// PromptHint is advice about the prompt the model needs (e.g. required
	// prefixes), for display only: the server never alters a prompt with it.
	PromptHint string `json:"prompt_hint,omitempty"`
	// Styles lists what an image model is good for ("photo", "anime",
	// "general"); StyleRank orders the candidates of a style (lower is
	// preferred). Quality ("low", "medium", "high") places a video bundle on
	// the editor's quality scale. All are omitted when not declared.
	Styles []string `json:"styles,omitempty"`
	// Adult marks a model made for adult content: a project that uses it is labelled adult, and a server that hides
	// adult projects does not list it.
	Adult bool `json:"adult,omitempty"`
	// Edit marks an editing model (FLUX.1 Kontext): it edits a picture it is given by instruction, so it is not a
	// model to generate pictures with and is never chosen for a look.
	Edit bool `json:"edit,omitempty"`
	// Timing is how long the entry takes to make one picture or one clip on the reference machine
	// (an RTX 5060 Ti with 16 GB): the planner of the dashboard scales it to the settings and the GPU.
	Timing    *Timing `json:"timing,omitempty"`
	StyleRank int     `json:"style_rank,omitempty"`
	Quality   string  `json:"quality,omitempty"`
	Files     []File  `json:"files"`
	Installed bool    `json:"installed"`
}

// LowVRAM is a measured way to run a video bundle on a card with less memory than its preset needs.
type LowVRAM struct {
	VRAMMB     int    `json:"vram_mb"`
	Resolution string `json:"resolution"`
}

// Timing is the time one picture (image models) or one clip (video bundles) takes at the given
// settings on the reference machine. Measured says whether it was timed or is an estimate.
type Timing struct {
	Seconds    int    `json:"seconds"`
	Resolution string `json:"resolution"`
	Steps      int    `json:"steps"`
	// Frames is set for clips only.
	Frames int `json:"frames,omitempty"`
	// TurboSeconds and TurboSteps are the same with the entry's turbo LoRA (MiniMax H3).
	TurboSeconds int  `json:"turbo_seconds,omitempty"`
	TurboSteps   int  `json:"turbo_steps,omitempty"`
	Measured     bool `json:"measured"`
}

// Preset holds recommended settings. Each field is optional: absent
// means "no recommendation", so CfgScale is a pointer (0 is a real value).
type Preset struct {
	Steps      int      `json:"steps,omitempty"`
	CfgScale   *float64 `json:"cfg_scale,omitempty"`
	Resolution string   `json:"resolution,omitempty"`
}

// FillLicence sets Licence from the license fields of the entry, for an entry whose license is not plainly permissive (Commercial is not "yes"). The
// terms carry the English note when there is one, and the address of the model's page (the repository of its first file on Hugging Face or GitHub),
// where the license text is. An entry with no license, or a plainly permissive one, gets none.
func (e *Entry) FillLicence() {
	e.Licence = nil
	if strings.TrimSpace(e.License) == "" || e.Commercial == "" || e.Commercial == licence.Yes {
		return
	}
	summary := strings.TrimSpace(e.CommercialNoteEN)
	if summary == "" {
		summary = strings.TrimSpace(e.CommercialNote)
	}
	page := ""
	if len(e.Files) > 0 {
		page = ModelPage(e.Files[0].URL)
	}
	e.Licence = &licence.Terms{Name: e.License, URL: page, Summary: summary, Commercial: e.Commercial}
}

// ModelPage is the page of the repository a file is downloaded from ("https://huggingface.co/owner/repo/resolve/main/f.gguf" is
// "https://huggingface.co/owner/repo"), or "" for an address that is not on Hugging Face or GitHub.
func ModelPage(fileURL string) string {
	u, err := url.Parse(fileURL)
	if err != nil || u.Scheme != "https" {
		return ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	switch u.Host {
	case "huggingface.co", "github.com":
		if len(parts) >= 2 {
			return "https://" + u.Host + "/" + parts[0] + "/" + parts[1]
		}
	}
	return ""
}
