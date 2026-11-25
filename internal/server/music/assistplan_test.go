package music

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"veritmo/internal/assistant/assist"
	"veritmo/internal/music/musicmotion"
)

func planSeams(s *testServer, g assist.PlanGuess, planErr error, musicErr error) {
	s.Assistant.AssistPlanRun = func(ctx context.Context, in assist.PlanInput, model string) (assist.PlanGuess, error) {
		return g, planErr
	}
	s.Assistant.AssistMusicRun = func(ctx context.Context, text string, o assist.MusicOptions, model string) (assist.MusicSuggestion, error) {
		if musicErr != nil {
			return assist.MusicSuggestion{}, musicErr
		}
		return assist.MusicSuggestion{Caption: "synthwave", Lyrics: "[verse]\nla", Tags: "Synthwave, Neon."}, nil
	}
}

func TestAssistPlan_OneSentenceToTheWholePlan(t *testing.T) {
	s := mmServer(t)
	existing := musicmotion.NewCatalog(nil).List()[0].Name
	planSeams(s, assist.PlanGuess{Title: "Night Drive", Seconds: 90, Style: "illustrated", Level: "medium", FPS: 30, Mode: "dark", Concept: "a car at night",
		World: "a night road, headlights and dust", Device: "one object across the song", Hook: "a pair of headlights cut the dark",
		Parts: []assist.PlanPartGuess{{Name: existing}, {Name: "year-counter", Request: "a counter that rolls through years"}, {Name: existing}, {Name: "no-request"}}}, nil, nil)
	var a PlanAnswer
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/assist/plan", map[string]string{"text": "  a synthwave track about driving at night  ", "generator": "ace-step"})
	_ = json.Unmarshal(rec.Body.Bytes(), &a)
	if rec.Code != http.StatusOK || a.Song.Title != "Night Drive" || a.Song.Seconds != 90 || a.Song.Caption != "synthwave" || a.Song.Tags != "synthwave,neon" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if a.Animation.Style != "illustrated" || a.Animation.Level != "medium" || a.Animation.FPS != 30 || a.Animation.Mode != "dark" || a.Animation.Prompt != "a synthwave track about driving at night" {
		t.Fatalf("%+v", a.Animation)
	}
	if a.Animation.World != "a night road, headlights and dust" || a.Animation.Device != "one object across the song" || a.Animation.Hook != "a pair of headlights cut the dark" || a.Animation.Note != "" {
		t.Fatalf("the storyline: %+v", a.Animation)
	}
	if len(a.Parts) != 2 || !a.Parts[0].Exists || a.Parts[0].Name != existing || a.Parts[1].Exists || a.Parts[1].Request == "" {
		t.Fatalf("%+v", a.Parts)
	}
	if len(a.Warnings) != 1 || !strings.Contains(a.Warnings[0], "no-request") {
		t.Fatalf("%v", a.Warnings)
	}
}

func TestAssistPlan_WhatTheCatalogDoesNotOfferIsEmptyWithAWarning(t *testing.T) {
	s := mmServer(t)
	planSeams(s, assist.PlanGuess{Title: "x", Style: "cubist", Level: "simple", FPS: 25, Mode: "dark", Concept: "c"}, nil, errors.New("no music"))
	var a PlanAnswer
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/assist/plan", map[string]string{"text": "algo"})
	_ = json.Unmarshal(rec.Body.Bytes(), &a)
	if rec.Code != http.StatusOK || a.Animation.Style != "" || a.Animation.FPS != 0 || a.Animation.Level != "simple" || a.Song.Caption != "" || len(a.Warnings) < 4 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestAssistPlan_RefusalsAndBadRequests(t *testing.T) {
	s := mmServer(t)
	planSeams(s, assist.PlanGuess{}, assist.ErrRefused, nil)
	if rec := doJSON(t, s.Handler(), http.MethodPost, "/api/assist/plan", map[string]string{"text": "algo"}); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	for _, body := range []map[string]string{{"text": "  "}, {"text": "x", "language": "español"}, {"text": "x", "provider": "nope"}} {
		if rec := doJSON(t, s.Handler(), http.MethodPost, "/api/assist/plan", body); rec.Code != http.StatusBadRequest {
			t.Fatalf("%v: %d %s", body, rec.Code, rec.Body)
		}
	}
}

func TestAssistPlan_TheCatalogSaysTheServerCanPlan(t *testing.T) {
	found := false
	for _, f := range musicmotion.Features {
		found = found || f == "plan"
	}
	if !found {
		t.Fatal(musicmotion.Features)
	}
}

func TestAssistPlan_AnUnknownOrMissingDeviceFallsToTypeWithANote(t *testing.T) {
	s := mmServer(t)
	for device, want := range map[string]string{"a heist movie": "is not one of the devices offered", "": "gave no device"} {
		planSeams(s, assist.PlanGuess{Title: "x", Style: "illustrated", Level: "simple", FPS: 30, Mode: "dark", Concept: "c", World: "a city", Hook: "a door opens", Device: device}, nil, nil)
		var a PlanAnswer
		rec := doJSON(t, s.Handler(), http.MethodPost, "/api/assist/plan", map[string]string{"text": "algo"})
		_ = json.Unmarshal(rec.Body.Bytes(), &a)
		if rec.Code != http.StatusOK || a.Animation.Device != "type carries the argument" || !strings.Contains(a.Animation.Note, want) {
			t.Fatalf("%q: %d %+v", device, rec.Code, a.Animation)
		}
		if !strings.Contains(strings.Join(a.Warnings, " "), want) {
			t.Errorf("%q: the note is also a warning: %v", device, a.Warnings)
		}
	}
	// a long world is cut to what a piece keeps
	planSeams(s, assist.PlanGuess{Title: "x", Style: "illustrated", Level: "simple", FPS: 30, Mode: "dark", Concept: "c", World: strings.Repeat("w", 900), Device: "a number lands", Hook: "h"}, nil, nil)
	var a PlanAnswer
	_ = json.Unmarshal(doJSON(t, s.Handler(), http.MethodPost, "/api/assist/plan", map[string]string{"text": "algo"}).Body.Bytes(), &a)
	if len([]rune(a.Animation.World)) != assist.MaxPlanLine {
		t.Errorf("the world has %d characters", len([]rune(a.Animation.World)))
	}
}
