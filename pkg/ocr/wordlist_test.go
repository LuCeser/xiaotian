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
		readerErr  error
		wantErr    error
		name       string
		reply      string
		checkMime  string
		image      []byte
		wantWords  []string
		wantMeans  []string
		wantConfs  []float64
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

			res, err := p.Recognize(context.Background(), tt.image, "")
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
			res, err := p.Recognize(context.Background(), jpegSoi, "")
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
	if _, err := p.Recognize(context.Background(), jpegSoi, ""); !errors.Is(err, errNilSeam) {
		t.Fatalf("Recognize error = %v, want errNilSeam", err)
	}
}

// TestWordlistPipeline_ReaderNeverCalledOnRejection proves the fail-fast
// guards (empty / non-image / oversize) happen before any vision-API cost.
func TestWordlistPipeline_ReaderNeverCalledOnRejection(t *testing.T) {
	fr := &fakeListReader{reply: `[]`}
	p := NewWordlistPipeline(fr.read, 16)
	if _, err := p.Recognize(context.Background(), nil, ""); !errors.Is(err, ErrEmptyImage) {
		t.Fatalf("empty: err = %v", err)
	}
	if _, err := p.Recognize(context.Background(), []byte("not an image"), ""); !errors.Is(err, ErrNotImage) {
		t.Fatalf("non-image: err = %v", err)
	}
	if _, err := p.Recognize(context.Background(), append(append([]byte{}, pngMagic...), make([]byte, 64)...), ""); !errors.Is(err, ErrImageTooLarge) {
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
	if _, err := p.Recognize(context.Background(), jpegSoi, ""); err != nil {
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

// TestWordlistChinesePromptContract (Issue #10, widened by Issue #11 into
// the MIXED-PAGE contract and by Issue #13 with the passages class) pins
// the SUBJECT selection and the 语文 prompt's own clauses:
// category=chinese routes the Chinese system+user prompts (subject
// isolation starts at the seam), the clause set keeps 词语 whole (不拆
// 单字), keeps the material's own pinyin/annotation as the meaning (no
// forced gloss), ignores English content on a mixed page, — #11 —
// extracts WHOLE poems (title/author/ordered lines, verbatim, never
// generated), and — #13 — extracts WHOLE passages (natural-paragraph
// boundaries and order, per-paragraph recitation marks, exercises never
// merged into the text) while 练习题 stays display-only in others (区分
// 展示, 练习题不自动选入). The default ("" and "english") keeps the
// Issue #3 English prompts.
func TestWordlistChinesePromptContract(t *testing.T) {
	for _, tc := range []struct {
		name        string
		category    string
		wantChinese bool
	}{
		{"chinese selects the chinese prompts", CategoryChinese, true},
		{"english keeps the english prompts", CategoryEnglish, false},
		{"absent category defaults to english", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fr := &fakeListReader{reply: `[]`}
			p := NewWordlistPipeline(fr.read, 1<<20)
			if _, err := p.Recognize(context.Background(), jpegSoi, tc.category); err != nil {
				t.Fatalf("Recognize: %v", err)
			}
			if got := strings.Contains(fr.gotSystem, "语文学习内容导入助手"); got != tc.wantChinese {
				t.Errorf("system prompt chinese=%v, want %v\n%s", got, tc.wantChinese, fr.gotSystem)
			}
			if got := strings.Contains(fr.gotUser, "完整古诗"); got != tc.wantChinese {
				t.Errorf("user prompt chinese=%v, want the mixed-page extraction ask, got %q", got, fr.gotUser)
			}
			if got := strings.Contains(fr.gotSystem, "英语词表导入助手"); got == tc.wantChinese {
				t.Errorf("system prompt english=%v, want %v", got, !tc.wantChinese)
			}
		})
	}

	// The chinese clause set (AC2: 明确词语表, 不拆单字; AC6: 同页中英混排隔离;
	// 保留材料给出的拼音, 不强制释义; #11: 古诗整首、原文不生成、练习题只展示;
	// #13: 课文整篇按自然段、背诵标记段落、练习题不并入课文).
	fr := &fakeListReader{reply: `[]`}
	p := NewWordlistPipeline(fr.read, 1<<20)
	if _, err := p.Recognize(context.Background(), jpegSoi, CategoryChinese); err != nil {
		t.Fatalf("Recognize chinese: %v", err)
	}
	for _, want := range []string{
		"词语表",        // only clear word-list entries
		"不要拆成单个汉字",   // multi-char words stay whole
		"英文单词",       // mixed-page English content is ignored
		"拼音",         // the material's own annotation is transcribed
		"不要求补写释义",    // no forced English-style gloss
		"完整的古诗",      // #11: whole poems are their own array
		"整首保存",       // #11: a poem is saved as one work
		"保留原有分行",     // #11: line breaks and order survive
		"不要凭记忆补写或生成", // #11/#13: never generate the unclear原文
		"背诵或默写要求",    // #11: the page's explicit mark rides required
		"完整的课文",      // #13: whole passages are their own array
		"整篇保存",       // #13: a passage is saved as one original
		"保留自然段的划分",   // #13: paragraph boundaries and order survive
		"自然段序号",      // #13: per-paragraph marks ride required_paragraphs
		"不要并入",       // #13: exercises never merge into the passage
		"不会导入",       // #11: others are display-only
		"JSON",       // strict output contract
		"教材",         // textbook header reading
	} {
		if !strings.Contains(fr.gotSystem, want) {
			t.Errorf("chinese system prompt lost clause %q:\n%s", want, fr.gotSystem)
		}
	}
}

// TestWordlistRecognizeChineseEntries — the chinese draft parses through the
// SAME lenient reply pipeline (object contract, textbook header, empty
// meanings legal), so a 语文 reply needs no parsing special-case.
func TestWordlistRecognizeChineseEntries(t *testing.T) {
	reply := `{"textbook":{"edition":"统编版","stage":null,"grade":"二年级","volume":"下册","module":null,"lesson":"第5课"},
		"entries":[
			{"word":"美丽","meaning":"měi lì","lesson":"第5课","confidence":0.95},
			{"word":"世界","meaning":"","lesson":null,"confidence":0.9}
		]}`
	fr := &fakeListReader{reply: reply}
	p := NewWordlistPipeline(fr.read, 1<<20)
	res, err := p.Recognize(context.Background(), jpegSoi, CategoryChinese)
	if err != nil {
		t.Fatalf("Recognize chinese entries: %v", err)
	}
	if res.Textbook == nil || res.Textbook.Edition != "统编版" || res.Textbook.Lesson != "第5课" {
		t.Errorf("textbook = %+v, want 统编版 header with lesson 第5课", res.Textbook)
	}
	if len(res.Entries) != 2 {
		t.Fatalf("entries len = %d, want 2", len(res.Entries))
	}
	if res.Entries[0].Word != "美丽" || res.Entries[0].Meaning != "měi lì" || res.Entries[0].Lesson != "第5课" {
		t.Errorf("entry[0] = %+v, want 美丽/měi lì/第5课", res.Entries[0])
	}
	// A null own-lesson inherits the page-level lesson; an empty meaning is
	// legal (词语表没有给注释).
	if res.Entries[1].Word != "世界" || res.Entries[1].Meaning != "" || res.Entries[1].Lesson != "第5课" {
		t.Errorf("entry[1] = %+v, want 世界/empty/第5课 (page-level lesson inherited)", res.Entries[1])
	}
}

// TestWordlistRecognizeMixedPagePoems (Issue #11) — a 语文 mixed-page reply
// classifies its blocks: 词语 entries, WHOLE poems (title/author/ordered
// lines/required/lesson) and display-only others (正文 text / 练习题
// exercise). The poems keep their line order and fall back to the
// page-level lesson exactly like entries do.
func TestWordlistRecognizeMixedPagePoems(t *testing.T) {
	reply := `{"textbook":{"edition":"统编版","grade":"二年级","volume":"下册","lesson":"第8课"},
		"entries":[{"word":"夜空","meaning":"yè kōng","lesson":"第8课","confidence":0.9}],
		"poems":[
			{"title":"静夜思","author":"李白","lines":["床前明月光，疑是地上霜。","","举头望明月，低头思故乡。"],"lesson":"第8课","required":"背诵","confidence":0.92},
			{"title":"春晓","author":null,"lines":["春眠不觉晓，处处闻啼鸟。","夜来风雨声，花落知多少。"],"lesson":null,"required":null,"confidence":0.88}
		],
		"others":[
			{"kind":"exercise","summary":"读一读，记一记","confidence":0.85},
			{"kind":"text","summary":"课文《夕阳》正文段落","confidence":0.8},
			{"kind":"weird","summary":"未知类型归正文","confidence":0.7},
			{"kind":"text","summary":"  ","confidence":0.6}
		]}`
	fr := &fakeListReader{reply: reply}
	p := NewWordlistPipeline(fr.read, 1<<20)
	res, err := p.Recognize(context.Background(), jpegSoi, CategoryChinese)
	if err != nil {
		t.Fatalf("Recognize mixed page: %v", err)
	}

	if len(res.Entries) != 1 || res.Entries[0].Word != "夜空" {
		t.Errorf("entries = %+v, want the single 词语", res.Entries)
	}

	if len(res.Poems) != 2 {
		t.Fatalf("poems len = %d, want 2 (%+v)", len(res.Poems), res.Poems)
	}
	first := res.Poems[0]
	if first.Title != "静夜思" || first.Author != "李白" || first.Required != "背诵" || first.Lesson != "第8课" {
		t.Errorf("poem[0] = %+v, want 静夜思/李白/背诵/第8课", first)
	}
	// The empty middle line is dropped; the two real lines keep their order
	// (分行和顺序保留).
	if len(first.Lines) != 2 || first.Lines[0] != "床前明月光，疑是地上霜。" || first.Lines[1] != "举头望明月，低头思故乡。" {
		t.Errorf("poem[0].lines = %q, want the two ordered verse lines", first.Lines)
	}
	// A null author decodes empty; a null own-lesson inherits the page-level
	// one; a null required stays "" (the page printed no mark).
	second := res.Poems[1]
	if second.Title != "春晓" || second.Author != "" || second.Required != "" || second.Lesson != "第8课" {
		t.Errorf("poem[1] = %+v, want 春晓/empty author/empty required/inherited 第8课", second)
	}

	// Others are display-only: the empty-summary row is dropped, an unknown
	// kind normalizes to text, and the two kinds stay distinguishable.
	if len(res.Others) != 3 {
		t.Fatalf("others len = %d, want 3 (%+v)", len(res.Others), res.Others)
	}
	if res.Others[0].Kind != OtherKindExercise || res.Others[0].Summary != "读一读，记一记" {
		t.Errorf("others[0] = %+v, want the exercise block", res.Others[0])
	}
	if res.Others[1].Kind != OtherKindText {
		t.Errorf("others[1] = %+v, want the body-text block", res.Others[1])
	}
	if res.Others[2].Kind != OtherKindText {
		t.Errorf("others[2] = %+v, want the unknown kind normalized to text", res.Others[2])
	}
}

// TestWordlistRecognizePoemsOnlyPage — a poems-only mixed page (entries
// legitimately empty) still parses: the poem array alone is a legal reply.
func TestWordlistRecognizePoemsOnlyPage(t *testing.T) {
	reply := `{"textbook":null,"entries":[],"poems":[{"title":"咏鹅","author":"骆宾王","lines":["鹅，鹅，鹅，曲项向天歌。"],"required":"背诵","confidence":0.9}],"others":[]}`
	fr := &fakeListReader{reply: reply}
	p := NewWordlistPipeline(fr.read, 1<<20)
	res, err := p.Recognize(context.Background(), jpegSoi, CategoryChinese)
	if err != nil {
		t.Fatalf("Recognize poems-only page: %v", err)
	}
	if len(res.Entries) != 0 {
		t.Errorf("entries len = %d, want 0", len(res.Entries))
	}
	if len(res.Poems) != 1 || res.Poems[0].Title != "咏鹅" {
		t.Errorf("poems = %+v, want the single 咏鹅", res.Poems)
	}
	if len(res.Others) != 0 {
		t.Errorf("others len = %d, want 0", len(res.Others))
	}
}

// TestWordlistRecognizeDropsBrokenPoems — a poem missing its title or every
// line is a misparse, not a work to salvage: it is dropped so the preview
// never offers an empty shell for confirmation.
func TestWordlistRecognizeDropsBrokenPoems(t *testing.T) {
	reply := `{"entries":[],"poems":[
		{"title":"","author":"无名","lines":["有内容没有题目"],"confidence":0.9},
		{"title":"只有题目","author":"","lines":["","  "],"confidence":0.9}
	]}`
	fr := &fakeListReader{reply: reply}
	p := NewWordlistPipeline(fr.read, 1<<20)
	res, err := p.Recognize(context.Background(), jpegSoi, CategoryChinese)
	if err != nil {
		t.Fatalf("Recognize broken poems: %v", err)
	}
	if len(res.Poems) != 0 {
		t.Errorf("poems = %+v, want both broken drafts dropped", res.Poems)
	}
}

// TestWordlistRecognizeTooManyPoems — the poems array obeys its own runaway
// cap (ErrTooManyPoems), distinct from the entries cap.
func TestWordlistRecognizeTooManyPoems(t *testing.T) {
	poems := make([]string, 0, maxWordlistPoems+1)
	for i := 0; i <= maxWordlistPoems; i++ {
		poems = append(poems, fmt.Sprintf(`{"title":"诗%d","lines":["一行"],"confidence":0.9}`, i))
	}
	reply := `{"entries":[],"poems":[` + strings.Join(poems, ",") + `]}`
	fr := &fakeListReader{reply: reply}
	p := NewWordlistPipeline(fr.read, 1<<20)
	if _, err := p.Recognize(context.Background(), jpegSoi, CategoryChinese); !errors.Is(err, ErrTooManyPoems) {
		t.Fatalf("Recognize over-cap poems: err = %v, want ErrTooManyPoems", err)
	}
}

// TestWordlistRecognizeEnglishReplyHasNoPoems — the English contract never
// asks for poems: an English reply decodes with empty poems/others arrays
// (never nil), so the wire shape stays array-typed for either subject.
func TestWordlistRecognizeEnglishReplyHasNoPoems(t *testing.T) {
	fr := &fakeListReader{reply: `[{"word":"apple","meaning":"苹果","confidence":0.9}]`}
	p := NewWordlistPipeline(fr.read, 1<<20)
	res, err := p.Recognize(context.Background(), jpegSoi, CategoryEnglish)
	if err != nil {
		t.Fatalf("Recognize english: %v", err)
	}
	if res.Poems == nil || len(res.Poems) != 0 {
		t.Errorf("poems = %+v, want empty non-nil", res.Poems)
	}
	if res.Others == nil || len(res.Others) != 0 {
		t.Errorf("others = %+v, want empty non-nil", res.Others)
	}
	if res.Passages == nil || len(res.Passages) != 0 {
		t.Errorf("passages = %+v, want empty non-nil", res.Passages)
	}
}

// TestWordlistRecognizeMixedPagePassages (Issue #13) — a 语文 mixed-page
// reply classifies its blocks: WHOLE passages (title/ordered 自然段/
// per-paragraph marks/lesson) alongside 词语, poems and display-only
// others. The paragraphs keep their boundaries and order, a null own
// lesson inherits the page-level one, and the exercise block stays in
// others — never inside the passage (练习题不自动选入).
func TestWordlistRecognizeMixedPagePassages(t *testing.T) {
	reply := `{"textbook":{"edition":"统编版","grade":"三年级","volume":"上册","lesson":"第6课"},
		"entries":[{"word":"钥匙","meaning":"yào shi","lesson":"第6课","confidence":0.9}],
		"passages":[
			{"title":"秋天的雨","author":null,"paragraphs":["秋天的雨，是一把钥匙。","","秋天的雨，有一盒五彩缤纷的颜料。","秋天的雨，藏着非常好闻的气味。"],"lesson":null,"required":"背诵课文","required_paragraphs":[3,1,1],"confidence":0.9}
		],
		"poems":[],
		"others":[{"kind":"exercise","summary":"课后题：你喜欢秋天的雨吗","confidence":0.85}]}`
	fr := &fakeListReader{reply: reply}
	p := NewWordlistPipeline(fr.read, 1<<20)
	res, err := p.Recognize(context.Background(), jpegSoi, CategoryChinese)
	if err != nil {
		t.Fatalf("Recognize mixed page passages: %v", err)
	}

	if len(res.Passages) != 1 {
		t.Fatalf("passages len = %d, want 1 (%+v)", len(res.Passages), res.Passages)
	}
	got := res.Passages[0]
	// The empty middle paragraph is dropped; the three real paragraphs keep
	// their order (段落边界和顺序保留).
	if len(got.Paragraphs) != 3 || got.Paragraphs[0] != "秋天的雨，是一把钥匙。" || got.Paragraphs[2] != "秋天的雨，藏着非常好闻的气味。" {
		t.Errorf("paragraphs = %q, want the three ordered 自然段", got.Paragraphs)
	}
	if got.Title != "秋天的雨" || got.Author != "" || got.Required != "背诵课文" {
		t.Errorf("passage = %+v, want 秋天的雨/empty author/whole-text mark", got)
	}
	// A null own-lesson inherits the page-level lesson; the per-paragraph
	// marks are sorted and deduplicated onto the KEPT paragraphs.
	if got.Lesson != "第6课" {
		t.Errorf("lesson = %q, want the page-level 第6课 inherited", got.Lesson)
	}
	if len(got.RequiredParagraphs) != 2 || got.RequiredParagraphs[0] != 1 || got.RequiredParagraphs[1] != 3 {
		t.Errorf("required_paragraphs = %v, want [1 3]", got.RequiredParagraphs)
	}
	if len(res.Others) != 1 || res.Others[0].Kind != OtherKindExercise {
		t.Errorf("others = %+v, want the exercise block display-only", res.Others)
	}
}

// TestWordlistRecognizePassagesOnlyPage — a passages-only mixed page
// (entries legitimately empty) still parses: the passage array alone is a
// legal reply, exactly like a poems-only page.
func TestWordlistRecognizePassagesOnlyPage(t *testing.T) {
	reply := `{"textbook":null,"entries":[],"passages":[{"title":"雪孩子","paragraphs":["下了一天的大雪。","雪孩子回来了。"],"required":"朗读课文","required_paragraphs":[],"confidence":0.9}]}`
	fr := &fakeListReader{reply: reply}
	p := NewWordlistPipeline(fr.read, 1<<20)
	res, err := p.Recognize(context.Background(), jpegSoi, CategoryChinese)
	if err != nil {
		t.Fatalf("Recognize passages-only page: %v", err)
	}
	if len(res.Passages) != 1 || res.Passages[0].Title != "雪孩子" || res.Passages[0].Required != "朗读课文" {
		t.Errorf("passages = %+v, want the single 雪孩子 with its own mark", res.Passages)
	}
	if len(res.Passages[0].RequiredParagraphs) != 0 {
		t.Errorf("required_paragraphs = %v, want empty (no per-paragraph mark printed)", res.Passages[0].RequiredParagraphs)
	}
}

// TestWordlistRecognizeDropsBrokenPassages — a passage missing its title
// or every paragraph is a misparse, not an original to salvage: it is
// dropped so the preview never offers an empty shell for confirmation.
func TestWordlistRecognizeDropsBrokenPassages(t *testing.T) {
	reply := `{"entries":[],"passages":[
		{"title":"","paragraphs":["有内容没有题目"],"confidence":0.9},
		{"title":"只有题目","paragraphs":["","  "],"confidence":0.9}
	]}`
	fr := &fakeListReader{reply: reply}
	p := NewWordlistPipeline(fr.read, 1<<20)
	res, err := p.Recognize(context.Background(), jpegSoi, CategoryChinese)
	if err != nil {
		t.Fatalf("Recognize broken passages: %v", err)
	}
	if len(res.Passages) != 0 {
		t.Errorf("passages = %+v, want both broken drafts dropped", res.Passages)
	}
}

// TestWordlistRecognizePassageMarksClamped — a mark the model numbered
// outside the real paragraphs (0, past the end) is noise: it is dropped,
// never carried as a default-selection hint pointing at a paragraph that
// does not exist.
func TestWordlistRecognizePassageMarksClamped(t *testing.T) {
	reply := `{"entries":[],"passages":[{"title":"黄山奇石","paragraphs":["一段。","二段。"],"required_paragraphs":[0,2,5,-1],"confidence":0.9}]}`
	fr := &fakeListReader{reply: reply}
	p := NewWordlistPipeline(fr.read, 1<<20)
	res, err := p.Recognize(context.Background(), jpegSoi, CategoryChinese)
	if err != nil {
		t.Fatalf("Recognize clamped marks: %v", err)
	}
	marks := res.Passages[0].RequiredParagraphs
	if len(marks) != 1 || marks[0] != 2 {
		t.Errorf("required_paragraphs = %v, want [2] only", marks)
	}
}

// TestWordlistRecognizeTooManyPassages — the passages array obeys its own
// runaway cap (ErrTooManyPassages), distinct from the entries/poems caps.
func TestWordlistRecognizeTooManyPassages(t *testing.T) {
	passages := make([]string, 0, maxWordlistPassages+1)
	for i := 0; i <= maxWordlistPassages; i++ {
		passages = append(passages, fmt.Sprintf(`{"title":"课文%d","paragraphs":["一段"],"confidence":0.9}`, i))
	}
	reply := `{"entries":[],"passages":[` + strings.Join(passages, ",") + `]}`
	fr := &fakeListReader{reply: reply}
	p := NewWordlistPipeline(fr.read, 1<<20)
	if _, err := p.Recognize(context.Background(), jpegSoi, CategoryChinese); !errors.Is(err, ErrTooManyPassages) {
		t.Fatalf("Recognize over-cap passages: err = %v, want ErrTooManyPassages", err)
	}
}
