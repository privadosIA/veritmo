package workspace

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"veritmo/internal/platform/settings"
)

// seedPieces makes the table of the animations (the music package owns it) and puts rows in it.
func seedPieces(t *testing.T, s *testServer, rows ...[3]string) {
	t.Helper()
	db := s.projects.db.DB
	if err := db.Exec("CREATE TABLE IF NOT EXISTS mm_pieces (id text primary key, client text, created_at datetime, updated_at datetime)").Error; err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		at, _ := time.Parse(time.RFC3339, r[2])
		if err := db.Exec("INSERT INTO mm_pieces (id, client, created_at, updated_at) VALUES (?, ?, ?, ?)", r[0], r[1], at, at).Error; err != nil {
			t.Fatal(err)
		}
	}
}

func clientsOf(t *testing.T, s *testServer) map[string]ClientLabel {
	t.Helper()
	var list []ClientLabel
	if code := getJSON(t, s, "/api/clients", &list); code != http.StatusOK {
		t.Fatalf("GET /api/clients: %d", code)
	}
	out := map[string]ClientLabel{}
	for _, l := range list {
		out[l.Name] = l
	}
	if len(out) != len(list) {
		t.Fatalf("a label is listed twice: %+v", list)
	}
	return out
}

func TestClients_TheLabelsInUseWithTheirCountsAndColours(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	s, _ := newProjectsServer(t)
	if got := clientsOf(t, s); len(got) != 0 {
		t.Fatalf("a server with nothing: %+v", got)
	}
	seedMixed(t, s)
	seedPieces(t, s,
		[3]string{"api-mm-20261107-000000-aaaa01", "video-studio", "2026-11-20T10:00:00Z"},
		[3]string{"api-mm-20261107-000000-aaaa02", "", "2026-11-21T10:00:00Z"},
		[3]string{"api-mm-20261107-000000-aaaa03", "secret-label", "2026-11-22T10:00:00Z"},
		[3]string{"api-mm-20261107-000000-aaaa04", "piece-only", "2026-11-23T10:00:00Z"})
	s.hidden = map[string]bool{"api-mm-20261107-000000-aaaa03": true}

	got := clientsOf(t, s)
	want := map[string]ClientCounts{
		"embedded":     {Projects: 1},
		"video-studio": {Projects: 1, Generations: 1, Animations: 1},
		"dash-b":       {ImageEdits: 1},
		"unknown":      {VideoEdits: 1, Animations: 1},
		"piece-only":   {Animations: 1},
	}
	if len(got) != len(want) {
		t.Fatalf("labels: %+v", got)
	}
	for name, c := range want {
		l, ok := got[name]
		if !ok || l.Counts != c || l.Total != c.Projects+c.Animations+c.Generations+c.ImageEdits+c.VideoEdits+c.ModelGenerations {
			t.Errorf("%s: %+v, want %+v", name, l, c)
		}
		if l.Color != settings.DefaultClientColour(name) || l.LastUsed == "" {
			t.Errorf("%s: colour %q or last_used %q", name, l.Color, l.LastUsed)
		}
	}
	if _, ok := got["secret-label"]; ok {
		t.Error("a label that only a hidden item has is listed")
	}
	// a label that only an animation has is last used when the animation was; the list is newest use first
	if got["piece-only"].LastUsed != "2026-11-23T10:00:00Z" {
		t.Errorf("last_used: %q", got["piece-only"].LastUsed)
	}
	var list []ClientLabel
	getJSON(t, s, "/api/clients", &list)
	for i := 1; i < len(list); i++ {
		if list[i-1].LastUsed < list[i].LastUsed {
			t.Errorf("not newest first: %+v", list)
		}
	}
}

func TestClients_TheColourOfALabelIsSetRemovedAndValidated(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	s, _ := newProjectsServer(t)
	seedMixed(t, s)
	put := func(name string, body any) (int, map[string]string) {
		rec := doJSON(t, s.Handler(), "PUT", "/api/clients/"+name, body)
		var out map[string]string
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	if code, out := put("video-studio", map[string]any{"color": "#3366CC"}); code != 200 || out["color"] != "#3366cc" || out["name"] != "video-studio" {
		t.Fatalf("set: %d %v", code, out)
	}
	if got := clientsOf(t, s)["video-studio"].Color; got != "#3366cc" {
		t.Fatalf("listed: %s", got)
	}
	// a label with no items may have a colour, and it is kept for when it has some
	if code, _ := put("future-label", map[string]any{"color": "#112233"}); code != 200 {
		t.Fatalf("a label with no items: %d", code)
	}
	if _, ok := clientsOf(t, s)["future-label"]; ok {
		t.Error("a label with no items is listed")
	}
	if code, out := put("video-studio", map[string]any{"color": nil}); code != 200 || out["color"] != settings.DefaultClientColour("video-studio") {
		t.Fatalf("back to the default: %d %v", code, out)
	}
	for name, c := range map[string]struct {
		label string
		body  any
	}{
		"a bad name":      {"Dashboard%20Full", map[string]any{"color": "#112233"}},
		"a bad colour":    {"video-studio", map[string]any{"color": "red"}},
		"a number":        {"video-studio", map[string]any{"color": 12}},
		"no colour field": {"video-studio", map[string]any{}},
	} {
		if code, _ := put(c.label, c.body); code != 400 {
			t.Errorf("%s: %d", name, code)
		}
	}
	if got := clientsOf(t, s)["video-studio"].Color; got != settings.DefaultClientColour("video-studio") {
		t.Errorf("a refused request changed the colour: %s", got)
	}
	if code, _ := put("embedded", map[string]any{"color": "#aabbcc"}); code != 200 {
		t.Errorf("the reserved labels may have a colour: %d", code)
	}
}
