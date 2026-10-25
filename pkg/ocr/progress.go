package ocr

// progress.go implements the teaching-progress interpretation pipeline
// (Issue #82): the parent states learned progress in ONE natural sentence
// ("现在学到第三单元了"); this pipeline sends the sentence plus the
// REGISTERED textbook catalogs to a text LLM and turns the reply into a
// VERIFIED proposal — the model picks the book and lesson, the pipeline
// checks the lesson against the authoritative lesson list (verbatim,
// never a guess) and resolves the learned count by list position
// (CONTEXT.md: 课次先后由名单书写顺序决定，不按名称排序).
//
// Like meaning.go this owns NO state and writes NOTHING: the result is a
// proposal the parent confirms; persistence rides the ordinary textbook
// update path, so interpreting progress can never start a classroom or
// touch cards (已学进度 states: 家长明确说明，导入与理解本身不改进度).

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Interpretation status values (the locked wire vocabulary shared by the
// pipeline result and the HTTP response).
const (
	// ProgressProposed: exactly one textbook+lesson was understood AND
	// verified against the catalog — ready for the parent's confirmation.
	ProgressProposed = "proposed"
	// ProgressUnclear: the statement is about progress but the target
	// cannot be determined (missing catalog, ambiguous book, …). Reason
	// carries the one question to ask; Candidates may carry the minimal
	// book choices when only the book is in doubt.
	ProgressUnclear = "unclear"
	// ProgressNotStatement: the sentence is NOT about teaching progress
	// (adding words, starting a classroom, …) — reason explains it.
	ProgressNotStatement = "not_progress"
)

// ProgressTextbook is one registered textbook as the interpretation sees
// it: the display identity, the metadata that disambiguates same-name
// lessons across books, the ordered lesson list (the ground truth the
// model must copy lesson names from), and the current learned count.
type ProgressTextbook struct {
	ID             int64
	Name           string
	Category       string
	Edition        string
	Stage          string
	Grade          string
	Volume         string
	Module         string
	Lessons        []string
	LearnedLessons int
}

// ProgressCandidate is one minimal confirmation choice when only the BOOK
// is in doubt: the book that contains the named lesson, with the learned
// count applying this candidate would set.
type ProgressCandidate struct {
	TextbookID     int64  `json:"textbookId"`
	TextbookName   string `json:"textbookName"`
	Lesson         string `json:"lesson"`
	LearnedLessons int    `json:"learnedLessons"`
}

// ProgressInterpretation is the verified outcome. Proposed fills the
// textbook/lesson/learned fields; Unclear fills Reason (the question to
// ask) and possibly Candidates; NotStatement fills Reason only.
type ProgressInterpretation struct {
	Status         string
	TextbookID     int64
	TextbookName   string
	Lesson         string
	LearnedLessons int
	Reason         string
	Candidates     []ProgressCandidate
}

// progressSystemPrompt states the task: decide whether the sentence is a
// progress statement, pick the lesson VERBATIM from a registered list,
// name the book only when the statement plus context identify exactly one.
const progressSystemPrompt = `你是听写助手小天的家长端进度理解助手。家长会用一句话说明孩子的教学进度，例如「现在学到第五课了」「语文上到第二单元了」。
给你已登记教材的真实信息：编号、名称、学科、教材信息、按教学顺序的课次名单、当前已学进度。你的任务：
1. 判断这句话是否在说明教学进度。指定默写、加入范围、开始课堂等不是进度说明，用 not_progress 并用一句中文说明原因。
2. 找到家长说的课次：课次名称必须从某本教材的课次名单里逐字选择。家长可能说「第三单元」「Unit 3」「第五课」，你要对应到名单里的实际名称；找不到对应课次（名单为空或没有匹配）就返回 unclear，reason 用一句中文向家长说明还缺什么，不要编造课次。
3. 确定教材：这句话和教材信息能确定唯一一本时给出 textbook_id；两本以上教材都说得通（例如不同教材有同名课次，而话里没有说明是哪本）时 textbook_id 用 null，lesson 填你找到的课次名，reason 用一句中文问家长是哪一本。
只输出一个 JSON 对象，不要输出 JSON 以外的内容。格式：
- 能确定教材和课次：{"status":"proposed","textbook_id":7,"lesson":"第三单元"}
- 课次明确但教材有歧义：{"status":"unclear","lesson":"第三单元","reason":"请问是哪一本教材？"}
- 无法确定课次：{"status":"unclear","reason":"..."}
- 不是进度说明：{"status":"not_progress","reason":"..."}`

// BuildProgressUserPrompt renders the per-call user turn from the catalog
// and the parent's sentence. Exported so the wire handler's tests can pin
// what the prompt carries; pure.
func BuildProgressUserPrompt(message string, books []ProgressTextbook) string {
	var b strings.Builder
	b.WriteString("已登记教材：\n")
	for _, book := range books {
		fmt.Fprintf(&b, "编号 %d《%s》", book.ID, book.Name)
		info := []string{}
		if label := progressCategoryLabel(book.Category); label != "" {
			info = append(info, "学科 "+label)
		}
		if meta := progressMetaSummary(book); meta != "" {
			info = append(info, meta)
		}
		if len(info) > 0 {
			b.WriteString("（" + strings.Join(info, "·") + "）")
		}
		b.WriteString("\n")
		if len(book.Lessons) == 0 {
			b.WriteString("  课次名单：还没有课次名单\n")
		} else {
			b.WriteString("  课次名单（按教学顺序）：" + strings.Join(book.Lessons, "、") + "\n")
		}
		if book.LearnedLessons > 0 {
			fmt.Fprintf(&b, "  当前已学完第 %d 课\n", book.LearnedLessons)
		} else {
			b.WriteString("  当前尚未开始\n")
		}
	}
	b.WriteString("家长说：「")
	b.WriteString(message)
	b.WriteString("」")
	return b.String()
}

// progressMetaSummary joins the non-empty metadata fields with · (the
// parent-textbook UI's textbookMetaSummary, server-side twin).
func progressMetaSummary(book ProgressTextbook) string {
	parts := []string{}
	for _, v := range []string{book.Edition, book.Stage, book.Grade, book.Volume, book.Module} {
		if v = strings.TrimSpace(v); v != "" {
			parts = append(parts, v)
		}
	}
	return strings.Join(parts, "·")
}

// progressCategoryLabel maps the wire category onto the display word the
// prompt promises the model (学科). An empty/unknown category renders
// nothing rather than a guess.
func progressCategoryLabel(category string) string {
	switch strings.TrimSpace(category) {
	case "english":
		return "英语"
	case "chinese":
		return "语文"
	default:
		return ""
	}
}

// ProgressInterpreter turns one parent sentence into a verified progress
// proposal end-to-end: call the text seam with the catalog-grounded
// prompts, parse the reply leniently, verify against the authoritative
// lists. It owns no state; one instance serves every request.
type ProgressInterpreter struct {
	chat ChatReader
}

// NewProgressInterpreter builds the interpreter. A nil ChatReader yields
// an interpreter whose Interpret fails with the errNilSeam wiring error
// (the HTTP layer keeps the endpoint mounted and honest via 503 — the
// meaning generator's degraded-boot contract).
func NewProgressInterpreter(chat ChatReader) *ProgressInterpreter {
	return &ProgressInterpreter{chat: chat}
}

// Interpret runs one understanding pass. Error contract mirrors
// MeaningGenerator.Generate: sentinels detectable via errors.Is; a reply
// that names a lesson outside every registered list is NOT an error — it
// comes back Unclear with a question (the pipeline never invents or
// fuzzy-matches a lesson, 不臆造课次).
func (p *ProgressInterpreter) Interpret(ctx context.Context, message string, books []ProgressTextbook) (ProgressInterpretation, error) {
	if p.chat == nil {
		return ProgressInterpretation{}, errNilSeam
	}
	message = strings.TrimSpace(message)
	if message == "" {
		return ProgressInterpretation{Status: ProgressUnclear, Reason: "请先用一句话说明学到哪里，例如「现在学到第三单元了」"}, nil
	}

	reply, err := p.chat(ctx, progressSystemPrompt, BuildProgressUserPrompt(message, books))
	if err != nil {
		return ProgressInterpretation{}, fmt.Errorf("%w: %w", errReaderFailed, err)
	}
	parsed, ok := parseProgressReply(reply)
	if !ok {
		return ProgressInterpretation{}, errUnparseableReply
	}
	return verifyProgress(parsed, books), nil
}

// progressReplyJSON is the asked-for decode shape.
type progressReplyJSON struct {
	Status     string `json:"status"`
	TextbookID *int64 `json:"textbook_id"`
	Lesson     string `json:"lesson"`
	Reason     string `json:"reason"`
}

// parseProgressReply leniently extracts the reply object: the outermost
// {...} span (a chatty model's fence or prose survives), else the first
// {...} a Decoder finds. ok=false means nothing parseable — the caller's
// retryable error, distinct from a parsed but unusable answer.
func parseProgressReply(reply string) (progressReplyJSON, bool) {
	var parsed progressReplyJSON
	if start, end := strings.Index(reply, "{"), strings.LastIndex(reply, "}"); start >= 0 && end > start {
		if err := json.Unmarshal([]byte(reply[start:end+1]), &parsed); err == nil {
			return parsed, true
		}
	}
	for i := 0; i < len(reply); i++ {
		if reply[i] != '{' {
			continue
		}
		if err := json.NewDecoder(strings.NewReader(reply[i:])).Decode(&parsed); err == nil {
			return parsed, true
		}
	}
	return parsed, false
}

// verifyProgress maps the model's answer onto the catalog. Proposed picks
// are checked: the textbook must exist and the lesson must be IN that
// book's list verbatim; a failed check degrades through the candidate
// resolution (the named lesson still picks out the books that really
// contain it) instead of trusting the model. When the lesson sits in
// several books but the model NAMES one, the named pick stands — the model
// resolved the sentence's own identification, and the parent's confirm
// card shows the book for the explicit correction (家长的明确纠正优先).
func verifyProgress(reply progressReplyJSON, books []ProgressTextbook) ProgressInterpretation {
	switch strings.TrimSpace(reply.Status) {
	case ProgressNotStatement:
		return ProgressInterpretation{Status: ProgressNotStatement, Reason: reply.Reason}
	case ProgressProposed, ProgressUnclear:
	default:
		return ProgressInterpretation{Status: ProgressUnclear, Reason: "没有听懂这句话，请换个说法说明学到哪里"}
	}

	lesson := strings.TrimSpace(reply.Lesson)
	if lesson == "" {
		return ProgressInterpretation{Status: ProgressUnclear, Reason: orDefaultReason(reply.Reason, "还没有听出具体学到哪一课，请说明课次或单元")}
	}

	// Which books really contain the named lesson — the authoritative
	// membership check, exact-match (never fuzzy, never sorted guesses).
	var matches []ProgressTextbook
	for _, book := range books {
		for _, name := range book.Lessons {
			if name == lesson {
				matches = append(matches, book)
				break
			}
		}
	}

	// A proposed pick survives only when the named book is one of the
	// real containers; otherwise the lesson's own containers decide.
	if reply.Status == ProgressProposed && reply.TextbookID != nil {
		for _, book := range matches {
			if book.ID == *reply.TextbookID {
				return proposedResult(book, lesson)
			}
		}
	}
	if len(matches) == 1 {
		// The model could not name the book (or named a wrong one) but the
		// lesson itself identifies exactly one registered book.
		return proposedResult(matches[0], lesson)
	}
	if len(matches) > 1 {
		out := ProgressInterpretation{Status: ProgressUnclear, Lesson: lesson,
			Reason: orDefaultReason(reply.Reason, "有不止一本教材都有课次「"+lesson+"」，请选择是哪一本")}
		for _, book := range matches {
			out.Candidates = append(out.Candidates, ProgressCandidate{
				TextbookID:     book.ID,
				TextbookName:   book.Name,
				Lesson:         lesson,
				LearnedLessons: lessonPosition(book.Lessons, lesson),
			})
		}
		return out
	}
	// The named lesson is in NO registered list: ask, never invent.
	return ProgressInterpretation{Status: ProgressUnclear, Lesson: lesson,
		Reason: orDefaultReason(reply.Reason, "登记的教材里没有找到课次「"+lesson+"」，请核对课次名单或补充课次")}
}

// proposedResult builds the verified proposal for one book+lesson pair
// (shared by the model-pick and unique-container branches).
func proposedResult(book ProgressTextbook, lesson string) ProgressInterpretation {
	return ProgressInterpretation{
		Status:         ProgressProposed,
		TextbookID:     book.ID,
		TextbookName:   book.Name,
		Lesson:         lesson,
		LearnedLessons: lessonPosition(book.Lessons, lesson),
	}
}

// lessonPosition returns the lesson's 1-based list position (已学完第 N
// 课 includes lesson N). The caller has verified membership.
func lessonPosition(lessons []string, lesson string) int {
	for i, name := range lessons {
		if name == lesson {
			return i + 1
		}
	}
	return 0
}

// orDefaultReason keeps the model's clarification when it is usable and
// substitutes the honest fallback otherwise.
func orDefaultReason(reason, fallback string) string {
	if r := strings.TrimSpace(reason); r != "" {
		return r
	}
	return fallback
}
