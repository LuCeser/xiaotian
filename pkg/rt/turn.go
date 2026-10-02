// turn.go — the per-connection turn manager (13-04 Task 2): the single
// owner of session state on top of the /rt transport.
//
// Responsibilities (CONTEXT locked decisions, all landed here):
//   - Turn FSM: listening → processing → speaking → listening.
//   - Epoch cancellation: a monotonic counter; interrupt bumps it, cancels
//     the in-flight provider context, and every late TTS chunk / tool-loop
//     hop that observes a stale epoch is discarded (Pitfall 4). Cancelling
//     speech NEVER rolls back an already-committed advance_word.
//   - MiMo tool loop: execute tools FIRST, feed results back as role:"tool",
//     and only then speak the final confirmation (先执行后播报).
//   - Sentence-streamed TTS: subtitles first, audio chunks forwarded the
//     moment they arrive (收即播). Audio bytes crossing this layer are
//     already bare s16le PCM (AudioChunkFunc); the MiMo wire's encoded-chunk
//     decoding belongs to the 13-06 MimoTTS adapter alone (review M-1).
//   - History trimmed to what the student actually heard (Pitfall 9),
//     never touching the turn's tool_calls/role:"tool" records (review M-2).
//   - Half-duplex gating: uplink audio is dropped while the teacher speaks
//     (Pattern 3 — echo countermeasure); interrupts travel the client
//     control channel only.
package rt

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/pkg/errors"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
	"github.com/xiaotian/home-tutor-agent/pkg/mimo"
)

// ErrStaleEpoch is returned by TTS chunk callbacks (and checked at every
// tool-loop hop) when the epoch they were launched under no longer matches
// the session's current epoch — the caller discards the late output instead
// of playing it (Pitfall 4: old audio must not resurrect).
var ErrStaleEpoch = errors.New("rt: stale epoch")

// LessonExecutor is the classroom-state seam the turn manager depends on
// (13-01). Signatures align verbatim with pkg/lesson's methods so 13-06 can
// bind them with a compile-time check; EnsureLesson lives on *lesson.Store
// and the read/write pair on *lesson.Executor, so the production
// implementation is a small adapter over both.
type LessonExecutor interface {
	// EnsureLesson creates the lesson row if absent and returns it (first
	// stored content wins).
	EnsureLesson(ctx context.Context, lessonID string, words []string) (lesson.Lesson, error)
	// GetLessonState returns the current lesson row (get_lesson_state tool).
	GetLessonState(ctx context.Context, lessonID string) (lesson.Lesson, error)
	// AdvanceWord moves the cursor forward one word, idempotently by
	// requestID (the MiMo tool_call id), guarded by expectedVersion.
	AdvanceWord(ctx context.Context, lessonID string, expectedVersion int, requestID string) (lesson.AdvanceResult, error)
}

// TurnManagerConfig configures one TurnManager.
type TurnManagerConfig struct {
	// LessonID is the server-owned lesson this session is bound to. Tool
	// arguments carrying any other lesson_id are rejected (M-5 / RESEARCH
	// V4: neither client nor model may self-report a foreign lesson id).
	LessonID string
	// LessonWords is the R0 fixed word list (3 words) for EnsureLesson.
	LessonWords []string
	// MaxToolLoops bounds Chat hops per turn (default 3) so tool-loop churn
	// cannot stall the latency budget.
	MaxToolLoops int
	// HistoryMaxTurns bounds the LLM context (default 20 turns); the system
	// message is always kept.
	HistoryMaxTurns int
}

// teacherSystemPrompt is the tutor persona plus the classroom rules CONTEXT
// locked:
//   - progress is ONLY readable/writable through the tools — never narrated
//     from imagination (口头编造进度禁止);
//   - ordinary silence is writing time — no auto-advancing on quiet;
//   - “不会写” gets a hint or a clarifying question first; advance_word only
//     after the student explicitly confirms;
//   - encouraging wording only — humiliation, threats, or punitive phrasing
//     toward elementary-school children are forbidden (prohibition).
const teacherSystemPrompt = `你是一位亲切、耐心的小学听写家教老师，正在通过语音陪学生完成听写练习。

课堂规则：
1. 课堂的真实进度（单词列表、当前写到第几个词、版本号）只能通过 get_lesson_state 工具读取、通过 advance_word 工具修改。严禁凭记忆或想象口头编造课堂进度。
2. 学生安静沉默是在书写，属于正常情况：耐心等待，不要催促，更不要因为沉默就调用 advance_word 跳词。
3. 学生说“不会写”或求助时，先给一点提示（字形、部首、读音或例句），或者温和地询问是否想跳过这个词；只有学生明确表示“写完了”“跳过吧”“下一个”之后，才调用 advance_word。
4. 调用 advance_word 时，expected_version 必须使用最近一次 get_lesson_state 返回的 version 值。
5. 回应要简短、自然、温暖，适合小学生：写对了就真诚表扬，写错了就耐心鼓励再试一次。严禁任何羞辱、恐吓、惩罚性或让孩子难过的表述。
请始终用简短的中文口语回答。`

// lessonToolDefs declares the two classroom tools. request_id is deliberately
// NOT a tool parameter: the server takes it from the MiMo tool_call id
// (request_id=tool_call id 约定, 13-CONTEXT) — the model cannot invent or
// reuse idempotency keys.
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

// sessionCounter disambiguates session ids within one process.
var sessionCounter atomic.Uint64

// sentenceInfo is one spoken sentence of the current turn, kept so an
// interrupt can trim history to the sentences actually played (Pitfall 9).
type sentenceInfo struct {
	id   string
	text string
}

// TurnManager implements SessionManager for one /rt connection.
//
// Concurrency: HandleControl/IngestAudio run on the transport read loop;
// processTurn (and its TTS callbacks) run on separate goroutines. mu guards
// every field below. The VAD is only touched from the read loop (IngestAudio
// feeds it, interrupt Resets it) and needs no lock. Provider calls
// (ASR/LLM/TTS) always happen OUTSIDE mu; their results re-acquire it.
// SendControl/SendAudio are non-blocking queue enqueues, so calling them
// under mu is safe and makes "epoch check + send" atomic with respect to
// interrupt handling — after HandleControl(interrupt) returns, no further
// chunk or subtitle of the interrupted turn can be emitted.
type TurnManager struct {
	asr      ASRProvider
	llm      LLMProvider
	tts      TTSSynthesizer
	exec     LessonExecutor
	cfg      TurnManagerConfig
	recorder *EventRecorder
	logger   infra.Logger
	session  string

	mu sync.Mutex
	// state is one of StateListening/StateProcessing/StateSpeaking, or ""
	// before session_init / after close.
	state string
	// closed suppresses duplicate session_end emissions when the transport
	// calls Close after an end_session already closed the session.
	closed bool
	// epoch is the monotonic cancellation token. In-flight provider work
	// remembers the epoch it launched under; on mismatch it aborts.
	epoch uint64
	// turnCancel cancels the current turn's provider context.
	turnCancel context.CancelFunc
	// turnID counts turns (TurnStartMsg/TranscriptMsg carry it).
	turnID uint64
	// history is the LLM conversation; history[0] is the system prompt.
	history []mimo.Message
	// spokenMsgIdx is the history index of the CURRENT turn's spoken-text
	// assistant message (the interrupt trim target), or -1. It is only
	// non-negative while the turn is speaking; nothing appends history
	// between the append and the interrupt, so trimming can never shift it.
	spokenMsgIdx int
	// turnSentences are the sentences sent for the current turn, in order.
	turnSentences []sentenceInfo
	// acked marks sentence ids the client confirmed playing.
	acked map[string]bool
	// turnWrote/turnWroteVersion record whether this turn committed a
	// lesson write (for the lesson_version event at natural completion).
	turnWrote        bool
	turnWroteVersion int
	// droppedUplink counts audio chunks dropped by half-duplex gating.
	droppedUplink int

	// vad is fed from the read loop only (see concurrency note).
	vad *VAD
}

// Compile-time check: TurnManager satisfies the transport's SessionManager
// contract (13-03 key_link).
var _ SessionManager = (*TurnManager)(nil)

// NewTurnManager builds a turn manager. A nil asr/llm/tts still constructs —
// the session then answers session_init with ErrCodeVoiceUnavailable (RT-10
// degraded semantics: no MIMO_API_KEY configured server-side; 13-06 uses
// this). A nil logger becomes the no-op logger and a nil recorder a
// no-session recorder (D-42).
func NewTurnManager(asr ASRProvider, llm LLMProvider, tts TTSSynthesizer, exec LessonExecutor, cfg TurnManagerConfig, recorder *EventRecorder, logger infra.Logger) *TurnManager {
	if cfg.MaxToolLoops <= 0 {
		cfg.MaxToolLoops = 3
	}
	if cfg.HistoryMaxTurns <= 0 {
		cfg.HistoryMaxTurns = 20
	}
	if logger == nil {
		logger = infra.NewNoOpLogger()
	}
	if recorder == nil {
		recorder = NewEventRecorder(logger, "")
	}
	return &TurnManager{
		asr:          asr,
		llm:          llm,
		tts:          tts,
		exec:         exec,
		cfg:          cfg,
		recorder:     recorder,
		logger:       logger,
		session:      fmt.Sprintf("rt-%d", sessionCounter.Add(1)),
		spokenMsgIdx: -1,
		acked:        map[string]bool{},
		vad:          NewVAD(DefaultVADConfig()),
		history:      []mimo.Message{{Role: "system", Content: teacherSystemPrompt}},
	}
}

// ---------------------------------------------------------------------------
// SessionManager: control path
// ---------------------------------------------------------------------------

// HandleControl dispatches one JSON control payload (the transport hands raw
// TextMessage bodies; parse failures answer a coded ErrorMsg).
func (tm *TurnManager) HandleControl(s Sender, data []byte) {
	msg, err := ParseControl(data)
	if err != nil {
		tm.sendError(s, ErrCodeInvalidMessage, "无法识别的控制消息", true)
		return
	}
	switch m := msg.(type) {
	case SessionInitMsg:
		tm.handleSessionInit(s, m)
	case InterruptMsg:
		tm.handleInterrupt(s, m)
	case SentenceAckMsg:
		tm.handleSentenceAck(m)
	case PingMsg:
		tm.handlePing(s, m)
	case EndSessionMsg:
		tm.handleEndSession(s)
	default:
		// Server→client types parse but are not accepted from a client;
		// the transport only moves bytes — this is the enforcement point.
		tm.sendError(s, ErrCodeInvalidMessage, "该消息类型不能由客户端发送", true)
	}
}

// handleSessionInit validates the negotiated audio format, binds the lesson,
// and answers session_ready plus the initial listening state.
func (tm *TurnManager) handleSessionInit(s Sender, m SessionInitMsg) {
	if err := ValidateAudioInit(m.Audio); err != nil {
		tm.sendError(s, ErrCodeUnsupportedFormat, "仅支持 16000Hz、s16le、单声道的上行音频", true)
		return
	}
	if tm.asr == nil || tm.llm == nil || tm.tts == nil {
		// RT-10 degraded semantics: the voice path is disabled server-side.
		tm.sendError(s, ErrCodeVoiceUnavailable, "服务器未配置语音服务密钥，语音模式不可用", false)
		return
	}
	if tm.exec == nil {
		tm.sendError(s, ErrCodeProviderFailed, "服务器课堂模块未就绪", false)
		return
	}

	l, err := tm.exec.EnsureLesson(context.Background(), tm.cfg.LessonID, tm.cfg.LessonWords)
	if err != nil {
		tm.sendError(s, ErrCodeProviderFailed, "课堂初始化失败，请稍后重试", true)
		return
	}

	tm.mu.Lock()
	tm.state = StateListening
	tm.mu.Unlock()

	_ = s.SendControl(SessionReadyMsg{
		Type:      TypeSessionReady,
		SessionID: tm.session,
		Audio:     AudioSpec{SampleRate: requiredSampleRate, Format: requiredFormat, Channels: requiredChannels},
		TTS:       TTSSpec{SampleRate: TTSOutputSampleRate},
		Lesson:    LessonView{LessonID: l.LessonID, Version: l.Version, CurrentIndex: l.CurrentIndex, Words: l.Words},
	})
	tm.recorder.Emit(EventConnected, infra.F("session_id", tm.session))
	_ = s.SendControl(StateMsg{Type: TypeState, Value: StateListening})
}

// handleInterrupt is the epoch bump + provider cancellation + history trim
// (M-2 unified semantics). The trim targets ONLY the current turn's
// spoken-text message — the turn's assistant(tool_calls)/role:"tool" records
// are executed facts and are never touched; when the tool loop is still in
// flight (processing) there is no spoken text yet and nothing is trimmed.
func (tm *TurnManager) handleInterrupt(s Sender, m InterruptMsg) {
	tm.mu.Lock()
	oldEpoch := tm.epoch
	tm.epoch++
	newEpoch := tm.epoch
	if tm.turnCancel != nil {
		tm.turnCancel()
	}

	if tm.state == StateSpeaking && tm.spokenMsgIdx >= 0 {
		// Played = this turn's acked sentences ∪ the interrupt's
		// last_sentence_id (Pitfall 9: the next turn must only know what
		// the student actually heard).
		played := make(map[string]bool, len(tm.acked)+1)
		for id := range tm.acked {
			played[id] = true
		}
		if m.LastSentenceID != "" {
			played[m.LastSentenceID] = true
		}
		var kept []string
		for _, si := range tm.turnSentences {
			if played[si.id] {
				kept = append(kept, si.text)
			}
		}
		if len(kept) == 0 {
			// Nothing was actually heard: the spoken-text message leaves
			// history entirely (the teacher "never said it").
			tm.history = append(tm.history[:tm.spokenMsgIdx], tm.history[tm.spokenMsgIdx+1:]...)
		} else {
			tm.history[tm.spokenMsgIdx].Content = strings.Join(kept, "")
		}
	}
	// Per-turn bookkeeping ends here either way.
	tm.spokenMsgIdx = -1
	tm.turnSentences = nil
	tm.acked = map[string]bool{}

	if tm.state != "" && !tm.closed {
		tm.state = StateListening
	}
	tm.mu.Unlock()

	tm.vad.Reset()

	tm.recorder.Emit(EventInterruptHandled,
		infra.F("old_epoch", oldEpoch),
		infra.F("new_epoch", newEpoch),
		infra.F("last_sentence_id", m.LastSentenceID),
		infra.F("played_until_ms", m.PlayedUntilMs),
	)
	_ = s.SendControl(InterruptAckMsg{
		Type:           TypeInterruptAck,
		OldEpoch:       oldEpoch,
		NewEpoch:       newEpoch,
		LastSentenceID: m.LastSentenceID,
		PlayedUntilMs:  m.PlayedUntilMs,
	})
	_ = s.SendControl(StateMsg{Type: TypeState, Value: StateListening})
}

// handleSentenceAck records one sentence the client actually played — the
// ground truth for history trimming and RT-04 measurement.
func (tm *TurnManager) handleSentenceAck(m SentenceAckMsg) {
	tm.mu.Lock()
	tm.acked[m.SentenceID] = true
	tm.mu.Unlock()
}

// handlePing answers the clock-offset probe. TServerMonoNs shares the
// recorder's anchor with every rt_event t_mono_ns (13-03 protocol).
func (tm *TurnManager) handlePing(s Sender, m PingMsg) {
	_ = s.SendControl(PongMsg{
		Type:          TypePong,
		TClientMs:     m.TClientMs,
		TServerMonoNs: tm.recorder.MonoNs(),
	})
}

// handleEndSession is the explicit goodbye (RT-10/SC7 server half).
func (tm *TurnManager) handleEndSession(s Sender) {
	_ = s.SendControl(SessionEndedMsg{Type: TypeSessionEnded, Reason: "client_end"})
	tm.recorder.Emit(EventSessionEnd, infra.F("session_id", tm.session))
	tm.internalClose()
}

// internalClose cancels in-flight work and resets the FSM without emitting
// the session_end event again.
func (tm *TurnManager) internalClose() {
	tm.mu.Lock()
	if tm.turnCancel != nil {
		tm.turnCancel()
	}
	tm.state = ""
	tm.closed = true
	tm.mu.Unlock()
	tm.vad.Reset()
}

// Close implements SessionManager: the transport read loop exited for any
// reason (disconnect, deadline, read error). Idempotent — an end_session
// path already closed the session.
func (tm *TurnManager) Close(Sender) {
	tm.mu.Lock()
	already := tm.closed
	tm.mu.Unlock()
	if already {
		return
	}
	tm.recorder.Emit(EventSessionEnd, infra.F("session_id", tm.session))
	tm.internalClose()
}

// ---------------------------------------------------------------------------
// SessionManager: audio path
// ---------------------------------------------------------------------------

// IngestAudio is the half-duplex gate (Pattern 3): while the teacher speaks
// (or a turn is processing), uplink audio is dropped and counted — the
// server VAD must not segment the speaker's own echo. In listening state
// the VAD segments the stream; each closed utterance starts one turn
// goroutine.
func (tm *TurnManager) IngestAudio(s Sender, data []byte) {
	tm.mu.Lock()
	state := tm.state
	tm.mu.Unlock()
	if state != StateListening {
		tm.mu.Lock()
		tm.droppedUplink++
		tm.mu.Unlock()
		return
	}
	segment, closed := tm.vad.Feed(data)
	if !closed {
		return
	}
	// Utterances shorter than two frames (60 ms) are nose breaths, not
	// speech — do not spend an ASR call on them.
	if len(segment) < 2*960 {
		return
	}
	go tm.processTurn(s, segment)
}

// processTurn runs one utterance → ASR → tool loop → speech cycle on its
// own goroutine (IngestAudio launches it). The epoch captured at entry
// governs every provider call and the final speech; any mismatch aborts the
// whole turn without issuing further Chat/TTS calls.
func (tm *TurnManager) processTurn(s Sender, segment []byte) {
	tm.mu.Lock()
	if tm.state != StateListening {
		tm.mu.Unlock()
		return
	}
	epoch := tm.epoch
	tm.turnID++
	turnID := tm.turnID
	tm.state = StateProcessing
	ctx, cancel := context.WithCancel(context.Background())
	tm.turnCancel = cancel
	// Fresh per-turn bookkeeping.
	tm.acked = map[string]bool{}
	tm.turnSentences = nil
	tm.spokenMsgIdx = -1
	tm.turnWrote = false
	tm.turnWroteVersion = 0
	tm.mu.Unlock()
	defer cancel()

	tm.recorder.Emit(EventSpeechStarted, infra.F("turn_id", turnID))
	tm.recorder.Emit(EventSpeechEnded, infra.F("turn_id", turnID))
	_ = s.SendControl(StateMsg{Type: TypeState, Value: StateProcessing})
	_ = s.SendControl(TurnStartMsg{Type: TypeTurnStart, TurnID: turnID, Epoch: epoch})

	transcript, err := tm.asr.Transcribe(ctx, segment)
	if err != nil {
		tm.failTurn(s, epoch, ErrCodeProviderFailed, "语音识别失败，请再对我说一遍")
		return
	}
	if transcript != "" {
		_ = s.SendControl(TranscriptMsg{Type: TypeTranscript, TurnID: turnID, Text: transcript})
		tm.appendHistory(mimo.Message{Role: "user", Content: transcript})
	}

	// Tool loop: execute first, speak the confirmation after (先执行后播报).
	finalText := ""
	for hop := 0; hop < tm.cfg.MaxToolLoops; hop++ {
		if !tm.epochCurrent(epoch) {
			return // interrupted: abort the whole turn silently
		}
		resp, err := tm.llm.Chat(ctx, tm.historySnapshot(),
			mimo.WithTools(lessonToolDefs), mimo.WithThinkingDisabled())
		if err != nil {
			tm.failTurn(s, epoch, ErrCodeProviderFailed, "老师思考时出了点问题，请再说一遍")
			return
		}
		if !resp.ToolCallsRequested() {
			finalText = resp.Text()
			break
		}
		toolCalls := resp.FirstToolCalls()

		// Replay the assistant's tool request verbatim, then answer each
		// call with a role:"tool" result (Pattern 4 wire shape).
		var content string
		if len(resp.Choices) > 0 {
			content = resp.Choices[0].Message.Content
		}
		tm.appendHistory(mimo.Message{Role: "assistant", Content: content, ToolCalls: toolCalls})
		for _, tc := range toolCalls {
			tm.recorder.Emit(EventToolStart,
				infra.F("turn_id", turnID),
				infra.F("tool", tc.Function.Name),
				infra.F("request_id", tc.ID),
			)
			result := tm.executeToolCall(s, ctx, tc, turnID)
			tm.recorder.Emit(EventToolEnd,
				infra.F("turn_id", turnID),
				infra.F("tool", tc.Function.Name),
				infra.F("request_id", tc.ID),
			)
			tm.appendHistory(mimo.Message{Role: "tool", ToolCallID: tc.ID, Content: result})
		}
		// Loop continues: the next Chat hop sees the tool results.
	}
	if finalText == "" {
		// Tool budget exhausted without a final answer.
		tm.failTurn(s, epoch, ErrCodeProviderFailed, "老师想得太久了，我们再试一次好吗")
		return
	}

	tm.speakTurn(s, finalText, turnID, epoch, ctx, cancel)
}

// executeToolCall validates and runs ONE tool call, returning the JSON tool
// result that is fed back to the model. Validation failures and version
// conflicts are tool RESULTS (the model gets a chance to react); invalid
// input never reaches the executor (T-13-03 / T-13-05b).
func (tm *TurnManager) executeToolCall(s Sender, ctx context.Context, tc mimo.ToolCall, turnID uint64) string {
	args, err := validateToolArgs(tm.cfg.LessonID, tc.Function.Name, tc.Function.Arguments)
	if err != nil {
		return toolErrorResult("invalid_arguments", err.Error())
	}

	switch tc.Function.Name {
	case "get_lesson_state":
		l, err := tm.exec.GetLessonState(ctx, args.LessonID)
		if err != nil {
			tm.sendError(s, ErrCodeProviderFailed, "读取课堂进度失败", true)
			return toolErrorResult("tool_failed", "get_lesson_state failed")
		}
		payload, _ := json.Marshal(map[string]any{
			"lesson_id":     l.LessonID,
			"words":         l.Words,
			"current_index": l.CurrentIndex,
			"version":       l.Version,
		})
		return string(payload)

	case "advance_word":
		// request_id = the MiMo tool_call id: idempotency is keyed on the
		// provider's own call identity, never on model-invented input.
		res, err := tm.exec.AdvanceWord(ctx, args.LessonID, *args.ExpectedVersion, tc.ID)
		if err != nil {
			var conflict *lesson.ConflictError
			if errors.As(err, &conflict) {
				// Fed back for a verbal resolution — never a retried write.
				payload, _ := json.Marshal(map[string]any{
					"ok":              false,
					"error":           "version_conflict",
					"current_version": conflict.CurrentVersion,
				})
				return string(payload)
			}
			tm.sendError(s, ErrCodeProviderFailed, "课堂进度更新失败", true)
			return toolErrorResult("tool_failed", "advance_word failed")
		}
		tm.mu.Lock()
		tm.turnWrote = true
		tm.turnWroteVersion = res.NewVersion
		tm.mu.Unlock()
		_ = s.SendControl(LessonUpdateMsg{
			Type:         TypeLessonUpdate,
			LessonID:     args.LessonID,
			Version:      res.NewVersion,
			CurrentIndex: res.NewIndex,
			RequestID:    tc.ID,
		})
		payload, _ := json.Marshal(map[string]any{
			"ok":        true,
			"new_index": res.NewIndex,
			"version":   res.NewVersion,
		})
		return string(payload)
	}

	// validateToolArgs whitelists the name; unreachable.
	return toolErrorResult("invalid_arguments", "unknown tool "+tc.Function.Name)
}

// speakTurn streams the final text sentence by sentence: subtitle first,
// audio chunks forwarded the instant they arrive. The spoken-text message is
// appended to history UP FRONT and its index recorded — interrupt-time
// trimming is the ONLY mutation of it (M-2: this function never rolls back
// its own append; the interrupt branch owns the final state).
func (tm *TurnManager) speakTurn(s Sender, text string, turnID, epoch uint64, ctx context.Context, cancel context.CancelFunc) {
	tm.mu.Lock()
	if tm.epoch != epoch {
		// Stale before the first sound: nothing appended, nothing spoken.
		tm.mu.Unlock()
		return
	}
	tm.history = append(tm.history, mimo.Message{Role: "assistant", Content: text})
	tm.spokenMsgIdx = len(tm.history) - 1
	sentences := splitSentences(text)
	tm.state = StateSpeaking
	tm.mu.Unlock()

	_ = s.SendControl(StateMsg{Type: TypeState, Value: StateSpeaking})

	var firstChunk atomic.Bool
	for i, sentence := range sentences {
		sentID := fmt.Sprintf("t%d-s%d", turnID, i)

		// Subtitle first (字幕先行), atomically against interrupt handling —
		// after an interrupt is acknowledged, no further subtitle of the
		// interrupted turn can go out.
		tm.mu.Lock()
		if tm.epoch != epoch {
			tm.mu.Unlock()
			return
		}
		tm.turnSentences = append(tm.turnSentences, sentenceInfo{id: sentID, text: sentence})
		_ = s.SendControl(TeacherSentenceMsg{
			Type: TypeTeacherSentence, TurnID: turnID, Epoch: epoch, SentenceID: sentID, Text: sentence,
		})
		_ = s.SendControl(AudioStartMsg{Type: TypeAudioStart, TurnID: turnID, Epoch: epoch, SentenceID: sentID})
		tm.mu.Unlock()

		// pcm arrives as bare s16le bytes (AudioChunkFunc); the wire
		// encoding is digested by the 13-06 adapter, never here (M-1).
		_, err := tm.tts.SynthesizeStream(ctx, sentence, func(pcm []byte, isFinal bool) error {
			tm.mu.Lock()
			if tm.epoch != epoch {
				tm.mu.Unlock()
				cancel() // late chunk: abort the provider stream too
				return ErrStaleEpoch
			}
			if firstChunk.CompareAndSwap(false, true) {
				tm.recorder.Emit(EventFirstAudioReceived, infra.F("turn_id", turnID))
			}
			err := s.SendAudio(pcm)
			tm.mu.Unlock()
			return err
		})
		if err != nil && !errors.Is(err, ErrStaleEpoch) && tm.epochCurrent(epoch) {
			// A genuine provider failure on a still-live turn.
			tm.failTurn(s, epoch, ErrCodeProviderFailed, "老师的声音出了点问题，请再说一遍")
			return
		}

		tm.mu.Lock()
		stale := tm.epoch != epoch
		if !stale {
			_ = s.SendControl(AudioEndMsg{Type: TypeAudioEnd, TurnID: turnID, Epoch: epoch, SentenceID: sentID})
		}
		tm.mu.Unlock()
		if stale {
			return // interrupted mid-sentence: the interrupt branch trims
		}
	}

	// All sentences played naturally: the spoken-text message keeps the full
	// text, the turn ends, and a lesson write (if any) gets its event.
	tm.mu.Lock()
	tm.state = StateListening
	wrote, wroteVersion := tm.turnWrote, tm.turnWroteVersion
	tm.spokenMsgIdx = -1
	tm.mu.Unlock()
	_ = s.SendControl(StateMsg{Type: TypeState, Value: StateListening})
	if wrote {
		tm.recorder.Emit(EventLessonVersion,
			infra.F("turn_id", turnID),
			infra.F("version", wroteVersion),
		)
	}
}

// failTurn reports a turn-level failure and returns the FSM to listening —
// but only if the turn is still current: an interrupted turn must not emit a
// spurious error on top of the interrupt ack.
func (tm *TurnManager) failTurn(s Sender, epoch uint64, code, message string) {
	if !tm.epochCurrent(epoch) {
		return
	}
	tm.sendError(s, code, message, true)
	tm.mu.Lock()
	tm.state = StateListening
	tm.mu.Unlock()
	_ = s.SendControl(StateMsg{Type: TypeState, Value: StateListening})
}

// sendError is the single error egress: coded ErrorMsg + error event.
func (tm *TurnManager) sendError(s Sender, code, message string, recoverable bool) {
	_ = s.SendControl(ErrorMsg{Type: TypeError, Code: code, Message: message, Recoverable: recoverable})
	tm.recorder.Emit(EventError, infra.F("code", code))
}

// ---------------------------------------------------------------------------
// Small locked helpers
// ---------------------------------------------------------------------------

func (tm *TurnManager) epochCurrent(launch uint64) bool {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	return tm.epoch == launch
}

func (tm *TurnManager) appendHistory(m mimo.Message) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.history = append(tm.history, m)
	tm.trimHistoryLocked()
}

// historySnapshot returns a copy for provider calls (providers must not
// alias the live slice).
func (tm *TurnManager) historySnapshot() []mimo.Message {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	return append([]mimo.Message(nil), tm.history...)
}

// trimHistoryLocked bounds the context to HistoryMaxTurns turns, keeping
// the system message. A "turn" is a user message plus everything after it up
// to the next user message; only whole oldest turns are dropped, so the
// current (newest) turn is never trimmed mid-flight.
func (tm *TurnManager) trimHistoryLocked() {
	max := tm.cfg.HistoryMaxTurns
	if max <= 0 {
		return
	}
	for {
		groups := 0
		for i := 1; i < len(tm.history); i++ {
			if tm.history[i].Role == "user" {
				groups++
			}
		}
		if groups <= max {
			return
		}
		// Drop the first group: from after the system message up to (not
		// including) the second user message.
		end := len(tm.history)
		seenUser := false
		for i := 1; i < len(tm.history); i++ {
			if tm.history[i].Role == "user" {
				if !seenUser {
					seenUser = true
					continue
				}
				end = i
				break
			}
		}
		tm.history = append(tm.history[:1], tm.history[end:]...)
	}
}

// ---------------------------------------------------------------------------
// Tool argument validation (T-13-03 / T-13-05b / M-5)
// ---------------------------------------------------------------------------

// lessonToolArgs is the strict parse of a lesson tool's arguments.
// ExpectedVersion is a pointer so "absent" is distinguishable from 0, and
// non-integer JSON (1.5, "1") fails the unmarshal instead of the executor.
type lessonToolArgs struct {
	LessonID        string `json:"lesson_id"`
	ExpectedVersion *int   `json:"expected_version"`
}

// validateToolArgs enforces the whitelist and per-field checks BEFORE any
// executor call: known tool name, valid JSON, non-empty lesson_id that MUST
// equal the session's lesson (session binding — a hallucinated or injected
// foreign lesson id never reaches the executor, M-5 / RESEARCH V4), and for
// advance_word an integer expected_version >= 1.
func validateToolArgs(sessionLessonID, toolName, argsJSON string) (*lessonToolArgs, error) {
	switch toolName {
	case "get_lesson_state", "advance_word":
	default:
		return nil, fmt.Errorf("unknown tool %q", toolName)
	}
	var args lessonToolArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return nil, fmt.Errorf("arguments are not valid JSON: %v", err)
	}
	if args.LessonID == "" {
		return nil, fmt.Errorf("lesson_id must be a non-empty string")
	}
	if args.LessonID != sessionLessonID {
		return nil, fmt.Errorf("lesson_id %q does not match this session's lesson", args.LessonID)
	}
	if toolName == "advance_word" {
		if args.ExpectedVersion == nil {
			return nil, fmt.Errorf("expected_version is required and must be an integer >= 1")
		}
		if *args.ExpectedVersion < 1 {
			return nil, fmt.Errorf("expected_version must be >= 1")
		}
	}
	return &args, nil
}

// toolErrorResult shapes the tool result fed back to the model when the call
// was invalid or failed — the model gets its chance to react (回喂), and
// nothing is written for invalid input.
func toolErrorResult(code, detail string) string {
	payload, _ := json.Marshal(map[string]any{
		"error":  code,
		"detail": detail,
	})
	return string(payload)
}

// splitSentences breaks teacher text into speakable sentences. Chinese
// terminators 。！？；, newlines, and ASCII .!? each end a sentence (the
// terminator stays with its sentence); whitespace between sentences is
// dropped; text with no terminator is returned as one sentence (兜底单句).
// Empty/blank input yields nil.
func splitSentences(text string) []string {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	var out []string
	start := 0
	for i, r := range text {
		if !isSentenceBreak(r) {
			continue
		}
		end := i + len(string(r))
		if seg := strings.TrimSpace(text[start:end]); seg != "" {
			out = append(out, seg)
		}
		start = end
	}
	if start < len(text) {
		if seg := strings.TrimSpace(text[start:]); seg != "" {
			out = append(out, seg)
		}
	}
	return out
}

func isSentenceBreak(r rune) bool {
	switch r {
	case '。', '！', '？', '；', '.', '!', '?', '\n':
		return true
	}
	return false
}
