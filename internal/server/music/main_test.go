package music

import (
	"os"
	"testing"
)

// TestMain keeps the suite independent of the developer's own keys (see the one of package server) and lets the
// test binary act as the fake service that installServices starts.
func TestMain(m *testing.M) {
	if mode := os.Getenv("VERITMO_FAKE_SERVICE"); mode != "" { // started as a service by installServices
		runFakeService(mode)
	}
	home, err := os.MkdirTemp("", "music-test-home-")
	if err != nil {
		panic(err)
	}
	_ = os.Setenv("HOME", home)
	for _, v := range []string{"HF_TOKEN", "CIVITAI_TOKEN", "HF_HOME", "XDG_CACHE_HOME", "XDG_CONFIG_HOME"} {
		_ = os.Unsetenv(v)
	}
	code := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(code)
}
