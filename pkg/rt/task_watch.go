package rt

import "sync"

// serverTaskHandle identifies ONE whole server task (issue #69 rethink3 S1
// taskDone). Pointer identity is the task id. end() releases it exactly once
// and only at the task's TRUE end — the whole-task wrapper's defer, OUTSIDE
// the task body: after the executor call, the FSRS write, the receipt and
// the event/presentation tails have all run. A task parked BEFORE its
// executor call is as live as one parked in its summary.
type serverTaskHandle struct {
	tracker *serverTaskTracker
}

// serverTaskTracker is the whole-task observation the tests wait on (S1):
// handles are reserved BEFORE the goroutine launches — by the parent call
// (formal submit, the other accepted saves, the init-continuation starters)
// or, for the close fallback, inside the close claim that DECIDES it — so
// the wait set is independent of the fields the tests verify (scope slots,
// checkWrites) and of the executor's own visibility. A replaced classroom's
// task stays registered on the connection until it truly exits; a second
// task a withdrawn dedup might spawn is registered the same way, before it
// first touches the executor. tm.taskTrack is nil in production and every
// method below is a no-op then.
type serverTaskTracker struct {
	mu    sync.Mutex
	live  int
	total uint64
}

func (t *serverTaskTracker) begin() *serverTaskHandle {
	t.mu.Lock()
	t.live++
	t.total++
	t.mu.Unlock()
	return &serverTaskHandle{tracker: t}
}

// end releases the handle exactly once; later calls are no-ops (the wrapper
// defers it, and a reservation that never launched — a claim whose fallback
// goroutine could not start — must not leak either).
func (h *serverTaskHandle) end() {
	if h == nil || h.tracker == nil {
		return
	}
	t := h.tracker
	t.mu.Lock()
	t.live--
	h.tracker = nil
	t.mu.Unlock()
}

// settled reports whether every registered task has fully ended.
func (t *serverTaskTracker) settled() bool {
	if t == nil {
		return true
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.live == 0
}

// beginServerTask reserves a whole-task handle (no-op without the seam).
func (tm *TurnManager) beginServerTask() *serverTaskHandle {
	if tm.taskTrack == nil {
		return nil
	}
	return tm.taskTrack.begin()
}

// goServerTask is the whole-task launcher: reserve the handle, then start
// the goroutine; the handle is released only after the ENTIRE task (defer
// tail included) has returned.
func (tm *TurnManager) goServerTask(task func()) {
	h := tm.beginServerTask()
	go func() {
		defer h.end()
		task()
	}()
}

// goServerTaskReserved launches a task whose handle was reserved earlier —
// the close fallback's shape (S1: 兜底任务随关闭决策预留句柄，锁外启动):
// the claim reserved it in the same critical section that decided it, so no
// observation window exists between the close decision and the task set.
func (tm *TurnManager) goServerTaskReserved(h *serverTaskHandle, task func()) {
	go func() {
		defer h.end()
		task()
	}()
}

// serverTasksSettled reports whether every registered server task has fully
// ended (true when the seam is off — production).
func (tm *TurnManager) serverTasksSettled() bool {
	return tm.taskTrack.settled()
}
