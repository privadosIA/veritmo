package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestModels3DRefusesWhatItCanBeToldAtOnce(t *testing.T) {
	for name, args := range map[string][]string{
		"no subcommand": {},
		"unknown":       {"nope"},
		"nothing":       {"generate"},
		"both":          {"generate", "--prompt", "x", "--image", "models3d_test.go"},
		"engine":        {"generate", "--prompt", "x", "--engine", "dalle"},
		"detail":        {"generate", "--prompt", "x", "--detail", "ultra"},
		"extra":         {"generate", "--prompt", "x", "y"},
	} {
		var out, errb bytes.Buffer
		if code := runModels3D(args, &out, &errb); code != 2 {
			t.Errorf("%s: exit %d (%s)", name, code, errb.String())
		}
		if out.Len() != 0 {
			t.Errorf("%s: wrote on stdout: %q", name, out.String())
		}
	}
}

// A file that is not a picture is refused by its content, whatever its name.
func TestModels3DRefusesAFileThatIsNotAPicture(t *testing.T) {
	var out, errb bytes.Buffer
	if code := runModels3D([]string{"generate", "--image", "models3d_test.go"}, &out, &errb); code != 2 || !strings.Contains(errb.String(), "png, jpg or webp") {
		t.Errorf("%d %s", code, errb.String())
	}
}

func TestModels3DUsageNamesTheEnginesAndTheDetails(t *testing.T) {
	var out, errb bytes.Buffer
	runModels3D([]string{"generate", "-h"}, &out, &errb)
	for _, want := range []string{"--prompt", "--image", "triposr", "sf3d", "low, normal or high", "pack \"generated\""} {
		if !strings.Contains(errb.String(), want) {
			t.Errorf("the help lacks %q:\n%s", want, errb.String())
		}
	}
}

func TestParseGPUs(t *testing.T) {
	got := parseGPUs("0, NVIDIA GeForce RTX 4070, 11000, 12282\n1, NVIDIA GeForce RTX 5060 Ti, 15900, 16311\nbad\n")
	if len(got) != 2 || got[1].Index != 1 || got[1].FreeVRAMMB != 15900 || got[1].TotalVRAMMB != 16311 || got[0].Name != "NVIDIA GeForce RTX 4070" {
		t.Errorf("%+v", got)
	}
}
