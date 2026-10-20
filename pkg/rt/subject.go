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
	tm.mu.Lock()
	tm.pendingAsk = &m
	tm.state = StateListening
	tm.mu.Unlock()

	_ = s.SendControl(SubjectAskMsg{Type: TypeSubjectAsk, Question: subjectAskText})
	tm.recorder.Emit(EventSubjectAsk)

	ctx, cancel := context.WithCancel(context.Background())
	tm.mu.Lock()
	tm.turnCancel = cancel
	tm.mu.Unlock()
	go tm.runAnnouncement(s, ctx, cancel, subjectAskText, purposeSubjectAsk, nil)
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
// retry wording and the clarify holds.
func (tm *TurnManager) handleSubjectAnswerTurn(s Sender, transcript string, epoch uint64, ctx context.Context, cancel context.CancelFunc) {
	category, ok := subjectFromAnswer(transcript)
	if !ok {
		tm.recorder.Emit(EventSubjectAsk, infra.F("retry", true))
		if !tm.epochCurrent(epoch) {
			return
		}
		tm.runAnnouncement(s, ctx, cancel, subjectAskRetryText, purposeSubjectAsk, nil)
		return
	}
	tm.finishSubjectAsk(s, category, "voice", epoch, true)
}

// finishSubjectAsk binds the child's answer and resumes the parked init —
// exactly once, and (the voice channel) only from a turn that is STILL
// current: the epoch validation and the pendingAsk consumption share one
// lock hold, so a reply recognized before an interrupt can never bind
// after it (a stale turn never starts a classroom — the same contract
// processTurn enforces everywhere else). The button channel passes
// fromTurn=false: a tap is not bound to any turn's epoch.
func (tm *TurnManager) finishSubjectAsk(s Sender, category, via string, epoch uint64, fromTurn bool) {
	tm.mu.Lock()
	if tm.pendingAsk == nil || tm.closed || (fromTurn && tm.epoch != epoch) {
		tm.mu.Unlock()
		return
	}
	m := *tm.pendingAsk
	tm.pendingAsk = nil
	tm.category = category
	tm.mu.Unlock()
	tm.recorder.Emit(EventSubjectPicked, infra.F("category", category), infra.F("via", via))
	tm.continueSessionInit(s, m)
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
