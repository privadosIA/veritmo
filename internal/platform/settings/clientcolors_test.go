package settings

import (
	"strings"
	"testing"
)

// The default colour is a fixed rule of the name (the same one is in docs/http-api.md): these vectors fix it, so a change of the rule is seen.
func TestDefaultClientColourIsDeterministicAndDiffersByName(t *testing.T) {
	for _, name := range []string{"dashboard-full", "claude-test", "embedded", "unknown"} {
		got := DefaultClientColour(name)
		if !ValidClientColour(got) || got != DefaultClientColour(name) {
			t.Errorf("%s: %q", name, got)
		}
	}
	if DefaultClientColour("dashboard-full") == DefaultClientColour("claude-test") {
		t.Error("two different labels have the same colour")
	}
}

func TestDefaultClientColourVectors(t *testing.T) {
	// computed apart with hashlib and colorsys from the rule of the spec: hues 260, 218 and 179
	want := map[string]string{"dashboard-full": "#6839c6", "claude-test": "#396dc6", "embedded": "#39c6c3"}
	for name, w := range want {
		if got := DefaultClientColour(name); got != w {
			t.Errorf("%s: %s, want %s", name, got, w)
		}
	}
	// hue 0, 120 and 240 at 55 % and 50 % are the known colours of the conversion
	for h, w := range map[float64]string{0: "#c63939", 120: "#39c639", 240: "#3939c6"} {
		if got := hslToHex(h, 0.55, 0.50); got != w {
			t.Errorf("hue %v: %s, want %s", h, got, w)
		}
	}
}

func TestAClientColourIsKeptRemovedAndValidated(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if got := ClientColour("dashboard-full"); got != DefaultClientColour("dashboard-full") {
		t.Fatalf("no colour set: %s", got)
	}
	if err := SetClientColour("dashboard-full", "#3366CC"); err != nil {
		t.Fatal(err)
	}
	if got := ClientColour("dashboard-full"); got != "#3366cc" {
		t.Fatalf("set: %s", got)
	}
	for _, bad := range []string{"red", "#fff", "3366cc", "#33666g"} {
		if err := SetClientColour("dashboard-full", bad); err == nil || !strings.Contains(err.Error(), "#rrggbb") {
			t.Errorf("%q was accepted: %v", bad, err)
		}
	}
	if got := ClientColour("dashboard-full"); got != "#3366cc" {
		t.Fatalf("a bad colour changed it: %s", got)
	}
	if err := SetClientColour("dashboard-full", ""); err != nil {
		t.Fatal(err)
	}
	if got := ClientColour("dashboard-full"); got != DefaultClientColour("dashboard-full") {
		t.Fatalf("removed: %s", got)
	}
}
