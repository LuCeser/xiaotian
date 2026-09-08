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
