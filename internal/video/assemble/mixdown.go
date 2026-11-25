package assemble

import (
	"fmt"
	"strings"

	"veritmo/internal/project/config"
)

// MixdownInput is one clip of a music mix with the file it reads and that
// file's length, which the caller probes.
type MixdownInput struct {
	Path     string
	Duration float64
	Clip     config.Clip
}

// Mixdown fixes the layout every clip is brought to before they are summed.
const (
	mixdownRate  = 44100
	mixdownLimit = 0.95 // linear ceiling of the limiter: overlapping clips never clip
)

// Master is what is done to the sum of the clips: a volume factor (1 leaves
// it) and, optionally, loudness normalisation.
type Master struct {
	Gain      float64
	Normalize bool
}

// MixdownPlan is what BuildMixdown decides: the ffmpeg arguments and the
// length of the result.
type MixdownPlan struct {
	Args    []string
	Seconds float64
}

// usedPart returns the part of the original a clip plays: from trim_start to
// trim_end (the end of the file when absent), clamped to the file.
func usedPart(in MixdownInput) (start, end float64, err error) {
	start = 0
	if in.Clip.TrimStart != nil {
		start = *in.Clip.TrimStart
	}
	end = in.Duration
	if in.Clip.TrimEnd != nil && *in.Clip.TrimEnd < end {
		end = *in.Clip.TrimEnd
	}
	if end-start <= 0 {
		return 0, 0, fmt.Errorf("the part to use (from %v s) is outside the file, which lasts %.1f s", start, in.Duration)
	}
	return start, end, nil
}

// BuildMixdown returns the ffmpeg arguments that render the clips into out:
// each non-muted clip is cut to its used part, faded, scaled, delayed to its
// start and brought to 44.1 kHz stereo; the clips are summed without
// normalisation (so the user's gains are kept) and limited so an overlap of
// loud clips cannot clip. The mix lasts until its last clip ends. A muted
// clip is neither read nor counted.
func BuildMixdown(inputs []MixdownInput, out, format string) (MixdownPlan, error) {
	return BuildMixdownMaster(inputs, out, format, Master{Gain: 1})
}

// BuildMixdownMaster is BuildMixdown with a master volume and normalisation
// applied to the sum, before the limiter. Normalisation (EBU R128 loudnorm, to
// config.NormalizeLUFS) comes after the master volume, so with it the volume
// no longer changes the loudness, only the headroom the normaliser works from.
func BuildMixdownMaster(inputs []MixdownInput, out, format string, master Master) (MixdownPlan, error) {
	var args []string
	var chains, labels []string
	seconds := 0.0
	idx := 0
	for n, in := range inputs {
		if in.Clip.Mute {
			continue
		}
		ts, te, err := usedPart(in)
		if err != nil {
			return MixdownPlan{}, fmt.Errorf("clip %d: %w", n+1, err)
		}
		used := te - ts
		args = append(args, "-i", in.Path)
		c := []string{
			fmt.Sprintf("atrim=start=%s:end=%s", num(ts), num(te)),
			"asetpts=PTS-STARTPTS",
			fmt.Sprintf("aresample=%d", mixdownRate),
			"aformat=sample_fmts=fltp:channel_layouts=stereo",
		}
		if fi := in.Clip.ResolvedFadeIn(); fi > 0 {
			c = append(c, fmt.Sprintf("afade=t=in:st=0:d=%s", num(minF(fi, used))))
		}
		if fo := in.Clip.ResolvedFadeOut(); fo > 0 {
			fo = minF(fo, used)
			c = append(c, fmt.Sprintf("afade=t=out:st=%s:d=%s", num(used-fo), num(fo)))
		}
		if g := in.Clip.ResolvedGain(); g != 1 {
			c = append(c, "volume="+num(g))
		}
		if start := in.Clip.ResolvedStart(); start > 0 {
			ms := num(float64(int64(start*1000 + 0.5)))
			c = append(c, fmt.Sprintf("adelay=delays=%s:all=1", ms))
		}
		label := fmt.Sprintf("c%d", idx)
		chains = append(chains, fmt.Sprintf("[%d:a]%s[%s]", idx, strings.Join(c, ","), label))
		labels = append(labels, "["+label+"]")
		if end := in.Clip.ResolvedStart() + used; end > seconds {
			seconds = end
		}
		idx++
	}
	if idx == 0 {
		return MixdownPlan{}, fmt.Errorf("every clip is muted: there is nothing to mix")
	}
	sum := strings.Join(labels, "") + fmt.Sprintf("amix=inputs=%d:normalize=0:duration=longest:dropout_transition=0", idx)
	if idx == 1 {
		sum = labels[0] + "anull"
	}
	if master.Gain != 1 {
		sum += ",volume=" + num(master.Gain)
	}
	if master.Normalize {
		sum += fmt.Sprintf(",loudnorm=I=%d:TP=-1.5:LRA=11,aresample=%d", config.NormalizeLUFS, mixdownRate)
	}
	graph := strings.Join(chains, ";") + ";" + sum + fmt.Sprintf(",alimiter=limit=%s:level=disabled[out]", num(mixdownLimit))

	full := []string{"-y"}
	full = append(full, args...)
	full = append(full, "-filter_complex", graph, "-map", "[out]", "-ar", fmt.Sprint(mixdownRate), "-ac", "2")
	switch format {
	case config.MusicFormatWAV:
		full = append(full, "-c:a", "pcm_s16le")
	default:
		full = append(full, "-c:a", "libmp3lame", "-q:a", "2")
	}
	full = append(full, out)
	return MixdownPlan{Args: full, Seconds: seconds}, nil
}

func minF(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
