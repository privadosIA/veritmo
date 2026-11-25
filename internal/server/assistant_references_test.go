package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"veritmo/internal/music/motionref"
	"veritmo/internal/platform/runner"
)

// refServer is a server whose settings folder is the test's own, with yt-dlp and ffprobe scripted and an analysis that
// writes two frames and answers at once (or waits for the context when block is given).
func refServer(t *testing.T, duration string, block chan struct{}) (*Server, *runner.FakeRunner) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	s := newTestServer(t)
	fr := runner.NewFakeRunner()
	fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
		joined := strings.Join(inv.Args, " ")
		switch {
		case inv.Name == "ffprobe":
			if strings.HasSuffix(inv.Args[len(inv.Args)-1], ".txt") {
				return runner.ScriptedResult{Err: errors.New("no video stream")}
			}
			return runner.ScriptedResult{Stdout: []byte("codec_type=video\nduration=" + duration + "\n")}
		case inv.Name == "ffmpeg": // a picture of a shot: the last argument is the file
			_ = os.WriteFile(inv.Args[len(inv.Args)-1], []byte("shot picture"), 0o644)
			return runner.ScriptedResult{}
		case strings.Contains(joined, "--print"):
			return runner.ScriptedResult{Stdout: []byte(duration + "\nA Fine Video\n")}
		default: // the download
			for i, a := range inv.Args {
				if a == "-o" {
					_ = os.WriteFile(strings.Replace(inv.Args[i+1], "%(ext)s", "mp4", 1), []byte("VIDEO"), 0o644)
				}
			}
			return runner.ScriptedResult{}
		}
	})
	s.mediaRunner = fr
	s.look = fakeLook
	s.assistant.RefAnalyse = func(ctx context.Context, video, framesDir string) (*motionref.Analysis, error) {
		if block != nil {
			select {
			case <-block:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		if _, err := os.Stat(video); err != nil {
			return nil, err
		}
		_ = os.WriteFile(filepath.Join(framesDir, "00.jpg"), []byte("jpg"), 0o644)
		return &motionref.Analysis{Duration: 10, FPS: 30, Width: 1280, Height: 720, Frames: []string{"00.jpg"}, Colours: []motionref.Colour{{Hex: "#112233", Share: 1}}, Thirds: [][]motionref.Colour{}}, nil
	}
	return s, fr
}

func addReference(t *testing.T, s *Server, body string) (int, motionref.Reference) {
	t.Helper()
	rec := call(s.Handler(), http.MethodPost, "/api/music-motion/references", body)
	var ref motionref.Reference
	_ = json.Unmarshal(rec.Body.Bytes(), &ref)
	return rec.Code, ref
}

func getReference(t *testing.T, s *Server, id string) (int, motionref.Reference) {
	t.Helper()
	rec := call(s.Handler(), http.MethodGet, "/api/music-motion/references/"+id, "")
	var ref motionref.Reference
	_ = json.Unmarshal(rec.Body.Bytes(), &ref)
	return rec.Code, ref
}

func TestReferences_AnAddressIsAcceptedAndAnalysedInTheBackground(t *testing.T) {
	s, _ := refServer(t, "95.5", nil)
	code, ref := addReference(t, s, `{"url":"`+goodURL+`","notes":"neon","tags":["pop"]}`)
	if code != http.StatusAccepted || ref.State != motionref.StateAnalysing || ref.Title != "A Fine Video" || ref.Source == "" || ref.Notes != "neon" {
		t.Fatalf("%d %+v", code, ref)
	}
	s.assistant.WaitReferences()
	code, got := getReference(t, s, ref.ID)
	if code != 200 || got.State != motionref.StateReady || got.Analysis == nil || got.Analysis.Duration != 10 {
		t.Fatalf("%d %+v", code, got)
	}
	// The video is not kept: the folder holds only the frames (the record of the reference is in the database).
	var names []string
	filepath.Walk(s.assistant.RefStore().Path(ref.ID), func(p string, i os.FileInfo, _ error) error {
		if !i.IsDir() {
			names = append(names, strings.TrimPrefix(p, s.assistant.RefStore().Path(ref.ID)+"/"))
		}
		return nil
	})
	if len(names) != 1 || strings.Contains(strings.Join(names, " "), "video") {
		t.Fatalf("the video must not stay: %v", names)
	}
	rec := call(s.Handler(), http.MethodGet, "/api/music-motion/references/"+ref.ID+"/frames/00.jpg", "")
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/jpeg" || rec.Body.String() != "jpg" {
		t.Fatalf("%d %s", rec.Code, rec.Header())
	}
	if rec := call(s.Handler(), http.MethodGet, "/api/music-motion/references/"+ref.ID+"/frames/..%2Freference.json", ""); rec.Code != 404 {
		t.Fatalf("a frame is a jpg of the folder: %d", rec.Code)
	}
}

func TestReferences_WhatCannotBeUsedIsRefusedAndNothingIsCreated(t *testing.T) {
	s, _ := refServer(t, "1500", nil)
	for _, body := range []string{`{"url":"https://example.com/a.mp4"}`, `{"url":"` + goodURL + `"}`, `{"url":"` + goodURL + `","title":"` + strings.Repeat("x", 121) + `"}`} {
		if code, _ := addReference(t, s, body); code != http.StatusUnprocessableEntity {
			t.Fatalf("%s: %d", body, code)
		}
	}
	// A file that is not a video
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "notes.txt")
	fw.Write([]byte("hello"))
	mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/music-motion/references", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	s.assistant.WaitReferences()
	if list := s.assistant.RefStore().List(); len(list) != 0 {
		t.Fatalf("nothing is created: %+v", list)
	}
	if entries, _ := os.ReadDir(s.assistant.RefStore().Dir); len(entries) != 0 {
		t.Fatalf("no leftovers: %v", entries)
	}
}

func TestReferences_AnUploadedVideoIsAnalysedAndNotKept(t *testing.T) {
	s, _ := refServer(t, "30", nil)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "my clip.mp4")
	fw.Write([]byte("VIDEO"))
	mw.WriteField("tags", `["a","b"]`)
	mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/music-motion/references", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	var ref motionref.Reference
	_ = json.Unmarshal(rec.Body.Bytes(), &ref)
	if rec.Code != http.StatusAccepted || ref.Title != "my clip" || ref.Source != "" || len(ref.Tags) != 2 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	s.assistant.WaitReferences()
	if _, got := getReference(t, s, ref.ID); got.State != motionref.StateReady {
		t.Fatalf("%+v", got)
	}
	entries, _ := os.ReadDir(s.assistant.RefStore().Dir)
	if len(entries) != 1 {
		t.Fatalf("the uploaded video is deleted afterwards: %v", entries)
	}
}

func TestReferences_AFailedAnalysisSaysWhy(t *testing.T) {
	s, _ := refServer(t, "30", nil)
	s.assistant.RefAnalyse = func(context.Context, string, string) (*motionref.Analysis, error) {
		return nil, errors.New("ffmpeg broke")
	}
	_, ref := addReference(t, s, `{"url":"`+goodURL+`"}`)
	s.assistant.WaitReferences()
	if _, got := getReference(t, s, ref.ID); got.State != motionref.StateFailed || !strings.Contains(got.Error, "ffmpeg broke") {
		t.Fatalf("%+v", got)
	}
}

func TestReferences_ListEditAndDelete(t *testing.T) {
	s, _ := refServer(t, "30", nil)
	_, a := addReference(t, s, `{"url":"`+goodURL+`","title":"first"}`)
	time.Sleep(1100 * time.Millisecond)
	_, b := addReference(t, s, `{"url":"`+goodURL+`","title":"second"}`)
	s.assistant.WaitReferences()
	rec := call(s.Handler(), http.MethodGet, "/api/music-motion/references", "")
	var list struct{ References []motionref.Reference }
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list.References) != 2 || list.References[0].ID != b.ID {
		t.Fatalf("newest first: %s", rec.Body)
	}
	rec = call(s.Handler(), http.MethodPatch, "/api/music-motion/references/"+a.ID, `{"notes":"warm","tags":["x"]}`)
	var got motionref.Reference
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if rec.Code != 200 || got.Notes != "warm" || got.Title != "first" || len(got.Tags) != 1 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := call(s.Handler(), http.MethodPatch, "/api/music-motion/references/"+a.ID, `{"title":"`+strings.Repeat("x", 200)+`"}`); rec.Code != 422 {
		t.Fatalf("%d", rec.Code)
	}
	if rec := call(s.Handler(), http.MethodDelete, "/api/music-motion/references/"+a.ID, ""); rec.Code != 204 {
		t.Fatalf("%d", rec.Code)
	}
	for _, m := range []string{http.MethodGet, http.MethodPatch, http.MethodDelete} {
		if rec := call(s.Handler(), m, "/api/music-motion/references/"+a.ID, `{}`); rec.Code != 404 {
			t.Fatalf("%s after the delete: %d", m, rec.Code)
		}
	}
	if _, err := os.Stat(s.assistant.RefStore().Path(a.ID)); err == nil {
		t.Fatal("the files go with it")
	}
}

func TestReferences_DeletingOneThatIsBeingAnalysedStopsIt(t *testing.T) {
	block := make(chan struct{})
	s, _ := refServer(t, "30", block)
	_, ref := addReference(t, s, `{"url":"`+goodURL+`"}`)
	if rec := call(s.Handler(), http.MethodDelete, "/api/music-motion/references/"+ref.ID, ""); rec.Code != 204 {
		t.Fatalf("%d", rec.Code)
	}
	done := make(chan struct{})
	go func() { s.assistant.WaitReferences(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the analysis was not stopped by the delete")
	}
	if _, err := os.Stat(s.assistant.RefStore().Path(ref.ID)); err == nil {
		t.Fatal("a stopped analysis must not bring the reference back")
	}
}

func TestReferences_ARestartMarksTheRunningOnesAsFailed(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	st := motionref.NewStore(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "veritmo", "references"))
	ref, err := st.Create("left", "", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	s := newTestServer(t)
	_, got := getReference(t, s, ref.ID)
	if got.State != motionref.StateFailed || !strings.Contains(got.Error, "interrupted") {
		t.Fatalf("%+v", got)
	}
}
