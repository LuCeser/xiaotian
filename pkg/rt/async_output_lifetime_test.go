package rt

import (
	"context"
	"errors"
	"sync"
	"testing"

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
}
