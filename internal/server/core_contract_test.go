package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"veritmo/internal/project/config"
)

func get(t *testing.T, s *Server, path string) string {
	t.Helper()
	rec := call(s.Handler(), http.MethodGet, path, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: %d %s", path, rec.Code, rec.Body)
	}
	return rec.Body.String()
}

// --- the answers of the real handlers -------------------------------------------------------------------

func TestContract_TheEnvironmentReportFitsItsDescription(t *testing.T) {
	c := newContractChecker(t)
	s := newTestServer(t)
	expectFits(t, c, "/api/environment", get(t, s, "/api/environment"))
}

func TestContract_TheStatisticsFitTheirDescription(t *testing.T) {
	c := newContractChecker(t)
	s, _ := launchServer(t)

	t.Run("with no runs", func(t *testing.T) {
		expectFits(t, c, "/api/stats", get(t, s, "/api/stats"+wide))
	})

	t.Run("with runs, phases and GPU samples", func(t *testing.T) {
		seedStat(t, s, statRun{id: "ok", project: "demo", state: stateSucceeded, gpu: "1", created: d0, waitS: 2, durS: 30,
			events: []string{ev("phase_start", "render", 0), ev("phase_end", "render", 12000), ev("skipped", "tts", 0)}})
		seedStat(t, s, statRun{id: "bad", project: "demo", state: stateFailed, gpu: "1", created: d0.Add(time.Hour), durS: 5})
		row := RunSample{RunID: "ok", TS: d0, GPU: 1, VRAMUsedMB: 9000, VRAMTotalMB: 16311, UtilPct: 80, RAMUsedMB: 5000, RAMTotalMB: 30000}
		if err := s.workspace.SeedRow(&row); err != nil {
			t.Fatal(err)
		}
		body := get(t, s, "/api/stats"+wide)
		expectFits(t, c, "/api/stats", body)
		if !strings.Contains(body, `"by_gpu":[{`) || !strings.Contains(body, `"phases":[{`) {
			t.Errorf("the case must have groups and phases to mean something: %.400s", body)
		}
	})
}

func TestContract_TheResourcesOfARunFitTheirDescription(t *testing.T) {
	c := newContractChecker(t)
	s, _ := launchServer(t)
	seedStat(t, s, statRun{id: "old", project: "demo", state: stateSucceeded, created: d0, durS: 5})
	t.Run("a run with no samples", func(t *testing.T) {
		expectFits(t, c, "/api/runs/{id}/resources", get(t, s, "/api/runs/old/resources"))
	})

	seedStat(t, s, statRun{id: "r", project: "demo", state: stateSucceeded, created: d0, durS: 10})
	for _, row := range []RunSample{
		{RunID: "r", TS: d0, GPU: 1, VRAMUsedMB: 1000, VRAMTotalMB: 16311, UtilPct: 0, RAMUsedMB: 5000, RAMTotalMB: 30000},
		{RunID: "r", TS: d0.Add(2 * time.Second), GPU: 1, VRAMUsedMB: 14200, VRAMTotalMB: 16311, UtilPct: 95, RAMUsedMB: 21000, RAMTotalMB: 30000},
	} {
		if err := s.workspace.SeedRow(&row); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("a run with samples", func(t *testing.T) {
		body := get(t, s, "/api/runs/r/resources")
		expectFits(t, c, "/api/runs/{id}/resources", body)
		if !strings.Contains(body, `"util_pct"`) {
			t.Errorf("the case must carry GPU samples: %.300s", body)
		}
	})
}

func TestContract_TheSettingsFitTheirDescription(t *testing.T) {
	c := newContractChecker(t)

	t.Run("with nothing saved", func(t *testing.T) {
		isolateSettings(t)
		s := newTestServer(t)
		expectFits(t, c, "/api/settings", get(t, s, "/api/settings"))
	})

	t.Run("with an Ollama address and a browser path saved", func(t *testing.T) {
		isolateSettings(t)
		s := newTestServer(t)
		rec := doJSON(t, s.Handler(), http.MethodPut, "/api/settings", map[string]any{"ollama_host": "192.168.1.50", "browser": fakeChrome(t)})
		if rec.Code != http.StatusOK {
			t.Fatalf("saving the settings: %d %s", rec.Code, rec.Body)
		}
		body := get(t, s, "/api/settings")
		expectFits(t, c, "/api/settings", body)
		// The optional fields of the browser must be in the answer at least once, or the case proves nothing.
		if !strings.Contains(body, `"path":`) || !strings.Contains(body, `"version":`) {
			t.Errorf("the case must carry the optional fields of the browser: %.400s", body)
		}
	})
}

// --- the checker itself: it must fail when it should --------------------------------------------------

func TestContract_TheCheckerCatchesWhatDriftsAndWhatItDoesNotUnderstand(t *testing.T) {
	c := newContractChecker(t)
	good := `{"edition":"public","content_mode":"censored","binaries":[],"models":[],"retouch":[],"voices":[],"loras":[],"music":[],"music_adapters":[],"transcription":[],"separation":[],"dictation":{"available":false},"gpus":[],"ollama":{"host":"h","source":"default","remote":false},"tokens":{"huggingface":false,"civitai":false},"token_sources":{}}`
	if p := c.CheckAnswer(t, "/api/environment", good); len(p) > 0 {
		t.Fatalf("the reference answer must fit: %v", p)
	}

	cases := []struct{ name, body, want string }{
		{"a field added to an answer without describing it", strings.Replace(good, `"gpus":[]`, `"gpus":[],"new_field":1`, 1), "$.new_field"},
		{"a required field that is no longer sent", strings.Replace(good, `"edition":"public",`, ``, 1), "$.edition"},
		{"a value of another type", strings.Replace(good, `"remote":false}`, `"remote":"no"}`, 1), "$.ollama.remote"},
		{"a value outside the declared set", strings.Replace(good, `"censored"`, `"hidden"`, 1), "$.content_mode"},
		{"a null that is not declared nullable", strings.Replace(good, `"models":[]`, `"models":null`, 1), "$.models"},
		{"a field of an item of a list", strings.Replace(good, `"binaries":[]`, `"binaries":[{"name":"sd","required":true,"present":true,"surprise":1}]`, 1), "$.binaries[0].surprise"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := c.CheckAnswer(t, "/api/environment", tc.body)
			if len(p) == 0 || !strings.Contains(strings.Join(p, "\n"), tc.want) {
				t.Errorf("must fail naming %s, got %v", tc.want, p)
			}
		})
	}

	t.Run("an object declared with no fields", func(t *testing.T) {
		c.Problems = nil
		c.Check(map[string]any{"type": "object"}, map[string]any{"a": 1.0}, "$")
		if len(c.Problems) == 0 || !strings.Contains(c.Problems[0], "no fields") {
			t.Errorf("\"any object\" must fail: %v", c.Problems)
		}
	})

	t.Run("a keyword it does not understand", func(t *testing.T) {
		c.Problems = nil
		c.Check(map[string]any{"type": "string", "minLength": 3.0}, "b", "$")
		if len(c.Problems) == 0 || !strings.Contains(c.Problems[0], `"minLength"`) {
			t.Errorf("an unknown keyword must fail instead of being skipped: %v", c.Problems)
		}
	})

	t.Run("a null where the description allows it", func(t *testing.T) {
		c.Problems = nil
		c.Check(map[string]any{"type": "object", "nullable": true, "properties": map[string]any{"a": map[string]any{"type": "integer"}}}, nil, "$")
		if len(c.Problems) != 0 {
			t.Errorf("nullable must let null through: %v", c.Problems)
		}
	})
}

// The checker holds an answer to the pattern the description gives, and the pattern of a client is the one the
// server enforces (config.ClientPatternText).
func TestContract_ThePatternOfAClientIsTheOneTheServerEnforces(t *testing.T) {
	c := &contractChecker{Doc: parseOpenAPI(t)}
	schema := c.Resolve(map[string]any{"$ref": "#/components/schemas/GenerationRequest"}).(map[string]any)
	pat, _ := schema["properties"].(map[string]any)["client"].(map[string]any)["pattern"].(string)
	if pat != config.ClientPatternText {
		t.Fatalf("the description says %q, the server enforces %q", pat, config.ClientPatternText)
	}
	for body, wantBad := range map[string]bool{`{"client":"video-studio"}`: false, `{"client":"Video Studio"}`: true} {
		var v any
		_ = json.Unmarshal([]byte(body), &v)
		c.Problems = nil
		c.Check(schema, v, "$")
		if bad := strings.Contains(strings.Join(c.Problems, "\n"), "pattern"); bad != wantBad {
			t.Errorf("%s: problems %v", body, c.Problems)
		}
	}
}
