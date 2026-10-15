package rt

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/pkg/errors"

	"github.com/xiaotian/home-tutor-agent/pkg/database"
	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
	"github.com/xiaotian/home-tutor-agent/pkg/mimo"
)

// Issue #9 / ADR-0006: the classroom generates one simple example sentence ON
// DEMAND when the student asks for an explanation or an example during the
// dictation stage. Generation is the dispatch stage's second (and last) LLM
// call for the turn: classification stays untouched at one call, the sentence
// rides the SAME turn ctx/epoch (interrupts cancel it; a late reply never
// speaks), and the practice position never moves — the reply settles back to
// listening and re-arms the per-word countdown like every other chat-class
// intent. Nothing is persisted: the textbook's own example is quoted verbatim
// when present, a generated sentence never replaces or backfills it.

// exampleCategoryEnglish scopes every resolution and sample query to the
// English vocabulary category — the argparse default the due fetch already
// runs under (vocab_manager.py). A word with no English card grounds nothing
// (Chinese content is #10–#13's domain), so the resolver refuses it.
const exampleCategoryEnglish = "english"

// Generation call shape and honesty caps (ADR-0006 #2/#5).
const (
	// exampleMaxTokens bounds the generation call: one short kid-facing
	// reply (a meaning gloss plus ONE simple English sentence) — the
	// classification call keeps its own tighter classifyMaxTokens.
	exampleMaxTokens = 300
	// maxExampleReplyRunes rejects bloated model output instead of
	// truncating a spoken reply mid-sentence (speakTurn's 500-rune cap
	// stays the last-resort guard for every announcement).
	maxExampleReplyRunes = 400
	// maxLearnedSampleWords caps the 已学词汇 list riding the prompt —
	// vocabulary steering, not a dictionary dump (1C1G latency budget).
	maxLearnedSampleWords = 60
	// exampleContextTimeout bounds the resolver's SQLite reads. Same
	// request-timeout class as practiceListTimeout, never the classroom
	// pacing clock; the turn ctx cancels it on interrupt regardless.
	exampleContextTimeout = 5 * time.Second
)

// Example timing-event vocabulary (practice.go convention: the event lives
// beside its user, timing.go untouched). Fields stay count/label class only
// — word_index and the result label, never word or reply text (T-13-06b).
const (
	// EventExampleServed: a meaning/example intent turn in the dictation
	// stage settled its reply. result records which of the four honest
	// paths served it.
	EventExampleServed = "example_served"
)

// exampleResult is the EventExampleServed result vocabulary: stored (the
// textbook's own example was quoted — no call made), generated (on-demand
// sentence spoken), fallback (generation attempted but unavailable/failed,
// the classification fallback reply spoke), no_meaning (no confirmed
// meaning to ground on — the fixed honest wording spoke, no call made).
const (
	exampleResultStored    = "stored"
	exampleResultGenerated = "generated"
	exampleResultFallback  = "fallback"
	exampleResultNoMeaning = "no_meaning"
)

// ErrExampleUngrounded: no English-category card carries this word surface,
// so nothing grounds a classroom example (Chinese content, a card deleted
// after the lesson snapshot). The caller must NOT generate — the
// classification fallback reply speaks instead (ADR-0006 #3).
var ErrExampleUngrounded = errors.New("rt/example: word has no english card to ground an example")

// ExampleContext is the per-request generation context resolved from the
// live vocabulary: the confirmed textbook background plus a capped sample of
// the child's practiceable (已学口径, ADR-0006 #3) English words. NO region
// field by design — textbook and learned content adapt the level (US-19).
type ExampleContext struct {
	// HasTextbook reports whether a textbook background was resolved.
	HasTextbook bool
	// Edition/Stage/Grade/Volume/Module are the confirmed textbook
	// metadata (unregistered fields stay empty and render omitted).
	Edition, Stage, Grade, Volume, Module string
	// LessonName is the word's lesson inside that textbook (empty when the
	// assignment is 待确认).
	LessonName string
	// LearnedLessons is that textbook's 已学完第 N 课 scale (0 = none
	// registered as learned yet).
	LearnedLessons int
	// LearnedWords is the capped practiceable-vocabulary sample steering
	// the sentence's vocabulary (already excludes the current word).
	LearnedWords []string
}

// ExampleSource is the classroom's example-context seam (Issue #9): given
// the lesson snapshot's word, resolve the generation context or explain why
// nothing grounds one. Nil in TurnManagerConfig disables on-demand
// generation — the honest fallbacks still speak (degraded wiring, tests).
type ExampleSource interface {
	ExampleContext(ctx context.Context, w lesson.Word) (ExampleContext, error)
}

// exampleCardLister and exampleBookSource are the narrow resolver
// dependencies: method subsets of database.CardRepository and
// database.TextbookRepository (PracticeSource precedent — the production
// repos satisfy them structurally, tests fake two tiny interfaces instead
// of whole repositories).
type exampleCardLister interface {
	List(ctx context.Context, filter database.CardFilter) ([]database.Card, error)
}

type exampleBookSource interface {
	Get(ctx context.Context, id int64) (database.Textbook, error)
	AssignmentsByCardIDs(ctx context.Context, cardIDs []int64) (map[int64][]database.TextbookAssignment, error)
}

// ExampleResolver is the production ExampleSource: exact word-surface
// resolution inside the English category, textbook pick among the word's
// attributions (learned-source first, then primary, then stable id order),
// and the practiceable-vocabulary sample — all read-only Go-side queries.
type ExampleResolver struct {
	Cards     exampleCardLister
	Textbooks exampleBookSource
}

// ErrExampleUnwired: the resolver was constructed without its read
// dependencies — an assembly bug, distinct from ErrExampleUngrounded's
// domain refusal (a wiring gap must never blame the word). Both land in
// the caller's honest fallback; the split keeps the failure class honest.
var ErrExampleUnwired = errors.New("rt/example: resolver constructed without read dependencies")

// ExampleContext resolves the generation context for one classroom word.
// ErrExampleUngrounded is the structured refusal for words with no English
// card; any other error is a read failure the caller treats as a fallback
// (never as "supplementary word" — that claim would be unearned).
func (r *ExampleResolver) ExampleContext(ctx context.Context, w lesson.Word) (ExampleContext, error) {
	ec := ExampleContext{}
	if r == nil || r.Cards == nil || r.Textbooks == nil {
		return ec, ErrExampleUnwired
	}
	cards, err := r.Cards.List(ctx, database.CardFilter{Category: exampleCategoryEnglish})
	if err != nil {
		return ec, errors.Wrap(err, "rt/example: list english cards")
	}
	var card *database.Card
	for i := range cards {
		if cards[i].Word == w.Word {
			card = &cards[i]
			break // List orders by (unit, id) — the first exact hit is stable
		}
	}
	if card == nil {
		return ec, ErrExampleUngrounded
	}

	// The sample rides whenever it can: a failed sample read degrades the
	// prompt's vocabulary steering (rendered 暂无) but never blocks the
	// sentence — the textbook background alone still grounds it.
	if eligible, err := r.Cards.List(ctx, database.CardFilter{
		Category:          exampleCategoryEnglish,
		ParticipatingOnly: true,
		ScopeEligibleOnly: true,
	}); err == nil {
		for _, c := range eligible {
			if c.Word == w.Word {
				continue
			}
			if len(ec.LearnedWords) >= maxLearnedSampleWords {
				break
			}
			ec.LearnedWords = append(ec.LearnedWords, c.Word)
		}
	}

	assignments, err := r.Textbooks.AssignmentsByCardIDs(ctx, []int64{card.ID})
	if err != nil {
		return ec, errors.Wrap(err, "rt/example: read assignments")
	}
	type pick struct {
		book    database.Textbook
		lesson  string
		learned bool
	}
	picks := make([]pick, 0, len(assignments[card.ID]))
	for _, a := range assignments[card.ID] {
		book, err := r.Textbooks.Get(ctx, a.TextbookID)
		if err != nil {
			continue // dangling attribution: skip, the rest still compete
		}
		learned := false
		if n := book.LearnedLessons; n > 0 {
			if n > len(book.Lessons) {
				n = len(book.Lessons) // defensive: validated at write, clamp on drift
			}
			for i := 0; i < n; i++ {
				if book.Lessons[i] == a.Lesson {
					learned = true
					break
				}
			}
		}
		picks = append(picks, pick{book: book, lesson: a.Lesson, learned: learned})
	}
	// Deterministic preference: a source whose lesson is already learned
	// (that is why the word reached the classroom), the primary book, then
	// stable id order.
	pickScore := func(p pick) int {
		score := 0
		if p.learned {
			score += 2
		}
		if p.book.IsPrimary {
			score++
		}
		return score
	}
	sort.SliceStable(picks, func(i, j int) bool {
		if si, sj := pickScore(picks[i]), pickScore(picks[j]); si != sj {
			return si > sj
		}
		return picks[i].book.ID < picks[j].book.ID
	})
	if len(picks) == 0 {
		return ec, nil // honest supplementary: no attribution at all
	}
	best := picks[0]
	ec.HasTextbook = true
	ec.Edition, ec.Stage, ec.Grade = best.book.Edition, best.book.Stage, best.book.Grade
	ec.Volume, ec.Module = best.book.Volume, best.book.Module
	ec.LessonName = best.lesson
	ec.LearnedLessons = best.book.LearnedLessons
	return ec, nil
}

// exampleKind carries which classroom intent asked for the sentence — the
// two intents share one prompt builder with a different task line.
type exampleKind uint8

const (
	exampleKindMeaning exampleKind = iota // 问词义：先简释词义，再给例句
	exampleKindExample                    // 求例句：直接给例句
)

// exampleSystemPrompt is the generation persona: the tutor persona and the
// child-safety wording carried over VERBATIM from buildClassifySystemPrompt,
// then the on-demand sentence task and its strict output contract.
var exampleSystemPrompt = `你是一位亲切、耐心的小学听写家教老师，正在通过语音陪学生完成听写练习。回应要简短、自然、温暖，适合小学生：写对了就真诚表扬，写错了就耐心鼓励再试一次。严禁任何羞辱、恐吓、惩罚性或让孩子难过的表述。
课堂节奏由服务端负责，你现在只帮学生理解当前这个词，不能推进课程。
请依据给出的已确认释义和教材背景，先用一两句简单中文讲清这个词的意思，再给一个用到该词的简单英语例句。
例句要短，优先只使用「已学词汇」列表里的词和最基础的常见词；例句必须贴合已确认释义，不得改用别的词义，不要编造教材原文。
只输出一个 JSON 对象，不要输出任何其它内容：{"reply":"<说给学生听的一句完整回复>"}`

var exampleSystemPromptSentenceFirst = strings.Replace(exampleSystemPrompt,
	"先用一两句简单中文讲清这个词的意思，再给一个用到该词的简单英语例句。",
	"直接给一个用到该词的简单英语例句，前面可以带一句极简的中文点题。", 1)

// buildExampleMessages assembles the on-demand generation request from the
// snapshot word and the resolved context. Pure — unit tests pin that the
// prompt carries the confirmed meaning, the textbook metadata (unregistered
// fields omitted), the learned-lessons scale and the learned-word sample,
// and never a region (ADR-0006 #3, US-19).
func buildExampleMessages(kind exampleKind, word, meaning string, ec ExampleContext) []mimo.Message {
	var b strings.Builder
	fmt.Fprintf(&b, "词：%s\n", word)
	fmt.Fprintf(&b, "已确认释义：%s\n", meaning)
	b.WriteString("教材背景：")
	if ec.HasTextbook {
		meta := []string{}
		fields := []struct{ label, value string }{
			{"版本", ec.Edition},
			{"学段", ec.Stage},
			{"年级", ec.Grade},
			{"册", ec.Volume},
			{"模块", ec.Module},
		}
		for _, f := range fields {
			if v := strings.TrimSpace(f.value); v != "" {
				meta = append(meta, f.label+" "+v)
			}
		}
		if len(meta) > 0 {
			b.WriteString(strings.Join(meta, " · "))
		} else {
			b.WriteString("有教材归属但版本信息未登记")
		}
	} else {
		b.WriteString("无教材归属，这是一个课外补充词汇")
	}
	b.WriteString("。\n")
	if ec.LessonName != "" {
		fmt.Fprintf(&b, "词的课次：%s\n", ec.LessonName)
	}
	b.WriteString("学习进度：")
	if ec.HasTextbook && ec.LearnedLessons > 0 {
		fmt.Fprintf(&b, "这本教材已学完第 %d 课（含第 %d 课）", ec.LearnedLessons, ec.LearnedLessons)
	} else if ec.HasTextbook {
		b.WriteString("这本教材暂无已学课次")
	} else {
		b.WriteString("暂无教材课次记录")
	}
	b.WriteString("。\n已学词汇（例句优先只用这些词和最基础的常见词）：")
	if len(ec.LearnedWords) > 0 {
		b.WriteString(strings.Join(ec.LearnedWords, ", "))
	} else {
		b.WriteString("暂无记录，只用最基础的常见词")
	}
	b.WriteString("\n")

	system := exampleSystemPrompt
	if kind == exampleKindExample {
		system = exampleSystemPromptSentenceFirst
	}
	return []mimo.Message{
		{Role: "system", Content: system},
		{Role: "user", Content: b.String()},
	}
}

// parseExampleReply extracts the kid-facing reply from the generation
// output: lenient about code fences and surrounding prose, strict about the
// JSON object and its string value (ADR-0006 #5 — no bare-text rescue; an
// unusable reply falls back to the classification wording instead of
// risking prompt junk reaching the child).
func parseExampleReply(text string) (string, bool) {
	s := strings.TrimSpace(text)
	if s == "" {
		return "", false
	}
	if strings.HasPrefix(s, "```") {
		if _, rest, ok := strings.Cut(s, "\n"); ok {
			s = strings.TrimSpace(rest)
		}
		s = strings.TrimSpace(strings.TrimSuffix(s, "```"))
	}
	i, j := strings.Index(s, "{"), strings.LastIndex(s, "}")
	if i < 0 || j <= i {
		return "", false
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(s[i:j+1]), &m); err != nil {
		return "", false
	}
	for _, key := range []string{"reply", "sentence"} {
		if v, ok := m[key].(string); ok {
			v = strings.TrimSpace(v)
			if v != "" && utf8.RuneCountInString(v) <= maxExampleReplyRunes {
				return v, true
			}
			return "", false
		}
	}
	return "", false
}

// exampleReply decides the FINAL spoken reply for a meaning/example intent
// turn in the dictation stage, running the on-demand generation when the
// word grounds one. classifyReply is the classification call's reply — the
// honest fallback whenever generation does not run or fails. The returned
// ok=false means "interrupted mid-flight": the caller must abort the turn
// silently, never speak (the processTurn post-classification discipline).
func (tm *TurnManager) exampleReply(ctx context.Context, epoch uint64, intent string, target TurnTarget, classifyReply string) (string, bool) {
	emit := func(result string) {
		tm.recorder.Emit(EventExampleServed,
			infra.F("word_index", target.WordIndex),
			infra.F("result", result),
		)
	}
	word := lesson.Word{}
	tm.mu.Lock()
	if tm.tutor != nil && target.WordIndex >= 0 && target.WordIndex < len(tm.tutor.words) {
		word = tm.tutor.words[target.WordIndex]
	}
	tm.mu.Unlock()

	if word.Meaning == "" {
		// No confirmed meaning to ground on: the classification reply
		// already carries the fixed honest wording — never invent (AC #5).
		emit(exampleResultNoMeaning)
		return classifyReply, true
	}
	if word.Example != "" {
		// The textbook's own example exists: the classification reply
		// quotes it verbatim — a generated sentence never replaces it.
		emit(exampleResultStored)
		return classifyReply, true
	}
	if tm.cfg.Examples == nil {
		emit(exampleResultFallback) // degraded wiring, honest fallback
		return classifyReply, true
	}

	// Context resolution rides the turn ctx (an interrupt cancels the read
	// itself) under its own read bound, and only a CURRENT epoch may
	// continue to the LLM call.
	readCtx, cancelRead := context.WithTimeout(ctx, exampleContextTimeout)
	ec, err := tm.cfg.Examples.ExampleContext(readCtx, word)
	cancelRead()
	if err != nil {
		if !tm.epochCurrent(epoch) {
			return "", false // interrupted mid-read: die silently
		}
		// Ungrounded (no English card) or a read failure: either way
		// nothing grounds a sentence — speak the classification fallback.
		emit(exampleResultFallback)
		return classifyReply, true
	}

	msgs := buildExampleMessages(exampleKindOf(intent), word.Word, word.Meaning, ec)
	resp, err := tm.llm.Chat(ctx, msgs,
		mimo.WithThinkingDisabled(), mimo.WithMaxTokens(exampleMaxTokens))
	if err != nil {
		if !tm.epochCurrent(epoch) {
			return "", false // interrupted mid-call: die silently
		}
		emit(exampleResultFallback) // one attempt, no retry (ADR-0006 #2)
		return classifyReply, true
	}
	if !tm.epochCurrent(epoch) {
		return "", false // late reply: never explain another moment's word
	}
	reply, ok := parseExampleReply(resp.Text())
	if !ok {
		emit(exampleResultFallback) // unparseable/bloated output, honest fallback
		return classifyReply, true
	}
	emit(exampleResultGenerated)
	return reply, true
}

// exampleKindOf maps the classroom intent onto the prompt task line.
func exampleKindOf(intent string) exampleKind {
	if intent == intentExample {
		return exampleKindExample
	}
	return exampleKindMeaning
}
