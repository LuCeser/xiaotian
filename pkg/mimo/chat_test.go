package mimo

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pkg/errors"
)

// defaultChatHandler provides a successful chat completion response with a
// plain content answer (the common non-thinking shape).
func defaultChatHandler(w http.ResponseWriter, r *http.Request) {
	auth := r.Header.Get("Authorization")
	if auth != "Bearer test-api-key" {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]interface{}{
				"code":    "authentication_failed",
				"message": "Invalid API key",
				"type":    "authentication_error",
			},
		})
		return
	}

	var req ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	response := ChatResponse{}
	response.Choices = append(response.Choices, struct {
		Message ChatMessageContent `json:"message"`
		Index   int                `json:"index"`
	}{
		Message: ChatMessageContent{Role: "assistant", Content: `{"action":"next_word","confidence":0.9}`},
	})

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(response)
}

// TestClient_Chat exercises the LLM chat completion path: happy path,
// defaults, options, thinking-model reasoning_content fallback, error mapping,
// and the empty-messages guard.
func TestClient_Chat(t *testing.T) {
	tests := []struct {
		name        string
		handler     http.HandlerFunc
		messages    []Message
		opts        []ChatOption
		wantErr     error
		wantText    string
		validateReq func(*testing.T, ChatRequest)
	}{
		{
			name:    "successful completion with default model",
			handler: defaultChatHandler,
			messages: []Message{
				{Role: "system", Content: "classify"},
				{Role: "user", Content: "好了"},
			},
			opts:     []ChatOption{},
			wantErr:  nil,
			wantText: `{"action":"next_word","confidence":0.9}`,
			validateReq: func(t *testing.T, req ChatRequest) {
				if req.Model != DefaultChatModel {
					t.Errorf("expected default model %q, got %q", DefaultChatModel, req.Model)
				}
				if len(req.Messages) != 2 {
					t.Errorf("expected 2 messages forwarded, got %d", len(req.Messages))
				}
				if req.MaxTokens != 0 {
					t.Errorf("expected MaxTokens unset by default, got %d", req.MaxTokens)
				}
			},
		},
		{
			name:     "options applied (model, max tokens, temperature)",
			handler:  defaultChatHandler,
			messages: []Message{{Role: "user", Content: "hi"}},
			opts: []ChatOption{
				WithChatModel("mimo-v2.5-pro"),
				WithMaxTokens(64),
				WithTemperature(0.2),
			},
			wantErr:  nil,
			wantText: `{"action":"next_word","confidence":0.9}`,
			validateReq: func(t *testing.T, req ChatRequest) {
				if req.Model != "mimo-v2.5-pro" {
					t.Errorf("expected model override, got %q", req.Model)
				}
				if req.MaxTokens != 64 {
					t.Errorf("expected MaxTokens 64, got %d", req.MaxTokens)
				}
				if req.Temperature != 0.2 {
					t.Errorf("expected Temperature 0.2, got %v", req.Temperature)
				}
			},
		},
		{
			name: "thinking model: empty content falls back to reasoning_content",
			handler: func(w http.ResponseWriter, r *http.Request) {
				response := ChatResponse{}
				response.Choices = append(response.Choices, struct {
					Message ChatMessageContent `json:"message"`
					Index   int                `json:"index"`
				}{
					Message: ChatMessageContent{Role: "assistant", ReasoningContent: "thinking..."},
				})
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(response)
			},
			messages: []Message{{Role: "user", Content: "hi"}},
			opts:     []ChatOption{},
			wantErr:  nil,
			wantText: "thinking...",
		},
		{
			name:     "authentication error is non-retryable",
			handler:  defaultChatHandler,
			messages: []Message{{Role: "user", Content: "hi"}},
			opts:     []ChatOption{},
			wantErr:  ErrAuthentication,
		},
		{
			name:     "empty messages rejected before HTTP",
			handler:  defaultChatHandler,
			messages: nil,
			opts:     []ChatOption{},
			wantErr:  ErrInvalidResponse,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// The auth-error case needs a client whose key differs from the
			// handler's expectation; build it per-case.
			key := "test-api-key"
			if tt.wantErr == ErrAuthentication {
				key = "wrong-key"
			}

			var capturedReq ChatRequest
			wrapHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(body, &capturedReq)
				r.Body = io.NopCloser(bytes.NewReader(body))
				tt.handler(w, r)
			})

			server, cleanup := mockTTSServer(wrapHandler)
			defer cleanup()

			config := ClientConfig{
				APIKey:     key,
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

			resp, err := client.Chat(context.Background(), tt.messages, tt.opts...)

			if tt.wantErr != nil {
				if err == nil {
					t.Fatalf("Chat() expected error %v, got nil", tt.wantErr)
				}
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("Chat() expected error %v, got %v", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Chat() unexpected error: %v", err)
			}
			if got := resp.Text(); got != tt.wantText {
				t.Errorf("Chat() text = %q, want %q", got, tt.wantText)
			}
			if tt.validateReq != nil {
				tt.validateReq(t, capturedReq)
			}
		})
	}
}

// TestClient_Chat_RetriesTransientErrors verifies the retry loop: a 503
// followed by 200 succeeds and the request was sent twice.
func TestClient_Chat_RetriesTransientErrors(t *testing.T) {
	var calls int32
	handler := func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			serviceUnavailableHandler(w, r)
			return
		}
		defaultChatHandler(w, r)
	}

	server, cleanup := mockTTSServer(handler)
	defer cleanup()

	config := ClientConfig{
		APIKey:     "test-api-key",
		BaseURL:    server.URL,
		Timeout:    5 * time.Second,
		MaxRetries: 2,
		RetryDelay: 1 * time.Millisecond,
	}
	client, err := NewClient(config, newTestLogger())
	if err != nil {
		t.Fatalf("NewClient() failed: %v", err)
	}
	defer client.Close()

	resp, err := client.Chat(context.Background(), []Message{{Role: "user", Content: "好了"}})
	if err != nil {
		t.Fatalf("Chat() unexpected error after retry: %v", err)
	}
	if resp.Text() == "" {
		t.Error("expected non-empty text after retry")
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("expected 2 calls (1 failure + 1 success), got %d", got)
	}
}
