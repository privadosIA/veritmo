package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"veritmo/internal/assistant/assist"
	"veritmo/internal/assistant/llm"
	"veritmo/internal/platform/settings"
)

// runAssist implements `veritmo assist <image|motion> "<text>"`: it
// prints the generated prompt (and nothing else) on stdout. Like import and
// serve it dispatches before pipeline flag parsing. The work runs on the CPU
// through a local Ollama; see internal/assistant/assist.
func runAssist(args []string, stdout, stderr io.Writer) int {
	return realDeps().assist(args, stdout, stderr)
}

func (dp deps) assist(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("assist", flag.ContinueOnError)
	fs.SetOutput(stderr)
	model := fs.String("model", "", "Ollama model (default: $VERITMO_ASSIST_MODEL, else "+assist.DefaultModel+")")
	instrumental := fs.Bool("instrumental", false, "music task: write the style only (no lyrics)")
	provider := fs.String("provider", "", "provider for this run instead of the one routed to the assistant (a remote one needs the acknowledgement given in Settings)")
	fs.Usage = func() {
		fmt.Fprintf(stderr, "Usage: veritmo assist [--model name] [--provider id] [--instrumental] <image|motion|music> \"<text>\"\n\n"+
			"Writes an English prompt from a scene description with a local model that runs\n"+
			"on the CPU only (the GPUs are never used). The music task writes a style and\n"+
			"lyrics (STYLE: / LYRICS:). Ollama is read from the saved setting, else $OLLAMA_HOST.\n\nFlags:\n")
		fs.PrintDefaults()
	}
	// Flags may appear before or after the positional arguments.
	var pos []string
	for len(args) > 0 {
		if err := fs.Parse(args); err != nil {
			return 2
		}
		rest := fs.Args()
		if len(rest) == 0 {
			break
		}
		pos = append(pos, rest[0])
		args = rest[1:]
	}
	if len(pos) < 2 {
		fmt.Fprintln(stderr, "assist: give a task (image, motion or music) and a text")
		fs.Usage()
		return 2
	}
	task, err := assist.ParseTask(pos[0])
	if err != nil {
		fmt.Fprintf(stderr, "assist: %v\n", err)
		return 2
	}
	text := strings.Join(pos[1:], " ")
	if err := assist.ValidateText(text); err != nil {
		fmt.Fprintf(stderr, "assist: %v\n", err)
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// The provider routed to the assistant, or the one named: a remote one needs the
	// acknowledgement given in Settings (the terminal cannot give it), and nothing falls
	// back to another provider. The built-in Ollama is the usual path.
	res, rerr := llm.For(settings.TaskAssistant, *provider)
	if rerr != nil {
		fmt.Fprintf(stderr, "assist: %v\n", rerr)
		return 1
	}
	asst := dp.newAssistant(*model)
	if !res.Builtin {
		asst = assist.FromResolved(res, *model)
	}
	var out string
	if task == assist.TaskMusic {
		var m assist.MusicSuggestion
		m, err = asst.Music(ctx, text, *instrumental)
		out = formatMusic(m)
	} else {
		out, err = asst.Run(ctx, task, text)
	}
	if err != nil {
		if errors.Is(err, context.Canceled) {
			fmt.Fprintln(stderr, "assist: interrupted")
		} else {
			fmt.Fprintf(stderr, "assist: %v\n", err)
		}
		return 1
	}
	fmt.Fprintln(stdout, out)
	return 0
}

// formatMusic prints a music suggestion in the format the assistant asks the
// model for, so it can be read back or pasted into a scenes.yaml.
func formatMusic(m assist.MusicSuggestion) string {
	out := "STYLE: " + m.Caption
	if m.Lyrics != "" {
		out += "\nLYRICS:\n" + m.Lyrics
	}
	return out
}
