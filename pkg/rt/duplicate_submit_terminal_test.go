package rt

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ttClosedNow reads the closed flag once (lock-guarded) — the observation
// callers poll for and assert on themselves (§5.1: 等待 helper 返回观察
// 结果，不在内部 Fatal).
func ttClosedNow(tm *TurnManager) bool {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	return tm.closed
}

// ttPreFallSubmitLatch flips the lesson row's checking→submitted latch the
// way another connection's submit would (a direct executor call under a
// foreign request id), so the NEXT check_submit on this session replays the
// latch (Idempotent) and takes the duplicate-submit terminal branch.
func ttPreFallSubmitLatch(t *testing.T, tm *TurnManager, exec *fakeExecutor) {
	t.Helper()
	tm.mu.Lock()
	version := tm.tutor.version
	tm.mu.Unlock()
	if _, err := exec.CompleteCheck(context.Background(), ttLessonID, version, "other-conn-submit", false); err != nil {
		t.Fatalf("pre-fall submit latch: %v", err)
	}
}

// TestDuplicateSubmitTerminal pins the duplicate check_submit's terminal
// branch (issue #69 备选一 步骤 7): the branch goes through the unified close
// entry — the session is marked closed FIRST and the one session_ended is
// attempted second, so a refused goodbye enqueue still completes the close
// (the old send-first-then-close shape skipped it), no second save runs,
// and nothing is answered past the terminal message.
//
// 完成判据（rethink2 §1.3）: closed、进入 SendControl、attempts==1 或 writer
// 退出都不足以证明关闭流程完成——每次断言前都等该分支自己的完成观察
// （closed + 唯一一次写出尝试 + EventSessionEnd 已记录）。
func TestDuplicateSubmitTerminal(t *testing.T) {
	t.Run("concurrent pings never answer past the terminal message", func(t *testing.T) {
		words := newFakeWords(ttCards("苹果", "香蕉"))
		tm, s, log, exec := ttDriveToCheck(t, ttCards("苹果", "香蕉"), words)
		ttCheckAll(t, tm, s, 2)
		ttPreFallSubmitLatch(t, tm, exec)

		// Pings hammer the read side while the duplicate submit closes: a
		// ping that wins is answered BEFORE session_ended; one that loses
		// (closed already marked) is refused outright.
		var wg sync.WaitGroup
		ping := mustJSON(t, PingMsg{Type: TypePing, TClientMs: 1})
		for i := 0; i < 16; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				tm.HandleControl(s, ping)
			}()
		}
		tm.HandleControl(s, mustJSON(t, CheckSubmitMsg{Type: TypeCheckSubmit, RequestID: "req-dup"}))
		wg.Wait()

		settled := waitFor(t, 3*time.Second, func() bool {
			return ttClosedNow(tm) && s.countOf(TypeSessionEnded) >= 1 &&
				ttEventCount(t, log, EventSessionEnd) >= 1
		})
		closedNow := ttClosedNow(tm)
		if !settled {
			t.Fatalf("the duplicate submit never settled: closed=%v session_ended=%d end_events=%d",
				closedNow, s.countOf(TypeSessionEnded), ttEventCount(t, log, EventSessionEnd))
		}
		if got := s.countOf(TypeSessionEnded); got != 1 {
			t.Fatalf("session_ended count = %d, want 1", got)
		}
		order := s.typeOrder()
		for i, typ := range order {
			if typ == TypeSessionEnded {
				for _, later := range order[i+1:] {
					if later == TypePong {
						t.Fatalf("pong answered after session_ended (order = %v)", order)
					}
				}
			}
		}
		if got := len(words.submissions()); got != 0 {
			t.Fatalf("review writes from the duplicate = %d, want 0", got)
		}
		if got := ttCompleteCheckCalls(exec); got != 2 {
			t.Fatalf("complete_check calls = %d, want 2 (the foreign latch + this replay)", got)
		}
	})

	t.Run("a refused session_ended still closes and never saves twice", func(t *testing.T) {
		words := newFakeWords(ttCards("苹果", "香蕉"))
		tm, inner, log, exec := ttDriveToCheck(t, ttCards("苹果", "香蕉"), words)
		ttCheckAll(t, tm, inner, 2)
		ttPreFallSubmitLatch(t, tm, exec)

		s := &failingControlSender{captureSender: inner, failType: TypeSessionEnded}
		tm.HandleControl(s, mustJSON(t, CheckSubmitMsg{Type: TypeCheckSubmit, RequestID: "req-dup"}))

		// The refused terminal's completion: the ONE attempt went out AND the
		// branch's EventSessionEnd record landed — attempts==1 alone would be
		// an early read (§1.3).
		settled := waitFor(t, 3*time.Second, func() bool {
			return ttClosedNow(tm) && atomic.LoadInt32(&s.attempts) == 1 &&
				ttEventCount(t, log, EventSessionEnd) >= 1
		})
		if !settled {
			t.Fatalf("the refused terminal never settled: closed=%v attempts=%d end_events=%d",
				ttClosedNow(tm), atomic.LoadInt32(&s.attempts), ttEventCount(t, log, EventSessionEnd))
		}
		if got := atomic.LoadInt32(&s.attempts); got != 1 {
			t.Fatalf("session_ended attempts = %d, want exactly 1", got)
		}
		if got := inner.countOf(TypeSessionEnded); got != 0 {
			t.Fatalf("delivered session_ended = %d, want 0", got)
		}
		if got := ttEventCount(t, log, EventSessionEnd); got != 1 {
			t.Fatalf("session_end events = %d, want 1 (bookkeeping survives the refusal)", got)
		}
		if got := len(words.submissions()); got != 0 {
			t.Fatalf("review writes from the duplicate = %d, want 0", got)
		}
		if got := ttCompleteCheckCalls(exec); got != 2 {
			t.Fatalf("complete_check calls = %d, want 2", got)
		}
	})
}
