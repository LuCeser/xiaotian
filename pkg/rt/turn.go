// turn.go — per-connection turn manager (13-04 Task 2).
//
// RED-phase compile stub: the full type surface the behavior tests in
// turn_test.go compile against. Method bodies are deliberately inert so the
// behavior tests fail on their assertions (never on compile or discovery
// errors). The GREEN phase replaces every body.
package rt

import (
	"context"
	"encoding/json"

	"github.com/pkg/errors"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
	"github.com/xiaotian/home-tutor-agent/pkg/mimo"
)

// ErrStaleEpoch marks late output whose launch epoch no longer matches.
var ErrStaleEpoch = errors.New("rt: stale epoch")

// LessonExecutor is the classroom-state seam (13-01 signatures).
type LessonExecutor interface {
	EnsureLesson(ctx context.Context, lessonID string, words []string) (lesson.Lesson, error)
	GetLessonState(ctx context.Context, lessonID string) (lesson.Lesson, error)
	AdvanceWord(ctx context.Context, lessonID string, expectedVersion int, requestID string) (lesson.AdvanceResult, error)
}

// TurnManagerConfig configures one TurnManager.
type TurnManagerConfig struct {
	LessonID       string
	LessonWords    []string
	MaxToolLoops   int
	HistoryMaxTurns int
}

// teacherSystemPrompt is the tutor persona (real content; it is data the
// GREEN phase sends as the system message).
const teacherSystemPrompt = `你是一位亲切、耐心的小学听写家教老师，正在通过语音陪学生完成听写练习。

课堂规则：
1. 课堂的真实进度（单词列表、当前写到第几个词、版本号）只能通过 get_lesson_state 工具读取、通过 advance_word 工具修改。严禁凭记忆或想象口头编造课堂进度。
2. 学生安静沉默是在书写，属于正常情况：耐心等待，不要催促，更不要因为沉默就调用 advance_word 跳词。
3. 学生说“不会写”或求助时，先给一点提示（字形、部首、读音或例句），或者温和地询问是否想跳过这个词；只有学生明确表示“写完了”“跳过吧”“下一个”之后，才调用 advance_word。
4. 调用 advance_word 时，expected_version 必须使用最近一次 get_lesson_state 返回的 version 值。
5. 回应要简短、自然、温暖，适合小学生：写对了就真诚表扬，写错了就耐心鼓励再试一次。严禁任何羞辱、恐吓、惩罚性或让孩子难过的表述。
请始终用简短的中文口语回答。`

// lessonToolDefs declares the two classroom tools (real data).
var lessonToolDefs = []mimo.ToolDef{
	{
		Type: "function",
		Function: mimo.ToolFunctionDef{
			Name:        "get_lesson_state",
			Description: "读取当前课堂状态：单词列表、当前进度（current_index）和版本号（version）。谈论或确认进度前必须先调用。",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"lesson_id": {"type": "string", "description": "课堂 ID"}
				},
				"required": ["lesson_id"]
			}`),
		},
	},
	{
		Type: "function",
		Function: mimo.ToolFunctionDef{
			Name:        "advance_word",
			Description: "推进到下一个单词。仅当学生明确表示写完或确认跳过时才调用；expected_version 必须等于最近一次 get_lesson_state 返回的 version。",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"lesson_id": {"type": "string", "description": "课堂 ID"},
					"expected_version": {"type": "integer", "description": "当前课堂版本号"}
				},
				"required": ["lesson_id", "expected_version"]
			}`),
			Strict: true,
		},
	},
}

// TurnManager owns one connection's session state (RED stub).
type TurnManager struct {
	cfg TurnManagerConfig

	stateMu stateMuStub
}

// stateMuStub is placeholder state plumbing so the stub's FSM hooks share
// the listening flag (GREEN replaces this with the real mutex-guarded
// state).
type stateMuStub struct {
	listening bool
}

// Compile-time SessionManager check.
var _ SessionManager = (*TurnManager)(nil)

// NewTurnManager builds a turn manager (RED stub: always constructs).
func NewTurnManager(asr ASRProvider, llm LLMProvider, tts TTSSynthesizer, exec LessonExecutor, cfg TurnManagerConfig, recorder *EventRecorder, logger infra.Logger) *TurnManager {
	_ = asr
	_ = llm
	_ = tts
	_ = exec
	_ = recorder
	_ = logger
	if cfg.MaxToolLoops <= 0 {
		cfg.MaxToolLoops = 3
	}
	if cfg.HistoryMaxTurns <= 0 {
		cfg.HistoryMaxTurns = 20
	}
	return &TurnManager{cfg: cfg}
}

// HandleControl is inert (RED stub).
func (tm *TurnManager) HandleControl(s Sender, data []byte) {
	_ = s
	_ = data
}

// IngestAudio is inert (RED stub).
func (tm *TurnManager) IngestAudio(s Sender, data []byte) {
	_ = s
	_ = data
}

// Close is inert (RED stub).
func (tm *TurnManager) Close(s Sender) { _ = s }

// processTurn is inert (RED stub).
func (tm *TurnManager) processTurn(s Sender, segment []byte) {
	_ = s
	_ = segment
}

// splitSentences is inert (RED stub).
func splitSentences(text string) []string { return nil }

// validateToolArgs is inert (RED stub).
func validateToolArgs(sessionLessonID, toolName, argsJSON string) (*lessonToolArgs, error) {
	_ = sessionLessonID
	_ = toolName
	_ = argsJSON
	return nil, nil
}

// lessonToolArgs is the strict parse target (RED stub type).
type lessonToolArgs struct {
	LessonID        string `json:"lesson_id"`
	ExpectedVersion *int   `json:"expected_version"`
}
