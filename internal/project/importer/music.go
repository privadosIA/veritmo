package importer

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
	"veritmo/internal/project/config"
)

// markerRe matches the marker lines of a song entry (case-insensitive).
var markerRe = regexp.MustCompile(`(?i)^(lyrics|language|bpm|duration|key|time\s*signature|seed|variants|tags)\s*:\s*(.*)$`)

// instrumentalRe matches a line that says the song has no vocals.
var instrumentalRe = regexp.MustCompile(`(?i)^\[?instrumental\]?\.?$`)

// MapMusic converts dataset entries into a music project: one track per
// accepted entry. Each entry's full_prompt is read like this: the lines before
// the first marker are the style (the track's prompt); `Lyrics:` starts the
// lyrics (same line and following lines, line breaks kept, up to the next
// marker); Language, BPM, Duration (seconds), Key, Time signature, Seed, Variants
// and Tags (comma-separated, for the HeartMuLa generator) set one field each; a line `Instrumental` (or `Lyrics:
// [Instrumental]`) makes an instrumental track. The title is ignored and
// lora_info is discarded: adapters are chosen in the studio. A bad entry is
// rejected with its reason and the others import; duplicate ids abort.
func MapMusic(entries []Entry, loaderRejected []Rejection, opts Options) (*Result, error) {
	seen := map[string]int{}
	for i, e := range entries {
		if prev, dup := seen[e.ID]; dup {
			return nil, fmt.Errorf("import: duplicate entry id %q (entries %d and %d)", e.ID, prev, i)
		}
		seen[e.ID] = i
	}

	res := &Result{Config: &config.Config{Project: opts.Project, Output: config.Output{Mode: config.ModeMusic}}}
	for _, r := range loaderRejected {
		res.Reports = append(res.Reports, EntryReport{ID: r.ID, Rejected: r.Reason})
		res.Rejected++
	}

	for _, e := range entries {
		track, report := mapMusicEntry(e)
		if report.Rejected != "" {
			res.Reports = append(res.Reports, report)
			res.Rejected++
			continue
		}
		res.Config.Tracks = append(res.Config.Tracks, track)
		res.Reports = append(res.Reports, report)
		res.Imported++
		if track.Instrumental {
			res.Instrumental++
		}
	}
	return res, nil
}

// mapMusicEntry reads one entry. A rejected entry comes back with only its
// report filled in.
func mapMusicEntry(e Entry) (config.Track, EntryReport) {
	report := EntryReport{ID: e.ID}
	reject := func(format string, args ...any) (config.Track, EntryReport) {
		report.Rejected = fmt.Sprintf(format, args...)
		return config.Track{}, report
	}
	if !config.ValidTrackID(e.ID) {
		return reject("id %q cannot be a track id (letters, digits, '.', '_' and '-', starting with a letter or digit)", e.ID)
	}

	track := config.Track{ID: e.ID}
	var style, lyrics []string
	inLyrics, lyricsSeen, instrumental := false, false, false

	for _, line := range strings.Split(strings.ReplaceAll(e.FullPrompt, "\r\n", "\n"), "\n") {
		t := strings.TrimSpace(line)
		if m := markerRe.FindStringSubmatch(t); m != nil {
			name := strings.ToLower(strings.Join(strings.Fields(m[1]), " "))
			val := strings.TrimSpace(m[2])
			if name == "lyrics" {
				if lyricsSeen {
					return reject("second Lyrics block (at most one per entry)")
				}
				lyricsSeen, inLyrics = true, true
				switch {
				case instrumentalRe.MatchString(val):
					instrumental = true
				case val != "":
					lyrics = append(lyrics, val)
				}
				continue
			}
			inLyrics = false
			if err := setTrackField(&track, name, val, &report); err != nil {
				return reject("%v", err)
			}
			continue
		}
		if instrumentalRe.MatchString(t) && !strings.HasPrefix(t, "[") {
			instrumental, inLyrics = true, false
			continue
		}
		if inLyrics {
			lyrics = append(lyrics, strings.TrimRight(line, " \t"))
		} else if t != "" {
			style = append(style, t)
		}
	}

	track.Prompt = strings.Join(strings.Fields(strings.Join(style, " ")), " ")
	if track.Prompt == "" {
		return reject("entry has no style (the lines before the first marker)")
	}
	report.Mapped = append([]string{fmt.Sprintf("prompt ← %d chars", len(track.Prompt))}, report.Mapped...)

	text := strings.Trim(strings.Join(lyrics, "\n"), "\n")
	switch {
	case instrumental:
		track.Instrumental = true
		report.Mapped = append(report.Mapped, "instrumental ← true")
		if text != "" {
			report.Discarded = append(report.Discarded, fmt.Sprintf("lyrics (%d lines): the track is instrumental", strings.Count(text, "\n")+1))
		}
	case text != "":
		track.Lyrics = text
		report.Mapped = append(report.Mapped, fmt.Sprintf("lyrics ← %d lines", strings.Count(text, "\n")+1))
	}
	if e.LoraInfo != "" {
		report.Discarded = append(report.Discarded, fmt.Sprintf("lora_info %q: adapters are chosen in the studio (adapter in scenes.yaml), field ignored", e.LoraInfo))
	}
	return track, report
}

// setTrackField applies one marker line to the track.
func setTrackField(t *config.Track, name, val string, report *EntryReport) error {
	note := func(field, v string) { report.Mapped = append(report.Mapped, fmt.Sprintf("%s ← %s", field, v)) }
	switch name {
	case "language":
		t.VocalLanguage = strings.ToLower(val)
		note("vocal_language", t.VocalLanguage)
	case "key":
		t.KeyScale = val
		note("keyscale", val)
	case "time signature", "timesignature":
		t.TimeSignature = val
		note("timesignature", val)
	case "bpm":
		n, err := strconv.Atoi(val)
		if err != nil || n <= 0 {
			return fmt.Errorf("BPM %q is not a positive integer", val)
		}
		t.BPM = &n
		note("bpm", val)
	case "duration":
		secs, err := strconv.ParseFloat(strings.TrimSuffix(strings.ToLower(val), "s"), 64)
		if err != nil || secs <= 0 {
			return fmt.Errorf("Duration %q is not a positive number of seconds", val)
		}
		t.DurationSeconds = &secs
		note("duration_seconds", val)
	case "seed":
		n, err := strconv.Atoi(val)
		if err != nil || n < 0 {
			return fmt.Errorf("Seed %q is not a non-negative integer", val)
		}
		t.Seed = &n
		note("seed", val)
	case "tags":
		t.Tags = config.NormalizeTags(val)
		if t.Tags != "" {
			note("tags", string(t.Tags))
		}
	case "variants":
		n, err := strconv.Atoi(val)
		if err != nil || n < 1 {
			return fmt.Errorf("Variants %q is not an integer >= 1", val)
		}
		t.Variants = &n
		note("variants", val)
	}
	return nil
}

// validateMusic checks the generated project with the real rules for tracks.
// The music models are stubbed for the check (as the model and the voice are in
// video mode): a failure here always means the tracks are wrong, never that
// something is not installed yet.
func (r *Result) validateMusic() error {
	dir, err := os.MkdirTemp("", "vt-import-models")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	// The stub models live under dir/models/music, the default models directory, so the check uses a relative
	// path inside a project folder, like any real project.
	modelsDir := filepath.Join(dir, config.DefaultMusicModelsDir)
	if err := os.MkdirAll(modelsDir, 0o755); err != nil {
		return err
	}
	for _, f := range []string{config.DefaultMusicLM, config.DefaultMusicSynth, "Qwen3-Embedding-stub.gguf", "vae-stub.gguf"} {
		if err := os.WriteFile(filepath.Join(modelsDir, f), nil, 0o644); err != nil {
			return err
		}
	}
	check := *r.Config
	check.Defaults.Music = &config.Music{}
	if len(check.Tracks) == 0 {
		return nil // nothing imported: nothing to validate (the caller reports it)
	}
	return check.ValidateInDir(dir)
}

// emitMusicYAML renders the generated music scenes.yaml: the tracks the dataset
// supplied, and commented placeholders for the engine settings.
func (r *Result) emitMusicYAML() []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "# Generated by `veritmo import --mode music`. Check the tracks below and\n")
	fmt.Fprintf(&b, "# adjust the engine settings (commented) before creating the songs.\n\n")
	fmt.Fprintf(&b, "project: %s\n", yamlScalar(r.Config.Project))
	r.writeAdult(&b)
	fmt.Fprintf(&b, "\n")
	fmt.Fprintf(&b, "output:\n  mode: music\n\n")
	fmt.Fprintf(&b, "defaults:\n  music:\n")
	fmt.Fprintf(&b, "    # models_dir: models/music\n")
	fmt.Fprintf(&b, "    # output_format: mp3\n")
	fmt.Fprintf(&b, "    # style_prefix: a sound shared by every track\n")
	fmt.Fprintf(&b, "    # adapter: chinese-new-year\n\n")

	tracks := r.Config.Tracks // dataset order is kept
	var doc strings.Builder
	enc := yaml.NewEncoder(&doc)
	enc.SetIndent(2)
	if err := enc.Encode(map[string]any{"tracks": tracks}); err == nil {
		_ = enc.Close()
		b.WriteString(doc.String())
	}
	return []byte(b.String())
}
