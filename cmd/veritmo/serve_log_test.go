package main

import (
	"log/slog"
	"testing"
)

func TestParseLogLevel(t *testing.T) {
	for in, want := range map[string]slog.Level{"debug": slog.LevelDebug, "info": slog.LevelInfo, "warn": slog.LevelWarn, "error": slog.LevelError} {
		if got, err := parseLogLevel(in); err != nil || got != want {
			t.Errorf("%q: %v %v, want %v", in, got, err, want)
		}
	}
	if _, err := parseLogLevel("loud"); err == nil {
		t.Error("an unknown level must be refused")
	}
}
