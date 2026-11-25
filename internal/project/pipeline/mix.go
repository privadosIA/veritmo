package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"veritmo/internal/platform/cache"
	"veritmo/internal/platform/events"
	"veritmo/internal/platform/probe"
	"veritmo/internal/platform/runner"
	"veritmo/internal/project/config"
	"veritmo/internal/project/export"
	"veritmo/internal/video/assemble"
)

// mixGraphVersion changes when the filter graph changes in a way that alters
// the sound, so mixes rendered by an older graph are rendered again.
const mixGraphVersion = "1"

// resolvedClip is a clip with the file it reads.
type resolvedClip struct {
	clip config.Clip
	path string // project-relative
}

// resolveMixInputs finds the audio file of every clip and fails, naming the
// clip, when one does not exist yet (a track that was not generated, a source
// that is gone). Muted clips are not read, so they are not required.
func resolveMixInputs(cfg *config.Config, base string, m config.Mix) ([]resolvedClip, error) {
	out := make([]resolvedClip, len(m.Clips))
	for i, cl := range m.Clips {
		rc := resolvedClip{clip: cl}
		switch {
		case cl.Track != "":
			p, ok := cfg.TrackFile(cl.Track, cl.ResolvedVariant())
			if !ok {
				return nil, fmt.Errorf("clip %d: track %q has no variant %d", i+1, cl.Track, cl.ResolvedVariant())
			}
			rc.path = p
			if !cl.Mute && !isFile(joinDir(base, p)) {
				return nil, fmt.Errorf("clip %d: track %q has no exported song yet (%s): generate the track first", i+1, cl.Track, p)
			}
		default:
			rc.path = filepath.Join(config.SourcesSubdir, cl.File)
			if !cl.Mute && !isFile(joinDir(base, rc.path)) {
				return nil, fmt.Errorf("clip %d: the source %q is not in the project's sources folder", i+1, cl.File)
			}
		}
		out[i] = rc
	}
	return out, nil
}

func isFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

// exportClips describes the clips as they are rendered.
func exportClips(clips []resolvedClip) []export.MixClip {
	out := make([]export.MixClip, len(clips))
	for i, rc := range clips {
		cl := rc.clip
		mc := export.MixClip{
			Track: cl.Track, File: cl.File, Start: cl.ResolvedStart(), Gain: cl.ResolvedGain(), Mute: cl.Mute,
			FadeIn: cl.ResolvedFadeIn(), FadeOut: cl.ResolvedFadeOut(), TrimStart: cl.TrimStart, TrimEnd: cl.TrimEnd,
		}
		if cl.Track != "" {
			mc.Variant = cl.ResolvedVariant()
		}
		out[i] = mc
	}
	return out
}

// mixHash identifies what a mix renders: its clips as described, the output
// format, the graph version and the content of every input that is read. A
// regenerated track therefore changes the hash of the mixes that use it.
func mixHash(base string, m config.Mix, clips []resolvedClip, format string) (string, []export.MixInput, error) {
	h := sha256.New()
	desc, err := json.Marshal(exportClips(clips))
	if err != nil {
		return "", nil, err
	}
	var inputs []export.MixInput
	fmt.Fprintf(h, "graph=%s\x00format=%s\x00clips=%s\x00", mixGraphVersion, format, desc)
	// The master settings only count when set, so mixes rendered before they existed keep their hash.
	if m.ResolvedGain() != 1 {
		fmt.Fprintf(h, "master=%v\x00", m.ResolvedGain())
	}
	if m.Normalize {
		fmt.Fprintf(h, "normalize=%d\x00", config.NormalizeLUFS)
	}
	for i, rc := range clips {
		if rc.clip.Mute {
			continue
		}
		sum, err := cache.FileSHA256(joinDir(base, rc.path))
		if err != nil {
			return "", nil, fmt.Errorf("clip %d: cannot read %s: %w", i+1, rc.path, err)
		}
		fmt.Fprintf(h, "input%d=%s\x00", i, sum)
		inputs = append(inputs, export.MixInput{File: rc.path, SHA256: sum})
	}
	return fmt.Sprintf("%x", h.Sum(nil)), inputs, nil
}

// runMixes renders the project's mixes after its tracks, one phase `mix` per
// mix, in project order. A mix that fails does not stop the others; the first
// failure is returned once every mix had its turn. A mix outside the --only
// filter, or unchanged since the previous manifest, keeps its previous entry.
func runMixes(ctx context.Context, cfg *config.Config, emitter *events.Emitter, execRunner runner.CommandRunner, base string, only cache.OnlyFilter, force bool, previous []export.MixEntry) ([]export.MixEntry, error) {
	byID := make(map[string]export.MixEntry, len(previous))
	for _, e := range previous {
		byID[e.ID] = e
	}
	format := cfg.Defaults.Music.ResolvedOutputFormat()
	var entries []export.MixEntry
	var first error
	for _, m := range cfg.Mixes {
		old, hadOld := byID[m.ID]
		if !only.ShouldProcess(m.ID) {
			if hadOld && isFile(joinDir(base, old.File)) {
				entries = append(entries, old)
			}
			continue
		}
		if ctx.Err() != nil {
			return entries, ctx.Err()
		}
		file := cfg.MixFile(m.ID)
		fail := func(msg string) {
			if first == nil {
				first = fmt.Errorf("mix %q: %s", m.ID, msg)
			}
			emitter.Error(m.ID, msg)
		}
		clips, err := resolveMixInputs(cfg, base, m)
		if err != nil {
			emitter.PhaseStart(m.ID, "mix")
			fail(err.Error())
			continue
		}
		hash, inputs, err := mixHash(base, m, clips, format)
		if err != nil {
			emitter.PhaseStart(m.ID, "mix")
			fail(err.Error())
			continue
		}
		if hadOld && !force && old.Hash == hash && isFile(joinDir(base, file)) {
			emitter.Skipped(m.ID, "mix")
			old.Clips = exportClips(clips)
			old.Inputs = inputs
			old.Gain, old.Normalize = masterGain(m), m.Normalize
			entries = append(entries, old)
			continue
		}

		emitter.PhaseStart(m.ID, "mix")
		seconds, err := renderMix(ctx, execRunner, base, file, format, clips, assemble.Master{Gain: m.ResolvedGain(), Normalize: m.Normalize})
		if err != nil {
			fail(err.Error())
			continue
		}
		emitter.PhaseEnd(m.ID, "mix", file)
		entries = append(entries, export.MixEntry{ID: m.ID, File: file, AudioSeconds: seconds, Hash: hash, Clips: exportClips(clips), Inputs: inputs, Gain: masterGain(m), Normalize: m.Normalize, LUFS: measureMix(ctx, execRunner, file)})
	}
	return entries, first
}

// measureMix reads the loudness of a rendered mix; nil when it cannot be
// measured (silent, or ffmpeg refuses): the manifest simply goes without it.
func measureMix(ctx context.Context, r runner.CommandRunner, file string) *float64 {
	v, err := probe.Loudness(ctx, r, file)
	if err != nil {
		return nil
	}
	v = math.Round(v*10) / 10
	return &v
}

// masterGain is the manifest's record of a mix's master volume: absent when it is 1.
func masterGain(m config.Mix) *float64 {
	if g := m.ResolvedGain(); g != 1 {
		return &g
	}
	return nil
}

// renderMix probes the clips, builds the graph and runs ffmpeg into a
// temporary file next to the result, which replaces it only on success. The
// runner works inside the project directory, so the programs get
// project-relative paths; the files this process touches itself are resolved
// against base.
func renderMix(ctx context.Context, r runner.CommandRunner, base, file, format string, clips []resolvedClip, master assemble.Master) (float64, error) {
	inputs := make([]assemble.MixdownInput, len(clips))
	for i, rc := range clips {
		in := assemble.MixdownInput{Clip: rc.clip, Path: rc.path}
		if !rc.clip.Mute {
			d, err := probe.Duration(ctx, r, rc.path)
			if err != nil {
				return 0, fmt.Errorf("clip %d: cannot read %s: %w", i+1, rc.path, err)
			}
			in.Duration = d.Seconds()
		}
		inputs[i] = in
	}
	if err := os.MkdirAll(filepath.Dir(joinDir(base, file)), 0o755); err != nil {
		return 0, err
	}
	ext := filepath.Ext(file)
	tmp := strings.TrimSuffix(file, ext) + ".part" + ext
	defer os.Remove(joinDir(base, tmp))
	plan, err := assemble.BuildMixdownMaster(inputs, tmp, format, master)
	if err != nil {
		return 0, err
	}
	if _, err := r.Run(ctx, "ffmpeg", plan.Args); err != nil {
		return 0, fmt.Errorf("ffmpeg failed: %w", err)
	}
	if !isFile(joinDir(base, tmp)) {
		return 0, fmt.Errorf("ffmpeg produced no file")
	}
	if err := os.Rename(joinDir(base, tmp), joinDir(base, file)); err != nil {
		return 0, err
	}
	if d, err := probe.Duration(ctx, r, file); err == nil {
		return d.Seconds(), nil
	}
	return plan.Seconds, nil
}
