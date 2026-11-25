package generation

// names.go: the names under which this family uses what the workspace and the catalog define.

import (
	"veritmo/internal/server/modelcat"
	"veritmo/internal/server/workspace"
)

type (
	// Run is a run of a project, as the workspace family keeps it.
	Run          = workspace.Run
	gpuStatus    = workspace.GPUStatus
	binaryStatus = workspace.BinaryStatus
	managedInfo  = workspace.ManagedInfo
	managedInput = workspace.ManagedInput

	catalogEntry   = modelcat.Entry
	catalogLowVRAM = modelcat.LowVRAM
)

const (
	stateQueued      = workspace.StateQueued
	stateRunning     = workspace.StateRunning
	stateSucceeded   = workspace.StateSucceeded
	stateFailed      = workspace.StateFailed
	stateCancelled   = workspace.StateCancelled
	stateInterrupted = workspace.StateInterrupted

	kindGeneration = workspace.KindGeneration
	kindImageEdit  = workspace.KindImageEdit
	kindVideoEdit  = workspace.KindVideoEdit

	genPrefix   = workspace.GenPrefix
	editPrefix  = workspace.EditPrefix
	videoPrefix = workspace.VideoPrefix
	piecePrefix = workspace.PiecePrefix
)

var (
	isManagedProject = workspace.IsManagedProject
	newManagedID     = workspace.NewManagedID
	requestClient    = workspace.RequestClient
	clientShown      = workspace.ClientShown
)
