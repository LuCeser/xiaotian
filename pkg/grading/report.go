package grading

import (
	"fmt"
	"math"
	"strings"
)

// WordInput is one graded queue word entering Analyze: the dictation word, the
// student's raw answer ("" when unanswered), and its GradeWord judgment.
type WordInput struct {
	Word     string
	Heard    string
	Judgment WordJudgment
}

// WordError is one wrong word's report entry: what happened and a concrete
// Chinese hint for the student. Ops is the FULL char alignment (matches
// included) — the UI renders the whole word with matches normal and
// mismatches highlighted, which needs the positional context.
type WordError struct {
	Word     string   `json:"word"`
	Heard    string   `json:"heard"`
	Hint     string   `json:"hint"`
	Ops      []CharOp `json:"ops"`
	Blank    bool     `json:"blank"`
	NearMiss bool     `json:"near_miss"`
}

// SessionReport is the per-session analysis the client renders on the report
// screen and the handler persists (Ticket 06).
type SessionReport struct {
	Encouragement string      `json:"encouragement"` // read aloud via TTS
	Errors        []WordError `json:"errors"`
	Patterns      []string    `json:"patterns"`
	Suggestions   []string    `json:"suggestions"`
	Total         int         `json:"total"`
	CorrectCount  int         `json:"correct_count"`
	Score         int         `json:"score"` // 0-100, rounded
}

// patternEvidence is how many words must share a pattern before it is worth
// telling the student about it (one occurrence is an anecdote, two is a habit).
const patternEvidence = 2

// Analyze builds the session report from graded words in queue order. Pure:
// cross-session patterns (repeat misses, trends) are merged by the caller,
// which owns the history query.
func Analyze(words []WordInput) SessionReport {
	r := SessionReport{Total: len(words)}
	var nearMisses, blanks, missingTails int

	for _, w := range words {
		if w.Judgment.Correct {
			r.CorrectCount++
			continue
		}
		we := WordError{
			Word:     w.Word,
			Heard:    w.Heard,
			Blank:    w.Judgment.Blank,
			NearMiss: w.Judgment.NearMiss,
			Ops:      w.Judgment.Diff,
		}
		we.Hint = wordHint(we)
		r.Errors = append(r.Errors, we)

		switch {
		case w.Judgment.Blank:
			blanks++
		case w.Judgment.NearMiss:
			nearMisses++
		}
		if isMissingTail(w.Word, w.Heard, w.Judgment) {
			missingTails++
		}
	}

	if r.Total > 0 {
		r.Score = Percent(r.CorrectCount, r.Total)
	}
	r.Patterns = sessionPatterns(blanks, nearMisses, missingTails, len(r.Errors))
	r.Suggestions = suggestions(r.Errors)
	r.Encouragement = Encouragement(r.Score, r.CorrectCount, r.Total)
	return r
}

// wordHint explains one wrong word in student-friendly Chinese. Blank words
// get the "write something" nudge; others describe their mismatch ops.
func wordHint(we WordError) string {
	if we.Blank {
		return "没有作答，下次听到就先把会的部分写下来"
	}
	var parts []string
	var subs, dels, ins []string
	for _, op := range we.Ops {
		switch op.Kind {
		case OpSub:
			subs = append(subs, fmt.Sprintf("「%s」写成了「%s」", op.Expected, op.Heard))
		case OpDel:
			dels = append(dels, fmt.Sprintf("「%s」", op.Expected))
		case OpIns:
			ins = append(ins, fmt.Sprintf("「%s」", op.Heard))
		}
	}
	if len(dels) > 0 {
		parts = append(parts, "漏写了"+strings.Join(dels, "、"))
	}
	if len(ins) > 0 {
		parts = append(parts, "多写了"+strings.Join(ins, "、"))
	}
	if len(subs) > 0 {
		parts = append(parts, strings.Join(subs, "、"))
	}
	if hasKind(we.Ops, OpSwap) {
		parts = append(parts, "顺序颠倒了")
	}
	hint := strings.Join(parts, "；")
	if we.NearMiss {
		return "只差一点！" + hint
	}
	return hint
}

// hasKind reports whether any op in the (full) alignment has the kind — the
// hint templates describe what went wrong, so one swap anywhere in the word
// is "顺序颠倒了".
func hasKind(ops []CharOp, kind string) bool {
	for _, op := range ops {
		if op.Kind == kind {
			return true
		}
	}
	return false
}

// isMissingTail reports whether the answer is the word minus its ending — the
// classic dropped word-tail ("eatin" for "eating", "goin" for "going").
func isMissingTail(word, heard string, j WordJudgment) bool {
	if j.Blank || j.Correct {
		return false
	}
	// Normalized prefix check without re-deriving the language here: the diff
	// of a missing-tail word is all matches plus one trailing deletion.
	var last string
	for _, op := range j.Diff {
		if op.Kind != OpMatch {
			last = op.Kind
		}
	}
	return last == OpDel
}

// sessionPatterns states the habits worth calling out. Each pattern needs at
// least patternEvidence words of evidence.
func sessionPatterns(blanks, nearMisses, missingTails, wrongCount int) []string {
	var patterns []string
	if blanks >= patternEvidence {
		patterns = append(patterns, fmt.Sprintf("有 %d 题没有作答，听到就先写下记住的部分", blanks))
	}
	if nearMisses >= patternEvidence {
		patterns = append(patterns, fmt.Sprintf("有 %d 个词只差一点点，写完再检查一遍就更棒了", nearMisses))
	}
	if missingTails >= patternEvidence {
		patterns = append(patterns, fmt.Sprintf("有 %d 个词漏了词尾，交卷前重点检查结尾", missingTails))
	}
	if len(patterns) == 0 && wrongCount >= patternEvidence {
		patterns = append(patterns, fmt.Sprintf("错了 %d 个词，把正确写法多看几遍", wrongCount))
	}
	return patterns
}

// suggestions returns concrete next-step actions. The wrong-word practice line
// always leads when there is anything to practice.
func suggestions(errors []WordError) []string {
	if len(errors) == 0 {
		return []string{"继续保持，挑战更长的词吧！"}
	}
	var wrong, blank, near []string
	for _, e := range errors {
		switch {
		case e.Blank:
			blank = append(blank, e.Word)
		case e.NearMiss:
			near = append(near, e.Word)
		default:
			wrong = append(wrong, e.Word)
		}
	}
	var out []string
	list := func(ws []string) string {
		const cap = 5
		if len(ws) > cap {
			return strings.Join(ws[:cap], "、") + " 等"
		}
		return strings.Join(ws, "、")
	}
	if len(wrong) > 0 {
		out = append(out, "把错的词各抄三遍："+list(wrong))
	}
	if len(near) > 0 {
		out = append(out, "只差一点的词对照正确写法再看一遍："+list(near))
	}
	if len(blank) > 0 {
		out = append(out, "没写出来的词多听几遍音频："+list(blank))
	}
	return out
}

// Percent is the score arithmetic every caller shares (0 for empty sessions;
// rounded half-up). Exported because the per-word completion path computes a
// score from a ratings map, not from Analyze inputs.
func Percent(correct, total int) int {
	if total <= 0 {
		return 0
	}
	return int(math.Round(float64(correct) * 100 / float64(total)))
}

// RatingCountsAsCorrect reports whether an FSRS rating verb represents a
// successfully recalled word ("good"/"easy" yes; "hard" is a near-miss and
// "again" a miss). The knowledge lives here so handlers never re-derive it.
func RatingCountsAsCorrect(rating string) bool {
	return rating == "good" || rating == "easy"
}

// Encouragement is the sentence spoken aloud (and shown) at grading time —
// the tiers are perfect / strong / okay / keep-trying. Exported for the
// per-word completion path, whose counts differ from Analyze inputs.
func Encouragement(score, correct, total int) string {
	switch {
	case total > 0 && score == 100:
		return fmt.Sprintf("太棒了！%d 个词全部正确，你是听写小达人！", total)
	case score >= 80:
		return fmt.Sprintf("真不错！%d 个词对了 %d 个，继续保持！", total, correct)
	case score >= 60:
		return fmt.Sprintf("不错哦，对了 %d 个词。把错的词再练一练，下次一定更好！", correct)
	default:
		return fmt.Sprintf("别灰心，这次对了 %d 个词，错的词多写几遍，你会越来越棒的！", correct)
	}
}
