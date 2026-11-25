package api

import (
	"bytes"
	"encoding/json"
	"io"
	"testing"

	"veritmo/internal/platform/events"
)

// TestEventLog_VerbatimBytes is the regression test for the marshal-once
// guarantee (design D2): the same emitter write reaches the CLI-style
// stream, the in-memory log, and the persistence sink byte-for-byte.
func TestEventLog_VerbatimBytes(t *testing.T) {
	var persisted [][]byte
	log := NewEventLog(func(seq int, line []byte) {
		persisted = append(persisted, append([]byte(nil), line...))
	})
	var cli bytes.Buffer

	// One emitter, fanned out exactly like the CLI (--json to a stream)
	// and the server (EventLog) consume it.
	emitter := events.New(io.MultiWriter(&cli, log), true)
	emitter.PipelineStart()
	emitter.PhaseStart("intro", "tts")
	emitter.PhaseEnd("intro", "tts", "tmp/scene_intro.wav")
	emitter.Skipped("intro", "render")
	emitter.Warn("_global", "free VRAM low")
	emitter.Done("out/demo.mp4")

	cliLines := bytes.Split(bytes.TrimRight(cli.Bytes(), "\n"), []byte("\n"))
	if len(cliLines) != 6 {
		t.Fatalf("expected 6 CLI lines, got %d", len(cliLines))
	}
	log.mu.Lock()
	stored := log.lines
	log.mu.Unlock()
	if len(stored) != 6 || len(persisted) != 6 {
		t.Fatalf("log=%d persisted=%d, want 6/6", len(stored), len(persisted))
	}
	for i := range cliLines {
		if !bytes.Equal(cliLines[i], stored[i]) {
			t.Errorf("line %d: log differs from CLI stream\ncli: %s\nlog: %s", i, cliLines[i], stored[i])
		}
		if !bytes.Equal(cliLines[i], persisted[i]) {
			t.Errorf("line %d: persisted differs from CLI stream", i)
		}
		var obj map[string]any
		if err := json.Unmarshal(stored[i], &obj); err != nil {
			t.Errorf("line %d is not one JSON object: %v", i, err)
		}
	}
}
