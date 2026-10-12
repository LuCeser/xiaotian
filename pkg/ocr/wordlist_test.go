package ocr

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// pngMagic is a minimal PNG prefix (8-byte signature + IHDR chunk start)
// for validation-path tests; the fake reader never decodes it.
var pngMagic = []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x0D, 'I', 'H', 'D', 'R'}

// fakeListReader captures the seam inputs and replies with a canned string.
type fakeListReader struct {
	reply     string
	err       error
	gotMime   string
	gotSystem string
	gotUser   string
	calls     int
}

func (f *fakeListReader) read(ctx context.Context, systemPrompt, userPrompt string, image []byte, mimeType string) (string, error) {
	f.calls++
	f.gotMime = mimeType
	f.gotSystem = systemPrompt
	f.gotUser = userPrompt
	if f.err != nil {
		return "", f.err
	}
	return f.reply, nil
}

func TestWordlistPipeline_Recognize(t *testing.T) {
	tests := []struct {
		name       string
		image      []byte
		reply      string
		readerErr  error
		wantErr    error
		wantWords  []string
		wantMeans  []string
		wantConfs  []float64
		checkMime  string
		maxBytes   int
		skipDetail bool
	}{
		{
			name:      "clean array parses with phrase integrity",
			image:     jpegSoi,
			reply:     `[{"word":"ice cream","meaning":"冰激凌","confidence":0.9},{"word":"apple","meaning":"苹果","confidence":0.85}]`,
			checkMime: "image/jpeg",
			wantWords: []string{"ice cream", "apple"},
			wantMeans: []string{"冰激凌", "苹果"},
			wantConfs: []float64{0.9, 0.85},
		},
		{
			name:      "png magic accepted and forwarded as image/png",
			image:     pngMagic,
			reply:     `[{"word":"apple","meaning":"苹果","confidence":0.9}]`,
			checkMime: "image/png",
			wantWords: []string{"apple"},
			wantMeans: []string{"苹果"},
			wantConfs: []float64{0.9},
		},
		{
			name:      "empty meaning is a legal word-list entry",
			image:     jpegSoi,
			reply:     `[{"word":"apple","meaning":"","confidence":0.7}]`,
			checkMime: "image/jpeg",
			wantWords: []string{"apple"},
			wantMeans: []string{""},
			wantConfs: []float64{0.7},
		},
		{
			name:      "entries are trimmed, empty-word entries dropped",
			image:     jpegSoi,
			reply:     `[{"word":"  apple  ","meaning":" 苹果 ","confidence":1.5},{"word":"   ","meaning":"x","confidence":0.9}]`,
			checkMime: "image/jpeg",
			wantWords: []string{"apple"},
			wantMeans: []string{"苹果"},
			wantConfs: []float64{1},
		},
		{
			name:      "fenced reply still parses",
			image:     jpegSoi,
			reply:     "```json\n[{\"word\":\"apple\",\"meaning\":\"苹果\",\"confidence\":0.9}]\n```",
			checkMime: "image/jpeg",
			wantWords: []string{"apple"},
			wantMeans: []string{"苹果"},
			wantConfs: []float64{0.9},
		},
		{
			name:      "thinking-model prose around the array still parses",
			image:     jpegSoi,
			reply:     "我先看一下这张图……最终结果：[{\"word\":\"go for a walk\",\"meaning\":\"散步\",\"confidence\":0.8}] 以上。",
			checkMime: "image/jpeg",
			wantWords: []string{"go for a walk"},
			wantMeans: []string{"散步"},
			wantConfs: []float64{0.8},
		},
		{
			name:      "empty array is a legal no-entries outcome",
			image:     jpegSoi,
			reply:     `[]`,
			checkMime: "image/jpeg",
			wantWords: []string{},
			wantMeans: []string{},
			wantConfs: []float64{},
		},
		{
			name:      "entries without meaning or confidence fields still decode",
			image:     jpegSoi,
			reply:     `[{"word":"river"},{"word":"house"}]`,
			checkMime: "image/jpeg",
			wantWords: []string{"river", "house"},
			wantMeans: []string{"", ""},
			wantConfs: []float64{0, 0},
		},
		{
			name:      "word-only reply without any array parses via the regex fallback",
			image:     jpegSoi,
			reply:     "识别到：{\"word\":\"river\",\"meaning\":\"河流\"}, {\"word\":\"house\",\"meaning\":\"房子\"}",
			checkMime: "image/jpeg",
			wantWords: []string{"river", "house"},
			wantMeans: []string{"河流", "房子"},
			wantConfs: []float64{0.5, 0.5},
		},
		{
			name:      "empty image fails before the reader",
			image:     nil,
			reply:     `[{"word":"apple","meaning":"苹果","confidence":0.9}]`,
			checkMime: "",
			wantErr:   ErrEmptyImage,
		},
		{
			name:      "non-image bytes rejected",
			image:     []byte("GIF89a...."),
			reply:     `[{"word":"apple","meaning":"苹果","confidence":0.9}]`,
			checkMime: "",
			wantErr:   ErrNotImage,
		},
		{
			name:      "oversize image rejected before the reader",
			image:     append(append([]byte{}, jpegSoi...), make([]byte, 100)...),
			reply:     `[{"word":"apple","meaning":"苹果","confidence":0.9}]`,
			maxBytes:  64,
			checkMime: "",
			wantErr:   ErrImageTooLarge,
		},
		{
			name:      "reader error wraps the sentinel",
			image:     jpegSoi,
			readerErr: errors.New("upstream 500"),
			checkMime: "image/jpeg",
			wantErr:   errReaderFailed,
		},
		{
			name:      "unparseable reply fails",
			image:     jpegSoi,
			reply:     "抱歉，我无法识别这张图片。",
			checkMime: "image/jpeg",
			wantErr:   errUnparseableReply,
		},
		{
			name:      "over-cap entry count fails with the cap sentinel",
			image:     jpegSoi,
			reply:     fmt.Sprintf("[%s]", strings.Repeat(`{"word":"w","meaning":"","confidence":0.9},`, 100)+`{"word":"extra","meaning":"","confidence":0.9}`),
			checkMime: "image/jpeg",
			wantErr:   ErrTooManyEntries,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fr := &fakeListReader{reply: tt.reply, err: tt.readerErr}
			maxBytes := tt.maxBytes
			if maxBytes == 0 {
				maxBytes = 1 << 20
			}
			p := NewWordlistPipeline(fr.read, maxBytes)

			res, err := p.Recognize(context.Background(), tt.image)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("Recognize error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Recognize: %v", err)
			}
			if tt.checkMime != "" && fr.gotMime != tt.checkMime {
				t.Errorf("reader mime = %q, want %q", fr.gotMime, tt.checkMime)
			}
			entries := res.Entries
			if len(entries) != len(tt.wantWords) {
				t.Fatalf("entries = %d (%+v), want %d", len(entries), entries, len(tt.wantWords))
			}
			for i, e := range entries {
				if e.Word != tt.wantWords[i] {
					t.Errorf("entries[%d].Word = %q, want %q", i, e.Word, tt.wantWords[i])
				}
				if e.Meaning != tt.wantMeans[i] {
					t.Errorf("entries[%d].Meaning = %q, want %q", i, e.Meaning, tt.wantMeans[i])
				}
				if e.Confidence != tt.wantConfs[i] {
					t.Errorf("entries[%d].Confidence = %v, want %v", i, e.Confidence, tt.wantConfs[i])
				}
			}
		})
	}
}

// TestWordlistPipeline_RecognizeTextbookMeta pins the Issue #5 recognition
// contract: the model reply is ONE object carrying the textbook header it
// could actually read (version/stage/grade/volume/module/lesson) plus the
// entries; unrecognized fields are null and must surface as empty strings
// (待确认) — never fabricated. Entries inherit the page-level lesson unless
// they carry their own. Legacy bare-array replies keep parsing with no meta.
func TestWordlistPipeline_RecognizeTextbookMeta(t *testing.T) {
	tests := []struct {
		name        string
		reply       string
		wantMeta    *WordlistTextbook
		wantLessons []string
		wantWords   []string
	}{
		{
			name: "object reply carries the readable textbook header",
			reply: `{"textbook":{"edition":"人教版PEP","stage":null,"grade":"三年级","volume":"上册","module":"Unit 3","lesson":"第5课"},` +
				`"entries":[{"word":"ice cream","meaning":"冰激凌","confidence":0.9},{"word":"apple","meaning":"苹果","confidence":0.85}]}`,
			wantMeta:    &WordlistTextbook{Edition: "人教版PEP", Grade: "三年级", Volume: "上册", Module: "Unit 3", Lesson: "第5课"},
			wantLessons: []string{"第5课", "第5课"},
			wantWords:   []string{"ice cream", "apple"},
		},
		{
			name: "entries without lesson inherit the page lesson, own lesson wins",
			reply: `{"textbook":{"edition":"外研版","lesson":"第2课"},"entries":[` +
				`{"word":"river","meaning":"河流","lesson":"第3课"},{"word":"house","meaning":"房子"}]}`,
			wantMeta:    &WordlistTextbook{Edition: "外研版", Lesson: "第2课"},
			wantLessons: []string{"第3课", "第2课"},
			wantWords:   []string{"river", "house"},
		},
		{
			name:        "textbook null keeps every field 待确认",
			reply:       `{"textbook":null,"entries":[{"word":"sun","meaning":"太阳"}]}`,
			wantMeta:    nil,
			wantLessons: []string{""},
			wantWords:   []string{"sun"},
		},
		{
			name:        "partial meta decodes with empty strings for the unread fields",
			reply:       `{"textbook":{"grade":"四年级"},"entries":[{"word":"moon","meaning":"月亮"}]}`,
			wantMeta:    &WordlistTextbook{Grade: "四年级"},
			wantLessons: []string{""},
			wantWords:   []string{"moon"},
		},
		{
			name:        "meta and lessons are trimmed",
			reply:       `{"textbook":{"edition":" 人教版 ","lesson":" 第7课 "},"entries":[{"word":"cat","meaning":"猫","lesson":" 第7课 "}]}`,
			wantMeta:    &WordlistTextbook{Edition: "人教版", Lesson: "第7课"},
			wantLessons: []string{"第7课"},
			wantWords:   []string{"cat"},
		},
		{
			name:        "thinking-model prose around the object still parses",
			reply:       "我看了下这页……{\"textbook\":{\"edition\":\"人教版\"},\"entries\":[{\"word\":\"dog\",\"meaning\":\"狗\"}]} 以上。",
			wantMeta:    &WordlistTextbook{Edition: "人教版"},
			wantLessons: []string{""},
			wantWords:   []string{"dog"},
		},
		{
			name:        "legacy bare-array reply parses with no meta",
			reply:       `[{"word":"apple","meaning":"苹果","confidence":0.9}]`,
			wantMeta:    nil,
			wantLessons: []string{""},
			wantWords:   []string{"apple"},
		},
		{
			name:        "all-empty meta object is reported as no meta",
			reply:       `{"textbook":{"edition":"","stage":null},"entries":[{"word":"pig","meaning":"猪"}]}`,
			wantMeta:    nil,
			wantLessons: []string{""},
			wantWords:   []string{"pig"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fr := &fakeListReader{reply: tt.reply}
			p := NewWordlistPipeline(fr.read, 1<<20)
			res, err := p.Recognize(context.Background(), jpegSoi)
			if err != nil {
				t.Fatalf("Recognize: %v", err)
			}
			if len(res.Entries) != len(tt.wantWords) {
				t.Fatalf("entries = %d (%+v), want %d", len(res.Entries), res.Entries, len(tt.wantWords))
			}
			for i, e := range res.Entries {
				if e.Word != tt.wantWords[i] {
					t.Errorf("entries[%d].Word = %q, want %q", i, e.Word, tt.wantWords[i])
				}
				if e.Lesson != tt.wantLessons[i] {
					t.Errorf("entries[%d].Lesson = %q, want %q", i, e.Lesson, tt.wantLessons[i])
				}
			}
			if tt.wantMeta == nil {
				if res.Textbook != nil {
					t.Fatalf("textbook meta = %+v, want nil", res.Textbook)
				}
				return
			}
			if res.Textbook == nil {
				t.Fatalf("textbook meta = nil, want %+v", tt.wantMeta)
			}
			if *res.Textbook != *tt.wantMeta {
				t.Errorf("textbook meta = %+v, want %+v", *res.Textbook, *tt.wantMeta)
			}
		})
	}
}

func TestWordlistPipeline_NilSeam(t *testing.T) {
	p := NewWordlistPipeline(nil, 1<<20)
	if _, err := p.Recognize(context.Background(), jpegSoi); !errors.Is(err, errNilSeam) {
		t.Fatalf("Recognize error = %v, want errNilSeam", err)
	}
}

// TestWordlistPipeline_ReaderNeverCalledOnRejection proves the fail-fast
// guards (empty / non-image / oversize) happen before any vision-API cost.
func TestWordlistPipeline_ReaderNeverCalledOnRejection(t *testing.T) {
	fr := &fakeListReader{reply: `[]`}
	p := NewWordlistPipeline(fr.read, 16)
	if _, err := p.Recognize(context.Background(), nil); !errors.Is(err, ErrEmptyImage) {
		t.Fatalf("empty: err = %v", err)
	}
	if _, err := p.Recognize(context.Background(), []byte("not an image")); !errors.Is(err, ErrNotImage) {
		t.Fatalf("non-image: err = %v", err)
	}
	if _, err := p.Recognize(context.Background(), append(append([]byte{}, pngMagic...), make([]byte, 64)...)); !errors.Is(err, ErrImageTooLarge) {
		t.Fatalf("oversize: err = %v", err)
	}
	if fr.calls != 0 {
		t.Fatalf("reader called %d times on rejected inputs, want 0", fr.calls)
	}
}

// TestWordlistPromptsCarryTheContract pins the prompt's behavioral clauses
// (Issue #3 + #5 acceptance): word-list-only extraction, phrase integrity,
// verbatim meanings, strict JSON output, textbook-header reading with the
// no-fabrication rule (unread → null). A prompt edit that drops one of
// these clauses should fail the matching assertion.
func TestWordlistPromptsCarryTheContract(t *testing.T) {
	fr := &fakeListReader{reply: `[]`}
	p := NewWordlistPipeline(fr.read, 1<<20)
	if _, err := p.Recognize(context.Background(), jpegSoi); err != nil {
		t.Fatalf("Recognize: %v", err)
	}
	for _, want := range []string{"词表", "短语", "原文", "JSON", "教材", "课次", "null"} {
		if !strings.Contains(fr.gotSystem, want) {
			t.Errorf("system prompt lost clause %q:\n%s", want, fr.gotSystem)
		}
	}
	if fr.gotUser == "" {
		t.Error("user prompt is empty")
	}
}
