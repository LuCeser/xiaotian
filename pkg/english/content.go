// Package english classifies stored dictation text conservatively. It does not
// infer grammar with a model or change the material's original text.
package english

import (
	"regexp"
	"strings"
	"unicode"
)

type Kind uint8

const (
	Word Kind = iota
	Phrase
	Sentence
	Ambiguous
)

var Placeholder = regexp.MustCompile(`\.(?:[\s\p{Zs}]*\.){2,}|…+`)
var token = regexp.MustCompile(`[a-zA-Z]+(?:['’-][a-zA-Z]+)*`)
var abbreviation = regexp.MustCompile(`(?i)\b(?:sb|sth|mr|mrs|ms|dr)\.|\b(?:[A-Za-z]\.){2,}`)

// The finite inventory deliberately prefers missed phrases to treating an
// unpunctuated sentence as a phrase. New constructions need explicit examples.
var knownPhrase = regexp.MustCompile(`(?i)^(?:on the (?:ground|first|second|third|top) floor|ice cream|look after|take care|take care of|in front of|next to|be kind to|live near)(?:\s+(?:sb\.|sth\.))?$`)
var sentenceStart = regexp.MustCompile(`(?i)^(?:i|you|he|she|it|we|they|i'm|you're|he's|she's|it's|we're|they're|where|what|when|why|who|how|is|are|am|was|were|do|does|did|can|could|will|would|shall|should|there|this|that|these|those)\b`)
var subject = regexp.MustCompile(`(?i)\b(?:i|you|he|she|it|we|they)\b`)

func Classify(text string) Kind {
	s := strings.TrimSpace(text)
	for _, r := range s {
		if unicode.IsLetter(r) && r > 127 {
			return Word // subject/category gates retain existing non-English behavior
		}
	}
	if Placeholder.MatchString(s) && token.MatchString(s) {
		return Phrase
	}
	withoutAbbreviations := abbreviation.ReplaceAllString(s, "abbreviation")
	if len(token.FindAllString(withoutAbbreviations, -1)) < 2 {
		return Word
	}
	if strings.ContainsAny(withoutAbbreviations, ".!?。！？\n\r") || sentenceStart.MatchString(s) || subject.MatchString(s) {
		return Sentence
	}
	if knownPhrase.MatchString(strings.Join(strings.Fields(s), " ")) {
		return Phrase
	}
	return Ambiguous
}
