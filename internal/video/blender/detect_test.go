package blender

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"veritmo/internal/platform/runner"
)

func detectFor(t *testing.T, found bool, version string) (Info, error) {
	t.Helper()
	lookPath := func(string) (string, error) {
		if !found {
			return "", fmt.Errorf("not found")
		}
		return "/usr/bin/blender", nil
	}
	fr := runner.NewFakeRunner()
	fr.Script(runner.ScriptedResult{Stdout: []byte(version)})
	return DetectWith(context.Background(), fr, lookPath)
}

func TestDetect(t *testing.T) {
	cases := []struct {
		name    string
		found   bool
		out     string
		wantErr error
		version string
	}{
		{"new", true, "Blender 5.2.3 LTS\n\tbuild date: x\n", nil, "5.2.3"},
		{"minimum", true, "Blender 4.2.0\n", nil, "4.2.0"},
		{"old", true, "Blender 4.1.1\n", ErrTooOld, "4.1.1"},
		{"missing", false, "", ErrNotInstalled, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			info, err := detectFor(t, c.found, c.out)
			if !errors.Is(err, c.wantErr) {
				t.Fatalf("err = %v, want %v", err, c.wantErr)
			}
			if c.version != "" && info.Version != c.version {
				t.Errorf("version = %q, want %q", info.Version, c.version)
			}
		})
	}
}

func TestDetectMissingSaysHowToInstall(t *testing.T) {
	_, err := detectFor(t, false, "")
	if err == nil || !errors.Is(err, ErrNotInstalled) || !strings.Contains(err.Error(), "install Blender") {
		t.Fatalf("err = %v", err)
	}
}

func TestDetectUnreadableVersion(t *testing.T) {
	if _, err := detectFor(t, true, "garbage"); err == nil {
		t.Fatal("an unreadable version must be an error")
	}
}

func TestParseDevices(t *testing.T) {
	out := "Blender 5.2.3\nVERITMO_DEVICE OPTIX NVIDIA GeForce RTX 5060 Ti\nVERITMO_DEVICE CUDA NVIDIA GeForce RTX 5060 Ti\nVERITMO_DEVICE CPU Intel Core Ultra 7 265\nnoise\n"
	devs := parseDevices(out)
	if len(devs) != 3 || devs[0].Type != "OPTIX" || devs[0].Name != "NVIDIA GeForce RTX 5060 Ti" || devs[2].Type != "CPU" {
		t.Fatalf("devices = %+v", devs)
	}
}

func TestDevicesPassesTheEnvironment(t *testing.T) {
	fr := runner.NewFakeRunner()
	fr.Script(runner.ScriptedResult{Stdout: []byte("VERITMO_DEVICE CPU x\n")})
	devs, err := Devices(context.Background(), fr, "/usr/bin/blender", "CUDA_VISIBLE_DEVICES=1")
	if err != nil || len(devs) != 1 {
		t.Fatalf("devs=%v err=%v", devs, err)
	}
	if c := fr.Calls()[0]; len(c.Env) != 1 || c.Env[0] != "CUDA_VISIBLE_DEVICES=1" {
		t.Errorf("env = %v", c.Env)
	}
}
