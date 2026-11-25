package music

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"veritmo/internal/platform/musicsvc"
	"veritmo/internal/platform/runner"
	"veritmo/internal/server/generation"
	"veritmo/internal/video/videoedit"
	"veritmo/internal/video/videovision"
)

// visionSetup is a server whose vision service is installed (or not), has its model files (or not) and runs (or
// not). A running service is an httptest server recorded in a state file against this very process, which the
// program accepts for the first seconds after the start.
type visionSetup struct {
	installed, depth, mask, running bool
}

func visionServer(t *testing.T, v visionSetup) (*testServer, *[]string) {
	t.Helper()
	s, _ := newProjectsServer(t)
	t.Cleanup(s.Workspace.Shutdown)
	fr := runner.NewFakeRunner()
	fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
		if inv.Name == "nvidia-smi" {
			return runner.ScriptedResult{Stdout: []byte("0, NVIDIA GeForce RTX 4070, 11000, 12282\n")}
		}
		return runner.ScriptedResult{}
	})
	s.heartMulaRunner = fr

	services, state := t.TempDir(), t.TempDir()
	if v.installed {
		if err := os.MkdirAll(filepath.Join(services, "videovision"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(services, "videovision", "videovision-service.dll"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s.Services = &musicsvc.ServiceHost{Dir: services, StateDir: state, Dotnet: "true"}
	s.videoVisionWeights = t.TempDir()
	for f, ok := range map[string]bool{videovision.DepthFile: v.depth, videovision.MaskFile: v.mask} {
		if ok {
			if err := os.WriteFile(filepath.Join(s.videoVisionWeights, f), []byte("onnx"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	var jobs []string
	if v.running {
		svc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/health":
				_, _ = w.Write([]byte(`{"ready":true,"version":"1","busy":false,"queued":0,"missing":[]}`))
			case "/mask", "/depth", "/control":
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				jobs = append(jobs, r.URL.Path+" "+body["frames_dir"].(string)+" "+body["out_dir"].(string))
				_, _ = w.Write([]byte(`{"frames":3,"provider":"cpu","seconds":0.5,"masks":[{"name":"f_0001.png","box":[1,2,30,40],"coverage":0.2}],"frames_without_people":0}`))
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		t.Cleanup(svc.Close)
		u, _ := url.Parse(svc.URL)
		port, _ := strconv.Atoi(u.Port())
		st, _ := json.Marshal(map[string]any{"pid": os.Getpid(), "port": port, "gpu": 0, "since": time.Now().UTC().Format(time.RFC3339)})
		if err := os.WriteFile(filepath.Join(state, videovision.Container+".json"), st, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s.engines.ttl = 1
	return s, &jobs
}

func ready(s *testServer) (bool, string) {
	return s.Models.Vision().Ready(context.Background())
}

func TestVideoVision_NotInstalledSaysSo(t *testing.T) {
	s, _ := visionServer(t, visionSetup{depth: true, mask: true})
	ok, why := ready(s)
	if ok || !strings.Contains(why, "not installed") {
		t.Errorf("ready = %v %q", ok, why)
	}
}

func TestVideoVision_AMissingModelFileIsNamed(t *testing.T) {
	s, _ := visionServer(t, visionSetup{installed: true, depth: true})
	ok, why := ready(s)
	if ok || !strings.Contains(why, videovision.MaskFile) || strings.Contains(why, videovision.DepthFile) {
		t.Errorf("ready = %v %q, want only the mask file named", ok, why)
	}
	s, _ = visionServer(t, visionSetup{installed: true})
	_, why = ready(s)
	if !strings.Contains(why, videovision.MaskFile) || !strings.Contains(why, videovision.DepthFile) {
		t.Errorf("both files are missing: %q", why)
	}
}

func TestVideoVision_StoppedIsNotAvailableUntilTheUserStartsIt(t *testing.T) {
	s, _ := visionServer(t, visionSetup{installed: true, depth: true, mask: true})
	ok, why := ready(s)
	if ok || !strings.Contains(why, "not running") || !strings.Contains(why, "Environment") {
		t.Errorf("ready = %v %q", ok, why)
	}
}

func TestVideoVision_RunningIsAvailableAndTheJobsGoToIt(t *testing.T) {
	s, jobs := visionServer(t, visionSetup{installed: true, depth: true, mask: true, running: true})
	if ok, why := ready(s); !ok {
		t.Fatalf("not ready: %s", why)
	}
	b := s.Models.Vision()
	sum, err := b.Mask(context.Background(), "/tmp/frames", "/tmp/out")
	if err != nil || sum.Frames != 3 || len(sum.Masks) != 1 || sum.Masks[0].Box[2] != 30 {
		t.Fatalf("mask = %+v, %v", sum, err)
	}
	if _, err := b.Depth(context.Background(), "/tmp/f", "/tmp/o"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Control(context.Background(), "/tmp/f", "/tmp/o", 4); err != nil {
		t.Fatal(err)
	}
	if strings.Join(*jobs, "|") != "/mask /tmp/frames /tmp/out|/depth /tmp/f /tmp/o|/control /tmp/f /tmp/o" {
		t.Errorf("jobs = %v", *jobs)
	}
}

// The tools that need the service are listed unavailable, with the reason, while it is not usable, and the ones that
// do not need it are not affected.
func TestVideoVision_TheToolsThatNeedItFollowItsState(t *testing.T) {
	// The tools that do not use the vision service need ffmpeg and ffprobe, so they are available exactly when those are installed.
	_, noFFmpeg := exec.LookPath("ffmpeg")
	_, noFFprobe := exec.LookPath("ffprobe")
	ffmpegThere := noFFmpeg == nil && noFFprobe == nil
	for _, tc := range []struct {
		name      string
		setup     visionSetup
		available bool
		reason    string
	}{
		{"stopped", visionSetup{installed: true, depth: true, mask: true}, false, "not running"},
		{"missing model", visionSetup{installed: true, depth: true}, false, videovision.MaskFile},
		{"not installed", visionSetup{depth: true, mask: true}, false, "not installed"},
		{"running", visionSetup{installed: true, depth: true, mask: true, running: true}, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := visionServer(t, tc.setup)
			noop := func(context.Context, videoedit.Job) (videoedit.Result, error) { return videoedit.Result{}, nil }
			for _, id := range []string{videoedit.RemoveBackground, videoedit.Depth, videoedit.MotionControl} {
				s.Stack.VideoEdit.Register(id, videoedit.Impl{Run: noop, Available: s.Stack.VideoEdit.NeedsVision})
			}
			rec := doJSON(t, s.Handler(), http.MethodGet, "/api/video-edits/options", nil)
			var out struct {
				Tools []generation.VideoEditToolView `json:"tools"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
			for _, tool := range out.Tools {
				switch tool.ID {
				case "remove-background", "depth", "motion-control":
					if tool.Available != tc.available || (tc.reason != "" && !strings.Contains(tool.Reason, tc.reason)) {
						t.Errorf("%s: available %v (%q), want %v with %q", tool.ID, tool.Available, tool.Reason, tc.available, tc.reason)
					}
				case "scale", "interpolate", "reframe":
					if tool.Available != ffmpegThere || strings.Contains(tool.Reason, "vision") {
						t.Errorf("%s must not depend on the vision service: available %v (%q) with ffmpeg installed = %v", tool.ID, tool.Available, tool.Reason, ffmpegThere)
					}
				}
			}
		})
	}
}

func TestVideoVision_AppearsAmongTheEnginesOfTheEnvironmentScreen(t *testing.T) {
	s, _ := visionServer(t, visionSetup{installed: true, depth: true, mask: true})
	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/music/engines", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var out struct {
		Engines map[string]struct {
			Reason string `json:"reason"`
		} `json:"engines"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	e, ok := out.Engines["videovision"]
	if !ok || e.Reason != "not_running" {
		t.Errorf("engines = %s", rec.Body)
	}
	// stopping what does not run is not an error; an unknown engine names the known ones
	if rec := doJSON(t, s.Handler(), http.MethodPost, "/api/music/engines/videovision/stop", nil); rec.Code != http.StatusOK {
		t.Errorf("stop: %d %s", rec.Code, rec.Body)
	}
	if rec := doJSON(t, s.Handler(), http.MethodPost, "/api/music/engines/nope/stop", nil); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "videovision") {
		t.Errorf("unknown: %d %s", rec.Code, rec.Body)
	}
}
