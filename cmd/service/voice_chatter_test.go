package main

// mimoChatter wire-contract tests (issue #45): the adapter must send
// thinking={"type":"disabled"} on every chat completion, so the intent
// classifier and the meaning generator never rely on the Text()
// reasoning_content fallback. The mock-server interception pattern mirrors
// pkg/mimo's chat_test.go: a real mimo client pointed at an httptest server
// that captures the request body — the assertion runs against the exact JSON
// that would hit the wire, not just the option slice.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/mimo"
)

// TestMimoChatter_DisablesThinkingOnTheWire pins issue #45's fix: mimoChatter
// forwards the system/user messages with the configured model AND thinking
// disabled, and returns the reply's content verbatim.
func TestMimoChatter_DisablesThinkingOnTheWire(t *testing.T) {
	const wantModel = "mimo-v2.6-flash"
	const reply = `{"action":"cannot_write","confidence":0.9}`

	var capturedReq mimo.ChatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("capture request body: %v", err)
			return
		}
		if err := json.Unmarshal(body, &capturedReq); err != nil {
			t.Errorf("request body is not valid ChatRequest JSON: %v (raw: %s)", err, body)
			return
		}
		resp := mimo.ChatResponse{}
		resp.Choices = append(resp.Choices, struct {
			FinishReason string                  `json:"finish_reason"`
			Message      mimo.ChatMessageContent `json:"message"`
			Index        int                     `json:"index"`
		}{
			FinishReason: "stop",
			Message:      mimo.ChatMessageContent{Role: "assistant", Content: reply},
		})
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Errorf("encode mock response: %v", err)
		}
	}))
	defer server.Close()

	client, err := mimo.NewClient(mimo.ClientConfig{
		APIKey:     "test-api-key",
		BaseURL:    server.URL,
		Timeout:    5 * time.Second,
		MaxRetries: 1,
		RetryDelay: 1 * time.Millisecond,
	}, infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("mimo client: %v", err)
	}
	defer func() { _ = client.Close() }()

	chatter := mimoChatter(client, wantModel)
	got, err := chatter(context.Background(), "你是语音命令识别器", "老师我觉得这个有点难")
	if err != nil {
		t.Fatalf("chatter() error: %v", err)
	}
	if got != reply {
		t.Errorf("chatter() = %q, want the reply content %q", got, reply)
	}

	// The regression this file pins: thinking must be explicitly disabled on
	// the wire — an absent thinking field means the server default (ON) applies
	// and the Text() fallback becomes load-bearing again.
	if capturedReq.Thinking == nil {
		t.Fatal("thinking field absent from chat request — server default (enabled) applies")
	}
	if capturedReq.Thinking.Type != "disabled" {
		t.Errorf("thinking.type = %q, want %q", capturedReq.Thinking.Type, "disabled")
	}
	if capturedReq.Model != wantModel {
		t.Errorf("model = %q, want %q", capturedReq.Model, wantModel)
	}
	if len(capturedReq.Messages) != 2 ||
		capturedReq.Messages[0].Role != "system" || capturedReq.Messages[0].Content != "你是语音命令识别器" ||
		capturedReq.Messages[1].Role != "user" || capturedReq.Messages[1].Content != "老师我觉得这个有点难" {
		t.Errorf("messages not forwarded as [system, user]: %+v", capturedReq.Messages)
	}
}
