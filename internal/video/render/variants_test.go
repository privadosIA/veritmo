package render

import (
	"context"
	"testing"

	"veritmo/internal/platform/runner"
	"veritmo/internal/project/config"
)

func argValue(t *testing.T, args []string, flag string) string {
	t.Helper()
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	t.Fatalf("args missing flag %q: %v", flag, args)
	return ""
}

func TestBuildArgs_ComposedPrompts(t *testing.T) {
	cases := []struct {
		name         string
		subject      string
		scenePrompt  string
		sceneNeg     string
		globalNeg    string
		wantPrompt   string
		wantNegative string // "" means the flag must be absent
	}{
		{
			name:        "subject_prompt prepended",
			subject:     "A cinematic wide shot of",
			scenePrompt: "a lighthouse at dusk",
			wantPrompt:  "A cinematic wide shot of a lighthouse at dusk",
		},
		{
			name:         "global negative concatenated after scene negative",
			scenePrompt:  "a cat",
			sceneNeg:     "blurry, low quality",
			globalNeg:    "watermark, text",
			wantPrompt:   "a cat",
			wantNegative: "blurry, low quality watermark, text",
		},
		{
			name:         "global negative alone",
			scenePrompt:  "a cat",
			globalNeg:    "watermark",
			wantPrompt:   "a cat",
			wantNegative: "watermark",
		},
		{
			name:        "both globals absent is identical to today",
			scenePrompt: "a cat",
			wantPrompt:  "a cat",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fr := runner.NewFakeRunner()
			scene := config.Scene{ID: "s1", Prompt: tc.scenePrompt, NegativePrompt: tc.sceneNeg}
			defaults := config.Defaults{
				Model: "m", Resolution: "512x512", Sampler: "euler", Steps: 20,
				SubjectPrompt: tc.subject, NegativePrompt: tc.globalNeg,
			}

			res, err := RunVariant(context.Background(), fr, scene, defaults, 0, 42, "out.png", nil)
			if err != nil {
				t.Fatalf("RunVariant() error = %v", err)
			}

			calls := fr.Calls()
			if len(calls) != 1 {
				t.Fatalf("got %d invocations, want 1", len(calls))
			}
			if got := argValue(t, calls[0].Args, Flags.Prompt); got != tc.wantPrompt {
				t.Errorf("prompt = %q, want %q", got, tc.wantPrompt)
			}
			if tc.wantNegative == "" {
				for _, a := range calls[0].Args {
					if a == Flags.NegativePrompt {
						t.Errorf("negative-prompt flag present, want absent: %v", calls[0].Args)
					}
				}
			} else if got := argValue(t, calls[0].Args, Flags.NegativePrompt); got != tc.wantNegative {
				t.Errorf("negative prompt = %q, want %q", got, tc.wantNegative)
			}
			if res.Prompt != tc.wantPrompt {
				t.Errorf("result.Prompt = %q, want %q", res.Prompt, tc.wantPrompt)
			}
			if res.NegativePrompt != tc.wantNegative {
				t.Errorf("result.NegativePrompt = %q, want %q", res.NegativePrompt, tc.wantNegative)
			}
		})
	}
}

func TestVariants_ExplicitSeedProducesBasePlusI(t *testing.T) {
	scene := config.Scene{ID: "s1", Prompt: "p", Seed: intPtr(100), Variants: intPtr(3)}
	seeds := VariantSeeds(scene, 3, func() int64 { t.Fatal("rng must not be called with an explicit seed"); return 0 })
	want := []int64{100, 101, 102}
	for i := range want {
		if seeds[i] != want[i] {
			t.Errorf("seeds[%d] = %d, want %d", i, seeds[i], want[i])
		}
	}
}

func TestVariants_UnsetSeedDrawsFromRNG(t *testing.T) {
	scene := config.Scene{ID: "s1", Prompt: "p"}
	next := int64(1000)
	seeds := VariantSeeds(scene, 2, func() int64 { next++; return next })
	if seeds[0] != 1001 || seeds[1] != 1002 {
		t.Errorf("seeds = %v, want [1001 1002]", seeds)
	}
}

func TestVariants_SequentialInvocationsWithSeedsAndPaths(t *testing.T) {
	fr := runner.NewFakeRunner()
	scene := config.Scene{ID: "s1", Prompt: "p", Seed: intPtr(100), Variants: intPtr(3)}
	defaults := config.Defaults{Model: "m", Resolution: "512x512", Sampler: "euler", Steps: 20}

	seeds := VariantSeeds(scene, 3, nil)
	outPaths := []string{"tmp/scene_s1_v1.png", "tmp/scene_s1_v2.png", "tmp/scene_s1_v3.png"}

	results, err := RenderVariants(context.Background(), fr, scene, defaults, seeds, outPaths, nil)
	if err != nil {
		t.Fatalf("RenderVariants() error = %v", err)
	}

	calls := fr.Calls()
	if len(calls) != 3 {
		t.Fatalf("got %d invocations, want exactly 3", len(calls))
	}
	wantSeeds := []string{"100", "101", "102"}
	for i, c := range calls {
		if c.Name != Binary {
			t.Errorf("call %d binary = %q, want %q", i, c.Name, Binary)
		}
		if got := argValue(t, c.Args, Flags.Seed); got != wantSeeds[i] {
			t.Errorf("call %d seed = %q, want %q (ordering must follow variant index)", i, got, wantSeeds[i])
		}
		if got := argValue(t, c.Args, Flags.Output); got != outPaths[i] {
			t.Errorf("call %d output = %q, want %q", i, got, outPaths[i])
		}
	}
	for i, r := range results {
		if r.VariantIndex != i {
			t.Errorf("results[%d].VariantIndex = %d", i, r.VariantIndex)
		}
		if r.EffectiveSeed != int64(100+i) {
			t.Errorf("results[%d].EffectiveSeed = %d, want %d", i, r.EffectiveSeed, 100+i)
		}
		if r.OutPath != outPaths[i] {
			t.Errorf("results[%d].OutPath = %q, want %q", i, r.OutPath, outPaths[i])
		}
	}
}

func TestVariants_StopsOnFirstError(t *testing.T) {
	fr := runner.NewFakeRunner()
	fr.Script(runner.ScriptedResult{}, runner.ScriptedResult{Err: errBoom})
	scene := config.Scene{ID: "s1", Prompt: "p", Seed: intPtr(1)}
	defaults := config.Defaults{Model: "m", Resolution: "512x512", Sampler: "euler", Steps: 1}

	results, err := RenderVariants(context.Background(), fr, scene, defaults, []int64{1, 2, 3}, []string{"a.png", "b.png", "c.png"}, nil)
	if err == nil {
		t.Fatal("expected error from second invocation")
	}
	if len(fr.Calls()) != 2 {
		t.Errorf("got %d invocations, want 2 (no further renders after a failure)", len(fr.Calls()))
	}
	if len(results) != 1 {
		t.Errorf("got %d completed results, want 1", len(results))
	}
}

func intPtr(v int) *int { return &v }
