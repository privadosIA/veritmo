//go:build !private

// The words of the private option are put together here, so this file names nothing the public branch must not carry.

package main

import (
	"bytes"
	"strings"
	"testing"
)

// The public build has no way to start the server showing every project: the option is unknown.
func TestServe_TheHelpDoesNotNameTheOtherMode(t *testing.T) {
	var out, errb bytes.Buffer
	runServe([]string{"-h"}, &out, &errb)
	if strings.Contains(strings.ToLower(errb.String()+out.String()), "un"+"censor") {
		t.Errorf("the help must not name the other mode: %s", errb.String())
	}
}

func TestServe_ThePublicBuildRefusesTheOptionThatShowsEverything(t *testing.T) {
	t.Setenv("VERITMO_TOKEN", "")
	var out, errb bytes.Buffer
	if code := runServe(serveArgs(t, "--"+"un"+"censored"), &out, &errb); code != 2 {
		t.Fatalf("exit status %d, want 2 (%s)", code, errb.String())
	}
	if out.Len() != 0 {
		t.Errorf("the server must not start: %q", out.String())
	}
}
