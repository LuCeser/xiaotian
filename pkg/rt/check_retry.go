package rt

import (
	"context"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
)

// handleCheckReviewRetry serves a second check_submit after the first
// finishCheckSubmit pass (reviewSeen, not checking). Returns true when the
// frame is consumed. The in-flight marker is taken BEFORE the receipt is
// read (#96 review): a retry owns the marker across the whole read-decide-
// write span, so a concurrent submit either sees the marker and stops, or
// reads a receipt no in-flight retry can silently supersede.
func (tm *TurnManager) handleCheckReviewRetry(s Sender, m CheckSubmitMsg) bool {
	if m.RequestID == "" {
		tm.sendError(s, ErrCodeInvalidMessage, checkVerdictInvalidNotice, true)
		return true
	}
	tm.mu.Lock()
	lessonID := tm.tutor.lessonID
	total := len(tm.tutor.words)
	prior := tm.tutor.reviewResult
	if tm.checkRetryBusy {
		tm.mu.Unlock()
		return true
	}
	if tm.cfg.Receipts == nil {
		tm.mu.Unlock()
		if prior.Type != "" {
			_ = s.SendControl(prior)
		}
		return true
	}
	tm.checkRetryBusy = true
	tm.mu.Unlock()

	// release hands the marker back on every path that does not write; the
	// write path keeps it until its goroutine settles.
	release := func() {
		tm.mu.Lock()
		tm.checkRetryBusy = false
		tm.mu.Unlock()
	}

	rec, ok := tm.latestCheckReceipt(context.Background(), lessonID)
	if !ok {
		release()
		if prior.Type != "" {
			_ = s.SendControl(prior)
		}
		return true
	}

	if rec.Status == lesson.ReceiptStatusPending {
		// 待核实 is not retryable (#96 Q4): the Python side may already have
		// committed — replay the pending receipt, never a second write.
		release()
		_ = s.SendControl(ReviewResultMsg{
			Type:      TypeReviewResult,
			Requested: len(rec.Requested),
			Updated:   0,
			Missing:   len(rec.Requested),
			Pending:   true,
		})
		return true
	}

	missingWords := append([]string(nil), rec.Missing...)
	if rec.Status == lesson.ReceiptStatusFailed {
		missingWords = append([]string(nil), rec.Requested...)
	}
	if len(missingWords) == 0 {
		// Full save (#96 review): replay the receipt ONLY. Re-running the
		// summary ending here would speak it a second time and park this
		// goroutine on a sentence_ack the read loop cannot serve.
		release()
		_ = s.SendControl(tm.reviewResultFromReceipt(rec))
		return true
	}

	tm.mu.Lock()
	wordsCopy := make([]lesson.Word, len(tm.tutor.words))
	copy(wordsCopy, tm.tutor.words)
	verdicts := append([]lesson.CheckVerdict(nil), tm.tutor.checkVerdicts...)
	outcomes := tm.tutor.snapshotCheckOutcomes()
	category := tm.tutor.category
	correct := 0
	for _, v := range verdicts {
		if v.Correct {
			correct++
		}
	}
	tm.mu.Unlock()

	ratings := checkRatingsForWords(wordsCopy, verdicts, outcomes, missingWords)
	if len(ratings) == 0 {
		release()
		return true
	}

	// Register the retry's cancel as the turn cancel (#96 review P2-2): an
	// end_session, a disconnect, or an interrupt must reach the in-flight
	// write exactly as it reaches the first submit's — and the epoch captured
	// here is the staleness baseline for everything the retry sends later.
	retryCtx, cancel := context.WithCancel(context.Background())
	tm.mu.Lock()
	retryEpoch := tm.epoch
	tm.turnCancel = cancel
	tm.mu.Unlock()
	go func() {
		defer cancel()
		defer release()
		tm.retryCheckReview(s, retryCtx, cancel, retryEpoch, m.RequestID, lessonID, category, ratings, total, correct)
	}()
	return true
}

// reviewStaleAt reports whether the session closed or the epoch moved since a
// write began — a review completing after end_session / disconnect /
// interrupt must not touch tutor state nor send any control behind the
// terminal session_ended (#96 review P2-2).
func (tm *TurnManager) reviewStaleAt(epoch uint64) bool {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	return tm.closed || tm.epoch != epoch
}

func (tm *TurnManager) latestCheckReceipt(ctx context.Context, lessonID string) (lesson.Receipt, bool) {
	rows, err := tm.cfg.Receipts.ListByLesson(ctx, lessonID)
	if err != nil || len(rows) == 0 {
		return lesson.Receipt{}, false
	}
	for i := len(rows) - 1; i >= 0; i-- {
		if rows[i].Kind == lesson.ReceiptKindCheck {
			return rows[i], true
		}
	}
	return lesson.Receipt{}, false
}

func (tm *TurnManager) reviewResultFromReceipt(rec lesson.Receipt) ReviewResultMsg {
	req := len(rec.Requested)
	upd := len(rec.Updated)
	miss := len(rec.Missing)
	if rec.Status == lesson.ReceiptStatusFailed {
		upd = 0
		miss = req
	}
	return ReviewResultMsg{
		Type:      TypeReviewResult,
		Requested: req,
		Updated:   upd,
		Missing:   miss,
	}
}

func checkRatingsForWords(words []lesson.Word, verdicts []lesson.CheckVerdict, outcomes map[string]lesson.WordOutcome, want []string) map[string]string {
	set := make(map[string]struct{}, len(want))
	for _, w := range want {
		set[w] = struct{}{}
	}
	all := checkRatings(words, verdicts, outcomes)
	out := make(map[string]string, len(want))
	for w, r := range all {
		if _, ok := set[w]; ok {
			out[w] = r
		}
	}
	return out
}

func (tm *TurnManager) retryCheckReview(s Sender, ctx context.Context, cancel context.CancelFunc, epoch uint64, requestID, lessonID, category string, ratings map[string]string, total, correct int) {
	defer cancel()
	requestedSet := sortedWordSet(ratings)
	receiptID := int64(0)
	if tm.cfg.Receipts != nil {
		rec, rerr := tm.cfg.Receipts.Begin(ctx, lesson.ReceiptInput{
			LessonID:  lessonID,
			RequestID: requestID,
			Kind:      lesson.ReceiptKindCheck,
			Requested: requestedSet,
		})
		if rerr != nil || rec.ID <= 0 {
			// No receipt row, no dedup basis for a later tap: answer an
			// honest zero-updated review_result and keep the panel open —
			// the write itself must NOT run (#96 review P2-3).
			if tm.reviewStaleAt(epoch) {
				return
			}
			tm.recorder.Emit(EventError, infra.F("code", ErrCodeProviderFailed), infra.F("at", "save_receipt"))
			result := ReviewResultMsg{
				Type:      TypeReviewResult,
				Requested: len(ratings),
				Updated:   0,
				Missing:   len(ratings),
			}
			tm.mu.Lock()
			tm.tutor.reviewResult = result
			tm.mu.Unlock()
			_ = s.SendControl(result)
			tm.enterGradingAt(s, epoch)
			return
		}
		receiptID = rec.ID
	}

	var (
		requested  = len(ratings)
		updated    = 0
		missing    = 0
		saveFailed = false
	)
	wres, err := tm.cfg.Words.SubmitReview(ctx, ratings, category)
	switch {
	case err != nil:
		saveFailed = true
		tm.recorder.Emit(EventError, infra.F("code", ErrCodeProviderFailed))
	default:
		updated, missing = len(wres.Updated), len(wres.Missing)
		tm.recorder.Emit(EventReviewSubmitted,
			infra.F("requested", requested),
			infra.F("updated", updated),
			infra.F("missing", missing),
		)
	}

	if tm.cfg.Receipts != nil && receiptID > 0 {
		if err != nil {
			_ = tm.cfg.Receipts.Complete(ctx, receiptID, nil, requestedSet, false)
		} else {
			_ = tm.cfg.Receipts.Complete(ctx, receiptID, wres.Updated, wres.Missing, true)
		}
	}

	// The write may outlive the session that asked for it (a blocked
	// provider released after end_session / disconnect / interrupt): nothing
	// may trail the terminal session_ended — no state mutation, no
	// review_result, no second ending (#96 review P2-2).
	if tm.reviewStaleAt(epoch) {
		return
	}

	result := ReviewResultMsg{
		Type:      TypeReviewResult,
		Requested: requested,
		Updated:   updated,
		Missing:   missing,
	}
	if saveFailed {
		result.Updated = 0
		result.Missing = requested
	}
	tm.mu.Lock()
	tm.tutor.reviewResult = result
	tm.mu.Unlock()
	_ = s.SendControl(result)

	if !saveFailed && updated == requested && requested > 0 {
		// This goroutine is off the read loop, so the ending's ack wait can
		// be served; the retry's own registered ctx/cancel ride along so a
		// close during the summary cancels the wait.
		summary := checkedSummaryText(total, correct, updated, requested, false)
		tm.runNaturalEnding(s, ctx, cancel, summary, purposeSummary, ReasonLessonComplete)
		return
	}
	tm.enterGradingAt(s, epoch)
}
