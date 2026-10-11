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
// meaning column). Confidence is the model's self-reported certainty for the
// entry, clamped to [0,1]; the parent reviews every row anyway, so nothing
// is gated on it here.
type WordlistEntry struct {
	Word       string  `json:"word"`
	Meaning    string  `json:"meaning"`
	Confidence float64 `json:"confidence"`
}

// wordlistSystemPrompt instructs the model to extract ONLY clear word-list
// entries. The clauses map to Issue #3's acceptance criteria: body text and
// exercise questions must not become entries (无需预先裁图 — the model does
// the cropping semantically), phrases keep their spaces, meanings are
// transcribed verbatim from the photo, and the output is a strict JSON
// array so parseWordlistJSON can extract it leniently.
const wordlistSystemPrompt = `你是家长端英语词表导入助手。家长上传一张英语单词表的照片（PNG 或 JPEG），可能是印刷教材，也可能拍照略有倾斜。
请只提取照片中明确的词表条目：以列表或表格排版、作为单词表出现的单词、短语和它们旁边的原文释义。
不要把普通正文句子里出现的词、练习题、题目编号、页眉页脚或装饰文字当作词条；无需裁图，直接忽略这些内容。
短语中的空格要保留，整个短语算一个条目；释义按照片原文转录（中文或英文都照抄），词表没有给释义就留空。
只输出一个 JSON 数组，格式：[{"word":"ice cream","meaning":"冰激凌","confidence":0.9}]
confidence 是你对这一条识别正确性的把握，0 到 1 的数字。没有识别到任何词表条目时输出 []。不要输出数组以外的内容。`

// wordlistUserPrompt is the per-image user turn (same shape as the student
// pipeline: the system prompt carries the contract, this keeps the vision
// turn's two content parts).
const wordlistUserPrompt = `请提取这张照片里的英语词表条目。`

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
	read      ListReader
	maxBytes  int
	maxInputs int
}

// NewWordlistPipeline builds the pipeline. maxBytes caps one upload (the
// caller passes the photo config's MaxImageBytes); inputs past the cap fail
// with ErrImageTooLarge before any API round trip. A nil ListReader yields
// a pipeline whose Recognize fails with the errNilSeam wiring error.
func NewWordlistPipeline(read ListReader, maxBytes int) *WordlistPipeline {
	return &WordlistPipeline{read: read, maxBytes: maxBytes, maxInputs: maxWordlistEntries}
}

// MaxBytes exposes the pipeline's upload byte cap so the HTTP layer can bound
// its request-body reads with the exact same limit (one source of truth —
// the api handler's MaxBytesReader and the pipeline check can never
// disagree).
func (p *WordlistPipeline) MaxBytes() int {
	return p.maxBytes
}

// Recognize converts one photo's bytes into draft word-list entries. The
// MIME type is sniffed from the magic bytes (never trusting the declared
// Content-Type) and forwarded so the vision API decodes the right format.
// Error contract mirrors Pipeline.Process: sentinels are detectable via
// errors.Is. A photo with no recognizable word-list entries is a legal
// outcome — the caller shows "nothing recognized", not an error.
func (p *WordlistPipeline) Recognize(ctx context.Context, image []byte) ([]WordlistEntry, error) {
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

	entries, ok := parseWordlistJSON(reply)
	if !ok {
		return nil, errUnparseableReply
	}
	if len(entries) > p.maxInputs {
		return nil, ErrTooManyEntries
	}
	out := make([]WordlistEntry, 0, len(entries))
	for _, e := range entries {
		word := strings.TrimSpace(e.Word)
		if word == "" {
			continue
		}
		out = append(out, WordlistEntry{
			Word:       word,
			Meaning:    strings.TrimSpace(e.Meaning),
			Confidence: clamp01(e.Confidence),
		})
	}
	return out, nil
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

// wordlistJSONEntry is one array entry's decode shape. Meaning may be absent
// (a meaning-less list is legal); Confidence then defaults to 0.5 via the
// regex fallback or stays 0 from json.Unmarshal — both display the same way
// because nothing gates on it.
type wordlistJSONEntry struct {
	Word       string  `json:"word"`
	Meaning    string  `json:"meaning"`
	Confidence float64 `json:"confidence"`
}

// wordlistEntryPattern extracts the word field of every array entry when the
// array itself cannot be parsed (order preserved; the entry boundaries come
// from the paired word+meaning regex below when possible).
var wordlistEntryPattern = regexp.MustCompile(`"word"\s*:\s*"((?:[^"\\]|\\.)*)"\s*,\s*"meaning"\s*:\s*"((?:[^"\\]|\\.)*)"`)

// parseWordlistJSON leniently extracts []wordlistJSONEntry from a model
// reply (mirror of parseWordsJSON's most-to-least-trusting strategy):
//  1. the outermost [...] span as one JSON array;
//  2. a decoder scan from every '[' offset — an array buried in
//     thinking-model reasoning or fenced with prose still parses;
//  3. a regex for paired word+meaning fields (meaning may be empty), in
//     order; entries whose meaning key is absent entirely fall back to the
//     word-only pattern.
func parseWordlistJSON(reply string) ([]wordlistJSONEntry, bool) {
	start := strings.Index(reply, "[")
	end := strings.LastIndex(reply, "]")
	if start >= 0 && end > start {
		var parsed []wordlistJSONEntry
		if err := json.Unmarshal([]byte(reply[start:end+1]), &parsed); err == nil && parsed != nil {
			return parsed, true
		}
	}
	for i := 0; i < len(reply); i++ {
		if reply[i] != '[' {
			continue
		}
		var parsed []wordlistJSONEntry
		if err := json.NewDecoder(strings.NewReader(reply[i:])).Decode(&parsed); err == nil && parsed != nil {
			return parsed, true
		}
	}
	if matches := wordlistEntryPattern.FindAllStringSubmatch(reply, -1); len(matches) > 0 {
		entries := make([]wordlistJSONEntry, 0, len(matches))
		for _, m := range matches {
			word, werr := strconv.Unquote(`"` + m[1] + `"`)
			meaning, merr := strconv.Unquote(`"` + m[2] + `"`)
			if werr != nil || merr != nil {
				continue
			}
			entries = append(entries, wordlistJSONEntry{Word: word, Meaning: meaning, Confidence: 0.5})
		}
		if len(entries) > 0 {
			return entries, true
		}
	}
	return nil, false
}
