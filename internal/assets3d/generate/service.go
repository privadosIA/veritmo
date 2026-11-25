package generate

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"veritmo/internal/platform/musicsvc"
	"veritmo/internal/platform/runner"
)

// Service is the 3D service on this machine: the container of docker/model3d, the weights it reads, and what a person has to do for an engine
// that is gated on Hugging Face. It is built from the same parts as the music engines.
type Service struct {
	Runner runner.CommandRunner
	// ModelsDir is the absolute path of the weights folder (models/model3d).
	ModelsDir string
	// HFKey gives the saved Hugging Face key ("" when there is none); the server passes the one of PUT /api/tokens/huggingface.
	HFKey func() string
	// HTTP asks Hugging Face whether a key may read the repository of a gated engine (default: a client that does not follow redirects).
	HTTP *http.Client
	// HFBase is Hugging Face's address (default https://huggingface.co); the tests give theirs.
	HFBase string
	// NoStart says that opening the service never starts it (the server: the person starts it); the command line starts it.
	NoStart bool
	// External is a service that runs elsewhere (a container of the compose stack, another machine): with an address, nothing is started, stopped
	// or installed from here, and pictures go to that address. Setting it is the operator's decision to send them there.
	External musicsvc.External
	// PreferGPU is the card to start it on (a CUDA index as text); empty chooses the one with most free memory.
	PreferGPU string
	// Services is where the .NET services are published (bin/services) and runs them; nil leaves every engine to the container.
	Services *musicsvc.ServiceHost
	// Backend says how the engines run here, musicsvc.BackendDotnet (one service per engine, the default) or musicsvc.BackendDocker (the one
	// container that serves both, the fallback); nil is the default.
	Backend func() string
}

// BackendOf is how an engine runs on this machine: the container when there is no host to run services (or an engine without a .NET service), otherwise what Backend says (the .NET service by default).
func (s *Service) BackendOf(engine string) string {
	if !HasService(engine) || s.Services == nil {
		return musicsvc.BackendDocker
	}
	if s.Backend != nil && s.Backend() == musicsvc.BackendDocker {
		return musicsvc.BackendDocker
	}
	return musicsvc.BackendDotnet
}

// WithBackend is a copy of the service that runs the engines on the backend given, whatever the setting says: what an install asks about the way that is
// not the one in use.
func (s *Service) WithBackend(backend string) *Service {
	c := *s
	c.Backend = func() string { return backend }
	return &c
}

// specOn is the spec of an engine on a backend: the same, except that the .NET service of Stable Fast 3D is known by a name of its own, because its
// state (pid, port, log) is kept by name and the two engines run side by side as services (in the container they are one process, one name).
func specOn(name, backend string) musicsvc.Spec {
	spec := Spec(name)
	if name == EngineSF3D && backend != musicsvc.BackendDocker {
		spec.Container += "-sf3d"
	}
	return spec
}

func (s *Service) engine(name string) *musicsvc.Engine {
	spec := specOn(name, s.BackendOf(name))
	return &musicsvc.Engine{
		Spec:      spec,
		Local:     musicsvc.Local{Spec: spec, Runner: s.Runner, Models: s.ModelsDir, Services: s.Services, Backend: s.BackendOf(name)},
		External:  s.External,
		NoStart:   s.NoStart,
		PreferGPU: s.PreferGPU,
	}
}

func (s *Service) hfBase() string {
	if s.HFBase != "" {
		return strings.TrimRight(s.HFBase, "/")
	}
	return "https://huggingface.co"
}

func (s *Service) hfHTTP() *http.Client {
	if s.HTTP != nil {
		return s.HTTP
	}
	return &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// builtEngines are the engines the image was built with, from its label; false when the image cannot be asked.
func (s *Service) builtEngines(ctx context.Context) ([]string, bool) {
	out, err := s.Runner.Run(ctx, musicsvc.DockerProgram(), []string{"image", "inspect", "-f", `{{ index .Config.Labels "` + EnginesLabel + `" }}`, Image})
	if err != nil {
		return nil, false
	}
	label := strings.TrimSpace(string(out))
	if label == "" || label == "<no value>" {
		return []string{EngineTripoSR}, true // an image without the label is the one that has only the default engine
	}
	return strings.Split(label, ","), true
}

// Availability checks, without starting anything, whether an engine can be used, in the order a person would fix what is missing: Docker, its
// image and runtime; the engine being in the image; for a gated engine the key and the licence; then the weights, the card and the container.
func (s *Service) Availability(ctx context.Context, name string) musicsvc.Availability {
	info, ok := Info(name)
	if !ok {
		return musicsvc.Availability{Reason: "unknown_engine", Detail: fmt.Sprintf("the engine must be one of %s", strings.Join(Engines(), ", "))}
	}
	eng := s.engine(name)
	a := eng.Availability(ctx)
	if s.External.Host != "" {
		return a // the service is somewhere else: what is in its image and its weights is what it says in its health
	}
	if HasService(name) && s.Services != nil {
		a.Backends = eng.Backends(ctx) // both ways: say what each lacks
	}
	if s.BackendOf(name) != musicsvc.BackendDocker {
		// the .NET service has no image; its gated weights still need the key and the licence before they can be fetched
		if info.Gated && a.Reason == musicsvc.ReasonNoWeights && NeedsKey(a.Missing) {
			if reason, detail := s.CheckGate(ctx); reason != "" {
				a.Reason, a.Detail = reason, detail
			}
		}
		return a
	}
	switch a.Reason {
	case musicsvc.ReasonNoDocker, musicsvc.ReasonNoImage, musicsvc.ReasonNoGPURuntime:
		return a
	}
	if built, known := s.builtEngines(ctx); known {
		has := false
		for _, e := range built {
			has = has || e == name
		}
		if !has {
			a.Available, a.Reason = false, ReasonNotBuilt
			a.Detail = fmt.Sprintf("the image %s was built without %s: build it with SF3D=1 (docker/model3d/README.md)", Image, info.Name)
			return a
		}
	}
	if info.Gated && a.Reason == musicsvc.ReasonNoWeights {
		// the weights cannot be installed until the account has what Hugging Face asks for: say that first
		if reason, detail := s.CheckGate(ctx); reason != "" {
			a.Reason, a.Detail = reason, detail
		}
	}
	return a
}

// CheckGate asks Hugging Face whether the saved key may read the repository of Stable Fast 3D. It never accepts a licence or tries another way in:
// it reports what is missing. "" means the key is accepted.
func (s *Service) CheckGate(ctx context.Context) (reason, detail string) {
	repoPage := s.hfBase() + "/" + SF3DRepo
	key := ""
	if s.HFKey != nil {
		key = strings.TrimSpace(s.HFKey())
	}
	if key == "" {
		return ReasonKeyMissing, "Stable Fast 3D needs the Hugging Face key of an account that has accepted the licence of " + repoPage + ": accept it on that page with your own account, then save the key with PUT /api/tokens/huggingface"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, repoPage+"/resolve/main/config.yaml", nil)
	if err != nil {
		return musicsvc.ReasonUnreachable, err.Error()
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := s.hfHTTP().Do(req)
	if err != nil {
		return musicsvc.ReasonUnreachable, "cannot ask Hugging Face about the licence: " + err.Error()
	}
	resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return musicsvc.ReasonKeyRejected, "Hugging Face does not know the saved key: save another one with PUT /api/tokens/huggingface"
	case resp.StatusCode == http.StatusForbidden:
		return ReasonLicenceNotAccepted, "Hugging Face refuses the saved key for " + SF3DRepo + ": its account has to accept the licence at " + repoPage
	case resp.StatusCode >= 400:
		return musicsvc.ReasonUnreachable, fmt.Sprintf("Hugging Face answered %d when asked about %s", resp.StatusCode, SF3DRepo)
	}
	return "", ""
}

// Open returns a client of the service ready to make a model with an engine, starting the container when the service lets it, and a function that
// stops what it started. It fails with the reason of the availability when the engine cannot be used.
func (s *Service) Open(ctx context.Context, name string) (*Client, func(), error) {
	if a := s.Availability(ctx, name); !a.Available && a.Reason != musicsvc.ReasonNotRunning {
		return nil, func() {}, &musicsvc.UnavailableError{Reason: a.Reason, Detail: a.Detail}
	}
	e := s.engine(name)
	c, err := e.Open(ctx)
	if err != nil {
		return nil, func() {}, err
	}
	return &Client{BaseURL: c.BaseURL, Token: c.Token}, e.Close, nil
}

// Start starts the service of an engine on a card (the one asked for, else the best): the .NET service on a free port of loopback, or the
// container of the image on its port. Stop ends it and State says whether it runs.
func (s *Service) Start(ctx context.Context, engine string) error {
	e := s.engine(engine)
	l := e.Local
	gpus, err := l.GPUs(ctx)
	if err != nil {
		return &musicsvc.UnavailableError{Reason: musicsvc.ReasonNoGPU, Detail: fmt.Sprintf("no GPU can be read: %v", err)}
	}
	gpu, err := musicsvc.PickGPU(e.Spec, gpus, s.PreferGPU)
	if err != nil {
		return err
	}
	port := Port
	if !l.UsesDocker() {
		if port, err = musicsvc.FreePort(); err != nil {
			return err
		}
	}
	return l.Start(ctx, gpu, port)
}

// Stop ends the service of an engine (it is not an error that there is none). The container of the other engine, which is the one image, is not touched.
func (s *Service) Stop(ctx context.Context, engine string) error {
	return s.engine(engine).Local.Stop(ctx)
}

// State says whether the service of an engine runs, on which card and since when.
func (s *Service) State(ctx context.Context, engine string) musicsvc.ContainerState {
	return s.engine(engine).Local.State(ctx, false)
}

// IsExternal says the service runs somewhere else, so its container and weights are not managed from here.
func (s *Service) IsExternal() bool { return s.External.Host != "" }
