//go:build real

package separate

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"veritmo/internal/platform/runner"
)

// With demucs installed (scripts/get-demucs.sh --build) separates 8 seconds of a synthetic stereo wav and checks that the vocal
// stem comes out and the other stems do not stay.
func TestReal_SeparatesAWav(t *testing.T) {
	root := os.Getenv("REAL_ROOT")
	if root == "" {
		root = "../../.."
	}
	bin, model, _, err := Lookup(filepath.Join(root, "bin"), filepath.Join(root, "models", "demucs"))
	if err != nil {
		t.Skipf("demucs is not installed: %v", err)
	}
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	dir := t.TempDir()
	in := filepath.Join(dir, "in.wav")
	if out, err := exec.Command(ff, "-y", "-f", "lavfi", "-i", "sine=frequency=220:duration=8", "-ar", "44100", "-ac", "2", in).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v: %s", err, out)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	vocals, err := Tool{Runner: runner.NewExecRunner(), Binary: bin, Model: model}.Run(ctx, in, filepath.Join(dir, "stems"))
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(vocals); err != nil || fi.Size() < 1000 {
		t.Fatalf("vocals = %q (%v)", vocals, err)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "stems", "*.wav")); len(left) != 1 {
		t.Errorf("stems left = %v, want only the vocals", left)
	}
}
