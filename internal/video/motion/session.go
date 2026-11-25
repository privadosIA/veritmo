package motion

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
	"veritmo/internal/platform/httpclient"
	"veritmo/internal/platform/legacyname"
)

// pageURL is the address the page is served under. Nothing is served there: the
// browser's requests are answered from memory, and every other request fails.
const pageURL = "http://motion.invalid/index.html"

// Page is what a session renders.
type Page struct {
	HTML        string
	Width       int
	Height      int
	Duration    float64 // seconds; told to the page as window.__motionDuration
	Transparent bool    // the background is left transparent (overlays)
	// Song is the song map (JSON) of the piece this page belongs to; the page then reads the beat clock. Empty
	// for any other page.
	Song json.RawMessage
	// Resource answers the requests of the page for anything but the page itself: it gets the address asked for and gives
	// back the body and its type, or false to refuse it (the request then fails and is reported). Nil refuses everything.
	Resource func(url string) (body []byte, contentType string, ok bool)
	// Init is a script that runs on every document before the page's own scripts, after the virtual clock.
	Init string
	// GPU asks the browser for WebGL and WebGPU in software instead of switching the GPU off.
	GPU bool
}

// Limits are the time a session gives the browser; zero values use the defaults.
type Limits struct {
	Load  time.Duration
	Frame time.Duration
}

const (
	defaultLoad  = 20 * time.Second
	defaultFrame = 10 * time.Second
)

// Session is one page loaded in a headless browser with a new profile. It must be closed.
type Session struct {
	page    Page
	limits  Limits
	cmd     *exec.Cmd
	profile string
	c       *cdp

	mu       sync.Mutex
	problems []string
	blocked  map[string]bool

	loaded     chan struct{}
	loadedOnce sync.Once
	closeOnce  sync.Once
}

// shim is injected before the page's own scripts. It gives the page a virtual
// clock that only the renderer moves: Web Animations and CSS animations are
// paused and set to the time, GSAP's global timeline is set to it, performance.now
// and Date.now report it, and requestAnimationFrame callbacks run once per seek
// with it. A page may define window.__seek(t) to do its own drawing.
const shim = `(function(){
  var realRaf = window.requestAnimationFrame.bind(window), vt = 0, rafs = [], rafId = 0, t0 = Date.now(), realNow = Date.now.bind(Date);
  window.__motionDuration = %g;
  performance.now = function(){ return vt; };
  Date.now = function(){ return t0 + vt; };
  window.requestAnimationFrame = function(cb){ rafs.push([++rafId, cb]); return rafId; };
  window.cancelAnimationFrame = function(id){ rafs = rafs.filter(function(r){ return r[0] !== id; }); };
  window.__motionSeek = function(t){
    vt = t * 1000;
    try { if (window.gsap && gsap.globalTimeline) { gsap.ticker && gsap.ticker.lagSmoothing && gsap.ticker.lagSmoothing(0); gsap.globalTimeline.pause(); gsap.globalTimeline.time(t, false); } } catch(e){}
    document.getAnimations().forEach(function(a){ try { a.pause(); a.currentTime = vt; } catch(e){} });
    var sp = (typeof window.__seek === 'function') ? window.__seek(t) : null;
    var q = rafs; rafs = [];
    q.forEach(function(r){ try { r[1](vt); } catch(e){ setTimeout(function(){ throw e; }); } });
    return Promise.resolve(sp).then(function(){ return new Promise(function(res){ realRaf(function(){ realRaf(function(){ res(true); }); }); }); });
  };
})();`

// Shim is the script that gives a page its virtual clock, set for a page of the given duration.
// The preview in the editor injects the same script the renderer does.
func Shim(duration float64) string { return fmt.Sprintf(shim, duration) + timelineScript }

// Open starts the browser, loads the page and waits for it and its fonts.
func Open(ctx context.Context, browser string, p Page, lim Limits) (*Session, error) {
	if lim.Load <= 0 {
		lim.Load = defaultLoad
	}
	if lim.Frame <= 0 {
		lim.Frame = defaultFrame
	}
	if p.Width <= 0 || p.Height <= 0 {
		return nil, errors.New("the page needs a width and a height")
	}
	s := &Session{page: p, limits: lim, blocked: map[string]bool{}, loaded: make(chan struct{})}
	if err := s.start(ctx, browser); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

var listening = regexp.MustCompile(`DevTools listening on ws://([^/\s]+)/`)

// browserArgs are the flags the browser is started with: a profile of its own, nothing from the network, and no
// keyring (--password-store=basic): a broken desktop keyring makes Chrome wait for it before it loads any page, and a
// browser that draws videos has no passwords to keep.
func browserArgs(profile string) []string {
	return []string{
		"--headless", "--remote-debugging-port=0", "--user-data-dir=" + profile,
		"--disable-extensions", "--disable-sync", "--disable-background-networking", "--disable-component-update",
		"--no-first-run", "--no-default-browser-check", "--hide-scrollbars", "--mute-audio",
		"--force-device-scale-factor=1", "--password-store=basic",
		"--disable-gpu", "--host-resolver-rules=MAP * ~NOTFOUND",
		"about:blank",
	}
}

// gpuArgs replaces --disable-gpu by what lets headless Chrome draw WebGL and WebGPU on the CPU (SwiftShader), the same on
// every machine.
func gpuArgs(args []string) []string {
	out := make([]string, 0, len(args)+6)
	for _, a := range args {
		if a != "--disable-gpu" {
			out = append(out, a)
		}
	}
	if legacyname.Getenv("BROWSER_GPU") == "hardware" {
		// the card of this machine through Vulkan (faster for heavy WebGL scenes; the frames can differ a little between machines)
		out = append(out, "--use-gl=angle", "--use-angle=vulkan", "--enable-features=Vulkan,VulkanFromANGLE", "--ignore-gpu-blocklist", "--enable-unsafe-webgpu",
			"--enable-blink-features=CanvasDrawElement", "--disable-vulkan-surface", "--unsafely-treat-insecure-origin-as-secure="+pageURL)
		if extra := legacyname.Getenv("BROWSER_GPU_FLAGS"); extra != "" {
			out = append(out, strings.Fields(extra)...)
		}
		return out
	}
	return append(out, "--use-gl=angle", "--use-angle=swiftshader", "--enable-unsafe-swiftshader", "--ignore-gpu-blocklist",
		"--enable-unsafe-webgpu", "--enable-features=Vulkan", "--enable-blink-features=CanvasDrawElement", "--use-webgpu-adapter=swiftshader", "--use-vulkan=swiftshader",
		"--unsafely-treat-insecure-origin-as-secure="+pageURL)
}

// gpuShim makes a request for a WebGPU adapter with a power preference that finds none (the browser of a server has no "high-performance" adapter
// with two cards of different makes) fall back to the default adapter, so a scene that asks for the fast one still draws.
func (s *Session) gpuShim() string {
	if !s.page.GPU {
		return ""
	}
	return `(function(){try{if(navigator.gpu&&navigator.gpu.requestAdapter){var o=navigator.gpu.requestAdapter.bind(navigator.gpu);navigator.gpu.requestAdapter=function(opt){return o(opt).then(function(a){return a||o({});});};}}catch(e){}})();
`
}

func (s *Session) start(ctx context.Context, browser string) error {
	dir, err := os.MkdirTemp("", "motion-profile-*")
	if err != nil {
		return err
	}
	s.profile = dir
	args := browserArgs(dir)
	if s.page.GPU {
		args = gpuArgs(args)
	}
	if os.Geteuid() == 0 {
		args = append([]string{"--no-sandbox"}, args...) // Chrome refuses to run as root otherwise
	}
	cmd := exec.Command(browser, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("cannot start the browser: %w", err)
	}
	s.cmd = cmd

	addr := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stderr)
		sent := false
		for sc.Scan() {
			if m := listening.FindStringSubmatch(sc.Text()); m != nil && !sent {
				addr <- m[1]
				sent = true
			}
		}
		if !sent {
			close(addr)
		}
		_, _ = io.Copy(io.Discard, stderr)
	}()
	lctx, cancel := context.WithTimeout(ctx, s.limits.Load)
	defer cancel()
	var host string
	select {
	case host = <-addr:
		if host == "" {
			return errors.New("the browser exited before it was ready")
		}
	case <-lctx.Done():
		return fmt.Errorf("the browser did not start in %s", s.limits.Load)
	}

	wsURL, err := pageSocket(lctx, host)
	if err != nil {
		return err
	}
	if s.c, err = dialCDP(lctx, wsURL, s.onEvent); err != nil {
		return err
	}
	return s.load(lctx)
}

// pageSocket asks the browser for the debugging address of its first page.
func pageSocket(ctx context.Context, host string) (string, error) {
	for {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+host+"/json/list", nil)
		resp, err := httpclient.Short.Do(req)
		if err == nil {
			var list []struct {
				Type string `json:"type"`
				WS   string `json:"webSocketDebuggerUrl"`
			}
			err = json.NewDecoder(resp.Body).Decode(&list)
			resp.Body.Close()
			if err == nil {
				for _, t := range list {
					if t.Type == "page" && t.WS != "" {
						return t.WS, nil
					}
				}
			}
		}
		select {
		case <-ctx.Done():
			return "", errors.New("the browser has no page to load")
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (s *Session) addProblem(msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.problems {
		if p == msg {
			return
		}
	}
	if len(s.problems) < 20 {
		s.problems = append(s.problems, msg)
	}
}

// onEvent runs on the reader goroutine: it must not wait for an answer, so the
// requests it must answer are answered from another goroutine.
func (s *Session) onEvent(method string, params json.RawMessage) {
	switch method {
	case "Page.loadEventFired":
		s.loadedOnce.Do(func() { close(s.loaded) })
	case "Fetch.requestPaused":
		var e struct {
			RequestID string `json:"requestId"`
			Request   struct {
				URL string `json:"url"`
			} `json:"request"`
		}
		if json.Unmarshal(params, &e) != nil {
			return
		}
		go s.answerRequest(e.RequestID, e.Request.URL)
	case "Runtime.exceptionThrown":
		var e struct {
			ExceptionDetails struct {
				Text      string `json:"text"`
				Exception struct {
					Description string `json:"description"`
				} `json:"exception"`
			} `json:"exceptionDetails"`
		}
		if json.Unmarshal(params, &e) == nil {
			msg := e.ExceptionDetails.Exception.Description
			if msg == "" {
				msg = e.ExceptionDetails.Text
			}
			s.addProblem("script error: " + firstLine(msg))
		}
	case "Runtime.consoleAPICalled":
		var e struct {
			Type string `json:"type"`
			Args []struct {
				Value       any    `json:"value"`
				Description string `json:"description"`
			} `json:"args"`
		}
		if json.Unmarshal(params, &e) == nil && e.Type == "error" {
			var parts []string
			for _, a := range e.Args {
				if a.Value != nil {
					parts = append(parts, fmt.Sprint(a.Value))
				} else {
					parts = append(parts, a.Description)
				}
			}
			s.addProblem("console.error: " + firstLine(strings.Join(parts, " ")))
		}
	}
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}

// answerRequest serves the page from memory and fails everything else, so the
// page reaches neither the network nor any file.
func (s *Session) answerRequest(id, url string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if url == pageURL {
		body := base64.StdEncoding.EncodeToString([]byte(s.page.HTML))
		_ = s.c.call(ctx, "Fetch.fulfillRequest", map[string]any{
			"requestId": id, "responseCode": 200, "body": body,
			"responseHeaders": []map[string]string{
				{"name": "Content-Type", "value": "text/html; charset=utf-8"},
			},
		}, nil)
		return
	}
	if url == "http://motion.invalid/favicon.ico" {
		_ = s.c.call(ctx, "Fetch.fulfillRequest", map[string]any{"requestId": id, "responseCode": 204}, nil)
		return
	}
	if s.page.Resource != nil {
		if body, ct, ok := s.page.Resource(url); ok {
			_ = s.c.call(ctx, "Fetch.fulfillRequest", map[string]any{
				"requestId": id, "responseCode": 200, "body": base64.StdEncoding.EncodeToString(body),
				"responseHeaders": []map[string]string{{"name": "Content-Type", "value": ct}, {"name": "Access-Control-Allow-Origin", "value": "*"}},
			}, nil)
			return
		}
	}
	s.mu.Lock()
	first := !s.blocked[url]
	s.blocked[url] = true
	s.mu.Unlock()
	if first && !strings.HasPrefix(url, "data:") {
		s.addProblem("the page asked for a resource outside itself: " + clip(url, 120))
	}
	_ = s.c.call(ctx, "Fetch.failRequest", map[string]any{"requestId": id, "errorReason": "BlockedByClient"}, nil)
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

func (s *Session) load(ctx context.Context) error {
	for _, step := range []struct {
		method string
		params any
	}{
		{"Page.enable", nil},
		{"Runtime.enable", nil},
		{"Fetch.enable", map[string]any{"patterns": []map[string]string{{"urlPattern": "*"}}}},
		{"Emulation.setDeviceMetricsOverride", map[string]any{"width": s.page.Width, "height": s.page.Height, "deviceScaleFactor": 1, "mobile": false}},
		{"Page.addScriptToEvaluateOnNewDocument", map[string]any{"source": ShimSong(s.page.Duration, s.page.Song) + "\n" + s.gpuShim() + s.page.Init}},
	} {
		if err := s.c.call(ctx, step.method, step.params, nil); err != nil {
			return err
		}
	}
	if s.page.Transparent {
		if err := s.c.call(ctx, "Emulation.setDefaultBackgroundColorOverride", map[string]any{"color": map[string]int{"r": 0, "g": 0, "b": 0, "a": 0}}, nil); err != nil {
			return err
		}
	}
	if err := s.c.call(ctx, "Page.navigate", map[string]any{"url": pageURL}, nil); err != nil {
		return err
	}
	select {
	case <-s.loaded:
	case <-ctx.Done():
		return fmt.Errorf("the page did not finish loading in %s", s.limits.Load)
	}
	return s.c.call(ctx, "Runtime.evaluate", map[string]any{"expression": "document.fonts ? document.fonts.ready.then(function(){return true}) : true", "awaitPromise": true}, nil)
}

// Frame renders the page at t seconds and returns the PNG.
func (s *Session) Frame(ctx context.Context, t float64) ([]byte, error) {
	fctx, cancel := context.WithTimeout(ctx, s.limits.Frame)
	defer cancel()
	var ev struct {
		Exception *struct {
			Text      string `json:"text"`
			Exception struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}
	err := s.c.call(fctx, "Runtime.evaluate", map[string]any{
		"expression": fmt.Sprintf("window.__motionSeek(%g)", t), "awaitPromise": true,
	}, &ev)
	if err != nil {
		return nil, s.frameError(ctx, t, err)
	}
	if ev.Exception != nil {
		msg := ev.Exception.Exception.Description
		if msg == "" {
			msg = ev.Exception.Text
		}
		return nil, fmt.Errorf("the page failed at %.2fs: %s", t, firstLine(msg))
	}
	var shot struct {
		Data string `json:"data"`
	}
	if err := s.c.call(fctx, "Page.captureScreenshot", map[string]any{"format": "png", "fromSurface": true}, &shot); err != nil {
		return nil, s.frameError(ctx, t, err)
	}
	return base64.StdEncoding.DecodeString(shot.Data)
}

func (s *Session) frameError(ctx context.Context, t float64, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("the page did not answer in %s at %.2fs (an endless loop?)", s.limits.Frame, t)
	}
	return err
}

// Problems are the script errors, console errors and blocked requests seen so far.
func (s *Session) Problems() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.problems...)
}

// Close stops the browser and every process it started and deletes its profile.
func (s *Session) Close() {
	s.closeOnce.Do(func() {
		if s.c != nil {
			s.c.close()
		}
		if s.cmd != nil && s.cmd.Process != nil {
			_ = syscall.Kill(-s.cmd.Process.Pid, syscall.SIGKILL)
			_ = s.cmd.Wait()
		}
		if s.profile != "" {
			_ = os.RemoveAll(s.profile)
		}
	})
}
