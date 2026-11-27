package assemble

import (
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func ffmpegOrSkip(t *testing.T) {
	t.Helper()
	for _, n := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(n); err != nil {
			t.Skipf("%s is not installed", n)
		}
	}
}

func run(t *testing.T, name string, args ...string) string {
	t.Helper()
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return string(out)
}

// audioStreams is how many audio streams a file has.
func audioStreams(t *testing.T, file string) int {
	t.Helper()
	out := strings.TrimSpace(run(t, "ffprobe", "-v", "error", "-select_streams", "a", "-show_entries", "stream=index", "-of", "csv=p=0", file))
	if out == "" {
		return 0
	}
	return len(strings.Split(out, "\n"))
}

// maxVolume is the loudest sample of the audio of a file in dB (ffmpeg says -91 dB for digital silence).
func maxVolume(t *testing.T, file string) float64 {
	t.Helper()
	out := run(t, "ffmpeg", "-hide_banner", "-i", file, "-af", "volumedetect", "-vn", "-f", "null", "-")
	m := regexp.MustCompile(`max_volume: (-?[0-9.]+) dB`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no volume in:\n%s", out)
	}
	v, _ := strconv.ParseFloat(m[1], 64)
	return v
}

// A video that has its own sound (an LTX or H3 clip written with audio) under an overlay: the scene's sound is the scene's own track
// (silence here, the narration elsewhere) and the clip's is left out. A music piece over a clip is built this way, so its final video
// has the song and nothing else.
func TestAClipWithSoundUnderAnOverlayKeepsOnlyTheScenesOwnSound(t *testing.T) {
	ffmpegOrSkip(t)
	dir := t.TempDir()
	clip := filepath.Join(dir, "loud.mp4")
	run(t, "ffmpeg", "-v", "error", "-y", "-f", "lavfi", "-i", "testsrc=duration=2:size=320x180:rate=16", "-f", "lavfi", "-i", "sine=frequency=880:duration=2",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", clip)
	if audioStreams(t, clip) != 1 || maxVolume(t, clip) < -20 {
		t.Fatal("the test clip must have sound")
	}
	overlay := filepath.Join(dir, "ov.webm")
	run(t, "ffmpeg", "-v", "error", "-y", "-f", "lavfi", "-i", "color=c=red@0.5:s=320x180:d=2:r=16,format=yuva420p", "-c:v", "libvpx-vp9", "-pix_fmt", "yuva420p", overlay)
	out := filepath.Join(dir, "scene.mp4")
	scene := Scene{ID: "p1", Duration: 2, Silent: true, Width: 320, Height: 180, AnimatedPath: clip, AnimatedDuration: 2, AnimatedSpeed: 1,
		OverlayPath: overlay, OverlayStart: 0, OverlayEnd: 2}
	args := BuildClipArgs(scene, 16, out)
	run(t, "ffmpeg", args...)
	if n := audioStreams(t, out); n != 1 {
		t.Fatalf("the scene has %d audio streams, want one", n)
	}
	if v := maxVolume(t, out); v > -60 {
		t.Fatalf("the clip's own sound is in the scene (loudest sample %.1f dB): only the scene's silence should be", v)
	}
}
