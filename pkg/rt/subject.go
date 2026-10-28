// subject.go — the unspecified-subject flow (Issue #55, reworked): when a
// word classroom (dictation or practice) is opened with category "auto" —
// the page's generic entry, the student never having picked a subject —
// the CHILD decides, never a model:
//
//   - only ONE subject holds due words → the classroom opens under that
//     subject directly (nothing to ask);
//   - BOTH subjects hold due words → the server asks the child which one
//     to dictate (the subject_ask question, spoken and shown with two pick
//     buttons), and the spoken or tapped answer binds the round;
//   - NEITHER subject holds due words → the english classroom opens and
//     ends through the ordinary no-due path.
//
// The bound subject then scopes the round through every #10 seam exactly
// like an explicit declaration (scoped fetch, per-subject row, scoped
// writes, session_ready echo). A declared english/chinese never reaches
// this file: the user's specification outranks any ask — zero peeks, zero
// asks, zero model calls (the confirmed AC2 rule).
package rt

import (
	"context"
	"strings"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
)

// sessionCategoryFromWire normalizes the page-supplied subject (Issue #55
// widens Issue #10's pair): "chinese" opens the 语文 classroom, "auto"
// enters the child-asks flow above, and anything else — including an
// absent field — is the English classroom, the pre-#10 deny-by-default
// landing untouched.
func sessionCategoryFromWire(category string) string {
	switch category {
	case lesson.CategoryChinese:
		return lesson.CategoryChinese
	case CategoryAuto:
		return CategoryAuto
	default:
		return lesson.CategoryEnglish
	}
}

// subjectAskText is the clarify question the teacher speaks AND the page
// renders above the two pick buttons — one wording for both channels, so
// the child can answer by voice or by tap interchangeably.
const subjectAskText = "语文和英语今天都有要默写的词语。这次想先默写哪一科？对我说语文，或者英语，也可以点屏幕上的按钮。"

// subjectAskRetryText re-asks after an unusable spoken answer (neither
// subject named, or both): the clarify holds and the buttons stay up.
const subjectAskRetryText = "老师没听清是哪一科。请说语文，或者英语。"

// resolveAutoSubject maps the two subjects' due snapshots onto the auto
// entry's outcome: the one subject holding due words, or ask=true when
// both do. Neither subject due resolves to english — the ordinary no-due
// english ending then explains honestly, exactly like an english bookmark
// over an empty vocabulary. The peeks stay lock-free (a resolution read
// must never lock cards away from a live classroom) and a peek error
// degrades that subject to "no due content" — the input degrades, never
// the classroom behind it.
func (tm *TurnManager) resolveAutoSubject() (category string, ask bool) {
	en := tm.subjectHasDue(lesson.CategoryEnglish)
	zh := tm.subjectHasDue(lesson.CategoryChinese)
	switch {
	case en && zh:
		return "", true
	case zh:
		return lesson.CategoryChinese, false
	default:
		return lesson.CategoryEnglish, false
	}
}

// subjectHasDue reports whether the subject holds at least one due word —
// a one-card peek, because presence is all the resolution needs. A nil
// Words (degraded constructions) or a peek error reads as "no due
// content".
func (tm *TurnManager) subjectHasDue(category string) bool {
	if tm.cfg.Words == nil {
		return false
	}
	words, err := tm.cfg.Words.PeekDueWords(context.Background(), 1, category)
	return err == nil && len(words) > 0
}

// startSubjectAsk parks the auto classroom on the clarify question: the
// subject_ask control rides out (the page's two pick buttons), the teacher
// speaks the same question, and the ORIGINAL session_init message is held
// until the child answers (subject_pick or a spoken 语文/英语). No lesson
// row exists yet — the row's subject is whatever the child picks, and the
// answered round then runs under that subject only.
func (tm *TurnManager) startSubjectAsk(s Sender, m SessionInitMsg) {
	ctx, cancel := context.WithCancel(context.Background())
	var askEpoch uint64
	if !tm.applySessionEffect(func() error {
		if err := s.SendControl(SubjectAskMsg{Type: TypeSubjectAsk, Question: subjectAskText}); err != nil {
			return err
		}
		tm.pendingAsk = &m
		tm.state = StateListening
		askEpoch = tm.epoch // fixed NOW: a pick processed before the goroutine
		// first runs bumps the epoch, and the ask's entry check must refuse it.
		tm.turnCancel = cancel
		return nil
	}) {
		cancel()
		return
	}
	tm.recorder.Emit(EventSubjectAsk)
	tm.goServerTask(func() {
		tm.runAnnouncementDisplayAt(s, askEpoch, ctx, cancel, subjectAskText, subjectAskText, purposeSubjectAsk, nil)
	})
}

// handleSubjectPick consumes the page's button answer to the clarify. A
// pick with no clarify parked is a no-op — the classroom is already under
// a subject, and a stray pick can never re-open one.
func (tm *TurnManager) handleSubjectPick(s Sender, m SubjectPickMsg) {
	tm.finishSubjectAsk(s, m.Category, "button", 0, false)
}

// handleSubjectAnswerTurn digests the child's SPOKEN answer to the clarify
// — keyword matching only, never an LLM call (the subject decision belongs
// to the child). A clean 语文/英语 binds and resumes the parked init; an
// unusable answer (neither subject named, or both) re-asks with the short
// retry wording and the clarify holds. The retry rides the ANSWER TURN's
// captured epoch (runAnnouncementDisplayAt's entry check is the gate): a
// pick that lands between the check and the announcement must find the
// retry dead too — it must never re-read the bumped epoch and speak over
// the classroom the pick just opened.
func (tm *TurnManager) handleSubjectAnswerTurn(s Sender, transcript string, epoch uint64, ctx context.Context, cancel context.CancelFunc) {
	category, ok := subjectFromAnswer(transcript)
	if !ok {
		tm.recorder.Emit(EventSubjectAsk, infra.F("retry", true))
		tm.runAnnouncementDisplayAt(s, epoch, ctx, cancel, subjectAskRetryText, subjectAskRetryText, purposeSubjectAsk, nil)
		return
	}
	tm.finishSubjectAsk(s, category, "voice", epoch, true)
}

// finishSubjectAsk binds the child's answer and resumes the parked init —
// exactly once, and (the voice channel) only from a turn that is STILL
// current. The binding's locked half (takeSubjectAsk) validates the epoch,
// consumes the pendingAsk, binds the category AND terminates the ask's
// playback in ONE lock hold: until the epoch moves inside that same hold, a
// timed-out ask's orderlyCloseAt still sees its epoch current and could
// close the session mid-binding — leaving a closed classroom to receive
// session_ready and a greeting it can never ack. The button channel passes
// fromTurn=false: a tap is not bound to any turn's epoch.
func (tm *TurnManager) finishSubjectAsk(s Sender, category, via string, epoch uint64, fromTurn bool) {
	m, oldEpoch, ok := tm.takeSubjectAsk(category, epoch, fromTurn)
	if !ok {
		return
	}
	tm.finalizeSubjectAsk(s, m, oldEpoch, category, via)
}

// takeSubjectAsk is the binding's locked half, atomic under one tm.mu hold:
// epoch validation (voice channel), pendingAsk consumption, category
// binding, and the ask's full playback termination (stopAskPlaybackLocked).
// Split out from finishSubjectAsk so the deterministic race tests can drive
// the locked half and interleave a timeout against it, exactly as the
// scheduler could. Returns the parked init message and the PRE-bump epoch;
// ok=false when there is nothing to bind.
func (tm *TurnManager) takeSubjectAsk(category string, epoch uint64, fromTurn bool) (SessionInitMsg, uint64, bool) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	if tm.pendingAsk == nil || tm.closed || (fromTurn && tm.epoch != epoch) {
		return SessionInitMsg{}, 0, false
	}
	m := *tm.pendingAsk
	tm.pendingAsk = nil
	tm.category = category
	if tm.subjectTakeInterleave != nil {
		// Still INSIDE the hold (PR #60 review): the seam fires between the
		// consumption and the bump precisely so a test can probe — via
		// TryLock — that no unlock ever separates them.
		tm.subjectTakeInterleave()
	}
	old := tm.stopAskPlaybackLocked()
	return m, old, true
}

// finalizeSubjectAsk is the binding's unlocked half: the client-facing
// aftermath of the termination (VAD reset + the interrupt_ack epoch notice)
// and the handoff to the picked classroom. An end_session that closed the
// session between the halves must not receive session_ready or a greeting.
// The stillOpen check here is only the fast path (it also spares the picked
// event); the REAL gates live at the classroom init's outward points — the
// starters (session_ready, tutor bind), the row creation and the
// announcement entry checks — because this half runs on the ANSWER TURN's
// goroutine and an end_session can close the session under any of that IO
// (PR #60 review: an entry-only check passes and still ships a classroom
// that outlives its own session_ended).
func (tm *TurnManager) finalizeSubjectAsk(s Sender, m SessionInitMsg, oldEpoch uint64, category, via string) {
	tm.vad.Reset()
	_ = s.SendControl(InterruptAckMsg{
		Type:     TypeInterruptAck,
		OldEpoch: oldEpoch,
		NewEpoch: oldEpoch + 1,
	})
	if !tm.stillOpen() {
		return
	}
	tm.recorder.Emit(EventSubjectPicked, infra.F("category", category), infra.F("via", via))
	tm.mu.Lock()
	installGen := tm.pendingAskInstallGen
	tm.pendingAskInstallGen = 0
	tm.mu.Unlock()
	tm.continueSessionInit(s, m, installGen)
}

// stillOpen reports whether the session is still live.
func (tm *TurnManager) stillOpen() bool {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	return !tm.closed
}

// stopAskPlayback kills the clarify question's announcement the same way an
// interrupt kills a turn, and TELLS the client: bump the epoch (the ask
// goroutine's in-flight chunks, playback waits and post-ack settle all turn
// stale), cancel its provider stream, drop its playback waits, then ship an
// interrupt_ack carrying the bump. The ack matters twice over on the wire:
// the client's playback stops its already-scheduled ask audio (its own
// epoch gate only DROPS future old-epoch chunks — it never stops what is
// queued), and it adopts the new epoch, without which the picked classroom's
// greeting — which, like every announcement, carries no turn_start — would
// be rejected as a stale epoch and the classroom would die on its
// playback-ack timeout. The production binding path runs the locked half
// INSIDE takeSubjectAsk's hold; this standalone form serves the
// deterministic race tests (no tutor history exists yet, so no trim
// ceremony).
func (tm *TurnManager) stopAskPlayback(s Sender) {
	tm.mu.Lock()
	old := tm.stopAskPlaybackLocked()
	tm.mu.Unlock()
	tm.vad.Reset()
	_ = s.SendControl(InterruptAckMsg{
		Type:     TypeInterruptAck,
		OldEpoch: old,
		NewEpoch: old + 1,
	})
}

// stopAskPlaybackLocked is the termination's locked half (caller holds
// tm.mu): epoch bump, provider cancel, playback-wait invalidation. Also
// called INSIDE the binding's own hold so the consumption and the bump can
// never be observed apart. Returns the pre-bump epoch.
func (tm *TurnManager) stopAskPlaybackLocked() uint64 {
	old := tm.epoch
	tm.epoch++
	if tm.turnCancel != nil {
		tm.turnCancel()
	}
	tm.turnSentences = nil
	tm.acked = map[string]bool{}
	for _, p := range tm.pendingPlayback {
		p.cancelWait()
	}
	tm.pendingPlayback = map[string]*pendingPlayback{}
	tm.playbackPending = false
	return old
}

// subjectFromAnswer maps one spoken answer onto a subject by keyword —
// the child's own words, matched mechanically (an LLM never judges the
// subject). Both subjects named, or neither, is NOT an answer: the
// clarify re-asks.
func subjectFromAnswer(text string) (string, bool) {
	zh := strings.Contains(text, "语文") || strings.Contains(text, "中文") || strings.Contains(text, "汉字")
	en := strings.Contains(text, "英语") || strings.Contains(text, "英文")
	switch {
	case zh && !en:
		return lesson.CategoryChinese, true
	case en && !zh:
		return lesson.CategoryEnglish, true
	default:
		return "", false
	}
}
