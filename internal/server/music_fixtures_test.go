package server

// music_fixtures_test.go: the names under which the HTTP-level tests of the server use what the music family
// exports.

import "veritmo/internal/server/music"

type (
	pieceCatalog      = music.PieceCatalog
	catalogPart       = music.CatalogPart
	catalogParam      = music.CatalogParam
	catalogCharacter  = music.CatalogCharacter
	catalogBackground = music.CatalogBackground
	pieceRequest      = music.PieceRequest
	draftRequest      = music.DraftRequest
	draftPut          = music.DraftPut
	learningView      = music.LearningView
	learnedView       = music.LearnedView
	learnRequest      = music.LearnRequest
	pieceView         = music.PieceView
	pieceSource       = music.PieceSource
	proposeRequest    = music.ProposeRequest
	proposeAnswer     = music.ProposeAnswer
	musicProposal     = music.Proposal
	renderAnswer      = music.RenderAnswer
)
