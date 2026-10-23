// passage.go — the Issue #14 passage 背默 classroom: the structured-recitation
// twin of the poem classroom (#12). The task is ONE passage RANGE — 整篇 or an
// explicit paragraph set the parent pre-selected (#13): the tutor announces
// the work and WHICH range this session covers (开始时明确本次整篇或指定段
// 落), and the child writes those paragraphs from memory on paper — the
// teacher NEVER reads the original (背默 ≠ 听写), there is no per-paragraph
// pacing and no per-word timer; pause/resume and 求助 keep working, with
// hints reduced to a paragraph's OPENING characters. 写完了 enters the SAME
// check stage as words (one row per PARAGRAPH, in the selected range's own
// 原文顺序, manual 对/错 gate, photo OCR prefills never final), and the
// submit records exactly the practiced content + range identity:
//
//   - the lessons row (r0w- id, passage_id + the range columns) is the
//     attempt + per-paragraph judgment;
//   - ONE passage_practice_logs row (via PassagePracticeRecorder) is the
//     save — keyed by content + range, never SubmitReview, never the
//     word-shaped practice_logs, never the FSRS schedule (passages are not
//     cards; the word and whole-poem scheduling semantics stay untouched);
//   - the two-phase save receipt carries kind "passage" with ONE entry —
//     the work plus the practiced range — so 保存失败 and 判断结果 stay
//     separately expressible (S-05).
//
// Range discipline (the issue's core acceptance): the check panel and every
// identity reference use ONLY the selected range's own paragraphs — an
// unselected paragraph never rides the task (不混入未选择段落), and the
// scope gate refuses an out-of-scope passage outright (不混入未学教材内
// 容), never a silent swap into a word lesson.
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
	"github.com/xiaotian/home-tutor-agent/pkg/passage"
)

// PassageSource is the passage read seam between the passage classroom and
// the passage store (the PoemSource pattern): the production implementation
// wraps pkg/passage.Store (read-only by construction); tests inject fakes.
// FetchPassage returns the work with its Issue #4 scope verdict and its
// stored practice ranges — the classroom branches on InScope and resolves
// the requested range row by id.
type PassageSource interface {
	// FetchPassage returns the stored passage by id; ErrPassageNotFound is
	// the honest miss for an unknown (or never-imported) id.
	FetchPassage(ctx context.Context, passageID int64) (passage.Passage, error)
}

// PassageStoreSource adapts *passage.Store onto PassageSource (the
// PoemStoreSource pattern: a tiny read-only adapter, no logic of its own).
type PassageStoreSource struct {
	Store *passage.Store
}

// FetchPassage resolves one stored work through the store (nil-guarded — a
// nil store must never be constructed; NewHandler-style fail-fast lives in
// main.go).
func (p *PassageStoreSource) FetchPassage(ctx context.Context, passageID int64) (passage.Passage, error) {
	if p == nil || p.Store == nil {
		return passage.Passage{}, errors.New("rt: passage store source is not wired")
	}
	return p.Store.Get(ctx, passageID)
}

// PassagePracticeRecorder is the passage practice-record write seam: ONE row
// per submit, keyed by content + range. The production implementation is
// *passage.PracticeStore; tests inject fakes.
type PassagePracticeRecorder interface {
	RecordPractice(ctx context.Context, r passage.PracticeRecord) error
}

// Passage timing-event vocabulary (Issue #14; count-class fields only —
// never the passage's text, T-13-06b's boundary).
const (
	// EventPassageStarted: a passage classroom initialized; the range shape
	// only (paragraph count + whole flag).
	EventPassageStarted = "passage_started"
	// EventPassageSubmitted: the passage submit landed its practice record;
	// fields carry the judged paragraph counts. THE audit marker that a
	// passage_practice_logs write happened instead of any FSRS review.
	EventPassageSubmitted = "passage_submitted"
)

// purposePassageGreeting / purposePassageSummary name the passage classroom's
// announcement purposes (the purposePoem* convention).
const (
	purposePassageGreeting = "passage_greeting"
	purposePassageSummary  = "passage_summary"
)

// Fixed passage-classroom wordings (short, child-safe, honest).
//
//nolint:gosec // G101 false positive on every name below: "passage" merely contains "pass"; these are fixed child-facing wordings, never credentials
const (
	// passageUnavailableNotice: no passage source wired server-side.
	passageUnavailableNotice = "课文练习暂时用不了，请稍后再试"
	// passageMissingNotice: the requested passage id does not exist.
	passageMissingNotice = "这篇课文找不到，请重新从家长页面打开"
	// passageOutOfScopeNotice: the passage exists but is outside the
	// practice scope (课次尚未学到且未手动加入) — an honest refusal, never a
	// silent lesson swap and never a partial-text fallback.
	passageOutOfScopeNotice = "这篇课文还在学习范围外，先学完对应的课再来背默吧"
	// passageScopeMissingNotice: the passage exists but the requested
	// practice-range row does not (removed, or another passage's range) —
	// the parent re-selects the range on the content page.
	passageScopeMissingNotice = "这个练习范围不见了，请回家长页面重新选择整篇或要背的段落"
	// passageNoReadNotice: repeat/slow on a 背默 task — the teacher must not
	// read the original (背默 ≠ 听写).
	passageNoReadNotice = "这是背默练习，老师不能把课文念给你听哦。先凭记忆写，实在想不起来就喊我，我只提醒你每段开头的字。"
	// passageHintAskLineNotice: a hint without a usable paragraph number.
	passageHintAskLineNotice = "你想不起哪一段？告诉我课文的第几段，我只提醒你这一段开头的字。"
	// passageMeaningNotice: meaning/example during the writing stage — no
	// stored interpretation exists, and the task is to finish first.
	passageMeaningNotice = "先把这段课文默写完，我们再一起聊聊它的意思吧。"
)

// resolvePassageLessonID derives the passage-attempt lesson id:
// "r0w-YYYYMMDD-HHMMSS.NNNNNNNNN-<passage>-<scope>" on the Beijing calendar.
// The nanosecond plus the two ids prevent two ranges opened in the same
// second from sharing an INSERT OR IGNORE row; the r0w- prefix keeps
// lessonKind's stable passage classification.
func resolvePassageLessonID(now time.Time, passageID, scopeID int64) string {
	cst := time.FixedZone("CST", 8*3600)
	local := now.In(cst)
	return fmt.Sprintf("r0w-%s-%d-%d", local.Format("20060102-150405.000000000"), passageID, scopeID)
}

// passageRangeLabel renders one practice range as parent/child copy: 整篇 or
// the original paragraph numbers in their own order (第 2、3 段). It is the
// single wording source for the greeting, the check bridge, the summary and
// the save receipt — one vocabulary, never re-derived per surface.
func passageRangeLabel(whole bool, paragraphs []int) string {
	if whole {
		return "整篇"
	}
	nums := make([]string, 0, len(paragraphs))
	for _, no := range paragraphs {
		nums = append(nums, fmt.Sprintf("%d", no))
	}
	return "第 " + strings.Join(nums, "、") + " 段"
}

// passageGreetingText opens the task: WHAT to write (title + THIS session's
// range), from memory, how many paragraphs — and the help/finish contract.
// It names title, author and paragraph numbers only, never a paragraph's
// text.
func passageGreetingText(title, author string, whole bool, paragraphs []int, total int) string {
	by := ""
	if strings.TrimSpace(author) != "" {
		by = "，是" + author + "写的"
	}
	unit := "这篇课文一共有"
	if !whole {
		unit = "这次要写"
	}
	return fmt.Sprintf("今天的任务是把《%s》%s默写下来%s。%s %d 个自然段，请凭记忆按顺序写在纸上，一段写一行。想不起来可以喊我提醒，写完了告诉我，我们对答案。",
		title, passageRangeLabel(whole, paragraphs), by, unit, total)
}

// passageResumeAckText re-opens the task after a pause.
func passageResumeAckText(title string, whole bool, paragraphs []int) string {
	return fmt.Sprintf("好，我们继续。接着把《%s》%s默写完，写好了叫我。", title, passageRangeLabel(whole, paragraphs))
}

// passageHintCueText is ONE paragraph's opening cue: the first min(2, n)
// characters plus the paragraph's length — enough to unblock, never the
// original itself.
func passageHintCueText(no int, paragraph string) string {
	runes := []rune(paragraph)
	head := 2
	if head > len(runes) {
		head = len(runes)
	}
	return fmt.Sprintf("第 %d 段的开头是「%s」，这一段比较长，再想想看。", no, string(runes[:head]))
}

// passageCheckBridgeText bridges 写完了 into the check stage (the panel rows
// are this range's own ordered paragraphs).
func passageCheckBridgeText(whole bool, paragraphs []int, total int) string {
	return fmt.Sprintf("%s都写完了，真棒！现在对照原文一段一段检查：这次一共 %d 段，每段点「对」还是「错」。", passageRangeLabel(whole, paragraphs), total)
}

// passageSummaryText builds the wrap line: the range's mastery result first
// (全部段落全对 or M/N 段), then the honest save-failure suffix when the
// record did not land (S-05: never a fabricated success).
func passageSummaryText(title string, whole bool, paragraphs []int, total, correct int, saveFailed bool) string {
	var b strings.Builder
	if total > 0 && correct == total {
		fmt.Fprintf(&b, "《%s》%s一共 %d 段全对，背得真棒！", title, passageRangeLabel(whole, paragraphs), total)
	} else {
		fmt.Fprintf(&b, "《%s》%s一共 %d 段，对了 %d 段。错的段落我们下次再背一背。", title, passageRangeLabel(whole, paragraphs), total, correct)
	}
	if saveFailed {
		b.WriteString(reviewFailedSuffix())
	}
	return b.String()
}

// passageSaveEntry is the save receipt's ONE requested entry: the work plus
// the practiced range (《题》的整篇 / 第 2、3 段) — the save scope is exactly
// that identity, never one entry per paragraph.
func passageSaveEntry(title string, whole bool, paragraphs []int) string {
	if strings.TrimSpace(title) == "" {
		title = "课文"
	}
	return title + "（" + passageRangeLabel(whole, paragraphs) + "）"
}

// passageTaskView projects the work + range identity for session_ready —
// never the text itself. Paragraphs carries the ORIGINAL 1-based indexes in
// the original's own order, so the page labels rows by the text's numbering.
func passageTaskView(p passage.Passage, scope passage.ScopeRow) *PassageTaskView {
	paragraphs := scope.Paragraphs
	if scope.Whole {
		paragraphs = make([]int, 0, len(p.Paragraphs))
		for i := range p.Paragraphs {
			paragraphs = append(paragraphs, i+1)
		}
	}
	return &PassageTaskView{
		ID:      p.ID,
		ScopeID: scope.ID,
		Title:   p.Title,
		Author:  p.Author,
		Method:  "背默",
		Whole:   scope.Whole,
		// ParagraphCount counts the PRACTICED range (== len(Paragraphs)) —
		// the page's banner and unit copy speak this session's task, never
		// the full work's length.
		Paragraphs:     paragraphs,
		ParagraphCount: len(paragraphs),
	}
}

// passageViewForRow projects a lesson ROW's passage identity (nil for
// non-passage rows). The title/author come from the passage store when it
// can serve them; the RANGE always comes from the ROW itself (what the child
// actually practiced — never a re-derivation against a possibly-edited
// scope list). A fetch failure degrades to the id/range view: the row's
// check panel still opens, only the banner shows less.
func (tm *TurnManager) passageViewForRow(l lesson.Lesson) *PassageTaskView {
	if l.PassageID <= 0 {
		return nil
	}
	view := &PassageTaskView{
		ID:             l.PassageID,
		ScopeID:        l.PassageScopeID,
		Whole:          l.PassageWhole,
		Paragraphs:     append([]int(nil), l.PassageParagraphs...),
		ParagraphCount: len(l.Cards),
		Method:         "背默",
	}
	if tm.cfg.Passages != nil {
		if p, err := tm.cfg.Passages.FetchPassage(context.Background(), l.PassageID); err == nil {
			view.Title, view.Author = p.Title, p.Author
		}
	}
	return view
}

// bindPassageRef attaches the passage identity to the live tutor (caller has
// already constructed it from the row or the init request). Caller holds NO
// lock — this runs between construction and message traffic.
func (tm *TurnManager) bindPassageRef(view *PassageTaskView) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	if tm.tutor == nil || view == nil {
		return
	}
	tm.tutor.passage = &passageRef{
		ID: view.ID, ScopeID: view.ScopeID,
		Title: view.Title, Author: view.Author,
		Whole: view.Whole, Paragraphs: append([]int(nil), view.Paragraphs...),
	}
}

// scopeParagraphs resolves the range's own paragraph TEXTS in original
// order: whole → every stored paragraph; a set → the named paragraphs at
// their original indexes. The scope row's indexes are already canonical
// (sorted, deduplicated, range-checked by the store that wrote them); a
// defensive bounds check keeps a corrupted row from panicking the classroom
// — an out-of-bounds index reports ok=false and the init refuses honestly.
func scopeParagraphs(p passage.Passage, scope passage.ScopeRow) ([]string, bool) {
	if scope.Whole {
		return append([]string(nil), p.Paragraphs...), true
	}
	texts := make([]string, 0, len(scope.Paragraphs))
	for _, no := range scope.Paragraphs {
		if no < 1 || no > len(p.Paragraphs) {
			return nil, false
		}
		texts = append(texts, p.Paragraphs[no-1])
	}
	return texts, len(texts) > 0
}

// handlePassageInit is the session_init{passage} branch (Issue #14): resolve
// the work, enforce the scope gate and the RANGE gate, then EITHER resume
// this range's in-flight attempt under its own id (a mid-writing reconnect
// or an unfinished check) or open a FRESH attempt row carrying exactly the
// selected paragraphs. A submitted attempt never reopens here — its judgment
// is settled history; re-practicing the range is a new row (段落历史不被伪
// 造).
func (tm *TurnManager) handlePassageInit(s Sender, passageID, scopeID int64) {
	if tm.cfg.Passages == nil {
		tm.recorder.Emit(EventError,
			infra.F("code", ErrCodeLessonUnavailable),
			infra.F("passage", "no_source"),
		)
		tm.sendError(s, ErrCodeLessonUnavailable, passageUnavailableNotice, false)
		return
	}
	// A passage classroom ALWAYS names its range: passage without scope is a
	// client bug, refused honestly rather than guessed (开始时明确本次范
	// 围).
	if scopeID <= 0 {
		tm.recorder.Emit(EventError,
			infra.F("code", ErrCodeLessonUnavailable),
			infra.F("passage", "no_scope"),
		)
		tm.sendError(s, ErrCodeLessonUnavailable, passageScopeMissingNotice, false)
		return
	}

	ctx := context.Background()
	p, err := tm.cfg.Passages.FetchPassage(ctx, passageID)
	switch {
	case errors.Is(err, passage.ErrPassageNotFound):
		tm.recorder.Emit(EventError,
			infra.F("code", ErrCodeLessonUnavailable),
			infra.F("passage", "not_found"),
		)
		tm.sendError(s, ErrCodeLessonUnavailable, passageMissingNotice, false)
		return
	case err != nil:
		tm.failLessonInit(s)
		return
	case !p.InScope:
		// 进入范围才有资格被安排 (Issue #4): the honest coded refusal —
		// never a silent fallback into a due-word lesson, never a partial
		// text.
		tm.recorder.Emit(EventError,
			infra.F("code", ErrCodeLessonUnavailable),
			infra.F("passage", "out_of_scope"),
		)
		tm.sendError(s, ErrCodeLessonUnavailable, passageOutOfScopeNotice, false)
		return
	}

	// The RANGE gate: the scope row must sit on THIS passage (another
	// passage's scope id, or a removed range, refuses honestly).
	var scope *passage.ScopeRow
	for i := range p.Scopes {
		if p.Scopes[i].ID == scopeID {
			scope = &p.Scopes[i]
			break
		}
	}
	if scope == nil {
		tm.recorder.Emit(EventError,
			infra.F("code", ErrCodeLessonUnavailable),
			infra.F("passage", "scope_not_found"),
		)
		tm.sendError(s, ErrCodeLessonUnavailable, passageScopeMissingNotice, false)
		return
	}
	texts, ok := scopeParagraphs(p, *scope)
	if !ok {
		tm.recorder.Emit(EventError,
			infra.F("code", ErrCodeLessonUnavailable),
			infra.F("passage", "scope_out_of_range"),
		)
		tm.sendError(s, ErrCodeLessonUnavailable, passageScopeMissingNotice, false)
		return
	}

	// The passage's OWN subject governs the row (a passage is stored with
	// its category; the poem twin hard-codes chinese because poems are one).
	tm.mu.Lock()
	tm.category = lesson.NormalizeCategory(p.Category)
	tm.mu.Unlock()

	// Resume probe: this RANGE's latest attempt row — keyed by content +
	// range, never the scope row id (a removed-and-re-added range keeps
	// resuming its in-flight attempt).
	latest, err := tm.exec.LatestPassageLessonID(ctx, p.ID, scope.Whole, scope.Paragraphs)
	if err != nil {
		tm.failLessonInit(s)
		return
	}
	if latest != "" {
		if l, gerr := tm.exec.GetLessonState(ctx, latest); gerr == nil &&
			l.PassageID == p.ID && len(l.Cards) > 0 && l.Stage != lesson.StageSubmitted &&
			l.PassageWhole == scope.Whole && passageIndexesEqual(l.PassageParagraphs, scope.Paragraphs) {
			// In-flight: an unfinished check reopens as the check panel; a
			// mid-writing attempt re-announces the task (the range's own
			// first-content-wins row continues).
			if l.Stage == lesson.StageChecking {
				tm.startCheckResume(s, l)
				return
			}
			tm.startPassageSession(s, l, p, *scope)
			return
		}
	}

	lessonID := resolvePassageLessonID(time.Now(), p.ID, scope.ID)
	// The per-lesson init lock discipline (two simultaneous connections for
	// this exact attempt id must not both create its row).
	unlock := lockLessonInit(lessonID)
	defer unlock()

	l, err := tm.exec.EnsurePassageLesson(ctx, lessonID, p.ID, scope.ID,
		scope.Whole, scope.Paragraphs, texts, p.Category)
	if err != nil {
		tm.failLessonInit(s)
		return
	}
	if l.PassageID != p.ID || l.PassageWhole != scope.Whole ||
		!passageIndexesEqual(l.PassageParagraphs, scope.Paragraphs) {
		// Defensive identity check: a lesson row must never announce one
		// range while checking another range's paragraphs.
		tm.recorder.Emit(EventError, infra.F("code", ErrCodeProviderFailed), infra.F("at", "passage_identity"))
		tm.failLessonInit(s)
		return
	}
	tm.recorder.Emit(EventPassageStarted,
		infra.F("paragraphs", len(texts)),
		infra.F("whole", scope.Whole),
	)
	tm.startPassageSession(s, l, p, *scope)
}

// passageIndexesEqual compares two paragraph index sets as sets (order and
// duplicates never matter — both sides are canonical in practice, but the
// resume probe must not misfire on a hand-edited row).
func passageIndexesEqual(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[int]int, len(a))
	for _, no := range a {
		seen[no]++
	}
	for _, no := range b {
		if seen[no] == 0 {
			return false
		}
		seen[no]--
	}
	return true
}

// startPassageSession binds the FSM to the passage row and announces the
// task. The greeting carries NO paragraph text, arms no timer, and its
// display equals its spoken text (title/author/range are public identity,
// not the answer).
func (tm *TurnManager) startPassageSession(s Sender, l lesson.Lesson, p passage.Passage, scope passage.ScopeRow) {
	view := passageTaskView(p, scope)
	tm.mu.Lock()
	tm.tutor = newDictationTutor(l)
	tm.tutor.passage = &passageRef{
		ID: l.PassageID, ScopeID: l.PassageScopeID,
		Title: p.Title, Author: p.Author,
		Whole: l.PassageWhole, Paragraphs: append([]int(nil), view.Paragraphs...),
	}
	tm.state = StateListening
	tm.sessionReadyAt = time.Now()
	total := len(tm.tutor.words)
	category := tm.tutor.category
	tm.mu.Unlock()

	_ = s.SendControl(SessionReadyMsg{
		Type:      TypeSessionReady,
		SessionID: tm.session,
		Audio:     AudioSpec{SampleRate: requiredSampleRate, Format: requiredFormat, Channels: requiredChannels},
		TTS:       TTSSpec{SampleRate: TTSOutputSampleRate},
		Lesson:    tm.lessonViewFor(l),
		Category:  category,
		Passage:   view,
	})
	tm.recorder.Emit(EventConnected, infra.F("session_id", tm.session))

	ctx, cancel := context.WithCancel(context.Background())
	tm.mu.Lock()
	tm.turnCancel = cancel
	tm.mu.Unlock()
	text := passageGreetingText(p.Title, p.Author, view.Whole, view.Paragraphs, total)
	// onAck is deliberately nil: the passage classroom arms NO per-word
	// timer (背默推进不靠机械计时) — nothing counts down while the child
	// writes.
	go tm.runAnnouncementDisplay(s, ctx, cancel, text, text, purposePassageGreeting, nil)
}

// buildPassageClassifyMessages assembles the passage writing-stage
// classification request: the intent set shrinks to confirm/pause/resume/
// hint/chat (repeat and slow exist only to be refused honestly;
// meaning/example likewise) and the ground truth names the title plus THIS
// session's range and count — never a paragraph's text.
func (tm *TurnManager) buildPassageClassifyMessages(transcript string) []mimo.Message {
	var user strings.Builder
	user.WriteString("学生说：")
	user.WriteString(transcript)
	tm.mu.Lock()
	total, title := 0, ""
	rangeText := ""
	if tm.tutor != nil {
		total = len(tm.tutor.words)
		if tm.tutor.passage != nil {
			title = tm.tutor.passage.Title
			rangeText = passageRangeLabel(tm.tutor.passage.Whole, tm.tutor.passage.Paragraphs)
		}
		if tm.tutor.paused {
			user.WriteString("\n（课堂正处于暂停状态，学生可能想继续）")
		}
		tm.tutor.appendShortHistoryLocked(&user)
	}
	tm.mu.Unlock()

	var sys strings.Builder
	sys.WriteString("你是一位耐心的小学老师，正在陪孩子完成课文背默练习。当前任务是《" + title + "》，这次默写" + rangeText + "，共 " + fmt.Sprint(total) + " 段。")
	sys.WriteString("学生正在凭记忆把指定的段落默写在纸上，你绝对不能念出任何一段课文原文。请把学生的话分类为一个 JSON 对象：{\"intent\":\"...\",\"reply\":\"...\",\"index\":N}。")
	sys.WriteString("intent 只能取以下值：")
	sys.WriteString("confirm（写完了/默好了/可以检查了）；")
	sys.WriteString("hint（想不起来某一段/提醒我一下，如果学生说了第几段，index 填课文的自然段号，就是任务里说的段号）；")
	sys.WriteString("pause（要休息/暂停）；resume（继续/接着写）；")
	sys.WriteString("repeat 或 slow（想再听一遍或慢一点读——学生可能把它当听写了）；")
	sys.WriteString("meaning 或 example（问课文的意思或用法）；chat（其它对话）。")
	sys.WriteString("reply 用一句简短、亲切的中文回应（不要在 reply 里出现任何一段课文原文）。index 只有 hint 需要填，其它 intent 填 0。只输出这个 JSON 对象。")

	return []mimo.Message{
		{Role: "system", Content: sys.String()},
		{Role: "user", Content: user.String()},
	}
}

// classifyPassageTurn is the passage writing stage's classifier: ONE Chat
// hop and exactly ONE correction retry (classifyTurn's discipline), then the
// deterministic fallback — a passage session never treats a failed
// classification as confirm (the check stage must never open by accident).
func (tm *TurnManager) classifyPassageTurn(ctx context.Context, transcript string) intentPayload {
	msgs := tm.buildPassageClassifyMessages(transcript)
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

// dispatchPassageTurn is the passage writing stage's branch table: confirm
// enters the check stage (the WHOLE task is one confirm — no per-paragraph
// advance), hint gives a paragraph's opening cue only, repeat/slow/
// meaning/example answer honest fixed refusals, pause/resume keep the
// classroom pause bit, chat speaks the reply. No branch arms the word timer.
func (tm *TurnManager) dispatchPassageTurn(s Sender, ctx context.Context, cancel context.CancelFunc, turnID, epoch uint64, p intentPayload) {
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
		tm.runAnnouncementAt(s, epoch, ctx, cancel, passageNoReadNotice, purposeChat, nil)
	case intentHint:
		tm.passageHint(s, ctx, cancel, epoch, p)
	case intentMeaning, intentExample:
		tm.runAnnouncementAt(s, epoch, ctx, cancel, passageMeaningNotice, purposeChat, nil)
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
		title, whole := "", false
		var paragraphs []int
		if !tm.applyTurnAt(epoch, func() error {
			tm.tutor.paused = false
			if tm.tutor.passage != nil {
				title = tm.tutor.passage.Title
				whole = tm.tutor.passage.Whole
				paragraphs = append([]int(nil), tm.tutor.passage.Paragraphs...)
			}
			return nil
		}) {
			return
		}
		tm.runAnnouncementAt(s, epoch, ctx, cancel, passageResumeAckText(title, whole, paragraphs), purposeChat, nil)
	case intentChat:
		tm.runAnnouncementDisplayAt(s, epoch, ctx, cancel, p.Reply, tm.replyDisplay(p.Reply), purposeChat, nil)
	}
	_ = turnID
}

// passageHint handles 求助: a named paragraph (the ORIGINAL 自然段号 the
// task announced) gets its OPENING characters — never the text; an unnamed,
// out-of-range or OUT-OF-RANGE-OF-THIS-TASK number gets the which-paragraph
// question. The hint count lands on that paragraph's outcome (the same U-9
// accounting the word classroom keeps).
func (tm *TurnManager) passageHint(s Sender, ctx context.Context, cancel context.CancelFunc, epoch uint64, p intentPayload) {
	var cue string
	hintPos, hintCount := -1, 0
	if !tm.applyTurnAt(epoch, func() error {
		if tm.tutor.passage != nil {
			for pos, no := range tm.tutor.passage.Paragraphs {
				if no == p.Index && pos < len(tm.tutor.words) {
					cue = passageHintCueText(no, tm.tutor.words[pos].Word)
					hintPos, hintCount = pos, tm.tutor.recordHint(pos)
					break
				}
			}
		}
		return nil
	}) {
		return
	}
	if cue == "" {
		cue = passageHintAskLineNotice
	} else {
		tm.recorder.Emit(EventHintGiven,
			infra.F("word_index", hintPos), infra.F("hint_count", hintCount))
	}
	tm.runAnnouncementAt(s, epoch, ctx, cancel, cue, purposeChat, nil)
}

// submitPassage is the passage check_submit: the SAME per-paragraph manual
// gate (D-05) and the SAME CompleteCheck latch as every other submit, then
// the content + range save instead of any word write.
func (tm *TurnManager) submitPassage(s Sender, m CheckSubmitMsg) {
	if m.RequestID == "" {
		tm.sendError(s, ErrCodeInvalidMessage, checkVerdictInvalidNotice, true)
		return
	}

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

	ctx, cancel := context.WithCancel(context.Background())
	tm.mu.Lock()
	tm.turnCancel = cancel
	tm.mu.Unlock()

	res, err := tm.exec.CompleteCheck(ctx, lessonID, expected, m.RequestID, false)
	if err != nil {
		var conflict *lesson.ConflictError
		if errors.As(err, &conflict) {
			tm.mu.Lock()
			tm.tutor.checking = false
			tm.mu.Unlock()
			tm.recorder.Emit(EventError,
				infra.F("code", ErrCodeDuplicateSubmit),
				infra.F("duplicate", "passage_submit_conflict"),
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
			infra.F("duplicate", "passage_submit_latch"),
		)
		return
	}

	tm.mu.Lock()
	tm.tutor.version = res.NewVersion
	tm.tutor.checking = false
	ref := *tm.tutor.passage
	tm.mu.Unlock()

	go tm.finishPassageSubmit(s, ctx, cancel, ref, lessonID, total, res, m.RequestID)
}

// finishPassageSubmit runs on its own goroutine (the ACK-gated ending
// blocks; the read loop must keep serving acks): compute the judged
// paragraph counts from the STORED verdict readback (the row is the
// authority), write ONE passage_practice_logs row bracketed by the
// two-phase kind=passage receipt, ship the honest review_result, then speak
// the wrap line through the ACK gate and end with session_ended
// (lesson_complete).
func (tm *TurnManager) finishPassageSubmit(s Sender, ctx context.Context, cancel context.CancelFunc, ref passageRef, lessonID string, total int, res lesson.CompleteCheckResult, requestID string) {
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

	requestedSet := []string{passageSaveEntry(ref.Title, ref.Whole, ref.Paragraphs)}

	recorded := false
	saveFailed := true
	if tm.cfg.PassagePractice != nil {
		// Receipt phase one: pending BEFORE the write (已提交 ≠ 已保存).
		receiptID := int64(0)
		if tm.cfg.Receipts != nil {
			rec, rerr := tm.cfg.Receipts.Begin(ctx, lesson.ReceiptInput{
				LessonID:  lessonID,
				RequestID: requestID,
				Kind:      lesson.ReceiptKindPassage,
				Requested: requestedSet,
			})
			if rerr != nil || rec.ID <= 0 {
				tm.recorder.Emit(EventError, infra.F("code", ErrCodeProviderFailed), infra.F("at", "save_receipt"))
			} else {
				receiptID = rec.ID
			}
		}

		// The save identity: content + range. A whole attempt stores the
		// EMPTY paragraph set (ref.Paragraphs is the DISPLAY numbering
		// [1..N] there — never the save identity), a paragraph attempt its
		// own canonical set.
		paragraphs := append([]int(nil), ref.Paragraphs...)
		if ref.Whole {
			paragraphs = nil
		}
		rec := passage.PracticeRecord{
			PassageID:         ref.ID,
			ScopeID:           ref.ScopeID,
			Whole:             ref.Whole,
			Paragraphs:        paragraphs,
			LessonID:          lessonID,
			TotalParagraphs:   total,
			CorrectParagraphs: correct,
			Hints:             hints,
			Mastered:          mastered,
			ReviewedDay:       beijingDay(time.Now()),
		}
		if err := tm.cfg.PassagePractice.RecordPractice(ctx, rec); err != nil {
			// Honest failure: the record was NOT saved — never a narrated
			// success (S-05). The judgment (verdicts, stage=submitted) is
			// already persisted on the lesson row and stays visible.
			tm.recorder.Emit(EventError, infra.F("code", ErrCodeProviderFailed))
			if tm.cfg.Receipts != nil && receiptID > 0 {
				if cerr := tm.cfg.Receipts.Complete(ctx, receiptID, nil, requestedSet, false); cerr != nil {
					tm.recorder.Emit(EventError, infra.F("code", ErrCodeProviderFailed), infra.F("at", "save_receipt"))
				}
			}
		} else {
			recorded = true
			saveFailed = false
			tm.recorder.Emit(EventPassageSubmitted,
				infra.F("total", total),
				infra.F("correct", correct),
				infra.F("mastered", mastered),
				infra.F("whole", ref.Whole),
			)
			if tm.cfg.Receipts != nil && receiptID > 0 {
				if cerr := tm.cfg.Receipts.Complete(ctx, receiptID, requestedSet, nil, true); cerr != nil {
					tm.recorder.Emit(EventError, infra.F("code", ErrCodeProviderFailed), infra.F("at", "save_receipt"))
				}
			}
		}
	} else {
		// Degraded wiring: no passage practice store — the submit reports
		// the honest zero-saved result (never a fabricated record).
		tm.recorder.Emit(EventError,
			infra.F("code", ErrCodeProviderFailed),
			infra.F("at", "passage_practice"),
		)
	}

	updated := 0
	missing := 1
	if recorded {
		updated, missing = 1, 0
	}
	tm.mu.Lock()
	tm.tutor.reviewSeen = true
	tm.mu.Unlock()

	_ = s.SendControl(ReviewResultMsg{
		Type:      TypeReviewResult,
		Requested: 1,
		Updated:   updated,
		Missing:   missing,
		Unchecked: false,
	})

	summary := passageSummaryText(ref.Title, ref.Whole, ref.Paragraphs, total, correct, saveFailed)
	tm.runNaturalEnding(s, ctx, cancel, summary, purposePassageSummary, ReasonLessonComplete)
}
