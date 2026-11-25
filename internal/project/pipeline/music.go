package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"veritmo/internal/music/diffrhythm2"
	"veritmo/internal/music/heartmula"
	"veritmo/internal/music/music"
	"veritmo/internal/platform/cache"
	"veritmo/internal/platform/events"
	"veritmo/internal/platform/musicsvc"
	"veritmo/internal/platform/runner"
	"veritmo/internal/platform/sched"
	"veritmo/internal/platform/settings"
	"veritmo/internal/project/config"
	"veritmo/internal/project/export"
)

// runMusic drives the music-mode pipeline: per track, compose (only when the
// track uses the language model) → synth (one engine run per variant) →
// export, then the global manifest write. It returns the manifest path for
// the pipeline_end event. base is the project directory for Go-side file
// access; engine arguments, cache entries and event artifacts stay
// project-relative.
func runMusic(ctx context.Context, cfg *config.Config, emitter *events.Emitter, execRunner runner.CommandRunner, manifest *cache.Manifest, onlyFilter cache.OnlyFilter, force bool, env []string, rng func() int64, base string, gpus []string, budget *sched.Budget, noStart bool, services *musicsvc.ServiceHost) (string, error) {
	exportDir := cfg.ExportDir()
	ws := music.Workspace{Base: base}
	m := cfg.Defaults.Music
	modelsDir := m.ResolvedModelsDir()
	format := m.ResolvedOutputFormat()
	synthModel, lmModel := m.ResolvedSynth(), m.ResolvedLM()
	adaptersDir := m.AdaptersDir()
	encoder := music.FindSupport(ws, modelsDir, config.MusicTextEncoderGlob)
	vae := music.FindSupport(ws, modelsDir, config.MusicVAEGlob)
	heartMuLa := m.UsesService() // HeartMuLa or DiffRhythm2: a service in a container with its own weights
	preferGPU := ""
	if heartMuLa {
		// The service has its own weights: none of ACE-Step's models are part of these songs.
		synthModel, lmModel, encoder, vae = serviceSynthModel(m), "", "", ""
		preferGPU = heartMulaPrefer(gpus)
		gpus, budget = heartMulaGPU(gpus), nil
	}

	engine := newMusicEngine(cfg, execRunner, base, preferGPU, noStart, services)
	defer func() { _ = engine.Close() }()
	if engine.Resident() {
		budget = nil // a resident server holds its memory for the whole run: nothing to reserve per phase
	}
	st := &runState{emitter: emitter, manifest: manifest, budget: budget}
	// Each engine program is its own process, so the language model and the
	// synthesis model are reserved separately.
	composeBytes := sched.Estimate(modelBytes(base, filepath.Join(modelsDir, lmModel)))
	synthModelBytes := modelBytes(base, filepath.Join(modelsDir, synthModel), filepath.Join(modelsDir, encoder), filepath.Join(modelsDir, vae))

	// Random seeds are drawn in project order before any track starts, so
	// the same random source gives the same seeds however the tracks are
	// scheduled.
	seedsBy := make([][]int64, len(cfg.Tracks))
	for i, t := range cfg.Tracks {
		if onlyFilter.ShouldProcess(t.ID) {
			seedsBy[i] = music.VariantSeeds(t, t.EffectiveVariants(), rng)
		}
	}
	entriesBy := make([][]export.MusicEntry, len(cfg.Tracks))
	knownChecks := previousChecks(base, exportDir)

	err := runItems(ctx, len(cfg.Tracks), gpus, env, func(ctx context.Context, idx int, env []string) error {
		track := cfg.Tracks[idx]
		if !onlyFilter.ShouldProcess(track.ID) {
			return nil
		}
		n := track.EffectiveVariants()
		useLM := track.EffectiveUseLM() && !heartMuLa
		if heartMuLa && track.Tags == "" {
			return st.fail(track.ID, fmt.Sprintf("synth failed: the %s generator needs tags (style words such as piano,happy): write them in the track", m.ResolvedGenerator()))
		}
		if m.UsesDiffRhythm2() {
			if lang := dr2Language(m, track); lang != "en" {
				return st.fail(track.ID, fmt.Sprintf("synth failed: the diffrhythm2 generator reads English lyrics only, and these look %s: use another generator for this track", lang))
			}
		}

		keys := make([]string, n)
		reqs := make([]music.Request, n)
		composePaths := make([]string, n)
		audioPaths := make([]string, n)
		enriched := make([][]byte, n)
		for i := 0; i < n; i++ {
			keys[i] = cache.VariantKey(track.ID, n, i)
			reqs[i] = music.BuildRequest(cfg, track, seedsBy[idx][i])
			composePaths[i], audioPaths[i] = cache.MusicPaths(keys[i], format)
		}

		// Phase: compose (only for tracks that use the language model).
		if useLM {
			hashes := make([]string, n)
			miss := make([]bool, n)
			anyMiss := false
			for i := 0; i < n; i++ {
				hashes[i] = cache.ComposeHash(ComposeParamsOf(reqs[i], track))
				st.mu.Lock()
				miss[i] = !cache.DecideCompose(manifest, base, keys[i], hashes[i], force)
				if miss[i] && !force {
					if src, ok := cache.FindCompose(manifest, base, hashes[i]); ok && src != composePaths[i] && export.CopyFile(ws.Path(src), ws.Path(composePaths[i])) == nil {
						e := manifest.Scenes[keys[i]]
						e.ComposeHash, e.ComposePath = hashes[i], composePaths[i]
						manifest.UpdateEntry(keys[i], e)
						miss[i] = false
					}
				}
				st.mu.Unlock()
				anyMiss = anyMiss || miss[i]
			}
			if !anyMiss {
				emitter.Skipped(track.ID, "compose")
			} else {
				release, err := st.reserve(ctx, composeBytes)
				if err != nil {
					return err
				}
				emitter.PhaseStart(track.ID, "compose")
				for i := 0; i < n; i++ {
					if !miss[i] {
						continue
					}
					out, err := engine.Compose(ctx, ws, modelsDir, cache.MusicWorkDir(keys[i]), reqs[i], env)
					if err == nil {
						err = writeFileIn(ws, composePaths[i], out)
					}
					if err != nil {
						release()
						return st.fail(track.ID, fmt.Sprintf("compose failed: %v", err))
					}
					st.mu.Lock()
					e := manifest.Scenes[keys[i]]
					e.ComposeHash, e.ComposePath = hashes[i], composePaths[i]
					manifest.UpdateEntry(keys[i], e)
					st.mu.Unlock()
				}
				release()
				emitter.PhaseEnd(track.ID, "compose", composePaths[n-1])
			}
			for i := 0; i < n; i++ {
				b, err := os.ReadFile(ws.Path(composePaths[i]))
				if err != nil {
					return st.fail(track.ID, fmt.Sprintf("compose failed: cannot read %s: %v", composePaths[i], err))
				}
				enriched[i] = b
			}
		}

		// Phase: synth. The request is the enriched one (with the current
		// synthesis settings on top) or, without the language model, the
		// track's own request; its content is what the cache hashes.
		synthReqs := make([][]byte, n)
		hashes := make([]string, n)
		miss := make([]bool, n)
		anyMiss := false
		for i := 0; i < n; i++ {
			b, err := music.SynthRequest(enriched[i], reqs[i])
			if err != nil {
				return st.fail(track.ID, fmt.Sprintf("synth failed: %v", err))
			}
			synthReqs[i] = b
			sp, err := SynthParamsOf(ws, adaptersDir, track, reqs[i], b, synthModel, vae)
			if err != nil {
				return st.fail(track.ID, fmt.Sprintf("synth failed: %v", err))
			}
			hashes[i] = cache.SynthHash(sp)
			st.mu.Lock()
			miss[i] = !cache.DecideSynth(manifest, base, keys[i], hashes[i], force)
			if miss[i] && !force {
				// The same song under another key (the variant kept from several): reuse it, as the
				// engine may not give the same song twice and the one chosen is the one to keep.
				if src, ok := cache.FindSynth(manifest, base, hashes[i]); ok && src != audioPaths[i] && export.CopyFile(ws.Path(src), ws.Path(audioPaths[i])) == nil {
					e := manifest.Scenes[keys[i]]
					e.SynthHash, e.AudioPath = hashes[i], audioPaths[i]
					manifest.UpdateEntry(keys[i], e)
					miss[i] = false
				}
			}
			st.mu.Unlock()
			anyMiss = anyMiss || miss[i]
		}
		if !anyMiss {
			emitter.Skipped(track.ID, "synth")
		} else {
			// The adapter, if any, is merged into the model in memory before it is
			// loaded: it counts towards what the process needs.
			synthBytes := sched.Estimate(synthModelBytes + music.AdapterBytes(ws, adaptersDir, reqs[0].Adapter))
			release, err := st.reserve(ctx, synthBytes)
			if err != nil {
				return err
			}
			emitter.PhaseStart(track.ID, "synth")
			for i := 0; i < n; i++ {
				if !miss[i] {
					continue
				}
				useAdapters := ""
				if reqs[i].Adapter != "" {
					useAdapters = adaptersDir
				}
				if err := synthAudio(ctx, engine, execRunner, ws, modelsDir, useAdapters, cache.MusicWorkDir(keys[i]), synthReqs[i], format, m.MP3BitrateOrDefault(), audioPaths[i], env, music.SourceOf(track)); err != nil {
					release()
					return st.fail(track.ID, fmt.Sprintf("synth failed: %v", err))
				}
				st.mu.Lock()
				e := manifest.Scenes[keys[i]]
				e.SynthHash, e.AudioPath = hashes[i], audioPaths[i]
				manifest.UpdateEntry(keys[i], e)
				st.mu.Unlock()
			}
			release()
			emitter.PhaseEnd(track.ID, "synth", audioPaths[n-1])
		}

		// Cover: a picture for the song, made from its style or the track's own, which goes into the
		// tags of the mp3 and next to it in out/<project>/. Its failure never fails the song.
		coverRel := ""
		switch cfg.CoverMode(track) {
		case config.CoverAuto:
			coverRel = ensureCover(ctx, st, cfg, track, force, execRunner, emitter, env, base)
		case "file":
			coverRel = filepath.Join(config.SourcesSubdir, track.Cover)
		}
		coverFile := ""
		if coverRel != "" {
			coverFile = coverExportName(exportDir, track.ID)
			if ext := filepath.Ext(coverRel); ext != ".png" {
				coverFile = strings.TrimSuffix(coverFile, ".png") + ext
			}
			if err := export.CopyFile(ws.Path(coverRel), ws.Path(coverFile)); err != nil {
				emitter.Warn(track.ID, fmt.Sprintf("cover not exported: %v", err))
				coverFile = ""
			}
		}

		// Phase: export (copy this track's audio into out/<project>/).
		emitter.PhaseStart(track.ID, "export")
		for i := 0; i < n; i++ {
			res, err := music.ParseResolved(synthReqs[i])
			if err != nil {
				return st.fail(track.ID, fmt.Sprintf("export failed: %v", err))
			}
			params, err := music.Params(synthReqs[i])
			if err != nil {
				return st.fail(track.ID, fmt.Sprintf("export failed: %v", err))
			}
			seconds, _ := music.AudioSeconds(ws.Path(audioPaths[i]), format, m.MP3BitrateOrDefault())
			file := filepath.Join(exportDir, config.VariantAudioName(track.ID, n, i, format))
			if err := export.CopyFile(ws.Path(audioPaths[i]), ws.Path(file)); err != nil {
				return st.fail(track.ID, fmt.Sprintf("export failed: %v", err))
			}
			var loudFrom, loudGain *float64
			if m != nil && m.LoudnessLUFS != nil {
				target := m.LoudnessLUFS
				from, gain, err := music.Normalize(ctx, execRunner, ws, file, format, m.MP3BitrateOrDefault(), *target)
				if err != nil {
					emitter.Warn(track.ID, fmt.Sprintf("loudness not evened: %v", err))
				} else if gain != 0 {
					from, gain = math.Round(from*10)/10, math.Round(gain*10)/10
					loudFrom, loudGain = &from, &gain
				}
			}
			title := track.DisplayTitle()
			if n > 1 {
				title = fmt.Sprintf("%s (v%d)", title, i+1)
			}
			if format == config.MusicFormatMP3 {
				tagSong(emitter, ws, track.ID, file, coverRel, music.Tags{
					Title: title, Artist: artistOf(cfg), Album: cfg.Project, Comment: res.Caption, Lyrics: res.Lyrics, Track: idx + 1,
				})
			}
			entry := export.MusicEntry{
				TrackID:         track.ID,
				Variant:         i,
				EffectiveSeed:   seedsBy[idx][i],
				UseLM:           useLM,
				Caption:         res.Caption,
				LyricsSung:      res.Lyrics,
				Instrumental:    track.Instrumental,
				BPM:             res.BPM,
				DurationSeconds: res.Duration,
				AudioSeconds:    seconds,
				KeyScale:        res.KeyScale,
				TimeSignature:   res.TimeSignature,
				VocalLanguage:   res.VocalLanguage,
				SynthModel:      synthModel,
				Params:          params,
				File:            file,
				Title:           title,
				Cover:           coverFile,
				LoudnessFrom:    loudFrom,
				LoudnessGain:    loudGain,
			}
			if sum, err := cache.FileSHA256(ws.Path(file)); err == nil {
				entry.SHA256 = sum
			}
			target := res.Duration
			if heartMuLa {
				target = 0 // the duration asked for is the longest a HeartMuLa song may be, not its length
			}
			entry.Check = checkSong(ctx, execRunner, emitter, knownChecks, track.ID, file, entry.SHA256, target, seconds)
			if track.Task != "" {
				entry.Task, entry.Source = track.Task, track.Source
			}
			entry.Reference = track.Reference
			if !track.Instrumental {
				entry.LyricsRequested = track.Lyrics
			}
			if useLM {
				entry.LMSeed = res.LMSeed
				entry.LMModel = lmModel
			}
			entriesBy[idx] = append(entriesBy[idx], entry)
		}
		emitter.PhaseEnd(track.ID, "export", entriesBy[idx][n-1].File)
		return nil
	})
	if err != nil {
		return "", st.runError(err)
	}

	// Assemble in project order, whatever order the tracks finished in. A run
	// restricted to one track keeps the other tracks' entries from the
	// previous manifest, so regenerating one song does not make the rest of the
	// project disappear from it.
	previous := previousMusicEntries(base, exportDir, onlyFilter)
	var entries []export.MusicEntry
	for idx, track := range cfg.Tracks {
		if onlyFilter.ShouldProcess(track.ID) {
			entries = append(entries, entriesBy[idx]...)
		} else {
			entries = append(entries, keptEntries(base, previous[track.ID])...)
		}
	}

	if err := cache.SaveManifest(joinDir(base, cache.ManifestPath()), manifest); err != nil {
		emitter.Warn("_global", fmt.Sprintf("cannot save cache manifest: %v", err))
	}

	// The mixes read the songs the tracks just exported.
	var mixEntries []export.MixEntry
	var mixErr error
	if len(cfg.Mixes) > 0 {
		mixEntries, mixErr = runMixes(ctx, cfg, emitter, execRunner, base, onlyFilter, force, previousMixEntries(base, exportDir))
		if mixErr != nil && ctx.Err() != nil {
			return "", mixErr
		}
	}

	// Global export step: the mandatory manifest.json.
	emitter.PhaseStart("_global", "export")
	if err := export.RejectCollisions(entries); err != nil {
		return "", fail(emitter, "_global", err.Error())
	}
	manifestPath := filepath.Join(exportDir, "manifest.json")
	if err := os.MkdirAll(joinDir(base, exportDir), 0o755); err != nil {
		return "", fail(emitter, "_global", fmt.Sprintf("export failed: %v", err))
	}
	removeSupersededSongs(base, manifestPath, cfg, entries, onlyFilter)
	mm := export.MusicManifest{
		Project:     cfg.Project,
		Mode:        string(config.ModeMusic),
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Tracks:      entries,
		Mixes:       mixEntries,
	}
	if err := export.WriteMusicManifest(joinDir(base, manifestPath), mm); err != nil {
		return "", fail(emitter, "_global", fmt.Sprintf("export failed: %v", err))
	}
	emitter.PhaseEnd("_global", "export", manifestPath)
	if mixErr != nil {
		return "", mixErr
	}
	return manifestPath, nil
}

// SynthParamsOf builds the synth cache-key inputs of one variant: the request
// handed to the program, the model files, the adapter's size and, for a task,
// the content of the source song. The cache report uses it too, so what the
// report calls a hit is what a run would reuse.
func SynthParamsOf(ws music.Workspace, adaptersDir string, t config.Track, req music.Request, synthReq []byte, synthModel, vae string) (cache.SynthParams, error) {
	p := cache.SynthParams{Request: synthReq, SynthModel: synthModel, VAE: vae, AdapterBytes: music.AdapterBytes(ws, adaptersDir, req.Adapter)}
	if src := music.SourceOf(t); src != nil {
		if src.Path != "" {
			sha, err := cache.FileSHA256(ws.Path(src.Path))
			if err != nil {
				return p, fmt.Errorf("cannot read the source %s: %w", t.Source, err)
			}
			p.SourceSHA = sha
		}
		// A close cover's reference is its source: already covered by SourceSHA.
		if src.Ref != "" && src.Ref != src.Path {
			sha, err := cache.FileSHA256(ws.Path(src.Ref))
			if err != nil {
				return p, fmt.Errorf("cannot read the reference %s: %w", t.Reference, err)
			}
			p.RefSHA = sha
		}
	}
	return p, nil
}

// serverStartHook replaces how the server engine launches ace-server; only
// tests set it.
var serverStartHook func(ctx context.Context, o music.StartOptions) (*music.Process, error)

// newMusicEngine returns the engine the project asks for: the programs, or an
// ace-server started for this run. The caller closes it.
func newMusicEngine(cfg *config.Config, execRunner runner.CommandRunner, base, preferGPU string, noStart bool, services *musicsvc.ServiceHost) music.Engine {
	m := cfg.Defaults.Music
	switch {
	case m.UsesHeartMuLa():
		return newHeartMulaEngine(cfg, execRunner, base, preferGPU, noStart, services)
	case m.UsesDiffRhythm2():
		return newDiffRhythm2Engine(cfg, execRunner, base, preferGPU, noStart, services)
	}
	return newACEEngine(cfg, execRunner, base)
}

// newACEEngine returns the ACE-Step engine the project asks for: the programs, or an ace-server started for this
// run. The soundtrack of a video project always uses it.
func newACEEngine(cfg *config.Config, execRunner runner.CommandRunner, base string) music.Engine {
	m := cfg.Defaults.Music
	if m.ResolvedEngine() == config.EngineServer {
		return music.NewServerEngine(music.ServerOptions{
			ModelsDir:   m.ResolvedModelsDir(),
			AdaptersDir: m.AdaptersDir(),
			KeepLoaded:  m.ResolvedKeepLoaded(),
			Dir:         base,
			Start:       serverStartHook,
		})
	}
	return music.ProgramEngine{Runner: execRunner}
}

// ComposeParamsOf builds the compose cache-key inputs for one variant: what
// the language model is given, and nothing about the synthesis.
func ComposeParamsOf(req music.Request, t config.Track) cache.ComposeParams {
	p := cache.ComposeParams{
		Caption:          req.Caption,
		Lyrics:           req.Lyrics,
		Instrumental:     t.Instrumental,
		BPM:              req.BPM,
		Duration:         req.Duration,
		KeyScale:         req.KeyScale,
		TimeSignature:    req.TimeSignature,
		VocalLanguage:    req.VocalLanguage,
		LMNegativePrompt: req.LMNegativePrompt,
		LMModel:          req.LMModel,
	}
	if req.Seed != nil {
		p.Seed = *req.Seed
	}
	p.LMTemperature = fmtFloat(req.LMTemperature)
	p.LMCfgScale = fmtFloat(req.LMCfgScale)
	p.LMTopP = fmtFloat(req.LMTopP)
	if req.LMTopK != nil {
		p.LMTopK = strconv.Itoa(*req.LMTopK)
	}
	return p
}

func fmtFloat(v *float64) string {
	if v == nil {
		return ""
	}
	return strconv.FormatFloat(*v, 'g', -1, 64)
}

// writeFileIn writes data to a project-relative path, creating its directory.
func writeFileIn(ws music.Workspace, rel string, data []byte) error {
	p := ws.Path(rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o644)
}

// previousMusicEntries reads the entries of the existing manifest, by track,
// when the run is restricted to one track. Without --only, or when the old
// manifest cannot be read or is not a music one, there is nothing to keep:
// an old file never fails a run.
func previousMusicEntries(base, exportDir string, only cache.OnlyFilter) map[string][]export.MusicEntry {
	if !only.Active() {
		return nil
	}
	m, err := export.ReadMusicManifest(joinDir(base, filepath.Join(exportDir, "manifest.json")))
	if err != nil {
		return nil
	}
	byTrack := make(map[string][]export.MusicEntry)
	for _, e := range m.Tracks {
		byTrack[e.TrackID] = append(byTrack[e.TrackID], e)
	}
	return byTrack
}

// removeSupersededSongs deletes the songs the previous manifest lists for a
// track this run produced again that the run no longer produces (a track that
// went from three variants to one leaves its a_v1..a_v3 behind). Only files
// the manifest says belong to that track are touched, never one another entry
// now uses, and a failure to delete is not an error.
func removeSupersededSongs(base, manifestPath string, cfg *config.Config, entries []export.MusicEntry, only cache.OnlyFilter) {
	old, err := export.ReadMusicManifest(joinDir(base, manifestPath))
	if err != nil {
		return
	}
	current := make(map[string]bool, len(entries))
	for _, e := range entries {
		current[filepath.Clean(e.File)] = true
	}
	inProject := make(map[string]bool, len(cfg.Tracks))
	for _, t := range cfg.Tracks {
		inProject[t.ID] = true
	}
	for _, e := range old.Tracks {
		if !inProject[e.TrackID] || !only.ShouldProcess(e.TrackID) || current[filepath.Clean(e.File)] {
			continue
		}
		_ = os.Remove(joinDir(base, e.File))
	}
}

// previousMixEntries returns the mixes of the previous manifest, which is
// what an unchanged mix is compared with; nothing when there is none.
func previousMixEntries(base, exportDir string) []export.MixEntry {
	m, err := export.ReadMusicManifest(joinDir(base, filepath.Join(exportDir, "manifest.json")))
	if err != nil {
		return nil
	}
	return m.Mixes
}

// keptEntries returns the old entries of a track still in the project whose
// audio is still on disk, in variant order: anything else would claim songs
// that are gone.
func keptEntries(base string, old []export.MusicEntry) []export.MusicEntry {
	var kept []export.MusicEntry
	for _, e := range old {
		if fi, err := os.Stat(joinDir(base, e.File)); err == nil && fi.Mode().IsRegular() {
			kept = append(kept, e)
		}
	}
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].Variant < kept[j].Variant })
	return kept
}

// synthAudio runs the engine for one variant. For an MP3 it asks the engine
// for a WAV and encodes it here behind a peak limiter: the engine's own MP3
// output overshoots full scale and crackles on playback.
func synthAudio(ctx context.Context, engine music.Engine, r runner.CommandRunner, ws music.Workspace, modelsDir, adaptersDir, workDir string, request []byte, format string, bitrate int, dst string, env []string, src *music.SourceAudio) error {
	if format != config.MusicFormatMP3 {
		return engine.Synth(ctx, ws, modelsDir, adaptersDir, workDir, request, format, dst, env, src)
	}
	doc := map[string]any{}
	if err := json.Unmarshal(request, &doc); err != nil {
		return err
	}
	doc["output_format"] = music.EngineFormat(config.MusicFormatWAV)
	delete(doc, "mp3_bitrate")
	wavReq, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	wav := dst + ".wav"
	if err := engine.Synth(ctx, ws, modelsDir, adaptersDir, workDir, wavReq, config.MusicFormatWAV, wav, env, src); err != nil {
		return err
	}
	return music.EncodeMP3(ctx, r, ws, wav, dst, bitrate)
}

// artistOf is the artist of the tags: the project's setting, or the project's name.
func artistOf(cfg *config.Config) string {
	if m := cfg.Defaults.Music; m != nil && strings.TrimSpace(m.Artist) != "" {
		return strings.TrimSpace(m.Artist)
	}
	return cfg.Project
}

// tagSong writes the tags and the cover into the exported mp3 (the audio bytes are not touched). When it cannot,
// the song stays as it was exported and a warning says so.
func tagSong(emitter *events.Emitter, ws music.Workspace, trackID, file, coverRel string, tags music.Tags) {
	if coverRel != "" {
		if data, err := os.ReadFile(ws.Path(coverRel)); err == nil {
			tags.Cover, tags.CoverMIME = data, coverMIME(coverRel)
		} else {
			emitter.Warn(trackID, fmt.Sprintf("cover not embedded: %v", err))
		}
	}
	mp3, err := os.ReadFile(ws.Path(file))
	if err == nil {
		err = os.WriteFile(ws.Path(file), music.WithID3(mp3, tags), 0o644)
	}
	if err != nil {
		emitter.Warn(trackID, fmt.Sprintf("tags not written: %v", err))
	}
}

func coverMIME(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	}
	return "image/png"
}

// previousChecks maps "file|sha256" of the songs in the previous manifest to
// their checks, so a song that did not change is not measured again.
func previousChecks(base, exportDir string) map[string]*export.MusicCheck {
	m, err := export.ReadMusicManifest(joinDir(base, filepath.Join(exportDir, "manifest.json")))
	if err != nil {
		return nil
	}
	known := make(map[string]*export.MusicCheck)
	for _, e := range m.Tracks {
		if e.Check != nil && e.SHA256 != "" {
			known[e.File+"|"+e.SHA256] = e.Check
		}
	}
	return known
}

// checkSong measures the exported song and warns about what is wrong with it
// (clipping, long silences, too quiet or too loud, wrong length). It never
// fails the run: without ffmpeg, or when it measures nothing, the song simply
// has no check in the manifest. A song identical to the one in the previous
// manifest keeps that check without running ffmpeg.
func checkSong(ctx context.Context, r runner.CommandRunner, emitter *events.Emitter, known map[string]*export.MusicCheck, trackID, file, sha string, target, seconds float64) *export.MusicCheck {
	c := known[file+"|"+sha]
	if c == nil || sha == "" {
		a := music.Analyze(ctx, r, file, seconds)
		if a.PeakDB == nil && a.LUFS == nil {
			return nil
		}
		c = &export.MusicCheck{PeakDB: a.PeakDB, LUFS: a.LUFS}
		for _, i := range music.Check(a, target, seconds) {
			c.Issues = append(c.Issues, export.MusicIssue{Code: i.Code, Detail: i.Detail})
		}
	}
	for _, i := range c.Issues {
		emitter.Warn(trackID, fmt.Sprintf("%s: %s", filepath.Base(file), i.Detail))
	}
	return c
}

// heartMulaGPU: the songs of the HeartMuLa service are made one at a time, on the card the container is given, so
// the tracks are not spread over several cards.
func heartMulaGPU(gpus []string) []string { return nil }

// heartMulaPrefer is the card the run asked for, when it asked for exactly one.
func heartMulaPrefer(gpus []string) string { // also the card DiffRhythm2 is given
	if len(gpus) == 1 {
		return gpus[0]
	}
	return ""
}

// heartMulaWeights is the folder with the weights of the service: inside the music models directory.
func heartMulaWeights(cfg *config.Config, base string) string {
	return serviceWeights(cfg, base, "heartmula")
}

// serviceWeights is the absolute folder with the weights of a generator (the name of its folder).
func serviceWeights(cfg *config.Config, base, folder string) string {
	dir := joinDir(base, filepath.Join(cfg.Defaults.Music.ResolvedModelsDir(), folder))
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	return dir
}

// serviceHook replaces how the HeartMuLa service is reached; only tests set it.
var heartMulaOpenHook func(ctx context.Context) (*heartmula.Client, error)

// newHeartMulaEngine makes songs with the service: the one on another machine when its address is saved in the
// settings, the container on this machine otherwise.
func newHeartMulaEngine(cfg *config.Config, execRunner runner.CommandRunner, base, preferGPU string, noStart bool, services *musicsvc.ServiceHost) music.Engine {
	if heartMulaOpenHook != nil {
		return &music.HeartMulaEngine{Open: heartMulaOpenHook}
	}
	e := &heartmula.Engine{
		Local:     heartmula.Local{Runner: execRunner, Models: heartMulaWeights(cfg, base), Services: services, Backend: settings.MusicBackend("heartmula")},
		External:  heartmula.ExternalFromSettings(),
		PreferGPU: preferGPU,
		NoStart:   noStart,
	}
	return &music.HeartMulaEngine{Open: e.Open, Release: e.Close}
}

// serviceSynthModel is what the songs of a generator that runs as a service record as their synthesis model.
func serviceSynthModel(m *config.Music) string {
	if m.UsesDiffRhythm2() {
		return music.DiffRhythm2SynthModel
	}
	return music.HeartMulaSynthModel
}

// dr2Language is the language of the lyrics of a track as DiffRhythm2 will read them: the one the track or the
// project names, else the one the lyrics look like (English when they say nothing).
func dr2Language(m *config.Music, t config.Track) string {
	if t.VocalLanguage != "" {
		return t.VocalLanguage
	}
	if m != nil && m.VocalLanguage != "" {
		return m.VocalLanguage
	}
	if lang := music.DetectLanguage(t.Lyrics); lang != "" {
		return lang
	}
	return "en"
}

// diffRhythm2OpenHook replaces how the DiffRhythm2 service is reached; only tests set it.
var diffRhythm2OpenHook func(ctx context.Context) (*diffrhythm2.Client, error)

// newDiffRhythm2Engine makes songs with the service: the one on another machine when its address is saved in the
// settings, the container on this machine otherwise.
func newDiffRhythm2Engine(cfg *config.Config, execRunner runner.CommandRunner, base, preferGPU string, noStart bool, services *musicsvc.ServiceHost) music.Engine {
	if diffRhythm2OpenHook != nil {
		return &music.DiffRhythm2Engine{Open: diffRhythm2OpenHook}
	}
	e := &diffrhythm2.Engine{
		Local:     diffrhythm2.Local{Runner: execRunner, Models: serviceWeights(cfg, base, "diffrhythm2"), Services: services, Backend: settings.MusicBackend("diffrhythm2")},
		External:  diffrhythm2.ExternalFromSettings(),
		PreferGPU: preferGPU,
		NoStart:   noStart,
	}
	return &music.DiffRhythm2Engine{Open: e.Open, Release: e.Close}
}
