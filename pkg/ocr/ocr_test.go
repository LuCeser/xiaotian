package ocr

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// jpegSoi is a minimal JPEG prefix (SOI + start-of-next-marker) for
// validation-path tests; the fake reader never decodes it.
var jpegSoi = []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F'}

func TestPipeline_Process(t *testing.T) {
	tests := []struct {
		name      string
		image     []byte
		reply     string
		readerErr error
		minConf   float64
		wantErr   error
		wantText  string
		wantConf  float64
	}{
		{
			name:     "confident read passes through",
			image:    jpegSoi,
			reply:    `{"text":"apple","confidence":0.92}`,
			minConf:  0.6,
			wantErr:  nil,
			wantText: "apple",
			wantConf: 0.92,
		},
		{
			name:     "chinese word round-trips",
			image:    jpegSoi,
			reply:    `{"text":"漂亮","confidence":0.88}`,
			minConf:  0.6,
			wantErr:  nil,
			wantText: "漂亮",
			wantConf: 0.88,
		},
		{
			name:     "low confidence returns result AND sentinel",
			image:    jpegSoi,
			reply:    `{"text":"appl?","confidence":0.4}`,
			minConf:  0.6,
			wantErr:  ErrLowConfidence,
			wantText: "appl?",
			wantConf: 0.4,
		},
		{
			name:     "fenced reply still parses",
			image:    jpegSoi,
			reply:    "```json\n{\"text\":\"apple\",\"confidence\":0.9}\n```",
			minConf:  0.6,
			wantErr:  nil,
			wantText: "apple",
			wantConf: 0.9,
		},
		{
			name:    "empty image fails before reader",
			image:   nil,
			reply:   `{"text":"x","confidence":1}`,
			minConf: 0.6,
			wantErr: ErrEmptyImage,
		},
		{
			name:    "png magic rejected",
			image:   []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A},
			reply:   `{"text":"x","confidence":1}`,
			minConf: 0.6,
			wantErr: ErrNotJPEG,
		},
		{
			name:      "reader error wraps and propagates",
			image:     jpegSoi,
			readerErr: errors.New("upstream 500"),
			minConf:   0.6,
			wantErr:   errReaderFailed,
		},
		{
			name:    "unparseable reply",
			image:   jpegSoi,
			reply:   "I could not read the image, sorry.",
			minConf: 0.6,
			wantErr: errUnparseableReply,
		},
		{
			name:    "empty recognized text",
			image:   jpegSoi,
			reply:   `{"text":"  ","confidence":0.99}`,
			minConf: 0.6,
			wantErr: ErrEmptyText,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := NewPipeline(func(_ context.Context, _, _ string, img []byte) (string, error) {
				if tt.readerErr != nil {
					return "", tt.readerErr
				}
				return tt.reply, nil
			}, tt.minConf)

			res, err := p.Process(context.Background(), tt.image)

			if tt.wantErr != nil {
				if err == nil {
					t.Fatalf("Process() expected error %v, got nil", tt.wantErr)
				}
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("Process() expected error %v, got %v", tt.wantErr, err)
				}
				// The low-confidence path still returns the best guess so the
				// client can prefill the manual input.
				if tt.wantErr == ErrLowConfidence && res.Text != tt.wantText {
					t.Errorf("low-confidence result text = %q, want %q", res.Text, tt.wantText)
				}
				return
			}
			if err != nil {
				t.Fatalf("Process() unexpected error: %v", err)
			}
			if res.Text != tt.wantText {
				t.Errorf("Process() text = %q, want %q", res.Text, tt.wantText)
			}
			if res.Confidence != tt.wantConf {
				t.Errorf("Process() confidence = %v, want %v", res.Confidence, tt.wantConf)
			}
		})
	}
}

func TestPipeline_NilSeam(t *testing.T) {
	p := NewPipeline(nil, 0.6)
	if _, err := p.Process(context.Background(), jpegSoi); !errors.Is(err, errNilSeam) {
		t.Fatalf("expected errNilSeam, got %v", err)
	}
}

func TestParseOCRJSON(t *testing.T) {
	tests := []struct {
		name     string
		reply    string
		wantText string
		wantConf float64
		wantOK   bool
	}{
		{name: "bare json", reply: `{"text":"apple","confidence":0.9}`, wantText: "apple", wantConf: 0.9, wantOK: true},
		{name: "prose-wrapped json", reply: `好的，识别结果：{"text":"apple","confidence":0.85} 请核对。`, wantText: "apple", wantConf: 0.85, wantOK: true},
		{name: "fenced json", reply: "```json\n{\"text\":\"漂亮\",\"confidence\":0.7}\n```", wantText: "漂亮", wantConf: 0.7, wantOK: true},
		{name: "leading whitespace json", reply: "\n\n  {\"text\":\"a\",\"confidence\":0.8}", wantText: "a", wantConf: 0.8, wantOK: true},
		{name: "regex fallback when json invalid", reply: `the text is {"text":"apple" somewhere`, wantText: "apple", wantConf: 0.5, wantOK: true},
		// Live-observed thinking-model shape: the JSON object with trailing
		// garbage braces breaks the outermost span, but the decoder scan
		// recovers the object (and its confidence, not the regex default).
		{name: "json buried with trailing brace garbage", reply: `思考：我认为 {"note":"x"} 最终答案是 {"text":"apple","confidence":0.9} 就这样 } 完毕`, wantText: "apple", wantConf: 0.9, wantOK: true},
		{name: "escaped quotes in text", reply: `{"text":"he said \"hi\"","confidence":0.8}`, wantText: `he said "hi"`, wantConf: 0.8, wantOK: true},
		{name: "confidence clamped above 1", reply: `{"text":"a","confidence":1.7}`, wantText: "a", wantConf: 1, wantOK: true},
		{name: "confidence clamped below 0", reply: `{"text":"a","confidence":-0.2}`, wantText: "a", wantConf: 0, wantOK: true},
		{name: "missing text field", reply: `{"confidence":0.9}`, wantOK: false},
		{name: "garbage", reply: "no json at all", wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text, conf, ok := parseOCRJSON(tt.reply)
			if ok != tt.wantOK {
				t.Fatalf("parseOCRJSON() ok = %v, want %v", ok, tt.wantOK)
			}
			if !tt.wantOK {
				return
			}
			if text != tt.wantText {
				t.Errorf("parseOCRJSON() text = %q, want %q", text, tt.wantText)
			}
			if conf != tt.wantConf {
				t.Errorf("parseOCRJSON() conf = %v, want %v", conf, tt.wantConf)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 13.1-03: ProcessWords — the multi-word ordered-array prompt variant.
// The Reader seam, JPEG validation and confidence gate are shared with
// Process; only the output contract differs (one array entry per written
// word, positionally aligned with the caller's expected list).
// ---------------------------------------------------------------------------

func TestPipeline_ProcessWords(t *testing.T) {
	tests := []struct {
		name      string
		image     []byte
		reply     string
		readerErr error
		minConf   float64
		wantErr   error
		wantTexts []string
	}{
		{
			name:      "strict json array round-trips in order",
			image:     jpegSoi,
			reply:     `[{"text":"apple","confidence":0.9},{"text":"banbana","confidence":0.8}]`,
			minConf:   0.6,
			wantTexts: []string{"apple", "banbana"},
		},
		{
			name:      "chinese words round-trip",
			image:     jpegSoi,
			reply:     `[{"text":"苹果","confidence":0.9},{"text":"香焦","confidence":0.7}]`,
			minConf:   0.6,
			wantTexts: []string{"苹果", "香焦"},
		},
		{
			name:      "fenced prose-wrapped array still parses",
			image:     jpegSoi,
			reply:     "识别结果：\n```json\n[{\"text\":\"apple\",\"confidence\":0.9},{\"text\":\"cat\",\"confidence\":0.85}]\n```\n请核对。",
			minConf:   0.6,
			wantTexts: []string{"apple", "cat"},
		},
		{
			name:      "unrecognized word keeps its position with empty text",
			image:     jpegSoi,
			reply:     `[{"text":"","confidence":0.2},{"text":"cat","confidence":0.9}]`,
			minConf:   0.6,
			wantErr:   ErrLowConfidence,
			wantTexts: []string{"", "cat"},
		},
		{
			name:      "one low-confidence item trips the gate, readings stay positional",
			image:     jpegSoi,
			reply:     `[{"text":"apple","confidence":0.95},{"text":"banbana","confidence":0.3}]`,
			minConf:   0.6,
			wantErr:   ErrLowConfidence,
			wantTexts: []string{"apple", "banbana"},
		},
		{
			name:      "length mismatch returns readings as-is (never fakes alignment)",
			image:     jpegSoi,
			reply:     `[{"text":"a","confidence":0.9},{"text":"b","confidence":0.9},{"text":"c","confidence":0.9}]`,
			minConf:   0.6,
			wantTexts: []string{"a", "b", "c"},
		},
		{
			name:    "empty image fails before reader",
			image:   nil,
			minConf: 0.6,
			wantErr: ErrEmptyImage,
		},
		{
			name:    "png magic rejected",
			image:   []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A},
			minConf: 0.6,
			wantErr: ErrNotJPEG,
		},
		{
			name:      "reader error wraps and propagates",
			image:     jpegSoi,
			readerErr: errors.New("upstream 500"),
			minConf:   0.6,
			wantErr:   errReaderFailed,
		},
		{
			name:    "unparseable reply",
			image:   jpegSoi,
			reply:   "I see a notebook but cannot transcribe it.",
			minConf: 0.6,
			wantErr: errUnparseableReply,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := NewPipeline(func(_ context.Context, _, _ string, _ []byte) (string, error) {
				if tt.readerErr != nil {
					return "", tt.readerErr
				}
				return tt.reply, nil
			}, tt.minConf)

			readings, err := p.ProcessWords(context.Background(), tt.image, []string{"apple", "banana"})

			if tt.wantErr != nil {
				if err == nil {
					t.Fatalf("ProcessWords() expected error %v, got nil", tt.wantErr)
				}
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("ProcessWords() expected error %v, got %v", tt.wantErr, err)
				}
				// The gated path still returns the positional readings so the
				// caller can display the recognized text without prefilling.
				if tt.wantErr == ErrLowConfidence {
					wantReadings(t, readings, tt.wantTexts)
				}
				return
			}
			if err != nil {
				t.Fatalf("ProcessWords() unexpected error: %v", err)
			}
			wantReadings(t, readings, tt.wantTexts)
		})
	}
}

func wantReadings(t *testing.T, readings []WordReading, wantTexts []string) {
	t.Helper()
	if len(readings) != len(wantTexts) {
		t.Fatalf("readings = %d items, want %d", len(readings), len(wantTexts))
	}
	for i, want := range wantTexts {
		if readings[i].Text != want {
			t.Errorf("readings[%d].Text = %q, want %q", i, readings[i].Text, want)
		}
	}
}

func TestPipeline_ProcessWordsNilSeam(t *testing.T) {
	p := NewPipeline(nil, 0.6)
	if _, err := p.ProcessWords(context.Background(), jpegSoi, []string{"apple"}); !errors.Is(err, errNilSeam) {
		t.Fatalf("expected errNilSeam, got %v", err)
	}
}

func TestPipeline_ProcessWordsPromptsCarryArrayContract(t *testing.T) {
	var gotSystem, gotUser string
	p := NewPipeline(func(_ context.Context, system, user string, _ []byte) (string, error) {
		gotSystem, gotUser = system, user
		return `[{"text":"苹果","confidence":0.9},{"text":"香蕉","confidence":0.8}]`, nil
	}, 0.6)

	expected := []string{"苹果", "香蕉"}
	if _, err := p.ProcessWords(context.Background(), jpegSoi, expected); err != nil {
		t.Fatalf("ProcessWords() unexpected error: %v", err)
	}
	if !strings.Contains(gotSystem, "JSON") || !strings.Contains(gotSystem, "数组") {
		t.Errorf("system prompt should pin the strict JSON array contract: %q", gotSystem)
	}
	if !strings.Contains(gotUser, "2") {
		t.Errorf("user prompt should carry the expected word count for segmentation: %q", gotUser)
	}
}

func TestParseWordsJSON(t *testing.T) {
	tests := []struct {
		name      string
		reply     string
		wantTexts []string
		wantConfs []float64
		wantOK    bool
	}{
		{
			name:      "bare array",
			reply:     `[{"text":"apple","confidence":0.9},{"text":"cat","confidence":0.8}]`,
			wantTexts: []string{"apple", "cat"},
			wantConfs: []float64{0.9, 0.8},
			wantOK:    true,
		},
		{
			name:      "prose-wrapped array",
			reply:     `好的，识别结果：[{"text":"苹果","confidence":0.85},{"text":"香蕉","confidence":0.7}] 请核对。`,
			wantTexts: []string{"苹果", "香蕉"},
			wantConfs: []float64{0.85, 0.7},
			wantOK:    true,
		},
		{
			name:      "fenced array",
			reply:     "```json\n[{\"text\":\"漂亮\",\"confidence\":0.7}]\n```",
			wantTexts: []string{"漂亮"},
			wantConfs: []float64{0.7},
			wantOK:    true,
		},
		{
			// Live-observed thinking-model shape: buried array plus trailing
			// brace garbage — the decoder scan recovers the LAST full array.
			name:      "array buried with trailing brace garbage",
			reply:     `思考：我认为 {"note":"x"} 最终答案是 [{"text":"apple","confidence":0.9},{"text":"cat","confidence":0.85}] 就这样 } 完毕`,
			wantTexts: []string{"apple", "cat"},
			wantConfs: []float64{0.9, 0.85},
			wantOK:    true,
		},
		{
			// Regex fallback: text fields extracted in order, confidence
			// defaults to 0.5 (below any sane threshold → the gate fires).
			name:      "regex fallback when array json invalid",
			reply:     `第一个词 {"text":"apple"} 第二个词 {"text":"cat"}。`,
			wantTexts: []string{"apple", "cat"},
			wantConfs: []float64{0.5, 0.5},
			wantOK:    true,
		},
		{
			name:      "empty array parses honestly (model saw nothing)",
			reply:     `[]`,
			wantTexts: []string{},
			wantConfs: []float64{},
			wantOK:    true,
		},
		{
			name:      "confidence clamped above 1",
			reply:     `[{"text":"a","confidence":1.7}]`,
			wantTexts: []string{"a"},
			wantConfs: []float64{1},
			wantOK:    true,
		},
		{
			name:      "confidence clamped below 0",
			reply:     `[{"text":"a","confidence":-0.2}]`,
			wantTexts: []string{"a"},
			wantConfs: []float64{0},
			wantOK:    true,
		},
		{name: "single object is not an array", reply: `{"text":"apple","confidence":0.9}`, wantOK: false},
		{name: "garbage", reply: "no json at all", wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			readings, ok := parseWordsJSON(tt.reply)
			if ok != tt.wantOK {
				t.Fatalf("parseWordsJSON() ok = %v, want %v", ok, tt.wantOK)
			}
			if !tt.wantOK {
				return
			}
			if len(readings) != len(tt.wantTexts) {
				t.Fatalf("parseWordsJSON() readings = %d, want %d", len(readings), len(tt.wantTexts))
			}
			for i := range readings {
				if readings[i].Text != tt.wantTexts[i] {
					t.Errorf("readings[%d].Text = %q, want %q", i, readings[i].Text, tt.wantTexts[i])
				}
				if readings[i].Confidence != tt.wantConfs[i] {
					t.Errorf("readings[%d].Confidence = %v, want %v", i, readings[i].Confidence, tt.wantConfs[i])
				}
			}
		})
	}
}

func TestPipeline_ReceivesPrompts(t *testing.T) {
	var gotSystem, gotUser string
	p := NewPipeline(func(_ context.Context, system, user string, _ []byte) (string, error) {
		gotSystem, gotUser = system, user
		return `{"text":"apple","confidence":0.9}`, nil
	}, 0.6)

	if _, err := p.Process(context.Background(), jpegSoi); err != nil {
		t.Fatalf("Process() unexpected error: %v", err)
	}
	if gotSystem == "" {
		t.Error("expected non-empty system prompt to reach the reader seam")
	}
	if gotUser == "" {
		t.Error("expected non-empty user prompt to reach the reader seam")
	}
	if !strings.Contains(gotSystem, "JSON") {
		t.Errorf("system prompt should pin the JSON output contract")
	}
}
