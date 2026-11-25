package videovision

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/platform/musicsvc"
)

func TestMissing_NamesTheFilesThatAreNotThereOrAreEmpty(t *testing.T) {
	dir := t.TempDir()
	if got := Missing(dir); len(got) != 2 {
		t.Fatalf("empty folder: %v", got)
	}
	if err := os.WriteFile(filepath.Join(dir, DepthFile), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := Missing(dir); len(got) != 1 || got[0] != MaskFile {
		t.Errorf("depth only: %v", got)
	}
	if err := os.WriteFile(filepath.Join(dir, MaskFile), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := Missing(dir); len(got) != 1 || got[0] != MaskFile {
		t.Errorf("an empty file is not a model: %v", got)
	}
	if got := Missing(dir, DepthFile); len(got) != 0 {
		t.Errorf("asking only for the depth model: %v", got)
	}
}

func TestSpec_RunsAsAServiceOnTheCPU(t *testing.T) {
	sp := Spec()
	if sp.Service.DLL != "videovision-service.dll" || sp.Service.Dir != "videovision" || sp.Service.Prefix != "VIDEOVISION" || sp.Service.Device {
		t.Errorf("service = %+v", sp.Service)
	}
	core := Engine{}.Core()
	if !core.NoStart {
		t.Error("the program never starts it by itself")
	}
}

func TestClient_SendsTheFoldersAndReadsTheSummary(t *testing.T) {
	var path string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&body)
		if strings.HasPrefix(r.URL.Path, "/depth") {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"code":"unavailable","error":"the model file depth-anything-v2-small.onnx is missing"}`))
			return
		}
		_, _ = w.Write([]byte(`{"frames":2,"provider":"cpu","seconds":1.5,"masks":[{"name":"a.png","box":[1,2,3,4],"coverage":0.1},{"name":"b.png","box":null,"coverage":0}],"frames_without_people":1}`))
	}))
	defer srv.Close()
	c := musicsvc.NewClient("Video vision", strings.TrimPrefix(srv.URL, "http://"), "")

	sum, err := Control(context.Background(), c, "/f", "/o", 4)
	if err != nil {
		t.Fatal(err)
	}
	if path != "/control" || body["frames_dir"] != "/f" || body["out_dir"] != "/o" || body["dilate"] != float64(4) {
		t.Errorf("sent %s %v", path, body)
	}
	if sum.Frames != 2 || len(sum.Masks) != 2 || sum.Masks[1].Box != nil || sum.FramesWithoutPeople == nil || *sum.FramesWithoutPeople != 1 {
		t.Errorf("summary = %+v", sum)
	}
	if _, err := Mask(context.Background(), c, "/f", "/o"); err != nil || path != "/mask" {
		t.Errorf("mask: %v %s", err, path)
	}
	if _, err := Depth(context.Background(), c, "/f", "/o"); err == nil || !strings.Contains(err.Error(), "depth-anything-v2-small.onnx is missing") {
		t.Errorf("a refusal of the service must reach the caller with its words: %v", err)
	}
}
