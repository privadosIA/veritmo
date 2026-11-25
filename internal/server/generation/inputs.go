package generation

// inputs.go says what each way of making a video accepts besides the prompt (spec video-generation-api, "The
// options say which inputs each one accepts"): a first and a last picture, and references. What an option takes is a
// property of its engine and bundle; how many references fit is a property of the card.

import "veritmo/internal/project/config"

// inputLimit is how many files of a kind an option takes and, for videos and audios, how long each may be.
type inputLimit struct {
	Min        int     `json:"min,omitempty"`
	Max        int     `json:"max"`
	MaxSeconds float64 `json:"max_seconds,omitempty"`
}

// generationInputs is the `inputs` object of an option. A reference kind that is not there is not accepted; the two
// pictures are always said.
type generationInputs struct {
	StartImage      bool        `json:"start_image"`
	EndImage        bool        `json:"end_image"`
	ReferenceImages *inputLimit `json:"reference_images,omitempty"`
	ReferenceVideos *inputLimit `json:"reference_videos,omitempty"`
	ReferenceAudios *inputLimit `json:"reference_audios,omitempty"`
	// Scene says the option takes the description of a 3D scene in the request (`blender.description`), and no picture.
	Scene bool `json:"scene,omitempty"`
}

// referenceResolution is the resolution a clip made with references is planned at, on every card: the one the limits
// below were measured at (a larger canvas holds more tokens and would not fit the memory of the measurements).
const referenceResolution = "768x416"

// referenceLimits are the most references a card holds, MEASURED on 2026-11-07 with MiniMax H3 Ref2VA at
// referenceResolution (22 frames, the project's flags): the first row that fits the card is the one used. Nothing above
// these was measured: the 9 pictures, 3 videos and 3 audios of other tools are not offered.
var referenceLimits = []struct {
	minVRAMMB    int
	images       int
	videos       int
	videoSeconds float64
	audios       int
	audioSeconds float64
}{
	// RTX 5060 Ti, 16 GB: 6 pictures + 2 videos of 2 and 3 s + 3 audios of 4 s took 129 s and peaked at 15.2 GB.
	{15360, 6, 2, 3, 3, 4},
	// RTX 4070, 12 GB: 2 pictures + 1 video of 2 s + 1 audio of 3 s took 55 s and peaked at 11.3 GB of 12.3.
	{0, 2, 1, 2, 1, 3},
}

// referenceLimitsFor is the limits of the references for a card with this much memory.
func referenceLimitsFor(vramMB int) (images, videos, audios inputLimit) {
	for _, l := range referenceLimits {
		if vramMB >= l.minVRAMMB {
			return inputLimit{Min: 1, Max: l.images}, inputLimit{Max: l.videos, MaxSeconds: l.videoSeconds}, inputLimit{Max: l.audios, MaxSeconds: l.audioSeconds}
		}
	}
	return inputLimit{}, inputLimit{}, inputLimit{}
}

// inputsOf says what an option takes. A first picture is taken by all; a last picture by MiniMax H3 when it runs its
// first-and-last-frame bundle, and by LTX; references only by MiniMax H3 when it runs the Ref2VA bundle (the options
// made for it), which cannot be mixed with a first or a last picture.
func inputsOf(c videoChoice, m planMachine) *generationInputs {
	in := &generationInputs{StartImage: true}
	switch c.Engine {
	case config.EngineMiniMaxH3:
		if c.References {
			in.StartImage = false
			imgs, vids, auds := referenceLimitsFor(m.VRAMMB)
			if imgs.Max > 0 {
				in.ReferenceImages, in.ReferenceVideos, in.ReferenceAudios = &imgs, &vids, &auds
			}
			return in
		}
		in.EndImage = c.FirstFrame
	case config.EngineLTX:
		in.EndImage = true
	}
	return in
}
