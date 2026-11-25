package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsLoopbackAddr(t *testing.T) {
	for addr, want := range map[string]bool{
		"127.0.0.1:4666": true, "127.1.2.3:1": true, "[::1]:4666": true, "localhost:4666": true,
		"0.0.0.0:4666": false, ":4666": false, "[::]:4666": false, "192.168.1.5:4666": false,
		"example.com:80": false, "garbage": false,
	} {
		if got := isLoopbackAddr(addr); got != want {
			t.Errorf("isLoopbackAddr(%q) = %v, want %v", addr, got, want)
		}
	}
}

func TestServe_RefusesNonLoopbackWithoutToken(t *testing.T) {
	t.Setenv("VERITMO_TOKEN", "")
	var out, errb bytes.Buffer
	if code := runServe([]string{"--addr", "0.0.0.0:0", "--projects-dir", t.TempDir(), "--db", t.TempDir() + "/o.db"}, &out, &errb); code == 0 {
		t.Fatal("expected non-zero exit")
	}
	if !strings.Contains(errb.String(), "--token") || out.Len() != 0 {
		t.Fatalf("stderr = %q, stdout = %q", errb.String(), out.String())
	}
}

func TestServe_TokenNeverPrinted(t *testing.T) {
	// A bind failure on a non-local address exits right after the startup checks, which
	// lets us inspect output without running a server.
	t.Setenv("VERITMO_TOKEN", "")
	var out, errb bytes.Buffer
	const tok = "very-secret-token"
	isolate(t)
	code := runServe([]string{"--addr", "192.0.2.1:1", "--token", tok, "--projects-dir", t.TempDir(), "--db", migrated(t)}, &out, &errb)
	if code == 0 {
		t.Fatal("expected bind failure on a non-local address")
	}
	if strings.Contains(out.String()+errb.String(), tok) {
		t.Fatal("token leaked in output")
	}
	if strings.Contains(errb.String(), "refusing") {
		t.Fatalf("token must satisfy the non-loopback rule: %q", errb.String())
	}
}

func TestServe_InsecureOptOutWarns(t *testing.T) {
	t.Setenv("VERITMO_TOKEN", "")
	var out, errb bytes.Buffer
	isolate(t)
	runServe([]string{"--addr", "192.0.2.1:1", "--insecure-no-auth", "--projects-dir", t.TempDir(), "--db", migrated(t)}, &out, &errb)
	if !strings.Contains(errb.String(), "WARNING") || strings.Contains(errb.String(), "refusing") {
		t.Fatalf("stderr = %q", errb.String())
	}
}

func TestServe_WebDirMustBeADirectory(t *testing.T) {
	t.Setenv("VERITMO_TOKEN", "")
	t.Setenv("VERITMO_WEB_DIR", "")
	missing := t.TempDir() + "/nope"
	var out, errb bytes.Buffer
	if code := runServe([]string{"--web-dir", missing, "--projects-dir", t.TempDir(), "--db", t.TempDir() + "/o.db"}, &out, &errb); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if !strings.Contains(errb.String(), missing) {
		t.Fatalf("stderr does not name the path: %q", errb.String())
	}
}

func TestServe_WebDirFromTheEnvironment(t *testing.T) {
	t.Setenv("VERITMO_TOKEN", "")
	missing := t.TempDir() + "/nope"
	t.Setenv("VERITMO_WEB_DIR", missing)
	var out, errb bytes.Buffer
	if code := runServe([]string{"--projects-dir", t.TempDir(), "--db", t.TempDir() + "/o.db"}, &out, &errb); code != 2 || !strings.Contains(errb.String(), missing) {
		t.Fatalf("exit %d, stderr %q", code, errb.String())
	}
}

func TestServe_UsageListsWebDir(t *testing.T) {
	var out, errb bytes.Buffer
	runServe([]string{"-h"}, &out, &errb)
	if !strings.Contains(errb.String(), "-web-dir") {
		t.Fatalf("usage: %q", errb.String())
	}
}

func TestServe_TheTokenCanBeAFileAndIsNeverPrinted(t *testing.T) {
	isolate(t)
	t.Setenv("VERITMO_TOKEN", "")
	const tok = "token-in-a-docker-secret"
	file := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(file, []byte(tok+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VERITMO_TOKEN_FILE", file)
	var out, errb bytes.Buffer
	// a bind failure on a non-local address, after the startup checks: the token of the file satisfies the non-loopback rule
	code := runServe([]string{"--addr", "192.0.2.1:1", "--projects-dir", t.TempDir(), "--db", migrated(t)}, &out, &errb)
	if code == 0 || strings.Contains(errb.String(), "refusing") {
		t.Fatalf("%d %q", code, errb.String())
	}
	if strings.Contains(out.String()+errb.String(), tok) {
		t.Fatal("the token of the file leaked in output")
	}
	// a file that cannot be read stops the start and names the file, not a value
	t.Setenv("VERITMO_TOKEN_FILE", filepath.Join(t.TempDir(), "missing"))
	out.Reset()
	errb.Reset()
	if code := runServe([]string{"--addr", "192.0.2.1:1", "--projects-dir", t.TempDir(), "--db", migrated(t)}, &out, &errb); code != 2 || !strings.Contains(errb.String(), "VERITMO_TOKEN_FILE") {
		t.Fatalf("%d %q", code, errb.String())
	}
}
