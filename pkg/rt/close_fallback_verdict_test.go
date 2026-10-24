package rt

import (
	"testing"
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
)

// TestCloseFallbackAfterVerdictCommit pins the close fallback's handling of
// admitted writes that commit AFTER the close snapshot (issue #69 备选一
// 步骤 9): a verdict write that raised the row version must not silently
// kill the fallback — the authoritative row is read lock-free and the latch
// retried once — and an already-admitted FORMAL submit owns the one FSRS
// write: the fallback replays the latch idempotently and writes nothing.
func TestCloseFallbackAfterVerdictCommit(t *testing.T) {
	t.Run("a verdict committed after the snapshot still gets its fallback", func(t *testing.T) {
		words := newFakeWords(ttCards("苹果", "香蕉"))
		tm, s, log, exec := ttDriveToCheck(t, ttCards("苹果", "香蕉"), words)

		// One screen verdict is admitted and its write parks in the
		// executor's commit-vs-return window: the row already carries the
		// verdict and version+1, the session has not seen the result.
		entered, release := exec.gateApplyVerdict()
		go tm.HandleControl(s, mustJSON(t, CheckVerdictMsg{
			Type: TypeCheckVerdict, RequestID: "req-v-race", WordIndex: 0, Correct: true,
		}))
		ttAwaitGate(t, entered, "the verdict write was not entered")
		verdictEchoes := s.countOf(TypeCheckVerdicts)

		tm.Close(s) // transport death: claim owns the fallback at version v
		close(release)

		// The fallback's first latch write conflicts on the verdict's
		// version bump; the authoritative re-read retries once and the
		// fallback lands: submitted+unchecked, one degraded batch.
		if got := ttCompleteCheckCalls(exec); got != 2 {
			t.Fatalf("complete_check calls = %d, want 2 (stale snapshot + authoritative retry)", got)
		}
		if got := ttEventCount(t, log, EventUncheckedFallback); got != 1 {
			t.Fatalf("unchecked_fallback events = %d, want 1", got)
		}
		batches := words.submissions()
		if len(batches) != 1 || len(batches[0]) != 2 {
			t.Fatalf("fallback review batches = %v, want one batch of both words", batches)
		}
		for word, rating := range batches[0] {
			if rating != ratingHard {
				t.Fatalf("fallback rating[%s] = %q, want the one-level downgrade %q", word, rating, ratingHard)
			}
		}
		stage, unchecked := ttRowStage(t, exec)
		if stage != lesson.StageSubmitted || !unchecked {
			t.Fatalf("row stage=%q unchecked=%v, want submitted+unchecked", stage, unchecked)
		}
		if got := s.countOf(TypeCheckVerdicts); got != verdictEchoes {
			t.Fatalf("verdict echoes after the close = %d, want unchanged %d", got, verdictEchoes)
		}
	})

	t.Run("an admitted formal submit does not double FSRS", func(t *testing.T) {
		words := newFakeWords(ttCards("苹果", "香蕉"))
		words.reviewResult = ReviewWriteResult{
			Requested: []string{"苹果", "香蕉"}, Updated: []string{"苹果", "香蕉"},
		}
		tm, s, log, exec := ttDriveToCheck(t, ttCards("苹果", "香蕉"), words)
		ttCheckAll(t, tm, s, 2)

		// The formal submit's latch write parks in its commit-vs-return
		// window: the row already reads submitted (unchecked=false), the
		// session has not flipped its checking bit yet.
		entered, release := exec.gateCompleteCheck()
		go tm.HandleControl(s, mustJSON(t, CheckSubmitMsg{Type: TypeCheckSubmit, RequestID: "req-formal"}))
		ttAwaitGate(t, entered, "the formal submit latch was not entered")

		// The transport dies inside that window: the claim still sees
		// checking=true and owns the fallback, whose latch write replays
		// the submitted row IDEMPOTENTLY — the formal submit keeps the one
		// FSRS write.
		tm.Close(s)
		close(release)

		if !waitFor(t, 5*time.Second, func() bool { return len(words.submissions()) == 1 }) {
			t.Fatalf("review writes = %d, want exactly the formal submit's one", len(words.submissions()))
		}
		if got := ttCompleteCheckCalls(exec); got != 2 {
			t.Fatalf("complete_check calls = %d, want 2 (formal latch + fallback replay)", got)
		}
		if got := ttEventCount(t, log, EventUncheckedFallback); got != 0 {
			t.Fatalf("unchecked_fallback events = %d, want 0 (latch replay writes nothing)", got)
		}
		stage, unchecked := ttRowStage(t, exec)
		if stage != lesson.StageSubmitted || unchecked {
			t.Fatalf("row stage=%q unchecked=%v, want the formal submit's submitted+checked", stage, unchecked)
		}
		if got := s.countOf(TypeReviewResult); got != 0 {
			t.Fatalf("review_result after the close = %d, want 0", got)
		}
	})
}
