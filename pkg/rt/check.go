// check.go — the check stage of the three-stage classroom (13.1-01, D-01).
// The last word's first advance commit no longer writes ratings: it enters
// the check stage (stage='checking' persisted, check_started broadcast with
// the full plaintext list — the D-10 unmask point), the student marks every
// word 对/错 on screen (check_verdict → the executor's idempotent write →
// the authoritative check_verdicts echo, RT-07), and one check_submit
// latches the unified FSRS write (D-07) exactly once before the battle
// summary speaks (D-04) and session_ended waits for its ack (Pitfall 9 —
// the new stage never bypasses the sentence_ack gate).
//
// 13.1-03 adds the OCR pass (runCheckPhoto): a completed notebook-photo
// upload is recognized per word, graded with grading.GradeWord, and the
// results PREFILL verdicts through the same ApplyVerdict path with
// manual=false (RT-07 — OCR never bypasses the executor, and the submit
// gate still demands a manual=true verdict for every word, D-05).
//
// The event constants below live HERE (timing.go is not touched by 13.1-01)
// beside their only users, following timing.go's naming style. All fields
// stay count/index-class (T-13-06b desensitization boundary).

package rt

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/pkg/errors"

	"github.com/xiaotian/home-tutor-agent/pkg/grading"
	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
	"github.com/xiaotian/home-tutor-agent/pkg/mimo"
	"github.com/xiaotian/home-tutor-agent/pkg/ocr"
)

// Check-stage timing-event vocabulary (13.1-01). Frozen JSONL identifiers.
const (
	// EventCheckStarted: the check stage opened; fields carry the word count
	// only — never the word text (T-13-06b).
	EventCheckStarted = "check_started"
	// EventVerdictApplied: one verdict write landed (first commit only);
	// fields carry word_index + the manual/correct flags.
	EventVerdictApplied = "verdict_applied"
	// EventReviewSubmitted: the unified FSRS write returned; fields carry
	// the real requested/updated/missing sets, the unchecked flag, and the
	// dictation-stage behavior aggregates (confirmed/timed_out/hinted,
	// hint_words/hint_total — the honest re-use of the old completion
	// event's behavior data).
	EventReviewSubmitted = "review_submitted"
	// EventUncheckedFallback: the D-03 close-path fallback wrote the
	// behavior rating degraded by one level (13.1-04); fields carry the
	// requested/updated counts only (T-13-06b). The honest audit trail for
	// "this lesson was NEVER checked" — the row's unchecked marker plus this
	// event distinguish the fallback from a checked submission (T-13.1-10).
	EventUncheckedFallback = "unchecked_fallback"
	// EventReviewCorrection: the correction session submitted its diff —
	// only the words the student OVERRID against the derived fallback hints
	// (13.1-04, Pitfall 7 discipline). Fields carry the changed count plus
	// the honest updated count: the audit chain "first write (or fallback) →
	// correction" stays replayable from the JSONL stream (T-13.1-10).
	EventReviewCorrection = "review_correction"
)

// purposeCheckBridge is the announcement purpose of the check-stage bridge
// sentence (an ordinary announcement: ack-gated settle, timeout → orderly
// close — never a natural ending).
const purposeCheckBridge = "check_bridge"

// Fixed human wordings for the check stage's visible errors (the codes carry
// the machine meaning; the messages stay short, child-safe and honest).
const (
	// checkVerdictInvalidNotice: out-of-range word index or an empty
	// request_id — a sanitized rejection, no write.
	checkVerdictInvalidNotice = "这个词好像不在名单里，再看看屏幕哦"
	// checkSubmitIncompleteNotice: the submit gate requires a manual verdict
	// for EVERY word before the unified write.
	checkSubmitIncompleteNotice = "还有词没有检查哦，每个词都点过「对」或「错」再提交吧"
	// checkStageClosedNotice: verdicts/submit frames arriving outside the
	// check stage (already submitted, or no classroom).
	checkStageClosedNotice = "这一课已经检查完了哦"
	// checkStoreFailedNotice: the check-stage store write failed — the same
	// honest store-failure wording the advance path uses.
	checkStoreFailedNotice = "课堂进度更新失败，请再试一次"
)

// checkBridgeText speaks the stage transition after check_started (ACK-gated
// like every announcement). Encouraging only — entering the check stage is
// framed as the lesson's next game, never as a judgment (prohibition P1).
func checkBridgeText(total int) string {
	return fmt.Sprintf("全都写完啦，真棒！现在我们一起检查：屏幕上有 %d 个词，想一想你写的是对还是错，点「对」或「错」告诉我吧。", total)
}

// checkResumeBridgeText speaks the reconnect-into-check transition
// (13.1-04): the student resumes exactly where they left off (Pitfall 2).
// ACK-gated like every announcement; word-free wording (the words are
// already on the screen — check_started unmasked them).
func checkResumeBridgeText(total int) string {
	return fmt.Sprintf("欢迎回来！我们继续检查刚才那 %d 个词，接着点「对」或「错」告诉我吧。", total)
}

// checkCorrectionBridgeText speaks the correction-mode transition (13.1-04,
// D-03): the fallback rated the lesson without a check, and this session
// re-checks it. The derived hints are framed as the teacher's first guess —
// never as a verdict (prohibition P1; D-05: the human call is mandatory).
func checkCorrectionBridgeText(total int) string {
	return fmt.Sprintf("欢迎回来！上次没来得及检查，我们现在补上：屏幕上有 %d 个词，老师先猜了一个，你来点「对」或「错」告诉老师真正的答案吧。", total)
}

// checkedSummaryText builds the post-submit battle summary (D-04): the
// D-13 lesson_complete phrasing first (今天的 N 个词都默写完了 — the
// completion ending's own copy, distinct from the no-due and practice
// endings), the 对/错 battle count, encouraging wording only (no shaming on
// wrong words, prohibition P1), then the honest save note — the failure
// suffix when the write errored, the M/N note when fewer words saved than
// requested (S-05: never a fabricated full success).
func checkedSummaryText(total, correct, updated, requested int, saveFailed bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "今天的 %d 个词都默写完了，对了 %d 个。", total, correct)
	switch {
	case correct == total && total > 0:
		b.WriteString("全部都写对了，你真棒！")
	case correct > 0:
		b.WriteString("已经有很棒的进步了，继续加油！")
	default:
		b.WriteString("没关系，多练几次就更熟啦。")
	}
	if saveFailed {
		b.WriteString(reviewFailedSuffix())
	} else if updated < requested {
		fmt.Fprintf(&b, "本次保存 %d/%d 个。", updated, requested)
	}
	return b.String()
}

// checkRatings assembles the unified-submit rating map from the STORED
// verdicts and the stored behavior snapshot: per word,
// checkRating(correct ? checkCorrect : checkWrong, hints) — the D-02 table.
// A word the matrix does not recognize is skipped, exactly like ratings().
func checkRatings(words []lesson.Word, verdicts []lesson.CheckVerdict, outcomes map[string]lesson.WordOutcome) map[string]string {
	out := make(map[string]string, len(verdicts))
	for _, v := range verdicts {
		if v.WordIndex < 0 || v.WordIndex >= len(words) {
			continue
		}
		result := checkWrong
		if v.Correct {
			result = checkCorrect
		}
		hints := 0
		if o, ok := outcomes[strconv.Itoa(v.WordIndex)]; ok {
			hints = o.Hints
		}
		rating := checkRating(result, hints)
		if rating == "" {
			continue
		}
		out[words[v.WordIndex].Word] = rating
	}
	return out
}

// wireVerdicts projects the stored verdict list onto the wire echo shape.
func wireVerdicts(verdicts []lesson.CheckVerdict) []WireVerdict {
	out := make([]WireVerdict, 0, len(verdicts))
	for _, v := range verdicts {
		out = append(out, WireVerdict{WordIndex: v.WordIndex, Correct: v.Correct, Manual: v.Manual})
	}
	return out
}

// broadcastCheckVerdicts sends the authoritative verdict echo (RT-07: the
// client renders ONLY this server state, never local optimistic state).
func (tm *TurnManager) broadcastCheckVerdicts(s Sender, verdicts []lesson.CheckVerdict) {
	_ = s.SendControl(CheckVerdictsMsg{Type: TypeCheckVerdicts, Verdicts: wireVerdicts(verdicts)})
}

// derivedVerdicts projects the STORED behavior snapshot onto pending check
// verdicts (13.1-04 correction mode): a word derives correct iff its
// behavior rating was good (the D-03 fallback read 无提示说好 and stored it
// one level down), everything else derives wrong. Manual stays FALSE — the
// derivation is a display hint, never a confirmation (D-05: every word is
// confirmed by a human before the correction submit proceeds).
func derivedVerdicts(words []lesson.Word, outcomes map[string]lesson.WordOutcome) []lesson.CheckVerdict {
	out := make([]lesson.CheckVerdict, 0, len(words))
	for i := range words {
		o := outcomes[strconv.Itoa(i)]
		out = append(out, lesson.CheckVerdict{
			WordIndex: i,
			Correct:   wordRating(o.Outcome, o.Hints) == ratingGood,
			Manual:    false,
		})
	}
	return out
}

// startCheckResume restores a session whose lesson row still sits in
// stage='checking' (13.1-04, Pitfall 2): the check never finished and the
// close fallback never landed (a crash before the close path, a failed
// fallback write, or a row from before the fallback existed). The check
// panel reopens from the STORED state and the student resumes exactly where
// they left off.
func (tm *TurnManager) startCheckResume(s Sender, l lesson.Lesson) {
	tm.startCheckPanelSession(s, l, CheckStartedReasonResume, false)
}

// startCheckCorrection reopens a SUBMITTED row carrying the D-03 unchecked
// marker (13.1-04): the fallback already rated the lesson from behavior, so
// this session is the post-hoc correction — the derived downgraded verdicts
// show as pending hints and check_submit runs the diff-only correction
// path.
func (tm *TurnManager) startCheckCorrection(s Sender, l lesson.Lesson) {
	tm.startCheckPanelSession(s, l, CheckStartedReasonCorrection, true)
}

// startCheckPanelSession is the shared body of the two reconnect-into-check
// starters: bind the tutor from the row (stored verdicts/outcomes into the
// caches), answer session_ready with the mode-appropriate lesson view
// (dictation is done, so the student view reveals every word), broadcast
// check_started with the FULL plaintext list (the D-10 unmask point — the
// same message the dictation_done entry uses, only the reason differs) plus
// the authoritative verdict echo, then speak the reason's bridge through the
// ACK gate. No due-fetch, no word timers, no rating write — those belong to
// the paths that FINISH the check (handleCheckVerdict / handleCheckSubmit).
func (tm *TurnManager) startCheckPanelSession(s Sender, l lesson.Lesson, reason string, correction bool) {
	words := make([]string, 0, len(l.Cards))
	for _, c := range l.Cards {
		words = append(words, c.Word)
	}
	// The panel's initial verdict state: stored verdicts for a resume, the
	// derived fallback hints for a correction (pending, manual=false).
	panelVerdicts := append([]lesson.CheckVerdict(nil), l.Verdicts...)
	if correction {
		panelVerdicts = derivedVerdicts(l.Cards, l.Outcomes)
	}

	tm.mu.Lock()
	if tm.closed {
		// PR #60 review: the subject-picked classroom reaches this resume
		// branch on the answer turn's goroutine — an end_session that closed
		// the session under the row IO gets no panel replay at all.
		tm.mu.Unlock()
		return
	}
	tm.tutor = newDictationTutor(l)
	tm.tutor.completed = true // word pacing is over; the countdown stays disarmed
	tm.tutor.checking = true  // the check panel owns the input surface
	tm.tutor.correction = correction
	tm.tutor.checkVerdicts = panelVerdicts
	tm.tutor.restoreOutcomes(l.Outcomes)
	tm.state = StateListening
	tm.sessionReadyAt = time.Now()
	tm.mu.Unlock()
	// Issue #12: a POEM row reopened into its check panel keeps its poem
	// identity (the task view on session_ready + the submit's whole-poem
	// save need it).
	poemView := tm.poemViewForRow(l)
	if poemView != nil {
		tm.bindPoemRef(l, poemView)
	}
	// Issue #14: a PASSAGE row reopened into its check panel keeps its
	// work + range identity the same way (the range always re-reads from
	// the ROW — what the child actually practiced).
	passageView := tm.passageViewForRow(l)
	if passageView != nil {
		tm.bindPassageRef(passageView)
	}

	_ = s.SendControl(SessionReadyMsg{
		Type:      TypeSessionReady,
		SessionID: tm.session,
		Audio:     AudioSpec{SampleRate: requiredSampleRate, Format: requiredFormat, Channels: requiredChannels},
		TTS:       TTSSpec{SampleRate: TTSOutputSampleRate},
		Lesson:    tm.lessonViewFor(l),
		Category:  tm.sessionCategory(),
		Poem:      poemView,
		Passage:   passageView,
	})
	tm.recorder.Emit(EventConnected, infra.F("session_id", tm.session))

	tm.recorder.Emit(EventCheckStarted, infra.F("total", len(words)))
	_ = s.SendControl(CheckStartedMsg{
		Type:   TypeCheckStarted,
		Words:  words,
		Reason: reason,
	})
	tm.broadcastCheckVerdicts(s, panelVerdicts)

	ctx, cancel := context.WithCancel(context.Background())
	tm.mu.Lock()
	if tm.closed {
		// A close that landed after the panel replay still gets no spoken
		// bridge (runAnnouncementDisplayAt's entry gate is the backstop).
		tm.mu.Unlock()
		cancel()
		return
	}
	tm.turnCancel = cancel
	tm.mu.Unlock()
	bridge := checkResumeBridgeText(len(words))
	if correction {
		bridge = checkCorrectionBridgeText(len(words))
	}
	// Own goroutine: handleSessionInit runs on the transport READ loop, and
	// the announcement blocks on its playback ack — the same shape
	// startDictationSession uses for the greeting (the ack must stay
	// servable).
	go tm.runAnnouncement(s, ctx, cancel, bridge, purposeCheckBridge, nil)
}

// enterCheck opens the check stage; reachable ONLY from advanceTarget's
// first-commit finished branch (a duplicate confirm replays idempotently and
// can never re-enter — the same single-entry property enterCompletion had).
// Order: persist stage='checking' + the behavior snapshot → check_started
// (full plaintext, D-10 unmask) → the ACK-gated bridge announcement (Pitfall
// 9: the new stage does not bypass sentence_ack). Replaces enterCompletion.
func (tm *TurnManager) enterCheck(s Sender, ctx context.Context, cancel context.CancelFunc, epoch uint64) {
	if !tm.epochCurrent(epoch) {
		return
	}
	tm.mu.Lock()
	tm.tutor.markCompleted() // word pacing is over; the countdown stays disarmed
	tm.stopWordTimerLocked() // no per-word timer in the check stage
	lessonID := tm.tutor.lessonID
	expected := tm.tutor.version
	outcomes := tm.tutor.snapshotCheckOutcomes()
	total := len(tm.tutor.words)
	isPoem := tm.tutor.poem != nil       // Issue #12: the poem's own bridge copy/reason
	isPassage := tm.tutor.passage != nil // Issue #14: the passage range's own bridge
	words := make([]string, 0, total)    // the FULL plaintext list (D-10 unmask)
	for _, c := range tm.tutor.words {
		words = append(words, c.Word)
	}
	tm.mu.Unlock()

	res, err := tm.exec.EnterCheck(ctx, lessonID, expected, lessonID+"-enter-check", outcomes)
	// PR #61 review: the stage write may commit UNDER a close or interrupt —
	// the row stands (a reconnect resumes from it), but a dead classroom
	// opens no panel: no tutor mutation, no check_started, no bridge.
	if !tm.epochCurrent(epoch) {
		return
	}
	switch err {
	case nil:
		// PR #61 review round 2: the liveness re-check above only covered the
		// executor's return moment. A close/interrupt landing between that
		// check and this mutation would flip a dead classroom into the check
		// stage — so the check rides the SAME lock hold as the mutation (the
		// advanceTarget first-commit pattern).
		tm.mu.Lock()
		if tm.epoch != epoch || tm.closed {
			tm.mu.Unlock()
			return
		}
		tm.tutor.version = res.NewVersion
		tm.tutor.checking = true
		tm.tutor.checkVerdicts = res.Verdicts
		tm.mu.Unlock()
	default:
		var conflict *lesson.ConflictError
		if !errors.As(err, &conflict) {
			// The store write failed: visible honest failure, no fabricated
			// check stage (the student's screen simply stays on the lesson).
			tm.failTurn(s, epoch, ErrCodeProviderFailed, checkStoreFailedNotice)
			return
		}
		// Conflict: align from the stored truth (another writer moved the
		// row) and continue into the check stage from THAT state.
		l, gerr := tm.exec.GetLessonState(ctx, lessonID)
		if gerr != nil {
			tm.failTurn(s, epoch, ErrCodeProviderFailed, checkStoreFailedNotice)
			return
		}
		tm.mu.Lock()
		if tm.epoch != epoch || tm.closed {
			tm.mu.Unlock()
			return
		}
		tm.tutor.align(l.CurrentIndex, l.Version)
		tm.tutor.resyncCheck(l)
		alreadySubmitted := l.Stage == lesson.StageSubmitted && !tm.tutor.correction
		tm.mu.Unlock()
		if alreadySubmitted {
			// The concurrent writer already submitted the row (WR-02,
			// 13.1-08): broadcasting check_started here would hand this
			// connection a panel that can never open. The honest completion
			// short path (D-04) is the tail startCompletedSession has: the
			// terminal signal — the frontend's existing lesson_complete
			// banner + notice carry the child-facing explanation — plus one
			// duplicate diagnostic, then close. EventCheckStarted,
			// CheckStartedMsg and the bridge announcement all stay
			// unsent. A correction session keeps its panel by construction
			// (a submitted row is its normal shape), hence the
			// !correction gate above.
			tm.recorder.Emit(EventError,
				infra.F("code", ErrCodeDuplicateSubmit),
				infra.F("duplicate", "enter_check_submitted"),
			)
			_ = s.SendControl(SessionEndedMsg{Type: TypeSessionEnded, Reason: ReasonLessonComplete})
			tm.recorder.Emit(EventSessionEnd, infra.F("session_id", tm.session))
			cancel() // the caller's ctx — beyond what internalClose cancels
			tm.internalClose()
			return
		}
	}

	reason := CheckStartedReasonDictationDone
	bridge := checkBridgeText(total)
	if isPoem {
		reason = CheckStartedReasonPoemDone
		bridge = poemCheckBridgeText(total)
	} else if isPassage {
		// Issue #14: the passage's own bridge copy/reason — the rows are
		// this range's paragraphs, labeled by the range the task announced.
		reason = CheckStartedReasonPassageDone
		tm.mu.Lock()
		if tm.tutor != nil && tm.tutor.passage != nil {
			bridge = passageCheckBridgeText(tm.tutor.passage.Whole, tm.tutor.passage.Paragraphs, total)
		}
		tm.mu.Unlock()
	}
	// The panel open rides ONE lock hold with the liveness re-check and the
	// enqueue (the speakTurn / lesson_update pattern): a close that races this
	// window either sets closed first (the branch dies here) or blocks on
	// tm.mu until check_started is queued — the close paths ship session_ended
	// only AFTER closed is set, so check_started can never trail the goodbye
	// on the wire (PR #61 review round 2).
	tm.mu.Lock()
	if tm.epoch != epoch || tm.closed {
		tm.mu.Unlock()
		return
	}
	_ = s.SendControl(CheckStartedMsg{
		Type:   TypeCheckStarted,
		Words:  words,
		Reason: reason,
	})
	tm.mu.Unlock()
	tm.recorder.Emit(EventCheckStarted, infra.F("total", total))

	// The bridge speaks under the TURN'S OWN epoch (runAnnouncementDisplayAt,
	// the subject-ask pattern): snapshotting the live epoch here would let an
	// interrupt that already bumped it play the check bridge over whatever
	// classroom comes next.
	tm.runAnnouncementDisplayAt(s, epoch, ctx, cancel, bridge, bridge, purposeCheckBridge, nil)
}

// handleCheckVerdict applies one screen verdict (the check_verdict control
// frame): validate (alive, stage checking, index in range, request_id
// present — invalid frames answer a sanitized error with NO write), write
// through the executor's idempotent path, and echo the FULL authoritative
// verdict list (RT-07 single render source). Runs on the transport read
// loop; the DB write is the same short-transaction class as session_init's.
func (tm *TurnManager) handleCheckVerdict(s Sender, m CheckVerdictMsg) {
	tm.mu.Lock()
	alive := tm.tutor != nil && !tm.closed && tm.tutor.checking
	total := 0
	if tm.tutor != nil {
		total = len(tm.tutor.words)
	}
	tm.mu.Unlock()
	if !alive {
		tm.sendError(s, ErrCodeInvalidMessage, checkStageClosedNotice, true)
		return
	}
	if m.RequestID == "" || m.WordIndex < 0 || m.WordIndex >= total {
		tm.sendError(s, ErrCodeInvalidMessage, checkVerdictInvalidNotice, true)
		return
	}

	tm.mu.Lock()
	lessonID := tm.tutor.lessonID
	expected := tm.tutor.version
	tm.mu.Unlock()

	// Screen verdicts are the only verdict kind this plan creates: manual
	// always true (OCR prefills arrive in 13.1-03 with Manual=false and do
	// NOT satisfy the submit gate).
	res, err := tm.exec.ApplyVerdict(context.Background(), lessonID, expected, m.RequestID,
		lesson.CheckVerdict{WordIndex: m.WordIndex, Correct: m.Correct, Manual: true})
	if err != nil {
		var conflict *lesson.ConflictError
		if errors.As(err, &conflict) {
			// Align from the stored truth and re-echo: the button state
			// re-renders from the server's authoritative list.
			if l, gerr := tm.exec.GetLessonState(context.Background(), lessonID); gerr == nil {
				tm.mu.Lock()
				tm.tutor.align(l.CurrentIndex, l.Version)
				tm.tutor.resyncCheck(l)
				tm.mu.Unlock()
				tm.broadcastCheckVerdicts(s, l.Verdicts)
			}
			return
		}
		tm.sendError(s, ErrCodeProviderFailed, checkStoreFailedNotice, true)
		return
	}

	tm.mu.Lock()
	tm.tutor.version = res.NewVersion
	tm.tutor.checkVerdicts = res.Verdicts
	tm.mu.Unlock()
	tm.broadcastCheckVerdicts(s, res.Verdicts)
	if !res.Idempotent {
		tm.recorder.Emit(EventVerdictApplied,
			infra.F("word_index", m.WordIndex),
			infra.F("manual", true),
			infra.F("correct", m.Correct),
		)
	}
}

// handleCheckSubmit runs the unified submission gate (the check_submit
// control frame): every word needs a MANUAL verdict before anything is
// written; the executor's checking→submitted transition is the latch and
// ONLY its first commit drives the one SubmitReview + summary flow (on its
// own goroutine — runNaturalEnding blocks on the summary ack, and the
// read loop must keep serving that ack).
func (tm *TurnManager) handleCheckSubmit(s Sender, m CheckSubmitMsg) {
	tm.mu.Lock()
	alive := tm.tutor != nil && !tm.closed
	checking := alive && tm.tutor.checking
	reviewSeen := alive && !tm.tutor.checking && tm.tutor.reviewSeen
	tm.mu.Unlock()
	if !alive {
		tm.sendError(s, ErrCodeInvalidMessage, checkStageClosedNotice, true)
		return
	}
	if !checking {
		if reviewSeen {
			// In-session duplicate submit (a network retry or a second
			// tap): the first review_result already went out — replay
			// NOTHING (no second FSRS write, no second review_result, no
			// second summary; Pitfall 6). A sanitized diagnostic only.
			tm.recorder.Emit(EventError,
				infra.F("code", ErrCodeInvalidMessage),
				infra.F("duplicate", "check_submit"),
			)
			return
		}
		tm.sendError(s, ErrCodeInvalidMessage, checkStageClosedNotice, true)
		return
	}

	tm.mu.Lock()
	correction := tm.tutor.correction
	correctionSeen := correction && tm.tutor.reviewSeen
	tm.mu.Unlock()
	if correction {
		if correctionSeen {
			// Duplicate correction submit: the diff batch already ran —
			// never a second SubmitReview (Pitfall 6, same discipline as
			// the normal path above).
			tm.recorder.Emit(EventError,
				infra.F("code", ErrCodeInvalidMessage),
				infra.F("duplicate", "correction_submit"),
			)
			return
		}
		tm.submitCorrections(s, m)
		return
	}
	tm.mu.Lock()
	isPoem := tm.tutor.poem != nil
	isPassage := tm.tutor.passage != nil
	tm.mu.Unlock()
	if isPoem {
		// Issue #12: the whole-poem submit — the same manual gate and the
		// same CompleteCheck latch, then the poem practice record instead
		// of ANY word write (in-session duplicates were already absorbed by
		// the reviewSeen gate above).
		tm.submitPoem(s, m)
		return
	}
	if isPassage {
		// Issue #14: the passage-range submit — the same manual gate and
		// the same CompleteCheck latch, then the content + range practice
		// record instead of ANY word write.
		tm.submitPassage(s, m)
		return
	}
	tm.mu.Lock()
	practice := tm.tutor.practice
	practiceSeen := practice && tm.tutor.reviewSeen
	tm.mu.Unlock()
	if practice {
		if practiceSeen {
			// Duplicate practice submit (13.1-05): the practice records
			// already landed — never a second practice_logs write (Pitfall 6
			// discipline; T-13.1-11 keeps FSRS isolation irrelevant here).
			tm.recorder.Emit(EventError,
				infra.F("code", ErrCodeInvalidMessage),
				infra.F("duplicate", "practice_submit"),
			)
			return
		}
		tm.submitPractice(s, m)
		return
	}
	if m.RequestID == "" {
		tm.sendError(s, ErrCodeInvalidMessage, checkVerdictInvalidNotice, true)
		return
	}

	// Submit gate: every word index must carry a MANUAL verdict (OCR
	// prefills from 13.1-03 do not satisfy this gate).
	tm.mu.Lock()
	lessonID := tm.tutor.lessonID
	expected := tm.tutor.version
	total := len(tm.tutor.words)
	manual := make(map[int]bool, total)
	for _, v := range tm.tutor.checkVerdicts {
		if v.Manual {
			manual[v.WordIndex] = true
		}
	}
	tm.mu.Unlock()
	if len(manual) < total {
		tm.sendError(s, ErrCodeInvalidMessage, checkSubmitIncompleteNotice, true)
		return
	}

	// Latch: the stage transition. ONLY the first commit proceeds.
	ctx, cancel := context.WithCancel(context.Background())
	tm.mu.Lock()
	tm.turnCancel = cancel
	tm.mu.Unlock()

	res, err := tm.exec.CompleteCheck(ctx, lessonID, expected, m.RequestID, false)
	if err != nil {
		var conflict *lesson.ConflictError
		if errors.As(err, &conflict) {
			// Align from the stored truth: if the row is already submitted
			// the write was a cross-connection race — treat it as the
			// duplicate path (no second write either way).
			tm.mu.Lock()
			tm.tutor.checking = false
			tm.mu.Unlock()
			tm.recorder.Emit(EventError,
				infra.F("code", ErrCodeDuplicateSubmit),
				infra.F("duplicate", "check_submit_conflict"),
			)
			return
		}
		tm.sendError(s, ErrCodeProviderFailed, checkStoreFailedNotice, true)
		return
	}
	if res.Idempotent {
		// The latch already fell elsewhere (a cross-connection race — the
		// other tab / the reconnected session submitted first). This caller
		// is the absorbed duplicate: NEVER finishCheckSubmit (the one
		// SubmitReview call site), NEVER checkRatings — the latch replay's
		// readback carries EMPTY verdicts/outcomes (executor.go:908), so
		// assembling here would build an empty batch; and NEVER a
		// review_result or a save count — fabricating one would violate the
		// transparency prohibition (S-05) across connections. The loser's
		// honest shape is the terminal signal (session_ended with the
		// existing lesson_complete copy) plus one diagnostic event
		// (13.1-08 review consensus). internalClose cancels the turnCancel
		// stored above, so no separate cancel is needed.
		tm.mu.Lock()
		tm.tutor.version = res.NewVersion
		tm.tutor.checking = false
		tm.mu.Unlock()
		tm.recorder.Emit(EventError,
			infra.F("code", ErrCodeDuplicateSubmit),
			infra.F("duplicate", "check_submit_latch"),
		)
		_ = s.SendControl(SessionEndedMsg{Type: TypeSessionEnded, Reason: ReasonLessonComplete})
		tm.recorder.Emit(EventSessionEnd, infra.F("session_id", tm.session))
		tm.internalClose()
		return
	}

	// Assemble the rating inputs from the STORED verdicts + outcomes
	// readback (never from the in-memory cache — the row is the authority).
	tm.mu.Lock()
	tm.tutor.version = res.NewVersion
	tm.tutor.checking = false
	wordsCopy := make([]lesson.Word, len(tm.tutor.words))
	copy(wordsCopy, tm.tutor.words)
	tm.mu.Unlock()
	ratings := checkRatings(wordsCopy, res.Verdicts, res.Outcomes)
	correct := 0
	for _, v := range res.Verdicts {
		if v.Correct {
			correct++
		}
	}

	go tm.finishCheckSubmit(s, ctx, cancel, ratings, total, correct, m.RequestID)
}

// sortedWordSet extracts a rating map's keys sorted — the receipt word-set
// encoding normalizes order before storage, so map iteration order never
// leaks into a stored row payload (13.3-01).
func sortedWordSet(m map[string]string) []string {
	words := make([]string, 0, len(m))
	for w := range m {
		words = append(words, w)
	}
	sort.Strings(words)
	return words
}

// finishCheckSubmit runs on its own goroutine (D-04 order): ONE SubmitReview
// call whose REAL result sets shape the honest review_result message and the
// battle summary; runNaturalEnding then speaks the summary and waits for its
// valid playback ack before session_ended(lesson_complete). This is the ONLY
// SubmitReview call site the check stage owns. 13.3-01 brackets the write
// with the two-phase save receipt (Begin pending before, Complete with the
// real outcome after) — a pure bypass that never alters the flow below.
func (tm *TurnManager) finishCheckSubmit(s Sender, ctx context.Context, cancel context.CancelFunc, ratings map[string]string, total, correct int, requestID string) {
	defer cancel()

	// Dictation-stage behavior data stays honestly reported (the D-01
	// rewrite keeps outcomeCounts/hintAccounting as event fields).
	tm.mu.Lock()
	confirmed, timedOutN, hinted := tm.tutor.outcomeCounts()
	hintWords, hintTotal := tm.tutor.hintAccounting()
	lessonID := tm.tutor.lessonID
	category := tm.tutor.category
	tm.mu.Unlock()

	var (
		requested  = len(ratings)
		updated    = 0
		missing    = 0
		saveFailed = false
	)
	if len(ratings) > 0 {
		// 13.3-01 receipt phase one: mark the attempt pending BEFORE the
		// write, so 「已提交」 never masquerades as 「已写入 FSRS」. Bypass
		// discipline (kept for phase two below): a receipt failure only
		// emits a server-side error event — it never returns, never touches
		// saveFailed/updated/missing, and NEVER drives a rewrite (S-05).
		receiptID := int64(0)
		requestedSet := sortedWordSet(ratings)
		if tm.cfg.Receipts != nil {
			rec, rerr := tm.cfg.Receipts.Begin(ctx, lesson.ReceiptInput{
				LessonID:  lessonID,
				RequestID: requestID,
				Kind:      lesson.ReceiptKindCheck,
				Requested: requestedSet,
			})
			if rerr != nil || rec.ID <= 0 {
				tm.recorder.Emit(EventError, infra.F("code", ErrCodeProviderFailed), infra.F("at", "save_receipt"))
			} else {
				receiptID = rec.ID
			}
		}

		wres, err := tm.cfg.Words.SubmitReview(ctx, ratings, category)
		switch {
		case err != nil:
			// Honest failure: the record was NOT saved — never a narrated
			// success (transparency prohibition). The review_result still
			// ships, reporting zero saved of the requested set.
			saveFailed = true
			tm.recorder.Emit(EventError, infra.F("code", ErrCodeProviderFailed))
		default:
			updated, missing = len(wres.Updated), len(wres.Missing)
			tm.recorder.Emit(EventReviewSubmitted,
				infra.F("requested", requested),
				infra.F("updated", updated),
				infra.F("missing", missing),
				infra.F("unchecked", false),
				infra.F("confirmed", confirmed),
				infra.F("timed_out", timedOutN),
				infra.F("hinted", hinted),
				infra.F("hint_words", hintWords),
				infra.F("hint_total", hintTotal),
			)
			if updated < requested {
				// Partial/zero update with nil error is not success: the
				// real counts ride the event plus a server-side error
				// marker, and the batch is NEVER retried blindly (S-05).
				tm.recorder.Emit(EventError, infra.F("code", ErrCodeProviderFailed))
			}
		}

		// 13.3-01 receipt phase two: fill the REAL outcome. SubmitReview
		// error → failed (nothing landed, missing = the full requested
		// set); nil error → saved with the real sets (a partial update is
		// still saved-with-M/N — the same honesty as the review_result
		// above, never a failed flip).
		if tm.cfg.Receipts != nil && receiptID > 0 {
			var cerr error
			if err != nil {
				cerr = tm.cfg.Receipts.Complete(ctx, receiptID, nil, requestedSet, false)
			} else {
				cerr = tm.cfg.Receipts.Complete(ctx, receiptID, wres.Updated, wres.Missing, true)
			}
			if cerr != nil {
				// The row stays pending — the parent view's honest 「待核实」.
				tm.recorder.Emit(EventError, infra.F("code", ErrCodeProviderFailed), infra.F("at", "save_receipt"))
			}
		}
	}

	result := ReviewResultMsg{
		Type:      TypeReviewResult,
		Requested: requested,
		Updated:   updated,
		Missing:   missing,
		Unchecked: false,
	}
	if saveFailed {
		result.Updated = 0
		result.Missing = requested
	}
	tm.mu.Lock()
	tm.tutor.reviewSeen = true
	tm.tutor.reviewResult = result
	tm.mu.Unlock()
	_ = s.SendControl(result)

	summary := checkedSummaryText(total, correct, result.Updated, requested, saveFailed)
	tm.runNaturalEnding(s, ctx, cancel, summary, purposeSummary, ReasonLessonComplete)
}

// ---------------------------------------------------------------------------
// Correction mode (13.1-04, D-03): a submitted+unchecked row reopens for the
// post-hoc check. The derived fallback verdicts are display hints; the
// human confirms every word (D-05) and check_submit runs the DIFF-ONLY
// second write — only overridden words ride the correction batch (Pitfall 7:
// the smallest possible second review, honestly recorded as a correction).
// ---------------------------------------------------------------------------

// submitCorrections is the correction-mode check_submit: the SAME per-word
// manual gate as the normal submit, but the row is already 'submitted' (the
// CompleteCheck latch fell with the fallback), so instead of a stage
// transition the confirmed verdicts are diffed against the DERIVED fallback
// hints and only overridden words are re-rated via checkRating and written.
// ClearUnchecked then resets the marker idempotently, the honest wrap line
// speaks through the ACK gate, and session_ended(lesson_complete) closes.
// All-agreeing confirmations produce an empty diff: no review write runs,
// the marker still clears, the wrap still thanks — the student confirmed
// the fallback as it stands.
func (tm *TurnManager) submitCorrections(s Sender, m CheckSubmitMsg) {
	if m.RequestID == "" {
		tm.sendError(s, ErrCodeInvalidMessage, checkVerdictInvalidNotice, true)
		return
	}

	// Submit gate (D-05, identical to the normal path): every word needs a
	// MANUAL verdict — the derived hints never satisfy it.
	tm.mu.Lock()
	lessonID := tm.tutor.lessonID
	total := len(tm.tutor.words)
	manual := make(map[int]bool, total)
	for _, v := range tm.tutor.checkVerdicts {
		if v.Manual {
			manual[v.WordIndex] = true
		}
	}
	tm.mu.Unlock()
	if len(manual) < total {
		tm.sendError(s, ErrCodeInvalidMessage, checkSubmitIncompleteNotice, true)
		return
	}

	// The row is the authority for the diff inputs (stored outcomes →
	// derived hints, current version for ClearUnchecked).
	ctx, cancel := context.WithCancel(context.Background())
	tm.mu.Lock()
	tm.turnCancel = cancel
	tm.mu.Unlock()
	l, err := tm.exec.GetLessonState(ctx, lessonID)
	if err != nil {
		tm.sendError(s, ErrCodeProviderFailed, checkStoreFailedNotice, true)
		return
	}
	if !l.Unchecked {
		// Same-family guard of the main submit's Idempotent branch
		// (13.1-08): another connection's correction already cleared the
		// marker — this straggler's diff batch must NEVER run (zero
		// SubmitReview, zero ClearUnchecked, zero review_result). The
		// honest shape is the terminal signal plus one diagnostic event.
		// Accepted residual race (FA-GAP08-TOCTOU, named in the 13-13
		// handoff): two connections reading unchecked=1 within the same
		// millisecond both pass this read-side gate — closing it fully
		// needs a preemptive ClearUnchecked latch, which would break the
		// S-05 retry semantics below (a failed review write must keep the
		// marker for the next session).
		tm.mu.Lock()
		tm.tutor.align(l.CurrentIndex, l.Version)
		tm.tutor.checking = false
		tm.mu.Unlock()
		tm.recorder.Emit(EventError,
			infra.F("code", ErrCodeDuplicateSubmit),
			infra.F("duplicate", "correction_submit_latch"),
		)
		_ = s.SendControl(SessionEndedMsg{Type: TypeSessionEnded, Reason: ReasonLessonComplete})
		tm.recorder.Emit(EventSessionEnd, infra.F("session_id", tm.session))
		tm.internalClose()
		return
	}

	tm.mu.Lock()
	confirmed := append([]lesson.CheckVerdict(nil), tm.tutor.checkVerdicts...)
	tm.mu.Unlock()
	hints := make(map[int]int, len(l.Cards))
	for key, o := range l.Outcomes {
		if idx, perr := strconv.Atoi(key); perr == nil {
			hints[idx] = o.Hints
		}
	}
	derived := derivedVerdicts(l.Cards, l.Outcomes)
	changed := make(map[string]string)
	for _, v := range confirmed {
		if v.WordIndex < 0 || v.WordIndex >= len(l.Cards) {
			continue
		}
		var base *lesson.CheckVerdict
		for i := range derived {
			if derived[i].WordIndex == v.WordIndex {
				base = &derived[i]
				break
			}
		}
		if base != nil && base.Correct == v.Correct {
			continue // the human call agrees with the derivation: stored rating stands
		}
		result := checkWrong
		if v.Correct {
			result = checkCorrect
		}
		rating := checkRating(result, hints[v.WordIndex])
		if rating == "" {
			continue
		}
		changed[l.Cards[v.WordIndex].Word] = rating
	}
	correct := 0
	for _, v := range confirmed {
		if v.Correct && v.WordIndex >= 0 && v.WordIndex < len(l.Cards) {
			correct++
		}
	}

	go tm.finishCorrections(s, ctx, cancel, correctionInputs{
		requestID: m.RequestID,
		lessonID:  lessonID,
		category:  lesson.NormalizeCategory(l.Category),
		version:   l.Version,
		changed:   changed,
		total:     total,
		correct:   correct,
	})
}

// correctionInputs carries the diff result into the finishing goroutine.
type correctionInputs struct {
	changed   map[string]string
	requestID string
	lessonID  string
	// category is the ROW's subject (Issue #10): the diff write lands on the
	// vocabulary column the lesson was actually taught in.
	category string
	version  int
	total    int
	correct  int
}

// finishCorrections runs on its own goroutine (runNaturalEnding blocks on
// the summary ack, and the read loop must keep serving that ack): the
// diff-only SubmitReview, the correction event, ClearUnchecked, the honest
// review_result, then the wrap line and the acked ending.
func (tm *TurnManager) finishCorrections(s Sender, ctx context.Context, cancel context.CancelFunc, in correctionInputs) {
	defer cancel()

	requested := len(in.changed)
	updated := 0
	saveFailed := false
	if requested > 0 {
		// 13.3-01 receipt phase one: pending marker before the diff write
		// (the same bypass discipline as finishCheckSubmit — a receipt
		// failure never returns, never rewrites, S-05).
		receiptID := int64(0)
		changedSet := sortedWordSet(in.changed)
		if tm.cfg.Receipts != nil {
			rec, rerr := tm.cfg.Receipts.Begin(ctx, lesson.ReceiptInput{
				LessonID:  in.lessonID,
				RequestID: in.requestID,
				Kind:      lesson.ReceiptKindCorrection,
				Requested: changedSet,
			})
			if rerr != nil || rec.ID <= 0 {
				tm.recorder.Emit(EventError, infra.F("code", ErrCodeProviderFailed), infra.F("at", "save_receipt"))
			} else {
				receiptID = rec.ID
			}
		}

		wres, err := tm.cfg.Words.SubmitReview(ctx, in.changed, in.category)
		switch {
		case err != nil:
			// Honest failure: the correction was NOT saved — never a
			// narrated success (transparency prohibition).
			saveFailed = true
			tm.recorder.Emit(EventError, infra.F("code", ErrCodeProviderFailed))
		default:
			updated = len(wres.Updated)
			tm.recorder.Emit(EventReviewCorrection,
				infra.F("changed", requested),
				infra.F("updated", updated),
			)
			if updated < requested {
				// Partial/zero update with nil error is not success (S-05).
				tm.recorder.Emit(EventError, infra.F("code", ErrCodeProviderFailed))
			}
		}

		// 13.3-01 receipt phase two: the REAL diff outcome (error → failed
		// with missing = the full diff set; nil error → saved with the real
		// sets).
		if tm.cfg.Receipts != nil && receiptID > 0 {
			var cerr error
			if err != nil {
				cerr = tm.cfg.Receipts.Complete(ctx, receiptID, nil, changedSet, false)
			} else {
				cerr = tm.cfg.Receipts.Complete(ctx, receiptID, wres.Updated, wres.Missing, true)
			}
			if cerr != nil {
				tm.recorder.Emit(EventError, infra.F("code", ErrCodeProviderFailed), infra.F("at", "save_receipt"))
			}
		}
	}

	// Clear the D-03 marker: idempotent, deterministic key. It runs even on
	// a failed/empty diff submit? NO — on a FAILED review write the row
	// keeps its unchecked marker so the next session can retry the
	// correction; only a clean run (or an empty diff) closes the loop.
	if !saveFailed {
		if _, err := tm.exec.ClearUnchecked(ctx, in.lessonID, in.version, in.lessonID+"-clear-unchecked"); err != nil {
			// A failed marker reset leaves the row re-openable for
			// correction — honest, recoverable, never retried blindly.
			tm.recorder.Emit(EventError,
				infra.F("code", ErrCodeProviderFailed),
				infra.F("at", "clear_unchecked"),
			)
		}
	}

	tm.mu.Lock()
	tm.tutor.version = in.version
	tm.tutor.checking = false
	tm.tutor.reviewSeen = true
	tm.mu.Unlock()

	result := ReviewResultMsg{
		Type:      TypeReviewResult,
		Requested: requested,
		Updated:   updated,
		Missing:   0,
		Unchecked: false,
	}
	if saveFailed {
		result.Updated = 0
		result.Missing = requested
	}
	_ = s.SendControl(result)

	summary := checkedSummaryText(in.total, in.correct, result.Updated, requested, saveFailed)
	tm.runNaturalEnding(s, ctx, cancel, summary, purposeSummary, ReasonLessonComplete)
}

// ---------------------------------------------------------------------------
// D-03 close-path fallback (13.1-04): a session that goes away while the
// tutor still holds stage='checking' persists the behavior rating degraded
// by ONE level plus the row's unchecked marker, so the lesson reads as
// "never checked" and a later session can reopen the correction path.
// ---------------------------------------------------------------------------

// uncheckedCloseSnapshot is the immutable input of the D-03 close-path
// fallback: copied under tm.mu while the classroom is still open, so the
// lock-free persistence never reads or mutates the closed session's tutor.
type uncheckedCloseSnapshot struct {
	lessonID string
	version  int
	category string
	poem     bool
	words    []lesson.Word
}

// ownUncheckedFallback grants the ONE-TIME close-path fallback ownership to
// this caller (the first closer wins) and snapshots its immutable input in
// the same lock hold. false means another close already owns the fallback,
// the session is already closed, or the classroom was never eligible (no
// tutor, or not in checking).
func (tm *TurnManager) ownUncheckedFallback() (uncheckedCloseSnapshot, bool) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	if tm.closeOwned || tm.closed || tm.tutor == nil || !tm.tutor.checking {
		return uncheckedCloseSnapshot{}, false
	}
	tm.closeOwned = true
	t := tm.tutor
	return uncheckedCloseSnapshot{
		lessonID: t.lessonID,
		version:  t.version,
		category: t.category,
		poem:     t.poem != nil,
		words:    append([]lesson.Word(nil), t.words...),
	}, true
}

// persistUncheckedFallback runs the OWNED fallback OUTSIDE tm.mu once the
// session is already marked closed (the server-side write completes after
// the connection is gone); it sends NO client messages, and the dead tutor
// is neither read nor written — the snapshot plus the STORED row are the
// authority. Idempotency (T-13.1-09): CompleteCheck's first-commit branch is
// the ONLY write point — the DB latch refuses any second submission, and
// replay/conflict branches do nothing, so repeated closes never double-write
// the review. A failed write is logged as an error event, never retried
// blindly (S-05): the row keeps stage='checking' and the next session's
// resume path still finds it intact.
func (tm *TurnManager) persistUncheckedFallback(snap uncheckedCloseSnapshot) {
	if snap.poem {
		// A poem has no behavior-derived fallback. Keep stage=checking so a
		// refresh or disconnect reopens the same ordered lines and preserves
		// the human confirmation work already stored on the lesson row.
		tm.recorder.Emit(EventUncheckedFallback, infra.F("requested", 0), infra.F("updated", 0), infra.F("poem", true))
		return
	}

	ctx := context.Background()
	res, err := tm.exec.CompleteCheck(ctx, snap.lessonID, snap.version, snap.lessonID+"-complete-check", true)
	if err != nil {
		tm.recorder.Emit(EventError,
			infra.F("code", ErrCodeProviderFailed),
			infra.F("at", "unchecked_fallback"),
		)
		return
	}
	if res.Idempotent {
		// The latch already fell (a racing check_submit or a prior close
		// wrote first): never a second SubmitReview (T-13.1-09).
		return
	}

	// First commit: the fallback batch derives from the STORED outcomes
	// readback — never from session memory (the row is the authority).
	ratings := uncheckedRatings(snap.words, res.Outcomes)
	requested, updated := len(ratings), 0
	if len(ratings) > 0 {
		// 13.3-01 receipt: the fallback's own kind, keyed by the CompleteCheck
		// latch id (lessonID+"-complete-check"). Mounted AFTER the
		// res.Idempotent guard — the replay path above stays receipt-free by
		// construction (behavior: no attempt, no receipt).
		receiptID := int64(0)
		requestedSet := sortedWordSet(ratings)
		if tm.cfg.Receipts != nil {
			rec, rerr := tm.cfg.Receipts.Begin(ctx, lesson.ReceiptInput{
				LessonID:  snap.lessonID,
				RequestID: snap.lessonID + "-complete-check",
				Kind:      lesson.ReceiptKindUncheckedFallback,
				Requested: requestedSet,
			})
			if rerr != nil || rec.ID <= 0 {
				tm.recorder.Emit(EventError, infra.F("code", ErrCodeProviderFailed), infra.F("at", "save_receipt"))
			} else {
				receiptID = rec.ID
			}
		}

		wres, werr := tm.cfg.Words.SubmitReview(ctx, ratings, snap.category)
		switch {
		case werr != nil:
			tm.recorder.Emit(EventError, infra.F("code", ErrCodeProviderFailed))
		default:
			updated = len(wres.Updated)
			if updated < requested {
				// Partial/zero update with nil error is not success (S-05).
				tm.recorder.Emit(EventError, infra.F("code", ErrCodeProviderFailed))
			}
		}

		// 13.3-01 receipt completion: the REAL fallback outcome. The close
		// path has no Sender — receipt errors ride the same bypass event
		// (T-13.3-03: single-row writes, same pool, no new external calls).
		if tm.cfg.Receipts != nil && receiptID > 0 {
			var cerr error
			if werr != nil {
				cerr = tm.cfg.Receipts.Complete(ctx, receiptID, nil, requestedSet, false)
			} else {
				cerr = tm.cfg.Receipts.Complete(ctx, receiptID, wres.Updated, wres.Missing, true)
			}
			if cerr != nil {
				tm.recorder.Emit(EventError, infra.F("code", ErrCodeProviderFailed), infra.F("at", "save_receipt"))
			}
		}
	}
	tm.recorder.Emit(EventUncheckedFallback,
		infra.F("requested", requested),
		infra.F("updated", updated),
	)
}

// ---------------------------------------------------------------------------
// OCR check pass (13.1-03, D-05/D-06①): photo → per-word recognition →
// GradeWord judgement → verdict PREFILL (manual=false) → echo + ocr_result.
// OCR never bypasses the executor, and the submit gate still demands a
// manual verdict for every word — a zero-human-action submit is impossible
// server-side (TestOcrSubmitRequiresManualConfirmation pins it).
// ---------------------------------------------------------------------------

// wordLang picks the grading normalization regime for one word: Han
// characters select the zh regime, everything else en (the same per-word
// script detection maskTranscript uses).
func wordLang(word string) string {
	for _, r := range word {
		if unicode.Is(unicode.Han, r) {
			return "zh"
		}
	}
	return "en"
}

// ocrJudgements matches OCR readings to the expected words IN ORDER (the
// readings are positional per the prompt) and grades each with
// grading.GradeWord — the single judgement engine (Anti-Pattern: never
// re-implement normalization here). Only the boolean Correct/NearMiss
// outcomes are consumed; grading's j.Rating is the stale single-word
// semantics and is never read (13.1 rates through checkRating after the
// human verdict). The OCR text is untrusted model output: it is rune-capped
// here before it can enter any message or the verdicts JSON, and it never
// enters the timing-event stream. Empty readings produce no entry — the
// word stays for manual confirmation instead of prefilling a guess.
func ocrJudgements(expected []string, readings []ocr.WordReading) []OcrResultEntry {
	n := len(expected)
	if len(readings) < n {
		n = len(readings)
	}
	entries := make([]OcrResultEntry, 0, n)
	for i := 0; i < n; i++ {
		text := truncateRunes(strings.TrimSpace(readings[i].Text), maxReplyRunes)
		if text == "" {
			continue
		}
		j := grading.GradeWord(expected[i], text, wordLang(expected[i]))
		entries = append(entries, OcrResultEntry{
			WordIndex: i,
			OcrText:   text,
			Correct:   j.Correct,
			NearMiss:  j.NearMiss,
		})
	}
	return entries
}

// manualVerdictFor reports whether the stored verdict list already carries a
// MANUAL verdict for the index (D-06①: a human verdict that landed before a
// prefill attempt wins — the prefill for that index is dropped).
func manualVerdictFor(verdicts []lesson.CheckVerdict, index int) bool {
	for _, v := range verdicts {
		if v.WordIndex == index {
			return v.Manual
		}
	}
	return false
}

// runCheckPhoto runs the OCR check pass over one assembled photo (13.1-03)
// on its own goroutine: vision OCR → per-word judgement → prefill → the
// authoritative check_verdicts echo + ocr_result. Low-confidence or failed
// recognition never blocks the manual path — the student just confirms
// every word by hand (D-05: the human pass is mandatory anyway). The vision
// call is bounded by TurnManagerConfig.PhotoRequestTimeout (Issue #44: the
// photo config block's RequestTimeout, injected by main.go).
func (tm *TurnManager) runCheckPhoto(s Sender, jpeg []byte) {
	ctx, cancel := context.WithTimeout(context.Background(), tm.cfg.PhotoRequestTimeout)
	defer cancel()

	tm.mu.Lock()
	pipeline := tm.ocrPipeline
	isPoem := tm.tutor != nil && tm.tutor.poem != nil
	var expected []string
	if tm.tutor != nil && !tm.closed && tm.tutor.checking {
		expected = make([]string, len(tm.tutor.words))
		for i, c := range tm.tutor.words {
			expected[i] = c.Word
		}
	}
	tm.mu.Unlock()
	// The session closed (or left the check stage) while the upload was in
	// flight: the photo is gone, nothing to do.
	if pipeline == nil || len(expected) == 0 {
		return
	}

	// Issue #12: a poem page transcribes per VERSE LINE (one written row per
	// line); a word page stays per word. Both grade positionally below.
	var readings []ocr.WordReading
	var err error
	if isPoem {
		readings, err = pipeline.ProcessLines(ctx, jpeg, expected)
	} else {
		readings, err = pipeline.ProcessWords(ctx, jpeg, expected)
	}
	if err != nil && !errors.Is(err, ocr.ErrLowConfidence) {
		// OCR genuinely failed: visible honest notice; the manual check
		// path continues untouched.
		tm.recorder.Emit(EventError,
			infra.F("code", ErrCodeProviderFailed),
			infra.F("photo", "ocr_failed"),
		)
		tm.sendError(s, ErrCodeProviderFailed, photoOcrFailedNotice, true)
		return
	}
	lowConf := errors.Is(err, ocr.ErrLowConfidence)

	entries := ocrJudgements(expected, readings)
	recognized := 0
	for _, r := range readings {
		if strings.TrimSpace(r.Text) != "" {
			recognized++
		}
	}
	tm.recorder.Emit(EventOcrCompleted,
		infra.F("recognized", recognized),
		infra.F("total", len(expected)),
	)

	if lowConf {
		// The gate fired (the pipeline owns the floor): recognition is too
		// shaky to prefill ANY verdict — the student confirms every word
		// manually. The recognized text still displays next to each row.
		tm.broadcastOcrEntries(s, entries)
		return
	}
	tm.applyOcrPrefills(s, entries)
}

// applyOcrPrefills writes each OCR judgement as a verdict prefill through
// the executor's idempotent ApplyVerdict path (RT-07: OCR, screen taps, and
// the 13.1-06 voice corrections all converge on this one write path), then
// broadcasts the authoritative verdict echo plus ocr_result. Per word:
//   - optimistic-lock conflict → align from the stored truth; a manual
//     verdict that landed first wins (D-06①) and the prefill is dropped;
//     otherwise retry once on the fresh version;
//   - store failure → stop prefilling (what landed stays); the student
//     confirms the rest manually.
//
// A non-conflict success can never clobber a manual verdict: the optimistic
// lock guarantees no other writer moved the row since our snapshot.
func (tm *TurnManager) applyOcrPrefills(s Sender, entries []OcrResultEntry) {
	tm.mu.Lock()
	tm.ocrRuns++
	lessonID := tm.tutor.lessonID
	version := tm.tutor.version
	run := tm.ocrRuns
	tm.mu.Unlock()

	ctx := context.Background()
	for _, e := range entries {
		verdict := lesson.CheckVerdict{
			WordIndex: e.WordIndex,
			Correct:   e.Correct,
			Manual:    false, // OCR prefill — never satisfies the submit gate
			OcrText:   e.OcrText,
		}
		// Unique per OCR run: a deterministic per-word key would be replayed
		// idempotently on a retake, silently pinning the FIRST reading.
		requestID := lessonID + "-ocr-" + strconv.FormatUint(run, 10) + "-" + strconv.Itoa(e.WordIndex)

		res, err := tm.exec.ApplyVerdict(ctx, lessonID, version, requestID, verdict)
		if err != nil {
			var conflict *lesson.ConflictError
			if !errors.As(err, &conflict) {
				tm.recorder.Emit(EventError, infra.F("code", ErrCodeProviderFailed))
				break
			}
			l, gerr := tm.exec.GetLessonState(ctx, lessonID)
			if gerr != nil {
				tm.recorder.Emit(EventError, infra.F("code", ErrCodeProviderFailed))
				break
			}
			tm.mu.Lock()
			tm.tutor.align(l.CurrentIndex, l.Version)
			tm.tutor.resyncCheck(l)
			tm.mu.Unlock()
			if manualVerdictFor(l.Verdicts, e.WordIndex) {
				continue // the human verdict landed first — it stands
			}
			version = l.Version
			res, err = tm.exec.ApplyVerdict(ctx, lessonID, version, requestID, verdict)
			if err != nil {
				continue // give up on this word; manual confirmation covers it
			}
		}
		version = res.NewVersion
		tm.mu.Lock()
		tm.tutor.version = version
		tm.tutor.checkVerdicts = res.Verdicts
		tm.mu.Unlock()
	}

	tm.mu.Lock()
	verdicts := append([]lesson.CheckVerdict(nil), tm.tutor.checkVerdicts...)
	tm.mu.Unlock()
	tm.broadcastCheckVerdicts(s, verdicts)
	tm.broadcastOcrEntries(s, entries)
}

// broadcastOcrEntries sends the per-word recognition entries (server →
// client) so the UI can show what was recognized next to each row.
func (tm *TurnManager) broadcastOcrEntries(s Sender, entries []OcrResultEntry) {
	_ = s.SendControl(OcrResultMsg{Type: TypeOcrResult, Entries: entries})
}

// ---------------------------------------------------------------------------
// Check-stage voice corrections (13.1-06, D-06②): the student speaks the
// per-word verdicts («第 N 个词对了») and the actual written forms («写的
// 是 aple»). The spoken utterance classifies through the CHECK prompt
// (buildCheckClassifySystemPrompt, Pitfall 8) and the verdict lands through
// the SAME ApplyVerdict write the screen buttons use (RT-07 — voice, taps
// and OCR prefills converge on one idempotent path). The LLM owns intent and
// wording only: intent→verdict is server-side deterministic logic, and the
// model's index/text payload is validated server-side before anything is
// written (T-13.1-13).
// ---------------------------------------------------------------------------

// checkVoiceClarifyText is the deterministic fallback when the spoken
// verdict's payload is unusable (missing/out-of-range index, spelling line
// without the actual text): the teacher asks again instead of guessing —
// no write, no fabricated verdict (T-13.1-13).
const checkVoiceClarifyText = "你说的是第几个词？再说一次好吗？"

// checkResumeAckText is the check-stage resume ack: the pause bit clears but
// there is no current word to re-announce in the check stage (the dictation
// resume line names one) — the panel simply continues.
func checkResumeAckText() string {
	return "好，我们继续检查。"
}

// verdictAckText is the teacher's spoken ack after a voice right/wrong
// verdict lands: it names the marked position (D-06②) with encouragement
// only (prohibition P1).
func verdictAckText(idx int, correct bool) string {
	if correct {
		return fmt.Sprintf("好，第 %d 个词对了，你真棒！", idx)
	}
	return fmt.Sprintf("好，第 %d 个词错了，我们在纸上把它改一改吧。", idx)
}

// spellingFeedbackText builds the teacher's line after a spoken spelling
// correction («写的是 aple»): the GradeWord diff drives the tone — an exact
// match confirms the word is right; a near miss gets the 差一个字母很接近了
// encouragement that names nothing shameful (prohibition P1); a larger
// distance gets a gentle let's-look-again line that SPOKEN-quotes the
// correct word (the check stage is unmasked — speaking the word is fine).
func spellingFeedbackText(word, actual string, j grading.WordJudgment) string {
	switch {
	case j.Correct:
		return fmt.Sprintf("对，%s 写的就是 %s，这个词写对了，真棒！", word, actual)
	case j.NearMiss:
		return fmt.Sprintf("%s 你写成了 %s，差一个字母，很接近了！把它改过来就更棒啦。", word, actual)
	default:
		return fmt.Sprintf("我们再看看 %s 这个词：你写的是 %s，把它改过来吧。", word, actual)
	}
}

// buildCheckClassifyMessages assembles the CHECK stage's classification
// request: the check prompt (full word list + verdict states + the extended
// output contract) plus one user message carrying the transcript and the
// same FSM short-history deque the dictation path uses.
func (tm *TurnManager) buildCheckClassifyMessages(transcript string) []mimo.Message {
	var user strings.Builder
	user.WriteString("学生说：")
	user.WriteString(transcript)
	tm.mu.Lock()
	var system string
	if tm.tutor != nil {
		words := append([]lesson.Word(nil), tm.tutor.words...)
		verdicts := append([]lesson.CheckVerdict(nil), tm.tutor.checkVerdicts...)
		system = buildCheckClassifySystemPrompt(words, verdicts)
		tm.tutor.appendShortHistoryLocked(&user)
	}
	tm.mu.Unlock()
	return []mimo.Message{
		{Role: "system", Content: system},
		{Role: "user", Content: user.String()},
	}
}

// classifyCheckTurn is the CHECK stage's per-turn classification: the same
// ONE-call-plus-ONE-retry discipline as classifyTurn (invalid-response
// discipline), the same EventIntentClassified emission, then the check
// dispatch. A second unusable output collapses deterministically to chat
// with the fixed fallback wording.
func (tm *TurnManager) classifyCheckTurn(s Sender, ctx context.Context, cancel context.CancelFunc, turnID, epoch uint64, transcript string) {
	msgs := tm.buildCheckClassifyMessages(transcript)
	p, ok := tm.classifyOncePayload(ctx, msgs)
	if !ok {
		msgs = append(msgs, mimo.Message{
			Role:    "user",
			Content: "你刚才的输出不符合要求。请只输出一个 JSON 对象，键为 intent 与 reply（检查口令再加 index 与 text），不要输出任何其它内容。",
		})
		p, ok = tm.classifyOncePayload(ctx, msgs)
	}
	if !ok {
		p = intentPayload{Intent: intentChat, Reply: classifyFallbackReply}
		// The LLM half of the invalid-response discipline: exactly one
		// correction retry, then the deterministic spoken fallback.
		tm.recorder.Emit(EventError, infra.F("code", ErrCodeProviderFailed), infra.F("attempts", 2))
	}
	// Desensitization boundary (T-13-06b): the intent label only.
	tm.recorder.Emit(EventIntentClassified, infra.F("intent", p.Intent))
	tm.checkDispatch(s, ctx, cancel, turnID, epoch, p)
}

// checkDispatch routes one classified check-stage turn. The three check
// intents converge on the verdict write; every other intent keeps the
// dictation dispatch semantics (repeat/slow re-read, pause/resume own the
// pause bit, meaning/chat speak the reply) against a NO-TARGET marker —
// the check stage has no advance target, and confirm/hint degrade to plain
// announcements there. An empty model reply is substituted with the fixed
// fallback so every reply-driven branch announces and settles (a bare
// return here would strand the FSM mid-turn).
func (tm *TurnManager) checkDispatch(s Sender, ctx context.Context, cancel context.CancelFunc, turnID, epoch uint64, p intentPayload) {
	switch p.Intent {
	case intentCheckRight, intentCheckWrong, intentCheckSpelling:
		tm.applyVoiceVerdict(s, ctx, cancel, epoch, p)
	case intentResume:
		// Resume in the check stage clears the pause bit; the dictation
		// resume line re-announces the CURRENT word, which does not exist
		// here — speak the stage-appropriate continuation instead.
		tm.mu.Lock()
		if tm.tutor != nil {
			tm.tutor.paused = false
		}
		tm.mu.Unlock()
		tm.runAnnouncement(s, ctx, cancel, checkResumeAckText(), purposeChat, nil)
	default:
		reply := p.Reply
		if reply == "" {
			reply = classifyFallbackReply
		}
		tm.dispatchTurn(s, ctx, cancel, turnID, epoch, p.Intent, reply, TurnTarget{WordIndex: -1})
	}
}

// applyVoiceVerdict validates the model's payload and lands the spoken
// verdict through the executor's idempotent ApplyVerdict — the SAME write
// the screen taps use (RT-07):
//   - invalid payload (index outside 1..len(words), or a spelling line
//     without the actual text): the clarification question announces, NO
//     write — the model is never guessed around (T-13.1-13);
//   - check_right/check_wrong: manual=true with the intent's correctness;
//   - check_spelling: the spoken written form is graded with GradeWord (the
//     single judgement engine); correctness follows the GRADE — the child
//     just told us what is on the paper, and grading it is server-side
//     deterministic logic, never model discretion. The verdict carries the
//     text as OcrText and the recognized entry rides the ocr_result display
//     channel beside the row;
//   - a store conflict aligns from the stored truth and retries ONCE on the
//     fresh version; a failed write degrades through failTurn honestly.
//
// The teacher's spoken ack goes through runAnnouncement — the ACK gate the
// check stage never bypasses (Pitfall 9).
func (tm *TurnManager) applyVoiceVerdict(s Sender, ctx context.Context, cancel context.CancelFunc, epoch uint64, p intentPayload) {
	tm.mu.Lock()
	alive := tm.tutor != nil && !tm.closed && tm.tutor.checking
	lessonID := tm.tutor.lessonID
	version := tm.tutor.version
	total := len(tm.tutor.words)
	tm.mu.Unlock()
	if !alive {
		// The session closed (or left the check stage) mid-turn: nothing to
		// act on, nothing to announce.
		return
	}

	spelling := p.Intent == intentCheckSpelling
	text := truncateRunes(strings.TrimSpace(p.Text), maxReplyRunes)
	if p.Index < 1 || p.Index > total || (spelling && text == "") {
		tm.recorder.Emit(EventError,
			infra.F("code", ErrCodeInvalidMessage),
			infra.F("at", "voice_verdict"),
		)
		tm.runAnnouncement(s, ctx, cancel, checkVoiceClarifyText, purposeChat, nil)
		return
	}
	idx := p.Index - 1

	var (
		correct bool
		line    string
		entry   *OcrResultEntry
	)
	if spelling {
		word := tm.tutorWordAt(idx)
		j := grading.GradeWord(word, text, wordLang(word))
		correct = j.Correct
		line = spellingFeedbackText(word, text, j)
		entry = &OcrResultEntry{WordIndex: idx, OcrText: text, Correct: j.Correct, NearMiss: j.NearMiss}
	} else {
		correct = p.Intent == intentCheckRight
		line = verdictAckText(p.Index, correct)
	}

	verdict := lesson.CheckVerdict{WordIndex: idx, Correct: correct, Manual: true}
	if spelling {
		verdict.OcrText = text
	}

	// Server-generated idempotency key, unique per voice mutation: the
	// version read fresh under the lock is baked in, and every successful
	// ApplyVerdict bumps the version — so a later spoken flip of the same
	// word never replays the earlier write (a replayed key would silently
	// pin the FIRST verdict, the OCR retake lesson).
	requestID := fmt.Sprintf("%s-verdict-%d-v%d-voice", lessonID, idx, version)
	res, err := tm.exec.ApplyVerdict(ctx, lessonID, version, requestID, verdict)
	if err != nil {
		var conflict *lesson.ConflictError
		if !errors.As(err, &conflict) {
			tm.failTurn(s, epoch, ErrCodeProviderFailed, checkStoreFailedNotice)
			return
		}
		// Conflict: align from the stored truth and retry once on the fresh
		// version (same discipline as the OCR prefill path).
		l, gerr := tm.exec.GetLessonState(ctx, lessonID)
		if gerr != nil {
			tm.failTurn(s, epoch, ErrCodeProviderFailed, checkStoreFailedNotice)
			return
		}
		tm.mu.Lock()
		tm.tutor.align(l.CurrentIndex, l.Version)
		tm.tutor.resyncCheck(l)
		tm.mu.Unlock()
		version = l.Version
		requestID = fmt.Sprintf("%s-verdict-%d-v%d-voice", lessonID, idx, version)
		res, err = tm.exec.ApplyVerdict(ctx, lessonID, version, requestID, verdict)
		if err != nil {
			tm.failTurn(s, epoch, ErrCodeProviderFailed, checkStoreFailedNotice)
			return
		}
	}

	tm.mu.Lock()
	tm.tutor.version = res.NewVersion
	tm.tutor.checkVerdicts = res.Verdicts
	verdicts := append([]lesson.CheckVerdict(nil), res.Verdicts...)
	tm.mu.Unlock()
	tm.broadcastCheckVerdicts(s, verdicts)
	if entry != nil {
		_ = s.SendControl(OcrResultMsg{Type: TypeOcrResult, Entries: []OcrResultEntry{*entry}})
	}
	if !res.Idempotent {
		tm.recorder.Emit(EventVerdictApplied,
			infra.F("word_index", idx),
			infra.F("manual", true),
			infra.F("correct", correct),
		)
	}
	tm.runAnnouncement(s, ctx, cancel, line, purposeChat, nil)
}

// tutorWordAt snapshots one lesson word (empty when out of range). Caller
// does NOT hold tm.mu — the single-lock discipline applies to every field.
func (tm *TurnManager) tutorWordAt(idx int) string {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	if tm.tutor == nil || idx < 0 || idx >= len(tm.tutor.words) {
		return ""
	}
	return tm.tutor.words[idx].Word
}
