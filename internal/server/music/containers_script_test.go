package music

import (
	"os"
	"regexp"
	"strconv"
	"testing"

	"veritmo/internal/music/diffrhythm2"
	"veritmo/internal/music/heartmula"
)

// scripts/music-containers.sh and the server run the same containers: the same images, ports, name and flags. The server's side is the Docker
// spec of each generator (musicsvc.DockerSpec) and musicsvc.Local.dockerStart; if one changes without the other, this fails.
func TestContainersScriptAgreesWithTheServer(t *testing.T) {
	raw, err := os.ReadFile("../../../scripts/music-containers.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := string(raw)
	table := func(name string) map[string]string {
		m := regexp.MustCompile(`declare -A ` + name + `=\(([^)]*)\)`).FindStringSubmatch(script)
		if m == nil {
			t.Fatalf("the script has no table %s", name)
		}
		out := map[string]string{}
		for _, kv := range regexp.MustCompile(`\[(\w+)\]=(\S+)`).FindAllStringSubmatch(m[1], -1) {
			out[kv[1]] = kv[2]
		}
		return out
	}
	ports, images := table("PORT"), table("IMAGE")
	for name, spec := range map[string]struct {
		container, image string
		port             int
	}{
		"heartmula":   {heartmula.Spec().Container, heartmula.Spec().Docker.Image, heartmula.Spec().Docker.Port},
		"diffrhythm2": {diffrhythm2.Spec().Container, diffrhythm2.Spec().Docker.Image, diffrhythm2.Spec().Docker.Port},
	} {
		if images[name] != spec.image {
			t.Errorf("%s: the script runs the image %q, the server %q", name, images[name], spec.image)
		}
		if ports[name] != strconv.Itoa(spec.port) {
			t.Errorf("%s: the script listens on %q, the server on %d", name, ports[name], spec.port)
		}
		if spec.container != "vt-"+name {
			t.Errorf("%s: the script names the container vt-%s, the server %q", name, name, spec.container)
		}
	}
	for _, flag := range []string{`--restart no`, `"127.0.0.1:${PORT[$engine]}:${PORT[$engine]}"`, `:/ckpt:ro`, `--gpus "device=${gpu}"`, `--name "vt-${engine}"`} {
		if !regexp.MustCompile(regexp.QuoteMeta(flag)).MatchString(script) {
			t.Errorf("the script no longer runs the container with %s, which the server does", flag)
		}
	}
}
