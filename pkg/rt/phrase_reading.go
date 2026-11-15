package rt

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
)

// Match placeholders only: ordinary periods, decimals and abbreviations survive.
var phrasePlaceholder = regexp.MustCompile(`\.(?:[\s\p{Zs}]*\.){2,}|…+`)

var englishExampleLetter = regexp.MustCompile(`[A-Za-z]`)

// wordReadingText projects a stored English phrase into speech. The original
// card remains the dictation/check target. This path never generates examples.
// ordinary preserves the existing templates for all other vocabulary/subjects.
func wordReadingText(word lesson.Word, category string, index int, ordinary string, slow bool) string {
	if category != lesson.CategoryEnglish || !phrasePlaceholder.MatchString(word.Word) {
		return ordinary
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
	spoken := fmt.Sprintf("第 %d 个词组，%s。", index, fixedText)
	if slow {
		spoken += fixedText + "。"
	}
	const instruction = "请写词组。"
	example := strings.TrimSpace(word.Example)
	if englishExampleLetter.MatchString(example) && !phrasePlaceholder.MatchString(example) &&
		utf8.RuneCountInString(spoken+"例句："+example+" "+instruction) <= maxReplyRunes {
		spoken += "例句：" + example + " "
	}
	return spoken + instruction
}
