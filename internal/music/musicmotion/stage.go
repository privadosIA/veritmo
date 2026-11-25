package musicmotion

// stage.go: the stage of an illustrated piece. A piece may have a background and characters from the library, each
// with a position, a pose, an expression, a size and an action from a fixed list; the actions are timed from the beat
// clock, so a character moves on the beat. See specs/motion-characters ("A script places characters on a stage").

import (
	"fmt"
	"regexp"
	"strings"
)

// Actions a character can do, timed from the beat clock.
const (
	ActionNone   = "none"
	ActionEnter  = "enter"  // slides in from the side during the first beat
	ActionExit   = "exit"   // slides out during the last beat of the piece
	ActionBounce = "bounce" // hops once per beat
	ActionLook   = "look"   // sways from side to side with the beat
	ActionWave   = "wave"   // sways quickly, twice per beat (with the pose wave)
	ActionSlide  = "slide"  // moves across a tenth of the frame and back over four beats
)

// Actions lists the actions.
var Actions = []string{ActionNone, ActionEnter, ActionExit, ActionBounce, ActionLook, ActionWave, ActionSlide}

// The styles of a piece.
const (
	StyleTypographic = "typographic"
	StyleIllustrated = "illustrated"
)

// Styles lists the styles.
var Styles = []string{StyleTypographic, StyleIllustrated}

// FrameRates are the frame rates a piece may have.
var FrameRates = []int{24, 30, 60}

// DefaultFPS is the frame rate of a style when none is asked: 24 for the illustrated one, 30 for the other.
func DefaultFPS(style string) int {
	if style == StyleIllustrated {
		return 24
	}
	return 30
}

// ResolveFPS is the frame rate to use: the one asked for, or the style's default when it is 0. It refuses a rate
// that is not one of FrameRates.
func ResolveFPS(style string, fps int) (int, error) {
	if fps == 0 {
		return DefaultFPS(style), nil
	}
	for _, r := range FrameRates {
		if r == fps {
			return fps, nil
		}
	}
	return 0, fmt.Errorf("the frame rate must be 24, 30 or 60, got %d", fps)
}

// StageCharacter places a character of the library. X and Y are where its anchor goes, as a percentage of the frame's
// width and height (Y counts from the top, so a character standing on the ground at 78% has Y 78); Size is its
// height as a percentage of the frame's height.
type StageCharacter struct {
	Character  string  `json:"character"`
	Pose       string  `json:"pose,omitempty"`
	Expression string  `json:"expression,omitempty"`
	Action     string  `json:"action,omitempty"`
	X          float64 `json:"x"`
	Y          float64 `json:"y"`
	Size       float64 `json:"size"`
}

// Stage is a background and the characters on it.
type Stage struct {
	Background string           `json:"background,omitempty"`
	Characters []StageCharacter `json:"characters,omitempty"`
}

func isAction(a string) bool {
	if a == "" {
		return true
	}
	for _, x := range Actions {
		if x == a {
			return true
		}
	}
	return false
}

// slideReach is how far a sliding character goes from its place, as a share of the frame's width.
const slideReach = 0.10

// bounceScale is the most a bouncing character is drawn larger than its size.
const bounceScale = 1.08

// Validate checks a stage against the library and the frame (width and height in pixels), and names what is wrong:
// a background, character, pose, expression or action that is not there, and a character that would be drawn
// partly outside the frame (unless it enters or exits).
func (st Stage) Validate(lib Library, width, height int) error {
	if st.Background != "" {
		if _, ok := lib.Background(st.Background); !ok {
			return fmt.Errorf("the background %q is not in the library", st.Background)
		}
	}
	for i, c := range st.Characters {
		n := fmt.Sprintf("character #%d (%q)", i+1, c.Character)
		ch, ok := lib.Character(c.Character)
		if !ok {
			return fmt.Errorf("%s: the character %q is not in the library", n, c.Character)
		}
		if c.Pose != "" {
			if _, ok := ch.Poses[c.Pose]; !ok {
				return fmt.Errorf("%s: the character %q has no pose %q (it has %s)", n, c.Character, c.Pose, strings.Join(ch.PoseNames(), ", "))
			}
		}
		if c.Expression != "" && !ch.HasExpression(c.Expression) {
			return fmt.Errorf("%s: the character %q has no expression %q (it has %s)", n, c.Character, c.Expression, strings.Join(ch.Expressions, ", "))
		}
		if !isAction(c.Action) {
			return fmt.Errorf("%s: the action %q is not one of %s", n, c.Action, strings.Join(Actions, ", "))
		}
		if c.Size <= 0 || c.Size > 100 || c.X < 0 || c.X > 100 || c.Y < 0 || c.Y > 140 {
			return fmt.Errorf("%s: the position (%v, %v) and the size %v must be percentages of the frame (size above 0, up to 100)", n, c.X, c.Y, c.Size)
		}
		if c.Action == ActionEnter || c.Action == ActionExit {
			continue
		}
		left, right, top, bottom := bounds(ch, c, width, height)
		w, h := float64(width), float64(height)
		if c.Action == ActionSlide {
			left, right = left-slideReach*w, right+slideReach*w
		}
		if left < -0.5 || right > w+0.5 || top < -0.5 || bottom > h+0.5 {
			return fmt.Errorf("%s would be drawn partly outside the frame (from x %.0f to %.0f of %d, from y %.0f to %.0f of %d): move it or make it smaller, or let it enter or exit", n, left, right, width, top, bottom, height)
		}
	}
	return nil
}

// bounds is the box a character is drawn in, in frame pixels, with the biggest it gets (a bouncing character swells).
func bounds(ch Character, c StageCharacter, width, height int) (left, right, top, bottom float64) {
	h := c.Size / 100 * float64(height)
	w := h * float64(ch.Width) / float64(ch.Height)
	if c.Action == ActionBounce {
		h, w = h*bounceScale, w*bounceScale
	}
	ax, ay := ch.Anchor.X, ch.Anchor.Y
	x, y := c.X/100*float64(width), c.Y/100*float64(height)
	return x - ax*w, x + (1-ax)*w, y - ay*h, y + (1-ay)*h
}

// Actor is a character ready to be drawn: its drawing and where it goes.
type Actor struct {
	ID         string  `json:"id"`
	Action     string  `json:"action"`
	X          float64 `json:"x"`
	Y          float64 `json:"y"`
	Size       float64 `json:"size"`
	AspectWH   float64 `json:"aspect"`
	AnchorX    float64 `json:"anchor_x"`
	AnchorY    float64 `json:"anchor_y"`
	Expression string  `json:"-"`
	SVG        string  `json:"-"`
	// Scope is the class of the element that holds the drawing; the drawing's CSS applies only inside it.
	Scope string `json:"-"`
}

var (
	reStyleBlock = regexp.MustCompile(`(?is)(<style[^>]*>)(.*?)(</style>)`)
	reRule       = regexp.MustCompile(`([^{}]+)\{`)
)

// scopeSVG makes the CSS of an inline drawing apply only inside its own element: two characters that use the same
// class name for different things must not change each other. Every selector of the drawing's <style> gets the
// scope class in front.
func scopeSVG(svg, scope string) string {
	return reStyleBlock.ReplaceAllStringFunc(svg, func(block string) string {
		m := reStyleBlock.FindStringSubmatch(block)
		css := reRule.ReplaceAllStringFunc(m[2], func(rule string) string {
			sel := strings.TrimSuffix(rule, "{")
			parts := strings.Split(sel, ",")
			for i, p := range parts {
				parts[i] = "." + scope + " " + strings.TrimSpace(p)
			}
			return strings.Join(parts, ",") + "{"
		})
		return m[1] + css + m[3]
	})
}

// ResolveStage turns a stage into the SVG of its background (empty for none) and its actors. The stage must be valid.
func ResolveStage(st *Stage, lib Library) (backdrop string, actors []Actor, err error) {
	if st == nil {
		return "", nil, nil
	}
	if st.Background != "" {
		b, ok := lib.Background(st.Background)
		if !ok {
			return "", nil, fmt.Errorf("the background %q is not in the library", st.Background)
		}
		backdrop = b.SVG
	}
	for i, c := range st.Characters {
		ch, ok := lib.Character(c.Character)
		if !ok {
			return "", nil, fmt.Errorf("the character %q is not in the library", c.Character)
		}
		pose := c.Pose
		if pose == "" {
			pose = ch.DefaultPose
		}
		expr := c.Expression
		if expr == "" {
			expr = ch.DefaultExpression
		}
		id := fmt.Sprintf("a%d", i+1)
		scope := "char-" + ch.Name + "-" + id
		actors = append(actors, Actor{Scope: scope, ID: id, Action: c.Action, X: c.X, Y: c.Y, Size: c.Size, AspectWH: float64(ch.Width) / float64(ch.Height),
			AnchorX: ch.Anchor.X, AnchorY: ch.Anchor.Y, Expression: expr, SVG: scopeSVG(ch.SVG[pose], scope)})
	}
	return backdrop, actors, nil
}

// AddStages makes a script illustrated: every piece draws nothing of its own (the blank part) and gets a stage, a
// background and two characters of the library chosen in turn, with poses, expressions and actions that change from
// piece to piece. The pieces of the same section share their background. It leaves a script alone when the library
// has no character or no background. The pieces carry no mode: they follow the design's.
func AddStages(s *Script, lib Library, d Design) {
	chars, bgs := lib.Characters(), lib.Backgrounds()
	if len(chars) == 0 || len(bgs) == 0 {
		return
	}
	actions := []string{ActionBounce, ActionLook, ActionWave, ActionSlide}
	secBG := map[string]int{}
	for i := range s.Pieces {
		p := &s.Pieces[i]
		if p.Blender != nil {
			continue // a 3D piece has no stage
		}
		p.Part, p.Params = "blank", nil
		bi, ok := secBG[p.Section]
		if !ok {
			bi = len(secBG) % len(bgs)
			secBG[p.Section] = bi
		}
		st := &Stage{Background: bgs[bi].Name}
		for k := 0; k < 2 && k < len(chars)+1; k++ {
			ch := chars[(i+k)%len(chars)]
			names := ch.PoseNames()
			pose := ch.DefaultPose
			if len(names) > 0 {
				pose = names[(i+k)%len(names)]
			}
			expr := ch.DefaultExpression
			if len(ch.Expressions) > 0 {
				expr = ch.Expressions[(i+k)%len(ch.Expressions)]
			}
			st.Characters = append(st.Characters, StageCharacter{Character: ch.Name, Pose: pose, Expression: expr, Action: actions[(i+2*k)%len(actions)],
				X: []float64{30, 68}[k], Y: 78, Size: 42})
		}
		p.Stage = st
	}
}
