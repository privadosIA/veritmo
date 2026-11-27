package musicmotion

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// projectDigest lists scenes.yaml and the hash of every file of motion/ of a written project, so that two writes can be compared.
func projectDigest(t *testing.T, dir string) string {
	t.Helper()
	yaml, err := os.ReadFile(filepath.Join(dir, "scenes.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	b.Write(yaml)
	entries, _ := os.ReadDir(filepath.Join(dir, "motion"))
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, n := range names {
		raw, _ := os.ReadFile(filepath.Join(dir, "motion", n))
		sum := sha256.Sum256(raw)
		fmt.Fprintf(&b, "motion/%s %s\n", n, hex.EncodeToString(sum[:8]))
	}
	return b.String()
}

// TestAProjectWithNoBackgroundIsWrittenAsBefore pins the project of a script with no background (scenes.yaml and the files of motion/) to what
// was written before pieces could have a video behind them: its scenes keep their cache.
func TestAProjectWithNoBackgroundIsWrittenAsBefore(t *testing.T) {
	dir := t.TempDir()
	spec := shortSpec(t, filepath.Join(dir, "proj"), makeSongOrFake(t, dir))
	if _, err := WriteProject(spec); err != nil {
		t.Fatal(err)
	}
	got := projectDigest(t, spec.Dir)
	path := filepath.Join("testdata", "project_nobackground.golden.txt")
	if *updateGolden {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("no golden project (run with -update-golden once): %v", err)
	}
	if got != string(want) {
		t.Fatalf("the project of a script with no background changed:\n%s\n--- want\n%s", got, want)
	}
}
