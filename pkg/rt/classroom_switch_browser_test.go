//go:build rtbrowser

// classroom_switch_browser_test.go — issue #69 rethink3 S4：真实浏览器的
// R0+R1 验收。所有竞态都经过真实 /rt Handler、真实 connWriter、生产
// /tutor/ 页面与生产播放器；换课是同一条真实 WebSocket 连接内的第二个
// session_init（生产支持、页面总是新连，故由 Go 侧经生产入口
// HandleControl＋该连接的真实 writer 注入——与读循环同构的异步来源），
// A、B 使用各自独立的真实课堂行（临时 SQLite 里的真实 lesson 行）。
// 自然播放产生 ACK：对播放的全部断言都来自页面原生 onended 后的真实
// 上行帧与播放器自身的登记（ledger/trace/droppedChunks），不是消息数量。
//
// 运行（命令必须带 -tags=rtbrowser）：
//
//	GOTOOLCHAIN=go1.26.9 go test -tags=rtbrowser ./pkg/rt -run 'TestClassroom' -count=1
package rt

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
)

// speechExpectedAskRound mirrors speakTurn's split of subjectAskText.
var speechExpectedAskRound = []string{
	"语文和英语今天都有要默写的词语。",
	"这次想先默写哪一科？",
	"对我说语文，或者英语，也可以点屏幕上的按钮。",
}

// speechExpectedSummaryAllCorrect mirrors checkedSummaryText(2, 2, 2, 2, false)
// split by speakTurn — the fixture's scripted review result saves both words.
var speechExpectedSummaryAllCorrect = []string{
	"今天的 2 个词都默写完了，对了 2 个。",
	"全部都写对了，你真棒！",
}

// speechEnglishWords is the fixture's independent english due batch: B's
// dictation classroom in the opening/ask switch cases (a REAL row of its own
// in the same temp SQLite — never the chinese row A runs on).
var speechEnglishWords = []lesson.Word{
	{Word: "apple", Meaning: "fixture english one"},
	{Word: "banana", Meaning: "fixture english two"},
}

// speechInjectSessionInit drives one same-connection classroom switch through
// the production entry: the second session_init rides the connection's REAL
// writer (the page cannot re-init in place — its every switch opens a new
// socket — so the deterministic interleaving uses the same entry the read
// loop uses). The call returns once the handler has decided (install or
// refusal), giving the test the decision point.
func speechInjectSessionInit(t *testing.T, f *speechBrowserFixture, m *speechManagerTracker, category string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var s Sender
	for time.Now().Before(deadline) {
		if s = m.currentSender(); s != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if s == nil {
		t.Fatal("the browser connection's writer was never observed")
	}
	m.HandleControl(s, mustJSON(t, SessionInitMsg{
		Type:     TypeSessionInit,
		Category: category,
		Audio:    AudioSpec{SampleRate: 16000, Format: "s16le", Channels: 1},
	}))
}

// speechAckEpochCount tallies the page's real sentence_acks for one epoch.
func speechAckEpochCount(m *speechManagerTracker, epoch uint64) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, a := range m.acks {
		if a.Epoch == epoch {
			n++
		}
	}
	return n
}

// speechTutorState is the live classroom's in-memory writeback surface.
type speechTutorState struct {
	lessonID  string
	version   int
	checking  bool
	practice  bool
	reviewSee bool
}

func speechTutorStateOf(m *speechManagerTracker) speechTutorState {
	m.TurnManager.mu.Lock()
	defer m.TurnManager.mu.Unlock()
	if m.TurnManager.tutor == nil {
		return speechTutorState{}
	}
	t := m.TurnManager.tutor
	return speechTutorState{lessonID: t.lessonID, version: t.version, checking: t.checking,
		practice: t.practice, reviewSee: t.reviewSeen}
}

// speechSeedEnglishCheckingWithManualVerdicts opens B's REAL english row in
// the check stage with every word manually verdicted (the page's submit gate
// mirrors the server's manual gate, so the panel's 提交 is enabled).
func speechSeedEnglishCheckingWithManualVerdicts(f *speechBrowserFixture) {
	f.t.Helper()
	ctx := context.Background()
	lessonID := lessonIDForCategory("speech-browser-fixture", time.Now(), lesson.CategoryEnglish)
	cards := speechEnglishWords
	l, err := f.tools.EnsureLessonCards(ctx, lessonID, cards, lesson.CategoryEnglish)
	if err == nil {
		var advance lesson.AdvanceResult
		advance, err = f.tools.AdvanceWord(ctx, lessonID, l.Version, "switch-b-seed-1")
		if err == nil {
			advance, err = f.tools.AdvanceWord(ctx, lessonID, advance.NewVersion, "switch-b-seed-2")
		}
		if err == nil {
			_, err = f.tools.EnterCheck(ctx, lessonID, advance.NewVersion, "switch-b-seed-check", nil)
		}
	}
	if err != nil {
		f.t.Fatalf("seed english checking row: %v", err)
	}
	if l, err = f.tools.GetLessonState(ctx, lessonID); err != nil {
		f.t.Fatalf("read english seeded row: %v", err)
	}
	for idx := range l.Cards {
		res, verr := f.tools.ApplyVerdict(ctx, lessonID, l.Version, fmt.Sprintf("switch-b-seed-manual-%d", idx), lesson.CheckVerdict{
			WordIndex: idx, Correct: true, Manual: true,
		})
		if verr != nil {
			f.t.Fatalf("seed english manual verdict %d: %v", idx, verr)
		}
		l.Version = res.NewVersion
	}
}

// speechCategoryFetchHold parks the FIRST FetchDueWords of one category (S4's
// late-continuation control point: the answered ask's own row fetch) and
// passes every other call through — the switched classroom's fetch must
// proceed while A parks, so B installs and greets for real.
type speechCategoryFetchHold struct {
	inner    WordSource
	category string

	mu        sync.Mutex
	entered   chan struct{}
	release   chan struct{}
	enterOnce sync.Once
	openOnce  sync.Once
}

func speechNewCategoryFetchHold(inner WordSource, category string) *speechCategoryFetchHold {
	return &speechCategoryFetchHold{
		inner: inner, category: category,
		entered: make(chan struct{}), release: make(chan struct{}),
	}
}

func (h *speechCategoryFetchHold) FetchDueWords(ctx context.Context, limit int, category string) ([]lesson.Word, error) {
	h.mu.Lock()
	park := category == h.category
	h.mu.Unlock()
	if park {
		h.enterOnce.Do(func() { close(h.entered) })
		<-h.release
	}
	return h.inner.FetchDueWords(ctx, limit, category)
}

func (h *speechCategoryFetchHold) PeekDueWords(ctx context.Context, limit int, category string) ([]lesson.Word, error) {
	return h.inner.PeekDueWords(ctx, limit, category)
}

func (h *speechCategoryFetchHold) SubmitReview(ctx context.Context, results map[string]string, category string) (ReviewWriteResult, error) {
	return h.inner.SubmitReview(ctx, results, category)
}

func (h *speechCategoryFetchHold) openUp() { h.openOnce.Do(func() { close(h.release) }) }

// speechWantRecoverableBusyError asserts the same-lessonID reentry refusal
// shape among the page's observed error frames.
func speechWantRecoverableBusyError(t *testing.T, res speechDriverResult) {
	t.Helper()
	busy := 0
	for _, e := range res.ErrorFrames {
		if e.Code == ErrCodeLessonUnavailable {
			busy++
			if !e.Recoverable {
				t.Fatalf("the same-lessonID reentry error must be recoverable, got %q", e.Message)
			}
		}
	}
	if busy != 1 {
		t.Fatalf("lesson_unavailable error frames = %d, want exactly 1 (the refused reentry): %+v", busy, res.ErrorFrames)
	}
}

// ---------------------------------------------------------------------------
// Case V — the current-classroom successful submit (R0's T1 success half in a
// real browser): everything natural, and the in-memory writeback must mirror
// the row exactly (the version=-6900/checking=true replay dies here).
// ---------------------------------------------------------------------------

func TestClassroomSubmitNaturalBrowser(t *testing.T) {
	f := newSpeechBrowserFixture(t)
	f.armPoemPassageSaveChain() // a REAL receipt store on the same temp SQLite
	f.seedCheckingWithManualVerdicts()
	// The fake's scripted FSRS answer saves both words — the summary then
	// carries no partial-save suffix.
	f.mu.Lock()
	f.words.reviewResult = ReviewWriteResult{
		Requested: []string{"听写浏览器", "第二词"},
		Updated:   []string{"听写浏览器", "第二词"},
	}
	f.mu.Unlock()

	driver := f.runDriverAsync("check-submit-natural", 60*time.Second,
		"--expected-second-json", string(mustJSON(t, speechExpectedSummaryAllCorrect)))
	res, got := f.recvSpeechDriver(driver, speechReclaimBound)
	if !got || !res.OK {
		f.reclaimAndFail(driver, nil, "the natural submit journey failed: %s", res.Err)
	}
	if len(res.Rounds) != 2 || !res.Rounds[0].AllAckedNaturally || !res.Rounds[1].AllAckedNaturally {
		t.Fatalf("rounds = %+v, want the bridge and the summary both acked naturally", res.Rounds)
	}
	if res.ReadyCount != 1 || res.CheckStartedCount != 1 || res.ReviewResultCount != 1 ||
		res.SessionEndedCount != 1 || res.WSCloseCount != 1 || len(res.InterruptAcks) != 0 {
		t.Fatalf("ready=%d check_started=%d review=%d ended=%d close=%d acks=%v, want 1/1/1/1/1/0",
			res.ReadyCount, res.CheckStartedCount, res.ReviewResultCount, res.SessionEndedCount, res.WSCloseCount, res.InterruptAcks)
	}
	if len(res.ErrorFrames) != 0 {
		t.Fatalf("error frames = %+v, want none on the natural path", res.ErrorFrames)
	}
	if !f.waitManagersClosed(15*time.Second) || !waitFor(t, 15*time.Second, f.speechServerTasksSettled) {
		t.Fatal("the connection or its server tasks did not settle after the natural ending")
	}

	lessonID := lessonIDForCategory("speech-browser-fixture", time.Now(), lesson.CategoryChinese)
	l, err := f.tools.GetLessonState(context.Background(), lessonID)
	if err != nil {
		t.Fatalf("read stored lesson: %v", err)
	}
	if l.Stage != lesson.StageSubmitted || l.Unchecked || len(l.Verdicts) != 2 {
		t.Fatalf("row = stage %q unchecked %v verdicts %d, want submitted/false/2", l.Stage, l.Unchecked, len(l.Verdicts))
	}
	if subs := f.words.submissions(); len(subs) != 1 || len(subs[0]) != 2 {
		t.Fatalf("FSRS saves = %v, want exactly one save of both words", subs)
	}
	if cats := f.words.submittedCategories(); len(cats) != 1 || cats[0] != "chinese" {
		t.Fatalf("FSRS save categories = %v, want [chinese]", cats)
	}
	receipts, err := f.receipts.ListByLesson(context.Background(), lessonID)
	if err != nil || len(receipts) != 1 || receipts[0].Status != lesson.ReceiptStatusSaved {
		t.Fatalf("receipts = %+v err=%v, want exactly one saved receipt for the row", receipts, err)
	}
	m := f.tryFirstManager(5 * time.Second)
	if m == nil {
		t.Fatal("no /rt connection was created")
	}
	tutor := speechTutorStateOf(m)
	if tutor.version != l.Version || tutor.checking || !tutor.reviewSee {
		t.Fatalf("writeback tutor version/checking/reviewSeen = %d/%v/%v, want %d/false/true mirroring the row — a polluted writeback must die here",
			tutor.version, tutor.checking, tutor.reviewSee, l.Version)
	}
}

// ---------------------------------------------------------------------------
// Case S — A's accepted submit parked across a same-connection switch to an
// independent REAL row (R1's T1 writeback half in a real browser): B finishes
// its own opening naturally before A resumes; A's save lands on A's row with
// A's receipt and zero late presentation; B's version/checking stay B's; B's
// own submit then succeeds through the real panel.
// ---------------------------------------------------------------------------

func TestClassroomSwitchDuringSubmitBrowser(t *testing.T) {
	f := newSpeechBrowserFixture(t)
	f.armPoemPassageSaveChain()
	f.seedCheckingWithManualVerdicts()             // A: today's REAL chinese row, checking
	speechSeedEnglishCheckingWithManualVerdicts(f) // B: an independent REAL row
	f.mu.Lock()
	f.words.reviewResult = ReviewWriteResult{
		Requested: []string{"听写浏览器", "第二词"},
		Updated:   []string{"听写浏览器", "第二词"},
	}
	f.mu.Unlock()
	before, _ := f.gateCheckSubmit(true, false)
	t.Cleanup(before.release)

	bridge := string(mustJSON(t, speechExpectedCheckResume))
	summary := string(mustJSON(t, speechExpectedSummaryAllCorrect))
	// late-window-ms bounds the driver's wait for the SERVER's switch gate
	// (armed only after A's released task truly exits), not a fixed window.
	driver := f.runDriverAsync("classroom-switch-submit", 60*time.Second,
		"--expected-second-json", bridge, "--expected-third-json", summary,
		"--late-window-ms", "15000")

	if !speechGateHit(before, 30*time.Second) {
		f.reclaimAndFail(driver, []func(){before.release}, "A's formal submit never reached the real latch write")
	}
	m := f.tryFirstManager(5 * time.Second)
	if m == nil {
		f.reclaimAndFail(driver, []func(){before.release}, "no /rt connection was created before the deadline")
	}

	// The switch: B's session_init through the production entry on the REAL
	// connection while A's accepted save is parked.
	speechInjectSessionInit(t, f, m, "english")
	if !waitFor(t, 30*time.Second, func() bool { return f.announcementDone.Load() >= 2 }) {
		f.reclaimAndFail(driver, []func(){before.release}, "B's resume bridge never settled through the real page")
	}

	// B's live classroom state, captured BEFORE A resumes.
	want := speechTutorStateOf(m)
	if want.lessonID == "" || want.checking != true {
		f.reclaimAndFail(driver, []func(){before.release}, "B's checking classroom was not the live one at the switch (state %+v)", want)
	}

	// Resume A: the accepted save completes on its own row; its presentation
	// must be refused by the switched epoch AND its writeback by the scope.
	before.release()
	if !waitFor(t, 30*time.Second, func() bool { return m.TurnManager.serverTasksSettled() }) {
		f.reclaimAndFail(driver, nil, "A's superseded submit task never finished after the release")
	}
	// The late window (and B's own submit beyond it) starts only after the
	// server confirmed A's true exit — the gate hands the completion over.
	f.armSwitchGate()
	got := speechTutorStateOf(m)
	if got != want {
		f.reclaimAndFail(driver, nil,
			"B's tutor after A's superseded save = %+v, want %+v — the writeback scope gate must keep B's version/checking B's own", got, want)
	}

	// A's row keeps the save, its words, and exactly one saved receipt.
	lessonIDA := lessonIDForCategory("speech-browser-fixture", time.Now(), lesson.CategoryChinese)
	l, err := f.tools.GetLessonState(context.Background(), lessonIDA)
	if err != nil {
		f.reclaimAndFail(driver, nil, "read A's row: %v", err)
	}
	if l.Stage != lesson.StageSubmitted || l.Unchecked || len(l.Verdicts) != 2 {
		f.reclaimAndFail(driver, nil, "A's row = stage %q unchecked %v verdicts %d, want submitted/false/2 — the accepted save must complete",
			l.Stage, l.Unchecked, len(l.Verdicts))
	}
	if subs := f.words.submittedCategories(); len(subs) != 1 || subs[0] != "chinese" {
		f.reclaimAndFail(driver, nil, "FSRS save categories after A's release = %v, want exactly [chinese] (A's save, nothing of B's yet)", subs)
	}
	receipts, err := f.receipts.ListByLesson(context.Background(), lessonIDA)
	if err != nil || len(receipts) != 1 || receipts[0].Status != lesson.ReceiptStatusSaved {
		f.reclaimAndFail(driver, nil, "A's receipts = %+v err=%v, want exactly one saved receipt owned by A's row", receipts, err)
	}

	res, gotDriver := f.recvSpeechDriver(driver, speechReclaimBound)
	if !gotDriver || !res.OK {
		f.reclaimAndFail(driver, nil, "B's own submit journey failed: %s (late window %+v)", res.Err, res.LateWindow)
	}
	if lw := res.LateWindow; lw.TeacherSentences != 0 || lw.ReviewResults != 0 || lw.SessionEnded != 0 || lw.Errors != 0 {
		t.Fatalf("late window after B's bridge = %+v, want zero A-side output — the switched epoch must refuse the superseded presentation", lw)
	}
	if len(res.Rounds) != 3 || !res.Rounds[0].AllAckedNaturally || !res.Rounds[1].AllAckedNaturally || !res.Rounds[2].AllAckedNaturally {
		t.Fatalf("rounds = %+v, want A bridge, B bridge and B summary all acked naturally", res.Rounds)
	}
	if res.ReadyCount != 2 || res.CheckStartedCount != 2 || res.ReviewResultCount != 1 || res.SessionEndedCount != 1 || res.WSCloseCount != 1 {
		t.Fatalf("ready=%d check_started=%d review=%d ended=%d close=%d, want 2/2/1/1/1 — B's own result and single ending",
			res.ReadyCount, res.CheckStartedCount, res.ReviewResultCount, res.SessionEndedCount, res.WSCloseCount)
	}
	if len(res.InterruptAcks) != 1 || res.InterruptAcks[0].OldEpoch != 0 || res.InterruptAcks[0].NewEpoch != 1 {
		t.Fatalf("interrupt_acks = %+v, want exactly the 0→1 install notice before B's session_ready", res.InterruptAcks)
	}
	if len(res.ReadyIndexes) < 2 || res.InterruptAcks[0].Index >= res.ReadyIndexes[1] || res.InterruptAcks[0].Index <= res.ReadyIndexes[0] {
		t.Fatalf("install ack index %d vs ready indexes %v — the epoch notice must precede B's session_ready",
			res.InterruptAcks[0].Index, res.ReadyIndexes)
	}
	if len(res.ErrorFrames) != 0 {
		t.Fatalf("error frames = %+v, want none on the switched journey", res.ErrorFrames)
	}

	// B's own submit landed on B's row; its writeback mirrors B's row exactly.
	lessonIDB := lessonIDForCategory("speech-browser-fixture", time.Now(), lesson.CategoryEnglish)
	lb, err := f.tools.GetLessonState(context.Background(), lessonIDB)
	if err != nil {
		t.Fatalf("read B's row: %v", err)
	}
	if lb.Stage != lesson.StageSubmitted || len(lb.Verdicts) != 2 {
		t.Fatalf("B's row = stage %q verdicts %d, want submitted/2 through B's own submit", lb.Stage, len(lb.Verdicts))
	}
	if cats := f.words.submittedCategories(); len(cats) != 2 || cats[0] != "chinese" || cats[1] != "english" {
		t.Fatalf("FSRS save categories = %v, want [chinese english] — one save per classroom", cats)
	}
	rb, err := f.receipts.ListByLesson(context.Background(), lessonIDB)
	if err != nil || len(rb) != 1 || rb[0].Status != lesson.ReceiptStatusSaved {
		t.Fatalf("B's receipts = %+v err=%v, want exactly one saved receipt owned by B's row", rb, err)
	}
	tutorB := speechTutorStateOf(m)
	if tutorB.version != lb.Version || tutorB.checking || !tutorB.reviewSee {
		t.Fatalf("B's writeback tutor version/checking/reviewSeen = %d/%v/%v, want %d/false/true mirroring B's row — the replayed version=-6900/checking=true must die here too",
			tutorB.version, tutorB.checking, tutorB.reviewSee, lb.Version)
	}
	if !f.waitManagersClosed(15*time.Second) || !waitFor(t, 15*time.Second, f.speechServerTasksSettled) {
		t.Fatal("the connection or its server tasks did not settle after B's ending")
	}
}

// ---------------------------------------------------------------------------
// Case E — the mid-opening switch (R1's epoch-notice half in a real browser):
// A's opening is playing with its later sentences already queued when the
// connection switches to B. The page must get interrupt_ack BEFORE B's
// session_ready (the queued old audio stops; the player adopts B's epoch and
// naturally ACKs B's whole opening). The mutation verdict rides the player's
// registration, never a frame count.
// ---------------------------------------------------------------------------

func TestClassroomSwitchOpeningEpochBrowser(t *testing.T) {
	f := newSpeechBrowserFixture(t)
	// B's dictation fetch batch (english): armed now, consumed only by B's
	// init — A's own entry resolves through the chinese peek only.
	f.words.setDue("english", speechEnglishWords)

	opening := string(mustJSON(t, speechExpectedOpening))
	driver := f.runDriverAsync("classroom-switch-opening", 75*time.Second,
		"--expected-second-json", opening, "--reject-margin-ms", "8000")

	m := f.tryFirstManager(10 * time.Second)
	if m == nil {
		f.reclaimAndFail(driver, nil, "no /rt connection was created before the deadline")
	}
	// The page's FIRST natural ack of A's opening is the switch instant:
	// sentence 1 has played, sentences 2-3 sit queued on the player.
	if !waitFor(t, 45*time.Second, func() bool { return speechAckEpochCount(m, 0) >= 1 }) {
		f.reclaimAndFail(driver, nil, "A's opening never produced its first natural ack")
	}
	speechInjectSessionInit(t, f, m, "english")

	res, gotDriver := f.recvSpeechDriver(driver, speechReclaimBound)
	if !gotDriver || !res.OK {
		f.reclaimAndFail(driver, nil, "the opening-switch journey failed: %s", res.Err)
	}
	if res.SecondRoundVerdict != "acked" {
		t.Fatalf("B's opening verdict = %q with rounds %+v (dropped=%d) — the player's registration must show B's audio accepted and naturally acked",
			res.SecondRoundVerdict, res.Rounds, res.Playback.DroppedChunks)
	}
	if len(res.Rounds) != 2 || !res.Rounds[1].AllAckedNaturally || res.Rounds[1].Epoch != 1 {
		t.Fatalf("B's round = %+v, want epoch 1 fully acked naturally", res.Rounds)
	}
	if res.Playback.DroppedChunks != 0 {
		t.Fatalf("dropped chunks = %d, want 0 — every delivered chunk must play", res.Playback.DroppedChunks)
	}
	if res.ReadyCount != 2 || len(res.InterruptAcks) != 1 || !res.InterruptAckBeforeReady {
		t.Fatalf("ready=%d acks=%+v before-ready=%v, want 2 readys and the 0→1 install notice ordered between them",
			res.ReadyCount, res.InterruptAcks, res.InterruptAckBeforeReady)
	}
	if len(res.ErrorFrames) != 0 {
		t.Fatalf("error frames = %+v, want none", res.ErrorFrames)
	}

	// The queued old audio stopped: at least one delivered sentence of A's
	// opening never completed (its ack never came) and the player cancelled.
	a := res.Rounds[0]
	aAcks := 0
	for _, s := range a.Sentences {
		aAcks += s.Acks
	}
	if len(a.Sentences) < 2 || aAcks != 1 || res.StopNowCount < 1 {
		t.Fatalf("A's opening delivered %d sentences with %d acks and stopNow=%d, want ≥2 delivered, exactly 1 acked (the queued remainder stopped by the switch)",
			len(a.Sentences), aAcks, res.StopNowCount)
	}
	if got := speechAckEpochCount(m, 0); got != 1 {
		t.Fatalf("server-side acks for epoch 0 = %d, want exactly 1 (the stopped queue never acks)", got)
	}
	if got := speechAckEpochCount(m, 1); got != len(res.Rounds[1].Sentences) {
		t.Fatalf("server-side acks for epoch 1 = %d, want %d natural acks of B's opening", got, len(res.Rounds[1].Sentences))
	}
	if res.SessionEndedCount != 0 || res.WSCloseCount != 0 {
		t.Fatalf("session_ended=%d close=%d, want 0/0 — B's live classroom keeps the connection", res.SessionEndedCount, res.WSCloseCount)
	}
	if !f.waitManagersClosed(15*time.Second) || !waitFor(t, 15*time.Second, f.speechServerTasksSettled) {
		t.Fatal("the connection or its server tasks did not settle after the page left")
	}
	if state := speechTutorStateOf(m); state.lessonID != lessonIDForCategory("speech-browser-fixture", time.Now(), lesson.CategoryEnglish) {
		t.Fatalf("live classroom at the end = %q, want B's english row", state.lessonID)
	}
}

// ---------------------------------------------------------------------------
// Case G — the late ask continuation (S2's fix held in a real browser): the
// answered 语文 continuation parks at its own row fetch; B (an independent
// REAL english row) installs and finishes its opening naturally; A's release
// must stay completely silent — no second session_ready, no A greeting, no
// second epoch notice.
// ---------------------------------------------------------------------------

func TestLateAskContinuationSilentBrowser(t *testing.T) {
	f := newSpeechBrowserFixture(t)
	f.words.setDue("english", speechEnglishWords)
	f.words.setPeek("english", speechEnglishWords[:1]) // both subjects due → the ask
	f.mu.Lock()
	f.wordsOverride = speechNewCategoryFetchHold(f.words, "chinese")
	f.expectedSentences = append([]string(nil), speechExpectedAskRound...)
	f.mu.Unlock()
	// The child's spoken 语文 answer rides the real capture → VAD → ASR.
	f.asr.mu.Lock()
	f.asr.script = append(f.asr.script, asrScript{text: "语文"})
	f.asr.mu.Unlock()

	opening := string(mustJSON(t, speechExpectedOpening))
	// late-window-ms now bounds the driver's wait for the SERVER's switch
	// gate (which arms only after the released continuation's task truly
	// exits), not a fixed observation window (review P1-4).
	driver := f.runDriverAsync("subject-ask-switch", 75*time.Second,
		"--expected-second-json", opening, "--late-window-ms", "15000")

	m := f.tryFirstManager(10 * time.Second)
	if m == nil {
		f.reclaimAndFail(driver, nil, "no /rt connection was created before the deadline")
	}
	hold, _ := f.wordsOverride.(*speechCategoryFetchHold)
	if hold == nil {
		f.reclaimAndFail(driver, nil, "the chinese fetch hold was not mounted")
	}
	if !speechChHit(hold.entered, 45*time.Second) {
		hold.openUp()
		f.reclaimAndFail(driver, nil, "the answered continuation never reached its own row fetch")
	}

	speechInjectSessionInit(t, f, m, "english")
	if !waitFor(t, 45*time.Second, func() bool { return f.announcementDone.Load() >= 1 }) {
		hold.openUp()
		f.reclaimAndFail(driver, nil, "B's opening never settled through the real page")
	}
	hold.openUp()
	if !waitFor(t, 30*time.Second, func() bool { return m.TurnManager.serverTasksSettled() }) {
		f.reclaimAndFail(driver, nil, "the late continuation's task never finished after the release")
	}
	// Only now may the driver count its late window: the voice-turn
	// registration (P1-4) makes settled cover the continuation's true exit,
	// and the gate hands that completion to the page side — no fixed window
	// stands in for the relation.
	f.armSwitchGate()

	res, gotDriver := f.recvSpeechDriver(driver, speechReclaimBound)
	if !gotDriver || !res.OK {
		f.reclaimAndFail(driver, nil, "the ask-switch journey failed: %s", res.Err)
	}
	if res.ReadyCount != 1 {
		t.Fatalf("session_ready count = %d, want exactly 1 (B's) — a late continuation must never install or ship a ready of its own", res.ReadyCount)
	}
	if len(res.InterruptAcks) != 1 || res.InterruptAcks[0].OldEpoch != 0 || res.InterruptAcks[0].NewEpoch != 1 {
		t.Fatalf("interrupt_acks = %+v, want exactly the pick's 0→1 notice (B installed onto an empty scope: no second notice)", res.InterruptAcks)
	}
	if len(res.Rounds) != 2 || !res.Rounds[0].AllAckedNaturally || !res.Rounds[1].AllAckedNaturally || res.Rounds[1].Epoch != 1 {
		t.Fatalf("rounds = %+v, want the ask and B's opening (epoch 1) both acked naturally", res.Rounds)
	}
	if lw := res.LateWindow; lw.TeacherSentences != 0 || lw.SessionReady != 0 || lw.InterruptAcks != 0 || lw.Errors != 0 {
		t.Fatalf("late window after B's opening = %+v, want zero late-continuation output", lw)
	}
	if res.Playback.DroppedChunks != 0 || res.WSCloseCount != 0 || len(res.ErrorFrames) != 0 {
		t.Fatalf("dropped=%d close=%d errors=%+v, want 0/0/none", res.Playback.DroppedChunks, res.WSCloseCount, res.ErrorFrames)
	}
	if !f.waitManagersClosed(15*time.Second) || !waitFor(t, 15*time.Second, f.speechServerTasksSettled) {
		t.Fatal("the connection or its server tasks did not settle after the page left")
	}
	if state := speechTutorStateOf(m); state.lessonID != lessonIDForCategory("speech-browser-fixture", time.Now(), lesson.CategoryEnglish) {
		t.Fatalf("live classroom at the end = %q, want B's english row — the late continuation must not have installed over it", state.lessonID)
	}
}

// ---------------------------------------------------------------------------
// Case R — the same-row re-init without an in-flight save (the confirmed
// check-resume fix in a real browser): repeating session_init into the same
// checking row bumps the epoch at install, ships interrupt_ack BEFORE the
// second session_ready, and the resume bridge re-speaks under the
// POST-INSTALL epoch — the page adopts it and naturally ACKs.
// ---------------------------------------------------------------------------

func TestCheckResumeReinitEpochBrowser(t *testing.T) {
	f := newSpeechBrowserFixture(t)
	f.seedChecking()

	bridge := string(mustJSON(t, speechExpectedCheckResume))
	driver := f.runDriverAsync("reinit-same-row", 75*time.Second,
		"--expected-second-json", bridge, "--late-window-ms", "4000")

	m := f.tryFirstManager(10 * time.Second)
	if m == nil {
		f.reclaimAndFail(driver, nil, "no /rt connection was created before the deadline")
	}
	if !waitFor(t, 45*time.Second, func() bool { return f.announcementDone.Load() >= 1 }) {
		f.reclaimAndFail(driver, nil, "the first resume bridge never settled")
	}
	speechInjectSessionInit(t, f, m, "chinese")

	res, gotDriver := f.recvSpeechDriver(driver, speechReclaimBound)
	if !gotDriver || !res.OK {
		f.reclaimAndFail(driver, nil, "the same-row re-init journey failed: %s", res.Err)
	}
	if res.ReadyCount != 2 || res.CheckStartedCount != 2 {
		t.Fatalf("ready=%d check_started=%d, want 2/2 — the re-init reopens the panel", res.ReadyCount, res.CheckStartedCount)
	}
	if len(res.Rounds) != 2 || !res.Rounds[0].AllAckedNaturally || !res.Rounds[1].AllAckedNaturally {
		t.Fatalf("rounds = %+v, want both bridges acked naturally", res.Rounds)
	}
	if res.Rounds[1].Epoch != res.Rounds[0].Epoch+1 {
		t.Fatalf("bridge epochs = %d → %d, want the re-spoken bridge under the POST-install epoch",
			res.Rounds[0].Epoch, res.Rounds[1].Epoch)
	}
	if len(res.InterruptAcks) != 1 || res.InterruptAcks[0].OldEpoch != res.Rounds[0].Epoch || res.InterruptAcks[0].NewEpoch != res.Rounds[1].Epoch {
		t.Fatalf("interrupt_acks = %+v, want exactly the install's %d→%d notice",
			res.InterruptAcks, res.Rounds[0].Epoch, res.Rounds[1].Epoch)
	}
	if len(res.ReadyIndexes) < 2 || res.InterruptAcks[0].Index >= res.ReadyIndexes[1] || res.InterruptAcks[0].Index <= res.ReadyIndexes[0] {
		t.Fatalf("install ack index %d vs ready indexes %v — the epoch notice must precede the second session_ready",
			res.InterruptAcks[0].Index, res.ReadyIndexes)
	}
	if lw := res.LateWindow; lw.TeacherSentences != 0 || lw.SessionReady != 0 || lw.Errors != 0 {
		t.Fatalf("late window = %+v, want zero output after the re-init's bridge", lw)
	}
	if res.SessionEndedCount != 0 || res.WSCloseCount != 0 || len(res.ErrorFrames) != 0 {
		t.Fatalf("ended=%d close=%d errors=%+v, want 0/0/none — the reopened classroom stays live", res.SessionEndedCount, res.WSCloseCount, res.ErrorFrames)
	}
	if !f.waitManagersClosed(15*time.Second) || !waitFor(t, 15*time.Second, f.speechServerTasksSettled) {
		t.Fatal("the connection or its server tasks did not settle after the page left")
	}
	// The re-init itself wrote nothing: the row only left checking through
	// the page's departure (the ordinary unchecked close fallback — the
	// SWITCH never fell back; the late window above proves the session stayed
	// open and live until the browser left).
	lessonID := lessonIDForCategory("speech-browser-fixture", time.Now(), lesson.CategoryChinese)
	l, err := f.tools.GetLessonState(context.Background(), lessonID)
	if err != nil || l.Stage != lesson.StageSubmitted || !l.Unchecked {
		t.Fatalf("row after the page left = stage %q unchecked %v err=%v, want submitted/true through the departure fallback", l.Stage, l.Unchecked, err)
	}
	if subs := f.words.submissions(); len(subs) != 0 {
		t.Fatalf("FSRS saves = %d, want 0 — the no-outcome fallback rates nothing", len(subs))
	}
}

// ---------------------------------------------------------------------------
// Case L — the same-lessonID reentry while the row's accepted submit is in
// flight (the S4 minimal fix in a real browser): the re-init is refused with
// ONE recoverable error, nothing installs; A's save then completes naturally
// (result, naturally-acked summary, single session_ended and close).
// ---------------------------------------------------------------------------

func TestSameLessonReinitDuringSubmitBrowser(t *testing.T) {
	f := newSpeechBrowserFixture(t)
	f.armPoemPassageSaveChain()
	f.seedCheckingWithManualVerdicts()
	f.mu.Lock()
	f.words.reviewResult = ReviewWriteResult{
		Requested: []string{"听写浏览器", "第二词"},
		Updated:   []string{"听写浏览器", "第二词"},
	}
	f.mu.Unlock()
	before, _ := f.gateCheckSubmit(true, false)
	t.Cleanup(before.release)

	summary := string(mustJSON(t, speechExpectedSummaryAllCorrect))
	driver := f.runDriverAsync("reinit-during-submit", 60*time.Second,
		"--expected-second-json", summary)

	if !speechGateHit(before, 30*time.Second) {
		f.reclaimAndFail(driver, []func(){before.release}, "A's formal submit never reached the real latch write")
	}
	m := f.tryFirstManager(5 * time.Second)
	if m == nil {
		f.reclaimAndFail(driver, []func(){before.release}, "no /rt connection was created before the deadline")
	}
	scopeBefore := speechTutorStateOf(m)

	// The same-row reentry while the admission is live: the production entry
	// answers (refusal) synchronously — no classroom on the stale row state.
	speechInjectSessionInit(t, f, m, "chinese")

	// Resume A: its save completes on its OWN still-live classroom.
	before.release()
	res, gotDriver := f.recvSpeechDriver(driver, speechReclaimBound)
	if !gotDriver || !res.OK {
		f.reclaimAndFail(driver, nil, "the reentry journey failed: %s", res.Err)
	}
	speechWantRecoverableBusyError(t, res)
	if res.ReadyCount != 1 || res.CheckStartedCount != 1 {
		t.Fatalf("ready=%d check_started=%d, want 1/1 — the refused reentry installs nothing", res.ReadyCount, res.CheckStartedCount)
	}
	if len(res.Rounds) != 2 || !res.Rounds[0].AllAckedNaturally || !res.Rounds[1].AllAckedNaturally {
		t.Fatalf("rounds = %+v, want A's bridge and naturally-acked summary", res.Rounds)
	}
	if res.ReviewResultCount != 1 || res.SessionEndedCount != 1 || res.WSCloseCount != 1 || len(res.InterruptAcks) != 0 {
		t.Fatalf("review=%d ended=%d close=%d acks=%v, want 1/1/1/0 — A's natural ending after a single save",
			res.ReviewResultCount, res.SessionEndedCount, res.WSCloseCount, res.InterruptAcks)
	}
	if !f.waitManagersClosed(15*time.Second) || !waitFor(t, 15*time.Second, f.speechServerTasksSettled) {
		t.Fatal("the connection or its server tasks did not settle after the natural ending")
	}

	lessonID := lessonIDForCategory("speech-browser-fixture", time.Now(), lesson.CategoryChinese)
	l, err := f.tools.GetLessonState(context.Background(), lessonID)
	if err != nil {
		t.Fatalf("read stored lesson: %v", err)
	}
	if l.Stage != lesson.StageSubmitted || l.Unchecked {
		t.Fatalf("row = stage %q unchecked %v, want submitted/false — the refusal must not stall or degrade the save", l.Stage, l.Unchecked)
	}
	if subs := f.words.submissions(); len(subs) != 1 {
		t.Fatalf("FSRS saves = %d, want exactly one — the reentry must not duplicate or drop the save", len(subs))
	}
	receipts, err := f.receipts.ListByLesson(context.Background(), lessonID)
	if err != nil || len(receipts) != 1 || receipts[0].Status != lesson.ReceiptStatusSaved {
		t.Fatalf("receipts = %+v err=%v, want exactly one saved receipt", receipts, err)
	}
	tutor := speechTutorStateOf(m)
	if tutor.version != l.Version || tutor.checking || !tutor.reviewSee {
		t.Fatalf("writeback tutor = %d/%v/%v, want %d/false/true — the refusal keeps A's own successful writeback",
			tutor.version, tutor.checking, tutor.reviewSee, l.Version)
	}
	if scopeBefore.lessonID != tutor.lessonID {
		t.Fatalf("live classroom %q → %q — the refused reentry must not replace the scope", scopeBefore.lessonID, tutor.lessonID)
	}
}
