package ocr

// wordlist.go implements the parent-side word-list photo pipeline (Issue #3):
// a parent photographs an English vocabulary list (PNG or JPEG) and the
// service extracts DRAFT word entries — word plus the photo's own meaning —
// for the parent to review, edit and check before anything is committed.
//
// This is deliberately a separate pipeline from the student-answer
// Pipeline/ProcessWords pair: different input contract (PNG accepted, not
// just JPEG), different output contract (entries with meanings, not
// positional transcriptions), and a different trust model — the student
// answer is JUDGED verbatim so corrections are forbidden, while the word
// list is CURATED by the parent so the model may (and must) classify what
// belongs to the list at all.

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// ListReader is the seam the word-list pipeline uses to reach a vision API.
// It mirrors Reader but carries the sniffed MIME type: the parent upload may
// be a PNG shipped verbatim (small files skip the canvas re-encode), and the
// vision wire format needs the real type. cmd/service adapts the mimo
// client's ChatVision call to this shape.
type ListReader func(ctx context.Context, systemPrompt, userPrompt string, image []byte, mimeType string) (string, error)

// WordlistEntry is one recognized draft entry. Word may be a phrase (spaces
// intact — one dictation entry, never split). Meaning is the photo's OWN
// meaning text transcribed verbatim (possibly empty when the list has no
// meaning column). Lesson is the entry's textbook lesson as READ off the
// photo (Issue #5): the entry's own value when the page shows per-list
// lessons, else the page-level lesson — empty when neither was readable
// (待确认; the parent supplies or corrects it in the preview). Confidence
// is the model's self-reported certainty for the entry, clamped to [0,1];
// the parent reviews every row anyway, so nothing is gated on it here.
type WordlistEntry struct {
	Word       string  `json:"word"`
	Meaning    string  `json:"meaning"`
	Lesson     string  `json:"lesson"`
	Confidence float64 `json:"confidence"`
}

// WordlistTextbook is the textbook header the model could actually READ off
// the photo (Issue #5): 版本/学段/年级/上下册/模块 plus the page-level 课次.
// Every field is empty-string-legal — an unread field stays empty and the
// preview shows 待确认; the pipeline NEVER fabricates a value from the
// grade, the words or the filename (AC "不凭年级或文件名编造教材和课次").
type WordlistTextbook struct {
	Edition string `json:"edition"`
	Stage   string `json:"stage"`
	Grade   string `json:"grade"`
	Volume  string `json:"volume"`
	Module  string `json:"module"`
	Lesson  string `json:"lesson"`
}

// WordlistResult is one recognition's full draft: the textbook header (nil
// when nothing was readable) plus the entries. Draft only — nothing here
// reaches a database until the parent commits.
type WordlistResult struct {
	Textbook *WordlistTextbook `json:"textbook"`
	Entries  []WordlistEntry   `json:"entries"`
}

// wordlistSystemPrompt instructs the model to extract ONLY clear word-list
// entries plus the textbook header that is actually printed on the page.
// The clauses map to Issue #3's acceptance criteria (body text and exercise
// questions must not become entries — 无需预先裁图, the model does the
// cropping semantically; phrases keep their spaces; meanings are verbatim)
// and Issue #5's honesty rule: unread header fields are null, never guessed
// from grade, word difficulty or the filename.
const wordlistSystemPrompt = `你是家长端英语词表导入助手。家长上传一张英语单词表的照片（PNG 或 JPEG），可能是印刷教材，也可能拍照略有倾斜。
请只提取照片中明确的词表条目：以列表或表格排版、作为单词表出现的单词、短语和它们旁边的原文释义。
不要把普通正文句子里出现的词、练习题、题目编号、页眉页脚或装饰文字当作词条；无需裁图，直接忽略这些内容。
短语中的空格要保留，整个短语算一个条目；释义按照片原文转录（中文或英文都照抄），词表没有给释义就留空。
同时读取照片上明确印出的教材信息：版本（出版社或教材系列）、学段、年级、上下册、单元或模块、课次（如 Unit 3、第 5 课、Lesson 2）。
教材信息只能报告照片上真实印着的内容；任何一项识别不出或照片上没有就填 null，不要根据单词难度、年级或文件名推测教材和课次。
只输出一个 JSON 对象，格式：{"textbook":{"edition":"人教版PEP","stage":null,"grade":"三年级","volume":"上册","module":"Unit 3","lesson":"第5课"},"entries":[{"word":"ice cream","meaning":"冰激凌","lesson":"第5课","confidence":0.9}]}
整个教材信息都识别不出时 textbook 为 null；entries 里每条的 lesson 能从照片确定就填，不能确定填 null。
confidence 是你对这一条识别正确性的把握，0 到 1 的数字。没有识别到任何词表条目时 entries 为 []。不要输出对象以外的内容。`

// wordlistUserPrompt is the per-image user turn (same shape as the student
// pipeline: the system prompt carries the contract, this keeps the vision
// turn's two content parts).
const wordlistUserPrompt = `请提取这张照片里的英语词表条目和教材信息。`

// maxWordlistEntries caps one recognition at the add path's per-request
// entry cap (100, pkg/web/api maxEntries): a photo whose recognized list
// exceeds it cannot be committed in one request anyway, and a runaway model
// reply must not materialize an unbounded slice.
const maxWordlistEntries = 100

// WordlistPipeline reads one word-list photo end-to-end: sniff PNG/JPEG,
// enforce the byte cap, call the vision seam, parse the reply into draft
// entries. It owns NO state — recognition results are a client-side draft
// until the parent explicitly commits them through the ordinary add path
// (nothing here touches any database).
type WordlistPipeline struct {
	read     ListReader
	maxBytes int
}

// NewWordlistPipeline builds the pipeline. maxBytes caps one upload (the
// caller passes the photo config's MaxImageBytes); inputs past the cap fail
// with ErrImageTooLarge before any API round trip. A nil ListReader yields
// a pipeline whose Recognize fails with the errNilSeam wiring error.
func NewWordlistPipeline(read ListReader, maxBytes int) *WordlistPipeline {
	return &WordlistPipeline{read: read, maxBytes: maxBytes}
}

// MaxBytes exposes the pipeline's upload byte cap so the HTTP layer can bound
// its request-body reads with the exact same limit (one source of truth —
// the api handler's MaxBytesReader and the pipeline check can never
// disagree).
func (p *WordlistPipeline) MaxBytes() int {
	return p.maxBytes
}

// Recognize converts one photo's bytes into the draft recognition result
// (textbook header + entries). The MIME type is sniffed from the magic
// bytes (never trusting the declared Content-Type) and forwarded so the
// vision API decodes the right format. Error contract mirrors
// Pipeline.Process: sentinels are detectable via errors.Is. A photo with
// no recognizable word-list entries is a legal outcome — the caller shows
// "nothing recognized", not an error.
func (p *WordlistPipeline) Recognize(ctx context.Context, image []byte) (*WordlistResult, error) {
	if len(image) == 0 {
		return nil, ErrEmptyImage
	}
	mimeType, ok := sniffImage(image)
	if !ok {
		return nil, ErrNotImage
	}
	if p.maxBytes > 0 && len(image) > p.maxBytes {
		return nil, ErrImageTooLarge
	}
	if p.read == nil {
		return nil, errNilSeam
	}

	reply, err := p.read(ctx, wordlistSystemPrompt, wordlistUserPrompt, image, mimeType)
	if err != nil {
		// Double-%w keeps both the sentinel and the upstream cause reachable
		// (same shape as Pipeline.Process).
		return nil, fmt.Errorf("%w: %w", errReaderFailed, err)
	}

	parsed, ok := parseWordlistReply(reply)
	if !ok {
		return nil, errUnparseableReply
	}
	if len(parsed.entries) > maxWordlistEntries {
		return nil, ErrTooManyEntries
	}

	var meta *WordlistTextbook
	if parsed.textbook != nil {
		trimmed := WordlistTextbook{
			Edition: strings.TrimSpace(parsed.textbook.Edition),
			Stage:   strings.TrimSpace(parsed.textbook.Stage),
			Grade:   strings.TrimSpace(parsed.textbook.Grade),
			Volume:  strings.TrimSpace(parsed.textbook.Volume),
			Module:  strings.TrimSpace(parsed.textbook.Module),
			Lesson:  strings.TrimSpace(parsed.textbook.Lesson),
		}
		if trimmed != (WordlistTextbook{}) {
			meta = &trimmed
		}
	}

	out := make([]WordlistEntry, 0, len(parsed.entries))
	for _, e := range parsed.entries {
		word := strings.TrimSpace(e.Word)
		if word == "" {
			continue
		}
		lesson := strings.TrimSpace(e.Lesson)
		if lesson == "" && meta != nil {
			// No own lesson → the page-level lesson governs (one photo is
			// usually one lesson's list; the parent can still re-assign per
			// row in the preview).
			lesson = meta.Lesson
		}
		out = append(out, WordlistEntry{
			Word:       word,
			Meaning:    strings.TrimSpace(e.Meaning),
			Lesson:     lesson,
			Confidence: clamp01(e.Confidence),
		})
	}
	return &WordlistResult{Textbook: meta, Entries: out}, nil
}

// sniffImage detects PNG and JPEG from their magic bytes and reports the
// MIME type the vision API expects. Everything else is rejected (ErrNotImage
// upstream): WebP/HEIC/PDF uploads fail fast with a clear reason instead of
// an opaque upstream decode error.
func sniffImage(b []byte) (string, bool) {
	if len(b) >= 8 && b[0] == 0x89 && b[1] == 'P' && b[2] == 'N' && b[3] == 'G' &&
		b[4] == 0x0D && b[5] == 0x0A && b[6] == 0x1A && b[7] == 0x0A {
		return "image/png", true
	}
	if isJPEG(b) {
		return "image/jpeg", true
	}
	return "", false
}

// wordlistReplyJSON is the whole Issue #5 object reply's decode shape.
// It decodes straight into the exported WordlistTextbook/WordlistEntry —
// field-for-field the same wire contract, so one struct per concept. A
// null field and an absent field both decode to "" (the honesty contract
// treats them identically, 待确认), and Textbook is a pointer so a null
// textbook stays distinguishable from an all-empty one at parse time
// (Recognize collapses both to meta == nil).
type wordlistReplyJSON struct {
	Textbook *WordlistTextbook `json:"textbook"`
	Entries  []WordlistEntry   `json:"entries"`
}

// wordlistEntryPattern extracts the word field of every array entry when the
// array itself cannot be parsed (order preserved; the entry boundaries come
// from the paired word+meaning regex below when possible).
var wordlistEntryPattern = regexp.MustCompile(`"word"\s*:\s*"((?:[^"\\]|\\.)*)"\s*,\s*"meaning"\s*:\s*"((?:[^"\\]|\\.)*)"`)

// parsedWordlist is parseWordlistReply's output — decoded but untrimmed.
type parsedWordlist struct {
	textbook *WordlistTextbook
	entries  []WordlistEntry
}

// parseWordlistReply leniently extracts the recognition result from a model
// reply (mirror of parseWordsJSON's most-to-least-trusting strategy):
//  1. the Issue #5 object contract: the outermost {...} span (or any {...}
//     from a decoder scan — thinking-model prose or fences still parse) as
//     {"textbook":…,"entries":[…]}; a textbook-less object is legal;
//  2. the Issue #3 legacy contract: [...] as one JSON array (no meta);
//  3. a regex for paired word+meaning fields (meaning may be empty), in
//     order — the degraded object-less fallback.
func parseWordlistReply(reply string) (parsedWordlist, bool) {
	if start, end := strings.Index(reply, "{"), strings.LastIndex(reply, "}"); start >= 0 && end > start {
		var parsed wordlistReplyJSON
		if err := json.Unmarshal([]byte(reply[start:end+1]), &parsed); err == nil &&
			(parsed.Entries != nil || parsed.Textbook != nil) {
			return parsedWordlist{textbook: parsed.Textbook, entries: parsed.Entries}, true
		}
	}
	for i := 0; i < len(reply); i++ {
		if reply[i] != '{' {
			continue
		}
		var parsed wordlistReplyJSON
		if err := json.NewDecoder(strings.NewReader(reply[i:])).Decode(&parsed); err == nil &&
			(parsed.Entries != nil || parsed.Textbook != nil) {
			return parsedWordlist{textbook: parsed.Textbook, entries: parsed.Entries}, true
		}
	}
	if entries, ok := parseWordlistLegacyArray(reply); ok {
		return parsedWordlist{entries: entries}, true
	}
	if matches := wordlistEntryPattern.FindAllStringSubmatch(reply, -1); len(matches) > 0 {
		entries := make([]WordlistEntry, 0, len(matches))
		for _, m := range matches {
			word, werr := strconv.Unquote(`"` + m[1] + `"`)
			meaning, merr := strconv.Unquote(`"` + m[2] + `"`)
			if werr != nil || merr != nil {
				continue
			}
			entries = append(entries, WordlistEntry{Word: word, Meaning: meaning, Confidence: 0.5})
		}
		if len(entries) > 0 {
			return parsedWordlist{entries: entries}, true
		}
	}
	return parsedWordlist{}, false
}

// parseWordlistLegacyArray is the Issue #3 contract: a bare [...] array of
// entries, extracted with the same two-span then scan-from-every-'['
// strategy the original parseWordlistJSON used.
func parseWordlistLegacyArray(reply string) ([]WordlistEntry, bool) {
	start := strings.Index(reply, "[")
	end := strings.LastIndex(reply, "]")
	if start >= 0 && end > start {
		var parsed []WordlistEntry
		if err := json.Unmarshal([]byte(reply[start:end+1]), &parsed); err == nil && parsed != nil {
			return parsed, true
		}
	}
	for i := 0; i < len(reply); i++ {
		if reply[i] != '[' {
			continue
		}
		var parsed []WordlistEntry
		if err := json.NewDecoder(strings.NewReader(reply[i:])).Decode(&parsed); err == nil && parsed != nil {
			return parsed, true
		}
	}
	return nil, false
}
