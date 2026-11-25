package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"

	"veritmo/internal/music/separate"
	"veritmo/internal/music/transcribe"
	"veritmo/internal/music/wordsbench"
	"veritmo/internal/platform/runner"
)

const wordsUsage = `Usage: veritmo words bench [--songs n] [--out report.json] [--bin-dir bin] [--models-dir models] <project-dir>

Measures how well the words of a song are timed against the attacks of the separated voice (where a syllable starts) and the beats. <project-dir> is a music project (a folder with out/<id>/manifest.json) or a
folder of them; up to n songs with written lyrics are taken (12 by default). Each is heard three ways (one word per segment on
the mix, the time of each token on the mix, the time of each token on the separated voice) and compared with the onsets of the
voice and with the beats. Needs whisper-cli, its model and demucs: without the separator it says it cannot measure and exits 0. It decides "separate: auto" only from at least 8 songs with lyrics of their own.`

// runWords implements `veritmo words bench` (spec music-word-timing, "The timing is measured against the voice").
func runWords(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "bench" {
		fmt.Fprintln(stderr, wordsUsage)
		return 2
	}
	fs := flag.NewFlagSet("words bench", flag.ContinueOnError)
	fs.SetOutput(stderr)
	songs := fs.Int("songs", 12, "How many songs to measure at most")
	out := fs.String("out", "", "Also write the report as JSON to this file")
	binDir := fs.String("bin-dir", "bin", "Folder of the programs (whisper-cli, demucs); the PATH is looked at when it is not there")
	modelsDir := fs.String("models-dir", "models", "Folder of the models (whisper/ and demucs/)")
	fs.Usage = func() { fmt.Fprintln(stderr, wordsUsage); fs.PrintDefaults() }
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return 2
	}
	list, err := wordsbench.Songs(fs.Arg(0), *songs)
	if err != nil {
		fmt.Fprintf(stderr, "words bench: %v\n", err)
		return 1
	}
	if len(list) == 0 {
		fmt.Fprintf(stderr, "words bench: no song with written lyrics under %s (it needs out/<id>/manifest.json)\n", fs.Arg(0))
		return 1
	}
	tools, err := wordsTools(*binDir, *modelsDir)
	if err != nil {
		fmt.Fprintf(stderr, "words bench: %v\n", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	rep := wordsbench.Run(ctx, tools, list, func(line string) { fmt.Fprintln(stderr, line) })
	rep.Machine = machineDescription()
	fmt.Fprint(stdout, wordsbench.Table(rep))
	if *out != "" {
		raw, _ := json.MarshalIndent(rep, "", "  ")
		if err := os.WriteFile(*out, append(raw, '\n'), 0o644); err != nil {
			fmt.Fprintf(stderr, "words bench: %v\n", err)
			return 1
		}
		fmt.Fprintf(stderr, "report written to %s\n", *out)
	}
	return 0
}

// wordsTools finds whisper-cli, its model, ffmpeg and, when they are installed, demucs and its weights.
func wordsTools(binDir, modelsDir string) (wordsbench.Tools, error) {
	find := func(name string) string {
		if p := filepath.Join(binDir, name); fileExists(p) {
			return p
		}
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
		return ""
	}
	whisper, ffmpeg := find("whisper-cli"), find("ffmpeg")
	if whisper == "" || ffmpeg == "" {
		return wordsbench.Tools{}, fmt.Errorf("whisper-cli and ffmpeg are needed (bash scripts/get-whisper.sh)")
	}
	model := whisperModel(filepath.Join(modelsDir, "whisper"))
	if model == "" {
		return wordsbench.Tools{}, fmt.Errorf("no Whisper model in %s (bash scripts/get-whisper.sh)", filepath.Join(modelsDir, "whisper"))
	}
	rn := runner.NewExecRunner()
	tools := wordsbench.Tools{Runner: rn, FFmpeg: ffmpeg, Whisper: transcribe.Tool{Runner: rn, Binary: whisper, Model: model}}
	if demucs := find("demucs"); demucs != "" {
		if weights, _, err := separate.Model(filepath.Join(modelsDir, "demucs")); err == nil {
			tools.Separator = &separate.Tool{Runner: rn, Binary: demucs, Model: weights}
		}
	}
	return tools, nil
}

// whisperModel is the Whisper model of a folder: the large-v3-turbo one when it is there, otherwise the first .bin by name.
func whisperModel(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var names []string
	for _, e := range entries {
		if e.Type().IsRegular() && strings.HasSuffix(e.Name(), ".bin") {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return ""
	}
	sort.Strings(names)
	for _, n := range names {
		if strings.Contains(n, "large-v3-turbo") {
			return filepath.Join(dir, n)
		}
	}
	return filepath.Join(dir, names[0])
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

// machineDescription is the CPU model and the number of threads, for the report.
func machineDescription() string {
	cpu := ""
	if raw, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		for _, l := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(l, "model name") {
				if _, v, ok := strings.Cut(l, ":"); ok {
					cpu = strings.TrimSpace(v)
				}
				break
			}
		}
	}
	if cpu == "" {
		cpu = runtime.GOARCH
	}
	return fmt.Sprintf("%s, %d threads", cpu, runtime.NumCPU())
}
