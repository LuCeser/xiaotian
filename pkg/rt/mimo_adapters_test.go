// PyFSRS adapter tests (13-10). The adapter is exercised against a manual
// fake pybridge.Executor (D-48, stdlib-only convention — same style as
// pybridge/bridge_test.go and providers_fake_test.go): no python subprocess
// is ever spawned. The five named tests anchor the contract 13-08's FSM
// relies on:
//   - RunDue always carries Limit (default 10) AND FetchLockHours=1
//   - RunDue errors propagate (→ lesson_unavailable upstream)
//   - empty results skip RunReview entirely (no python spawn)
//   - partial / zero Updated with nil error surface as HONEST sets
//     (no blind batch retry — exactly one RunReview per submission)
package rt

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
	"github.com/xiaotian/home-tutor-agent/pkg/pybridge"
)

// fakePyBridge is a scripted pybridge.Executor: RunDue pops the next
// scripted result (or error) and records the opts it was called with;
// RunReview records its opts and returns the scripted pair. The remaining
// Executor methods are never touched by the adapter and panic on use.
type fakePyBridge struct {
	reviewErr  error
	dueResults []pybridge.DueResult
	dueErrs    []error

	dueCalls    []pybridge.DueOpts
	reviewCalls []pybridge.ReviewOpts

	nextReview pybridge.ReviewResult
}

func (f *fakePyBridge) RunDue(_ context.Context, opts pybridge.DueOpts) (pybridge.DueResult, error) {
	f.dueCalls = append(f.dueCalls, opts)
	if len(f.dueErrs) > 0 {
		err := f.dueErrs[0]
		f.dueErrs = f.dueErrs[1:]
		return pybridge.DueResult{}, err
	}
	if len(f.dueResults) == 0 {
		return pybridge.DueResult{}, nil
	}
	res := f.dueResults[0]
	f.dueResults = f.dueResults[1:]
	return res, nil
}

func (f *fakePyBridge) RunReview(_ context.Context, opts pybridge.ReviewOpts) (pybridge.ReviewResult, error) {
	f.reviewCalls = append(f.reviewCalls, opts)
	return f.nextReview, f.reviewErr
}

func (f *fakePyBridge) RunPregenerate(context.Context, pybridge.PregenOpts) (pybridge.PregenResult, error) {
	panic("PyFSRS must never call RunPregenerate")
}
func (f *fakePyBridge) RunUnlock(context.Context, pybridge.UnlockOpts) (pybridge.UnlockResult, error) {
	panic("PyFSRS must never call RunUnlock")
}
func (f *fakePyBridge) RunAdd(context.Context, pybridge.AddOpts) (pybridge.AddResult, error) {
	panic("PyFSRS must never call RunAdd")
}
func (f *fakePyBridge) RunEdit(context.Context, pybridge.EditOpts) (pybridge.EditResult, error) {
	panic("PyFSRS must never call RunEdit")
}
func (f *fakePyBridge) RunSetParticipation(context.Context, pybridge.SetParticipationOpts) (pybridge.SetParticipationResult, error) {
	panic("PyFSRS must never call RunSetParticipation")
}
func (f *fakePyBridge) RunRenameUnit(context.Context, pybridge.RenameUnitOpts) (pybridge.RenameUnitResult, error) {
	panic("PyFSRS must never call RunRenameUnit")
}
func (f *fakePyBridge) RunDeleteWords(context.Context, pybridge.DeleteWordsOpts) (pybridge.DeleteWordsResult, error) {
	panic("PyFSRS must never call RunDeleteWords")
}

func (f *fakePyBridge) RunTextbookCreate(context.Context, pybridge.TextbookCreateOpts) (pybridge.TextbookMutationResult, error) {
	panic("PyFSRS must never call RunTextbookCreate")
}

func (f *fakePyBridge) RunTextbookUpdate(context.Context, pybridge.TextbookUpdateOpts) (pybridge.TextbookMutationResult, error) {
	panic("PyFSRS must never call RunTextbookUpdate")
}

func (f *fakePyBridge) RunAssignWords(context.Context, pybridge.AssignWordsOpts) (pybridge.AssignWordsResult, error) {
	panic("PyFSRS must never call RunAssignWords")
}

func (f *fakePyBridge) RunUnassignWords(context.Context, pybridge.UnassignWordsOpts) (pybridge.UnassignWordsResult, error) {
	panic("PyFSRS must never call RunUnassignWords")
}

func (f *fakePyBridge) RunDesignate(context.Context, pybridge.DesignateOpts) (pybridge.DesignateResult, error) {
	panic("PyFSRS must never call RunDesignate")
}

func (f *fakePyBridge) RunCreateDesignation(context.Context, pybridge.CreateDesignationOpts) (pybridge.DesignateResult, error) {
	panic("PyFSRS must never call RunCreateDesignation")
}

// TestPyFSRSNilExecRejected pins the D-47 nil-guard: constructing without a
// pybridge executor is an error, never a runtime panic later.
func TestPyFSRSNilExecRejected(t *testing.T) {
	if _, err := NewPyFSRS(nil, infra.NewNoOpLogger()); err == nil {
		t.Fatal("NewPyFSRS(nil, ...) = nil error, want rejection")
	}
	if _, err := NewPyFSRS(nil, nil); err == nil {
		t.Fatal("NewPyFSRS(nil, nil) = nil error, want rejection")
	}
}

// TestPyFSRSFetchDueMapsCards proves the DueCard → lesson.Word lossless
// field mapping (word/meaning/example, order preserved) AND that RunDue is
// invoked with the explicit operational contract: Limit as requested (0 →
// the FSM cap 10) and FetchLockHours=1 (T-13-20: never the script's 24h
// default, never a peek).
func TestPyFSRSFetchDueMapsCards(t *testing.T) {
	fake := &fakePyBridge{
		dueResults: []pybridge.DueResult{{Cards: []pybridge.DueCard{
			{ID: 1, Word: "苹果", Meaning: "apple", Example: "I eat an apple.", Unit: "u1"},
			{ID: 2, Word: "香蕉", Meaning: "banana", Example: "Bananas are yellow.", Unit: "u1"},
		}}},
	}
	ad, err := NewPyFSRS(fake, infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("NewPyFSRS: %v", err)
	}
	words, err := ad.FetchDueWords(context.Background(), 0, "")
	if err != nil {
		t.Fatalf("FetchDueWords: %v", err)
	}
	if len(words) != 2 {
		t.Fatalf("words length = %d, want 2", len(words))
	}
	want := []lessonWordCheck{
		{Word: "苹果", Meaning: "apple", Example: "I eat an apple."},
		{Word: "香蕉", Meaning: "banana", Example: "Bananas are yellow."},
	}
	for i, w := range want {
		if words[i].Word != w.Word || words[i].Meaning != w.Meaning || words[i].Example != w.Example {
			t.Errorf("words[%d] = %+v, want %+v (lossless DueCard mapping)", i, words[i], w)
		}
	}
	if len(fake.dueCalls) != 1 {
		t.Fatalf("RunDue calls = %d, want 1", len(fake.dueCalls))
	}
	opts := fake.dueCalls[0]
	if opts.Limit != dueWordsLimit {
		t.Errorf("RunDue opts.Limit = %d, want %d (0 → FSM cap)", opts.Limit, dueWordsLimit)
	}
	if opts.FetchLockHours != 1 {
		t.Errorf("RunDue opts.FetchLockHours = %v, want 1 (explicit 1h window, not the script default)", opts.FetchLockHours)
	}
	if opts.Peek {
		t.Error("RunDue opts.Peek = true, want false (fetch must lock, never peek)")
	}

	// A positive limit passes through unchanged with the same lock contract.
	fake2 := &fakePyBridge{}
	ad2, _ := NewPyFSRS(fake2, infra.NewNoOpLogger())
	if _, err := ad2.FetchDueWords(context.Background(), 5, ""); err != nil {
		t.Fatalf("FetchDueWords(5): %v", err)
	}
	if got := fake2.dueCalls[0].Limit; got != 5 {
		t.Errorf("RunDue opts.Limit = %d, want 5 (positive limit passes through)", got)
	}
	if got := fake2.dueCalls[0].FetchLockHours; got != 1 {
		t.Errorf("RunDue opts.FetchLockHours = %v, want 1", got)
	}
}

// lessonWordCheck keeps the mapping assertion table above readable without
// importing the lesson struct literally in the want literals.
type lessonWordCheck struct{ Word, Meaning, Example string }

// TestPyFSRSPeekDueMapsCardsLockFree pins the Issue #55 peek contract: the
// subject resolution's presence probe carries Peek=true and
// FetchLockHours=0 (the script's lock-free path — reading must never lock
// cards away from a live classroom), keeps the same lossless card mapping,
// and normalizes the category exactly like the fetch.
func TestPyFSRSPeekDueMapsCardsLockFree(t *testing.T) {
	fake := &fakePyBridge{
		dueResults: []pybridge.DueResult{{Cards: []pybridge.DueCard{
			{ID: 1, Word: "美丽", Meaning: "beautiful", Example: "美丽的花。", Unit: "u1"},
		}}},
	}
	ad, err := NewPyFSRS(fake, infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("NewPyFSRS: %v", err)
	}
	words, err := ad.PeekDueWords(context.Background(), 0, lesson.CategoryChinese)
	if err != nil {
		t.Fatalf("PeekDueWords: %v", err)
	}
	if len(words) != 1 || words[0].Word != "美丽" || words[0].Meaning != "beautiful" {
		t.Fatalf("words = %+v, want the lossless 美丽 mapping", words)
	}
	if len(fake.dueCalls) != 1 {
		t.Fatalf("RunDue calls = %d, want 1", len(fake.dueCalls))
	}
	opts := fake.dueCalls[0]
	if !opts.Peek {
		t.Error("RunDue opts.Peek = false, want true (the judgment's read never locks)")
	}
	if opts.FetchLockHours != 0 {
		t.Errorf("RunDue opts.FetchLockHours = %v, want 0 (peek must not take the fetch lock)", opts.FetchLockHours)
	}
	if opts.Category != lesson.CategoryChinese {
		t.Errorf("RunDue opts.Category = %q, want chinese", opts.Category)
	}
	if opts.Limit != dueWordsLimit {
		t.Errorf("RunDue opts.Limit = %d, want %d (0 → FSM cap)", opts.Limit, dueWordsLimit)
	}

	// A peek failure propagates like a fetch failure — the caller degrades
	// the judgment input honestly instead of mistaking it for "no content".
	fake2 := &fakePyBridge{dueErrs: []error{errors.New("python exit")}}
	ad2, _ := NewPyFSRS(fake2, infra.NewNoOpLogger())
	if _, err := ad2.PeekDueWords(context.Background(), 5, ""); err == nil {
		t.Fatal("PeekDueWords = nil error, want the python failure propagated")
	}
}

// TestPyFSRSFetchDueErrorPropagates proves a python-side failure (timeout,
// exit error, parse failure — whatever pybridge classifies) surfaces from
// the adapter instead of degrading into an empty word list: the FSM turns
// it into the visible lesson_unavailable path, so swallowing it here would
// fabricate a "no due words" ending.
func TestPyFSRSFetchDueErrorPropagates(t *testing.T) {
	sentinel := errors.New("python subprocess timed out")
	fake := &fakePyBridge{dueErrs: []error{sentinel}}
	ad, _ := NewPyFSRS(fake, infra.NewNoOpLogger())
	words, err := ad.FetchDueWords(context.Background(), 10, "")
	if err == nil {
		t.Fatal("FetchDueWords = nil error, want the python failure propagated")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("FetchDueWords error = %v, want errors.Is(err, sentinel)=true", err)
	}
	if words != nil {
		t.Errorf("FetchDueWords words = %+v, want nil on error", words)
	}
}

// TestPyFSRSSubmitReviewPassesResultsAndSkipsEmpty proves both halves of
// the submission contract: an empty map is a zero-value no-op with NO
// RunReview call (never spawn python for nothing), while a non-empty map
// reaches RunReview verbatim exactly once and the returned Requested set
// mirrors the submitted keys.
func TestPyFSRSSubmitReviewPassesResultsAndSkipsEmpty(t *testing.T) {
	fake := &fakePyBridge{
		nextReview: pybridge.ReviewResult{Updated: []pybridge.UpdatedCard{
			{Word: "苹果", Rating: "good"},
		}},
	}
	ad, _ := NewPyFSRS(fake, infra.NewNoOpLogger())

	// Empty map: no python spawn, zero result, nil error.
	res, err := ad.SubmitReview(context.Background(), map[string]string{}, "")
	if err != nil {
		t.Fatalf("SubmitReview(empty) = %v, want nil", err)
	}
	if res.Requested != nil || res.Updated != nil || res.Missing != nil {
		t.Errorf("SubmitReview(empty) = %+v, want zero ReviewWriteResult", res)
	}
	if len(fake.reviewCalls) != 0 {
		t.Fatalf("RunReview calls after empty map = %d, want 0 (never spawn python)", len(fake.reviewCalls))
	}

	// Non-empty map: forwarded verbatim, exactly one call.
	res, err = ad.SubmitReview(context.Background(), map[string]string{"苹果": "good", "香蕉": "hard"}, "")
	if err != nil {
		t.Fatalf("SubmitReview = %v, want nil", err)
	}
	if len(fake.reviewCalls) != 1 {
		t.Fatalf("RunReview calls = %d, want exactly 1", len(fake.reviewCalls))
	}
	if !reflect.DeepEqual(fake.reviewCalls[0].Results, map[string]string{"苹果": "good", "香蕉": "hard"}) {
		t.Errorf("RunReview opts.Results = %+v, want the submitted map verbatim", fake.reviewCalls[0].Results)
	}
	if !reflect.DeepEqual(res.Requested, []string{"苹果", "香蕉"}) {
		t.Errorf("Requested = %+v, want [苹果 香蕉] (sorted submitted keys)", res.Requested)
	}
}

// TestPyFSRSSubmitReviewReportsPartialUpdated proves the honest partial
// outcome: the script updated one of two requested words with a nil error —
// the adapter returns Updated=[苹果], Missing=[香蕉] and does NOT retry
// (exactly one RunReview call). The 13-08 completion path narrates "本次保存
// 1/2 个" from these sets.
func TestPyFSRSSubmitReviewReportsPartialUpdated(t *testing.T) {
	fake := &fakePyBridge{
		nextReview: pybridge.ReviewResult{Updated: []pybridge.UpdatedCard{
			{Word: "苹果", Rating: "good", NextDue: "2026-10-07"},
		}},
	}
	ad, _ := NewPyFSRS(fake, infra.NewNoOpLogger())
	res, err := ad.SubmitReview(context.Background(), map[string]string{"苹果": "good", "香蕉": "hard"}, "")
	if err != nil {
		t.Fatalf("SubmitReview = %v, want nil (partial update is not an error)", err)
	}
	if len(fake.reviewCalls) != 1 {
		t.Errorf("RunReview calls = %d, want exactly 1 (no blind batch retry)", len(fake.reviewCalls))
	}
	if !reflect.DeepEqual(res.Requested, []string{"苹果", "香蕉"}) {
		t.Errorf("Requested = %+v, want both submitted words", res.Requested)
	}
	if !reflect.DeepEqual(res.Updated, []string{"苹果"}) {
		t.Errorf("Updated = %+v, want [苹果] only", res.Updated)
	}
	if !reflect.DeepEqual(res.Missing, []string{"香蕉"}) {
		t.Errorf("Missing = %+v, want [香蕉]", res.Missing)
	}
}

// TestPyFSRSSubmitReviewReportsZeroUpdated proves the honest zero outcome:
// the script reports an empty updated array with a nil error (e.g. every
// word was already reviewed today) — the adapter returns all words as
// Missing, no error, one RunReview call. A nil error must never masquerade
// as full success.
func TestPyFSRSSubmitReviewReportsZeroUpdated(t *testing.T) {
	fake := &fakePyBridge{nextReview: pybridge.ReviewResult{Updated: nil}}
	ad, _ := NewPyFSRS(fake, infra.NewNoOpLogger())
	res, err := ad.SubmitReview(context.Background(), map[string]string{"苹果": "good", "香蕉": "hard"}, "")
	if err != nil {
		t.Fatalf("SubmitReview = %v, want nil (zero updates with nil error is a legal outcome)", err)
	}
	if len(fake.reviewCalls) != 1 {
		t.Errorf("RunReview calls = %d, want exactly 1 (no blind batch retry)", len(fake.reviewCalls))
	}
	if len(res.Updated) != 0 {
		t.Errorf("Updated = %+v, want empty", res.Updated)
	}
	if !reflect.DeepEqual(res.Missing, []string{"苹果", "香蕉"}) {
		t.Errorf("Missing = %+v, want every requested word (nothing was saved)", res.Missing)
	}
}
