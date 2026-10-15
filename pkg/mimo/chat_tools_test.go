package mimo

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"
)

// advanceWordToolDef is the advance_word declaration from 13-RESEARCH.md
// Pattern 4 — the write tool the realtime lesson loop exposes to the model.
func advanceWordToolDef() ToolDef {
	return ToolDef{
		Type: "function",
		Function: ToolFunctionDef{
			Name:        "advance_word",
			Description: "推进到下一个单词（仅当学生明确表示写完/确认）",
			Parameters: json.RawMessage(`{` +
				`"type":"object",` +
				`"properties":{` +
				`"lesson_id":{"type":"string"},` +
				`"expected_version":{"type":"integer"}` +
				`},` +
				`"required":["lesson_id","expected_version"]}`),
			Strict: true,
		},
	}
}

// writeChatEmptyChoice responds with a normal-shaped chat completion whose
// single choice carries an empty content (request-body tests only need the
// client to accept the response).
func writeChatEmptyChoice(w http.ResponseWriter) {
	response := ChatResponse{}
	response.Choices = append(response.Choices, struct {
		FinishReason string             `json:"finish_reason"`
		Message      ChatMessageContent `json:"message"`
		Index        int                `json:"index"`
	}{
		Message:      ChatMessageContent{Role: "assistant", Content: ""},
		FinishReason: "stop",
	})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(response); err != nil {
		return
	}
}

// captureChatBody runs call against a mock chat server built on the
// chat_test.go scaffold (mockTTSServer + newTestLogger) and returns the raw
// request body bytes the server saw. Fails the test if the call errors.
func captureChatBody(t *testing.T, respond func(w http.ResponseWriter), call func(client Client) error) []byte {
	t.Helper()

	var captured []byte
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		captured = body
		r.Body = io.NopCloser(bytes.NewReader(body))
		respond(w)
	})

	server, cleanup := mockTTSServer(handler)
	defer cleanup()

	config := ClientConfig{
		APIKey:     "test-api-key",
		BaseURL:    server.URL,
		Timeout:    5 * time.Second,
		MaxRetries: 1,
		RetryDelay: 1 * time.Millisecond,
	}
	client, err := NewClient(config, newTestLogger())
	if err != nil {
		t.Fatalf("NewClient() failed: %v", err)
	}
	defer client.Close()

	if err := call(client); err != nil {
		t.Fatalf("Chat() unexpected error: %v", err)
	}
	if len(captured) == 0 {
		t.Fatal("no request body captured")
	}
	return captured
}

// TestChatToolsRequestBody asserts the request-side tool wire format: with
// WithTools + WithThinkingDisabled + WithToolChoiceAuto, the captured body
// carries tools[0].function.name/strict, thinking.type, and tool_choice
// exactly as 13-RESEARCH Pattern 4 prescribes.
func TestChatToolsRequestBody(t *testing.T) {
	captured := captureChatBody(t, writeChatEmptyChoice, func(client Client) error {
		_, err := client.Chat(context.Background(),
			[]Message{{Role: "user", Content: "我写完了"}},
			WithTools([]ToolDef{advanceWordToolDef()}),
			WithThinkingDisabled(),
			WithToolChoiceAuto(),
		)
		return err
	})

	var body struct {
		Thinking   *ThinkingConfig `json:"thinking"`
		ToolChoice string          `json:"tool_choice"`
		Tools      []struct {
			Type     string `json:"type"`
			Function struct {
				Name       string          `json:"name"`
				Parameters json.RawMessage `json:"parameters"`
				Strict     bool            `json:"strict"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(captured, &body); err != nil {
		t.Fatalf("captured body is not valid JSON: %v\nbody: %s", err, captured)
	}

	if len(body.Tools) != 1 {
		t.Fatalf("expected 1 tool in request body, got %d (body: %s)", len(body.Tools), captured)
	}
	if body.Tools[0].Type != "function" {
		t.Errorf("tools[0].type = %q, want %q", body.Tools[0].Type, "function")
	}
	if body.Tools[0].Function.Name != "advance_word" {
		t.Errorf("tools[0].function.name = %q, want %q", body.Tools[0].Function.Name, "advance_word")
	}
	if !body.Tools[0].Function.Strict {
		t.Error("tools[0].function.strict = false, want true")
	}
	if !json.Valid(body.Tools[0].Function.Parameters) {
		t.Errorf("tools[0].function.parameters is not valid JSON: %s", body.Tools[0].Function.Parameters)
	}
	if !bytes.Contains(body.Tools[0].Function.Parameters, []byte("lesson_id")) {
		t.Errorf("tools[0].function.parameters missing lesson_id property: %s", body.Tools[0].Function.Parameters)
	}

	if body.ToolChoice != "auto" {
		t.Errorf("tool_choice = %q, want %q (Pitfall 7: only auto is honored)", body.ToolChoice, "auto")
	}

	if body.Thinking == nil {
		t.Fatal("thinking is absent from request body, want {\"type\":\"disabled\"}")
	}
	if body.Thinking.Type != "disabled" {
		t.Errorf("thinking.type = %q, want %q (Pitfall 1)", body.Thinking.Type, "disabled")
	}
}

// toolCallsResponseJSON is the Pattern 4 sample response: an assistant turn
// requesting advance_word with finish_reason "tool_calls" and empty content.
const toolCallsResponseJSON = `{
  "choices": [
    {
      "message": {
        "role": "assistant",
        "content": "",
        "tool_calls": [
          {
            "id": "call_abc",
            "type": "function",
            "function": {
              "name": "advance_word",
              "arguments": "{\"lesson_id\":\"L1\",\"expected_version\":3}"
            }
          }
        ]
      },
      "index": 0,
      "finish_reason": "tool_calls"
    }
  ]
}`

// TestChatToolCallsResponseParse asserts the response-side tool wire format:
// the client parses choices[0].message.tool_calls (id/type/function.name/
// function.arguments as a raw JSON string) and finish_reason=="tool_calls"
// drives ToolCallsRequested/FirstToolCalls.
func TestChatToolCallsResponseParse(t *testing.T) {
	server, cleanup := mockTTSServer(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if _, err := io.WriteString(w, toolCallsResponseJSON); err != nil {
			return
		}
	})
	defer cleanup()

	config := ClientConfig{
		APIKey:     "test-api-key",
		BaseURL:    server.URL,
		Timeout:    5 * time.Second,
		MaxRetries: 1,
		RetryDelay: 1 * time.Millisecond,
	}
	client, err := NewClient(config, newTestLogger())
	if err != nil {
		t.Fatalf("NewClient() failed: %v", err)
	}
	defer client.Close()

	resp, err := client.Chat(context.Background(),
		[]Message{{Role: "user", Content: "我写完了"}},
		WithTools([]ToolDef{advanceWordToolDef()}),
	)
	if err != nil {
		t.Fatalf("Chat() unexpected error: %v", err)
	}

	if !resp.ToolCallsRequested() {
		t.Fatalf("ToolCallsRequested() = false, want true (finish_reason==tool_calls); resp=%+v", resp)
	}

	calls := resp.FirstToolCalls()
	if len(calls) != 1 {
		t.Fatalf("FirstToolCalls() returned %d calls, want 1", len(calls))
	}
	if calls[0].ID != "call_abc" {
		t.Errorf("FirstToolCalls()[0].ID = %q, want %q", calls[0].ID, "call_abc")
	}
	if calls[0].Type != "function" {
		t.Errorf("FirstToolCalls()[0].Type = %q, want %q", calls[0].Type, "function")
	}
	if calls[0].Function.Name != "advance_word" {
		t.Errorf("FirstToolCalls()[0].Function.Name = %q, want %q", calls[0].Function.Name, "advance_word")
	}
	wantArgs := `{"lesson_id":"L1","expected_version":3}`
	if calls[0].Function.Arguments != wantArgs {
		// Arguments must stay a raw string — no eager unmarshal (T-13-05).
		t.Errorf("FirstToolCalls()[0].Function.Arguments = %q, want verbatim %q", calls[0].Function.Arguments, wantArgs)
	}

	// The arguments string itself must be valid JSON so the executor in
	// 13-04 can schema-validate it.
	var args map[string]any
	if err := json.Unmarshal([]byte(calls[0].Function.Arguments), &args); err != nil {
		t.Errorf("tool call arguments are not valid JSON: %v", err)
	}
}

// TestToolMessageRoundTrip asserts the tool-loop refill wire shape: an
// assistant message replaying its tool_calls plus a role:"tool" message
// answering with tool_call_id + content serialize verbatim onto the next
// request (Pattern 4's second sample).
func TestToolMessageRoundTrip(t *testing.T) {
	toolResult := `{"ok":true,"new_index":1,"version":4}`
	messages := []Message{
		{Role: "system", Content: "你是听写家教"},
		{Role: "user", Content: "我写完了"},
		{
			Role:    "assistant",
			Content: "",
			ToolCalls: []ToolCall{{
				ID:   "call_abc",
				Type: "function",
				Function: ToolCallFunction{
					Name:      "advance_word",
					Arguments: `{"lesson_id":"L1","expected_version":3}`,
				},
			}},
		},
		{Role: "tool", ToolCallID: "call_abc", Content: toolResult},
	}

	captured := captureChatBody(t, writeChatEmptyChoice, func(client Client) error {
		_, err := client.Chat(context.Background(), messages)
		return err
	})

	var body struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(captured, &body); err != nil {
		t.Fatalf("captured body is not valid JSON: %v\nbody: %s", err, captured)
	}
	if len(body.Messages) != len(messages) {
		t.Fatalf("expected %d messages in captured body, got %d (body: %s)", len(messages), len(body.Messages), captured)
	}

	assistant := body.Messages[2]
	toolCalls, ok := assistant["tool_calls"].([]any)
	if !ok {
		t.Fatalf("assistant message carries no tool_calls array: %s", captured)
	}
	if len(toolCalls) != 1 {
		t.Fatalf("assistant tool_calls has %d entries, want 1", len(toolCalls))
	}
	first, _ := toolCalls[0].(map[string]any)
	if first["id"] != "call_abc" {
		t.Errorf("assistant tool_calls[0].id = %v, want call_abc", first["id"])
	}
	fn, _ := first["function"].(map[string]any)
	if fn["name"] != "advance_word" {
		t.Errorf("assistant tool_calls[0].function.name = %v, want advance_word", fn["name"])
	}
	if fn["arguments"] != `{"lesson_id":"L1","expected_version":3}` {
		t.Errorf("assistant tool_calls[0].function.arguments = %v, want verbatim JSON string", fn["arguments"])
	}

	toolMsg := body.Messages[3]
	if toolMsg["role"] != "tool" {
		t.Errorf("role of refill message = %v, want tool", toolMsg["role"])
	}
	if toolMsg["tool_call_id"] != "call_abc" {
		t.Errorf("tool_call_id = %v, want call_abc", toolMsg["tool_call_id"])
	}
	if toolMsg["content"] != toolResult {
		t.Errorf("tool content = %v, want verbatim %s", toolMsg["content"], toolResult)
	}
}

// TestChatWithoutToolsUnchanged asserts the legacy no-tools request path does
// not drift: a Chat call without the new options serializes no tools,
// tool_choice, or thinking keys, so existing voice-command requests keep the
// exact wire shape they had before Phase 13.
func TestChatWithoutToolsUnchanged(t *testing.T) {
	captured := captureChatBody(t, writeChatEmptyChoice, func(client Client) error {
		_, err := client.Chat(context.Background(),
			[]Message{
				{Role: "system", Content: "classify"},
				{Role: "user", Content: "好了"},
			},
			WithMaxTokens(64),
		)
		return err
	})

	var body map[string]any
	if err := json.Unmarshal(captured, &body); err != nil {
		t.Fatalf("captured body is not valid JSON: %v\nbody: %s", err, captured)
	}
	for _, key := range []string{"tools", "tool_choice", "thinking"} {
		if _, present := body[key]; present {
			t.Errorf("no-option request body contains %q key (omitempty drift): %s", key, captured)
		}
	}

	// The pre-existing fields are still on the wire.
	if body["model"] != DefaultChatModel {
		t.Errorf("model = %v, want %q", body["model"], DefaultChatModel)
	}
	if _, present := body["messages"]; !present {
		t.Errorf("messages key missing from request body: %s", captured)
	}
}

// TestToolMessageFieldsOmittedOnEmpty anchors the message-level omitempty
// contract (review L-6): Message is shared with TTSRequest.Messages, so
// zero-value messages must never leak the tool-loop keys into any wire —
// neither a bare Message nor a TTSRequest built through the existing TTS
// options.
func TestToolMessageFieldsOmittedOnEmpty(t *testing.T) {
	// A zero-value-tool-fields Message serializes without either key.
	msgJSON, err := json.Marshal(Message{Role: "user", Content: "小朋友们好"})
	if err != nil {
		t.Fatalf("Marshal(Message) failed: %v", err)
	}
	for _, key := range []string{"tool_calls", "tool_call_id"} {
		if bytes.Contains(msgJSON, []byte(`"`+key+`"`)) {
			t.Errorf("zero-value Message serializes %q key: %s", key, msgJSON)
		}
	}

	// The same holds inside a TTSRequest built through the existing options —
	// tool-loop-filled history must not pollute the TTS wire.
	req := TTSRequest{
		Model:    "mimo-v2.5-tts",
		Messages: []Message{{Role: "user", Content: "读一遍 apple"}, {Role: "assistant", Content: "apple"}},
		Audio:    AudioConfig{Format: "pcm16", Voice: "冰糖"},
	}
	WithStyleInstruction("Use a gentle, encouraging tone.")(&req)
	WithTTSStream()(&req)
	reqJSON, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal(TTSRequest) failed: %v", err)
	}
	for _, key := range []string{"tool_calls", "tool_call_id"} {
		if bytes.Contains(reqJSON, []byte(`"`+key+`"`)) {
			t.Errorf("TTSRequest body serializes %q key: %s", key, reqJSON)
		}
	}
	// Sanity: the request is still a real TTS body, not an empty shell.
	if !bytes.Contains(reqJSON, []byte(`"messages"`)) || !bytes.Contains(reqJSON, []byte(`"stream":true`)) {
		t.Errorf("TTSRequest body lost expected keys: %s", reqJSON)
	}
}
