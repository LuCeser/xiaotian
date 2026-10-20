// subject.go — the unspecified-subject judgment (Issue #55): when a word
// classroom (dictation or practice) is opened with category "auto" — the
// page's generic entry, the student never having picked a subject — ONE
// model call judges the subject from the two subjects' due content, and the
// judged subject then binds the round through every #10 seam exactly like
// an explicit declaration (scoped fetch, per-subject row, scoped writes,
// session_ready echo). A declared english/chinese never reaches this file:
// the user's specification outranks the model's judgment and is never
// silently re-judged (the confirmed AC2 rule).
package rt

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
	"github.com/xiaotian/home-tutor-agent/pkg/mimo"
)

// subjectJudgeTimeout bounds the one judgment Chat call: an auto session
// must never hang on a wedged model — the deny-by-default english landing
// takes over and the classroom still opens.
const subjectJudgeTimeout = 8 * time.Second

// subjectJudgeMaxTokens caps the judgment reply (a single JSON object —
// the same tight bound philosophy as classifyMaxTokens).
const subjectJudgeMaxTokens = 50

// subjectJudgeSampleWords caps how many due WORDS ride the prompt per
// subject (counts always do; samples ground the judgment in content).
const subjectJudgeSampleWords = 5

// subjectJudgeSystemPrompt is the judgment contract: content-driven,
// mechanical, single-valued. 「用户意图」 has no channel before the
// classroom opens (init carries no speech), so the rules decide from the
// due content alone — only one subject has content → that subject; both →
// the richer round; neither → english and the no-due ending explains
// honestly, exactly like an english bookmark over an empty vocabulary.
const subjectJudgeSystemPrompt = `你是听写课堂的学科判定器。学生这次默写没有指定学科，请根据两个学科当前的到期词语内容判断这次默写应使用的学科。

规则：
1. 只有一个学科有到期词语时，选择该学科。
2. 两个学科都有到期词语时，选择到期词语更多的学科；数量相同选择 english。
3. 两个学科都没有到期词语时，选择 english（课堂会按无词可默正常结束）。

只输出一个 JSON 对象，键为 category，值为 "english" 或 "chinese"，不要输出任何其它内容。`

// subjectJudgeRetryNudge is the one correction retry's user turn (the 13-08
// invalid-response discipline: a non-JSON or off-enum reply gets exactly
// one chance to repair, then the deterministic fallback lands).
const subjectJudgeRetryNudge = "你刚才的输出不符合要求。请只输出一个 JSON 对象，键为 category，值为 english 或 chinese，不要输出任何其它内容。"

// sessionCategoryFromWire normalizes the page-supplied subject (Issue #55
// widens Issue #10's pair): "chinese" opens the 语文 classroom, "auto" asks
// for the model judgment, and anything else — including an absent field —
// is the English classroom, the pre-#10 deny-by-default landing untouched.
func sessionCategoryFromWire(category string) string {
	switch category {
	case lesson.CategoryChinese:
		return lesson.CategoryChinese
	case CategoryAuto:
		return CategoryAuto
	default:
		return lesson.CategoryEnglish
	}
}

// judgeSubjectCategory runs the one judgment call over the peek snapshot
// and returns the CONCRETE subject to bind. Degradations all land on
// english: a peek failure empties that subject's snapshot (the judgment
// input degrades, never the classroom), and a failed/invalid/malformed
// model reply after its one retry falls back deny-by-default with the
// coded error event — the same shape as the classification fallback.
func (tm *TurnManager) judgeSubjectCategory() string {
	ctx, cancel := context.WithTimeout(context.Background(), subjectJudgeTimeout)
	defer cancel()

	en := tm.peekSubjectWords(ctx, lesson.CategoryEnglish)
	zh := tm.peekSubjectWords(ctx, lesson.CategoryChinese)
	msgs := buildSubjectJudgeMessages(en, zh)
	for attempt := 1; attempt <= 2; attempt++ {
		resp, err := tm.llm.Chat(ctx, msgs,
			mimo.WithThinkingDisabled(), mimo.WithMaxTokens(subjectJudgeMaxTokens))
		if err == nil {
			if category, ok := parseSubjectJSON(resp.Text()); ok {
				return category
			}
		}
		msgs = append(msgs, mimo.Message{Role: "user", Content: subjectJudgeRetryNudge})
	}
	tm.recorder.Emit(EventError, infra.F("code", ErrCodeProviderFailed), infra.F("attempts", 2))
	return lesson.CategoryEnglish
}

// peekSubjectWords reads one subject's due snapshot without the fetch lock.
// A nil Words (word classrooms always have one wired; the guard is for
// degraded constructions) or a peek error degrades to "no content" — an
// advisory read must never block the classroom behind it.
func (tm *TurnManager) peekSubjectWords(ctx context.Context, category string) []lesson.Word {
	if tm.cfg.Words == nil {
		return nil
	}
	words, err := tm.cfg.Words.PeekDueWords(ctx, dueWordsLimit, category)
	if err != nil {
		return nil
	}
	return words
}

// buildSubjectJudgeMessages assembles the judgment call: the contract
// system prompt plus the user turn carrying both subjects' snapshots.
func buildSubjectJudgeMessages(en, zh []lesson.Word) []mimo.Message {
	var user strings.Builder
	user.WriteString("英语学科当前到期词语：")
	writeSubjectSnapshot(&user, en)
	user.WriteString("\n语文学科当前到期词语：")
	writeSubjectSnapshot(&user, zh)
	user.WriteString("\n请判断这次默写应使用的学科。")
	return []mimo.Message{
		{Role: "system", Content: subjectJudgeSystemPrompt},
		{Role: "user", Content: user.String()},
	}
}

// writeSubjectSnapshot renders one subject's due snapshot: the count
// always, up to subjectJudgeSampleWords words as content grounding. An
// empty snapshot renders 无 — the judgment's "no content" signal.
func writeSubjectSnapshot(b *strings.Builder, words []lesson.Word) {
	if len(words) == 0 {
		b.WriteString("无")
		return
	}
	fmt.Fprintf(b, "共 %d 个", len(words))
	shown := words
	if len(shown) > subjectJudgeSampleWords {
		shown = shown[:subjectJudgeSampleWords]
	}
	b.WriteString("（")
	for i, w := range shown {
		if i > 0 {
			b.WriteString("、")
		}
		b.WriteString(w.Word)
	}
	b.WriteString("）")
}

// subjectPayload is the judgment reply's strict JSON shape.
type subjectPayload struct {
	Category string `json:"category"`
}

// parseSubjectJSON parses the judgment reply through the same
// extract-and-unmarshal discipline as the intent classifier: the JSON block
// (fenced or bare) must carry a whitelisted category, anything else is a
// retry-grade failure.
func parseSubjectJSON(text string) (string, bool) {
	block, ok := extractJSON(text)
	if !ok {
		return "", false
	}
	var p subjectPayload
	if err := json.Unmarshal([]byte(block), &p); err != nil {
		return "", false
	}
	if p.Category != lesson.CategoryEnglish && p.Category != lesson.CategoryChinese {
		return "", false
	}
	return p.Category, true
}
