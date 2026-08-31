package voice

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
)

// Action is a recognized voice-command intent. The four core commands from
// Ticket 03 are ActionNextWord (好了), ActionRepeat (重读), ActionPause
// (暂停) and ActionCannotWrite (不会写); ActionResume rounds out the set so a
// paused student can come back by voice, and ActionNone marks "not a
// command".
type Action string

const (
	ActionNextWord    Action = "next_word"
	ActionRepeat      Action = "repeat"
	ActionPause       Action = "pause"
	ActionResume      Action = "resume"
	ActionCannotWrite Action = "cannot_write"
	ActionNone        Action = "none"
)

// ControlVerb maps an Action onto the session FSM's control verb (the same
// verbs the button controls emit). cannot_write maps to next_word — the
// dictation-teacher behavior: acknowledge, move on, grading happens at review
// time. ActionNone has no verb; ok is false.
func (a Action) ControlVerb() (verb string, ok bool) {
	switch a {
	case ActionNextWord, ActionCannotWrite:
		return "next_word", true
	case ActionRepeat:
		return "repeat", true
	case ActionPause:
		return "pause", true
	case ActionResume:
		return "resume", true
	default:
		return "", false
	}
}

// Intent is a classification result. Source is "rule" (keyword fast path,
// zero LLM latency) or "llm" (natural-language fallback).
type Intent struct {
	Action     Action
	Transcript string
	Confidence float64
	Source     string
}

// Classifier maps a student transcript to a command intent.
type Classifier interface {
	Classify(ctx context.Context, transcript string) (Intent, error)
}

// ruleKeywords lists the keyword tables per action. Tables are checked in
// slice order (pause, resume, repeat, cannot_write, next_word) — a fixed
// priority chosen so no keyword in an earlier table is a substring that could
// shadow a later, more specific match (the tables have no cross-group
// substring collisions; see TestRuleClassifier_PriorityOrder).
var ruleKeywords = []struct {
	action Action
	words  []string
}{
	{ActionPause, []string{
		"暂停", "停一下", "停停", "等等", "等一下", "稍等", "歇一会", "歇一会儿",
		"pause", "wait",
	}},
	{ActionResume, []string{
		"继续", "恢复", "接着写", "接着",
		"resume", "continue", "go on",
	}},
	{ActionRepeat, []string{
		"重读", "再读", "再说", "再念", "重复", "再听", "再来一遍",
		"repeat", "again", "one more",
	}},
	{ActionCannotWrite, []string{
		"不会写", "写不出来", "不会", "忘了怎么写", "想不起来", "不知道怎么写", "太难了",
		"can't write", "dont know", "don't know", "too hard",
	}},
	{ActionNextWord, []string{
		"写好了", "写完了", "写完啦", "好了", "好啦", "完成了", "完了", "下一题", "下一个",
		"done", "next",
	}},
}

// RuleClassifier is the deterministic keyword fast path. It exists because
// the exact commands (好了/重读/暂停) are what children say most of the time,
// and matching them locally costs zero LLM latency on the interactive
// dictation loop. Unmatched text yields ActionNone so the hybrid classifier
// can escalate to the LLM.
type RuleClassifier struct{}

// NewRuleClassifier builds the keyword classifier.
func NewRuleClassifier() *RuleClassifier { return &RuleClassifier{} }

// Classify matches the normalized transcript against the keyword tables.
// Empty transcripts classify as ActionNone (never an error — the caller
// decides what "no match" means).
func (c *RuleClassifier) Classify(_ context.Context, transcript string) (Intent, error) {
	norm := NormalizeTranscript(transcript)
	if norm == "" {
		return Intent{Action: ActionNone, Transcript: transcript, Source: "rule"}, nil
	}
	for _, table := range ruleKeywords {
		for _, kw := range table.words {
			if strings.Contains(norm, NormalizeTranscript(kw)) {
				return Intent{
					Action:     table.action,
					Transcript: transcript,
					Confidence: 1.0,
					Source:     "rule",
				}, nil
			}
		}
	}
	return Intent{Action: ActionNone, Transcript: transcript, Source: "rule"}, nil
}

// NormalizeTranscript strips whitespace and common full/half-width
// punctuation so ASR output like "好了。" or "好 了！" still keyword-matches.
func NormalizeTranscript(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case ' ', '\t', '\n', '\r', '　',
			'，', '。', '！', '？', '、', '；', '：',
			'“', '”', '‘', '’',
			'（', '）', '【', '】', '《', '》',
			',', '.', '!', '?', ';', ':', '"', '\'', '(', ')', '[', ']', '{', '}':
			continue
		}
		b.WriteRune(r)
	}
	return strings.ToLower(b.String())
}

// Chatter is the seam the LLM classifier uses to reach a chat-completion
// API: it renders the fixed system prompt plus the student's transcript and
// returns the raw model reply. cmd/service adapts the mimo client to this
// shape, keeping pkg/voice free of the mimo dependency.
type Chatter func(ctx context.Context, systemPrompt, userText string) (string, error)

// llmSystemPrompt instructs the model to classify a dictation student's
// utterance into one of the command actions. The output contract is strict
// JSON so parseActionJSON can extract it leniently.
const llmSystemPrompt = `你是小学听写课的语音命令识别器。学生正在听写单词，会说出控制指令。
把学生说的话归类为以下动作之一：
- next_word：写完了、好了、下一题（写完当前词，进入下一个）
- repeat：重读、再读一遍、再说一次（重放当前单词）
- pause：暂停、停一下、等一等
- resume：继续、接着写
- cannot_write：不会写、写不出来、不知道怎么写
- none：不是以上任何指令（比如自言自语、朗读单词、闲聊）
学生可能是6-12岁儿童，表达可能不标准，请理解意图而非字面匹配。
只输出一个 JSON 对象，格式：{"action":"next_word","confidence":0.95}
action 必须是上面六个值之一；confidence 是 0 到 1 的数字。不要输出其他内容。`

// LLMClassifier classifies transcripts via an LLM chat call. It handles the
// "varied natural expressions beyond fixed phrases" acceptance item: anything
// the keyword tables miss (e.g. "老师我觉得这个有点难") is the LLM's job.
type LLMClassifier struct {
	chat Chatter
}

// NewLLMClassifier builds the LLM fallback classifier. A nil Chatter yields a
// classifier that always returns ActionNone with an error — wire the hybrid
// classifier without it instead.
func NewLLMClassifier(chat Chatter) *LLMClassifier {
	return &LLMClassifier{chat: chat}
}

// Classify sends the transcript to the LLM and parses the JSON reply. Parse
// failures yield ActionNone plus a non-nil error (informational — callers
// treat ActionNone as "not a command" and only log the error).
func (c *LLMClassifier) Classify(ctx context.Context, transcript string) (Intent, error) {
	if c.chat == nil {
		return Intent{Action: ActionNone, Transcript: transcript, Source: "llm"},
			errNilChatter
	}
	reply, err := c.chat(ctx, llmSystemPrompt, transcript)
	if err != nil {
		return Intent{Action: ActionNone, Transcript: transcript, Source: "llm"}, err
	}
	action, conf, ok := parseActionJSON(reply)
	if !ok {
		return Intent{Action: ActionNone, Transcript: transcript, Source: "llm"},
			errUnparseableLLMReply
	}
	return Intent{Action: action, Transcript: transcript, Confidence: conf, Source: "llm"}, nil
}

// actionPattern extracts the action field even when the model wraps its JSON
// in prose or code fences.
var actionPattern = regexp.MustCompile(`"action"\s*:\s*"([a-z_]+)"`)

// parseActionJSON leniently extracts {"action": "...", "confidence": 0.x}
// from a model reply. It first tries the outermost {...} span as JSON; on
// failure it falls back to a regex for the action field alone. Unknown action
// values map to ActionNone with ok=true (the model answered, just not with a
// known verb — that is a classification result, not a parse failure).
func parseActionJSON(reply string) (Action, float64, bool) {
	start := strings.Index(reply, "{")
	end := strings.LastIndex(reply, "}")
	if start >= 0 && end > start {
		var parsed struct {
			Action     string  `json:"action"`
			Confidence float64 `json:"confidence"`
		}
		if err := json.Unmarshal([]byte(reply[start:end+1]), &parsed); err == nil && parsed.Action != "" {
			return normalizeAction(parsed.Action), clamp01(parsed.Confidence), true
		}
	}
	if m := actionPattern.FindStringSubmatch(reply); m != nil {
		return normalizeAction(m[1]), 0.5, true
	}
	return ActionNone, 0, false
}

// normalizeAction validates an action string against the known set.
func normalizeAction(s string) Action {
	switch Action(s) {
	case ActionNextWord, ActionRepeat, ActionPause, ActionResume, ActionCannotWrite:
		return Action(s)
	default:
		return ActionNone
	}
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

// HybridClassifier runs the keyword fast path first (zero-latency for the
// exact commands) and escalates unmatched transcripts to the LLM. When the
// LLM seam is nil the hybrid degrades to rules-only — the button controls
// remain the full fallback either way.
type HybridClassifier struct {
	rules *RuleClassifier
	llm   *LLMClassifier
}

// NewHybridClassifier builds the production classifier. llm may be nil
// (rules-only mode, e.g. when no MIMO_API_KEY is configured).
func NewHybridClassifier(llm *LLMClassifier) *HybridClassifier {
	return &HybridClassifier{rules: NewRuleClassifier(), llm: llm}
}

// Classify implements Classifier. The LLM's error is returned alongside an
// ActionNone intent — callers use the intent, log the error.
func (h *HybridClassifier) Classify(ctx context.Context, transcript string) (Intent, error) {
	if it, _ := h.rules.Classify(ctx, transcript); it.Action != ActionNone {
		return it, nil
	}
	if h.llm == nil {
		return Intent{Action: ActionNone, Transcript: transcript, Source: "rule"}, nil
	}
	return h.llm.Classify(ctx, transcript)
}
