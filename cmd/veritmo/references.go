package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"veritmo/internal/platform/legacyname"

	"veritmo/internal/music/motionref"
)

// runReferences implements `veritmo references add|list`: it puts references in the library of the running server
// (spec motion-references) through the same routes the dashboard uses, so a list of addresses is added the same way.
func runReferences(args []string, stdout, stderr io.Writer) int {
	usage := func() int {
		fmt.Fprintln(stderr, "Usage: veritmo references add [--server URL] [--token T] [--tag TAG]... <youtube-address>...\n       veritmo references list [--server URL] [--token T]")
		return 2
	}
	if len(args) == 0 || (args[0] != "add" && args[0] != "list") {
		return usage()
	}
	fs := flag.NewFlagSet("references "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	server := fs.String("server", "http://127.0.0.1:4666", "Address of the running server")
	token := fs.String("token", "", "Bearer token of the server (or VERITMO_TOKEN, or the token file the serve script keeps)")
	var tags stringList
	fs.Var(&tags, "tag", "Tag for the references added (repeatable)")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	tok := *token
	if tok == "" {
		tok = legacyname.Getenv("TOKEN")
	}
	if tok == "" {
		base := os.Getenv("XDG_CONFIG_HOME")
		if base == "" {
			home, _ := os.UserHomeDir()
			base = filepath.Join(home, ".config")
		}
		if b, err := os.ReadFile(filepath.Join(legacyname.Dir(base), "token")); err == nil {
			tok = strings.TrimSpace(string(b))
		}
	}
	base := strings.TrimRight(*server, "/") + "/api/music-motion/references"
	client := &http.Client{Timeout: 3 * time.Minute}
	do := func(method string, body any) (int, []byte, error) {
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req, err := http.NewRequest(method, base, rd)
		if err != nil {
			return 0, nil, err
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		resp, err := client.Do(req)
		if err != nil {
			return 0, nil, err
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, b, nil
	}

	if args[0] == "list" {
		code, b, err := do(http.MethodGet, nil)
		if err != nil || code != http.StatusOK {
			fmt.Fprintf(stderr, "fatal: cannot list the references (%s)\n", failure(code, b, err))
			return 1
		}
		var out struct {
			References []motionref.Reference `json:"references"`
		}
		_ = json.Unmarshal(b, &out)
		for _, r := range out.References {
			fmt.Fprintf(stdout, "%s  %-9s %s\n", r.ID, r.State, r.Title)
		}
		fmt.Fprintf(stdout, "%d reference(s)\n", len(out.References))
		return 0
	}

	if fs.NArg() == 0 {
		return usage()
	}
	failed := 0
	for _, url := range fs.Args() {
		code, b, err := do(http.MethodPost, map[string]any{"url": url, "tags": []string(tags)})
		if err != nil || code != http.StatusAccepted {
			fmt.Fprintf(stderr, "%s: not added (%s)\n", url, failure(code, b, err))
			failed++
			continue
		}
		var ref motionref.Reference
		_ = json.Unmarshal(b, &ref)
		fmt.Fprintf(stdout, "%s: added as %s (%s)\n", url, ref.ID, ref.Title)
	}
	if failed > 0 {
		return 1
	}
	return 0
}

// failure is the reason of a request that did not work, from the server's answer or the error.
func failure(code int, body []byte, err error) string {
	if err != nil {
		return err.Error()
	}
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && e.Error.Message != "" {
		return fmt.Sprintf("%d: %s", code, e.Error.Message)
	}
	return fmt.Sprintf("status %d", code)
}
