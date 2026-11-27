package server

// core_contentmode.go decides which projects the server shows. A project carries an
// `adult` label in its scenes.yaml (true: for adults; false: looked at and fine
// for everyone; absent: not looked at). The server runs in one of two modes, fixed
// when it starts and never changed by a request, a setting or an environment
// variable: censored (the default) hides the projects with adult: true, and
// strict also hides the ones that were not looked at. A hidden project does not
// exist for the API: every route that names it, directly or through one of its
// runs or reviews, answers the 404 of an unknown one, and every list leaves it out.
//
// The gate is applied in Server.handle, so a route cannot forget it; the test
// TestEveryRouteIsClassified fails for a pattern that is not classified below.

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"veritmo/internal/platform/edition"
	"veritmo/internal/server/api"
)

// ContentMode is how much of the projects the server shows.
type ContentMode string

const (
	// ContentCensored hides the projects marked for adults. It is the default.
	ContentCensored ContentMode = "censored"
	// ContentStrict also hides the projects that were not looked at.
	ContentStrict ContentMode = "strict"
	// ContentAll hides nothing. Only a build that allows it (see internal/platform/edition) can start in it.
	ContentAll ContentMode = "all"
)

// errContentModeNotAllowed is what New returns for a mode the build cannot run.
var errContentModeNotAllowed = errors.New("server: that content mode is not available in this build")

// normalizeContentMode applies the default and refuses what this build cannot run.
func normalizeContentMode(m ContentMode) (ContentMode, error) {
	switch m {
	case "", ContentCensored:
		return ContentCensored, nil
	case ContentStrict:
		return ContentStrict, nil
	case ContentAll:
		if edition.ContentAll() {
			return ContentAll, nil
		}
	}
	return "", errContentModeNotAllowed
}

// labelState is what a project's scenes.yaml says about its adult label.
type labelState int

const (
	labelMissing labelState = iota // there is no such project
	labelUnset                     // not looked at
	labelFine                      // adult: false
	labelAdult                     // adult: true
)

// projectLabel reads the adult label of a project from its row (a column derived from its definition each time it is saved).
func (s *Server) projectLabel(id string) labelState {
	adult, exists := s.workspace.ProjectAdult(id)
	switch {
	case !exists:
		return labelMissing
	case adult == nil:
		return labelUnset
	case *adult:
		return labelAdult
	}
	return labelFine
}

// projectVisible says whether the server shows the project. A project that does not
// exist is "visible": asking for it is the ordinary 404.
func (s *Server) projectVisible(id string) bool {
	return s.visibleAs(s.projectLabel(id), true)
}

// runProjectVisible is the rule for the project of a run or a review: when the project
// no longer exists its history stays (it holds numbers, not files), except in the
// strict mode, which shows only what was explicitly marked as fine.
func (s *Server) runProjectVisible(project string) bool {
	return s.visibleAs(s.projectLabel(project), false)
}

func (s *Server) visibleAs(l labelState, missingVisible bool) bool {
	switch s.content {
	case ContentAll:
		return true
	case ContentStrict:
		if l == labelMissing {
			return missingVisible
		}
		return l == labelFine
	default:
		return l != labelAdult
	}
}

// refuseAdultConfig writes the 422 of a request that would make a project for adults, and
// reports whether it did. It applies to every mode that hides them.
func (s *Server) refuseAdultConfig(w http.ResponseWriter, adult *bool) bool {
	if s.content == ContentAll || adult == nil || !*adult {
		return false
	}
	api.WriteError(w, http.StatusUnprocessableEntity, api.CodeContentNotAllowed, "this server does not keep content for adults")
	return true
}

// --- the gate ------------------------------------------------------------------------------------

// Route classes. A pattern's class says how the gate finds the project it concerns.
const (
	classProject = "project" // /api/projects/{id}/...
	classRun     = "run"     // /api/runs/{id}...
	classReview  = "review"  // /api/reviews/{rid}...
	classList    = "list"    // lists, totals and creations: filtered or checked by their own handler
	classOpen    = "open"    // nothing about a project
)

// listRoutes are the routes that cross projects: their handlers filter.
var listRoutes = map[string]bool{
	"GET /api/generation/options":                                     true,
	"POST /api/generations":                                           true,
	"GET /api/generations":                                            true,
	"GET /api/image-edits":                                            true,
	"GET /api/generations/{id}":                                       true,
	"PATCH /api/generations/{id}":                                     true,
	"DELETE /api/generations/{id}":                                    true,
	"GET /api/music-motion/pieces/{id}":                               true,
	"PUT /api/music-motion/pieces/{id}/design":                        true,
	"PUT /api/music-motion/pieces/{id}/script":                        true,
	"POST /api/music-motion/pieces/{id}/preview":                      true,
	"GET /api/music-motion/pieces/{id}/frame":                         true,
	"POST /api/music-motion/pieces/{id}/propose":                      true,
	"POST /api/music-motion/pieces/{id}/review":                       true,
	"POST /api/music-motion/pieces/{id}/script/{piece}/blender":       true,
	"POST /api/music-motion/pieces/{id}/script/{piece}/blender/frame": true,
	"GET /api/music-motion/pieces/{id}/models":                        true,
	"PUT /api/music-motion/pieces/{id}/models/{name}":                 true,
	"DELETE /api/music-motion/pieces/{id}/models/{name}":              true,
	"POST /api/music-motion/pieces/{id}/models":                       true,
	"GET /api/music-motion/pieces/{id}/backgrounds":                   true,
	"POST /api/music-motion/pieces/{id}/backgrounds":                  true,
	"PUT /api/music-motion/pieces/{id}/backgrounds/{name}":            true,
	"DELETE /api/music-motion/pieces/{id}/backgrounds/{name}":         true,
	"GET /api/music-motion/pieces/{id}/backgrounds/{name}/file":       true,
	"GET /api/music-motion/pieces/{id}/backgrounds/{name}/frame":      true,
	"GET /api/music-motion/pieces/{id}/backgrounds/{name}/thumbs":     true,
	"POST /api/music-motion/pieces/{id}/video/split":                  true,
	"POST /api/music-motion/pieces/{id}/video/join":                   true,
	"POST /api/music-motion/pieces/{id}/scenes":                       true,
	"DELETE /api/music-motion/pieces/{id}/scenes":                     true,
	"POST /api/music-motion/pieces/{id}/scenes/approve":               true,
	"POST /api/music-motion/pieces/{id}/render":                       true,
	"POST /api/music-motion/pieces/{id}/transcribe":                   true,
	"PATCH /api/music-motion/pieces/{id}/words":                       true,
	"DELETE /api/music-motion/pieces/{id}":                            true,
	"PATCH /api/music-motion/pieces/{id}":                             true,
	"GET /api/music-motion/part-drafts/{id}":                          true,
	"PUT /api/music-motion/part-drafts/{id}":                          true,
	"DELETE /api/music-motion/part-drafts/{id}":                       true,
	"POST /api/music-motion/part-drafts/{id}/retry":                   true,
	"POST /api/music-motion/part-drafts/{id}/approve":                 true,
	"GET /api/music-motion/part-drafts/{id}/preview/{name}":           true,
	"GET /api/music-motion/presets/{id}":                              true,
	"PUT /api/music-motion/presets/{id}":                              true,
	"DELETE /api/music-motion/presets/{id}":                           true,
	"GET /api/music-motion/references/{id}":                           true,
	"PATCH /api/music-motion/references/{id}":                         true,
	"DELETE /api/music-motion/references/{id}":                        true,
	"GET /api/music-motion/references/{id}/frames/{name}":             true,
	"POST /api/music-motion/references/{id}/learn":                    true,
	"POST /api/music-motion/references/{id}/breakdown":                true,
	"GET /api/music-motion/learnings/{id}":                            true,
	"DELETE /api/music-motion/learnings/{id}":                         true,
	"POST /api/image-edits":                                           true,
	"GET /api/image-edits/{id}":                                       true,
	"PATCH /api/image-edits/{id}":                                     true,
	"DELETE /api/image-edits/{id}":                                    true,
	"GET /api/video-edits/{id}":                                       true,
	"PATCH /api/video-edits/{id}":                                     true,
	"DELETE /api/video-edits/{id}":                                    true,
	"GET /api/projects":                                               true,
	"POST /api/projects":                                              true,
	"GET /api/runs":                                                   true,
	"POST /api/runs/delete":                                           true,
	"GET /api/stats":                                                  true,
	"GET /api/stats/phase-duration":                                   true,
	"POST /api/estimate":                                              true,
	"GET /api/clips":                                                  true,
	"GET /api/storage":                                                true,
	"GET /api/library":                                                true,
	"PUT /api/library/meta":                                           true,
	"GET /api/library/songs":                                          true,
	"GET /api/library/songs/{key}":                                    true,
	"PUT /api/library/songs/{key}/rating":                             true,
	"POST /api/library/rescan":                                        true,
	"GET /api/library/playlists":                                      true,
	"POST /api/library/playlists":                                     true,
	"GET /api/library/playlists/{id}":                                 true,
	"PUT /api/library/playlists/{id}":                                 true,
	"DELETE /api/library/playlists/{id}":                              true,
	"POST /api/import/preview":                                        true,
	"POST /api/import/confirm":                                        true,
	"POST /api/import/package/preview":                                true,
	"POST /api/import/package/confirm":                                true,
	"POST /api/reviews/delete":                                        true,
}

// routeClass classifies a route pattern ("METHOD /path").
func routeClass(pattern string) string {
	path := pattern
	if i := strings.IndexByte(pattern, ' '); i >= 0 {
		path = pattern[i+1:]
	}
	switch {
	case strings.HasPrefix(path, "/api/projects/{id}"):
		return classProject
	case strings.HasPrefix(path, "/api/runs/{id}"):
		return classRun
	case strings.HasPrefix(path, "/api/reviews/{rid}"):
		return classReview
	case strings.HasPrefix(path, "/api/models3d/generations"):
		return classList // a generation is a hidden project: its handlers check that the content mode shows it
	case strings.HasPrefix(path, "/api/models3d/"):
		return classOpen // the library of 3D models is not about a project
	case listRoutes[pattern]:
		return classList
	case strings.HasPrefix(path, "/api/providers/{id}"), strings.HasPrefix(path, "/api/downloads/{id}"), strings.HasPrefix(path, "/api/music/installs/{id}"), strings.HasPrefix(path, "/api/motions/{id}"), strings.HasPrefix(path, "/api/prompts/{id}"):
		return classOpen // providers, downloads, the motions of the library and the prompt presets are not about a project
	case strings.Contains(path, "{id}") || strings.Contains(path, "{rid}"):
		// a project, run or review value that no class above knows: unclassified
		return ""
	}
	return classOpen
}

// gate wraps a handler so that a hidden project answers like an unknown one.
func (s *Server) gate(pattern string, h http.HandlerFunc) http.HandlerFunc {
	class := routeClass(pattern)
	if class == classProject || class == classRun || class == classReview {
		return func(w http.ResponseWriter, r *http.Request) {
			if s.content != ContentAll && s.hiddenByRoute(class, r) {
				s.writeUnknown(w, class, r)
				return
			}
			h(w, r)
		}
	}
	return h
}

func (s *Server) hiddenByRoute(class string, r *http.Request) bool {
	switch class {
	case classProject:
		return !s.projectVisible(r.PathValue("id"))
	case classRun:
		rec, err := s.workspace.RunRecord(r.PathValue("id"))
		return err == nil && !s.runProjectVisible(rec.Project)
	case classReview:
		project, ok := s.workspace.ReviewProject(r.PathValue("rid"))
		return ok && !s.runProjectVisible(project)
	}
	return false
}

// writeUnknown writes the 404 that the same request gets when the thing does not exist.
func (s *Server) writeUnknown(w http.ResponseWriter, class string, r *http.Request) {
	switch class {
	case classProject:
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("project %q not found", r.PathValue("id")))
	case classRun:
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("run %s not found", r.PathValue("id")))
	default:
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("review %q not found", r.PathValue("rid")))
	}
}

// onlyVisibleRunIDs leaves out the ids of runs of hidden projects; an unknown id stays (it is ignored later).
func (s *Server) onlyVisibleRunIDs(ids []string) []string {
	if s.content == ContentAll {
		return ids
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if rec, err := s.workspace.RunRecord(id); err == nil && !s.runProjectVisible(rec.Project) {
			continue
		}
		out = append(out, id)
	}
	return out
}
