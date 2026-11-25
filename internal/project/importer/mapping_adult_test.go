package importer

import (
	"strings"
	"testing"

	"veritmo/internal/project/config"
)

// The label an entry gives (Adult: yes|no) reaches the scenes.yaml the import writes, in video and in music.
func TestEmitYAML_CarriesTheAdultLabel(t *testing.T) {
	for name, want := range map[string]*bool{"yes": ptr(true), "no": ptr(false), "none": nil} {
		for _, music := range []bool{false, true} {
			r := &Result{Config: &config.Config{Project: "p", Adult: want}}
			if music {
				r.Config.Output.Mode = config.ModeMusic
			}
			out := string(r.EmitYAML())
			has := strings.Contains(out, "\nadult: ")
			if (want != nil) != has {
				t.Errorf("%s (music %v): adult line present = %v\n%s", name, music, has, out)
			}
			if want != nil && !strings.Contains(out, "\nadult: "+map[bool]string{true: "true", false: "false"}[*want]+"\n") {
				t.Errorf("%s (music %v): wrong value\n%s", name, music, out)
			}
		}
	}
}

func ptr(b bool) *bool { return &b }
