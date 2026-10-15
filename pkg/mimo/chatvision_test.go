package mimo

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pkg/errors"
)

// visionTestImage is a minimal valid JPEG (SOI + EOI) — enough to exercise
// the request path; the mock server never decodes it as a real image.
var visionTestImage = []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00, 0xFF, 0xD9}

// defaultVisionHandler mirrors defaultChatHandler: auth check, then a plain
// assistant content answer.
func defaultVisionHandler(w http.ResponseWriter, r *http.Request) {
	auth := r.Header.Get("Authorization")
	if auth != "Bearer test-api-key" {
		w.WriteHeader(http.StatusUnauthorized)
		if err := json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]interface{}{
				"code":    "authentication_failed",
				"message": "Invalid API key",
				"type":    "authentication_error",
			},
		}); err != nil {
			return
		}
		return
	}

	response := ChatResponse{}
	response.Choices = append(response.Choices, struct {
		Message      ChatMessageContent `json:"message"`
		Index        int                `json:"index"`
		FinishReason string             `json:"finish_reason"`
	}{
		Message:      ChatMessageContent{Role: "assistant", Content: `{"text":"apple","confidence":0.9}`},
		FinishReason: "stop",
	})

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(response); err != nil {
		return
	}
}

// TestClient_ChatVision exercises the vision chat completion path: happy-path
// wire format (system as plain string, user as image_url+text content array,
// base64 data URI round-trip), options, auth error mapping, the empty-image
// guard, and the transient-retry loop.
func TestClient_ChatVision(t *testing.T) {
	tests := []struct {
		name        string
		handler     http.HandlerFunc
		image       []byte
		mimeType    string
		opts        []ChatOption
		key         string
		wantErr     error
		wantText    string
		validateReq func(*testing.T, *capturedVision)
	}{
		{
			name:     "successful completion with default model and doc wire shape",
			handler:  defaultVisionHandler,
			image:    visionTestImage,
			mimeType: "image/jpeg",
			opts:     []ChatOption{},
			wantText: `{"text":"apple","confidence":0.9}`,
			validateReq: func(t *testing.T, got *capturedVision) {
				if got.Model != DefaultChatModel {
					t.Errorf("expected default model %q, got %q", DefaultChatModel, got.Model)
				}
				if len(got.Messages) != 2 {
					t.Fatalf("expected 2 messages (system+user), got %d", len(got.Messages))
				}

				// System message stays a plain string (official docs shape).
				sys := got.Messages[0]
				if sys.Role != "system" {
					t.Errorf("messages[0] role = %q, want system", sys.Role)
				}
				var sysText string
				if err := json.Unmarshal(sys.Content, &sysText); err != nil {
					t.Fatalf("system content is not a JSON string: %v (raw: %s)", err, sys.Content)
				}
				if sysText != "read the photo" {
					t.Errorf("system content = %q, want %q", sysText, "read the photo")
				}

				// User message carries the content array: image first, then text.
				usr := got.Messages[1]
				if usr.Role != "user" {
					t.Errorf("messages[1] role = %q, want user", usr.Role)
				}
				var parts []ChatContentPart
				if err := json.Unmarshal(usr.Content, &parts); err != nil {
					t.Fatalf("user content is not a parts array: %v (raw: %s)", err, usr.Content)
				}
				if len(parts) != 2 {
					t.Fatalf("expected 2 content parts, got %d", len(parts))
				}
				if parts[0].Type != "image_url" || parts[0].ImageURL == nil {
					t.Fatalf("parts[0] = %+v, want image_url part first", parts[0])
				}
				const prefix = "data:image/jpeg;base64,"
				if !strings.HasPrefix(parts[0].ImageURL.URL, prefix) {
					t.Errorf("image url = %q, want data-uri prefix %q", parts[0].ImageURL.URL, prefix)
				}
				decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(parts[0].ImageURL.URL, prefix))
				if err != nil {
					t.Fatalf("image data-uri body is not valid base64: %v", err)
				}
				if !bytes.Equal(decoded, visionTestImage) {
					t.Errorf("image bytes did not round-trip: got %d bytes, want %d", len(decoded), len(visionTestImage))
				}
				if parts[1].Type != "text" || parts[1].Text != "what is written?" {
					t.Errorf("parts[1] = %+v, want text part %q", parts[1], "what is written?")
				}
			},
		},
		{
			name:     "options applied (model, max tokens)",
			handler:  defaultVisionHandler,
			image:    visionTestImage,
			mimeType: "image/jpeg",
			opts:     []ChatOption{WithChatModel("mimo-v2.5-pro"), WithMaxTokens(128)},
			wantText: `{"text":"apple","confidence":0.9}`,
			validateReq: func(t *testing.T, got *capturedVision) {
				if got.Model != "mimo-v2.5-pro" {
					t.Errorf("expected model override, got %q", got.Model)
				}
				if got.MaxTokens != 128 {
					t.Errorf("expected MaxTokens 128, got %d", got.MaxTokens)
				}
			},
		},
		{
			name:     "png mime is honored in the data uri",
			handler:  defaultVisionHandler,
			image:    []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A},
			mimeType: "image/png",
			opts:     []ChatOption{},
			wantText: `{"text":"apple","confidence":0.9}`,
			validateReq: func(t *testing.T, got *capturedVision) {
				var parts []ChatContentPart
				if err := json.Unmarshal(got.Messages[1].Content, &parts); err != nil {
					t.Fatalf("user content is not a parts array: %v", err)
				}
				if !strings.HasPrefix(parts[0].ImageURL.URL, "data:image/png;base64,") {
					t.Errorf("image url = %q, want png data-uri prefix", parts[0].ImageURL.URL)
				}
			},
		},
		{
			name:     "authentication error is non-retryable",
			handler:  defaultVisionHandler,
			image:    visionTestImage,
			mimeType: "image/jpeg",
			opts:     []ChatOption{},
			key:      "wrong-key",
			wantErr:  ErrAuthentication,
		},
		{
			name:     "empty image rejected before HTTP",
			handler:  defaultVisionHandler,
			image:    nil,
			mimeType: "image/jpeg",
			opts:     []ChatOption{},
			wantErr:  ErrInvalidResponse,
			validateReq: func(t *testing.T, got *capturedVision) {
				if got != nil {
					t.Errorf("expected no HTTP call for empty image, got request %+v", got)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := tt.key
			if key == "" {
				key = "test-api-key"
			}

			var captured *capturedVision
			var calls int32
			wrapHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				atomic.AddInt32(&calls, 1)
				body, _ := io.ReadAll(r.Body)
				captured = decodeCapturedVision(t, body)
				r.Body = io.NopCloser(bytes.NewReader(body))
				tt.handler(w, r)
			})

			server := httptest.NewServer(wrapHandler)
			defer server.Close()

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

			resp, err := client.ChatVision(context.Background(), "read the photo", "what is written?", tt.image, tt.mimeType, tt.opts...)

			if tt.wantErr != nil {
				if err == nil {
					t.Fatalf("ChatVision() expected error %v, got nil", tt.wantErr)
				}
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("ChatVision() expected error %v, got %v", tt.wantErr, err)
				}
				if tt.validateReq != nil {
					tt.validateReq(t, captured)
				}
				return
			}
			if err != nil {
				t.Fatalf("ChatVision() unexpected error: %v", err)
			}
			if got := resp.Text(); got != tt.wantText {
				t.Errorf("ChatVision() text = %q, want %q", got, tt.wantText)
			}
			if tt.validateReq != nil {
				tt.validateReq(t, captured)
			}
		})
	}
}

// TestClient_ChatVision_RetriesTransientErrors pins the retry loop: 503 then
// 200 succeeds with two calls.
func TestClient_ChatVision_RetriesTransientErrors(t *testing.T) {
	var calls int32
	handler := func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			serviceUnavailableHandler(w, r)
			return
		}
		defaultVisionHandler(w, r)
	}

	server := httptest.NewServer(http.HandlerFunc(handler))
	defer server.Close()

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

	resp, err := client.ChatVision(context.Background(), "read", "what is written?", visionTestImage, "image/jpeg")
	if err != nil {
		t.Fatalf("ChatVision() unexpected error after retry: %v", err)
	}
	if resp.Text() == "" {
		t.Error("expected non-empty text after retry")
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("expected 2 calls (1 failure + 1 success), got %d", got)
	}
}

// capturedVision is the decoded shape of a ChatVision request for assertions:
// message content stays raw because it is polymorphic (string for system,
// parts array for user).
type capturedVision struct {
	Model     string `json:"model"`
	MaxTokens int    `json:"max_tokens"`
	Messages  []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"messages"`
}

// decodeCapturedVision decodes a captured request body; returns nil for an
// empty body (the no-HTTP-call case).
func decodeCapturedVision(t *testing.T, body []byte) *capturedVision {
	t.Helper()
	if len(body) == 0 {
		return nil
	}
	var got capturedVision
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("captured request is not valid JSON: %v (%s)", err, body)
	}
	return &got
}
