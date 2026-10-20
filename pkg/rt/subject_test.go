// subject_test.go — Issue #55 subject-judgment regressions: the
// unspecified-subject ("auto") classroom is judged ONCE by the model from
// the two subjects' due content, and the judged subject then binds the
// round through every #10 seam (scoped fetch, per-subject row, session_ready
// echo). AC2: an explicit declaration never reaches the judgment — the
// user's word outranks the model's, with zero judgment calls. The
// degradation ladder (invalid reply → one retry → deny-by-default english
// with the coded error event) mirrors the 13-08 invalid-response
// discipline the classifier already pins.
package rt

import (
	"errors"
	"strings"
	"testing"

	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
)

// subjectScript builds one judgment ChatResponse carrying the strict
// {"category":...} JSON as its content — the judgment twin of intentScript.
func subjectScript(category string) llmScript {
	return llmScript{wire: `{"choices":[{"index":0,"message":{"role":"assistant","content":"{\"category\":\"` + category + `\"}"}}]}`}
}

// rawTextScript builds one ChatResponse whose content is arbitrary text
// (the invalid-reply rung of the degradation ladder).
func rawTextScript(text string) llmScript {
	return llmScript{wire: `{"choices":[{"index":0,"message":{"role":"assistant","content":"` + text + `"}}]}`}
}

// TestSessionInitAutoCategoryJudgedAndBound — the AC1 spine: a mixed
// vocabulary plus an auto declaration → one judgment call over BOTH
// subjects' peek snapshots → the judged subject binds the round exactly
// like an explicit declaration. Whatever the model judges, the fetch, the
// row id and the echo all ride the SAME single subject — the auto path can
// never produce a mixed round.
func TestSessionInitAutoCategoryJudgedAndBound(t *testing.T) {
	for _, tc := range []struct {
		name       string
		judged     string
		roundWords []string // the judged subject's fetch batch
		wantRowID  string
		wantPrompt []string // fragments the judgment's user turn must carry
	}{
		{
			name:       "model judges chinese over an english-only snapshot",
			judged:     lesson.CategoryChinese,
			roundWords: []string{"美丽", "世界"},
			wantRowID:  ttLessonID + "-chinese",
			// english empty (无), chinese carries content — both subjects'
			// snapshots ground the judgment.
			wantPrompt: []string{"英语学科当前到期词语：无", "美丽", "世界"},
		},
		{
			name:       "model judges english over a chinese-only snapshot",
			judged:     lesson.CategoryEnglish,
			roundWords: []string{"apple", "book"},
			wantRowID:  ttLessonID,
			wantPrompt: []string{"语文学科当前到期词语：无", "apple", "book"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			words := newFakeWords(ttCards(tc.roundWords...))
			words.setPeek(tc.judged, ttCards(tc.roundWords...))
			exec := newFakeExecutorNoRow(ttLessonID)
			llm := newFakeLLM(subjectScript(tc.judged))
			tm := NewTurnManager(newFakeASR(), llm, newFakeTTS(1, 320), exec, ttConfig(words), nil, nil)
			s := &captureSender{}
			tm.HandleControl(s, mustJSON(t, SessionInitMsg{
				Type:     TypeSessionInit,
				Audio:    AudioSpec{SampleRate: 16000, Format: "s16le", Channels: 1},
				Category: CategoryAuto,
			}))

			ready := s.controlsOf(t, TypeSessionReady)
			if len(ready) != 1 {
				t.Fatalf("session_ready count = %d, want 1", len(ready))
			}
			if got := ready[0]["category"]; got != tc.judged {
				t.Errorf("session_ready.category = %v, want the judged %q (never %q)", got, tc.judged, CategoryAuto)
			}
			if ids := exec.ensureIDCalls(); len(ids) != 1 || ids[0] != tc.wantRowID {
				t.Errorf("EnsureLessonCards ids = %v, want [%s]", ids, tc.wantRowID)
			}
			if cats := words.fetchedCategories(); len(cats) != 1 || cats[0] != tc.judged {
				t.Errorf("FetchDueWords categories = %v, want exactly one fetch under %q", cats, tc.judged)
			}
			peeks := words.peekedCategories()
			if len(peeks) != 2 {
				t.Errorf("PeekDueWords categories = %v, want both subjects snapshotted", peeks)
			}
			if llm.callCount() != 1 {
				t.Errorf("judgment Chat calls = %d, want 1 (a valid reply needs no retry)", llm.callCount())
			}
			user := llm.calls[0].messages[1].Content
			for _, frag := range tc.wantPrompt {
				if !strings.Contains(user, frag) {
					t.Errorf("judgment user turn %q missing %q (content-grounded input)", user, frag)
				}
			}
		})
	}
}

// TestSessionInitAutoCategoryRetryThenFallback — the degradation ladder:
// an invalid first reply gets exactly ONE correction retry (a repaired
// reply wins); a still-invalid second reply — or a hard provider error —
// lands deny-by-default english with the coded error event, and the
// classroom still opens under english (never stalled, never auto).
func TestSessionInitAutoCategoryRetryThenFallback(t *testing.T) {
	for _, tc := range []struct {
		name   string
		script []llmScript
	}{
		{"invalid then repaired", []llmScript{rawTextScript("我觉得语文比较好"), subjectScript(lesson.CategoryChinese)}},
		{"invalid twice falls back", []llmScript{rawTextScript("语文"), rawTextScript("sure")}},
		{"off-enum twice falls back", []llmScript{subjectScript("france"), subjectScript("math")}},
		{"hard provider error falls back", []llmScript{{err: errSubjectJudgeBoom}, {err: errSubjectJudgeBoom}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			words := newFakeWords(ttCards("apple", "book"))
			exec := newFakeExecutorNoRow(ttLessonID)
			log := &captureLogger{}
			llm := newFakeLLM(tc.script...)
			tm := NewTurnManager(newFakeASR(), llm, newFakeTTS(1, 320), exec, ttConfig(words), NewEventRecorder(log, "sj"), nil)
			s := &captureSender{}
			tm.HandleControl(s, mustJSON(t, SessionInitMsg{
				Type:     TypeSessionInit,
				Audio:    AudioSpec{SampleRate: 16000, Format: "s16le", Channels: 1},
				Category: CategoryAuto,
			}))

			if llm.callCount() != 2 {
				t.Fatalf("judgment Chat calls = %d, want 2 (one correction retry, never more)", llm.callCount())
			}
			// The retry nudge rides the SECOND call's user turn.
			last := llm.calls[1].messages
			if len(last) == 0 || last[len(last)-1].Content != subjectJudgeRetryNudge {
				t.Errorf("retry messages tail = %v, want the fixed correction nudge", last)
			}

			repaired := tc.name == "invalid then repaired"
			wantCategory := lesson.CategoryEnglish
			wantRow := ttLessonID
			if repaired {
				wantCategory = lesson.CategoryChinese
				wantRow = ttLessonID + "-chinese"
			}
			ready := s.controlsOf(t, TypeSessionReady)
			if len(ready) != 1 {
				t.Fatalf("session_ready count = %d, want 1 (the classroom opens whatever the judgment did)", len(ready))
			}
			if got := ready[0]["category"]; got != wantCategory {
				t.Errorf("session_ready.category = %v, want %q", got, wantCategory)
			}
			if ids := exec.ensureIDCalls(); len(ids) != 1 || ids[0] != wantRow {
				t.Errorf("EnsureLessonCards ids = %v, want [%s]", ids, wantRow)
			}
			if cats := words.fetchedCategories(); len(cats) != 1 || cats[0] != wantCategory {
				t.Errorf("FetchDueWords categories = %v, want [%s]", cats, wantCategory)
			}

			if !repaired {
				var sawFallback bool
				for _, e := range log.snapshot() {
					if e.msg != "rt_event" {
						continue
					}
					if fieldValue(t, e, "event") == EventError && fieldValue(t, e, "code") == ErrCodeProviderFailed {
						sawFallback = true
					}
				}
				if !sawFallback {
					t.Error("no error event for the exhausted judgment (want code=provider_failed, attempts=2)")
				}
			}
		})
	}
}

// TestSessionInitExplicitCategorySkipsJudgment — the AC2 spine: a declared
// english/chinese NEVER reaches the model — zero judgment calls, zero
// peeks — and the declared subject binds the round even when the due
// content would point the other way (the model never gets the chance to
// disagree, so it can never silently overrule the user).
func TestSessionInitExplicitCategorySkipsJudgment(t *testing.T) {
	for _, tc := range []struct {
		name      string
		declared  string
		peekCat   string // the subject whose content WOULD judge the other way
		wantRowID string
	}{
		{"declared english over chinese-only content", lesson.CategoryEnglish, lesson.CategoryChinese, ttLessonID},
		{"declared chinese over english-only content", lesson.CategoryChinese, lesson.CategoryEnglish, ttLessonID + "-chinese"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			words := newFakeWords(ttCards("美丽", "世界"))
			words.setPeek(tc.peekCat, ttCards("美丽", "世界"))
			exec := newFakeExecutorNoRow(ttLessonID)
			// No scripted replies: ANY Chat call errors ("script exhausted"),
			// so a spurious judgment would fail loudly via the fallback path
			// — the zero-call assertion below is the precise pin.
			llm := newFakeLLM()
			tm := NewTurnManager(newFakeASR(), llm, newFakeTTS(1, 320), exec, ttConfig(words), nil, nil)
			s := &captureSender{}
			tm.HandleControl(s, mustJSON(t, SessionInitMsg{
				Type:     TypeSessionInit,
				Audio:    AudioSpec{SampleRate: 16000, Format: "s16le", Channels: 1},
				Category: tc.declared,
			}))

			ready := s.controlsOf(t, TypeSessionReady)
			if len(ready) != 1 {
				t.Fatalf("session_ready count = %d, want 1", len(ready))
			}
			if got := ready[0]["category"]; got != tc.declared {
				t.Errorf("session_ready.category = %v, want the declared %q (never re-judged)", got, tc.declared)
			}
			if llm.callCount() != 0 {
				t.Errorf("judgment Chat calls = %d, want 0 (an explicit declaration is final)", llm.callCount())
			}
			if peeks := words.peekedCategories(); len(peeks) != 0 {
				t.Errorf("PeekDueWords categories = %v, want none (no judgment, no content read)", peeks)
			}
			if cats := words.fetchedCategories(); len(cats) != 1 || cats[0] != tc.declared {
				t.Errorf("FetchDueWords categories = %v, want [%s]", cats, tc.declared)
			}
			if ids := exec.ensureIDCalls(); len(ids) != 1 || ids[0] != tc.wantRowID {
				t.Errorf("EnsureLessonCards ids = %v, want [%s]", ids, tc.wantRowID)
			}
		})
	}
}

// TestParseSubjectJSON — the judgment reply's strict-JSON whitelist: bare
// and fenced blocks parse, garbage and off-enum values do not.
func TestParseSubjectJSON(t *testing.T) {
	for _, tc := range []struct {
		text   string
		want   string
		wantOK bool
	}{
		{`{"category":"chinese"}`, lesson.CategoryChinese, true},
		{`{"category":"english"}`, lesson.CategoryEnglish, true},
		{"前置说明```json\n{\"category\":\"chinese\"}\n```尾随", lesson.CategoryChinese, true},
		{`{"category":"france"}`, "", false},
		{`{"subject":"chinese"}`, "", false},
		{"我觉得语文比较好", "", false},
		{"", "", false},
	} {
		got, ok := parseSubjectJSON(tc.text)
		if ok != tc.wantOK || got != tc.want {
			t.Errorf("parseSubjectJSON(%q) = (%q,%v), want (%q,%v)", tc.text, got, ok, tc.want, tc.wantOK)
		}
	}
}

// errSubjectJudgeBoom is the judgment tests' provider-error sentinel.
var errSubjectJudgeBoom = errors.New("judgment model unavailable")
