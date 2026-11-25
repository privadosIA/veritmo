package music

import (
	"context"

	"veritmo/internal/platform/runner"
)

// Engine runs the two engine phases for the pipeline: compose (request to
// enriched request) and synth (request, optional source song, to an audio
// file). The programs engine starts one short-lived process per call; the
// server engine sends the same requests to one long-lived ace-server. The
// pipeline does not know which it has: requests, cache hashes, files and events
// are the same.
type Engine interface {
	Compose(ctx context.Context, ws Workspace, modelsDir, workDir string, req Request, env []string) ([]byte, error)
	Synth(ctx context.Context, ws Workspace, modelsDir, adaptersDir, workDir string, request []byte, format, dst string, env []string, src *SourceAudio) error
	// Close releases what the engine holds (the server process); it is safe to
	// call more than once.
	Close() error
	// Resident reports whether the engine keeps its models in memory for the
	// whole run, in which case per-phase memory reservations do not apply.
	Resident() bool
}

// ProgramEngine runs ace-lm and ace-synth, one process per call.
type ProgramEngine struct{ Runner runner.CommandRunner }

// Compose runs ace-lm.
func (e ProgramEngine) Compose(ctx context.Context, ws Workspace, modelsDir, workDir string, req Request, env []string) ([]byte, error) {
	return Compose(ctx, e.Runner, ws, modelsDir, workDir, req, env)
}

// Synth runs ace-synth.
func (e ProgramEngine) Synth(ctx context.Context, ws Workspace, modelsDir, adaptersDir, workDir string, request []byte, format, dst string, env []string, src *SourceAudio) error {
	return Synth(ctx, e.Runner, ws, modelsDir, adaptersDir, workDir, request, format, dst, env, src)
}

// Close does nothing: the processes end with each call.
func (ProgramEngine) Close() error { return nil }

// Resident is false: every call loads and frees its own models.
func (ProgramEngine) Resident() bool { return false }
