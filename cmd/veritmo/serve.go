package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	mediapkg "veritmo/internal/platform/media"

	"veritmo/internal/platform/settings"
	"veritmo/internal/server"
)

// stringList is a repeatable string flag.
type stringList []string

func (l *stringList) String() string     { return strings.Join(*l, ",") }
func (l *stringList) Set(v string) error { *l = append(*l, v); return nil }

// isLoopbackAddr reports whether a listen address binds only to loopback.
// Empty hosts (":4666") and unspecified ones (0.0.0.0, ::) are not.
func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// runServe implements the `veritmo serve` subcommand. Like `import`,
// it dispatches before pipeline flag parsing so the existing CLI surface
// is untouched (see specs/http-server).
func runServe(args []string, stdout, stderr io.Writer) int {
	args, showAll := contentOption(args)
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	addr := fs.String("addr", "127.0.0.1:4666", "Listen address (loopback by default; set explicitly to expose beyond this machine)")
	projectsDir := fs.String("projects-dir", "projects", "Directory whose subdirectories holding a scenes.yaml are the managed projects")
	mediaDir := fs.String("media-dir", "", "Directory where whole projects are kept, possibly on another disk (or set MEDIA_DIR); the project folder keeps links into it. Empty: projects stay whole inside --projects-dir")
	modelsDir := fs.String("models-dir", "", "Directory where the model files live, possibly on another disk (or set MODELS_DIR, or write it in ~/.config/veritmo/models-dir); models/ is a link to it (see `veritmo models migrate`)")
	dbPath := fs.String("db", ".orchestrator/orchestrator.db", "SQLite file for the run history")
	token := fs.String("token", "", "Bearer token required on /api/* (or set ORCHESTRATOR_TOKEN; the env var avoids exposing it in ps)")
	insecure := fs.Bool("insecure-no-auth", false, "Allow a non-loopback --addr without a token (API exposed unauthenticated)")
	strict := fs.Bool("strict", false, "Also hide the projects that were not marked as reviewed (adult: false); by default only the ones marked for adults (adult: true) are hidden")
	logLevel := fs.String("log-level", "info", "How much the server logs on stderr: debug, info, warn or error")
	webDir := fs.String("web-dir", "", "Directory of a built front end to serve at the root (or set ORCHESTRATOR_WEB_DIR); the server serves its files and does not care which front end it is")
	var corsOrigins stringList
	fs.Var(&corsOrigins, "cors-origin", "Allowed cross-origin web UI origin, e.g. http://host:4200 (repeatable; default: same-origin only)")
	fs.Usage = func() {
		fmt.Fprintf(stderr, "Usage: veritmo serve [flags]\n\nFlags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}

	tok := *token
	if tok == "" {
		var err error
		// the token may be a file (ORCHESTRATOR_TOKEN_FILE, a Docker secret): it never shows in the environment of the process
		if tok, err = settings.Env("ORCHESTRATOR_TOKEN"); err != nil {
			fmt.Fprintf(stderr, "fatal: %v\n", err)
			return 2
		}
	}
	media := *mediaDir
	if media == "" {
		media = os.Getenv("MEDIA_DIR")
	}
	web := *webDir
	if web == "" {
		web = os.Getenv("ORCHESTRATOR_WEB_DIR")
	}
	if web != "" {
		if fi, err := os.Stat(web); err != nil || !fi.IsDir() {
			fmt.Fprintf(stderr, "fatal: --web-dir %s is not a directory\n", web)
			return 2
		}
	}
	if !isLoopbackAddr(*addr) && tok == "" {
		if !*insecure {
			fmt.Fprintf(stderr, "fatal: refusing to listen on non-loopback address %s without an access token; set --token (or ORCHESTRATOR_TOKEN), or pass --insecure-no-auth to expose the API unauthenticated\n", *addr)
			return 2
		}
		fmt.Fprintf(stderr, "WARNING: serving on %s with NO authentication: anyone who can reach it can launch runs and modify projects\n", *addr)
	}

	// Make the repo-local bin/ (scripts/build-sd.sh, get-piper.sh drop
	// binaries there) resolvable for preflight, runs, and the environment
	// report alike, matching docs/INSTALL.md's "on PATH or resolvable from the
	// project root".
	if abs, err := filepath.Abs("bin"); err == nil {
		if fi, err := os.Stat(abs); err == nil && fi.IsDir() {
			_ = os.Setenv("PATH", abs+string(os.PathListSeparator)+os.Getenv("PATH"))
		}
	}

	mode := server.ContentCensored
	switch {
	case showAll && *strict:
		fmt.Fprintln(stderr, "fatal: --strict and the option that shows everything cannot be used together")
		return 2
	case showAll:
		mode = server.ContentAll
	case *strict:
		mode = server.ContentStrict
	}
	services := musicServices(*dbPath)
	lvl, lerr := parseLogLevel(*logLevel)
	if lerr != nil {
		fmt.Fprintf(stderr, "fatal: %v\n", lerr)
		return 2
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: lvl})))

	// The database is opened and checked here: the server never changes the schema. When migrations are pending it does not start and says
	// to run `veritmo data migrate`, which the deployment workflow does.
	st, err := openStore(*dbPath, *projectsDir, settings.GetDatabase(), server.WithMediaDir(media))
	if err != nil {
		fmt.Fprintf(stderr, "fatal: %v\n", err)
		return 1
	}
	defer func() { _ = st.Close() }()
	if err := st.CheckMigrated(); err != nil {
		fmt.Fprintf(stderr, "fatal: %v\n", err)
		return 1
	}

	srv, err := server.New(server.Config{
		Store:         st,
		Services:      services,
		DatabaseState: true,
		Database:      settings.GetDatabase(),
		ServerName:    settings.ServerName(),
		ContentMode:   mode,
		Addr:          *addr,
		ProjectsDir:   *projectsDir,
		MediaDir:      media,
		ModelsDir:     mediapkg.ResolveModelsDir(*modelsDir, os.Getenv, mediapkg.UserConfigDir(os.Getenv)),
		DBPath:        *dbPath,
		Token:         tok,
		CORSOrigins:   corsOrigins,
		WebDir:        web,
	})
	if err != nil {
		fmt.Fprintf(stderr, "fatal: %v\n", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	fmt.Fprintf(stdout, "veritmo serve listening on http://%s (projects: %s)\n", *addr, *projectsDir)
	if o, n := srv.CORSCounts(); o+n == 0 {
		fmt.Fprintln(stdout, "cors: no browser origin is allowed besides the server's own (see: veritmo cors add)")
	} else {
		fmt.Fprintf(stdout, "cors: %d origin(s) and %d network(s) allowed from a browser\n", o, n)
	}
	if err := srv.Run(ctx); err != nil {
		fmt.Fprintf(stderr, "fatal: %v\n", err)
		return 1
	}
	return 0
}

// parseLogLevel turns the --log-level text into a slog level.
func parseLogLevel(s string) (slog.Level, error) {
	var l slog.Level
	if err := l.UnmarshalText([]byte(s)); err != nil {
		return 0, fmt.Errorf("--log-level %q is not one of debug, info, warn, error", s)
	}
	return l, nil
}
