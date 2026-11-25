package generation

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"veritmo/internal/video/blender"
)

func TestEnvironment_BlenderAvailableWithItsDevices(t *testing.T) {
	s, _ := newProjectsServer(t)
	s.blenderProbeFn = func(context.Context) blenderProbe {
		return blenderProbe{
			Info:    blender.Info{Path: "/usr/bin/blender", Version: "5.2.3"},
			Devices: []blender.Device{{Type: "OPTIX", Name: "NVIDIA GeForce RTX 4070"}, {Type: "CPU", Name: "Core"}},
		}
	}
	b := binByName(t, getEnvironment(t, s, ""), "blender")
	if !b.Present || b.Required || b.Version != "5.2.3" || len(b.Devices) != 2 || b.Devices[0] != "OPTIX NVIDIA GeForce RTX 4070" {
		t.Errorf("blender = %+v", b)
	}
}

func TestEnvironment_BlenderMissingSaysWhy(t *testing.T) {
	s, _ := newProjectsServer(t)
	s.blenderProbeFn = func(context.Context) blenderProbe {
		return blenderProbe{Err: fmt.Errorf("%w: %s", blender.ErrNotInstalled, blender.InstallHint)}
	}
	b := binByName(t, getEnvironment(t, s, ""), "blender")
	if b.Present || b.Required || !strings.Contains(b.Fix, "not installed") || !strings.Contains(b.Fix, "install Blender") {
		t.Errorf("blender = %+v", b)
	}
}

func TestEnvironment_BlenderTooOldIsNotAvailable(t *testing.T) {
	s, _ := newProjectsServer(t)
	s.blenderProbeFn = func(context.Context) blenderProbe {
		return blenderProbe{Info: blender.Info{Path: "/usr/bin/blender", Version: "4.1.1"}, Err: fmt.Errorf("%w: found 4.1.1, need 4.2 or newer", blender.ErrTooOld)}
	}
	b := binByName(t, getEnvironment(t, s, ""), "blender")
	if b.Present || b.Version != "4.1.1" || !strings.Contains(b.Fix, "too old") {
		t.Errorf("blender = %+v", b)
	}
}

func TestSources_ABlendFileIsAcceptedAndListedAsBlend(t *testing.T) {
	if sourceKind("escena.blend") != "blend" || sourceKind("a.png") != "image" || sourceKind("a.mp4") != "video" || sourceKind("a.mp3") != "audio" {
		t.Error("sourceKind")
	}
}
