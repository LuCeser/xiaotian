package voice

import (
	"context"
	"errors"
	"testing"
)

// TestRuleClassifier maps the four core commands plus common child variants
// and English expressions to their actions, and confirms non-commands and
// empty transcripts classify as none.
func TestRuleClassifier(t *testing.T) {
	t.Run("table", func(t *testing.T) {
		cases := []struct {
			transcript string
			want       Action
		}{
			{"好了", ActionNextWord},
			{"写好了。", ActionNextWord},
			{"好 了 ！", ActionNextWord},
			{"老师我写完了", ActionNextWord},
			{"重读", ActionRepeat},
			{"再读一遍", ActionRepeat},
			{"再说一次好吗", ActionRepeat},
			{"暂停", ActionPause},
			{"等一下", ActionPause},
			{"停一下", ActionPause},
			{"继续", ActionResume},
			{"接着写", ActionResume},
			{"不会写", ActionCannotWrite},
			{"这个我不会", ActionCannotWrite},
			{"写不出来", ActionCannotWrite},
			{"done", ActionNextWord},
			{"repeat please", ActionRepeat},
			{"pause", ActionPause},
			{"I can't write it", ActionCannotWrite},
			// Non-commands.
			{"苹果", ActionNone},
			{"我在写字", ActionNone},
			{"", ActionNone},
			{"？？", ActionNone},
		}
		c := NewRuleClassifier()
		for _, tc := range cases {
			got, err := c.Classify(context.Background(), tc.transcript)
			if err != nil {
				t.Errorf("Classify(%q) unexpected error: %v", tc.transcript, err)
				continue
			}
			if got.Action != tc.want {
				t.Errorf("Classify(%q) action = %q, want %q", tc.transcript, got.Action, tc.want)
			}
			if got.Action != ActionNone && got.Source != "rule" {
				t.Errorf("Classify(%q) source = %q, want rule", tc.transcript, got.Source)
			}
		}
	})
}

// TestRuleClassifier_PriorityOrder documents the fixed-table priority: no
// keyword of a higher-priority table may be a substring collision that
// shadows the intended action for a lower-priority table's canonical input.
func TestRuleClassifier_PriorityOrder(t *testing.T) {
	canonical := []struct {
		transcript string
		want       Action
	}{
		{"好了", ActionNextWord},
		{"重读", ActionRepeat},
		{"暂停", ActionPause},
		{"不会写", ActionCannotWrite},
		{"继续", ActionResume},
	}
	c := NewRuleClassifier()
	for _, tc := range canonical {
		got, _ := c.Classify(context.Background(), tc.transcript)
		if got.Action != tc.want {
			t.Errorf("canonical %q classified %q, want %q", tc.transcript, got.Action, tc.want)
		}
	}
}

// TestControlVerb verifies the Action → FSM control-verb mapping, including
// cannot_write → next_word and none → no verb.
func TestControlVerb(t *testing.T) {
	tests := []struct {
		action Action
		verb   string
		ok     bool
	}{
		{ActionNextWord, "next_word", true},
		{ActionCannotWrite, "next_word", true},
		{ActionRepeat, "repeat", true},
		{ActionPause, "pause", true},
		{ActionResume, "resume", true},
		{ActionNone, "", false},
		{Action("bogus"), "", false},
	}
	for _, tc := range tests {
		verb, ok := tc.action.ControlVerb()
		if verb != tc.verb || ok != tc.ok {
			t.Errorf("%q.ControlVerb() = (%q, %v), want (%q, %v)", tc.action, verb, ok, tc.verb, tc.ok)
		}
	}
}

// TestLLMClassifier covers the LLM fallback: strict JSON reply, prose-wrapped
// JSON, unknown action → none, chatter error propagation, and nil seam.
func TestLLMClassifier(t *testing.T) {
	tests := []struct {
		name       string
		chatter    Chatter
		transcript string
		want       Action
		wantErr    bool
	}{
		{
			name: "strict json reply",
			chatter: func(context.Context, string, string) (string, error) {
				return `{"action":"repeat","confidence":0.9}`, nil
			},
			transcript: "麻烦再念一次那个词",
			want:       ActionRepeat,
		},
		{
			name: "prose-wrapped json reply",
			chatter: func(context.Context, string, string) (string, error) {
				return `好的，结果是 {"action":"pause","confidence":0.8} 请查收`, nil
			},
			transcript: "老师等我一下下",
			want:       ActionPause,
		},
		{
			name: "regex fallback when json malformed",
			chatter: func(context.Context, string, string) (string, error) {
				return `result: "action": "next_word" (done)`, nil
			},
			transcript: "写完了啊",
			want:       ActionNextWord,
		},
		{
			name:       "unknown action maps to none",
			chatter:    func(context.Context, string, string) (string, error) { return `{"action":"dance","confidence":1}`, nil },
			transcript: "跳个舞",
			want:       ActionNone,
		},
		{
			name:       "no action json at all",
			chatter:    func(context.Context, string, string) (string, error) { return `我不明白`, nil },
			transcript: "呃",
			want:       ActionNone,
			wantErr:    true,
		},
		{
			name:       "chatter error propagates",
			chatter:    func(context.Context, string, string) (string, error) { return "", errors.New("boom") },
			transcript: "好了",
			want:       ActionNone,
			wantErr:    true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := NewLLMClassifier(tc.chatter)
			got, err := c.Classify(context.Background(), tc.transcript)
			if tc.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Action != tc.want {
				t.Errorf("action = %q, want %q", got.Action, tc.want)
			}
			if got.Source != "llm" {
				t.Errorf("source = %q, want llm", got.Source)
			}
		})
	}

	t.Run("nil chatter", func(t *testing.T) {
		c := NewLLMClassifier(nil)
		got, err := c.Classify(context.Background(), "好了")
		if err == nil || !errors.Is(err, errNilChatter) {
			t.Fatalf("expected errNilChatter, got %v", err)
		}
		if got.Action != ActionNone {
			t.Errorf("action = %q, want none", got.Action)
		}
	})
}

// TestHybridClassifier verifies the escalation order: keyword match short-
// circuits (no LLM call), unmatched text goes to the LLM, and a nil LLM
// degrades to rules-only.
func TestHybridClassifier(t *testing.T) {
	ctx := context.Background()

	t.Run("keyword match short-circuits llm", func(t *testing.T) {
		llmCalled := false
		h := NewHybridClassifier(NewLLMClassifier(func(context.Context, string, string) (string, error) {
			llmCalled = true
			return `{"action":"pause"}`, nil
		}))
		got, err := h.Classify(ctx, "好了")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.Action != ActionNextWord || got.Source != "rule" {
			t.Errorf("got (%q,%s), want (next_word,rule)", got.Action, got.Source)
		}
		if llmCalled {
			t.Error("LLM was called for a keyword match — fast path broken")
		}
	})

	t.Run("unmatched text escalates to llm", func(t *testing.T) {
		h := NewHybridClassifier(NewLLMClassifier(func(_ context.Context, _, user string) (string, error) {
			if user != "老师我觉得这个词有点难" {
				t.Errorf("llm received %q", user)
			}
			return `{"action":"cannot_write","confidence":0.7}`, nil
		}))
		got, err := h.Classify(ctx, "老师我觉得这个词有点难")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.Action != ActionCannotWrite || got.Source != "llm" {
			t.Errorf("got (%q,%s), want (cannot_write,llm)", got.Action, got.Source)
		}
	})

	t.Run("nil llm degrades to rules-only", func(t *testing.T) {
		h := NewHybridClassifier(nil)
		got, err := h.Classify(ctx, "老师我觉得这个词有点难")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.Action != ActionNone {
			t.Errorf("action = %q, want none", got.Action)
		}
	})
}

// TestNormalizeTranscript checks punctuation/whitespace stripping and
// lowercasing.
func TestNormalizeTranscript(t *testing.T) {
	tests := []struct{ in, want string }{
		{"好了。", "好了"},
		{"好 了！", "好了"},
		{"Next, please.", "nextplease"},
		{"“继续”", "继续"},
		{"  spaces \t and\nnewlines  ", "spacesandnewlines"},
	}
	for _, tc := range tests {
		if got := NormalizeTranscript(tc.in); got != tc.want {
			t.Errorf("NormalizeTranscript(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
