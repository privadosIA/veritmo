package assist

import (
	"errors"
	"fmt"
	"strings"

	"veritmo/internal/platform/edition"
)

// ErrRefused marks an answer in which the model declined to write the text.
// The refusal is the model's own behaviour: the application adds no content
// filter and its instructions say nothing about content.
var ErrRefused = errors.New("the model refused")

// maxRefusalWords: a real prompt is 70 to 100 words of description, a refusal
// is short, so a long reply is never taken for one.
const maxRefusalWords = 80

// refusalOpeners are the phrases a refusal opens with, lower case, with
// straight apostrophes (see normalizeForRefusal).
var refusalOpeners = []string{
	"i can't", "i cannot", "i can not", "i'm sorry", "i am sorry", "sorry",
	"i'm unable", "i am unable", "i won't", "i will not", "i apologize", "as an ai",
	"lo siento", "no puedo", "lamento", "no me es posible",
}

// IsRefusal reports whether a (cleaned) reply is a refusal: it opens with a
// refusal phrase in English or Spanish, ignoring case and wrapping quotes, and
// has at most 80 words.
func IsRefusal(reply string) bool {
	s := normalizeForRefusal(reply)
	if s == "" || len(strings.Fields(s)) > maxRefusalWords {
		return false
	}
	for _, o := range refusalOpeners {
		if strings.HasPrefix(s, o) {
			// "Sorry-looking dog": the opener must end the word.
			rest := s[len(o):]
			if rest == "" || !isWordChar(rest[0]) && rest[0] != '-' {
				return true
			}
		}
	}
	return false
}

func isWordChar(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b >= 0x80
}

func normalizeForRefusal(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.NewReplacer("’", "'", "‘", "'", "´", "'").Replace(s)
	return strings.TrimLeft(s, "\"'“”«»* \t\r\n")
}

// refused is the error for a refusal by the named model.
func refused(model string) error {
	if !edition.RefusalAdvice() {
		// The public edition says which model declined and nothing about getting around it.
		return fmt.Errorf("%w: %s refused to write this text. The refusal comes from the model, not from the application", ErrRefused, model)
	}
	return fmt.Errorf("%w: %s refused to write this text. The refusal comes from the model, not from the application: "+
		"choose another installed model (the model selector, --model or ORCHESTRATOR_ASSIST_MODEL)", ErrRefused, model)
}
