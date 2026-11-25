// Package blender renders the scenes made from a .blend file: it finds Blender, lists what Cycles can render on,
// runs it in the background with a small inline script and joins the frames it writes into a video. Blender is
// optional: everything here reports its absence as an error that says why and how to fix it.
package blender

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"veritmo/internal/platform/runner"
)

// Minimum is the oldest Blender the render is written for.
const (
	MinMajor = 4
	MinMinor = 2
)

// InstallHint tells a user without Blender what to do.
const InstallHint = "install Blender 4.2 or newer with your system package manager (for example: sudo pacman -S blender)"

// ErrNotInstalled means there is no blender command on the PATH.
var ErrNotInstalled = errors.New("Blender is not installed")

// ErrTooOld means Blender is installed but older than the minimum.
var ErrTooOld = errors.New("Blender is too old")

// Info is the Blender found on this machine.
type Info struct {
	Path    string
	Version string // "5.2.3"
}

var versionRe = regexp.MustCompile(`(\d+)\.(\d+)(?:\.(\d+))?`)

// parseVersion reads the first "Blender 5.2.3" of the output of blender --version: the dotted number and its
// major and minor parts.
func parseVersion(out string) (version string, major, minor int, ok bool) {
	line := strings.SplitN(strings.TrimSpace(out), "\n", 2)[0]
	m := versionRe.FindStringSubmatch(line)
	if m == nil {
		return "", 0, 0, false
	}
	major, _ = strconv.Atoi(m[1])
	minor, _ = strconv.Atoi(m[2])
	return m[0], major, minor, true
}

// Detect finds Blender and checks that it is new enough. The error is ErrNotInstalled or ErrTooOld, wrapped with
// what to do about it.
func Detect(ctx context.Context, r runner.CommandRunner) (Info, error) {
	return DetectWith(ctx, r, exec.LookPath)
}

// DetectWith is Detect with the way the program is looked for given (a test answers for its own machine).
func DetectWith(ctx context.Context, r runner.CommandRunner, lookPath func(string) (string, error)) (Info, error) {
	p, err := lookPath("blender")
	if err != nil {
		return Info{}, fmt.Errorf("%w: %s", ErrNotInstalled, InstallHint)
	}
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := r.Run(ctx, p, []string{"--version"})
	if err != nil {
		return Info{}, fmt.Errorf("cannot ask Blender (%s) for its version: %w", p, err)
	}
	v, major, minor, ok := parseVersion(string(out))
	if !ok {
		return Info{}, fmt.Errorf("cannot read the version of Blender (%s) from %q", p, strings.TrimSpace(string(out)))
	}
	if major < MinMajor || (major == MinMajor && minor < MinMinor) {
		return Info{Path: p, Version: v}, fmt.Errorf("%w: found %s, need %d.%d or newer", ErrTooOld, v, MinMajor, MinMinor)
	}
	return Info{Path: p, Version: v}, nil
}

// devicesScript prints one line per Cycles device ("VERITMO_DEVICE <type> <name>").
const devicesScript = `import bpy
p = bpy.context.preferences.addons['cycles'].preferences
for t in ('OPTIX', 'CUDA'):
    try:
        p.compute_device_type = t
        p.get_devices()
    except Exception:
        continue
    for d in p.devices:
        if d.type == t:
            print('VERITMO_DEVICE', d.type, d.name)
for d in p.devices:
    if d.type == 'CPU':
        print('VERITMO_DEVICE CPU', d.name)
        break
`

// Device is something Cycles can render on.
type Device struct {
	Type string // OPTIX, CUDA or CPU
	Name string
}

// Devices asks Blender which devices Cycles can use. The environment is the one the render will have: with
// CUDA_VISIBLE_DEVICES pinned, only that card is listed.
func Devices(ctx context.Context, r runner.CommandRunner, bin string, env ...string) ([]Device, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	var stderr bytes.Buffer
	out, err := r.Run(ctx, bin, []string{"-b", "-noaudio", "--factory-startup", "--python-expr", devicesScript}, runner.WithEnv(env...), runner.WithStderr(&stderr))
	if err != nil {
		return nil, fmt.Errorf("cannot list the Cycles devices: %w", err)
	}
	return parseDevices(string(out)), nil
}

func parseDevices(out string) []Device {
	var devs []Device
	for _, line := range strings.Split(out, "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "VERITMO_DEVICE ")
		if !ok {
			continue
		}
		typ, name, _ := strings.Cut(rest, " ")
		devs = append(devs, Device{Type: typ, Name: strings.TrimSpace(name)})
	}
	return devs
}

var (
	cacheMu   sync.Mutex
	cacheAt   time.Time
	cacheInfo Info
	cacheErr  error
)

// cacheTTL is how long Cached keeps an answer: the version is needed for the cache keys of every scene, and asking
// starts Blender.
const cacheTTL = time.Minute

// Cached is Detect with the real runner, remembered for a minute.
func Cached(ctx context.Context) (Info, error) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	if !cacheAt.IsZero() && time.Since(cacheAt) < cacheTTL {
		return cacheInfo, cacheErr
	}
	cacheInfo, cacheErr = Detect(ctx, runner.NewExecRunner())
	cacheAt = time.Now()
	return cacheInfo, cacheErr
}
