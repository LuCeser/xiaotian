package rt

import (
	"testing"
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
)

// TestCloseFallbackAfterVerdictCommit pins the close fallback's coordination
// with admitted writes (issue #69 路径 A 1.11): the fallback AWAITS the
// saves the close claim captured, then reads the AUTHORITATIVE row — never
// the snapshot's pre-close version — and competes for the latch exactly
// once on the fresh version. A verdict committed after the snapshot thus
// still gets its fallback; an already-admitted FORMAL submit owns the one
// FSRS write and the fallback saves nothing on top of the submitted row.
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

		// Transport death: the claim owns the fallback and the close
		// returns at once; the owned persistence then WAITS for the parked
		// verdict write instead of racing the snapshot's stale version.
		tm.Close(s)
		close(release)

		// The fallback's single complete_check runs on the AUTHORITATIVE
		// version the waited verdict left behind: submitted+unchecked, one
		// degraded batch — no stale first attempt, no retry ladder.
		if !waitFor(t, 5*time.Second, func() bool { return ttEventCount(t, log, EventUncheckedFallback) == 1 }) {
			t.Fatalf("unchecked_fallback events = %d, want 1", ttEventCount(t, log, EventUncheckedFallback))
		}
		if got := ttCompleteCheckCalls(exec); got != 1 {
			t.Fatalf("complete_check calls = %d, want 1 (the authoritative latch only)", got)
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
		stateReads := len(exec.stateIDCalls())

		// The transport dies inside that window: the claim still sees
		// checking=true and owns the fallback, which waits for the submit
		// to finish and then reads the row AUTHORITATIVELY — submitted
		// means the formal submit owns the save and the fallback neither
		// replays the latch nor writes FSRS.
		tm.Close(s)
		close(release)

		if !waitFor(t, 5*time.Second, func() bool { return len(words.submissions()) == 1 }) {
			t.Fatalf("review writes = %d, want exactly the formal submit's one", len(words.submissions()))
		}
		if !waitFor(t, 5*time.Second, func() bool { return len(exec.stateIDCalls()) > stateReads }) {
			t.Fatal("the fallback never read the authoritative row after waiting")
		}
		if got := ttCompleteCheckCalls(exec); got != 1 {
			t.Fatalf("complete_check calls = %d, want 1 (the formal submit's own latch)", got)
		}
		if got := ttEventCount(t, log, EventUncheckedFallback); got != 0 {
			t.Fatalf("unchecked_fallback events = %d, want 0 (the fallback yields to the submitted row)", got)
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
