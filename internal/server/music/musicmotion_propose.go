package music

// musicmotion_propose.go: the proposals of a piece (spec music-motion). A proposal asks the assistant for the design,
// the script or both, steered by the person's prompt. It is synchronous for `design` and `script`, and runs in the
// background (state in proposal.json, read in the view of the piece) for `all` and for `async: true`.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"veritmo/internal/assistant/llm"
	"veritmo/internal/music/musicmotion"
	"veritmo/internal/music/songmap"
	"veritmo/internal/platform/settings"
	"veritmo/internal/server/api"
	"veritmo/internal/server/assistant"
	"veritmo/internal/video/motion"
)

// The states of a proposal in the background.
const (
	ProposalWaiting = "waiting" // a run is queued or active and the assistant needs the memory
	ProposalWriting = "writing"
	ProposalDone    = "done"
	ProposalFailed  = "failed"
)

// How a proposal in the background waits for the runs to end (the Handler's proposalPoll and proposalMaxWait, which
// tests shorten so they do not wait).
const (
	defaultProposalPoll    = 2 * time.Second
	defaultProposalMaxWait = 15 * time.Minute
)

// ProposeRequest is the body of POST /api/music-motion/pieces/{id}/propose.
type ProposeRequest struct {
	// What is "design", "script" or "all" (the design and then the script).
	What     string `json:"what"`
	Provider string `json:"provider,omitempty"`
	// Prompt is the person's direction for the video (at most 2000 characters); empty uses the one kept with the piece.
	Prompt string `json:"prompt,omitempty"`
	// Reference is the id of a ready reference of the library to follow the rhythm of: it replaces the one kept with the piece, and
	// an empty text clears it; absent keeps the piece's.
	Reference *string `json:"reference,omitempty"`
	// Model is an installed model of the assistant's provider; empty uses the routing.
	Model string `json:"model,omitempty"`
	// Async answers 202 and works in the background; "all" always does.
	Async bool `json:"async,omitempty"`
	// Blocks is "auto" (the default: the script writer is offered the blocks and components that can be drawn) or "off".
	Blocks string `json:"blocks,omitempty"`
}

// ProposeAnswer is what a synchronous proposal answers.
type ProposeAnswer struct {
	What           string `json:"what"`
	Source         string `json:"source"` // assistant or fallback
	FallbackReason string `json:"fallback_reason,omitempty"`
	// FallbackPieces are the pieces whose part the fixed rule chose, in a script that fell back (what: script).
	FallbackPieces []string `json:"fallback_pieces,omitempty"`
	// DesignNote says what the program changed in the assistant's design (the accent's lightness, for its contrast); empty for nothing.
	DesignNote   string                    `json:"design_note,omitempty"`
	Model        string                    `json:"model,omitempty"`
	Provider     string                    `json:"provider,omitempty"` // the provider asked for on the request; empty when the routing's own wrote
	Design       *musicmotion.Stored       `json:"design,omitempty"`
	Script       *musicmotion.Script       `json:"script,omitempty"`
	Replacements []musicmotion.Replacement `json:"replacements,omitempty"`
	// Notes say what the proposal did not use of what it was given (a reference with no rhythm sheet).
	Notes []string `json:"notes,omitempty"`
}

// Proposal is the state of a proposal in the background, as the view of a piece shows it.
type Proposal struct {
	State                string     `json:"state"`
	What                 string     `json:"what"`
	Model                string     `json:"model,omitempty"`
	Provider             string     `json:"provider,omitempty"`
	StartedAt            time.Time  `json:"started_at"`
	FinishedAt           *time.Time `json:"finished_at,omitempty"`
	DesignSource         string     `json:"design_source,omitempty"`
	DesignFallbackReason string     `json:"design_fallback_reason,omitempty"`
	DesignNote           string     `json:"design_note,omitempty"`
	ScriptSource         string     `json:"script_source,omitempty"`
	ScriptFallbackReason string     `json:"script_fallback_reason,omitempty"`
	// ScriptFallbackPieces are the pieces of a fallback script (the ones whose part the fixed rule chose).
	ScriptFallbackPieces []string `json:"script_fallback_pieces,omitempty"`
	// Notes say what the proposal did not use of what it was given (a reference with no rhythm sheet).
	Notes []string `json:"notes,omitempty"`
	Error string   `json:"error,omitempty"`
}

// proposalJobs are the proposals in the background that are running, by piece, with what stops them.
type proposalJobs struct {
	mu   sync.Mutex
	jobs map[string]context.CancelFunc
}

func (p *proposalJobs) start(id string, cancel context.CancelFunc) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, busy := p.jobs[id]; busy {
		return false
	}
	if p.jobs == nil {
		p.jobs = map[string]context.CancelFunc{}
	}
	p.jobs[id] = cancel
	return true
}

func (p *proposalJobs) end(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.jobs, id)
}

func (p *proposalJobs) running(id string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.jobs[id]
	return ok
}

func (p *proposalJobs) cancel(id string) {
	p.mu.Lock()
	c := p.jobs[id]
	p.mu.Unlock()
	if c != nil {
		c()
	}
}

func (h *Handler) saveProposal(id string, p Proposal) {
	raw, _ := json.MarshalIndent(p, "", "  ")
	_ = h.place(id).WriteDoc(musicmotion.DocProposal, raw)
}

// proposalOf reads the last proposal of a piece. One that was running when the server stopped reads as failed.
func (h *Handler) proposalOf(id string) *Proposal {
	raw, ok := h.place(id).ReadDoc(musicmotion.DocProposal)
	if !ok {
		return nil
	}
	var p Proposal
	if json.Unmarshal(raw, &p) != nil {
		return nil
	}
	if (p.State == ProposalWaiting || p.State == ProposalWriting) && !h.proposals.running(id) {
		now := time.Now().UTC()
		p.State, p.FinishedAt, p.Error = ProposalFailed, &now, "the proposal was interrupted by a restart of the server"
	}
	return &p
}

// --- the writer ----------------------------------------------------------------------------------

// writerChoice is the assistant a proposal uses, or why there is none.
type writerChoice struct {
	w     musicmotion.Writer
	ctx   context.Context
	model string // the model that will write, or the one that was asked for
	why   string // why there is no writer
	busy  string // the run that holds the memory, when that is why
}

// mmChoose is the writer of the assistant for the proposals: the one of the motion-graphics task, with the provider
// and the model of the request. It has no writer (and says why) when no model is available or when a run is in
// progress and the model is a local one.
func (h *Handler) mmChoose(ctx context.Context, provider, model string) writerChoice {
	ctx = motion.WithModel(llm.WithProvider(ctx, provider), model)
	ch := writerChoice{ctx: ctx, model: model}
	if err := llm.Check(settings.TaskMotionGraphics, provider); err != nil {
		ch.why = "the assistant is not available: " + err.Error()
		return ch
	}
	if id, busy := h.ws.AnyBusy(); busy && api.NeedsLocalMemory(ctx, settings.TaskMotionGraphics) {
		ch.busy = id
		ch.why = fmt.Sprintf("run %s is queued or active and the model needs the memory it uses", id)
		return ch
	}
	newWriter := h.host.MotionWriter()
	if newWriter == nil {
		newWriter = motion.NewWriter
	}
	wr, info, err := newWriter(ctx)
	if err != nil {
		ch.why = "the assistant is not available: " + err.Error()
		return ch
	}
	ch.w = wr
	if info.Model != "" {
		ch.model = info.Model
	}
	return ch
}

// mmWriter is the writer of the assistant for the proposals; nil when there is none.
func (h *Handler) mmWriter(ctx context.Context, provider string) (musicmotion.Writer, context.Context) {
	ch := h.mmChoose(ctx, provider, "")
	return ch.w, ch.ctx
}

// checkModel refuses, before anything is done, a model that is not installed. Only a local Ollama can be asked what it
// has: for another provider the name goes through and a failure names it.
func (h *Handler) checkModel(ctx context.Context, provider, model string) error {
	if model == "" || !api.NeedsLocalMemory(llm.WithProvider(ctx, provider), settings.TaskMotionGraphics) {
		return nil
	}
	list := h.assistant.AssistModels
	if list == nil {
		list = assistant.DefaultAssistModels
	}
	_, models, err := list(ctx)
	if err != nil {
		return nil // Ollama does not answer: the writer will fail on its own terms and name the model
	}
	names := make([]string, 0, len(models))
	for _, m := range models {
		if m.Name == model {
			return nil
		}
		names = append(names, m.Name)
	}
	return fmt.Errorf("the model %q is not installed (installed: %s)", model, strings.Join(names, ", "))
}

// --- the steps -----------------------------------------------------------------------------------

type proposeFail struct {
	status int
	msg    string
}

func (e *proposeFail) Error() string { return e.msg }

func refusal(format string, a ...any) *proposeFail {
	return &proposeFail{status: http.StatusUnprocessableEntity, msg: fmt.Sprintf(format, a...)}
}

func internalFail(msg string) *proposeFail {
	return &proposeFail{status: http.StatusInternalServerError, msg: msg}
}

func (h *Handler) writeFail(w http.ResponseWriter, f *proposeFail) {
	if f.status == http.StatusUnprocessableEntity {
		api.Refuse(w, "%s", f.msg)
		return
	}
	api.WriteError(w, f.status, api.CodeInternal, f.msg)
}

// songText is what the assistant is told of the song: the sung lines and the section tags.
func songText(m songmap.Map) (string, []string) {
	lyrics := ""
	tags := make([]string, 0, len(m.Sections))
	for _, sec := range m.Sections {
		tags = append(tags, sec.Tag)
	}
	for _, l := range lineTexts(m) {
		lyrics += l + "\n"
	}
	return lyrics, tags
}

// designStep writes a new design for the piece and keeps it.
func (h *Handler) designStep(ch writerChoice, info *pieceInfo, m songmap.Map, prompt string, sheet *musicmotion.ReferenceSheet) (musicmotion.Stored, *proposeFail) {
	lyrics, tags := songText(m)
	st, err := musicmotion.ResolveAt(ch.ctx, ch.w, h.place(info.ID), musicmotion.Song{Style: info.Caption, Lyrics: lyrics, SectionTags: tags, Prompt: prompt, World: info.World, Device: info.Device, Reference: sheet, NoAssistant: ch.why}, true)
	if err != nil {
		return st, internalFail(err.Error())
	}
	return st, nil
}

// scriptStep writes a new script for the piece from the design and keeps it.
func (h *Handler) scriptStep(ch writerChoice, info *pieceInfo, m songmap.Map, st musicmotion.Stored, prompt string, sheet *musicmotion.ReferenceSheet, noBlocks bool) (musicmotion.Proposal, *proposeFail) {
	prop, err := musicmotion.ProposeScript(ch.ctx, ch.w, musicmotion.ScriptInput{Map: m, Design: st.Design, Level: info.Level, Cat: h.mmCatalog(), Prompt: prompt, Device: info.Device, Hook: info.Hook, Reference: sheet, NoAssistant: ch.why, NoBlocks: noBlocks})
	if err != nil {
		return prop, refusal("%v", err)
	}
	if info.Style == musicmotion.StyleIllustrated {
		musicmotion.AddStages(&prop.Script, h.mmLibrary(), st.Design)
	}
	if err := prop.Script.WithSources(h.pieceSources(info.ID)).ValidateAll(h.mmCatalog(), h.mmLibrary(), m, st.Design, info.Width, info.Height); err != nil {
		return prop, refusal("%v", err)
	}
	if err := musicmotion.SaveScriptAt(h.place(info.ID), prop.Script); err != nil {
		return prop, internalFail(err.Error())
	}
	info.ScriptEdited, info.ScriptSource = false, prop.Source
	_ = h.writePiece(info)
	return prop, nil
}

// --- the endpoint --------------------------------------------------------------------------------

// POST /api/music-motion/pieces/{id}/propose
func (h *Handler) handleMusicMotionPropose(w http.ResponseWriter, r *http.Request) {
	info, ok := h.pieceFromPath(w, r)
	if !ok || h.refuseIfRendering(w, info) {
		return
	}
	var req ProposeRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
		return
	}
	if req.What != "design" && req.What != "script" && req.What != "all" {
		api.Refuse(w, `"what" must be "design", "script" or "all", got %q`, req.What)
		return
	}
	if req.Blocks != "" && req.Blocks != "auto" && req.Blocks != "off" {
		api.Refuse(w, `"blocks" must be "auto" or "off", got %q`, req.Blocks)
		return
	}
	if n := len([]rune(req.Prompt)); n > musicmotion.MaxPrompt {
		api.Refuse(w, "the prompt has %d characters: at most %d", n, musicmotion.MaxPrompt)
		return
	}
	req.Provider = strings.TrimSpace(req.Provider)
	if req.Provider != "" { // a provider asked for is not silently replaced by the fixed rule: unknown, no consent... are refused
		if err := llm.Check(settings.TaskMotionGraphics, req.Provider); err != nil && api.WriteRoutingError(w, err) {
			return
		}
	}
	if req.Reference != nil && !h.checkReference(w, *req.Reference) {
		return
	}
	if err := h.checkModel(r.Context(), req.Provider, req.Model); err != nil {
		api.Refuse(w, "%v", err)
		return
	}
	m, ok := h.pieceMap(info.ID)
	if !ok {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, "the piece has lost its song map")
		return
	}
	if p := strings.TrimSpace(req.Prompt); p != "" && p != info.Prompt {
		info.Prompt = p
		_ = h.writePiece(info)
	}
	if req.Reference != nil {
		if ref := strings.TrimSpace(*req.Reference); ref != info.Reference {
			info.Reference = ref
			_ = h.writePiece(info)
		}
	}
	sheet, notes := h.referenceSheet(info)
	prompt := info.Prompt
	if req.What == "all" || req.Async {
		h.startProposal(w, info, m, req, prompt, sheet, notes)
		return
	}
	h.proposeNow(w, r, info, m, req, prompt, sheet, notes)
}

// proposeNow is the synchronous proposal of the design or of the script: with a run in progress it falls back at once.
func (h *Handler) proposeNow(w http.ResponseWriter, r *http.Request, info *pieceInfo, m songmap.Map, req ProposeRequest, prompt string, sheet *musicmotion.ReferenceSheet, notes []string) {
	ch := h.mmChoose(r.Context(), req.Provider, req.Model)
	if req.What == "design" {
		st, fail := h.designStep(ch, info, m, prompt, sheet)
		if fail != nil {
			h.writeFail(w, fail)
			return
		}
		api.WriteJSON(w, http.StatusOK, ProposeAnswer{What: "design", Source: st.Source, FallbackReason: st.FallbackReason, DesignNote: st.Note, Model: ch.model, Provider: req.Provider, Design: &st, Notes: notes})
		return
	}
	st, ok := musicmotion.LoadDesignAt(h.place(info.ID))
	if !ok {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, "the piece has lost its design")
		return
	}
	prop, fail := h.scriptStep(ch, info, m, st, prompt, sheet, req.Blocks == "off")
	if fail != nil {
		h.writeFail(w, fail)
		return
	}
	api.WriteJSON(w, http.StatusOK, ProposeAnswer{What: "script", Source: prop.Source, FallbackReason: prop.FallbackReason, FallbackPieces: prop.FallbackPieces, Model: ch.model, Provider: req.Provider, Script: &prop.Script, Replacements: prop.Replacements, Notes: append(notes, prop.Warnings...)})
}

// startProposal begins a proposal in the background and answers 202 with its state.
func (h *Handler) startProposal(w http.ResponseWriter, info *pieceInfo, m songmap.Map, req ProposeRequest, prompt string, sheet *musicmotion.ReferenceSheet, notes []string) {
	ctx, cancel := context.WithCancel(context.Background())
	if !h.proposals.start(info.ID, cancel) {
		cancel()
		api.WriteError(w, http.StatusConflict, api.CodeConflict, "a proposal of this piece is still being written: wait for it")
		return
	}
	p := Proposal{State: ProposalWriting, What: req.What, Model: req.Model, Provider: req.Provider, StartedAt: time.Now().UTC(), Notes: notes}
	h.saveProposal(info.ID, p)
	go h.runProposal(ctx, info, m, req, prompt, sheet, p)
	api.WriteJSON(w, http.StatusAccepted, p)
}

// runProposal does the work of a proposal in the background and keeps its state.
func (h *Handler) runProposal(ctx context.Context, info *pieceInfo, m songmap.Map, req ProposeRequest, prompt string, sheet *musicmotion.ReferenceSheet, p Proposal) {
	defer h.proposals.end(info.ID)
	defer func() {
		if rec := recover(); rec != nil {
			p.State, p.Error = ProposalFailed, fmt.Sprintf("the proposal broke: %v", rec)
			now := time.Now().UTC()
			p.FinishedAt = &now
			h.saveProposal(info.ID, p)
		}
	}()
	finish := func(fail *proposeFail) {
		now := time.Now().UTC()
		p.FinishedAt = &now
		if fail != nil {
			p.State, p.Error = ProposalFailed, fail.msg
		} else {
			p.State = ProposalDone
		}
		h.saveProposal(info.ID, p)
	}
	ch := h.waitForWriter(ctx, req, &p, info.ID)
	if ctx.Err() != nil {
		finish(internalFail("the proposal was cancelled"))
		return
	}
	if ch.model != "" {
		p.Model = ch.model
	}
	p.State = ProposalWriting
	h.saveProposal(info.ID, p)
	if req.What == "design" || req.What == "all" {
		st, fail := h.designStep(ch, info, m, prompt, sheet)
		if fail != nil {
			finish(fail)
			return
		}
		p.DesignSource, p.DesignFallbackReason, p.DesignNote = st.Source, st.FallbackReason, st.Note
		h.saveProposal(info.ID, p)
	}
	if req.What == "script" || req.What == "all" {
		st, ok := musicmotion.LoadDesignAt(h.place(info.ID))
		if !ok {
			finish(internalFail("the piece has lost its design"))
			return
		}
		prop, fail := h.scriptStep(ch, info, m, st, prompt, sheet, req.Blocks == "off")
		if fail != nil {
			if req.What == "all" {
				fail.msg = "the design was kept but the script failed: " + fail.msg
			}
			finish(fail)
			return
		}
		p.ScriptSource, p.ScriptFallbackReason, p.ScriptFallbackPieces = prop.Source, prop.FallbackReason, prop.FallbackPieces
	}
	finish(nil)
}

// waitForWriter chooses the writer, waiting (state waiting) while a run holds the memory, for up to the Handler's proposalMaxWait.
func (h *Handler) waitForWriter(ctx context.Context, req ProposeRequest, p *Proposal, id string) writerChoice {
	poll, maxWait := h.proposalPoll, h.proposalMaxWait
	if poll == 0 {
		poll = defaultProposalPoll
	}
	if maxWait == 0 {
		maxWait = defaultProposalMaxWait
	}
	deadline := time.Now().Add(maxWait)
	for {
		ch := h.mmChoose(ctx, req.Provider, req.Model)
		if ch.busy == "" || ctx.Err() != nil {
			return ch
		}
		if !time.Now().Before(deadline) {
			ch.why = fmt.Sprintf("waited %s for the runs to end and run %s still held the memory", maxWait, ch.busy)
			return ch
		}
		if p.State != ProposalWaiting {
			p.State = ProposalWaiting
			h.saveProposal(id, *p)
		}
		select {
		case <-ctx.Done():
			return ch
		case <-time.After(poll):
		}
	}
}

// refuseIfProposing stops an edit or a render while a proposal in the background writes the piece.
func (h *Handler) refuseIfProposing(w http.ResponseWriter, info *pieceInfo) bool {
	if h.proposals.running(info.ID) {
		api.WriteError(w, http.StatusConflict, api.CodeConflict, "a proposal or a batch of scenes is being written for this piece: wait for it")
		return true
	}
	return false
}
