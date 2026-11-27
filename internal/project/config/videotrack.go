package config

// videotrack.go: the video track of a video project and its layer scenes (spec config, "A project can have a video track and layer scenes").
// A project with a track draws its motion scenes as transparent layers and lays them over the track (the `track` and `stack` phases).

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

// MaxVideoTrackItems is the most items a video track has.
const MaxVideoTrackItems = 64

// MaxTrackScrim is the most of the design's background colour an item can lay over its clip.
const MaxTrackScrim = 0.8

// DefaultTrackScrim is the scrim of an item that says none.
const DefaultTrackScrim = 0.3

var trackItemID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,23}$`)

// MaxLocalScrim is the most of the design's background colour a local scrim can lay over the rows of the verse.
const MaxLocalScrim = 0.8

// The bands of the frame a local scrim can be laid over: where the verse is drawn.
var localScrimBands = map[string]bool{"top": true, "center": true, "bottom": true}

// LocalScrim is a strip of the design's background colour over the rows of the frame where the verse is drawn (Band: top, center or bottom),
// with Scrim of the colour at its flat part (see assemble.LocalScrimStrips).
type LocalScrim struct {
	Band  string  `yaml:"band" json:"band"`
	Scrim float64 `yaml:"scrim" json:"scrim"`
}

// VideoTrackItem is a clip of sources/ played from Start to End, in seconds of the output video. The clip fields mean what they mean in a
// scene with `clip`; Scrim lays that share of the design's background colour over the clip; TransitionSeconds is the crossfade from the
// item before it when the two touch.
type VideoTrackItem struct {
	ID                string   `yaml:"id" json:"id"`
	Clip              string   `yaml:"clip" json:"clip"`
	Start             float64  `yaml:"start" json:"start"`
	End               float64  `yaml:"end" json:"end"`
	ClipStart         *float64 `yaml:"clip_start,omitempty" json:"clip_start,omitempty"`
	ClipEnd           *float64 `yaml:"clip_end,omitempty" json:"clip_end,omitempty"`
	ClipSpeed         *float64 `yaml:"clip_speed,omitempty" json:"clip_speed,omitempty"`
	Scrim             *float64 `yaml:"scrim,omitempty" json:"scrim,omitempty"`
	TransitionSeconds *float64 `yaml:"transition_seconds,omitempty" json:"transition_seconds,omitempty"`
	// LocalScrims are strips of the scrim colour over the bands where a verse is drawn, over Scrim; none for the item as it always was.
	LocalScrims []LocalScrim `yaml:"local_scrims,omitempty" json:"local_scrims,omitempty"`
}

// ScrimValue is the item's scrim with the default filled in.
func (it VideoTrackItem) ScrimValue() float64 {
	if it.Scrim == nil {
		return DefaultTrackScrim
	}
	return *it.Scrim
}

// Seconds is how long the item lasts.
func (it VideoTrackItem) Seconds() float64 { return it.End - it.Start }

// HasVideoTrack says whether the project has a video track.
func (c *Config) HasVideoTrack() bool { return len(c.VideoTrack) > 0 }

// IsLayer says whether the scene is a transparent page laid over the video track instead of a picture of its own.
func (s Scene) IsLayer() bool { return s.Layer }

// knownLength is the length of the project's video when every scene says how long it lasts (silence_seconds); false when a narrated scene's
// length is only known when it is spoken.
func (c *Config) knownLength() (float64, bool) {
	total := 0.0
	for _, s := range c.Scenes {
		if !s.IsSilent() {
			return 0, false
		}
		total += s.SilenceDuration()
	}
	return total, len(c.Scenes) > 0
}

// validateVideoTrack checks video_track and the scenes that say `layer`.
func (c *Config) validateVideoTrack(statPath func(string) string) []error {
	var errs []error
	for _, s := range c.Scenes {
		if !s.Layer {
			continue
		}
		bad := func(format string, a ...any) {
			errs = append(errs, fmt.Errorf("config: scene %q: "+format, append([]any{s.ID}, a...)...))
		}
		if !s.IsMotionGraphic() {
			bad("layer: true needs a motion_graphic or a motion_part: a layer is a page drawn over the video track")
		}
		for name, set := range map[string]bool{"clip": s.Clip != "", "image": s.Image != "", "init_image": s.InitImage != "", "animate": s.Animate, "overlay": s.Overlay != nil, "render": s.Render != nil, "animation": s.Animation != nil} {
			if set {
				bad("layer: true cannot be combined with %s: a layer is a page, not a picture or a clip", name)
			}
		}
	}
	if len(c.VideoTrack) == 0 {
		return errs
	}
	bad := func(i int, format string, a ...any) {
		it := c.VideoTrack[i]
		label := fmt.Sprintf("item #%d", i+1)
		if it.ID != "" {
			label = fmt.Sprintf("item %q (#%d)", it.ID, i+1)
		}
		errs = append(errs, fmt.Errorf("config: video_track %s: "+format, append([]any{label}, a...)...))
	}
	if len(c.Scenes) == 0 {
		errs = append(errs, fmt.Errorf("config: video_track needs scenes: it is the picture under them"))
	}
	if len(c.VideoTrack) > MaxVideoTrackItems {
		errs = append(errs, fmt.Errorf("config: video_track may have up to %d items, it has %d", MaxVideoTrackItems, len(c.VideoTrack)))
	}
	length, known := c.knownLength()
	ids := map[string]bool{}
	for i, it := range c.VideoTrack {
		if !trackItemID.MatchString(it.ID) {
			bad(i, "id must be lower case letters, digits and dashes, up to 24 characters")
		} else if ids[it.ID] {
			bad(i, "id is repeated")
		}
		ids[it.ID] = true
		switch {
		case it.End <= it.Start:
			bad(i, "end (%v) must be greater than start (%v)", it.End, it.Start)
		case it.Start < 0:
			bad(i, "start must be 0 or more, got %v", it.Start)
		case known && it.End > length+0.05:
			bad(i, "ends at %v s but the project lasts %v s", it.End, length)
		}
		if i > 0 && it.Start < c.VideoTrack[i-1].End-0.001 {
			bad(i, "overlaps the item before it: the items of the track follow one another, in order")
		}
		if !ValidClipName(it.Clip) {
			bad(i, "clip %q must be the name of a .mp4, .mov, .webm or .mkv file, not a path", it.Clip)
		} else if _, err := os.Stat(statPath(filepath.Join(SourcesSubdir, it.Clip))); err != nil {
			bad(i, "clip %q not found in %s", it.Clip, SourcesSubdir)
		}
		if sp := it.ClipSpeed; sp != nil && (*sp < MinClipSpeed || *sp > MaxClipSpeed) {
			bad(i, "clip_speed must be between %v and %v, got %v", MinClipSpeed, MaxClipSpeed, *sp)
		}
		if it.ClipStart != nil && *it.ClipStart < 0 {
			bad(i, "clip_start must be 0 or more, got %v", *it.ClipStart)
		}
		if it.ClipEnd != nil {
			start := 0.0
			if it.ClipStart != nil {
				start = *it.ClipStart
			}
			if *it.ClipEnd <= start {
				bad(i, "clip_end must be greater than clip_start (%v), got %v", start, *it.ClipEnd)
			}
		}
		if sc := it.ScrimValue(); sc < 0 || sc > MaxTrackScrim {
			bad(i, "scrim must be between 0 and %v, got %v", MaxTrackScrim, sc)
		}
		seenBand := map[string]bool{}
		for _, l := range it.LocalScrims {
			switch {
			case !localScrimBands[l.Band]:
				bad(i, "local_scrims band must be top, center or bottom, got %q", l.Band)
			case seenBand[l.Band]:
				bad(i, "local_scrims has the band %q twice", l.Band)
			case l.Scrim < 0 || l.Scrim > MaxLocalScrim:
				bad(i, "local_scrims scrim must be between 0 and %v, got %v", MaxLocalScrim, l.Scrim)
			}
			seenBand[l.Band] = true
		}
		if t := it.TransitionSeconds; t != nil {
			if *t < 0 {
				bad(i, "transition_seconds must be 0 or more, got %v", *t)
			} else if i > 0 && *t > 0 && c.VideoTrack[i-1].End >= it.Start-0.001 {
				if limit := minF(c.VideoTrack[i-1].Seconds(), it.Seconds()); *t > limit+0.001 {
					bad(i, "transition_seconds %v is longer than the shorter of the two items it joins (%.2f s)", *t, limit)
				}
			}
		}
	}
	hasLayer := false
	for _, s := range c.Scenes {
		hasLayer = hasLayer || s.Layer
	}
	if !hasLayer && len(c.Scenes) > 0 {
		c.warn("video_track has no layer scene: the scenes are not laid over it, so the track is drawn and then hidden or ignored")
	}
	return errs
}

func minF(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
