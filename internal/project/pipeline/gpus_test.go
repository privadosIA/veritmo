package pipeline

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"

	"veritmo/internal/platform/events"
	"veritmo/internal/platform/runner"
	"veritmo/internal/project/config"
)

func TestParseGPUs(t *testing.T) {
	ok := map[string][]string{
		"":      nil,
		"1":     {"1"},
		"0,1":   {"0", "1"},
		" 1, 0": {"1", "0"},
		"01":    {"1"},
	}
	for in, want := range ok {
		got, err := ParseGPUs(in)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("ParseGPUs(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	bad := map[string]string{"0,x": `"x"`, "1,1": "duplicate", "-1": `"-1"`, "0,,1": `""`, "a": `"a"`}
	for in, want := range bad {
		if _, err := ParseGPUs(in); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("ParseGPUs(%q) error = %v, want containing %s", in, err, want)
		}
	}
}

func TestRun_InvalidGPUListStartsNothing(t *testing.T) {
	fr := runner.NewFakeRunner()
	var buf bytes.Buffer
	_, err := Run(context.Background(), &config.Config{}, Options{GPU: "0,0", Runner: fr}, events.New(&buf, true))
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("err = %v", err)
	}
	if n := len(fr.Calls()); n != 0 {
		t.Errorf("%d subprocesses started, want none", n)
	}
	if strings.Contains(buf.String(), "pipeline_start") {
		t.Errorf("pipeline_start must not be emitted for an invalid GPU list: %s", buf.String())
	}
}

func TestCheckVRAM_WarnsPerGPUNamingIt(t *testing.T) {
	freeVRAMMB := func(gpu string) (int, error) {
		if gpu == "0" {
			return 1000, nil
		}
		return 9000, nil
	}
	var buf bytes.Buffer
	em := events.New(&buf, true)
	for _, g := range []string{"0", "1"} {
		if err := checkVRAM(freeVRAMMB, g, 2048, em); err != nil {
			t.Fatal(err)
		}
	}
	out := buf.String()
	if strings.Count(out, `"type":"warn"`) != 1 || !strings.Contains(out, "GPU 0") || strings.Contains(out, "GPU 1") {
		t.Errorf("expected exactly one warning naming GPU 0, got:\n%s", out)
	}
}
