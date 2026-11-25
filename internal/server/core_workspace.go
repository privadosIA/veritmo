package server

// core_workspace.go: the names under which the families still in package server use the workspace (projects and runs,
// internal/server/workspace). They are aliases and one-line delegates, so a family reads `s.projectDir(id)` and `Run`
// as before; the code behind them is in the workspace package.

import (
	"io"
	"net/http"

	"veritmo/internal/server/workspace"
)

type (
	// Run is a run of a project, as the workspace family keeps it.
	Run          = workspace.Run
	binaryStatus = workspace.BinaryStatus
	assetEntry   = workspace.AssetEntry
)

// Run states, kinds of managed project and their reserved prefixes.
const (
	stateQueued      = workspace.StateQueued
	stateRunning     = workspace.StateRunning
	stateSucceeded   = workspace.StateSucceeded
	stateFailed      = workspace.StateFailed
	stateCancelled   = workspace.StateCancelled
	stateInterrupted = workspace.StateInterrupted
)

var (
	isManagedProject = workspace.IsManagedProject
	listAssets       = workspace.ListAssets
)

func (s *Server) projectDir(id string) string { return s.workspace.ProjectDir(id) }
func (s *Server) sourcesDir(id string) string { return s.workspace.SourcesDir(id) }
func (s *Server) dropsAvailability(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		next(w, r)
		s.workspace.DropAvailability()
		s.music.BumpEngines()
	}
}

// WriteProjectPackage writes the package of the project in projectDir (its scenes.yaml, sources/, a README and the
// recipe) to w; `veritmo export` uses it.
func WriteProjectPackage(w io.Writer, projectDir, assetsRoot, lang string) error {
	return workspace.WriteProjectPackage(catalog, w, projectDir, assetsRoot, lang)
}
