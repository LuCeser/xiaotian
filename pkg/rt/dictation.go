// dictation.go — the server-side dictation-tutor state machine (13-08, the
// 2026-10-03 pivot). The FSM owns classroom pacing: FSRS due words (≤10) →
// teacher opening announcement → per-word confirm (or 13-09 timeout) →
// advance through the idempotent lesson executor → wrap-up summary plus the
// review write. The LLM's job shrinks to intent classification and wording;
// it never advances, completes, or narrates progress on its own authority
// (STATE.md Phase 13 decision; the 13-04 tool loop is deleted).
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

// dueWordsLimit caps one due fetch: at most 10 words per session; fewer when
// FSRS holds fewer due words; zero means the graceful no-due ending.
const dueWordsLimit = 10

// WordSource is the due-word seam between the dictation FSM and FSRS.
//
//   - The production implementation is the 13-10 pybridge adapter (RunDue /
//     RunReview over the python vocab_manager); tests inject fakes.
//   - A nil WordSource in TurnManagerConfig means the classroom cannot
//     start: session_init answers lesson_unavailable (the honest degraded
//     state until 13-10 wires the adapter).
type WordSource interface {
	// FetchDueWords returns up to limit due vocabulary cards. Zero cards is
	// a legal result (no due words today), not an error.
	FetchDueWords(ctx context.Context, limit int) ([]lesson.Word, error)
	// SubmitReview writes one review batch (word → good/hard/again/easy
	// rating, the pybridge ReviewOpts.Results vocabulary) and reports what
	// ACTUALLY happened: partial and zero updates can accompany a nil
	// error, so callers must branch on the returned sets, never assume
	// full success from err == nil.
	SubmitReview(ctx context.Context, results map[string]string) (ReviewWriteResult, error)
}

// ReviewWriteResult is the actual outcome of one review write, reported as
// word sets: Requested is what was submitted, Updated what the backend
// really refreshed, Missing what it did not (already reviewed today, absent
// from the vocab store, ...). Partial/zero Updated with nil error is legal —
// the completion path reports the real counts instead of claiming success.
type ReviewWriteResult struct {
	Requested []string
	Updated   []string
	Missing   []string
}

// Intent vocabulary the classifier may return. Anything outside this
// whitelist is treated as chat (unknown intents never drive the FSM).
const (
	intentConfirm = "confirm" // 写完了 / 可以了 / 下一个
	intentRepeat  = "repeat"  // 再读一遍
	intentSlow    = "slow"    // 慢一点读
	intentHint    = "hint"    // 不会写，求提示
	intentMeaning = "meaning" // 问词义
	intentPause   = "pause"   // 要休息 / 暂停
	intentResume  = "resume"  // 要继续
	intentChat    = "chat"    // 其它对话
)

// intentWhitelist is the authoritative intent set; parseIntentJSON rejects
// everything else (T-13-13: the model output is untrusted input).
var intentWhitelist = map[string]bool{
	intentConfirm: true,
	intentRepeat:  true,
	intentSlow:    true,
	intentHint:    true,
	intentMeaning: true,
	intentPause:   true,
	intentResume:  true,
	intentChat:    true,
}

// Per-word outcome constants recorded in DictationTutor.perWordOutcome.
const (
	outcomeConfirmed = "confirmed" // student confirmed the word as written
	outcomeTimeout   = "timeout"   // 13-09 auto-advance after the word timer
	outcomeHint      = "hint"      // student asked for a hint and moved on
)

// outcomeRating maps internal outcomes to the pybridge ReviewOpts.Results
// rating vocabulary (good/hard/again/easy; R0 never emits easy).
//
// EXPLICIT ASSUMPTION (13-08 plan, 13-12 checkpoint): this mapping is an
// implementation choice pending user ratification at the 13-12 checkpoint —
// it must be confirmed before VPS formal acceptance.
var outcomeRating = map[string]string{
	outcomeConfirmed: "good",
	outcomeTimeout:   "hard",
	outcomeHint:      "again",
}

// Classification bounds: one Chat call per turn, thinking disabled, a tight
// token cap (the reply is one or two spoken sentences), and a short-history
// deque of the last 4 rounds for coherence.
const (
	classifyMaxTokens     = 200
	classifyHistoryRounds = 4
)

// classifyFallbackReply is the deterministic spoken fallback when the
// classification JSON cannot be parsed after its one retry (Task 2): the
// teacher asks again out loud — no user-visible ErrorMsg.
const classifyFallbackReply = "老师没听清，你再和我说一遍好吗？"

// lessonUnavailableNotice is the ONLY user-visible wording for classroom
// initialization failures (T-13-14): a fixed human phrase — python paths and
// internal detail stay in server-side structured logs.
const lessonUnavailableNotice = "取词失败了，请稍后再试"

// playbackAckTimeoutNotice is the fixed human wording for the
// playback_ack_timeout ErrorMsg (the code carries the machine meaning; the
// message stays honest and non-technical).
const playbackAckTimeoutNotice = "老师的话好像没有播放完，我们先休息一下吧。"

// TurnTarget is the immutable advance target captured under the manager lock
// BEFORE classification starts. A late confirm may only ever act on THIS
// index/version pair — never on a re-read currentIndex — so a delayed
// confirmation cannot double-advance or target a different word (T-13-15c).
type TurnTarget struct {
	WordIndex       int
	ExpectedVersion int
	Word            string
}

// historyRound is one round of the FSM short-history deque: the student's
// transcription and the teacher's reply (as actually heard — interrupt trim
// applies here, Pitfall 9 semantics on the deque instead of LLM history).
type historyRound struct {
	userText    string
	teacherText string
}

// DictationTutor holds the classroom-side state of one dictation lesson.
//
// SINGLE-LOCK DISCIPLINE: every field here is guarded by the owning
// TurnManager's mu — the tutor never locks on its own, and every method
// below assumes the caller already holds that lock.
type DictationTutor struct {
	lessonID      string
	words         []lesson.Word
	version       int
	currentIndex  int
	completed     bool
	perWordOutcome map[int]string
	shortHistory  []historyRound // capped deque, oldest dropped (last 4 rounds)
	// paused is the independent CLASSROOM pause bit (13-09): set by a
	// recognized pause command, cleared by a recognized resume. It is
	// deliberately NOT the voice state — the internal state may be
	// processing/speaking (the pause acknowledgement itself, or a student
	// utterance being classified), while the client-effective state stays
	// paused once playback settles (effectiveClientStateLocked). While
	// paused the per-word timer stays stopped and ordinary uplink keeps
	// flowing so the resume command is recognized through the real ASR /
	// classification path.
	paused bool
}

// newDictationTutor binds the FSM to a lesson row read back from the
// executor (resumable mid-lesson: currentIndex/version come from the row).
func newDictationTutor(l lesson.Lesson) *DictationTutor {
	return &DictationTutor{
		lessonID:       l.LessonID,
		words:          l.Cards,
		version:        l.Version,
		currentIndex:   l.CurrentIndex,
		perWordOutcome: map[int]string{},
	}
}

// currentWord returns the word card at the cursor.
func (t *DictationTutor) currentWord() lesson.Word {
	return t.words[t.currentIndex]
}

// turnTarget snapshots the immutable advance target for one classification.
func (t *DictationTutor) turnTarget() TurnTarget {
	return TurnTarget{
		WordIndex:       t.currentIndex,
		ExpectedVersion: t.version,
		Word:            t.words[t.currentIndex].Word,
	}
}

// align syncs the in-memory cursor to an externally observed state (the
// first committed result of an idempotent hit, or a conflict read-back).
func (t *DictationTutor) align(index, version int) {
	t.currentIndex = index
	t.version = version
}

// commitOutcome records the outcome for a word index (first commit only —
// callers gate on the AdvanceWord first-commit branch).
func (t *DictationTutor) commitOutcome(index int, outcome string) {
	t.perWordOutcome[index] = outcome
}

// markCompleted marks the lesson finished; the completion path runs once.
func (t *DictationTutor) markCompleted() {
	t.completed = true
}

// ratings maps the recorded outcomes to review ratings, keyed by word.
func (t *DictationTutor) ratings() map[string]string {
	out := make(map[string]string, len(t.perWordOutcome))
	for idx, outcome := range t.perWordOutcome {
		if idx < 0 || idx >= len(t.words) {
			continue
		}
		rating, ok := outcomeRating[outcome]
		if !ok {
			continue
		}
		out[t.words[idx].Word] = rating
	}
	return out
}

// outcomeCounts tallies the outcomes for the completion summary.
func (t *DictationTutor) outcomeCounts() (confirmed, timedOut, hinted int) {
	for _, outcome := range t.perWordOutcome {
		switch outcome {
		case outcomeConfirmed:
			confirmed++
		case outcomeTimeout:
			timedOut++
		case outcomeHint:
			hinted++
		}
	}
	return confirmed, timedOut, hinted
}

// appendUserRound appends the student's transcription as a new round,
// dropping the oldest beyond the 4-round cap.
func (t *DictationTutor) appendUserRound(text string) {
	t.shortHistory = append(t.shortHistory, historyRound{userText: text})
	if len(t.shortHistory) > classifyHistoryRounds {
		t.shortHistory = t.shortHistory[len(t.shortHistory)-classifyHistoryRounds:]
	}
}

// recordTeacherReply attaches an announcement's full text to the newest
// round (appending a teacher-only round for session-init announcements that
// have no student utterance before them).
func (t *DictationTutor) recordTeacherReply(text string) {
	if n := len(t.shortHistory); n > 0 && t.shortHistory[n-1].teacherText == "" {
		t.shortHistory[n-1].teacherText = text
		return
	}
	t.appendUserRound("")
	t.shortHistory[len(t.shortHistory)-1].teacherText = text
}

// trimTeacherReply applies interrupt trim (Pitfall 9) to the newest round:
// only the sentences the student actually heard survive. Zero-played removes
// the reply entirely — and a teacher-only round with it.
func (t *DictationTutor) trimTeacherReply(played []string) {
	n := len(t.shortHistory)
	if n == 0 {
		return
	}
	last := &t.shortHistory[n-1]
	if len(played) == 0 {
		if last.userText == "" {
			t.shortHistory = t.shortHistory[:n-1]
		} else {
			last.teacherText = ""
		}
		return
	}
	last.teacherText = strings.Join(played, "")
}

// effectiveClientStateLocked returns the student-facing connection state:
// the classroom pause bit overrides the voice state. The internal state may
// be processing/speaking during the pause acknowledgement (the ack speech
// itself plays), but once playback settles the student still sees paused —
// never a silent resume to listening. Caller holds tm.mu.
func (tm *TurnManager) effectiveClientStateLocked() string {
	if tm.tutor != nil && tm.tutor.paused {
		return StatePaused
	}
	return tm.state
}

// ---------------------------------------------------------------------------
// Per-word confirm countdown (13-09 Task 2): the 60s auto-advance of the
// turning decision. Arming happens ONLY from an ack-driven path — the final
// valid sentence_ack of a word announcement (announceAcked) or of a
// student-reply announcement (the repeat/slow/hint/meaning/chat/resume
// re-arm rule). TTS send completion and audio_end never arm; a playback
// ack timeout never fabricates a countdown start.
// ---------------------------------------------------------------------------

// armWordTimer (re)arms the per-word countdown for the CURRENT word.
// Arming first bumps timerGeneration, so any callback already queued under
// an older generation is dead on arrival — same epoch and word index
// included (T-13-16).
func (tm *TurnManager) armWordTimer() {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.armWordTimerLocked()
}

// armWordTimerLocked is the locked arm body. Caller holds tm.mu.
func (tm *TurnManager) armWordTimerLocked() {
	tm.timerGen++
	tm.stopTimerHandleLocked()
	if tm.tutor == nil || tm.tutor.completed || tm.tutor.paused {
		// No classroom, lesson finished, or paused: no countdown. The
		// generation bump above still invalidates any queued callback.
		return
	}
	gen := tm.timerGen
	epoch := tm.epoch
	target := tm.tutor.turnTarget()
	factory := tm.timerFactory
	if factory == nil {
		factory = time.AfterFunc
	}
	tm.wordTimer = factory(tm.cfg.WordTimeout, func() {
		tm.onWordTimeout(gen, epoch, target)
	})
}

// stopWordTimer suspends the countdown (the pause command, session close).
// The generation bump kills every callback queued under the old handle.
func (tm *TurnManager) stopWordTimer() {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.stopWordTimerLocked()
}

// stopWordTimerLocked is the locked stop body. Caller holds tm.mu.
func (tm *TurnManager) stopWordTimerLocked() {
	tm.timerGen++
	tm.stopTimerHandleLocked()
}

// stopTimerHandleLocked drops the live handle without touching the
// generation. Caller holds tm.mu.
func (tm *TurnManager) stopTimerHandleLocked() {
	if tm.wordTimer != nil {
		tm.wordTimer.Stop()
		tm.wordTimer = nil
	}
}

// onWordTimeout is the countdown firing (its own goroutine). It re-acquires
// the lock and validates in order: generation, session close, epoch, tutor
// liveness, the pause bit, and the FULL TurnTarget (word/index/version — a
// late confirm that already advanced bumps the version, so a stale target
// mismatches). The processing branch is handled BEFORE the generic
// state-rejection drop: a student reply being classified must not swallow
// the timeout — that firing is discarded and a fresh countdown re-armed.
// speaking is ack-driven and never advances early; anything else is
// dropped.
func (tm *TurnManager) onWordTimeout(gen uint64, epoch uint64, target TurnTarget) {
	tm.mu.Lock()
	if gen != tm.timerGen || tm.closed || tm.epoch != epoch ||
		tm.tutor == nil || tm.tutor.completed || tm.tutor.paused {
		tm.mu.Unlock()
		return
	}
	if tm.tutor.turnTarget() != target {
		tm.mu.Unlock()
		return
	}
	switch tm.state {
	case StateProcessing:
		// Mid-classification: discard this firing and re-arm a fresh
		// countdown — this round's reply ack re-arms again anyway.
		tm.armWordTimerLocked()
		tm.mu.Unlock()
	case StateListening:
		s := tm.sender
		tm.mu.Unlock()
		tm.timeoutAdvance(s, epoch, target)
	default:
		// speaking (or the transient ""): the announcement's ack path
		// re-arms; no early advance.
		tm.mu.Unlock()
	}
}

// timeoutAdvance executes the timeout's auto-advance: the SAME idempotency
// key and optimistic lock as confirm (`lessonID-advance-<index>`), so the
// confirm/timeout race resolves through the executor — the loser replays
// Idempotent=true and aligns with zero side effects (only the first driver
// writes the outcome, messages, narration and review).
func (tm *TurnManager) timeoutAdvance(s Sender, epoch uint64, target TurnTarget) {
	if s == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	tm.mu.Lock()
	tm.turnCancel = cancel
	tm.mu.Unlock()
	defer cancel()

	tm.advanceTarget(s, ctx, cancel, epoch, target, outcomeTimeout, "", func() {
		// EventTimeoutFired is emitted by the FIRST COMMIT ONLY: a timeout
		// that lost the race to confirm is an idempotent replay and never
		// claims it advanced anything.
		tm.recorder.Emit(EventTimeoutFired,
			infra.F("word_index", target.WordIndex),
			infra.F("epoch", epoch),
		)
	})
}

// ---------------------------------------------------------------------------
// Template wording (pure functions — the opening/announcement/summary path
// makes ZERO LLM calls, so first-audible latency is FSRS fetch + TTS first
// packet only; RT-04 structure)
// ---------------------------------------------------------------------------

// greetingText builds the opening announcement: a short warm hello plus the
// FIRST word's announcement. The teacher speaks immediately after
// session_ready — this is the first-greeting defect fix (首问候缺失).
func greetingText(wordCount int, firstWord string) string {
	if wordCount == 1 {
		return fmt.Sprintf("你好呀！今天我们只有一个词要写。听好哦：第 1 个词，%s。", firstWord)
	}
	return fmt.Sprintf("你好呀！今天我们有 %d 个词要写。听好哦：第 1 个词，%s。", wordCount, firstWord)
}

// announceText announces one word at its 1-based position.
func announceText(idx, total int, word string) string {
	return fmt.Sprintf("第 %d 个词，%s。", idx, word)
}

// completionSummaryText builds the wrap-up summary: honest counts, short
// spoken sentences, encouraging wording only (child-safety prohibition).
func completionSummaryText(total, confirmed, timedOut, hinted int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "今天一共写了 %d 个词。", total)
	switch {
	case confirmed == total && total > 0:
		b.WriteString("全部都写完了，你真棒！")
	case confirmed > 0:
		fmt.Fprintf(&b, "自己写完了 %d 个，很厉害！", confirmed)
	}
	if hinted > 0 {
		fmt.Fprintf(&b, "有 %d 个词看了提示，多练几次就更牢啦。", hinted)
	}
	if timedOut > 0 {
		fmt.Fprintf(&b, "有 %d 个词是超时跳过的，下次遇到了别紧张。", timedOut)
	}
	return b.String()
}

// noDueWordsText explains the zero-due-words ending.
func noDueWordsText() string {
	return "今天没有要到期的单词，你真棒，好好休息，我们下次再练吧。"
}

// reviewFailedSuffix is appended to the summary when the review write
// actually failed: the record was NOT saved and the student hears that —
// never a false "saved" (transparency prohibition).
func reviewFailedSuffix() string {
	return "不过今天的学习记录没有保存成功，记得下次告诉老师哦。"
}

// ---------------------------------------------------------------------------
// Intent reply templates (13-09 Task 1 — the four command templates). Pure
// functions like the opening/summary wording above: deterministic spoken
// behavior with zero LLM calls on the template path.
// ---------------------------------------------------------------------------

// repeatText re-reads the current word once, warmly.
func repeatText(word string) string {
	return fmt.Sprintf("好，我再读一遍。%s。", word)
}

// slowReadText repeats the word twice with full-stop pauses so the student
// can keep up. PROHIBITION (plan #1, the dictation leak ban): it NEVER
// spells the word out character by character — no pinyin, no per-character
// assembly of the full answer; a pause between whole-word readings is the
// entire "slowness".
func slowReadText(word string) string {
	return fmt.Sprintf("%s。%s。慢慢写，不着急。", word, word)
}

// pauseAckText confirms the pause command.
func pauseAckText() string {
	return "好，我们先休息一会儿，想继续的时候告诉老师哦。"
}

// resumeAckText confirms the resume at the SAME word position (idx is the
// 1-based position, matching announceText's convention).
func resumeAckText(idx int, word string) string {
	return fmt.Sprintf("好，我们继续。还是第 %d 个词，%s，慢慢写。", idx, word)
}

// confirmPausedRejectText gently declines a confirm while the classroom is
// paused: pacing stays suspended — no advance, no lesson_update.
func confirmPausedRejectText() string {
	return "我们还在休息呢，想继续的时候说继续，老师再陪你写这个词。"
}

// resolveLessonID derives the R0 lesson id: the configured id when present,
// otherwise a Beijing-calendar "r0-YYYYMMDD" (time.FixedZone, no tzdata
// dependency). Same-day reconnects derive the same id, so EnsureLessonCards'
// first-content-wins resumes the same lesson.
func resolveLessonID(configured string, now time.Time) string {
	if configured != "" {
		return configured
	}
	cst := time.FixedZone("CST", 8*3600)
	return "r0-" + now.In(cst).Format("20060102")
}

// ---------------------------------------------------------------------------
// Intent classification (LLM responsibility #1; wording comes back as
// #2's reply field). One Chat call per turn — never a tool loop.
// ---------------------------------------------------------------------------

// buildClassifySystemPrompt assembles the classification system prompt:
//   - the tutor persona with the child-safety wording prohibition carried
//     over VERBATIM from the 13-04 teacherSystemPrompt;
//   - the eight-value intent vocabulary with one-line definitions;
//   - the word ground truth (current word, meaning, example — either may be
//     absent, and the prompt says so honestly);
//   - the strict output contract, including the hint (never leak the full
//     word) and meaning (never invent; fall back to the honest fixed reply
//     when no ground truth exists) reply rules.
func buildClassifySystemPrompt(word lesson.Word, index, total int) string {
	var b strings.Builder
	b.WriteString(`你是一位亲切、耐心的小学听写家教老师，正在通过语音陪学生完成听写练习。回应要简短、自然、温暖，适合小学生：写对了就真诚表扬，写错了就耐心鼓励再试一次。严禁任何羞辱、恐吓、惩罚性或让孩子难过的表述。
课堂节奏由服务端负责，你不能推进课程，只判断学生这句话的意图并给出回应。
`)
	fmt.Fprintf(&b, "当前是第 %d 个词（共 %d 个）：“%s”。\n", index+1, total, word.Word)
	if word.Meaning != "" {
		fmt.Fprintf(&b, "这个词的释义：%s。\n", word.Meaning)
	} else {
		b.WriteString("老师这边暂无这个词的释义。\n")
	}
	if word.Example != "" {
		fmt.Fprintf(&b, "这个词的例句：%s。\n", word.Example)
	} else {
		b.WriteString("老师这边暂无这个词的例句。\n")
	}
	b.WriteString(`意图词表（intent 只能是以下八个值之一）：
confirm：学生表示写完了/可以了/下一个
repeat：再读一遍
slow：慢一点读
hint：不会写，求提示
meaning：问词义
pause：要休息/暂停
resume：要继续
chat：其它对话
输出契约：只输出一个 JSON 对象，不要输出任何其它内容：{"intent":"<意图>","reply":"<给学生听的回复>"}
reply 只有 intent 为 hint/meaning/chat 时才需要，其它意图 reply 给空字符串。
hint 的 reply 只能给部分提示（字形、部首、读音或例句线索），严禁给出整词的写法或完整拼音，不能泄题。
meaning 的 reply 只能依据上面给出的释义与例句组织；两者都没有时，reply 固定原样输出：老师这边没有这个词的释义，你凭印象写吧。`)
	return b.String()
}

// intentPayload is the strict classification JSON shape.
type intentPayload struct {
	Intent string `json:"intent"`
	Reply  string `json:"reply"`
}

// extractJSON extracts the first balanced braces block from text (models
// occasionally wrap JSON in prose or code fences).
func extractJSON(text string) (string, bool) {
	start := strings.Index(text, "{")
	if start < 0 {
		return "", false
	}
	depth := 0
	for i := start; i < len(text); i++ {
		switch text[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return text[start : i+1], true
			}
		}
	}
	return "", false
}

// parseIntentJSON parses the classification output and enforces the intent
// whitelist. Any failure reports ok=false (the caller retries, then falls
// back to chat).
func parseIntentJSON(text string) (intentPayload, bool) {
	block, ok := extractJSON(text)
	if !ok {
		return intentPayload{}, false
	}
	var p intentPayload
	if err := json.Unmarshal([]byte(block), &p); err != nil {
		return intentPayload{}, false
	}
	if !intentWhitelist[p.Intent] {
		return intentPayload{}, false
	}
	return p, true
}

// buildClassifyMessages assembles the classification request: the system
// prompt (persona + intent vocabulary + word ground truth + output
// contract) plus one user message carrying the transcript and the FSM
// short-history deque (last 4 rounds), so chat/hint/meaning stay coherent.
func (tm *TurnManager) buildClassifyMessages(transcript string, target TurnTarget) []mimo.Message {
	var user strings.Builder
	user.WriteString("学生说：")
	user.WriteString(transcript)
	tm.mu.Lock()
	total := 0
	var card lesson.Word
	if tm.tutor != nil {
		total = len(tm.tutor.words)
		fmt.Fprintf(&user, "\n（当前进度：第 %d 个词，共 %d 个）", tm.tutor.currentIndex+1, total)
		if target.WordIndex >= 0 && target.WordIndex < total {
			card = tm.tutor.words[target.WordIndex]
		}
		if len(tm.tutor.shortHistory) > 0 {
			user.WriteString("\n最近对话：")
			for _, r := range tm.tutor.shortHistory {
				if r.userText != "" {
					fmt.Fprintf(&user, "\n学生：%s", r.userText)
				}
				if r.teacherText != "" {
					fmt.Fprintf(&user, "\n老师：%s", r.teacherText)
				}
			}
		}
	}
	tm.mu.Unlock()
	system := buildClassifySystemPrompt(card, target.WordIndex, total)
	return []mimo.Message{
		{Role: "system", Content: system},
		{Role: "user", Content: user.String()},
	}
}

// classifyTurn runs the per-turn intent classification: ONE Chat call, and
// on an unusable output (unparseable JSON, missing/non-whitelisted intent)
// exactly ONE correction retry — the same conversation plus a user-level
// "JSON only" nudge. A second failure collapses deterministically to chat
// with the fixed fallback wording: the teacher asks again out loud (no
// user-visible ErrorMsg), while the server records the provider failure.
func (tm *TurnManager) classifyTurn(ctx context.Context, transcript string, target TurnTarget) (string, string) {
	msgs := tm.buildClassifyMessages(transcript, target)
	intent, reply, ok := tm.classifyOnce(ctx, msgs)
	if !ok {
		msgs = append(msgs, mimo.Message{
			Role:    "user",
			Content: "你刚才的输出不符合要求。请只输出一个 JSON 对象，键为 intent 与 reply，不要输出任何其它内容。",
		})
		intent, reply, ok = tm.classifyOnce(ctx, msgs)
	}
	if !ok {
		intent, reply = intentChat, classifyFallbackReply
		tm.recorder.Emit(EventError, infra.F("code", ErrCodeProviderFailed))
	}
	// Desensitization boundary (T-13-06b): the intent label only — never
	// the transcript text — reaches the event stream.
	tm.recorder.Emit(EventIntentClassified, infra.F("intent", intent))
	return intent, reply
}

// classifyOnce runs one Chat hop and parses its strict JSON output.
func (tm *TurnManager) classifyOnce(ctx context.Context, msgs []mimo.Message) (intent, reply string, ok bool) {
	resp, err := tm.llm.Chat(ctx, msgs,
		mimo.WithThinkingDisabled(), mimo.WithMaxTokens(classifyMaxTokens))
	if err != nil {
		return "", "", false
	}
	p, ok := parseIntentJSON(resp.Text())
	if !ok {
		return "", "", false
	}
	return p.Intent, p.Reply, true
}
