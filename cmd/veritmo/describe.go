package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"veritmo/internal/assistant/describe"
	"veritmo/internal/assistant/llm"
	"veritmo/internal/platform/edition"
	"veritmo/internal/platform/settings"
)

// runDescribe implements `veritmo describe [--model name] [--detail] <file>`:
// it reads a picture or clip with a local vision model and prints the prompt
// (and nothing else) on stdout. The model used goes to stderr. Like assist it
// dispatches before pipeline flag parsing.
func runDescribe(args []string, stdout, stderr io.Writer) int {
	return realDeps().describe(args, stdout, stderr)
}

func (dp deps) describe(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("describe", flag.ContinueOnError)
	fs.SetOutput(stderr)
	model := fs.String("model", "", "vision model installed in Ollama (default: "+describe.DefaultModel+", else the first one installed)")
	detail := new(bool)
	if dm := edition.DetailModel(); dm != "" { // the public edition has no "more detail" descriptor
		detail = fs.Bool("detail", false, "use the more detailed model ("+dm+")")
	}
	provider := fs.String("provider", "", "provider for this run instead of the one routed to describing (a remote one needs the acknowledgement given in Settings)")
	split := fs.Bool("split", false, "a clip: one prompt per shot instead of one for the whole clip")
	fs.Usage = func() {
		fmt.Fprintf(stderr, "Usage: veritmo describe [--model name] [--provider id] [--split] <picture|clip>\n\n"+
			"Reads a picture (png, jpg, webp) or a clip (mp4, mov, webm, mkv, avi) with a local\n"+
			"vision model and prints the prompt that would draw it; for a clip, a second line\n"+
			"starting with MOTION: has the movement. The file is only read. A picture of a\n"+
			"minor, or whose ages cannot be confirmed, is refused. With --split a clip gives one line\n"+
			"per shot, [start-end] prompt, each followed by its MOTION: line. Ollama is read from the saved setting, else $OLLAMA_HOST.\n\nFlags:\n")
		fs.PrintDefaults()
	}
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
	if len(pos) != 1 {
		fmt.Fprintln(stderr, "describe: give one picture or clip")
		fs.Usage()
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// The provider routed to describing this kind of file, or the one named: a remote one needs
	// the acknowledgement given in Settings (the terminal cannot give it), and nothing falls back
	// to another provider. The built-in Ollama is the usual path.
	kind, kerr := describe.KindOf(pos[0])
	if kerr != nil {
		fmt.Fprintf(stderr, "describe: %v\n", kerr)
		return 2
	}
	routed, rerr := llm.For(describe.TaskOf(kind), *provider)
	if rerr != nil {
		fmt.Fprintf(stderr, "describe: %v\n", rerr)
		return 1
	}
	d := dp.newDescriber()
	if !routed.Builtin {
		d = describe.FromResolved(routed)
	}
	res, err := d.Describe(ctx, pos[0], describe.Options{Model: *model, Detail: *detail, Split: *split})
	if err != nil {
		switch {
		case errors.Is(err, context.Canceled):
			fmt.Fprintln(stderr, "describe: interrupted")
		case errors.Is(err, describe.ErrInvalidInput):
			fmt.Fprintf(stderr, "describe: %v\n", err)
			return 2
		default:
			fmt.Fprintf(stderr, "describe: %v\n", err)
		}
		return 1
	}
	if res.Provider != "" && res.Provider != settings.BuiltinID {
		fmt.Fprintf(stderr, "described by %s (%s)\n", res.Model, res.Provider)
	} else {
		fmt.Fprintf(stderr, "described by %s\n", res.Model)
	}
	if len(res.Shots) > 0 {
		for _, sh := range res.Shots {
			fmt.Fprintf(stdout, "[%.1f-%.1f] %s\n", sh.Start, sh.End, sh.Prompt)
			if sh.Motion != "" {
				fmt.Fprintln(stdout, "MOTION: "+sh.Motion)
			}
		}
		return 0
	}
	fmt.Fprintln(stdout, res.Prompt)
	if res.Motion != "" {
		fmt.Fprintln(stdout, "MOTION: "+res.Motion)
	}
	return 0
}
