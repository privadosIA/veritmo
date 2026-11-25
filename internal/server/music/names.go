package music

// names.go: the names under which this family uses what the workspace defines.

import "veritmo/internal/server/workspace"

type (
	// Run is a run of a project, as the workspace family keeps it.
	Run         = workspace.Run
	launchError = workspace.LaunchError
)

const (
	piecePrefix     = workspace.PiecePrefix
	availabilityTTL = workspace.AvailabilityTTL
)

type runRequest = workspace.RunRequest

var (
	newManagedID  = workspace.NewManagedID
	requestClient = workspace.RequestClient
	sourceKind    = workspace.SourceKind
)
