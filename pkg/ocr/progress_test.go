package ocr

// progress_test.go — the teaching-progress interpretation pipeline (Issue
// #82): a parent's ONE sentence ("现在学到第三单元了") plus the registered
// textbook catalogs go to a text LLM; the pipeline verifies the model's
// answer against the AUTHORITATIVE lesson lists and resolves the learned
// count by list position (never lexicographic, CONTEXT.md 课次). The chat
// seam is the recording fake — no network.

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// progressBooks is the standing catalog: two books whose lesson orders are
// deliberately NOT dictionary order (CONTEXT.md: 先后由课次名单的书写顺
// 序决定), plus a same-name lesson across two books for the ambiguity
// slices.
func progressBooks() []ProgressTextbook {
	return []ProgressTextbook{
		{
			ID: 7, Name: "快乐英语", Category: "english",
			Edition: "人教版", Stage: "小学", Grade: "三年级", Volume: "上册",
			Lessons:        []string{"第一单元", "第二单元", "第三单元", "第四单元"},
			LearnedLessons: 0,
		},
		{
			ID: 9, Name: "阳光英语", Category: "english",
			Lessons: []string{"第三单元", "第五单元"},
		},
		{
			ID: 11, Name: "语文读本", Category: "chinese",
			Lessons: []string{"第1课", "第2课"},
		},
	}
}

// recordingChat answers once with reply and captures every prompt pair.
func recordingChat(reply string) (ChatReader, *[]string) {
	var seen []string
	return func(ctx context.Context, system, user string) (string, error) {
		seen = append(seen, system+"\x00"+user)
		return reply, nil
	}, &seen
}

func TestProgressInterpreterProposed(t *testing.T) {
	chat, seen := recordingChat(`{"status":"proposed","textbook_id":7,"lesson":"第三单元"}`)
	p := NewProgressInterpreter(chat)

	res, err := p.Interpret(context.Background(), "现在学到第三单元了", progressBooks())
	if err != nil {
		t.Fatalf("Interpret: %v", err)
	}
	if res.Status != ProgressProposed {
		t.Fatalf("status = %q, want proposed", res.Status)
	}
	if res.TextbookID != 7 || res.TextbookName != "快乐英语" {
		t.Fatalf("textbook = %d/%q, want 7/快乐英语", res.TextbookID, res.TextbookName)
	}
	// 第三单元 is list position 3 → 已学完第 3 课 (inclusive, by LIST order).
	if res.Lesson != "第三单元" || res.LearnedLessons != 3 {
		t.Fatalf("lesson/learned = %q/%d, want 第三单元/3", res.Lesson, res.LearnedLessons)
	}

	// The prompt carries the full catalog context: book ids, names,
	// metadata, IN-ORDER lesson lists and the parent's sentence verbatim.
	if len(*seen) != 1 {
		t.Fatalf("chat calls = %d, want 1", len(*seen))
	}
	sys, user, _ := strings.Cut((*seen)[0], "\x00")
	if !strings.Contains(user, "现在学到第三单元了") {
		t.Errorf("user prompt drops the parent's sentence: %q", user)
	}
	for _, want := range []string{"7", "快乐英语", "第一单元", "第四单元", "阳光英语", "语文读本"} {
		if !strings.Contains(user, want) {
			t.Errorf("user prompt missing %q:\n%s", want, user)
		}
	}
	// The in-order list must appear in list order (not dictionary order).
	if strings.Index(user, "第一单元") > strings.Index(user, "第四单元") {
		t.Errorf("lesson list is not rendered in teaching order:\n%s", user)
	}
	if !strings.Contains(sys, "教学进度") {
		t.Errorf("system prompt does not state the progress task:\n%s", sys)
	}
}

func TestProgressInterpreterNilSeam(t *testing.T) {
	p := NewProgressInterpreter(nil)
	if _, err := p.Interpret(context.Background(), "学到第三单元", progressBooks()); !errors.Is(err, errNilSeam) {
		t.Fatalf("err = %v, want errNilSeam", err)
	}
}

func TestProgressInterpreterAmbiguousCandidates(t *testing.T) {
	// 第三单元 sits in BOTH 快乐英语 (position 3) and 阳光英语 (position 1)
	// — the model must not guess the book.
	chat, _ := recordingChat(`{"status":"unclear","lesson":"第三单元","reason":"请问是哪一本教材？"}`)
	res, err := NewProgressInterpreter(chat).Interpret(context.Background(), "学到第三单元了", progressBooks())
	if err != nil {
		t.Fatalf("Interpret: %v", err)
	}
	if res.Status != ProgressUnclear {
		t.Fatalf("status = %q, want unclear", res.Status)
	}
	if res.Reason == "" {
		t.Error("unclear result carries no question")
	}
	if len(res.Candidates) != 2 {
		t.Fatalf("candidates = %+v, want 2", res.Candidates)
	}
	byID := map[int64]ProgressCandidate{}
	for _, c := range res.Candidates {
		byID[c.TextbookID] = c
	}
	if byID[7].LearnedLessons != 3 || byID[9].LearnedLessons != 1 {
		t.Fatalf("per-book learned counts = %+v, want 7→3 and 9→1 (each book's OWN list position)", res.Candidates)
	}
	if byID[7].TextbookName != "快乐英语" || byID[9].TextbookName != "阳光英语" {
		t.Fatalf("candidate names = %+v", res.Candidates)
	}
}

func TestProgressInterpreterWrongBookResolvesByLesson(t *testing.T) {
	// The model names a book that does NOT contain the lesson, but the
	// lesson itself (第二单元) exists in exactly one book — the catalog's
	// membership beats the model's misfired id.
	chat, _ := recordingChat(`{"status":"proposed","textbook_id":11,"lesson":"第二单元"}`)
	res, err := NewProgressInterpreter(chat).Interpret(context.Background(), "学到第二单元了", progressBooks())
	if err != nil {
		t.Fatalf("Interpret: %v", err)
	}
	if res.Status != ProgressProposed || res.TextbookID != 7 || res.LearnedLessons != 2 {
		t.Fatalf("res = %+v, want proposed book 7 learned 2", res)
	}
}

func TestProgressInterpreterLessonOutsideEveryList(t *testing.T) {
	// 不臆造课次: a lesson in no registered list is a QUESTION, never a
	// persisted guess.
	chat, _ := recordingChat(`{"status":"proposed","textbook_id":7,"lesson":"第七单元"}`)
	res, err := NewProgressInterpreter(chat).Interpret(context.Background(), "学到第七单元了", progressBooks())
	if err != nil {
		t.Fatalf("Interpret: %v", err)
	}
	if res.Status != ProgressUnclear || len(res.Candidates) != 0 {
		t.Fatalf("res = %+v, want unclear with no candidates", res)
	}
	if !strings.Contains(res.Reason, "第七单元") {
		t.Errorf("reason should name the unmatched lesson: %q", res.Reason)
	}
}

func TestProgressInterpreterNotStatement(t *testing.T) {
	chat, _ := recordingChat(`{"status":"not_progress","reason":"这句话是想开始一次默写"}`)
	res, err := NewProgressInterpreter(chat).Interpret(context.Background(), "开始默写吧", progressBooks())
	if err != nil {
		t.Fatalf("Interpret: %v", err)
	}
	if res.Status != ProgressNotStatement || res.Reason != "这句话是想开始一次默写" {
		t.Fatalf("res = %+v, want not_progress with the model's reason", res)
	}
}

func TestProgressInterpreterCrossCategoryExactMatch(t *testing.T) {
	// 第1课 belongs to the CHINESE book only; 第一单元 is a different
	// string — exact membership must never blur them.
	chat, _ := recordingChat(`{"status":"proposed","lesson":"第1课","textbook_id":11}`)
	res, err := NewProgressInterpreter(chat).Interpret(context.Background(), "语文学到第1课了", progressBooks())
	if err != nil {
		t.Fatalf("Interpret: %v", err)
	}
	if res.Status != ProgressProposed || res.TextbookID != 11 || res.Category != "chinese" || res.LearnedLessons != 1 {
		t.Fatalf("res = %+v, want proposed book 11 (chinese) learned 1", res)
	}
}

func TestProgressInterpreterParseLeniency(t *testing.T) {
	// A fenced reply with leading prose still yields the object.
	fenced := "好的，我理解为：\n```json\n{\"status\":\"proposed\",\"textbook_id\":7,\"lesson\":\"第三单元\"}\n```"
	chat := func(ctx context.Context, system, user string) (string, error) { return fenced, nil }
	res, err := NewProgressInterpreter(chat).Interpret(context.Background(), "学到第三单元", progressBooks())
	if err != nil {
		t.Fatalf("Interpret: %v", err)
	}
	if res.Status != ProgressProposed || res.TextbookID != 7 {
		t.Fatalf("res = %+v, want proposed 7", res)
	}
}

func TestProgressInterpreterErrorSentinels(t *testing.T) {
	transportErr := errors.New("connection reset")
	_, err := NewProgressInterpreter(func(ctx context.Context, s, u string) (string, error) {
		return "", transportErr
	}).Interpret(context.Background(), "学到第三单元", progressBooks())
	if !errors.Is(err, errReaderFailed) {
		t.Fatalf("transport err = %v, want errReaderFailed", err)
	}

	_, err = NewProgressInterpreter(func(ctx context.Context, s, u string) (string, error) {
		return "我没有理解这个问题。", nil
	}).Interpret(context.Background(), "学到第三单元", progressBooks())
	if !errors.Is(err, errUnparseableReply) {
		t.Fatalf("garbage err = %v, want errUnparseableReply", err)
	}
}

func TestProgressInterpreterEmptyMessageShortCircuits(t *testing.T) {
	called := false
	chat := func(ctx context.Context, s, u string) (string, error) {
		called = true
		return "{}", nil
	}
	res, err := NewProgressInterpreter(chat).Interpret(context.Background(), "   ", progressBooks())
	if err != nil {
		t.Fatalf("Interpret: %v", err)
	}
	if called {
		t.Error("an empty message must not cost an LLM round trip")
	}
	if res.Status != ProgressUnclear {
		t.Fatalf("status = %q, want unclear", res.Status)
	}
}
