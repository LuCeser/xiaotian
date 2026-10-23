package main

// Live smoke test for mimoChatter with thinking disabled (issue #45), the
// voice-side twin of pkg/mimo's chat_tools_live_test.go pattern. Unlike
// voice_live_test.go (full TTS→ASR→LLM pipeline over the socket), this file
// probes the chatter leg directly: the production mimoChatter adapter wrapped
// around a recording client, feeding the real LLMClassifier — asserting (a) no
// reasoning_content came back on any call, (b) intent classification stays
// usable (the issue's acceptance item: thinking off must not regress quality),
// and (c) measured latency per call. Gated on the shared explicit live switch
// via liveAPIKey (XIAOTIAN_LIVE_TESTS=1 + MIMO_API_KEY); otherwise the test
// skips, so offline runs stay green. Each run costs one flash-model chat call
// per case.

import (
	"context"
	"testing"
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/mimo"
	"github.com/xiaotian/home-tutor-agent/pkg/voice"
)

// recordingChatClient wraps a mimo.Client and records each Chat response's
// reasoning_content and finish_reason. The voice.Chatter seam only surfaces
// the final text, so this is how the live test observes the wire-level
// thinking state THROUGH the production mimoChatter adapter (everything else
// delegates untouched). No mutex: the live test drives it synchronously from
// one goroutine — mimo's Chat is a blocking call.
type recordingChatClient struct {
	mimo.Client
	reasoning []string
	finishes  []string
}

// Chat delegates to the wrapped client and records the thinking evidence.
func (r *recordingChatClient) Chat(ctx context.Context, messages []mimo.Message, opts ...mimo.ChatOption) (mimo.ChatResponse, error) {
	resp, err := r.Client.Chat(ctx, messages, opts...)
	if err == nil && len(resp.Choices) > 0 {
		r.reasoning = append(r.reasoning, resp.Choices[0].Message.ReasoningContent)
		r.finishes = append(r.finishes, resp.Choices[0].FinishReason)
	}
	return resp, err
}

// TestMimoChatter_LiveThinkingDisabled drives the real chat model through
// mimoChatter (thinking off since issue #45) for three transcripts that must
// land in three different intents, then asserts no reasoning_content was
// returned on any call. Latency is logged per case — the issue's acceptance
// asks for a measured record, not a threshold.
func TestMimoChatter_LiveThinkingDisabled(t *testing.T) {
	apiKey := liveAPIKey(t)

	client, err := mimo.NewClient(mimo.ClientConfig{
		APIKey:  apiKey,
		BaseURL: infra.DefaultConfig().Voice.BaseURL,
		Timeout: 45 * time.Second,
	}, infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("mimo client construction failed: %v", err)
	}
	defer func() { _ = client.Close() }()

	// The exact adapter composition main.go performs; the recorder only
	// observes, it changes nothing on the wire.
	rec := &recordingChatClient{Client: client}
	classifier := voice.NewLLMClassifier(mimoChatter(rec, infra.DefaultConfig().Voice.ChatModel))

	cases := []struct {
		name       string
		transcript string
		want       voice.Action
	}{
		{"difficulty complaint → cannot_write", "老师我觉得这个有点难", voice.ActionCannotWrite},
		{"varied pause ask → pause", "可以停一停吗我想喝口水", voice.ActionPause},
		{"chitchat → none", "今天天气真好啊", voice.ActionNone},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()

			start := time.Now()
			intent, err := classifier.Classify(ctx, tc.transcript)
			elapsed := time.Since(start)
			t.Logf("live classify %q → action=%q confidence=%.2f in %dms",
				tc.transcript, intent.Action, intent.Confidence, elapsed.Milliseconds())
			if err != nil {
				t.Fatalf("Classify() error: %v (reply not usable — thinking-off regression?)", err)
			}
			if intent.Action != tc.want {
				t.Errorf("live action = %q, want %q", intent.Action, tc.want)
			}
		})
	}

	// Issue #45 core: with thinking disabled no call may carry reasoning
	// content — an empty content would mean the Text() fallback silently fed
	// half-formed reasoning to the classifier again.
	if len(rec.reasoning) != len(cases) {
		t.Fatalf("recorded %d chat calls, want %d", len(rec.reasoning), len(cases))
	}
	for i, reasoning := range rec.reasoning {
		if reasoning != "" {
			t.Errorf("call %d returned reasoning_content (%d chars) despite thinking disabled: %.80s",
				i, len(reasoning), reasoning)
		}
	}
	t.Logf("finish_reasons across %d calls: %v", len(rec.finishes), rec.finishes)
}
