package describe

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"veritmo/internal/platform/runner"
)

func needFFmpeg(t *testing.T) {
	t.Helper()
	for _, b := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(b); err != nil {
			t.Skipf("%s not installed", b)
		}
	}
}

func TestExtractFramesImageAndVideo(t *testing.T) {
	needFFmpeg(t)
	dir := t.TempDir()
	png := filepath.Join(dir, "big.png")
	mp4 := filepath.Join(dir, "c.mp4")
	for _, args := range [][]string{
		{"-y", "-v", "error", "-f", "lavfi", "-i", "testsrc=size=2000x1000:rate=1", "-frames:v", "1", png},
		{"-y", "-v", "error", "-f", "lavfi", "-i", "testsrc=size=320x240:rate=5", "-t", "10", "-pix_fmt", "yuv420p", mp4},
	} {
		if out, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
			t.Fatalf("ffmpeg: %v\n%s", err, out)
		}
	}
	r := runner.NewExecRunner()
	img, err := extractFrames(context.Background(), r, png, KindImage)
	if err != nil || len(img) != 1 || len(img[0]) == 0 {
		t.Fatalf("image frame: %v", err)
	}
	vid, err := extractFrames(context.Background(), r, mp4, KindVideo)
	if err != nil || len(vid) != 4 {
		t.Fatalf("a 10 s clip gets 4 frames, got %d (%v)", len(vid), err)
	}
	// Nothing is left behind in the temp dir.
	left, _ := filepath.Glob(filepath.Join(os.TempDir(), "describe-*"))
	if len(left) != 0 {
		t.Fatalf("temporary frames remain: %v", left)
	}
	if _, err := extractFrames(context.Background(), r, filepath.Join(dir, "missing.png"), KindImage); err == nil {
		t.Fatal("a missing file must fail")
	}
}
