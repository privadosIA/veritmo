package generation

// inputs_api.go: the last picture and the references of POST /api/generations (spec video-generation-api,
// "A video can end on a picture", "A video can be guided by references"). The parts are read to temporary files,
// checked against what the option announces before anything is created, and saved in the generation's sources/.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"veritmo/internal/project/config"
	"veritmo/internal/server/api"
)

// The kinds of input a generation reports.
const (
	inputStartImage = "start_image"
	inputEndImage   = "end_image"
	inputRefImage   = "reference_image"
	inputRefVideo   = "reference_video"
	inputRefAudio   = "reference_audio"
	inputScene      = "scene"
)

// The multipart parts that carry them.
const (
	partEndImage  = "end_image"
	partRefImages = "reference_images"
	partRefVideos = "reference_videos"
	partRefAudios = "reference_audios"
)

// genInput is one file of a generation besides its start picture, on its way into the project's sources/.
type genInput struct {
	Kind string
	Name string // its name in sources/
	Path string // where it is now: a temporary file, or a file of another generation when it is reused
	// Owned says Path is a temporary file this request removes; a reused file is only copied.
	Owned   bool
	Seconds float64
}

func removeInputs(in []genInput) {
	for _, f := range in {
		if f.Owned && f.Path != "" {
			_ = os.Remove(f.Path)
		}
	}
}

func hasKind(in []genInput, kinds ...string) bool {
	for _, f := range in {
		for _, k := range kinds {
			if f.Kind == k {
				return true
			}
		}
	}
	return false
}

func countKind(in []genInput, kind string) int {
	n := 0
	for _, f := range in {
		if f.Kind == kind {
			n++
		}
	}
	return n
}

// readGenerationInputs reads the input parts of an already parsed multipart request. The caller removes the files
// (removeInputs), also when it returns false.
func readGenerationInputs(w http.ResponseWriter, r *http.Request) ([]genInput, bool) {
	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if ct != "multipart/form-data" || r.MultipartForm == nil {
		return nil, true
	}
	parts := []struct {
		part, kind, base string
		valid            func(string) bool
		what             string
		single           bool
	}{
		{partEndImage, inputEndImage, "final", config.ValidImageName, "a .png, .jpg, .jpeg or .webp file", true},
		{partRefImages, inputRefImage, "ref-imagen", config.ValidImageName, "a .png, .jpg, .jpeg or .webp file", false},
		{partRefVideos, inputRefVideo, "ref-video", config.ValidClipName, "an .mp4, .mov, .webm or .mkv file", false},
		{partRefAudios, inputRefAudio, "ref-audio", config.ValidReferenceAudioName, "an .mp3, .wav, .m4a, .aac, .ogg or .flac file", false},
	}
	var out []genInput
	fail := func() ([]genInput, bool) { removeInputs(out); return nil, false }
	for _, p := range parts {
		files := r.MultipartForm.File[p.part]
		if p.single && len(files) > 1 {
			api.Refuse(w, "%s takes one picture, %d came", p.part, len(files))
			return fail()
		}
		for i, hdr := range files {
			ext := strings.ToLower(filepath.Ext(hdr.Filename))
			if ext == ".jpe" {
				ext = ".jpg"
			}
			name := p.base + ext
			if !p.single {
				name = fmt.Sprintf("%s-%d%s", p.base, i+1, ext)
			}
			if !p.valid(name) {
				api.Refuse(w, "%s must be %s (%q is not)", p.part, p.what, hdr.Filename)
				return fail()
			}
			f, err := hdr.Open()
			if err != nil {
				api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("the %s part cannot be read", p.part))
				return fail()
			}
			tmp, err := os.CreateTemp("", "generation-*"+ext)
			if err != nil {
				_ = f.Close()
				api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
				return fail()
			}
			n, err := io.Copy(tmp, io.LimitReader(f, config.MaxSourceBytes+1))
			_ = f.Close()
			_ = tmp.Close()
			in := genInput{Kind: p.kind, Name: name, Path: tmp.Name(), Owned: true}
			out = append(out, in)
			switch {
			case err != nil || n == 0:
				api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("the %s part %q is empty or cannot be read", p.part, hdr.Filename))
				return fail()
			case n > config.MaxSourceBytes:
				api.WriteError(w, http.StatusRequestEntityTooLarge, api.CodeInvalid, fmt.Sprintf("the %s part %q is larger than %d MB", p.part, hdr.Filename, config.MaxSourceBytes>>20))
				return fail()
			}
		}
	}
	return out, true
}

// ffprobeAudio reads an audio file: its length and whether it has sound (the Handler's probeAudio, which tests replace).
func ffprobeAudio(ctx context.Context, path string) (seconds float64, hasAudio bool, err error) {
	raw, err := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-print_format", "json", "-show_streams", "-show_format", path).Output()
	if err != nil {
		return 0, false, fmt.Errorf("ffprobe cannot read the file: %w", err)
	}
	var p struct {
		Streams []struct {
			CodecType string `json:"codec_type"`
			Duration  string `json:"duration"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return 0, false, err
	}
	for _, s := range p.Streams {
		if s.CodecType == "audio" {
			hasAudio = true
		}
	}
	seconds, _ = strconv.ParseFloat(p.Format.Duration, 64)
	return seconds, hasAudio, nil
}

// optionsWith lists the available options that satisfy ok, for the refusals that say which ones do.
func optionsWith(options []videoOptionView, ok func(*generationInputs) bool) string {
	var ids []string
	for _, o := range options {
		if o.Available && o.Inputs != nil && ok(o.Inputs) {
			ids = append(ids, o.ID)
		}
	}
	if len(ids) == 0 {
		return "none is available on this machine"
	}
	return strings.Join(ids, ", ")
}

// limitSlack lets a file measured at the limit by ffprobe pass (a 2 s file is often 2.02 s).
const limitSlack = 0.1

// checkGenerationInputs applies the rules of the spec, in the order of the design, to the inputs sent with a request
// for option opt: it returns the refusal, or "" when they are fine. start says whether a start picture came. It
// fills the length of the videos and audios.
func (h *Handler) checkGenerationInputs(ctx context.Context, opt videoOptionView, options []videoOptionView, start bool, in []genInput) string {
	if len(in) == 0 {
		return ""
	}
	end := hasKind(in, inputEndImage)
	refs := hasKind(in, inputRefImage, inputRefVideo, inputRefAudio)
	// 1. the option takes the input
	if end && (opt.Inputs == nil || !opt.Inputs.EndImage) {
		return fmt.Sprintf("%s: option %s cannot end on a picture; the options that can: %s", partEndImage, opt.ID, optionsWith(options, func(i *generationInputs) bool { return i.EndImage }))
	}
	if refs && (opt.Inputs == nil || opt.Inputs.ReferenceImages == nil) {
		return fmt.Sprintf("option %s takes no references (reference_images, reference_videos, reference_audios); the options that do: %s", opt.ID, optionsWith(options, func(i *generationInputs) bool { return i.ReferenceImages != nil }))
	}
	// 2. the mixes that cannot be
	if refs && (start || end) {
		return "references cannot be combined with a start picture (image) or a last picture (end_image): send the references alone, the first reference picture anchors the scene"
	}
	if end && !start {
		return "end_image needs the start picture too (image): a last picture needs a first one"
	}
	// 3. the counts
	if refs {
		for _, c := range []struct {
			kind, part string
			limit      *inputLimit
		}{{inputRefImage, partRefImages, opt.Inputs.ReferenceImages}, {inputRefVideo, partRefVideos, opt.Inputs.ReferenceVideos}, {inputRefAudio, partRefAudios, opt.Inputs.ReferenceAudios}} {
			n := countKind(in, c.kind)
			max := 0
			if c.limit != nil {
				max = c.limit.Max
			}
			if n > max {
				return fmt.Sprintf("%s: %d sent, option %s accepts at most %d on this machine", c.part, n, opt.ID, max)
			}
		}
	}
	// 4. the type of every file and the length of the videos and audios
	for i := range in {
		f := &in[i]
		switch f.Kind {
		case inputEndImage, inputRefImage:
			part := partEndImage
			if f.Kind == inputRefImage {
				part = partRefImages
			}
			info, err := h.host.VideoEdit().Probe(ctx, f.Path)
			if err != nil || info.Width <= 0 || info.Height <= 0 {
				return fmt.Sprintf("%s: the picture %s cannot be read as a picture", part, f.Name)
			}
		case inputRefVideo:
			info, err := h.host.VideoEdit().Probe(ctx, f.Path)
			if err != nil || info.Width <= 0 || info.Duration <= 0 {
				return fmt.Sprintf("%s: %s is not a video with a picture", partRefVideos, f.Name)
			}
			f.Seconds = info.Duration
			if max := opt.Inputs.ReferenceVideos.MaxSeconds; max > 0 && info.Duration > max+limitSlack {
				return fmt.Sprintf("%s: %s lasts %.1f s, option %s accepts at most %g s", partRefVideos, f.Name, info.Duration, opt.ID, max)
			}
		case inputRefAudio:
			secs, has, err := h.probeAudio(ctx, f.Path)
			if err != nil || !has {
				return fmt.Sprintf("%s: %s has no sound", partRefAudios, f.Name)
			}
			f.Seconds = secs
			if max := opt.Inputs.ReferenceAudios.MaxSeconds; max > 0 && secs > max+limitSlack {
				return fmt.Sprintf("%s: %s lasts %.1f s, option %s accepts at most %g s", partRefAudios, f.Name, secs, opt.ID, max)
			}
		}
	}
	// 5. a reference without a picture
	if refs && !hasKind(in, inputRefImage) {
		return "references need at least one reference picture (reference_images): it anchors the scene and is the first reference"
	}
	return ""
}
