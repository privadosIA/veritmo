package motionref

// learn.go: the work of a learning from a reference (spec motion-learning). It draws one style from the analysis, asks
// the assistant for a few ideas of parts, compares every proposal with what exists and keeps only what is different: the
// style through the style store, each part as a draft the server starts. Every dependency is a seam, so the tests give fakes.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"veritmo/internal/music/musicmotion"
)

// MaxPartIdeas is how many ideas of parts a learning asks the assistant for.
const MaxPartIdeas = 3

// LearnEngine does the work of a learning.
type LearnEngine struct {
	Store   *LearningStore
	Presets *PresetStore
	Drafts  *musicmotion.DraftStore
	Cat     musicmotion.Catalog
	// Writer is the assistant (nil when there is none): it chooses the parts of the style and gives the ideas of parts.
	Writer musicmotion.Writer
	// PartsUnavailable says why drafts of parts cannot be made now ("" when they can): no assistant, no browser...
	PartsUnavailable string
	// WithPictures says that a key frame of the reference can inspire a draft (a vision model exists).
	WithPictures bool
	// StartDraft makes a draft of a part from a sentence (and, when frame is not empty, a key frame of the reference) and
	// starts it in the background; the draft is marked as learned by the learning. It returns the id of the draft.
	StartDraft func(ctx context.Context, learningID, request, frame string) (string, error)
}

// Run learns from a reference and saves the learning (which Create left running). Anything that cannot be done is said
// in Skipped; the learning is failed only when it cannot be saved or the context ends.
func (e *LearnEngine) Run(ctx context.Context, id string, ref Reference) {
	l, err := e.Store.Get(id)
	if err != nil {
		return
	}
	st, _ := musicmotion.ParseStrictness(l.Strictness)
	e.learnStyle(ctx, &l, ref, st)
	e.learnParts(ctx, &l, ref, st)
	now := time.Now().UTC()
	l.FinishedAt = &now
	if err := ctx.Err(); err != nil {
		l.State, l.Reason = LearningFailed, "the learning was stopped: "+err.Error()
	} else {
		l.State = LearningReady
	}
	_ = e.Store.Save(l)
}

func (e *LearnEngine) learnStyle(ctx context.Context, l *Learning, ref Reference, st musicmotion.Strictness) {
	p, err := FromReference(ctx, e.Writer, e.Cat, ref)
	if err != nil {
		l.Skipped = append(l.Skipped, Skipped{What: "style", Reason: err.Error()})
		return
	}
	p.Origin = musicmotion.OriginLearned
	near, found := musicmotion.ClosestStyle(p, e.Presets.List())
	if found && musicmotion.TooSimilar(near, st) {
		l.Discarded = append(l.Discarded, Discarded{Kind: "style", Name: p.Name, SimilarTo: near,
			Reason: fmt.Sprintf("too similar to the style %q (distance %.2f, below %.2f for %s): %s", near.Name, near.Distance, st.Threshold(), st, near.Reason)})
		return
	}
	kept, err := e.Presets.Add(p)
	if err != nil {
		l.Skipped = append(l.Skipped, Skipped{What: "style", Reason: "the style could not be saved: " + err.Error()})
		return
	}
	item := Learned{ID: kept.ID, Name: kept.Name, Novel: true}
	if found {
		item.SimilarTo = &near
	}
	l.Styles = append(l.Styles, item)
}

// partIdea is what the assistant proposes: a name and what the part shows and how it moves.
type partIdea struct {
	Name string `json:"name"`
	Idea string `json:"idea"`
}

const ideasSystem = `You propose new parts for a library of animated parts that follow a song. A part is a small animation drawn on the beat (a counter, a ring that spreads, a bar that fills...). You are given the look of a reference video and the parts that already exist.
Answer with ONE JSON object and nothing else: {"ideas": [ {"name": "lower-case-with-dashes (2 to 32 characters)", "idea": "one sentence: what it shows and how it moves on the beat"} ]} with at most %d ideas.
Each idea must be DIFFERENT from the existing parts (another shape, another movement), take its inspiration from the reference (the roles of its colours, its shapes and its kind of movement), and need no text from a song. Do not propose people or faces.`

func (e *LearnEngine) ideasUser(ref Reference) string {
	var b strings.Builder
	b.WriteString("Reference: " + ref.Title + "\n")
	if a := ref.Analysis; a != nil {
		if strings.TrimSpace(a.Description) != "" {
			b.WriteString("Its look: " + strings.TrimSpace(a.Description) + "\n")
		}
		fmt.Fprintf(&b, "Cuts: %d, mean shot %.1f s", a.Cuts, a.MeanShot)
		if a.Tempo != nil {
			fmt.Fprintf(&b, ", tempo %.0f bpm", *a.Tempo)
		}
		b.WriteString("\n")
	}
	b.WriteString("\nExisting parts:\n")
	for _, p := range e.Cat.List() {
		if !p.Manual {
			fmt.Fprintf(&b, "- %s: %s\n", p.Name, p.Description)
		}
	}
	for _, d := range e.Drafts.List() {
		fmt.Fprintf(&b, "- (draft) %s: %s\n", d.Name, d.Request)
	}
	return b.String()
}

// askIdeas asks the assistant for ideas; a refused answer is asked again with the reason, like the other answers of the assistant.
func (e *LearnEngine) askIdeas(ctx context.Context, ref Reference) ([]partIdea, error) {
	problem := ""
	user := e.ideasUser(ref)
	for i := 0; i < musicmotion.Attempts; i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		q := user
		if problem != "" {
			q += "\nYour previous answer was refused: " + problem + "\nAnswer again with the JSON object fixed.\n"
		}
		answer, err := e.Writer.Write(ctx, fmt.Sprintf(ideasSystem, MaxPartIdeas), q, 800)
		if err != nil {
			return nil, fmt.Errorf("the assistant did not answer: %w", err)
		}
		ideas, err := parseIdeas(answer)
		if err == nil {
			return ideas, nil
		}
		problem = err.Error()
	}
	return nil, fmt.Errorf("no valid ideas in %d answers; the last was refused: %s", musicmotion.Attempts, problem)
}

func parseIdeas(answer string) ([]partIdea, error) {
	start, end := strings.Index(answer, "{"), strings.LastIndex(answer, "}")
	if start < 0 || end <= start {
		return nil, errors.New("the answer holds no JSON object")
	}
	var got struct {
		Ideas []partIdea `json:"ideas"`
	}
	if err := json.Unmarshal([]byte(answer[start:end+1]), &got); err != nil {
		return nil, fmt.Errorf("the answer is not a valid JSON object: %w", err)
	}
	var out []partIdea
	for _, i := range got.Ideas {
		i.Name, i.Idea = strings.TrimSpace(i.Name), strings.TrimSpace(i.Idea)
		if i.Name == "" || i.Idea == "" {
			continue
		}
		if r := []rune(i.Idea); len(r) > musicmotion.MaxDraftRequest {
			i.Idea = string(r[:musicmotion.MaxDraftRequest])
		}
		out = append(out, i)
		if len(out) == MaxPartIdeas {
			break
		}
	}
	if len(out) == 0 {
		return nil, errors.New("the answer holds no idea with a name and a sentence")
	}
	return out, nil
}

// existingParts are the parts of the catalog and the drafts, which an idea has to differ from.
func (e *LearnEngine) existingParts() []musicmotion.PartLike {
	var out []musicmotion.PartLike
	for _, p := range e.Cat.List() {
		out = append(out, musicmotion.PartLike{Kind: "part", ID: p.Name, Name: p.Name, Description: p.Description})
	}
	for _, d := range e.Drafts.List() {
		name := d.Name
		if name == "" {
			name = d.Request
		}
		out = append(out, musicmotion.PartLike{Kind: "draft", ID: d.ID, Name: name, Description: d.Request})
	}
	return out
}

func (e *LearnEngine) learnParts(ctx context.Context, l *Learning, ref Reference, st musicmotion.Strictness) {
	if e.PartsUnavailable != "" {
		l.Skipped = append(l.Skipped, Skipped{What: "parts", Reason: e.PartsUnavailable})
		return
	}
	if e.Writer == nil || e.StartDraft == nil {
		l.Skipped = append(l.Skipped, Skipped{What: "parts", Reason: "there is no assistant to propose parts"})
		return
	}
	ideas, err := e.askIdeas(ctx, ref)
	if err != nil {
		l.Skipped = append(l.Skipped, Skipped{What: "parts", Reason: err.Error()})
		return
	}
	existing := e.existingParts()
	for i, idea := range ideas {
		cand := musicmotion.PartLike{Name: idea.Name, Description: idea.Idea}
		near, found := musicmotion.ClosestPart(cand, existing)
		if found && musicmotion.TooSimilar(near, st) {
			l.Discarded = append(l.Discarded, Discarded{Kind: "part", Name: idea.Name, Idea: idea.Idea, SimilarTo: near,
				Reason: fmt.Sprintf("too similar to the %s %q (distance %.2f, below %.2f for %s): %s", near.Kind, near.Name, near.Distance, st.Threshold(), st, near.Reason)})
			continue
		}
		frame := ""
		if e.WithPictures && ref.Analysis != nil && len(ref.Analysis.Frames) > 0 {
			frame = ref.Analysis.Frames[i*len(ref.Analysis.Frames)/len(ideas)]
		}
		id, err := e.StartDraft(ctx, l.ID, idea.Idea, frame)
		if err != nil {
			l.Skipped = append(l.Skipped, Skipped{What: "part " + idea.Name, Reason: err.Error()})
			continue
		}
		item := Learned{ID: id, Name: idea.Name, Novel: true}
		if found {
			item.SimilarTo = &near
		}
		l.PartDrafts = append(l.PartDrafts, item)
		existing = append(existing, musicmotion.PartLike{Kind: "draft", ID: id, Name: idea.Name, Description: idea.Idea})
	}
}
