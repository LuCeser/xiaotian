package ocr

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Reader is the seam the pipeline uses to reach a vision API: it receives the
// fixed system + user prompts and the raw JPEG bytes, and returns the raw
// model reply. cmd/service adapts the mimo client's ChatVision call to this
// shape, keeping pkg/ocr free of the mimo dependency (same pattern as
// pkg/voice's Transcriber/Chatter seams).
type Reader func(ctx context.Context, systemPrompt, userPrompt string, imageJPEG []byte) (string, error)

// Result is one OCR outcome. Text is the transcribed handwriting (trimmed);
// Confidence is the model's self-reported certainty, clamped to [0,1].
type Result struct {
	Text       string
	Confidence float64
}

// ocrSystemPrompt instructs the model to transcribe a dictation student's
// written answer verbatim. The output contract is strict JSON so
// parseOCRJSON can extract it leniently. Verbatim matters: the answer is
// JUDGED against the expected word afterwards, so a "corrected" transcription
// would mask the student's actual mistake. "手写或印刷都算" closes a live-
// observed ambiguity: the model otherwise debated whether neatly-written
// text counted as handwriting and nearly answered empty.
const ocrSystemPrompt = `你是小学听写课的作业识别器。学生把听写答案写在纸上并拍照上传。
请原样转录照片上写着的答案文字（手写或印刷都算）：不要纠正错别字、不要补全、不要翻译、不要加标点说明。
学生可能是6-12岁儿童，笔迹可能不工整，请尽力辨认。
如果照片里没有可辨认的文字，text 为空字符串。
只输出一个 JSON 对象，格式：{"text":"apple","confidence":0.9}
confidence 是你对转录正确性的把握，0 到 1 的数字。不要输出其他内容。`

// ocrUserPrompt is the per-image user turn. The system prompt carries the
// contract; this stays a short fixed instruction so the vision turn has both
// content parts (official image-understanding wire shape).
const ocrUserPrompt = `请转录这张照片里的手写文字。`

// Pipeline reads one photo's handwritten answer end-to-end: validate the
// JPEG, call the vision seam, parse the reply, gate on confidence. The
// handler layer owns chunk buffering (voice.Buffers) and answer judging;
// the pipeline owns the API composition.
type Pipeline struct {
	read          Reader
	minConfidence float64
}

// NewPipeline builds the pipeline. minConfidence is the floor below which
// Process returns ErrLowConfidence alongside the best guess (the client then
// offers manual input prefilled with the guess). A nil Reader yields a
// pipeline whose Process calls fail with the errNilSeam wiring error.
func NewPipeline(read Reader, minConfidence float64) *Pipeline {
	return &Pipeline{read: read, minConfidence: minConfidence}
}

// Process converts one photo's JPEG bytes into an OCR Result. Error contract
// mirrors pkg/voice: sentinel errors are detectable via errors.Is, and
// ErrLowConfidence still carries a usable Result so the caller can prefill
// the manual-input fallback. Empty or non-JPEG input fails fast with no API
// round trip.
func (p *Pipeline) Process(ctx context.Context, imageJPEG []byte) (Result, error) {
	if len(imageJPEG) == 0 {
		return Result{}, ErrEmptyImage
	}
	if !isJPEG(imageJPEG) {
		return Result{}, ErrNotJPEG
	}
	if p.read == nil {
		return Result{}, errNilSeam
	}

	reply, err := p.read(ctx, ocrSystemPrompt, ocrUserPrompt, imageJPEG)
	if err != nil {
		// Double-%w keeps both the sentinel (errors.Is) and the upstream
		// cause (logging) reachable — pkg/errors.Wrap would drop the sentinel.
		return Result{}, fmt.Errorf("%w: %w", errReaderFailed, err)
	}

	text, conf, ok := parseOCRJSON(reply)
	if !ok {
		return Result{}, errUnparseableReply
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return Result{}, ErrEmptyText
	}

	res := Result{Text: text, Confidence: conf}
	if conf < p.minConfidence {
		return res, ErrLowConfidence
	}
	return res, nil
}

// isJPEG checks the SOI signature plus the start of the first marker
// (FF D8 FF). Every well-formed JPEG — JFIF, Exif, raw — carries this prefix;
// PNG/HEIC/WebP uploads do not.
func isJPEG(b []byte) bool {
	return len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF
}

// textPattern extracts the text field even when the model wraps its JSON in
// prose or code fences (the regex fallback when the outermost {...} span is
// not valid JSON).
var textPattern = regexp.MustCompile(`"text"\s*:\s*"((?:[^"\\]|\\.)*)"`)

// parseOCRJSON leniently extracts {"text": "...", "confidence": 0.x} from a
// model reply. Strategy, most-to-least trusting:
//  1. the outermost {...} span as one JSON object;
//  2. a decoder scan from every '{' offset — json.Decoder accepts one value
//     and ignores trailing data, so a JSON object buried in thinking-model
//     reasoning or fenced with prose still parses (live-observed shape);
//  3. a regex for the text field alone (confidence defaults to 0.5, below
//     any sane threshold, so a mangled reply routes to manual input).
func parseOCRJSON(reply string) (string, float64, bool) {
	type ocrJSON struct {
		Text       string  `json:"text"`
		Confidence float64 `json:"confidence"`
	}

	start := strings.Index(reply, "{")
	end := strings.LastIndex(reply, "}")
	if start >= 0 && end > start {
		var parsed ocrJSON
		if err := json.Unmarshal([]byte(reply[start:end+1]), &parsed); err == nil && parsed.Text != "" {
			return parsed.Text, clamp01(parsed.Confidence), true
		}
	}
	for i := 0; i < len(reply); i++ {
		if reply[i] != '{' {
			continue
		}
		var parsed ocrJSON
		if err := json.NewDecoder(strings.NewReader(reply[i:])).Decode(&parsed); err == nil && parsed.Text != "" {
			return parsed.Text, clamp01(parsed.Confidence), true
		}
	}
	if m := textPattern.FindStringSubmatch(reply); m != nil {
		text, err := strconv.Unquote(`"` + m[1] + `"`)
		if err == nil && text != "" {
			return text, 0.5, true
		}
	}
	return "", 0, false
}

func clamp01(f float64) float64 {
	if f < 0 {
		return 0
	}
	if f > 1 {
		return 1
	}
	return f
}
