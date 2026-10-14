package ocr

// meaning.go implements the missing-meaning supplement pipeline (Issue #8):
// when a parent's imported English word list carries entries WITHOUT the
// material's own meaning text, this pipeline asks a text LLM for concise
// Chinese glosses grounded in the CONFIRMED textbook context (edition /
// stage / grade / volume / module plus the learned-lessons gauge) — never
// in a region or grade guess (ADR-0005 #2, US-19).
//
// Like wordlist.go this owns NO state and touches NO database: the result
// is a client-side draft supplement. The preview marks supplemented rows
// (source=generated), the parent may edit / cancel / retry, and only the
// final confirmed meaning reaches the ordinary add path. Words the model
// does not answer come back in Missing — an honest per-word failure, not
// an error (ADR-0005 #5).

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// ChatReader is the seam the meaning generator uses to reach a text LLM
// (no image part — unlike ListReader). cmd/service adapts the mimo
// client's Chat call to this shape, mirroring mimoListReader.
type ChatReader func(ctx context.Context, systemPrompt, userPrompt string) (string, error)

// MeaningTextbook is the CONFIRMED textbook metadata that grounds the
// generation — the values from the preview's textbook panel (an existing
// book's stored fields, or the 新建教材 mini-form's confirmed values).
// Every field is empty-string-legal; the prompt builder reports only what
// is registered and says so honestly when nothing is. There is
// deliberately NO region field (US-19: 不凭地区推定水平).
type MeaningTextbook struct {
	Edition string `json:"edition"`
	Stage   string `json:"stage"`
	Grade   string `json:"grade"`
	Volume  string `json:"volume"`
	Module  string `json:"module"`
}

// MeaningRequest is one supplement call: the words whose material carried
// no meaning plus the confirmed context. LearnedLessons is nil when no
// progress is registered yet (a book created in this very import) — the
// prompt then says 尚未登记学习进度 instead of pretending zero lessons
// were learned.
type MeaningRequest struct {
	Words          []string        `json:"words"`
	Textbook       MeaningTextbook `json:"textbook"`
	LearnedLessons *int            `json:"learnedLessons"`
}

// MeaningResult is the supplement draft: one meaning per answered word
// plus the request-order list of words that got NO usable answer (empty
// or over-long gloss). Missing is a legal, honest outcome — those rows
// stay missing-meaning in the preview and remain submittable as-is.
type MeaningResult struct {
	Meanings map[string]string `json:"meanings"`
	Missing  []string          `json:"missing"`
}

// maxMeaningWords caps one supplement call at the add path's per-request
// entry cap (100, pkg/web/api maxEntries — the same ceiling the word-list
// recognition uses). The HTTP layer validates the cap; this guard keeps
// the pipeline itself un-bounded-input-safe.
const maxMeaningWords = 100

// maxGeneratedMeaningRunes mirrors pkg/web/api's maxMeaningRunes (1000):
// a generated gloss longer than the add path would accept can never be
// committed as-is, so it is reported as missing rather than truncated
// into something the model did not say.
const maxGeneratedMeaningRunes = 1000

// meaningSystemPrompt instructs the model to gloss ONLY the listed
// English words for THIS textbook's student. The clauses map to Issue
// #8's acceptance criteria: concise grade-appropriate Chinese glosses,
// no textbook-original fabrication, no 古诗/课文 generation, every listed
// word answered exactly once (an unusable answer is an empty string,
// which the pipeline turns into Missing — the honest per-word failure).
const meaningSystemPrompt = `你是家长端英语词表导入助手。家长导入的教材英语词表里，有些单词和短语旁边没有印释义，需要你补充。
请只依据给出的教材背景（版本、学段、年级、册、模块）和已学进度，为列表中的每个英语单词或短语给出一个简明的常用中文释义，适合该教材学生的理解水平，格式如「n. 苹果」或「冰激凌」，不要长篇解释。
释义是你的补充，不是教材原文；不要编造教材上的原文释义，不要生成古诗或课文内容。
只输出一个 JSON 数组，格式：[{"word":"apple","meaning":"n. 苹果"}]。列表中的每个词都要出现且只出现一次；确实无法给出释义的词，meaning 填空字符串。不要输出数组以外的内容。`

// BuildMeaningUserPrompt renders the per-call user turn from the confirmed
// context. Exported so the wire handler's tests can pin what the prompt
// carries (AC 自动测试验证提示所用背景); pure.
func BuildMeaningUserPrompt(req MeaningRequest) string {
	var b strings.Builder
	b.WriteString("教材背景：")
	meta := []string{}
	fields := []struct{ label, value string }{
		{"版本", req.Textbook.Edition},
		{"学段", req.Textbook.Stage},
		{"年级", req.Textbook.Grade},
		{"册", req.Textbook.Volume},
		{"模块", req.Textbook.Module},
	}
	for _, f := range fields {
		if v := strings.TrimSpace(f.value); v != "" {
			meta = append(meta, f.label+" "+v)
		}
	}
	if len(meta) > 0 {
		b.WriteString(strings.Join(meta, " · "))
	} else {
		b.WriteString("家长未登记教材版本信息")
	}
	b.WriteString("。\n已学进度：")
	if req.LearnedLessons != nil {
		b.WriteString(fmt.Sprintf("已学完第 %d 课（含第 %d 课）", *req.LearnedLessons, *req.LearnedLessons))
	} else {
		b.WriteString("尚未登记学习进度")
	}
	b.WriteString("。\n待补释义的英语单词：\n")
	for i, w := range req.Words {
		b.WriteString(fmt.Sprintf("%d. %s\n", i+1, w))
	}
	return b.String()
}

// MeaningGenerator supplements missing word-list meanings end-to-end: call
// the text seam with the context-grounded prompts, parse the reply
// leniently, keep only answers for the REQUESTED words. It owns no state;
// one instance serves every request.
type MeaningGenerator struct {
	chat ChatReader
}

// NewMeaningGenerator builds the generator. A nil ChatReader yields a
// generator whose Generate fails with the errNilSeam wiring error (the
// service wires a real adapter only when a key is configured; the HTTP
// layer keeps the endpoint mounted and honest via 503 — ADR-0005 #5).
func NewMeaningGenerator(chat ChatReader) *MeaningGenerator {
	return &MeaningGenerator{chat: chat}
}

// Generate supplements one batch. Error contract mirrors
// WordlistPipeline.Recognize: sentinels detectable via errors.Is. A reply
// that answers none of the requested words is NOT an error — every word
// lands in Missing (the caller shows 生成失败 per row, retryable).
func (g *MeaningGenerator) Generate(ctx context.Context, req MeaningRequest) (*MeaningResult, error) {
	words := normalizeMeaningWords(req.Words)
	if len(words) == 0 {
		return nil, ErrEmptyWords
	}
	if len(words) > maxMeaningWords {
		return nil, ErrTooManyEntries
	}
	if g.chat == nil {
		return nil, errNilSeam
	}

	reply, err := g.chat(ctx, meaningSystemPrompt, BuildMeaningUserPrompt(req))
	if err != nil {
		// Double-%w keeps both the sentinel and the upstream cause reachable
		// (same shape as WordlistPipeline.Recognize).
		return nil, fmt.Errorf("%w: %w", errReaderFailed, err)
	}

	answered, ok := parseMeaningReply(reply)
	if !ok {
		return nil, errUnparseableReply
	}
	out := &MeaningResult{
		Meanings: make(map[string]string, len(words)),
		Missing:  []string{},
	}
	for _, w := range words {
		meaning := strings.TrimSpace(answered[w])
		if meaning == "" || len([]rune(meaning)) > maxGeneratedMeaningRunes {
			out.Missing = append(out.Missing, w)
			continue
		}
		out.Meanings[w] = meaning
	}
	return out, nil
}

// normalizeMeaningWords trims, drops empties and de-duplicates (request
// order preserved) — the words the reply is filtered against.
func normalizeMeaningWords(words []string) []string {
	seen := make(map[string]bool, len(words))
	out := make([]string, 0, len(words))
	for _, w := range words {
		v := strings.TrimSpace(w)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

// parseMeaningReply leniently extracts {word → meaning} pairs from a model
// reply (the wordlistReply parse strategy, minus the textbook header):
//  1. a JSON array of {"word","meaning"} objects — the asked-for shape —
//     via outermost-span then scan-from-every-'[';
//  2. a JSON object whose "meanings" (or "entries") value is either that
//     array or a bare {"word":"meaning"} map — the shapes a chatty model
//     wraps around its answer;
//  3. a bare {"word":"meaning"} map spanning the outermost {...};
//  4. the paired word+meaning regex fallback (wordlistEntryPattern kin).
//
// ok=false means NOTHING parseable — a contract-breaking reply the caller
// reports as a retryable error, distinct from a parsed reply that answers
// none of the requested words (an honest all-missing result). Unlike
// parseWordlistReply there is no entries cap: the caller filters
// everything against its ≤100-word requested set, so the reply cannot
// materialize an unbounded result.
func parseMeaningReply(reply string) (map[string]string, bool) {
	if pairs, ok := parseMeaningArray(reply); ok {
		return pairs, true
	}
	if start, end := strings.Index(reply, "{"), strings.LastIndex(reply, "}"); start >= 0 && end > start {
		var wrapper map[string]json.RawMessage
		if err := json.Unmarshal([]byte(reply[start:end+1]), &wrapper); err == nil {
			for _, key := range []string{"meanings", "entries"} {
				if raw, ok := wrapper[key]; ok {
					if pairs, ok := parseMeaningArray(string(raw)); ok {
						return pairs, true
					}
					var m map[string]string
					if err := json.Unmarshal(raw, &m); err == nil {
						return compactMeaningMap(m), true
					}
				}
			}
		}
		var m map[string]string
		if err := json.Unmarshal([]byte(reply[start:end+1]), &m); err == nil {
			return compactMeaningMap(m), true
		}
	}
	if matches := wordlistEntryPattern.FindAllStringSubmatch(reply, -1); len(matches) > 0 {
		pairs := make(map[string]string, len(matches))
		for _, m := range matches {
			word, werr := strconv.Unquote(`"` + m[1] + `"`)
			meaning, merr := strconv.Unquote(`"` + m[2] + `"`)
			if werr != nil || merr != nil {
				continue
			}
			pairs[strings.TrimSpace(word)] = meaning
		}
		if len(pairs) > 0 {
			return pairs, true
		}
	}
	return nil, false
}

// meaningPairJSON is the array element decode shape.
type meaningPairJSON struct {
	Word    string `json:"word"`
	Meaning string `json:"meaning"`
}

// parseMeaningArray is the asked-for shape: [...] of {"word","meaning"}
// objects, extracted with the outermost-span then scan-from-every-'['
// strategy parseWordlistLegacyArray uses (the span uses json.Unmarshal —
// the whole slice must be the value; the scan uses a Decoder so a reply
// whose array is followed by prose still parses).
func parseMeaningArray(reply string) (map[string]string, bool) {
	build := func(parsed []meaningPairJSON) map[string]string {
		pairs := make(map[string]string, len(parsed))
		for _, p := range parsed {
			pairs[strings.TrimSpace(p.Word)] = p.Meaning
		}
		return pairs
	}
	if start, end := strings.Index(reply, "["), strings.LastIndex(reply, "]"); start >= 0 && end > start {
		var parsed []meaningPairJSON
		if err := json.Unmarshal([]byte(reply[start:end+1]), &parsed); err == nil && parsed != nil {
			return build(parsed), true
		}
	}
	for i := 0; i < len(reply); i++ {
		if reply[i] != '[' {
			continue
		}
		var parsed []meaningPairJSON
		if err := json.NewDecoder(strings.NewReader(reply[i:])).Decode(&parsed); err == nil && parsed != nil {
			return build(parsed), true
		}
	}
	return nil, false
}

// compactMeaningMap trims a decoded {"word":"meaning"} map.
func compactMeaningMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for w, v := range m {
		out[strings.TrimSpace(w)] = v
	}
	return out
}
