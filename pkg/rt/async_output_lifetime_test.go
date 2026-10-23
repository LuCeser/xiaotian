package rt

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
	"github.com/xiaotian/home-tutor-agent/pkg/ocr"
)

// gatedOCRReader holds one OCR call open at its REAL return position: the
// vision read has been entered (entered closes) and only finishes when the
// test releases — a close in between is a genuine session end mid-call,
// never a pre-closed fixture.
type gatedOCRReader struct {
	inner   func(context.Context, string, string, []byte) (string, error)
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (g *gatedOCRReader) read(ctx context.Context, system, user string, jpeg []byte) (string, error) {
	g.once.Do(func() { close(g.entered) })
	<-g.release
	return g.inner(ctx, system, user, jpeg)
}

// ttBindGatedOCR wires a gated OCR reader into a manager already inside the
// check stage (ttBindPhoto's shape, reader-agnostic).
func ttBindGatedOCR(tm *TurnManager, reader *gatedOCRReader) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.cfg.OCR = reader.read
	tm.ocrPipeline = ocr.NewPipeline(ocr.Reader(reader.read), tm.cfg.PhotoConfidence)
}

// TestAsyncOutputAfterClose pins the step-21 rule at the REAL async return
// positions: whatever returns after the session ended — an OCR pass, a
// committed verdict write, a submit latch — presents nothing past the
// terminal session_ended, while every write that was already admitted
// still completes (the accepted-save semantics).
func TestAsyncOutputAfterClose(t *testing.T) {
	t.Run("ocr prefill returned after end is saved but not presented", func(t *testing.T) {
		cards := ttCards("苹果", "香蕉")
		tm, s, _, exec := ttDriveToCheck(t, cards, newFakeWords(cards))
		gated := &gatedOCRReader{
			inner: (&fakeOCRReader{replies: []string{
				`[{"text":"苹果","confidence":0.95},{"text":"香蕉","confidence":0.9}]`,
			}}).read,
			entered: make(chan struct{}), release: make(chan struct{}),
		}
		ttBindGatedOCR(tm, gated)
		tm.mu.Lock()
		versionBefore, verdictsBefore := tm.tutor.version, len(tm.tutor.checkVerdicts)
		tm.mu.Unlock()
		echoesBefore, entriesBefore := s.countOf(TypeCheckVerdicts), s.countOf(TypeOcrResult)

		done := make(chan struct{})
		go func() { tm.runCheckPhoto(s, fakeJPEG(2000)); close(done) }()
		ttAwaitGate(t, gated.entered, "the OCR call was not entered")
		tm.HandleControl(s, mustJSON(t, EndSessionMsg{Type: TypeEndSession}))
		close(gated.release)
		ttAwaitGate(t, done, "the OCR pass did not finish")

		// The accepted save survived the end: both prefills are on the row.
		exec.mu.Lock()
		stored := len(exec.lesson.Verdicts)
		exec.mu.Unlock()
		if stored != 2 {
			t.Fatalf("stored verdicts = %d, want 2 — the admitted upload's writes must complete", stored)
		}
		// Nothing was presented past the terminal message, and the dead
		// tutor was not mutated.
		if got := s.countOf(TypeCheckVerdicts); got != echoesBefore {
			t.Fatalf("check_verdicts echoes = %d, want %d (nothing past session_ended)", got, echoesBefore)
		}
		if got := s.countOf(TypeOcrResult); got != entriesBefore {
			t.Fatalf("ocr_result messages = %d, want %d", got, entriesBefore)
		}
		tm.mu.Lock()
		version, verdicts := tm.tutor.version, len(tm.tutor.checkVerdicts)
		tm.mu.Unlock()
		if version != versionBefore || verdicts != verdictsBefore {
			t.Fatalf("tutor version/verdicts = %d/%d, want %d/%d — the closed tutor must stay untouched",
				version, verdicts, versionBefore, verdictsBefore)
		}
	})

	t.Run("ocr failure returned after end is not announced", func(t *testing.T) {
		cards := ttCards("苹果")
		tm, s, _, _ := ttDriveToCheck(t, cards, newFakeWords(cards))
		gated := &gatedOCRReader{
			inner:   (&fakeOCRReader{err: errors.New("vision down")}).read,
			entered: make(chan struct{}), release: make(chan struct{}),
		}
		ttBindGatedOCR(tm, gated)
		errsBefore := s.countOf(TypeError)

		done := make(chan struct{})
		go func() { tm.runCheckPhoto(s, fakeJPEG(2000)); close(done) }()
		ttAwaitGate(t, gated.entered, "the OCR call was not entered")
		tm.HandleControl(s, mustJSON(t, EndSessionMsg{Type: TypeEndSession}))
		close(gated.release)
		ttAwaitGate(t, done, "the failed OCR pass did not finish")

		if got := s.countOf(TypeError); got != errsBefore {
			t.Fatalf("error frames = %d, want %d — no late failure notice past session_ended", got, errsBefore)
		}
	})

	t.Run("photo frame after the goodbye is not answered", func(t *testing.T) {
		cards := ttCards("苹果")
		tm, s, _, _ := ttDriveToCheck(t, cards, newFakeWords(cards))
		reader := &fakeOCRReader{}
		ttBindPhoto(tm, reader, 0)
		errsBefore := s.countOf(TypeError)

		tm.HandleControl(s, mustJSON(t, EndSessionMsg{Type: TypeEndSession}))
		ttPhotoChunk(t, tm, s, 1, fakeJPEG(100), true)

		if got := s.countOf(TypeError); got != errsBefore {
			t.Fatalf("error frames = %d, want %d — no reply past the goodbye", got, errsBefore)
		}
		if got := reader.imageCount(); got != 0 {
			t.Fatalf("OCR calls = %d, want 0", got)
		}
	})

	t.Run("manual verdict returned after end is saved but not presented", func(t *testing.T) {
		cards := ttCards("苹果")
		tm, s, _, exec := ttDriveToCheck(t, cards, newFakeWords(cards))
		entered, release := exec.gateApplyVerdict()
		tm.mu.Lock()
		versionBefore, verdictsBefore := tm.tutor.version, len(tm.tutor.checkVerdicts)
		tm.mu.Unlock()
		echoesBefore := s.countOf(TypeCheckVerdicts)

		done := make(chan struct{})
		go func() {
			tm.HandleControl(s, mustJSON(t, CheckVerdictMsg{
				Type: TypeCheckVerdict, RequestID: "req-manual-end",
				WordIndex: 0, Correct: true,
			}))
			close(done)
		}()
		ttAwaitGate(t, entered, "the verdict write was not entered")
		tm.HandleControl(s, mustJSON(t, EndSessionMsg{Type: TypeEndSession}))
		close(release)
		ttAwaitGate(t, done, "the verdict turn did not finish")

		// The accepted save survived the end: the manual verdict is on the row.
		exec.mu.Lock()
		stored := len(exec.lesson.Verdicts)
		manual := stored == 1 && exec.lesson.Verdicts[0].Manual
		exec.mu.Unlock()
		if !manual {
			t.Fatalf("stored verdicts = %d manual=%v, want 1 manual — the write must complete", stored, manual)
		}
		if got := s.countOf(TypeCheckVerdicts); got != echoesBefore {
			t.Fatalf("check_verdicts echoes = %d, want %d (nothing past session_ended)", got, echoesBefore)
		}
		tm.mu.Lock()
		version, verdicts := tm.tutor.version, len(tm.tutor.checkVerdicts)
		tm.mu.Unlock()
		if version != versionBefore || verdicts != verdictsBefore {
			t.Fatalf("tutor version/verdicts = %d/%d, want %d/%d — the closed tutor must stay untouched",
				version, verdicts, versionBefore, verdictsBefore)
		}
	})

	t.Run("conflict read-back returned after end is not echoed", func(t *testing.T) {
		cards := ttCards("苹果")
		tm, s, _, exec := ttDriveToCheck(t, cards, newFakeWords(cards))
		exec.mu.Lock()
		exec.conflictVersion = 1
		exec.mu.Unlock()
		entered, release := exec.gateGetState()
		tm.mu.Lock()
		versionBefore := tm.tutor.version
		tm.mu.Unlock()
		echoesBefore := s.countOf(TypeCheckVerdicts)

		done := make(chan struct{})
		go func() {
			tm.HandleControl(s, mustJSON(t, CheckVerdictMsg{
				Type: TypeCheckVerdict, RequestID: "req-conflict-end",
				WordIndex: 0, Correct: true,
			}))
			close(done)
		}()
		ttAwaitGate(t, entered, "the conflict read-back was not entered")
		tm.HandleControl(s, mustJSON(t, EndSessionMsg{Type: TypeEndSession}))
		close(release)
		ttAwaitGate(t, done, "the conflict turn did not finish")

		if got := s.countOf(TypeCheckVerdicts); got != echoesBefore {
			t.Fatalf("check_verdicts echoes = %d, want %d — no late echo past session_ended", got, echoesBefore)
		}
		tm.mu.Lock()
		version := tm.tutor.version
		tm.mu.Unlock()
		if version != versionBefore {
			t.Fatalf("tutor version = %d, want %d — the closed tutor must not align", version, versionBefore)
		}
	})

	t.Run("submit latch replay after end sends no second goodbye", func(t *testing.T) {
		cards := ttCards("苹果")
		words := newFakeWords(cards)
		tm, s, _, exec := ttDriveToCheck(t, cards, words)
		// The manual gate passes and the latch already fell elsewhere.
		tm.mu.Lock()
		tm.tutor.checkVerdicts = []lesson.CheckVerdict{{WordIndex: 0, Correct: true, Manual: true}}
		tm.mu.Unlock()
		exec.mu.Lock()
		exec.lesson.Stage = lesson.StageSubmitted
		exec.mu.Unlock()
		entered, release := exec.gateCompleteCheck()
		endsBefore := s.countOf(TypeSessionEnded)

		done := make(chan struct{})
		go func() {
			tm.HandleControl(s, mustJSON(t, CheckSubmitMsg{
				Type: TypeCheckSubmit, RequestID: "req-dup-end",
			}))
			close(done)
		}()
		ttAwaitGate(t, entered, "the latch write was not entered")
		tm.HandleControl(s, mustJSON(t, EndSessionMsg{Type: TypeEndSession}))
		close(release)
		ttAwaitGate(t, done, "the submit turn did not finish")

		if got := s.countOf(TypeSessionEnded); got != endsBefore+1 {
			t.Fatalf("session_ended messages = %d, want %d — exactly one goodbye", got, endsBefore+1)
		}
		if got := s.countOf(TypeReviewResult); got != 0 {
			t.Fatalf("review_result messages = %d, want 0", got)
		}
		if got := len(words.submissions()); got != 0 {
			t.Fatalf("SubmitReview calls = %d, want 0", got)
		}
	})

	t.Run("ping after end is not answered", func(t *testing.T) {
		cards := ttCards("苹果")
		tm, s, _, _ := ttDriveToCheck(t, cards, newFakeWords(cards))
		tm.HandleControl(s, mustJSON(t, PingMsg{Type: TypePing, TClientMs: 42}))
		if got := s.countOf(TypePong); got != 1 {
			t.Fatalf("pong before end = %d, want 1", got)
		}

		tm.HandleControl(s, mustJSON(t, EndSessionMsg{Type: TypeEndSession}))
		tm.HandleControl(s, mustJSON(t, PingMsg{Type: TypePing, TClientMs: 43}))

		if got := s.countOf(TypePong); got != 1 {
			t.Fatalf("pong messages = %d, want 1 — no reply past session_ended", got)
		}
	})
}
