package musicsvc

// service.go: the generators and CLAP as .NET services run by this program as ordinary processes (published by
// scripts/get-services.sh into bin/services/<name>). See specs/music-containers.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ServiceSpec says how the .NET service of a generator is found and configured.
type ServiceSpec struct {
	// Dir is the folder under bin/services that holds the published service; DLL its entry point there.
	Dir string
	DLL string
	// Prefix is the prefix of its environment variables (HEARTMULA gives HEARTMULA_CKPT and HEARTMULA_PORT).
	Prefix string
	// Device says the service also reads PREFIX_DEVICE; it then sees only the card it was given (as cuda:0).
	Device bool
	// Cards says the service takes its devices by number (Model:Device and Codec:Device, as HeartMuLa does): all the
	// cards stay visible, the model goes to the card chosen and the codec to the other card with most free memory
	// (or the same one when there is no other). HeartMuLa's codec needs about 13 GB, which does not fit with the
	// model on a 12 GB card.
	Cards bool
	// Espeak says the service needs the espeak-ng library that sits in bin/.
	Espeak bool
	// Env are further variables (NAME=value) the service is started with, for what is the same on every machine (TRIPOSR_HOST=127.0.0.1).
	Env []string
}

// ServiceHost is where the services are installed and where the program keeps what it knows of the ones it started.
type ServiceHost struct {
	// Dir is bin/services (absolute); StateDir is where the pid, port and log of each running service are kept.
	Dir      string
	StateDir string
	// BinDir is bin (absolute), for the espeak-ng library.
	BinDir string
	// Dotnet is the program that runs a service's DLL ("dotnet" when empty).
	Dotnet string
}

func (h ServiceHost) dotnet() string {
	if h.Dotnet != "" {
		return h.Dotnet
	}
	return "dotnet"
}

// DLLPath is the entry point of a service.
func (h ServiceHost) DLLPath(s ServiceSpec) string { return filepath.Join(h.Dir, s.Dir, s.DLL) }

// Installed reports whether the service is published.
func (h ServiceHost) Installed(s ServiceSpec) bool {
	fi, err := os.Stat(h.DLLPath(s))
	return err == nil && fi.Mode().IsRegular()
}

// DotnetFound reports whether the program that runs the services is installed.
func (h ServiceHost) DotnetFound() error {
	_, err := exec.LookPath(h.dotnet())
	return err
}

type serviceState struct {
	PID   int    `json:"pid"`
	Port  int    `json:"port"`
	GPU   int    `json:"gpu"`
	Since string `json:"since"`
}

func (h ServiceHost) statePath(name string) string { return filepath.Join(h.StateDir, name+".json") }

// LogPath is the file the service writes its output to.
func (h ServiceHost) LogPath(name string) string { return filepath.Join(h.StateDir, name+".log") }

func (h ServiceHost) readState(name string) (serviceState, bool) {
	data, err := os.ReadFile(h.statePath(name))
	if err != nil {
		return serviceState{}, false
	}
	var st serviceState
	if json.Unmarshal(data, &st) != nil || st.PID <= 0 {
		return serviceState{}, false
	}
	// A pid can be reused by another program after a reboot: it must still be the service's process. For a moment
	// after the start the process has not replaced the environment it was forked with, so a young one is accepted.
	environ, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", st.PID))
	marked := err == nil && strings.Contains("\x00"+string(environ), "\x00"+serviceMarker(name)+"\x00")
	if !marked {
		young := false
		if t, err := time.Parse(time.RFC3339, st.Since); err == nil && time.Since(t) < 10*time.Second && alive(st.PID) {
			young = true
		}
		if !young {
			_ = os.Remove(h.statePath(name))
			return serviceState{}, false
		}
	}
	return st, true
}

// serviceMarker is an environment variable of the process, so it can be told from another program with the same pid.
func serviceMarker(name string) string { return "VERITMO_SERVICE=" + name }

// start runs the service of an engine on a card and port, detached from this program (it survives a restart of the
// server), with the weights directory as its checkpoint.
func (h ServiceHost) start(ctx context.Context, l Local, gpu GPUInfo, port int) error {
	sp := l.Spec.Service
	if !h.Installed(sp) {
		return fmt.Errorf("the %s service is not installed: run scripts/get-services.sh", l.Spec.Name)
	}
	if err := os.MkdirAll(h.StateDir, 0o755); err != nil {
		return err
	}
	logf, err := os.OpenFile(h.LogPath(l.Spec.Container), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer logf.Close()
	dll := h.DLLPath(sp)
	cmd := exec.Command(h.dotnet(), dll)
	cmd.Dir = filepath.Dir(dll)
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	env := append(os.Environ(),
		serviceMarker(l.Spec.Container),
		"CUDA_DEVICE_ORDER=PCI_BUS_ID",
		sp.Prefix+"_CKPT="+l.Models,
		sp.Prefix+"_PORT="+strconv.Itoa(port),
		"DOTNET_gcServer=0",
	)
	env = append(env, sp.Env...)
	switch {
	case sp.Cards:
		codec := gpu.Index
		if gpus, err := l.GPUs(ctx); err == nil {
			best := int64(-1)
			for _, g := range gpus {
				if g.Index != gpu.Index && g.FreeMB > best {
					codec, best = g.Index, g.FreeMB
				}
			}
		}
		env = append(env, "Model__Device=cuda:"+strconv.Itoa(gpu.Index), "Codec__Device=cuda:"+strconv.Itoa(codec))
	default:
		env = append(env, "CUDA_VISIBLE_DEVICES="+strconv.Itoa(gpu.Index))
		if sp.Device {
			env = append(env, sp.Prefix+"_DEVICE=cuda:0")
		}
	}
	if sp.Espeak && h.BinDir != "" {
		env = append(env,
			sp.Prefix+"_ESPEAK_LIB="+filepath.Join(h.BinDir, "libespeak-ng.so.1"),
			sp.Prefix+"_ESPEAK_DATA="+filepath.Join(h.BinDir, "espeak-ng-data"),
			"LD_LIBRARY_PATH="+h.BinDir+":"+os.Getenv("LD_LIBRARY_PATH"))
	}
	cmd.Env = env
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting the %s service: %w", l.Spec.Name, err)
	}
	st := serviceState{PID: cmd.Process.Pid, Port: port, GPU: gpu.Index, Since: time.Now().UTC().Format(time.RFC3339)}
	data, _ := json.Marshal(st)
	if err := os.WriteFile(h.statePath(l.Spec.Container), data, 0o644); err != nil {
		_ = syscall.Kill(-st.PID, syscall.SIGKILL)
		return err
	}
	go func() { _ = cmd.Wait() }() // reaps it if it ends while this program lives
	return nil
}

// stop ends the service (its whole process group, which includes DiffRhythm2's song workers) and forgets it. It is
// not an error that there is none.
func (h ServiceHost) stop(ctx context.Context, name string) error {
	st, ok := h.readState(name)
	if !ok {
		_ = os.Remove(h.statePath(name))
		return nil
	}
	_ = syscall.Kill(-st.PID, syscall.SIGTERM)
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if !alive(st.PID) {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	if alive(st.PID) {
		_ = syscall.Kill(-st.PID, syscall.SIGKILL)
		time.Sleep(300 * time.Millisecond)
		if alive(st.PID) {
			return errors.New("the service did not stop")
		}
	}
	_ = os.Remove(h.statePath(name))
	return nil
}

func alive(pid int) bool {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	// "pid (comm) S ...": a zombie has ended.
	if i := strings.LastIndexByte(string(b), ')'); i >= 0 && len(b) > i+2 {
		return b[i+2] != 'Z'
	}
	return true
}

// memoryMB is the resident memory of the service's process.
func memoryMB(pid int) int64 {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		if rest, ok := strings.CutPrefix(line, "VmRSS:"); ok {
			f := strings.Fields(rest)
			if len(f) > 0 {
				if kb, err := strconv.ParseInt(f[0], 10, 64); err == nil {
					return kb / 1024
				}
			}
		}
	}
	return 0
}
