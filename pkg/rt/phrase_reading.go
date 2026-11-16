package rt

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/xiaotian/home-tutor-agent/pkg/english"
	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
)

// Match placeholders only: ordinary periods, decimals and abbreviations survive.
var phrasePlaceholder = english.Placeholder

var englishExampleLetter = regexp.MustCompile(`[A-Za-z]`)

// wordReadingText projects a stored English phrase into speech. The original
// card remains the dictation/check target. This path never generates examples.
// ordinary preserves the existing templates for all other vocabulary/subjects.
func wordReadingText(word lesson.Word, category string, index int, ordinary string, slow bool) string {
	if category != lesson.CategoryEnglish {
		return ordinary
	}
	kind := english.Classify(word.Word)
	if kind == english.Word {
		return ordinary
	}
	if kind != english.Phrase {
		text := strings.TrimSpace(word.Word)
		if !strings.ContainsAny(text[len(text)-1:], ".!?") && !strings.HasSuffix(text, "。") && !strings.HasSuffix(text, "！") && !strings.HasSuffix(text, "？") {
			text += "。"
		}
		label, instruction := "句", "请写整句。"
		if kind == english.Ambiguous {
			label, instruction = "项", "请按原文写完整内容。"
		}
		spoken := fmt.Sprintf("第 %d %s，%s", index, label, text)
		if slow {
			spoken += text
		}
		return spoken + instruction
	}
	fixed := []string{}
	start := 0
	for _, span := range phrasePlaceholder.FindAllStringIndex(word.Word, -1) {
		end := span[0]
		previous, _ := utf8.DecodeLastRuneInString(word.Word[:end])
		next, _ := utf8.DecodeRuneInString(word.Word[end+1:])
		// In "U.S. ...", the abbreviation's final period is separated from
		// a complete placeholder. Keep that punctuation with the fixed text.
		if unicode.IsLetter(previous) && unicode.IsSpace(next) && strings.Count(word.Word[end:span[1]], ".") >= 4 {
			end++
		}
		if part := strings.TrimSpace(word.Word[start:end]); part != "" {
			fixed = append(fixed, part)
		}
		start = span[1]
	}
	if part := strings.TrimSpace(word.Word[start:]); part != "" {
		fixed = append(fixed, part)
	}
	fixedText := strings.Join(fixed, "，")
	ending := "。"
	if strings.HasSuffix(fixedText, ".") && !phrasePlaceholder.MatchString(word.Word) {
		ending = ""
	}
	spoken := fmt.Sprintf("第 %d 个词组，%s%s", index, fixedText, ending)
	if slow {
		spoken += fixedText + ending
	}
	const instruction = "请写词组。"
	example := strings.TrimSpace(word.Example)
	if englishExampleLetter.MatchString(example) && !phrasePlaceholder.MatchString(example) &&
		utf8.RuneCountInString(spoken+"例句："+example+" "+instruction) <= maxReplyRunes {
		spoken += "例句：" + example + " "
	}
	return spoken + instruction
}
