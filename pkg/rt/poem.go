// poem.go — the Issue #12 whole-poem 背默 classroom. The poem is ONE work:
// the tutor announces the task (title/author/line count) and the child
// writes the ENTIRE poem from memory on paper — the teacher NEVER reads the
// lines (明确是背默任务，不能用逐词听写方式把原文整首读给孩子), there is no
// per-line pacing and no per-word timer; pause/resume and 求助 keep working,
// with hints reduced to a line's OPENING characters. 写完了 enters the SAME
// check stage as words (one row per LINE, manual 对/错 gate, photo OCR
// prefills never final), and the submit records the WHOLE-poem scope:
//
//   - the lessons row (r0p- id, poem_id) is the attempt + per-line judgment;
//   - ONE poem_practice_logs row (via PoemPracticeRecorder) is the save —
//     never SubmitReview, never the word-shaped practice_logs (poems are
//     not cards; the FSRS write-separation red line holds);
//   - the two-phase save receipt carries kind "poem" with the TITLE as its
//     single requested entry, so 保存失败 and 判断结果 stay separately
//     expressible (S-05).
package rt

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
	"github.com/xiaotian/home-tutor-agent/pkg/mimo"
	"github.com/xiaotian/home-tutor-agent/pkg/poem"
)

// PoemSource is the whole-poem read seam between the poem classroom and the
// poem store (the WordSource/PracticeSource pattern): the production
// implementation wraps pkg/poem.Store (read-only by construction); tests
// inject fakes. FetchPoem returns the work with its Issue #4 scope verdict —
// the classroom branches on InScope (从符合范围的整首古诗开始练习).
type PoemSource interface {
	// FetchPoem returns the stored poem by id; ErrPoemNotFound is the honest
	// miss for an unknown (or never-imported) id.
	FetchPoem(ctx context.Context, poemID int64) (poem.Poem, error)
}

// PoemStoreSource adapts *poem.Store onto PoemSource (the LessonTools
// pattern: a tiny read-only adapter, no logic of its own).
type PoemStoreSource struct {
	Store *poem.Store
}

// FetchPoem resolves one whole work through the store (nil-guarded — a nil
// store must never be constructed; NewHandler-style fail-fast lives in
// main.go).
func (p *PoemStoreSource) FetchPoem(ctx context.Context, poemID int64) (poem.Poem, error) {
	if p == nil || p.Store == nil {
		return poem.Poem{}, errors.New("rt: poem store source is not wired")
	}
	return p.Store.Get(ctx, poemID)
}

// PoemPracticeRecorder is the whole-poem practice-record write seam: ONE row
// per submit, keyed by the poem. The production implementation is
// *poem.PracticeStore; tests inject fakes.
type PoemPracticeRecorder interface {
	RecordPractice(ctx context.Context, r poem.PracticeRecord) error
}

// Poem timing-event vocabulary (Issue #12; count-class fields only — never
// the poem's text, T-13-06b's boundary).
const (
	// EventPoemStarted: a poem classroom initialized; the line count only.
	EventPoemStarted = "poem_started"
	// EventPoemSubmitted: the poem submit landed its practice record; fields
	// carry the judged line counts. THE audit marker that a poem_practice_logs
	// write happened instead of any FSRS review.
	EventPoemSubmitted = "poem_submitted"
)

// purposePoemGreeting / purposePoemSummary name the poem classroom's
// announcement purposes (the purposeChat/purposeSummary convention).
const (
	purposePoemGreeting = "poem_greeting"
	purposePoemSummary  = "poem_summary"
)

// Fixed poem-classroom wordings (short, child-safe, honest).
const (
	// poemUnavailableNotice: no poem source wired server-side.
	poemUnavailableNotice = "古诗练习暂时用不了，请稍后再试"
	// poemMissingNotice: the requested poem id does not exist.
	poemMissingNotice = "这首古诗找不到，请重新从家长页面打开"
	// poemOutOfScopeNotice: the poem exists but is outside the practice
	// scope (课次尚未学到且未手动加入) — an honest refusal, never a
	// silent lesson swap.
	poemOutOfScopeNotice = "这首古诗还在学习范围外，先学完对应的课再来背默吧"
	// poemNoReadNotice: repeat/slow on a 背默 task — the teacher must not
	// read the original (背默 ≠ 听写).
	poemNoReadNotice = "这是背默练习，老师不能把诗句念给你听哦。先凭记忆写，实在想不起来就喊我，我只提醒你开头的字。"
	// poemHintAskLineNotice: a hint without a named line.
	poemHintAskLineNotice = "你想不起哪一句？告诉我第几句，我只提醒你这一句开头的字。"
	// poemMeaningNotice: meaning/example during the writing stage — no
	// stored interpretation exists, and the task is to finish first.
	poemMeaningNotice = "先把这首诗默写完，我们再一起聊聊它的意思吧。"
)

// resolvePoemLessonID derives the poem-attempt lesson id:
// "r0p-YYYYMMDD-HHMMSS.NNNNNNNNN-<poem>" on the Beijing calendar. The
// nanosecond plus poem id prevents two works opened in the same second from
// sharing an INSERT OR IGNORE row; the r0p- prefix keeps lessonKind's stable
// whole-poem classification.
func resolvePoemLessonID(now time.Time, poemID int64) string {
	cst := time.FixedZone("CST", 8*3600)
	local := now.In(cst)
	return fmt.Sprintf("r0p-%s-%d", local.Format("20060102-150405.000000000"), poemID)
}

// poemGreetingText opens the task: WHAT to write, from memory, how many
// lines — and the help/finish contract. It names title and author only,
// never a line.
func poemGreetingText(title, author string, total int) string {
	by := ""
	if strings.TrimSpace(author) != "" {
		by = "，是" + author + "写的"
	}
	return fmt.Sprintf("今天的任务是把《%s》默写下来%s。这首诗一共有 %d 句，请凭记忆把整首诗写在纸上，一句写一行。想不起来可以喊我提醒，写完了告诉我，我们对答案。", title, by, total)
}

// poemResumeAckText re-opens the task after a pause.
func poemResumeAckText(title string) string {
	return fmt.Sprintf("好，我们继续。接着把《%s》默写完，写好了叫我。", title)
}

// poemHintCueText is ONE line's opening cue: the first min(2, n) characters
// plus the line's length — enough to unblock, never the verse itself.
func poemHintCueText(idxOneBased, total int, line string) string {
	runes := []rune(line)
	head := 2
	if head > len(runes) {
		head = len(runes)
	}
	return fmt.Sprintf("第 %d 句的开头是「%s」，这一句一共 %d 个字。再想想看。", idxOneBased, string(runes[:head]), len(runes))
}

// poemCheckBridgeText bridges 写完了 into the check stage (the panel rows
// are the poem's own ordered lines).
func poemCheckBridgeText(total int) string {
	return fmt.Sprintf("整首诗都写完了，真棒！现在对照原文一句一句检查：一共有 %d 句，每句点「对」还是「错」。", total)
}

// poemSummaryText builds the wrap line: the whole-poem mastery result first
// (整首全对 or M/N 句), then the honest save-failure suffix when the record
// did not land (S-05: never a fabricated success).
func poemSummaryText(title string, total, correct int, saveFailed bool) string {
	var b strings.Builder
	if total > 0 && correct == total {
		fmt.Fprintf(&b, "《%s》整首 %d 句全对，背得真棒！", title, total)
	} else {
		fmt.Fprintf(&b, "《%s》一共 %d 句，对了 %d 句。错的句子我们下次再背一背。", title, total, correct)
	}
	if saveFailed {
		b.WriteString(reviewFailedSuffix())
	}
	return b.String()
}

// poemTaskView projects the work identity for session_ready — never the
// text itself.
func poemTaskView(p poem.Poem) *PoemTaskView {
	return &PoemTaskView{
		ID:        p.ID,
		Title:     p.Title,
		Author:    p.Author,
		LineCount: len(p.Lines),
		Method:    "背默",
	}
}

// poemViewForRow projects a lesson ROW's poem identity (nil for non-poem
// rows). The title/author come from the poem store when it can serve them;
// a fetch failure degrades to the id/line-count view — the row's check
// panel still opens, only the banner shows less.
func (tm *TurnManager) poemViewForRow(l lesson.Lesson) *PoemTaskView {
	if l.PoemID <= 0 {
		return nil
	}
	if tm.cfg.Poems != nil {
		if p, err := tm.cfg.Poems.FetchPoem(context.Background(), l.PoemID); err == nil {
			return poemTaskView(p)
		}
	}
	lines := len(l.Cards)
	return &PoemTaskView{ID: l.PoemID, LineCount: lines, Method: "背默"}
}

// handlePoemInit is the session_init{poem} branch (Issue #12): resolve the
// work, enforce the scope gate, then EITHER resume this poem's in-flight
// attempt under its own id (a mid-writing reconnect or an unfinished check)
// or open a FRESH attempt row. A submitted attempt never reopens here — its
// judgment is settled history; re-practicing the poem is a new row.
func (tm *TurnManager) handlePoemInit(s Sender, poemID int64, installGen uint64) {
	if tm.cfg.Poems == nil {
		tm.recorder.Emit(EventError,
			infra.F("code", ErrCodeLessonUnavailable),
			infra.F("poem", "no_source"),
		)
		tm.sendError(s, ErrCodeLessonUnavailable, poemUnavailableNotice, false)
		return
	}
	// The poem classroom is 语文 by construction (poems are a Chinese
	// content type) — whatever the page's category parameter said.
	tm.mu.Lock()
	tm.category = lesson.CategoryChinese
	tm.mu.Unlock()

	ctx := context.Background()
	p, err := tm.cfg.Poems.FetchPoem(ctx, poemID)
	switch {
	case errors.Is(err, poem.ErrPoemNotFound):
		tm.recorder.Emit(EventError,
			infra.F("code", ErrCodeLessonUnavailable),
			infra.F("poem", "not_found"),
		)
		tm.sendError(s, ErrCodeLessonUnavailable, poemMissingNotice, false)
		return
	case err != nil:
		tm.failLessonInit(s, installGen)
		return
	case !p.InScope:
		// 进入范围才有资格被安排 (Issue #4): the honest coded refusal —
		// never a silent fallback into a due-word lesson.
		tm.recorder.Emit(EventError,
			infra.F("code", ErrCodeLessonUnavailable),
			infra.F("poem", "out_of_scope"),
		)
		tm.sendError(s, ErrCodeLessonUnavailable, poemOutOfScopeNotice, false)
		return
	}

	// Resume probe: this poem's latest attempt row.
	latest, err := tm.exec.LatestPoemLessonID(ctx, poemID)
	if err != nil {
		tm.failLessonInit(s, installGen)
		return
	}
	if latest != "" {
		if l, gerr := tm.exec.GetLessonState(ctx, latest); gerr == nil &&
			l.PoemID == poemID && len(l.Cards) > 0 && l.Stage != lesson.StageSubmitted {
			// In-flight: an unfinished check reopens as the check panel; a
			// mid-writing attempt re-announces the task (the poem's own
			// first-content-wins row continues).
			if l.Stage == lesson.StageChecking {
				tm.startCheckResume(s, l, installGen, lessonRowSaveGuard{})
				return
			}
			tm.startPoemSession(s, l, p, installGen)
			return
		}
	}

	lessonID := resolvePoemLessonID(time.Now(), poemID)
	// The per-lesson init lock discipline (two simultaneous poem connections
	// for this exact attempt id must not both create its row).
	unlock := lockLessonInit(lessonID)
	defer unlock()

	l, err := tm.exec.EnsurePoemLesson(ctx, lessonID, poemID, p.Lines)
	if err != nil {
		tm.failLessonInit(s, installGen)
		return
	}
	if l.PoemID != poemID {
		// Defensive identity check: a lesson row must never announce one work
		// while checking another work's lines.
		tm.recorder.Emit(EventError, infra.F("code", ErrCodeProviderFailed), infra.F("at", "poem_identity"))
		tm.failLessonInit(s, installGen)
		return
	}
	tm.recorder.Emit(EventPoemStarted, infra.F("lines", len(p.Lines)))
	tm.startPoemSession(s, l, p, installGen)
}

// startPoemSession binds the FSM to the poem row and announces the task.
// The greeting carries NO line text, arms no timer, and its display equals
// its spoken text (title/author are public identity, not the answer).
func (tm *TurnManager) startPoemSession(s Sender, l lesson.Lesson, p poem.Poem, installGen uint64) {
	tutor := newDictationTutor(l)
	tutor.poem = &poemRef{ID: l.PoemID, Title: p.Title, Author: p.Author}
	total, category := len(tutor.words), tutor.category
	ctx, cancel := context.WithCancel(context.Background())
	ready := SessionReadyMsg{
		Type:      TypeSessionReady,
		SessionID: tm.session,
		Audio:     AudioSpec{SampleRate: requiredSampleRate, Format: requiredFormat, Channels: requiredChannels},
		TTS:       TTSSpec{SampleRate: TTSOutputSampleRate},
		Lesson:    tm.lessonViewFor(l),
		Category:  category,
		Poem:      poemTaskView(p),
	}
	var epoch uint64
	if !tm.applyClassroomInstallEffect(installGen, func() error {
		if err := tm.replaceActiveClassroomLocked(installGen, l.LessonID, tutor, s); err != nil {
			return err
		}
		if err := s.SendControl(ready); err != nil {
			return err
		}
		tm.state = StateListening
		tm.sessionReadyAt = time.Now()
		tm.turnCancel = cancel
		epoch = tm.epoch
		return nil
	}) {
		cancel()
		return
	}
	if tm.activeClassroomGen() != installGen {
		cancel()
		return
	}
	tm.recorder.Emit(EventConnected, infra.F("session_id", tm.session))
	text := poemGreetingText(p.Title, p.Author, total)
	// onAck is deliberately nil: the poem classroom arms NO per-word timer
	// (不用单词短计时器把整首任务机械推进) — nothing counts down while the
	// child writes.
	tm.goServerTask(func() {
		tm.runAnnouncementDisplayAt(s, epoch, ctx, cancel, text, text, purposePoemGreeting, nil)
	})
}

// buildPoemClassifyMessages assembles the poem writing-stage classification
// request: the intent set shrinks to confirm/pause/resume/hint/chat (repeat
// and slow exist only to be refused honestly; meaning/example likewise) and
// the ground truth names title/line count — never a line.
func (tm *TurnManager) buildPoemClassifyMessages(transcript string) []mimo.Message {
	var user strings.Builder
	user.WriteString("学生说：")
	user.WriteString(transcript)
	tm.mu.Lock()
	total, title := 0, ""
	if tm.tutor != nil {
		total = len(tm.tutor.words)
		if tm.tutor.poem != nil {
			title = tm.tutor.poem.Title
		}
		if tm.tutor.paused {
			user.WriteString("\n（课堂正处于暂停状态，学生可能想继续）")
		}
		tm.tutor.appendShortHistoryLocked(&user)
	}
	tm.mu.Unlock()

	var sys strings.Builder
	sys.WriteString("你是一位耐心的小学语文老师，正在陪孩子完成古诗背默练习。当前任务是《" + title + "》，共 " + fmt.Sprint(total) + " 句。")
	sys.WriteString("学生正在凭记忆把整首诗默写在纸上，你绝对不能念出任何一句诗句。请把学生的话分类为一个 JSON 对象：{\"intent\":\"...\",\"reply\":\"...\",\"index\":N}。")
	sys.WriteString("intent 只能取以下值：")
	sys.WriteString("confirm（写完了/默好了/可以检查了）；")
	sys.WriteString("hint（想不起来某一句/提醒我一下，如果学生说了第几句，index 填 1 起始的句号）；")
	sys.WriteString("pause（要休息/暂停）；resume（继续/接着写）；")
	sys.WriteString("repeat 或 slow（想再听一遍或慢一点读——学生可能把它当听写了）；")
	sys.WriteString("meaning 或 example（问诗的意思或用法）；chat（其它对话）。")
	sys.WriteString("reply 用一句简短、亲切的中文回应（不要在 reply 里出现任何完整诗句）。index 只有 hint 需要填，其它 intent 填 0。只输出这个 JSON 对象。")

	return []mimo.Message{
		{Role: "system", Content: sys.String()},
		{Role: "user", Content: user.String()},
	}
}

// classifyPoemTurn is the poem writing stage's classifier: ONE Chat hop and
// exactly ONE correction retry (classifyTurn's discipline), then the
// deterministic fallback — a poem session never treats a failed
// classification as confirm (the check stage must never open by accident).
func (tm *TurnManager) classifyPoemTurn(ctx context.Context, transcript string) intentPayload {
	msgs := tm.buildPoemClassifyMessages(transcript)
	p, ok := tm.classifyOncePayload(ctx, msgs)
	if !ok {
		msgs = append(msgs, mimo.Message{
			Role:    "user",
			Content: "你刚才的输出不符合要求。请只输出一个 JSON 对象，键为 intent、reply 与 index，不要输出任何其它内容。",
		})
		p, ok = tm.classifyOncePayload(ctx, msgs)
	}
	if !ok {
		p = intentPayload{Intent: intentChat, Reply: classifyFallbackReply}
		tm.recorder.Emit(EventError, infra.F("code", ErrCodeProviderFailed), infra.F("attempts", 2))
	}
	tm.recorder.Emit(EventIntentClassified, infra.F("intent", p.Intent))
	return p
}

// dispatchPoemTurn is the poem writing stage's branch table: confirm enters
// the check stage (the WHOLE task is one confirm — no per-line advance),
// hint gives a line's opening cue only, repeat/slow/meaning/example answer
// honest fixed refusals, pause/resume keep the classroom pause bit, chat
// speaks the reply. No branch arms the word timer.
func (tm *TurnManager) dispatchPoemTurn(s Sender, ctx context.Context, cancel context.CancelFunc, turnID, epoch uint64, p intentPayload) {
	switch p.Intent {
	case intentConfirm:
		paused, checking := false, false
		if !tm.applyTurnAt(epoch, func() error {
			paused = tm.tutor != nil && tm.tutor.paused
			checking = tm.tutor != nil && tm.tutor.checking
			return nil
		}) {
			return
		}
		if paused {
			tm.runAnnouncementAt(s, epoch, ctx, cancel, confirmPausedRejectText(), purposeChat, nil)
			return
		}
		if checking {
			// Unreachable through processTurn's gate; kept defensive.
			return
		}
		tm.enterCheck(s, ctx, cancel, epoch)
	case intentRepeat, intentSlow:
		tm.runAnnouncementAt(s, epoch, ctx, cancel, poemNoReadNotice, purposeChat, nil)
	case intentHint:
		tm.poemHint(s, ctx, cancel, epoch, p)
	case intentMeaning, intentExample:
		tm.runAnnouncementAt(s, epoch, ctx, cancel, poemMeaningNotice, purposeChat, nil)
	case intentPause:
		if !tm.applyTurnAt(epoch, func() error {
			tm.tutor.paused = true
			tm.stopWordTimerLocked()
			return nil
		}) {
			return
		}
		tm.runAnnouncementAt(s, epoch, ctx, cancel, pauseAckText(), purposeChat, nil)
	case intentResume:
		title := ""
		if !tm.applyTurnAt(epoch, func() error {
			tm.tutor.paused = false
			if tm.tutor.poem != nil {
				title = tm.tutor.poem.Title
			}
			return nil
		}) {
			return
		}
		tm.runAnnouncementAt(s, epoch, ctx, cancel, poemResumeAckText(title), purposeChat, nil)
	case intentChat:
		tm.runAnnouncementDisplayAt(s, epoch, ctx, cancel, p.Reply, tm.replyDisplay(p.Reply), purposeChat, nil)
	}
	_ = turnID
}

// poemHint handles 求助: a named line (1-based index from the classifier)
// gets its OPENING characters and length — never the verse; an unnamed or
// out-of-range ask gets the which-line question. The hint count lands on
// that line's outcome (the same U-9 accounting the word classroom keeps).
func (tm *TurnManager) poemHint(s Sender, ctx context.Context, cancel context.CancelFunc, epoch uint64, p intentPayload) {
	total := 0
	var cue string
	hintCount := 0
	if !tm.applyTurnAt(epoch, func() error {
		total = len(tm.tutor.words)
		if p.Index >= 1 && p.Index <= total {
			line := tm.tutor.words[p.Index-1].Word
			cue = poemHintCueText(p.Index, total, line)
			hintCount = tm.tutor.recordHint(p.Index - 1)
		}
		return nil
	}) {
		return
	}
	if cue == "" {
		cue = poemHintAskLineNotice
	} else {
		tm.recorder.Emit(EventHintGiven,
			infra.F("word_index", p.Index-1), infra.F("hint_count", hintCount))
	}
	tm.runAnnouncementAt(s, epoch, ctx, cancel, cue, purposeChat, nil)
}

// submitPoem is the poem check_submit: the SAME per-line manual gate (D-05)
// and the SAME CompleteCheck latch as every other submit, then the
// whole-poem save instead of any word write.
func (tm *TurnManager) submitPoem(s Sender, m CheckSubmitMsg) {
	if m.RequestID == "" {
		tm.sendError(s, ErrCodeInvalidMessage, checkVerdictInvalidNotice, true)
		return
	}

	tm.mu.Lock()
	lessonID := tm.tutor.lessonID
	expected := tm.tutor.version
	total := len(tm.tutor.words)
	ref := *tm.tutor.poem
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

	ctx, cancel := context.WithCancel(context.Background())
	tm.mu.Lock()
	write, writeAdmitted := tm.admitCheckWriteLocked(lessonID)
	epoch := tm.epoch
	tm.turnCancel = cancel
	tm.mu.Unlock()
	if !writeAdmitted {
		cancel()
		return
	}
	requestID := m.RequestID
	tm.goServerTask(func() {
		tm.runAcceptedPoemSubmit(s, ctx, cancel, epoch, write, ref, lessonID, expected, requestID, total)
	})
}

func (tm *TurnManager) runAcceptedPoemSubmit(s Sender, ctx context.Context, cancel context.CancelFunc, epoch uint64, write *checkWriteAdmission, ref poemRef, lessonID string, expected int, requestID string, total int) {
	persistCtx, persistDone := checkPersistContext(ctx)
	defer persistDone()

	res, err := tm.exec.CompleteCheck(persistCtx, lessonID, expected, requestID, false)
	if err != nil {
		tm.completeCheckWrite(write)
		cancel()
		var conflict *lesson.ConflictError
		if errors.As(err, &conflict) {
			tm.mu.Lock()
			tm.tutor.checking = false
			tm.mu.Unlock()
			tm.recorder.Emit(EventError,
				infra.F("code", ErrCodeDuplicateSubmit),
				infra.F("duplicate", "poem_submit_conflict"),
			)
			return
		}
		tm.sendError(s, ErrCodeProviderFailed, checkStoreFailedNotice, true)
		return
	}
	if res.Idempotent {
		tm.mu.Lock()
		tm.tutor.checking = false
		tm.mu.Unlock()
		tm.recorder.Emit(EventError,
			infra.F("code", ErrCodeDuplicateSubmit),
			infra.F("duplicate", "poem_submit_latch"),
		)
		tm.completeCheckWrite(write)
		cancel()
		return
	}

	tm.applyTurnAt(epoch, func() error {
		tm.tutor.version = res.NewVersion
		tm.tutor.checking = false
		return nil
	})

	tm.finishPoemSubmit(s, persistCtx, ctx, cancel, epoch, ref, lessonID, total, res, requestID, write)
}

// finishPoemSubmit runs on its own goroutine (the ACK-gated ending blocks;
// the read loop must keep serving acks): compute the judged line counts
// from the STORED verdict readback (the row is the authority), write ONE
// poem_practice_logs row bracketed by the two-phase kind=poem receipt, ship
// the honest review_result, then speak the wrap line through the ACK gate
// and end with session_ended(lesson_complete).
func (tm *TurnManager) finishPoemSubmit(s Sender, persistCtx context.Context, ctx context.Context, cancel context.CancelFunc, epoch uint64, ref poemRef, lessonID string, total int, res lesson.CompleteCheckResult, requestID string, write *checkWriteAdmission) {
	defer cancel()

	correct := 0
	for _, v := range res.Verdicts {
		if v.Correct && v.Manual {
			correct++
		}
	}
	hints := 0
	for _, o := range res.Outcomes {
		hints += o.Hints
	}
	mastered := total > 0 && correct == total

	title := ref.Title
	if strings.TrimSpace(title) == "" {
		// A resumed session whose poem fetch degraded: the receipt still
		// names ONE whole-poem entry, never per-line entries.
		title = fmt.Sprintf("古诗#%d", ref.ID)
	}
	requestedSet := []string{title}

	recorded := false
	saveFailed := true
	if tm.cfg.PoemPractice != nil {
		// Receipt phase one: pending BEFORE the write (已提交 ≠ 已保存).
		receiptID := int64(0)
		if tm.cfg.Receipts != nil {
			rec, rerr := tm.cfg.Receipts.Begin(persistCtx, lesson.ReceiptInput{
				LessonID:  lessonID,
				RequestID: requestID,
				Kind:      lesson.ReceiptKindPoem,
				Requested: requestedSet,
			})
			if rerr != nil || rec.ID <= 0 {
				tm.recorder.Emit(EventError, infra.F("code", ErrCodeProviderFailed), infra.F("at", "save_receipt"))
			} else {
				receiptID = rec.ID
			}
		}

		rec := poem.PracticeRecord{
			PoemID:       ref.ID,
			LessonID:     lessonID,
			TotalLines:   total,
			CorrectLines: correct,
			Hints:        hints,
			Mastered:     mastered,
			ReviewedDay:  beijingDay(time.Now()),
		}
		if err := tm.cfg.PoemPractice.RecordPractice(persistCtx, rec); err != nil {
			// Honest failure: the record was NOT saved — never a narrated
			// success (S-05). The judgment (verdicts, stage=submitted) is
			// already persisted on the lesson row and stays visible.
			tm.recorder.Emit(EventError, infra.F("code", ErrCodeProviderFailed))
			if tm.cfg.Receipts != nil && receiptID > 0 {
				if cerr := tm.cfg.Receipts.Complete(persistCtx, receiptID, nil, requestedSet, false); cerr != nil {
					tm.recorder.Emit(EventError, infra.F("code", ErrCodeProviderFailed), infra.F("at", "save_receipt"))
				}
			}
		} else {
			recorded = true
			saveFailed = false
			tm.recorder.Emit(EventPoemSubmitted,
				infra.F("total", total),
				infra.F("correct", correct),
				infra.F("mastered", mastered),
			)
			if tm.cfg.Receipts != nil && receiptID > 0 {
				if cerr := tm.cfg.Receipts.Complete(persistCtx, receiptID, requestedSet, nil, true); cerr != nil {
					tm.recorder.Emit(EventError, infra.F("code", ErrCodeProviderFailed), infra.F("at", "save_receipt"))
				}
			}
		}
	} else {
		// Degraded wiring: no poem practice store — the submit reports the
		// honest zero-saved result (never a fabricated record).
		tm.recorder.Emit(EventError,
			infra.F("code", ErrCodeProviderFailed),
			infra.F("at", "poem_practice"),
		)
	}
	// The accepted poem save ends with its practice record and receipt. The
	// review result and summary remain presentation work and are not part of
	// close-time persistence coordination.
	tm.completeCheckWrite(write)

	updated := 0
	missing := 1
	if recorded {
		updated, missing = 1, 0
	}
	result := ReviewResultMsg{
		Type:      TypeReviewResult,
		Requested: 1,
		Updated:   updated,
		Missing:   missing,
		Unchecked: false,
	}

	summary := poemSummaryText(title, total, correct, saveFailed)
	if tm.checkSubmitPresentationInterleave != nil {
		tm.checkSubmitPresentationInterleave()
	}
	if !tm.applyTurnAt(epoch, func() error {
		if err := s.SendControl(result); err != nil {
			return err
		}
		tm.tutor.reviewSeen = true
		tm.tutor.reviewResult = result
		return nil
	}) {
		return
	}
	if tm.checkSubmitSummaryInterleave != nil {
		tm.checkSubmitSummaryInterleave()
	}
	tm.runNaturalEndingAt(s, epoch, ctx, cancel, summary, purposePoemSummary, ReasonLessonComplete)
}
