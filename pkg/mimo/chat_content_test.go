package mimo

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/pkg/errors"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
)

// TestChatMessageContentUnmarshal covers the wire shapes of assistant
// message.content. String content already round-trips through the client
// tests; these lock null, multimodal arrays, and rejected types.
func TestChatMessageContentUnmarshal(t *testing.T) {
	toolCalls := `[{"id":"call_abc","type":"function","function":{"name":"advance_word","arguments":"{\"lesson_id\":\"L1\"}"}}]`

	tests := []struct {
		name      string
		raw       string
		want      string
		wantRole  string
		wantThink string
		wantTools int
		wantErr   bool
	}{
		{
			name:     "string",
			raw:      `{"role":"assistant","content":"{\"text\":\"apple\"}"}`,
			want:     `{"text":"apple"}`,
			wantRole: "assistant",
		},
		{
			name:      "null falls through to reasoning",
			raw:       `{"role":"assistant","content":null,"reasoning_content":"thought"}`,
			want:      "",
			wantRole:  "assistant",
			wantThink: "thought",
		},
		{
			name:     "absent content",
			raw:      `{"role":"assistant"}`,
			want:     "",
			wantRole: "assistant",
		},
		{
			name:     "text parts concatenated, image part skipped",
			raw:      `{"role":"assistant","content":[{"type":"text","text":"{\"text\":\"apple\""},{"type":"image_url","image_url":{"url":"data:image/png;base64,abcd"}},{"type":"text","text":",\"confidence\":0.9}"}]}`,
			want:     `{"text":"apple","confidence":0.9}`,
			wantRole: "assistant",
		},
		{
			name:     "array of strings",
			raw:      `{"role":"assistant","content":["apple"," banana"]}`,
			want:     "apple banana",
			wantRole: "assistant",
		},
		{
			name:     "unknown object with text",
			raw:      `{"role":"assistant","content":[{"type":"future_text","text":"apple"}]}`,
			want:     "apple",
			wantRole: "assistant",
		},
		{
			name:      "null content keeps tool calls",
			raw:       `{"role":"assistant","content":null,"tool_calls":` + toolCalls + `}`,
			want:      "",
			wantRole:  "assistant",
			wantTools: 1,
		},
		{
			name:    "number is rejected",
			raw:     `{"role":"assistant","content":1}`,
			wantErr: true,
		},
		{
			name:    "object is rejected",
			raw:     `{"role":"assistant","content":{"text":"apple"}}`,
			wantErr: true,
		},
		{
			name:    "number inside the array is rejected",
			raw:     `{"role":"assistant","content":[{"type":"text","text":"apple"}, 1]}`,
			wantErr: true,
		},
		{
			name:    "unknown object without text is rejected",
			raw:     `{"role":"assistant","content":[{"type":"video_url","video_url":{"url":"https://example.invalid/video"}}]}`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var msg ChatMessageContent
			err := json.Unmarshal([]byte(tt.raw), &msg)
			if tt.wantErr {
				if err == nil {
					t.Fatal("Unmarshal() expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("Unmarshal() unexpected error: %v", err)
			}
			if msg.Content != tt.want {
				t.Errorf("Content = %q, want %q", msg.Content, tt.want)
			}
			if msg.Role != tt.wantRole {
				t.Errorf("Role = %q, want %q", msg.Role, tt.wantRole)
			}
			if msg.ReasoningContent != tt.wantThink {
				t.Errorf("ReasoningContent = %q, want %q", msg.ReasoningContent, tt.wantThink)
			}
			if len(msg.ToolCalls) != tt.wantTools {
				t.Errorf("len(ToolCalls) = %d, want %d", len(msg.ToolCalls), tt.wantTools)
			}
			if tt.wantTools == 1 && msg.ToolCalls[0].Function.Name != "advance_word" {
				t.Errorf("ToolCalls[0].Function.Name = %q, want advance_word", msg.ToolCalls[0].Function.Name)
			}
		})
	}
}

// TestClient_ChatContentShapes drives both chat completions through httptest:
// null content and a multimodal array must decode, and a garbage body must
// stay ErrInvalidResponse while the truncated body is logged without secrets.
func TestClient_ChatContentShapes(t *testing.T) {
	const apiKey = "test-api-key"
	const bearer = "super-secret-bearer-token"
	garbage := "<<<not-json " + apiKey + " Bearer " + bearer + " " +
		strings.Repeat("Z", 3000) + "TAILMARKER>>>"

	tests := []struct {
		name    string
		body    string
		want    string
		wantErr string
	}{
		{
			name: "null content",
			body: `{"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":null,"reasoning_content":"thought"}}]}`,
			want: "thought",
		},
		{
			name: "array content",
			body: `{"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":[{"type":"text","text":"apple"},{"type":"image_url","image_url":{"url":"data:image/png;base64,abcd"}},{"type":"text","text":" banana"}]}}]}`,
			want: "apple banana",
		},
		{
			name:    "garbage body",
			body:    garbage,
			wantErr: "invalid API response",
		},
	}

	callers := []struct {
		name string
		op   string
		wrap string
		call func(Client) (ChatResponse, error)
	}{
		{
			name: "chat",
			op:   "chat",
			wrap: "failed to decode chat response: invalid API response",
			call: func(c Client) (ChatResponse, error) {
				return c.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}})
			},
		},
		{
			name: "vision chat",
			op:   "vision chat",
			wrap: "failed to decode vision chat response: invalid API response",
			call: func(c Client) (ChatResponse, error) {
				return c.ChatVision(context.Background(), "read", "what is written?", visionTestImage, "image/jpeg")
			},
		},
	}

	for _, caller := range callers {
		t.Run(caller.name, func(t *testing.T) {
			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					logger := newTestLogger()
					client := newContentShapeClient(t, logger, apiKey, tt.body)
					defer client.Close()

					resp, err := caller.call(client)
					if tt.wantErr != "" {
						if err == nil {
							t.Fatal("expected error, got nil")
						}
						if !errors.Is(err, ErrInvalidResponse) {
							t.Fatalf("error = %v, want errors.Is ErrInvalidResponse", err)
						}
						if err.Error() != caller.wrap {
							t.Errorf("error = %q, want %q", err.Error(), caller.wrap)
						}
						if strings.Contains(err.Error(), apiKey) || strings.Contains(err.Error(), bearer) {
							t.Errorf("error string leaked a secret: %q", err.Error())
						}
						prefix := loggedStringField(t, logger, "failed to decode chat completion response", "body_prefix")
						if !strings.Contains(prefix, "<<<not-json") {
							t.Errorf("body_prefix = %q, want the garbage prefix", prefix)
						}
						if strings.Contains(prefix, apiKey) || strings.Contains(prefix, bearer) || strings.Contains(prefix, "TAILMARKER") {
							t.Errorf("body_prefix leaked a secret or the truncated tail: %q", prefix)
						}
						if len(prefix) > maxLoggedResponseBytes {
							t.Errorf("body_prefix length = %d, want <= %d", len(prefix), maxLoggedResponseBytes)
						}
						if got := loggedStringField(t, logger, "failed to decode chat completion response", "operation"); got != caller.op {
							t.Errorf("operation = %q, want %q", got, caller.op)
						}
						assertLogsOmit(t, logger, apiKey, bearer)
						return
					}
					if err != nil {
						t.Fatalf("unexpected error: %v", err)
					}
					if got := resp.Text(); got != tt.want {
						t.Errorf("Text() = %q, want %q", got, tt.want)
					}
					if _, ok := findLog(logger, "failed to decode chat completion response"); ok {
						t.Error("successful decode logged a response body")
					}
				})
			}
		})
	}
}

// TestTruncateResponseForLog pins the boundary behavior the httptest fixture
// only samples: a key straddling the cut is fully redacted, and a multibyte
// cut does not leave an invalid tail.
func TestTruncateResponseForLog(t *testing.T) {
	const apiKey = "test-api-key"
	body := append([]byte(strings.Repeat("a", maxLoggedResponseBytes-8)), []byte(apiKey+"TAILMARKER")...)
	got := truncateResponseForLog(body, apiKey)
	// "test-api" is the slice of the key that sits inside the first 2KiB.
	// Without the overlap window, ReplaceAll would miss the full key and
	// this prefix would survive.
	if strings.Contains(got, "test-api") {
		t.Errorf("prefix still contains the API key: %q", got)
	}
	if strings.Contains(got, "TAILMARKER") {
		t.Errorf("prefix still contains the tail past the window: %q", got)
	}
	if len(got) > maxLoggedResponseBytes {
		t.Errorf("len = %d, want <= %d", len(got), maxLoggedResponseBytes)
	}

	runes := strings.Repeat("你", 2000)
	cut := truncateResponseForLog([]byte(runes), "")
	if !utf8.ValidString(cut) {
		t.Errorf("cut multibyte prefix is not valid UTF-8: %q", cut)
	}
	if len(cut) > maxLoggedResponseBytes {
		t.Errorf("multibyte len = %d, want <= %d", len(cut), maxLoggedResponseBytes)
	}
}

func TestTruncateResponseForLogRedactsShortAPIKey(t *testing.T) {
	const apiKey = "short"
	got := truncateResponseForLog([]byte("response echoed "+apiKey), apiKey)
	if strings.Contains(got, apiKey) {
		t.Errorf("body prefix leaked the API key: %q", got)
	}
}

func TestReadAndDecodeChatResponseLimitsBody(t *testing.T) {
	body := `{"choices":[{"message":{"content":"` +
		strings.Repeat("Z", maxChatResponseBytes) + `"}}]}`
	reader := &countingReader{Reader: strings.NewReader(body)}
	logger := newTestLogger()
	client := &mimoClient{logger: logger, config: ClientConfig{APIKey: "short"}}

	_, err := client.readAndDecodeChatResponse(
		&http.Response{Body: io.NopCloser(reader)}, "chat",
	)
	if err == nil {
		t.Fatal("readAndDecodeChatResponse() expected oversized response error")
	}
	if reader.bytesRead != maxChatResponseBytes+1 {
		t.Errorf("body bytes read = %d, want %d", reader.bytesRead, maxChatResponseBytes+1)
	}
	prefix := loggedStringField(t, logger, "failed to decode chat completion response", "body_prefix")
	if !strings.HasPrefix(prefix, `{"choices"`) {
		t.Errorf("body_prefix = %q, want response prefix", prefix)
	}
}

func TestClientChatRejectsOversizedResponse(t *testing.T) {
	body := `{"choices":[{"message":{"content":"` +
		strings.Repeat("Z", maxChatResponseBytes) + `"}}]}`
	client := newContentShapeClient(t, newTestLogger(), "short", body)
	defer client.Close()

	_, err := client.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}})
	if !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("Chat() error = %v, want errors.Is ErrInvalidResponse", err)
	}
}

func TestClientChatRejectsUnknownContentObject(t *testing.T) {
	body := `{"choices":[{"message":{"role":"assistant","content":[{"type":"unknown"}]}}]}`
	client := newContentShapeClient(t, newTestLogger(), "short", body)
	defer client.Close()

	_, err := client.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}})
	if !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("Chat() error = %v, want errors.Is ErrInvalidResponse", err)
	}
}

type countingReader struct {
	io.Reader
	bytesRead int
}

func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.bytesRead += n
	return n, err
}

func newContentShapeClient(t *testing.T, logger *testLogger, apiKey, body string) Client {
	t.Helper()
	server, cleanup := mockTTSServer(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer "+apiKey {
			t.Errorf("Authorization = %q, want bearer API key", auth)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte(body)); err != nil {
			t.Errorf("write response: %v", err)
		}
	})
	t.Cleanup(cleanup)

	client, err := NewClient(ClientConfig{
		APIKey:     apiKey,
		BaseURL:    server.URL,
		Timeout:    5 * time.Second,
		MaxRetries: 1,
		RetryDelay: time.Millisecond,
	}, logger)
	if err != nil {
		t.Fatalf("NewClient() failed: %v", err)
	}
	return client
}

func loggedStringField(t *testing.T, logger *testLogger, msg, key string) string {
	t.Helper()
	entry, ok := findLog(logger, msg)
	if !ok {
		t.Fatalf("no log entry %q in %+v", msg, logger.logs)
	}
	fields, _ := entry["fields"].([]infra.Field)
	for _, f := range fields {
		if f.Key == key {
			s, _ := f.Value.(string)
			return s
		}
	}
	t.Fatalf("log %q has no field %q: %+v", msg, key, fields)
	return ""
}

func findLog(logger *testLogger, msg string) (map[string]interface{}, bool) {
	for _, entry := range logger.logs {
		if entry["msg"] == msg {
			return entry, true
		}
	}
	return nil, false
}

func assertLogsOmit(t *testing.T, logger *testLogger, secrets ...string) {
	t.Helper()
	for _, entry := range logger.logs {
		blob := entry["msg"].(string)
		fields, _ := entry["fields"].([]infra.Field)
		for _, f := range fields {
			blob += " " + f.Key + "=" + stringifyField(f.Value)
		}
		for _, secret := range secrets {
			if strings.Contains(blob, secret) {
				t.Errorf("log entry leaked %q: %s", secret, blob)
			}
		}
	}
}

func stringifyField(v interface{}) string {
	s, ok := v.(string)
	if ok {
		return s
	}
	return ""
}
