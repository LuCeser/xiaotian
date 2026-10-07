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
	"strconv"
	"strings"
	"unicode"

	"github.com/pkg/errors"

	"github.com/xiaotian/home-tutor-agent/pkg/grading"
	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
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

// checkedSummaryText builds the post-submit battle summary (D-04): the
// 对/错 battle count first, encouraging wording only (no shaming on wrong
// words, prohibition P1), then the honest save note — the failure suffix
// when the write errored, the M/N note when fewer words saved than
// requested (S-05: never a fabricated full success).
func checkedSummaryText(total, correct, updated, requested int, saveFailed bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "今天 %d 个词对了 %d 个。", total, correct)
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
	tm.tutor.markCompleted()  // word pacing is over; the countdown stays disarmed
	tm.stopWordTimerLocked()  // no per-word timer in the check stage
	lessonID := tm.tutor.lessonID
	expected := tm.tutor.version
	outcomes := tm.tutor.snapshotCheckOutcomes()
	total := len(tm.tutor.words)
	words := make([]string, 0, total) // the FULL plaintext list (D-10 unmask)
	for _, c := range tm.tutor.words {
		words = append(words, c.Word)
	}
	tm.mu.Unlock()

	res, err := tm.exec.EnterCheck(ctx, lessonID, expected, lessonID+"-enter-check", outcomes)
	switch {
	case err == nil:
		tm.mu.Lock()
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
		tm.tutor.align(l.CurrentIndex, l.Version)
		tm.tutor.resyncCheck(l)
		tm.mu.Unlock()
	}

	tm.recorder.Emit(EventCheckStarted, infra.F("total", total))
	_ = s.SendControl(CheckStartedMsg{
		Type:   TypeCheckStarted,
		Words:  words,
		Reason: CheckStartedReasonDictationDone,
	})

	tm.runAnnouncement(s, ctx, cancel, checkBridgeText(total), purposeCheckBridge, nil)
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
				infra.F("code", ErrCodeProviderFailed),
				infra.F("duplicate", "check_submit_conflict"),
			)
			return
		}
		tm.sendError(s, ErrCodeProviderFailed, checkStoreFailedNotice, true)
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

	go tm.finishCheckSubmit(s, ctx, cancel, ratings, total, correct)
}

// finishCheckSubmit runs on its own goroutine (D-04 order): ONE SubmitReview
// call whose REAL result sets shape the honest review_result message and the
// battle summary; runNaturalEnding then speaks the summary and waits for its
// valid playback ack before session_ended(lesson_complete). This is the ONLY
// SubmitReview call site the check stage owns.
func (tm *TurnManager) finishCheckSubmit(s Sender, ctx context.Context, cancel context.CancelFunc, ratings map[string]string, total, correct int) {
	defer cancel()

	// Dictation-stage behavior data stays honestly reported (the D-01
	// rewrite keeps outcomeCounts/hintAccounting as event fields).
	tm.mu.Lock()
	confirmed, timedOutN, hinted := tm.tutor.outcomeCounts()
	hintWords, hintTotal := tm.tutor.hintAccounting()
	tm.mu.Unlock()

	var (
		requested  = len(ratings)
		updated    = 0
		missing    = 0
		saveFailed = false
	)
	if len(ratings) > 0 {
		wres, err := tm.cfg.Words.SubmitReview(ctx, ratings)
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
// every word by hand (D-05: the human pass is mandatory anyway).
func (tm *TurnManager) runCheckPhoto(s Sender, jpeg []byte) {
	ctx, cancel := context.WithTimeout(context.Background(), photoRequestTimeout)
	defer cancel()

	tm.mu.Lock()
	pipeline := tm.ocrPipeline
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

	readings, err := pipeline.ProcessWords(ctx, jpeg, expected)
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
