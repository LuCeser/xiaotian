package mimo

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
)

// Real-key smoke tests for the tool loop with thinking disabled (Phase 13
// RT-03/RT-04, Assumption A4). Gating reuses integration_test.go's
// getTestAPIKey: no MIMO_API_KEY in the environment or -short mode means
// t.Skip — a skip is never a failure, so key-less environments stay green.

// getLessonStateToolDef declares the read tool the realtime lesson loop
// offers to the model (13-RESEARCH Pattern 4 vocabulary).
func getLessonStateToolDef() ToolDef {
	return ToolDef{
		Type: "function",
		Function: ToolFunctionDef{
			Name:        "get_lesson_state",
			Description: "查询当前课堂状态（当前单词与进度）。课堂状态必须通过工具读写，禁止口头编造进度。",
			Parameters: json.RawMessage(`{` +
				`"type":"object",` +
				`"properties":{` +
				`"lesson_id":{"type":"string"}` +
				`},` +
				`"required":["lesson_id"]}`),
			Strict: true,
		},
	}
}

// newLiveToolsClient builds a client against the real API for the live tool
// smoke tests. Returns nil-skipping behavior is handled by getTestAPIKey.
func newLiveToolsClient(t *testing.T) Client {
	t.Helper()

	config := ClientConfig{
		APIKey:     getTestAPIKey(t),
		BaseURL:    getTestBaseURL(),
		Timeout:    60 * time.Second,
		MaxRetries: 2,
		RetryDelay: 1 * time.Millisecond,
	}
	client, err := NewClient(config, infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// liveTutorMessages is the minimal realtime-tutor turn used by both probes.
func liveTutorMessages() []Message {
	return []Message{
		{
			Role: "system",
			Content: "你是一位亲切的小学听写家教。课堂进度必须通过工具读写，" +
				"禁止口头编造进度。沉默是书写时间，不要自动跳词。",
		},
		{Role: "user", Content: "现在学到哪个词了？"},
	}
}

// TestLiveChatToolsThinkingDisabled is the A4 smoke test: with
// WithThinkingDisabled the real API must not emit reasoning_content, and the
// model either requests get_lesson_state (arguments must be valid JSON —
// the official docs warn they may not be) or answers in plain text. Elapsed
// time is logged for RT-04 latency baseline; no threshold is asserted here.
func TestLiveChatToolsThinkingDisabled(t *testing.T) {
	client := newLiveToolsClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	start := time.Now()
	resp, err := client.Chat(ctx, liveTutorMessages(),
		WithTools([]ToolDef{getLessonStateToolDef()}),
		WithToolChoiceAuto(),
		WithThinkingDisabled(),
	)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Chat() failed: %v", err)
	}
	t.Logf("thinking-disabled chat completed in %dms", elapsed.Milliseconds())

	if len(resp.Choices) == 0 {
		t.Fatal("no choices in live response")
	}

	// A4 core: thinking off means no reasoning_content on the wire.
	if reasoning := resp.Choices[0].Message.ReasoningContent; reasoning != "" {
		t.Errorf("reasoning_content non-empty after WithThinkingDisabled: %q", reasoning)
	}

	t.Logf("finish_reason=%q tool_calls_requested=%v",
		resp.Choices[0].FinishReason, resp.ToolCallsRequested())

	if resp.ToolCallsRequested() {
		calls := resp.FirstToolCalls()
		if len(calls) == 0 {
			t.Fatal("ToolCallsRequested() true but FirstToolCalls() empty")
		}
		if calls[0].Function.Name != "get_lesson_state" {
			t.Errorf("tool name = %q, want get_lesson_state", calls[0].Function.Name)
		}
		var args map[string]any
		if err := json.Unmarshal([]byte(calls[0].Function.Arguments), &args); err != nil {
			t.Errorf("tool arguments are not valid JSON (hallucinated args, Pitfall 2): %v (raw: %s)",
				err, calls[0].Function.Arguments)
		}
		t.Logf("model requested tool %s arguments=%s",
			calls[0].Function.Name, calls[0].Function.Arguments)
	} else {
		text := resp.Text()
		if text == "" {
			t.Error("no tool call and empty text — nothing usable came back")
		}
		t.Logf("model answered in text (%d chars): %.80s", len(text), text)
	}
}

// TestLiveChatThinkingDefaultLatencyProbe is the A4 comparison probe: the
// same request WITHOUT WithThinkingDisabled (thinking is on by default). Only HTTP success is asserted — elapsed time and whether
// reasoning_content appeared are logged to leave real-device evidence for
// the RT-04 latency assessment (sixth-week check), per the plan.
func TestLiveChatThinkingDefaultLatencyProbe(t *testing.T) {
	client := newLiveToolsClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	start := time.Now()
	resp, err := client.Chat(ctx, liveTutorMessages(),
		WithTools([]ToolDef{getLessonStateToolDef()}),
		WithToolChoiceAuto(),
	)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Chat() failed: %v", err)
	}
	t.Logf("thinking-default (server default on) chat completed in %dms", elapsed.Milliseconds())

	reasoning := ""
	finishReason := ""
	if len(resp.Choices) > 0 {
		reasoning = resp.Choices[0].Message.ReasoningContent
		finishReason = resp.Choices[0].FinishReason
	}
	t.Logf("reasoning_content present=%v (%d chars), finish_reason=%q, tool_calls_requested=%v",
		reasoning != "", len(reasoning), finishReason, resp.ToolCallsRequested())
}
