package assist

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Kind is what a free-form request asks to make.
type Kind string

// The kinds of request: a video, a picture, a song or a 3D model. The model never decides the last one: only the caller asks for it.
const (
	KindVideo   Kind = "video"
	KindImage   Kind = "image"
	KindMusic   Kind = "music"
	KindModel3D Kind = "model3d"
)

// ParseKind validates a kind name.
func ParseKind(s string) (Kind, error) {
	switch k := Kind(strings.ToLower(strings.TrimSpace(s))); k {
	case KindVideo, KindImage, KindMusic, KindModel3D:
		return k, nil
	}
	return "", fmt.Errorf("%w: unknown kind %q (use %q, %q, %q or %q)", ErrInvalidInput, s, KindVideo, KindImage, KindMusic, KindModel3D)
}

// TaskKind reads a request and says what it asks to make. It is not offered through ParseTask: the
// request planner is its only caller, and it goes through Run for the model's lifecycle.
const TaskKind Task = "kind"

// kindTask is the prompt of the task that says what a request asks to make.
func kindTask() taskSpec {
	return taskSpec{
		system: "You read a request for an AI generator, written in any language, and say what it asks to make. " +
			"Reply with ONE line in exactly this format and nothing else:\n" +
			"KIND=<video|image|music>; LANG=<two-letter code of the language of the request>; TITLE=<at most 6 words, in the language of the request>; " +
			"INSTRUMENTAL=<yes|no>; SECONDS=<length in seconds if the request names one, else 0>\n" +
			"music = a song, tune, melody, soundtrack or beat; image = a photo, picture, illustration, drawing, portrait or poster; " +
			"video = a clip, video, animation or scene, and also anything that is not clearly one of the others. " +
			"INSTRUMENTAL is yes only when the request asks for no singing or no voice. No preamble, no quotes.",
		user:      func(text string) string { return "Request: " + text },
		maxTokens: 80,
	}
}

// KindGuess is what the model understood of a request. Understood is false when its reply held no known
// kind (the caller then falls back on the attachments or on video).
type KindGuess struct {
	Kind         Kind
	Understood   bool
	Language     string
	Title        string
	Instrumental bool
	Seconds      int
}

var (
	kindField    = regexp.MustCompile(`(?i)\bKIND[\s*_]*[=:][\s*_]*([A-Za-z]+)`)
	langField    = regexp.MustCompile(`(?i)\bLANG[\s*_]*[=:][\s*_]*([A-Za-z]{2,3})\b`)
	titleField   = regexp.MustCompile(`(?i)\bTITLE[\s*_]*[=:][\s*_]*([^;\n]*)`)
	instrField   = regexp.MustCompile(`(?i)\bINSTRUMENTAL[\s*_]*[=:][\s*_]*([A-Za-z]+)`)
	secondsField = regexp.MustCompile(`(?i)\bSECONDS[\s*_]*[=:][\s*_]*(\d{1,4})`)
)

// ParseKindReply reads the one-line reply of TaskKind. It never fails: what it cannot read is left empty.
func ParseKindReply(reply string) KindGuess {
	var g KindGuess
	if m := kindField.FindStringSubmatch(reply); m != nil {
		if k, err := ParseKind(m[1]); err == nil && k != KindModel3D { // the kind of a 3D model is the caller's to name, never the model's
			g.Kind, g.Understood = k, true
		}
	}
	if m := langField.FindStringSubmatch(reply); m != nil {
		g.Language = strings.ToLower(m[1])
	}
	if m := titleField.FindStringSubmatch(reply); m != nil {
		g.Title = strings.Trim(strings.TrimSpace(m[1]), `"'«»“”.`)
	}
	if m := instrField.FindStringSubmatch(reply); m != nil {
		g.Instrumental = strings.EqualFold(m[1], "yes") || strings.EqualFold(m[1], "true")
	}
	if m := secondsField.FindStringSubmatch(reply); m != nil {
		g.Seconds, _ = strconv.Atoi(m[1])
	}
	return g
}

// TitleFrom is the title to use when the model gave none: the first words of the request.
func TitleFrom(text string) string {
	words := strings.Fields(text)
	if len(words) > 6 {
		words = words[:6]
	}
	return strings.Trim(strings.Join(words, " "), `.,;:!?¡¿"'`)
}

var (
	// quotedSpeech holds the words between guillemets, curly quotes or straight double quotes.
	quotedSpeech = regexp.MustCompile(`«([^»]+)»|“([^”]+)”|"([^"]+)"`)
	// saidSpeech is a verb of speaking followed by the words, up to the end of the request.
	saidSpeech = regexp.MustCompile(`(?i)(?:\bque\s+|\band\s+)?\b(?:dice|dicen|diga|digan|diciendo|says|say|saying|pronuncia|narra|narrando)\b\s*[:,]?\s+(\S.*)$`)
	// introducer is the verb of speaking left in front of a quote that was taken out.
	introducer = regexp.MustCompile(`(?i)(?:\bque\s+|\band\s+)?\b(?:dice|dicen|diga|digan|diciendo|says|say|saying|pronuncia|narra|narrando)\b[\s:,]*$`)
	// danglingConnector is what a removed quote leaves at the end of the request.
	danglingConnector = regexp.MustCompile(`(?i)[\s,;:]+(?:que|and|y|saying|diciendo|says|dice|diga)?[\s,;:]*$`)
)

// SplitSpeech separates the words a video request asks to be said from the rest of the request. The words
// are what the request quotes, or what follows a verb of speaking ("que diga …", "says …"); the rest is the
// request without them, for the tasks that write the picture and the movement. Neither is translated. When
// the request names no words the speech is empty and the rest is the request as it was.
func SplitSpeech(text string) (speech, rest string) {
	text = strings.TrimSpace(text)
	if ms := quotedSpeech.FindAllStringSubmatchIndex(text, -1); len(ms) > 0 {
		var words []string
		var b strings.Builder
		last := 0
		for _, m := range ms {
			for g := 1; g <= 3; g++ {
				if m[2*g] >= 0 {
					if w := strings.TrimSpace(text[m[2*g]:m[2*g+1]]); w != "" {
						words = append(words, w)
					}
				}
			}
			b.WriteString(introducer.ReplaceAllString(text[last:m[0]], ""))
			last = m[1]
		}
		b.WriteString(text[last:])
		if len(words) > 0 {
			return strings.Join(words, " "), cleanRest(b.String())
		}
	}
	if m := saidSpeech.FindStringSubmatchIndex(text); m != nil {
		words := strings.Trim(strings.TrimSpace(text[m[2]:m[3]]), `.,;:!?¡¿"'«»“”`)
		if words != "" {
			return words, cleanRest(text[:m[0]])
		}
	}
	return "", text
}

// cleanRest tidies the request after the words were taken out of it.
func cleanRest(s string) string {
	s = danglingConnector.ReplaceAllString(strings.TrimSpace(s), "")
	return strings.Join(strings.Fields(s), " ")
}

// model3DTask is the prompt of the task that writes the prompt of one 3D model.
func model3DTask() taskSpec {
	return taskSpec{
		system: "You write prompts for a 3D model generator that makes ONE object from one picture of it. Reply with ONE line in English, 12 to 35 words, " +
			"at most 200 characters, describing a single object alone: its shape, material, colour and finish, as a product photographed on its own. " +
			"No scene, no room, no people, no hands, no text to read, no background (it is added later) and no several objects together. No preamble, no quotes.",
		user:      func(text string) string { return "Object: " + text + "\n(Answer in English.)" },
		maxTokens: 100,
	}
}
