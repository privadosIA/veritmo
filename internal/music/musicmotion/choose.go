package musicmotion

// choose.go: the choice of a part by the model for a scene with motion_part: auto. The model gets the description of the
// scene, its narration, its length and the catalog (names, descriptions and parameters with their limits) and answers one
// JSON object with the part and its parameters; the answer is checked as a scene that names the part is, and asked
// again with the reason when it fails. See spec motion-graphics ("The model can choose the part").

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"veritmo/internal/project/config"
	"veritmo/internal/project/pipeline"
)

const chooseSystem = `You choose a part of a catalog of animated parts for one scene of a video, and fill in its parameters.
Answer with ONE JSON object and nothing else, with these keys:
  "part": the name of a part of the catalog below,
  "params": an object with the parameters of that part that you want to set (the others keep their defaults); lists are text with | between items,
  "text": optional, a short line (at most %d characters) to show under the scene, taken from the description or the narration.
Use only parts of the catalog and only their parameters, inside their limits. Make the scene say what the description says.`

func catalogForPrompt(cat Catalog) string {
	var b strings.Builder
	for _, p := range cat.List() {
		if p.Manual {
			continue
		}
		fmt.Fprintf(&b, "- %s: %s\n", p.Name, p.Description)
		for _, pr := range p.Params {
			line := fmt.Sprintf("    %s (%s, default %v", pr.Name, pr.Type, pr.Default)
			if pr.Min != nil {
				line += fmt.Sprintf(", from %v", *pr.Min)
			}
			if pr.Max != nil {
				line += fmt.Sprintf(" to %v", *pr.Max)
			}
			if pr.MaxLength > 0 {
				line += fmt.Sprintf(", at most %d characters", pr.MaxLength)
			}
			if len(pr.Options) > 0 {
				line += ", one of " + strings.Join(pr.Options, "|")
			}
			b.WriteString(line + ")")
			if pr.Description != "" {
				b.WriteString(": " + pr.Description)
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}

func chooseUser(cat Catalog, r pipeline.PartChoiceRequest, problem string) string {
	var b strings.Builder
	b.WriteString("Catalog:\n" + catalogForPrompt(cat))
	fmt.Fprintf(&b, "\nDescription of the scene: %s\n", strings.TrimSpace(r.Description))
	if strings.TrimSpace(r.Narration) != "" {
		fmt.Fprintf(&b, "Narration: %s\n", strings.TrimSpace(r.Narration))
	}
	fmt.Fprintf(&b, "Length: about %.0f seconds\n", r.Duration)
	if strings.TrimSpace(r.Concept) != "" {
		fmt.Fprintf(&b, "The visual idea of the whole video: %s\n", strings.TrimSpace(r.Concept))
	}
	if problem != "" {
		fmt.Fprintf(&b, "\nYour previous answer was refused: %s\nAnswer again with the JSON object fixed.\n", problem)
	}
	return b.String()
}

// parseChoice reads and checks an answer: the part is in the catalog and the parameters are its own and inside their limits.
func parseChoice(answer string, cat Catalog) (pipeline.PartChoice, error) {
	raw := extractJSON(answer)
	if raw == "" {
		return pipeline.PartChoice{}, errors.New("the answer holds no JSON object")
	}
	var c pipeline.PartChoice
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return pipeline.PartChoice{}, fmt.Errorf("the answer is not a valid JSON object: %w", err)
	}
	c.Part = strings.TrimSpace(c.Part)
	part, ok := cat.Get(c.Part)
	if !ok {
		return pipeline.PartChoice{}, fmt.Errorf("the part %q is not in the catalog", c.Part)
	}
	if part.Manual {
		return pipeline.PartChoice{}, fmt.Errorf("the part %q is not one to choose", c.Part)
	}
	if _, err := part.ResolveParams(c.Params); err != nil {
		return pipeline.PartChoice{}, err
	}
	c.Text = strings.TrimSpace(c.Text)
	if n := len([]rune(c.Text)); n > config.MaxMotionText {
		return pipeline.PartChoice{}, fmt.Errorf("the text has %d characters, the most is %d", n, config.MaxMotionText)
	}
	return c, nil
}

// ChoosePart asks the model for a part and its parameters; a refused answer is asked again with the reason, up to Attempts
// times, and when none is valid the error says why the last one was refused.
func ChoosePart(ctx context.Context, w Writer, cat Catalog, r pipeline.PartChoiceRequest) (pipeline.PartChoice, error) {
	if w == nil {
		return pipeline.PartChoice{}, errors.New("there is no model to ask")
	}
	problem := ""
	for i := 0; i < Attempts; i++ {
		if err := ctx.Err(); err != nil {
			return pipeline.PartChoice{}, err
		}
		answer, err := w.Write(ctx, fmt.Sprintf(chooseSystem, config.MaxMotionText), chooseUser(cat, r, problem), 1024)
		if err != nil {
			return pipeline.PartChoice{}, fmt.Errorf("the model did not answer: %w", err)
		}
		c, err := parseChoice(answer, cat)
		if err == nil {
			return c, nil
		}
		problem = err.Error()
	}
	return pipeline.PartChoice{}, fmt.Errorf("no valid choice in %d answers; the last was refused: %s", Attempts, problem)
}
