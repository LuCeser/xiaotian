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

