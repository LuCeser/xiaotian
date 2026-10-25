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
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/xiaotian/home-tutor-agent/pkg/grading"
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
//
// Category (Issue #10): both methods take the classroom's subject
// ("english"/"chinese") so the due fetch and the review write scope to the
// SAME vocabulary category the session was opened under — the fetch never
// hands the 语文 classroom English words, and the review never lands a
// Chinese word's history on the English column (the script's --category
// default is english; omitting it was the AC4 defect this seam closes).
type WordSource interface {
	// FetchDueWords returns up to limit due vocabulary cards in category.
	// Zero cards is a legal result (no due words today), not an error.
	FetchDueWords(ctx context.Context, limit int, category string) ([]lesson.Word, error)
	// PeekDueWords returns up to limit due vocabulary cards in category
	// WITHOUT taking the fetch lock (Issue #55: the auto subject
	// resolution's presence probe — reading must never lock cards away from
	// a live classroom, so it rides the script's --peek). Zero cards and
	// errors degrade the resolution's input, never the classroom itself.
	PeekDueWords(ctx context.Context, limit int, category string) ([]lesson.Word, error)
	// SubmitReview writes one review batch in category (word →
	// good/hard/again/easy rating, the pybridge ReviewOpts.Results
	// vocabulary) and reports what ACTUALLY happened: partial and zero
	// updates can accompany a nil error, so callers must branch on the
	// returned sets, never assume full success from err == nil.
	SubmitReview(ctx context.Context, results map[string]string, category string) (ReviewWriteResult, error)
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
	intentExample = "example" // 求例句 / 问这个词怎么用（Issue #9, ADR-0006）
	intentPause   = "pause"   // 要休息 / 暂停
	intentResume  = "resume"  // 要继续
	intentChat    = "chat"    // 其它对话
)

// Check-stage voice-correction intents (13.1-06, D-06②): the spoken
// per-word verdict commands. They ride the SAME whitelist — parseIntentJSON
// stays stage-agnostic — and only the checking stage's dispatcher consumes
// them (dictation-stage prompts never offer them, so the model has no
// reason to emit one there; the check-stage prompt is a separate build,
// Pitfall 8).
const (
	intentCheckRight    = "check_right"    // 第 N 个词对了
	intentCheckWrong    = "check_wrong"    // 第 N 个词错了
	intentCheckSpelling = "check_spelling" // 写的是 aple（口述实际写法）
)

// intentWhitelist is the authoritative intent set; parseIntentJSON rejects
// everything else (T-13-13: the model output is untrusted input). TWELVE
// values: the nine dictation intents plus the three check-stage voice
// corrections.
var intentWhitelist = map[string]bool{
	intentConfirm:       true,
	intentRepeat:        true,
	intentSlow:          true,
	intentHint:          true,
	intentMeaning:       true,
	intentExample:       true,
	intentPause:         true,
	intentResume:        true,
	intentChat:          true,
	intentCheckRight:    true,
	intentCheckWrong:    true,
	intentCheckSpelling: true,
}

// Per-word outcome constants recorded in DictationTutor.perWordOutcome.
const (
	outcomeConfirmed = "confirmed" // student confirmed the word as written
	outcomeTimeout   = "timeout"   // 13-09 auto-advance after the word timer
	outcomeHint      = "hint"      // student asked for a hint and moved on
)

// Review rating vocabulary submitted in the pybridge ReviewOpts.Results
// map (good/hard/again/easy; R0 never emits easy).
const (
	ratingGood  = "good"
	ratingHard  = "hard"
	ratingAgain = "again"
)

// wordRating derives one word's FSRS rating from its recorded outcome and
// its hint count — the SINGLE reference point of the rating matrix.
//
// The matrix is the user's 2026-10-06 ruling (U-9; 13-CHECKPOINTS 评级映射
// 追认段): what matters is whether the word was written correctly AFTER a
// hint —
//
//   - confirmed,  no hint        → good  (wrote it unaided)
//   - confirmed,  after hint(s)  → hard  (needed help but finished)
//   - timeout,    no hint        → hard  (ran out of time, no help asked)
//   - timeout,    after hint(s)  → again (unfinished even with help)
//   - hint pre-record never advanced (lesson ended on this word) → again
//   - anything else → "" (ratings() skips the word)
//
// Phase 13.1 single reference point: the UNVERIFIED fallback path (photo
// check absent) MUST rate through this function — 13.1 must not fork a
// second matrix. The "没听清提示/多音字" human-synthesis wording stays a
// 13.1 concern; this (outcome, hints) signature is the entire reserved seam
// for it. No 13.1 functionality lives here.
func wordRating(outcome string, hints int) string {
	switch outcome {
	case outcomeConfirmed:
		if hints > 0 {
			return ratingHard
		}
		return ratingGood
	case outcomeTimeout:
		if hints > 0 {
			return ratingAgain
		}
		return ratingHard
	case outcomeHint:
		// Pre-recorded but never advanced: the lesson ended on this word
		// with only a hint request on record.
		return ratingAgain
	default:
		return ""
	}
}

// Check-stage result vocabulary (13.1-01, D-02): whether the written word
// was judged correct at check time — by the screen 对/错 tap in this plan,
// by the OCR+manual-correction loop from 13.1-03 on. checkRating is
// wordRating's SIBLING function sharing the ratingGood/ratingHard/
// ratingAgain constants (single matrix family, never a fork; same discipline
// as the wordRating comment above).
const (
	checkCorrect = "correct" // 检查对
	checkWrong   = "wrong"   // 检查错
)

// checkRating derives one word's FSRS rating from its CHECK result and its
// hint count — the D-02 unified-submit table (2026-10-06 裁定, 13.1-CONTEXT):
//
//   - checked correct, no hint        → good  (写对了，没要提示)
//   - checked correct, after hint(s)  → hard  (要了提示但写对了)
//   - checked wrong (any hints)       → again (检查结果优先于行为)
//   - anything else → "" (checkRatings skips the word, like ratings())
func checkRating(result string, hints int) string {
	switch result {
	case checkCorrect:
		if hints > 0 {
			return ratingHard
		}
		return ratingGood
	case checkWrong:
		return ratingAgain
	default:
		return ""
	}
}

// downgradeOneRating is the D-03 one-level degradation of the 13.1-04
// unverified-fallback path: a lesson closed before its check ran rates the
// student's BEHAVIOR one level below the matrix value — good→hard,
// hard→again, again floors at again, unknown stays "" (skipped). It is
// deliberately NOT a new matrix row: the input is always a value wordRating
// already produced (Pitfall 1 — never fork a second rating table).
func downgradeOneRating(r string) string {
	switch r {
	case ratingGood:
		return ratingHard
	case ratingHard:
		return ratingAgain
	case ratingAgain:
		return ratingAgain
	default:
		return ""
	}
}

// uncheckedRatings assembles the D-03 fallback batch from the STORED outcome
// snapshot (the lessons row is the authority — whatever the session recorded
// in memory after EnterCheck is ignored): per word,
// downgradeOneRating(wordRating(outcome, hints)). Words the matrix does not
// recognize are skipped, exactly like ratings()/checkRatings.
func uncheckedRatings(words []lesson.Word, outcomes map[string]lesson.WordOutcome) map[string]string {
	out := make(map[string]string, len(outcomes))
	for key, o := range outcomes {
		idx, err := strconv.Atoi(key)
		if err != nil || idx < 0 || idx >= len(words) {
			continue
		}
		rating := downgradeOneRating(wordRating(o.Outcome, o.Hints))
		if rating == "" {
			continue
		}
		out[words[idx].Word] = rating
	}
	return out
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
	Word            string
	WordIndex       int
	ExpectedVersion int
}

// historyRound is one round of the FSM short-history deque: the student's
// transcription and the teacher's reply (as actually heard — interrupt trim
// applies here, Pitfall 9 semantics on the deque instead of LLM history).
type historyRound struct {
	userText    string
	teacherText string
}

// dictationTurnContext is the immutable snapshot of the classroom's task
// state captured under tm.mu BEFORE a turn enters processing (Issue #56):
// what the classifier — and the full-phrase confirmation correction — may
// assume about the classroom as it stood when the child started speaking.
// It is a local per-turn view, never a second copy of classroom state:
// execution still re-checks the live epoch, close flag, and pause bit at
// dispatch time.
type dictationTurnContext struct {
	target TurnTarget
	turnID uint64
	epoch  uint64
	// awaitingWord: an ordinary WORD dictation in its writing stage — the
	// teacher announced the current word and the classroom is waiting for
	// the child to finish writing it. The ONLY state where #56's
	// completion-phrase correction may override the model's intent.
	awaitingWord bool
	paused       bool
	checking     bool
}

// captureDictationTurnContextLocked snapshots the turn's task context.
// Caller holds tm.mu; checking and target are the caller's turn-entry
// values (already computed under the same hold — the capture adds the
// stage bits the classifier needs, judging the classroom at turn START).
func (tm *TurnManager) captureDictationTurnContextLocked(turnID, epoch uint64, checking bool, target TurnTarget) dictationTurnContext {
	snap := dictationTurnContext{
		turnID:   turnID,
		epoch:    epoch,
		target:   target,
		checking: checking,
	}
	if tm.tutor == nil {
		return snap
	}
	snap.paused = tm.tutor.paused
	snap.awaitingWord = !checking && !tm.tutor.recitation() && !tm.tutor.paused &&
		!tm.tutor.completed &&
		target.WordIndex >= 0 && target.WordIndex < len(tm.tutor.words)
	return snap
}

// DictationTutor holds the classroom-side state of one dictation lesson.
//
// SINGLE-LOCK DISCIPLINE: every field here is guarded by the owning
// TurnManager's mu — the tutor never locks on its own, and every method
// below assumes the caller already holds that lock.
type DictationTutor struct {
	poem           *poemRef
	passage        *passageRef
	perWordHints   map[int]int
	perWordOutcome map[int]string
	lessonID       string
	category       string
	shortHistory   []historyRound
	words          []lesson.Word
	checkVerdicts  []lesson.CheckVerdict
	reviewResult   ReviewResultMsg
	currentIndex   int
	version        int
	paused         bool
	checking       bool
	completed      bool
	reviewSeen     bool
	correction     bool
	practice       bool
}

// recitation reports whether this classroom is a STRUCTURED 背默 task (a
// poem or a passage range — Issue #12/#14): the original must never reach
// the child before check_started, in any mode, and the writing stage
// classifies through its own prompt instead of the per-word pacing switch.
func (t *DictationTutor) recitation() bool {
	return t.poem != nil || t.passage != nil
}

// poemRef is the poem classroom's work identity (Issue #12): what the child
// is writing from memory. The lines themselves live in words (one line = one
// card, the check stage's word_index space); this ref carries only the
// display/save identity — title and author ride session_ready's PoemTaskView
// and the receipt's requested set.
type poemRef struct {
	Title  string
	Author string
	ID     int64
}

// passageRef is the passage classroom's work + range identity (Issue #14):
// what the child is writing from memory and WHICH range this attempt covers.
// The paragraphs themselves live in words (one paragraph = one card, the
// check stage's word_index space); Paragraphs here carries the ORIGINAL
// 1-based 自然段号 of those cards in the same order (display numbering —
// the SAVE identity stays whole + the canonical set, zeroed for whole by
// the submit path).
type passageRef struct {
	Title      string
	Author     string
	Paragraphs []int
	ID         int64
	ScopeID    int64
	Whole      bool
}

// newDictationTutor binds the FSM to a lesson row read back from the
// executor (resumable mid-lesson: currentIndex/version come from the row).
func newDictationTutor(l lesson.Lesson) *DictationTutor {
	return &DictationTutor{
		lessonID:       l.LessonID,
		category:       lesson.NormalizeCategory(l.Category),
		words:          l.Cards,
		version:        l.Version,
		currentIndex:   l.CurrentIndex,
		perWordOutcome: map[int]string{},
		perWordHints:   map[int]int{},
	}
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

// recordHint records one hint request for a word index: the per-word count
// increments and the outcome is PRE-RECORDED as hint (preserving the
// existing semantics — a word that never advances by session end still
// counts as hinted/again). The word's first confirm/timeout commit
// OVERWRITES the pre-recorded outcome BY DESIGN; the hint count lives in
// perWordHints and survives that overwrite — the final rating is
// wordRating(outcome, hints) at completion, so a hint is never lost from
// the rating (U-9, 2026-10-06). Returns the incremented count.
func (t *DictationTutor) recordHint(index int) int {
	t.perWordHints[index]++
	t.commitOutcome(index, outcomeHint)
	return t.perWordHints[index]
}

// markCompleted marks the lesson finished; the completion path runs once.
func (t *DictationTutor) markCompleted() {
	t.completed = true
}

// ratings maps the recorded outcomes to review ratings, keyed by word. The
// rating derives from wordRating(outcome, hints) — the U-9 matrix — and a
// word whose outcome the matrix does not recognize is skipped.
func (t *DictationTutor) ratings() map[string]string {
	out := make(map[string]string, len(t.perWordOutcome))
	for idx, outcome := range t.perWordOutcome {
		if idx < 0 || idx >= len(t.words) {
			continue
		}
		rating := wordRating(outcome, t.perWordHints[idx])
		if rating == "" {
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

// hintAccounting aggregates the per-word hint counts for the completion
// event (U-9, 2026-10-06): how many valid words received at least one hint,
// and the total number of hint requests across the lesson. Caller holds
// tm.mu (single-lock discipline).
func (t *DictationTutor) hintAccounting() (hintWords, hintTotal int) {
	for idx := range t.perWordOutcome {
		if idx < 0 || idx >= len(t.words) {
			continue
		}
		if t.perWordHints[idx] > 0 {
			hintWords++
		}
	}
	for _, n := range t.perWordHints {
		hintTotal += n
	}
	return hintWords, hintTotal
}

// snapshotCheckOutcomes flattens the recorded per-word behavior into the
// persisted WordOutcome map (keyed by word index string) the check stage
// stores at EnterCheck — the hint counts must survive into the unified
// rating (checkRating(result, hints)) even across a reconnect. Caller holds
// tm.mu (single-lock discipline).
func (t *DictationTutor) snapshotCheckOutcomes() map[string]lesson.WordOutcome {
	out := make(map[string]lesson.WordOutcome, len(t.words))
	for idx := range t.words {
		out[strconv.Itoa(idx)] = lesson.WordOutcome{
			Outcome: t.perWordOutcome[idx],
			Hints:   t.perWordHints[idx],
		}
	}
	return out
}

// restoreOutcomes rebuilds the in-memory behavior caches from the row's
// stored outcome snapshot — the rating input of the resume/correction
// sessions (13.1-04): perWordOutcome/perWordHints feed the fallback and the
// correction diff exactly as they fed EnterCheck's snapshot. Unknown indexes
// are skipped. Caller holds tm.mu (single-lock discipline).
func (t *DictationTutor) restoreOutcomes(outcomes map[string]lesson.WordOutcome) {
	for key, o := range outcomes {
		idx, err := strconv.Atoi(key)
		if err != nil || idx < 0 || idx >= len(t.words) {
			continue
		}
		t.perWordOutcome[idx] = o.Outcome
		t.perWordHints[idx] = o.Hints
	}
}

// resyncCheck aligns the in-memory check state to an externally observed
// lessons row (a conflict read-back or an executor replay): the cache always
// bends to the store. A correction session (13.1-04) keeps its panel open —
// the row's stage is 'submitted' by construction there. Caller holds tm.mu
// (single-lock discipline).
func (t *DictationTutor) resyncCheck(l lesson.Lesson) {
	t.version = l.Version
	t.checking = t.correction || l.Stage == lesson.StageChecking
	t.checkVerdicts = l.Verdicts
	if l.Stage == lesson.StageSubmitted {
		t.completed = true
	}
}

// maskedWords returns the STILL-MASKED word set for outbound masking: every
// word in the lesson, including already-written words. Once the check stage
// opens the set is empty: check_started already
// delivered the full plaintext list, and nothing re-masks after it (D-10).
// Caller holds tm.mu (single-lock discipline).
func (t *DictationTutor) maskedWords() []string {
	if t.checking {
		return nil
	}
	out := make([]string, 0, len(t.words))
	for i := 0; i < len(t.words); i++ {
		out = append(out, t.words[i].Word)
	}
	return out
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
// liveness, the pause bit, and the captured target's index+version (a late
// confirm that already advanced bumps the version, so a stale target
// mismatches). The target is compared by index+version, NEVER through
// turnTarget(): in the window between the last word's first-commit align and
// enterCheck's markCompleted/stop the cursor already sits at len(words) —
// turnTarget would index past the word list (the #56 最后一词越界 red line).
// The processing branch is handled BEFORE the generic state-rejection drop:
// a student reply being classified must not swallow the timeout — that
// firing is discarded and a fresh countdown re-armed. speaking is
// ack-driven and never advances early; anything else is dropped.
func (tm *TurnManager) onWordTimeout(gen uint64, epoch uint64, target TurnTarget) {
	tm.mu.Lock()
	if gen != tm.timerGen || tm.closed || tm.epoch != epoch ||
		tm.tutor == nil || tm.tutor.completed || tm.tutor.paused {
		tm.mu.Unlock()
		return
	}
	if tm.tutor.currentIndex != target.WordIndex || tm.tutor.version != target.ExpectedVersion {
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

// completionSummaryText RETIRED with the write-at-completion model
// (13.1-01, D-01/D-04): the wrap-up now speaks the CHECKED battle counts
// (checkedSummaryText in check.go) after the unified submit.

// noDueWordsText explains the zero-due-words ending — the no-data fallback
// when the vocabulary's next due is not presentable (no future due at all,
// or the Practice source is unavailable). The D-13 days variant lives in
// noDueWordsTextWithDays.
func noDueWordsText() string {
	return "今天没有要到期的单词，你真棒，好好休息，我们下次再练吧。"
}

// noDueWordsTextWithDays is the D-13 no-due copy variant that names the next
// review in whole days (nextDueDays computed from the vocabulary's fsrs due
// data). Two endings, two copies — never a shared template (D-13).
func noDueWordsTextWithDays(days int) string {
	return fmt.Sprintf("今天没有要到期的单词，下次复习大约在 %d 天后，好好休息，我们 %d 天后再练吧。", days, days)
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

// ---------------------------------------------------------------------------
// Issue #56: state-gated full-phrase confirmation correction. The model
// occasionally misreads a plain 「好了」 as repeat or chat, and a completion
// phrase must advance exactly once — so BEFORE dispatch, a small pure
// function corrects a FINITE table of unambiguous whole phrases. The gate
// (awaitingWord: ordinary word dictation, writing stage, unpaused,
// unfinished, valid target) is enforced by the caller; every other
// expression keeps the model's verdict — and a collapsed classification
// keeps its fixed spoken fallback, the exact-phrase table excepted (the
// transcript is the input, never the model output).
// ---------------------------------------------------------------------------

// isDictationPhraseEdge reports whether a rune may be stripped from the END
// of an utterance during phrase normalization: surrounding whitespace plus
// a finite set of STATEMENT terminators and soft pauses. Question marks are
// deliberately absent — 「好了？」 is a question, not a completion — and
// nothing inside the phrase is ever touched.
func isDictationPhraseEdge(r rune) bool {
	switch r {
	case '。', '！', '，', '、', '；', ',', '.', '!', ';', ' ', '\t', '\n', '\r', '　':
		return true
	}
	return unicode.IsSpace(r)
}

// normalizeDictationPhrase trims statement punctuation and whitespace from
// both ends of an utterance, leaving question marks, negations, and all
// interior structure intact.
func normalizeDictationPhrase(s string) string {
	return strings.TrimFunc(s, isDictationPhraseEdge)
}

// The finite whole-phrase tables (Issue #56). EXACT matches on the
// normalized phrase — never a substring, never fuzzy, never a homophone
// substitution. Compound utterances (「好了，再读一遍」「好了再叫你」) keep
// their interior structure and therefore never hit.
var (
	dictationConfirmPhrases = map[string]bool{
		"好了": true, "好嘞": true, "写好了": true, "写完了": true,
		"可以了": true, "下一个": true, "下一个词": true,
	}
	dictationRepeatPhrases  = map[string]bool{"再读一遍": true}
	dictationNotDonePhrases = map[string]bool{
		"还没好": true, "还没写好": true, "没写完": true, "不要下一个": true,
	}
	dictationPausePhrases = map[string]bool{"等一下": true}
)

// notDoneAckText is the fixed gentle reply to an explicit not-yet-done
// phrase: the classroom keeps waiting on the SAME word — no advance, no
// re-read, just patience.
func notDoneAckText() string {
	return "不着急，慢慢写，写好了告诉老师就行。"
}

// resolveDictationIntent corrects the model's verdict for the finite
// full-phrase table (Issue #56); everything else passes through untouched:
//
//   - completion phrases → confirm with the reply CLEARED — the state
//     machine alone narrates the next word, so a stale model reply must
//     never ride along (and a model misjudgement of repeat/chat cannot
//     swallow the advance);
//   - 再读一遍 → repeat (an explicit re-read outranks any affirmative
//     reading — the current word is re-read, the cursor never moves);
//   - explicit not-yet-done phrases → chat with the fixed gentle ack — a
//     model confirm here must NOT advance;
//   - 等一下 → pause, the existing pause handling (timer suspended, ack
//     line spoken).
//
// The caller gates this on awaitingWord, so the correction can only ever
// fire inside a live word-dictation writing stage.
func resolveDictationIntent(transcript, modelIntent, modelReply string) (string, string) {
	phrase := normalizeDictationPhrase(transcript)
	switch {
	case dictationConfirmPhrases[phrase]:
		return intentConfirm, ""
	case dictationRepeatPhrases[phrase]:
		return intentRepeat, ""
	case dictationNotDonePhrases[phrase]:
		return intentChat, notDoneAckText()
	case dictationPausePhrases[phrase]:
		return intentPause, ""
	}
	return modelIntent, modelReply
}

// ---------------------------------------------------------------------------
// Student-mode masked templates (13.1-02, D-10): the SCREEN copies of
// word-bearing announcements. Voice is the answer channel — TTS always gets
// the raw template — while the subtitle carries the masked variant so the
// spelling never reaches the wire. Beside the pure-function family above.
// ---------------------------------------------------------------------------

// maskWord is the FIXED masked placeholder for a not-yet-revealed word.
// Fixed length by design — a length-proportional mask would leak the word's
// own length, which is itself a hint.
func maskWord() string {
	return "•••"
}

// wordPositionText names a word by its 1-based position — the masked form
// teacher subtitles use (「第 N 个词」, 13.1 D-10).
func wordPositionText(idx int) string {
	return fmt.Sprintf("第 %d 个词", idx)
}

// greetingMaskedText mirrors greetingText with the first word's text dropped.
func greetingMaskedText(wordCount int) string {
	if wordCount == 1 {
		return "你好呀！今天我们只有一个词要写。听好哦：第 1 个词。"
	}
	return fmt.Sprintf("你好呀！今天我们有 %d 个词要写。听好哦：%s。", wordCount, wordPositionText(1))
}

// announceMaskedText announces one word's position only (「第 N 个词。」);
// total mirrors announceText's signature for call-site symmetry.
func announceMaskedText(idx, total int) string {
	return wordPositionText(idx) + "。"
}

// repeatMaskedText mirrors repeatText with the word masked to its position.
func repeatMaskedText(idx int) string {
	return fmt.Sprintf("好，我再读一遍。%s。", wordPositionText(idx))
}

// slowMaskedText mirrors slowReadText (the whole-word pause shape preserved,
// still zero spelling) with the word masked to its position.
func slowMaskedText(idx int) string {
	return fmt.Sprintf("%s。%s。慢慢写，不着急。", wordPositionText(idx), wordPositionText(idx))
}

// resumeMaskedText mirrors resumeAckText with the word masked to its
// position.
func resumeMaskedText(idx int) string {
	return fmt.Sprintf("好，我们继续。还是%s，慢慢写。", wordPositionText(idx))
}

// maskTranscript replaces every occurrence of a still-masked lesson word in
// text with maskWord(). Comparison goes through grading.Normalize — the
// SINGLE normalization authority (case folding + language-aware punctuation
// stripping; the language regime follows each word's script) — while the
// replacement lands on the ORIGINAL raw span, so the surrounding sentence
// keeps its exact characters. Callers pass only the STILL-MASKED word set:
// words already revealed (the check stage) are never masked again (D-10).
//
// The scan window per start position is bounded (normalized word length plus
// a small punctuation slack): normalization only ever REMOVES runes from a
// span, so a matching span cannot be much longer than the word itself.
func maskTranscript(text string, words []string) string {
	if text == "" || len(words) == 0 {
		return text
	}
	const maskWindowSlack = 8
	runes := []rune(text)
	type span struct{ start, end int }
	var spans []span
	for _, w := range words {
		if w == "" {
			continue
		}
		lang := "en"
		for _, r := range w {
			if unicode.Is(unicode.Han, r) {
				lang = "zh"
				break
			}
		}
		nw := grading.Normalize(w, lang)
		window := len([]rune(nw)) + maskWindowSlack
		if window <= 0 {
			continue
		}
		for i := 0; i < len(runes); i++ {
			matched := 0
			for j := i + 1; j <= len(runes) && j-i <= window; j++ {
				if grading.Normalize(string(runes[i:j]), lang) == nw {
					matched = j
					break
				}
			}
			if matched > 0 {
				spans = append(spans, span{i, matched})
				i = matched - 1 // the match consumes its span
			}
		}
	}
	if len(spans) == 0 {
		return text
	}
	sort.Slice(spans, func(a, b int) bool { return spans[a].start < spans[b].start })
	merged := []span{spans[0]}
	for _, s := range spans[1:] {
		last := &merged[len(merged)-1]
		if s.start <= last.end {
			if s.end > last.end {
				last.end = s.end
			}
			continue
		}
		merged = append(merged, s)
	}
	var b strings.Builder
	prev := 0
	for _, s := range merged {
		b.WriteString(string(runes[prev:s.start]))
		b.WriteString(maskWord())
		prev = s.end
	}
	b.WriteString(string(runes[prev:]))
	return b.String()
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

// chineseLessonIDSuffix qualifies a lesson id for the 语文 classroom
// (Issue #10). ONE daily lesson row per subject: an English classroom and a
// Chinese classroom on the same day (or under the same configured pinned id)
// must never share a row — first-content-wins would hand whichever subject
// initialized second the FIRST subject's words. The suffix keeps the r0-
// prefix family intact (lessonKind's r0-/r0x- classification keeps working)
// and stays inside the 64-rune lesson-id cap for every realistic id.
const chineseLessonIDSuffix = "-chinese"

// lessonIDForCategory scopes resolveLessonID's output to the classroom's
// subject: chinese qualifies the id (daily and configured alike); english —
// and any other value — keeps the legacy id byte-for-byte, so every
// existing row and resume path is untouched.
func lessonIDForCategory(configured string, now time.Time, category string) string {
	if lesson.NormalizeCategory(category) == lesson.CategoryChinese {
		return resolveLessonID(configured, now) + chineseLessonIDSuffix
	}
	return resolveLessonID(configured, now)
}

// ---------------------------------------------------------------------------
// Intent classification (LLM responsibility #1; wording comes back as
// #2's reply field). One Chat call per turn — never a tool loop.
// ---------------------------------------------------------------------------

// buildClassifySystemPrompt assembles the classification system prompt:
//   - the tutor persona with the child-safety wording prohibition carried
//     over VERBATIM from the 13-04 teacherSystemPrompt;
//   - the waiting context and the judgment rules (Issue #56): the teacher
//     has announced the current word and is waiting for the child to finish
//     writing it — natural completion phrases are confirm, while negation,
//     waiting, and re-read requests are NEVER completion;
//   - the nine-value intent vocabulary with one-line definitions;
//   - the word ground truth (current word, meaning, example — either may be
//     absent, and the prompt says so honestly);
//   - the strict output contract, including the hint (never leak the full
//     word) and meaning (never invent; fall back to the honest fixed reply
//     when no ground truth exists) reply rules.
func buildClassifySystemPrompt(word lesson.Word, index, total int) string {
	var b strings.Builder
	b.WriteString(`你是一位亲切、耐心的小学听写家教老师，正在通过语音陪学生完成听写练习。回应要简短、自然、温暖，适合小学生：写对了就真诚表扬，写错了就耐心鼓励再试一次。严禁任何羞辱、恐吓、惩罚性或让孩子难过的表述。
课堂节奏由服务端负责，你不能推进课程，只判断学生这句话的意图并给出回应。
现在的情形是：老师已经报完当前这个词，正在等学生把它写完。
判断规则（按此优先级）：
- 学生说“好了”“好嘞”“写好了”“写完了”“可以了”“下一个”“下一个词”，都表示当前这个词写完了，intent 是 confirm。
- 学生明确说还没写完，比如“还没好”“还没写好”“没写完”“不要下一个”，不是 confirm，按 chat 回应，鼓励学生慢慢写。
- 学生要等待，比如“等一下”“等一等”，不是 confirm，按 pause 处理。
- 学生要“再读一遍”“慢一点读”，是要老师重读当前词，不是写完了。
- 疑问或不肯定的语气（比如“好了？”）不算明确的完成表达；复合的话（比如“好了，再读一遍”）按学生真正的意图判断。
- 想休息才是 pause，想继续才是 resume；肯定词本身不代表休息或继续。
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
	b.WriteString(`意图词表（intent 只能是以下九个值之一）：
confirm：学生表示写完了/可以了/下一个
repeat：再读一遍
slow：慢一点读
hint：不会写，求提示
meaning：问词义
example：求例句，或问这个词怎么用
pause：要休息/暂停
resume：要继续
chat：其它对话
输出契约：只输出一个 JSON 对象，不要输出任何其它内容：{"intent":"<意图>","reply":"<给学生听的回复>"}
reply 只有 intent 为 hint/meaning/example/chat 时才需要，其它意图 reply 给空字符串。
hint 的 reply 只能给部分提示（字形、部首、读音或例句线索），严禁给出整词的写法或完整拼音，不能泄题。
meaning 的 reply 只能依据上面给出的释义与例句组织；两者都没有时，reply 固定原样输出：老师这边没有这个词的释义，你凭印象写吧。
example 的 reply 只能原样使用上面给出的例句；上面没有例句时，reply 固定原样输出：老师这边还没有这个例句，你先凭印象写吧。`)
	return b.String()
}

// buildCheckClassifySystemPrompt assembles the CHECK stage's classification
// prompt (13.1-06, D-06②) — built separately from buildClassifySystemPrompt,
// never reusing the dictation eight-word-table (Pitfall 8: a stage-expanded
// whitelist with a stale prompt lands every spoken verdict in chat):
//   - persona + child-safety wording carried over VERBATIM;
//   - the FULL word list with 1-based positions and each word's current
//     verdict state (check stage is unmasked — words may be named);
//   - the three check intents plus the base intents that stay legal in the
//     check stage (repeat/slow/pause/resume/meaning/chat); hint is NOT
//     offered — the dictation is done, a hint request classifies as chat;
//   - the strict output contract extended with the optional check payload:
//     index (1-based position heard) and text (the actual written form the
//     student spelled out, verbatim, never corrected), required only for the
//     three check intents. The consumer validates both — the model is never
//     trusted with bounds (T-13.1-13).
func buildCheckClassifySystemPrompt(words []lesson.Word, verdicts []lesson.CheckVerdict) string {
	var b strings.Builder
	b.WriteString(`你是一位亲切、耐心的小学听写家教老师，正在通过语音陪学生完成听写练习。回应要简短、自然、温暖，适合小学生：写对了就真诚表扬，写错了就耐心鼓励再试一次。严禁任何羞辱、恐吓、惩罚性或让孩子难过的表述。
课堂节奏由服务端负责，你不能推进课程，只判断学生这句话的意图并给出回应。
现在是检查阶段：默写已经全部结束，屏幕上正展示全部词的检查面板。学生正在逐词核对纸上的答案，可能会逐词说出对错，也可能说出某个词实际写成的样子。
`)
	correctByIndex := make(map[int]lesson.CheckVerdict, len(verdicts))
	for _, v := range verdicts {
		correctByIndex[v.WordIndex] = v
	}
	for i, w := range words {
		line := fmt.Sprintf("第 %d 个词：%s", i+1, w.Word)
		if v, ok := correctByIndex[i]; ok {
			switch {
			case v.Manual && v.Correct:
				line += "——已判定：对"
			case v.Manual:
				line += "——已判定：错"
			default:
				line += "——有待确认的识别结果"
			}
		} else {
			line += "——还没有判定"
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	b.WriteString(`意图词表（intent 只能是以下值之一）：
check_right：学生说第几个词写对了
check_wrong：学生说第几个词写错了
check_spelling：学生说出某个词实际写成了什么样（比如「写的是 aple」）
repeat：再读一遍
slow：慢一点读
pause：要休息/暂停
resume：要继续
meaning：问词义
chat：其它对话（以前求提示的口令现在也按 chat 处理——默写已经结束，不再有提示）
输出契约：只输出一个 JSON 对象，不要输出任何其它内容：{"intent":"<意图>","reply":"<给学生听的回复>","index":<词的序号>,"text":"<学生口述的实际写法>"}
index 和 text 只有 intent 为 check_right/check_wrong/check_spelling 时才需要，其它意图不要输出这两个键。
index 是学生说的第几个词（从 1 数起）；text 是学生口述的那个词的实际写法，原样记录，绝不改正、绝不补全。
reply 只有 intent 为 meaning/chat 时才需要，其它意图 reply 给空字符串。
meaning 的 reply 只能依据上面词表里该词的信息组织；没有信息时，reply 固定原样输出：老师这边没有这个词的释义，你看着纸上的字自己核对吧。`)
	return b.String()
}

// intentPayload is the strict classification JSON shape. Index/Text are the
// check-stage optional payload (13.1-06): Index is the 1-based word position
// the student named, Text the actual written form they spoke. Both are MODEL
// output — untrusted — so the parser accepts them shape-only and the
// CHECK-STAGE CONSUMER validates: Index bounds-checked against the lesson
// (1..len(words)), Text rune-capped via truncateRunes before any use
// (T-13.1-13).
type intentPayload struct {
	Intent string `json:"intent"`
	Reply  string `json:"reply"`
	Text   string `json:"text"`
	Index  int    `json:"index"`
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

// appendShortHistoryLocked writes the FSM short-history deque (last 4
// rounds) into a classification user message so chat/meaning replies stay
// coherent. Caller holds tm.mu (single-lock discipline).
func (t *DictationTutor) appendShortHistoryLocked(b *strings.Builder) {
	if len(t.shortHistory) == 0 {
		return
	}
	b.WriteString("\n最近对话：")
	for _, r := range t.shortHistory {
		if r.userText != "" {
			fmt.Fprintf(b, "\n学生：%s", r.userText)
		}
		if r.teacherText != "" {
			fmt.Fprintf(b, "\n老师：%s", r.teacherText)
		}
	}
}

// appendShortHistoryExceptLocked is the dictation-stage history render
// (Issue #56): OLDER rounds only, written BEFORE the current utterance. The
// trailing round carrying THIS turn's transcript (processTurn appends it
// under the lock right before classification; it cannot have a teacher
// reply yet) is the current utterance itself — the caller renders it
// separately, so it must not be duplicated as history.
// appendShortHistoryLocked stays byte-identical for the check-stage and 背默
// classifiers, which this issue does not touch. Caller holds tm.mu.
func (t *DictationTutor) appendShortHistoryExceptLocked(b *strings.Builder, current string) {
	rounds := t.shortHistory
	if n := len(rounds); n > 0 && current != "" &&
		rounds[n-1].userText == current && rounds[n-1].teacherText == "" {
		rounds = rounds[:n-1]
	}
	if len(rounds) == 0 {
		return
	}
	b.WriteString("最近对话：")
	for _, r := range rounds {
		if r.userText != "" {
			fmt.Fprintf(b, "\n学生：%s", r.userText)
		}
		if r.teacherText != "" {
			fmt.Fprintf(b, "\n老师：%s", r.teacherText)
		}
	}
	b.WriteString("\n")
}

// buildClassifyMessages assembles the classification request: the system
// prompt (persona + waiting context + intent vocabulary + word ground truth
// + output contract) plus one user message structured HISTORY FIRST, then
// the progress line, then the CURRENT utterance marked separately (Issue
// #56) — the current input is never duplicated as history, and the
// progress rides the CAPTURED target so the system prompt and the user
// message can never point at two different words (a stale live cursor
// mid-advance must not re-describe the classroom).
func (tm *TurnManager) buildClassifyMessages(transcript string, target TurnTarget) []mimo.Message {
	var user strings.Builder
	tm.mu.Lock()
	total := 0
	var card lesson.Word
	if tm.tutor != nil {
		total = len(tm.tutor.words)
		if target.WordIndex >= 0 && target.WordIndex < total {
			card = tm.tutor.words[target.WordIndex]
		}
		tm.tutor.appendShortHistoryExceptLocked(&user, transcript)
	}
	tm.mu.Unlock()
	if total > 0 && target.WordIndex >= 0 {
		fmt.Fprintf(&user, "（当前进度：第 %d 个词，共 %d 个）\n", target.WordIndex+1, total)
	}
	user.WriteString("本轮学生说：")
	user.WriteString(transcript)
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
// turnCtx (Issue #56) carries the captured target plus the turn's task
// context; the emitted event correlates this turn's identity and target
// with the model's verdict.
func (tm *TurnManager) classifyTurn(ctx context.Context, transcript string, turnCtx dictationTurnContext) (string, string) {
	msgs := tm.buildClassifyMessages(transcript, turnCtx.target)
	intent, reply, ok := tm.classifyOnce(ctx, msgs)
	if !ok {
		msgs = append(msgs, mimo.Message{
			Role:    "user",
			Content: "你刚才的输出不符合要求。请只输出一个 JSON 对象，键为 intent 与 reply，不要输出任何其它内容。",
		})
		intent, reply, ok = tm.classifyOnce(ctx, msgs)
	}
	modelIntent := intent
	if !ok {
		intent, reply = intentChat, classifyFallbackReply
		// The LLM half of the invalid-response discipline: exactly one
		// correction retry, then the deterministic spoken fallback — the
		// failure count rides the event fields like the ASR half's does.
		tm.recorder.Emit(EventError, infra.F("code", ErrCodeProviderFailed), infra.F("attempts", 2))
	}
	if turnCtx.awaitingWord {
		// Issue #56: in a live word-dictation writing stage, correct the
		// finite table of unambiguous whole phrases BEFORE dispatch — a
		// completion phrase advances whatever the model made of it, and an
		// explicit not-yet-done / waiting / re-read phrase never advances.
		// The correction keys on the TRANSCRIPT alone, so it also runs on a
		// collapsed classification (PR #61 review round 2): an exact 「好了」
		// advances even when the model never produced legal JSON — the
		// reliable transcript needs no model verdict to be recognized; every
		// other utterance keeps the fixed spoken fallback.
		intent, reply = resolveDictationIntent(transcript, intent, reply)
	}
	// Desensitization boundary (T-13-06b): the intent label only — never
	// the transcript text — reaches the event stream. Issue #56: the event
	// also carries the turn identity and the CAPTURED target (word index +
	// version, count-class values) plus the MODEL's intent beside the final
	// one, so a confirmation the classifier misjudged stays diagnosable
	// from the evidence stream alone; a collapsed classification has no
	// model verdict to report and omits model_intent.
	fields := []infra.Field{
		infra.F("intent", intent),
		infra.F("turn_id", turnCtx.turnID),
		infra.F("word_index", turnCtx.target.WordIndex),
		infra.F("version", turnCtx.target.ExpectedVersion),
	}
	if ok {
		fields = append(fields, infra.F("model_intent", modelIntent))
	}
	tm.recorder.Emit(EventIntentClassified, fields...)
	return intent, reply
}

// classifyOnce runs one Chat hop and parses its strict JSON output.
func (tm *TurnManager) classifyOnce(ctx context.Context, msgs []mimo.Message) (intent, reply string, ok bool) {
	p, ok := tm.classifyOncePayload(ctx, msgs)
	if !ok {
		return "", "", false
	}
	return p.Intent, p.Reply, true
}

// classifyOncePayload is classifyOnce returning the FULL parsed payload —
// the check stage's dispatcher (13.1-06) also consumes the optional
// Index/Text fields beside intent/reply.
func (tm *TurnManager) classifyOncePayload(ctx context.Context, msgs []mimo.Message) (intentPayload, bool) {
	resp, err := tm.llm.Chat(ctx, msgs,
		mimo.WithThinkingDisabled(), mimo.WithMaxTokens(classifyMaxTokens))
	if err != nil {
		return intentPayload{}, false
	}
	return parseIntentJSON(resp.Text())
}
