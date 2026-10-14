package ocr

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
)

// fakeChatReader captures the seam inputs and replies with a canned string
// (the text-LLM kin of fakeListReader).
type fakeChatReader struct {
	reply     string
	err       error
	gotSystem string
	gotUser   string
	calls     int
}

func (f *fakeChatReader) chat(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	f.calls++
	f.gotSystem = systemPrompt
	f.gotUser = userPrompt
	if f.err != nil {
		return "", f.err
	}
	return f.reply, nil
}

func TestMeaningGenerator_Generate(t *testing.T) {
	tests := []struct {
		name        string
		words       []string
		reply       string
		chatErr     error
		wantErr     error
		wantMeaning map[string]string
		wantMissing []string
	}{
		{
			name:        "array reply answers every requested word",
			words:       []string{"apple", "ice cream"},
			reply:       `[{"word":"apple","meaning":"n. 苹果"},{"word":"ice cream","meaning":"冰激凌"}]`,
			wantMeaning: map[string]string{"apple": "n. 苹果", "ice cream": "冰激凌"},
			wantMissing: []string{},
		},
		{
			name:        "unanswered word lands in missing, not an error",
			words:       []string{"apple", "banana"},
			reply:       `[{"word":"apple","meaning":"n. 苹果"},{"word":"banana","meaning":""}]`,
			wantMeaning: map[string]string{"apple": "n. 苹果"},
			wantMissing: []string{"banana"},
		},
		{
			name:        "reply filtered to the requested set — extra words ignored",
			words:       []string{"apple"},
			reply:       `[{"word":"apple","meaning":"n. 苹果"},{"word":"cherry","meaning":"樱桃"}]`,
			wantMeaning: map[string]string{"apple": "n. 苹果"},
			wantMissing: []string{},
		},
		{
			name:        "meanings map wrapped in a meanings object parses",
			words:       []string{"apple"},
			reply:       `{"meanings":{"apple":"n. 苹果"}}`,
			wantMeaning: map[string]string{"apple": "n. 苹果"},
			wantMissing: []string{},
		},
		{
			name:        "bare word→meaning map parses",
			words:       []string{"apple", "banana"},
			reply:       "前缀废话\n{\"apple\":\"n. 苹果\",\"banana\":\"香蕉\"}\n后缀废话",
			wantMeaning: map[string]string{"apple": "n. 苹果", "banana": "香蕉"},
			wantMissing: []string{},
		},
		{
			name:        "fenced array with prose parses (thinking-model shape)",
			words:       []string{"apple"},
			reply:       "好的，以下是补充释义：\n```json\n[{\"word\":\"apple\",\"meaning\":\"n. 苹果\"}]\n```\n完成。",
			wantMeaning: map[string]string{"apple": "n. 苹果"},
			wantMissing: []string{},
		},
		{
			name:        "regex fallback recovers paired fields from broken JSON",
			words:       []string{"apple"},
			reply:       `[{"word":"apple","meaning":"n. 苹果",}]`,
			wantMeaning: map[string]string{"apple": "n. 苹果"},
			wantMissing: []string{},
		},
		{
			name:        "over-long gloss is missing, never truncated",
			words:       []string{"apple"},
			reply:       `[{"word":"apple","meaning":"` + strings.Repeat("长", maxGeneratedMeaningRunes+1) + `"}]`,
			wantMeaning: map[string]string{},
			wantMissing: []string{"apple"},
		},
		{
			name:        "whitespace-only gloss is missing",
			words:       []string{"apple"},
			reply:       `[{"word":"apple","meaning":"   "}]`,
			wantMeaning: map[string]string{},
			wantMissing: []string{"apple"},
		},
		{
			name:    "seam failure wraps the sentinel",
			words:   []string{"apple"},
			chatErr: errors.New("upstream 500"),
			wantErr: errReaderFailed,
		},
		{
			name:    "unparseable reply is an error (retryable 502)",
			words:   []string{"apple"},
			reply:   "抱歉，我无法完成这个任务。",
			wantErr: errUnparseableReply,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeChatReader{reply: tt.reply, err: tt.chatErr}
			gen := NewMeaningGenerator(fake.chat)
			learned := 12
			res, err := gen.Generate(context.Background(), MeaningRequest{
				Words:          tt.words,
				Textbook:       MeaningTextbook{Edition: "人教版PEP", Grade: "三年级", Volume: "上册"},
				LearnedLessons: &learned,
			})
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("Generate error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Generate error = %v, want nil", err)
			}
			if len(res.Meanings) != len(tt.wantMeaning) {
				t.Fatalf("Meanings = %v, want %v", res.Meanings, tt.wantMeaning)
			}
			for w, m := range tt.wantMeaning {
				if res.Meanings[w] != m {
					t.Fatalf("Meanings[%s] = %q, want %q", w, res.Meanings[w], m)
				}
			}
			if strings.Join(res.Missing, ",") != strings.Join(tt.wantMissing, ",") {
				t.Fatalf("Missing = %v, want %v", res.Missing, tt.wantMissing)
			}
		})
	}
}

func TestMeaningGenerator_Guards(t *testing.T) {
	fake := &fakeChatReader{reply: `[]`}
	gen := NewMeaningGenerator(fake.chat)

	if _, err := gen.Generate(context.Background(), MeaningRequest{Words: nil}); !errors.Is(err, ErrEmptyWords) {
		t.Fatalf("empty words error = %v, want ErrEmptyWords", err)
	}
	if _, err := gen.Generate(context.Background(), MeaningRequest{Words: []string{"  ", "  "}}); !errors.Is(err, ErrEmptyWords) {
		t.Fatalf("whitespace words error = %v, want ErrEmptyWords", err)
	}
	if fake.calls != 0 {
		t.Fatalf("guard failures must not reach the seam, calls = %d", fake.calls)
	}

	many := make([]string, maxMeaningWords+1)
	for i := range many {
		many[i] = "word" + strconv.Itoa(i)
	}
	if _, err := gen.Generate(context.Background(), MeaningRequest{Words: many}); !errors.Is(err, ErrTooManyEntries) {
		t.Fatalf("over-cap error = %v, want ErrTooManyEntries", err)
	}

	// Duplicates collapse; the missing list keeps REQUEST order.
	fake.reply = `[{"word":"apple","meaning":"n. 苹果"}]`
	res, err := gen.Generate(context.Background(), MeaningRequest{Words: []string{" apple ", "banana", "apple"}})
	if err != nil {
		t.Fatalf("Generate error = %v", err)
	}
	if len(res.Meanings) != 1 || res.Meanings["apple"] != "n. 苹果" {
		t.Fatalf("Meanings = %v, want only apple", res.Meanings)
	}
	if strings.Join(res.Missing, ",") != "banana" {
		t.Fatalf("Missing = %v, want [banana]", res.Missing)
	}

	var nilGen = NewMeaningGenerator(nil)
	if _, err := nilGen.Generate(context.Background(), MeaningRequest{Words: []string{"apple"}}); !errors.Is(err, errNilSeam) {
		t.Fatalf("nil seam error = %v, want errNilSeam", err)
	}
}

// TestMeaningPromptsCarryTheContract pins the AC 「自动测试验证提示所用
// 背景」: the system prompt fixes the supplement's honesty rules and the
// user prompt carries the CONFIRMED textbook context — edition, grade,
// volume and the learned-lessons gauge (US-19: grounded in the specific
// textbook, never a region guess; there is no region field to carry).
func TestMeaningPromptsCarryTheContract(t *testing.T) {
	if !strings.Contains(meaningSystemPrompt, "只依据给出的教材背景") {
		t.Error("system prompt must ground generation in the given textbook context")
	}
	if !strings.Contains(meaningSystemPrompt, "不是教材原文") {
		t.Error("system prompt must state the supplement is not textbook original (AC 不冒充教材原文)")
	}
	if !strings.Contains(meaningSystemPrompt, "不要生成古诗或课文内容") {
		t.Error("system prompt must forbid generating 古诗/课文 content")
	}

	learned := 12
	user := BuildMeaningUserPrompt(MeaningRequest{
		Words:          []string{"apple", "ice cream"},
		Textbook:       MeaningTextbook{Edition: "人教版PEP", Stage: "小学", Grade: "三年级", Volume: "上册", Module: "Unit 3"},
		LearnedLessons: &learned,
	})
	for _, want := range []string{
		"版本 人教版PEP", "学段 小学", "年级 三年级", "册 上册", "模块 Unit 3",
		"已学完第 12 课（含第 12 课）",
		"1. apple", "2. ice cream",
	} {
		if !strings.Contains(user, want) {
			t.Errorf("user prompt missing confirmed context %q:\n%s", want, user)
		}
	}

	// Unregistered context stays honest: no meta → said so; no progress →
	// 尚未登记, never a fabricated gauge.
	bare := BuildMeaningUserPrompt(MeaningRequest{Words: []string{"apple"}})
	if !strings.Contains(bare, "家长未登记教材版本信息") {
		t.Errorf("bare prompt must say the meta is unregistered:\n%s", bare)
	}
	if !strings.Contains(bare, "尚未登记学习进度") {
		t.Errorf("bare prompt must say progress is unregistered:\n%s", bare)
	}
}
