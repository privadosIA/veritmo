package main

import "testing"

// chdirTemp switches into a fresh temp dir for tests that exercise the
// relative output layout; the working directory goes back when the test ends.
func chdirTemp(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	return dir
}
