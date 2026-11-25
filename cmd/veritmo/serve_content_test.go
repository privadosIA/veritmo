package main

import (
	"bytes"
	"strings"
	"testing"
)

func serveArgs(t *testing.T, extra ...string) []string {
	t.Helper()
	// A bind failure on a non-local address exits right after the startup checks.
	return append([]string{"--addr", "192.0.2.1:1", "--token", "t", "--projects-dir", t.TempDir(), "--db", t.TempDir() + "/o.db"}, extra...)
}

func TestServe_StrictIsAnOptionAndTheHelpSaysSo(t *testing.T) {
	t.Setenv("VERITMO_TOKEN", "")
	var out, errb bytes.Buffer
	if code := runServe(serveArgs(t, "--strict"), &out, &errb); code == 2 {
		t.Fatalf("--strict must be accepted: %s", errb.String())
	}
	out.Reset()
	errb.Reset()
	runServe([]string{"-h"}, &out, &errb)
	help := errb.String() + out.String()
	if !strings.Contains(help, "-strict") {
		t.Errorf("the help must describe --strict: %s", help)
	}
}
