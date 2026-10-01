// providers_fake_test.go — scripted fake ASR/LLM/TTS providers shared by this
// package's tests and by 13-04's turn-manager tests (same package). They
// implement the providers.go interfaces with zero provider wire detail: PCM
// in and out is always bare s16le, never base64, never a container.
package rt

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/mimo"
)

// ---------------------------------------------------------------------------
// fakeASR
// ---------------------------------------------------------------------------

// asrScript is one scripted Transcribe outcome: exactly one of text / err.
type asrScript struct {
	text string
	err  error
}

// fakeASR answers Transcribe calls from a FIFO script queue and records the
// byte counts it was fed.
type fakeASR struct {
	mu       sync.Mutex
	script   []asrScript
	fedBytes []int // len(pcm) per call, in call order
}

func newFakeASR(script ...asrScript) *fakeASR {
	return &fakeASR{script: append([]asrScript(nil), script...)}
}

// Transcribe implements ASRProvider.
func (f *fakeASR) Transcribe(_ context.Context, pcm []byte) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fedBytes = append(f.fedBytes, len(pcm))
	if len(f.script) == 0 {
		return "", fmt.Errorf("fakeASR: script exhausted")
	}
	head := f.script[0]
	f.script = f.script[1:]
	if head.err != nil {
		return "", head.err
	}
	return head.text, nil
}

// ---------------------------------------------------------------------------
// fakeLLM
// ---------------------------------------------------------------------------

// llmScript is one scripted Chat outcome. Exactly one payload kind is set:
// resp (a prebuilt Go value), wire (raw provider response JSON, parsed
// lazily at call time), or err.
//
// Wire entries exist because 13-03 and 13-02 (which adds the
// tool_calls/finish_reason response fields to mimo.ChatResponse) run in the
// same wave on separate worktrees: a wire script stores the tool_calls
// sample VERBATIM today, and the lazy parse means the same script yields a
// fully populated ChatResponse the moment 13-02's fields merge — no fake
// code change, no merge conflict in pkg/mimo.
type llmScript struct {
	resp mimo.ChatResponse
	wire string
	err  error
}

// llmCall records one Chat invocation for post-hoc assertion (13-04 asserts
// the tool-loop回填 shape here).
type llmCall struct {
	messages []mimo.Message
	opts     int
}

// fakeLLM answers Chat calls from a FIFO script queue and records every
// received messages slice (deep-copied) plus the option count.
type fakeLLM struct {
	mu     sync.Mutex
	script []llmScript
	calls  []llmCall
}

func newFakeLLM(script ...llmScript) *fakeLLM {
	return &fakeLLM{script: append([]llmScript(nil), script...)}
}

// Chat implements LLMProvider.
func (f *fakeLLM) Chat(_ context.Context, messages []mimo.Message, opts ...mimo.ChatOption) (mimo.ChatResponse, error) {
	f.mu.Lock()
	if len(f.script) == 0 {
		f.mu.Unlock()
		return mimo.ChatResponse{}, fmt.Errorf("fakeLLM: script exhausted")
	}
	head := f.script[0]
	f.script = f.script[1:]
	cp := make([]mimo.Message, len(messages))
	copy(cp, messages)
	f.calls = append(f.calls, llmCall{messages: cp, opts: len(opts)})
	f.mu.Unlock()

	if head.err != nil {
		return mimo.ChatResponse{}, head.err
	}
	if head.wire != "" {
		var resp mimo.ChatResponse
		if err := json.Unmarshal([]byte(head.wire), &resp); err != nil {
			return mimo.ChatResponse{}, fmt.Errorf("fakeLLM: bad wire script: %w", err)
		}
		return resp, nil
	}
	return head.resp, nil
}

func (f *fakeLLM) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// toolCallsWireSample is the RESEARCH Pattern 4 provider response sample:
// an assistant message carrying one advance_word tool_call with
// finish_reason "tool_calls". Kept verbatim so the lazy parse picks up
// whatever fields the compiled mimo.ChatResponse carries.
const toolCallsWireSample = `{"choices":[{"index":0,"message":{"role":"assistant","content":"","tool_calls":[{"id":"call_abc","type":"function","function":{"name":"advance_word","arguments":"{\"lesson_id\":\"L1\",\"expected_version\":3}"}}]},"finish_reason":"tool_calls"}]}`

// ---------------------------------------------------------------------------
// fakeTTS
// ---------------------------------------------------------------------------

// fakeTTS answers SynthesizeStream calls by handing the callback bare PCM
// chunks (chunkSize bytes each, chunkCount per call, the last flagged
// isFinal). It supports an injected per-chunk delay and a
// stream-until-cancelled mode for interrupt tests (13-04), and records the
// text of every call. No base64, no WAV header — AudioChunkFunc's bare-PCM
// contract is exactly what the fake exercises.
type fakeTTS struct {
	mu sync.Mutex

	// chunkCount is the number of chunks per call (>=1) in normal mode.
	chunkCount int
	// chunkSize is the byte size of each chunk (bare s16le payload).
	chunkSize int
	// delay is injected before every chunk; ctx cancellation aborts.
	delay time.Duration
	// streamUntilCancel switches to open-ended production: chunks keep
	// flowing until ctx is cancelled or the callback errors (interrupt
	// semantics — isFinal never arrives on the cancelled path).
	streamUntilCancel bool
	// failWith, when non-nil, makes every call return it immediately.
	failWith error

	texts []string // recorded per call
}

func newFakeTTS(chunkCount, chunkSize int) *fakeTTS {
	return &fakeTTS{chunkCount: chunkCount, chunkSize: chunkSize}
}

// SynthesizeStream implements TTSSynthesizer.
func (f *fakeTTS) SynthesizeStream(ctx context.Context, text string, onAudio AudioChunkFunc, _ ...mimo.TTSOption) (*mimo.StreamResult, error) {
	f.mu.Lock()
	f.texts = append(f.texts, text)
	failWith := f.failWith
	chunkCount, chunkSize, delay, untilCancel := f.chunkCount, f.chunkSize, f.delay, f.streamUntilCancel
	f.mu.Unlock()

	if failWith != nil {
		return nil, failWith
	}

	// A recognizable bare-PCM payload: a mild sawtooth so tests can confirm
	// bytes pass through untouched (and that no RIFF/base64 wrapper appears).
	total := 0
	emit := func(isFinal bool) error {
		buf := make([]byte, chunkSize)
		for i := range buf {
			buf[i] = byte(i % 251)
		}
		if err := onAudio(buf, isFinal); err != nil {
			return err
		}
		total++
		return nil
	}

	wait := func() error {
		if delay <= 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
			return nil
		}
	}

	if untilCancel {
		for {
			if err := wait(); err != nil {
				return &mimo.StreamResult{ChunkCount: total}, err
			}
			if err := emit(false); err != nil {
				return &mimo.StreamResult{ChunkCount: total}, err
			}
			if err := ctx.Err(); err != nil {
				return &mimo.StreamResult{ChunkCount: total}, err
			}
		}
	}

	for i := 0; i < chunkCount; i++ {
		if err := wait(); err != nil {
			return &mimo.StreamResult{ChunkCount: total}, err
		}
		if err := emit(i == chunkCount-1); err != nil {
			return &mimo.StreamResult{ChunkCount: total}, err
		}
	}
	// Duration estimate at the declared output rate (s16le mono → 2 bytes
	// per sample).
	ms := 0
	if chunkSize > 0 {
		ms = total * chunkSize / 2 * 1000 / TTSOutputSampleRate
	}
	return &mimo.StreamResult{TotalDuration: ms, ChunkCount: total}, nil
}

func (f *fakeTTS) recordedTexts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.texts...)
}

// ---------------------------------------------------------------------------
// Fake provider tests
// ---------------------------------------------------------------------------

func TestFakesASRScriptQueue(t *testing.T) {
	wantErr := fmt.Errorf("asr upstream 500")
	asr := newFakeASR(asrScript{text: "苹果"}, asrScript{err: wantErr})

	got1, err := asr.Transcribe(context.Background(), []byte("0123456789abcdef"))
	if err != nil || got1 != "苹果" {
		t.Fatalf("first Transcribe = (%q, %v), want (苹果, nil)", got1, err)
	}
	got2, err := asr.Transcribe(context.Background(), []byte{0, 1, 2})
	if err == nil || got2 != "" {
		t.Fatalf("second Transcribe = (%q, %v), want error", got2, err)
	}

	if len(asr.fedBytes) != 2 || asr.fedBytes[0] != 16 || asr.fedBytes[1] != 3 {
		t.Errorf("fedBytes = %v, want [16 3]", asr.fedBytes)
	}
}

func TestFakesASRScriptExhausted(t *testing.T) {
	asr := newFakeASR()
	if _, err := asr.Transcribe(context.Background(), nil); err == nil {
		t.Fatal("empty script returned no error, want script-exhausted error")
	}
}

// wireChatResponse builds a mimo.ChatResponse from provider wire JSON,
// keeping tests free of brittle anonymous-struct literals.
func wireChatResponse(t *testing.T, raw string) mimo.ChatResponse {
	t.Helper()
	var resp mimo.ChatResponse
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("bad wire fixture %q: %v", raw, err)
	}
	return resp
}

func TestFakesLLMRecordsMessagesAndOpts(t *testing.T) {
	llm := newFakeLLM(
		llmScript{resp: wireChatResponse(t, `{"choices":[{"message":{"role":"assistant","content":"第一跳"}}]}`)},
		llmScript{resp: wireChatResponse(t, `{"choices":[{"message":{"role":"assistant","content":"第二跳"}}]}`)},
	)

	msgs := []mimo.Message{{Role: "user", Content: "你好"}}
	resp, err := llm.Chat(context.Background(), msgs)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if got := resp.Text(); got != "第一跳" {
		t.Errorf("first Chat text = %q, want 第一跳", got)
	}

	// Mutating the caller's slice after the call must not alter the record
	// (deep copy at capture time).
	msgs[0].Content = "mutated"
	if llm.calls[0].messages[0].Content != "你好" {
		t.Errorf("recorded message mutated: %+v", llm.calls[0].messages)
	}

	if _, err := llm.Chat(context.Background(), nil); err != nil {
		t.Fatalf("second Chat: %v", err)
	}
	if n := llm.callCount(); n != 2 {
		t.Errorf("call count = %d, want 2", n)
	}
}

// TestFakesLLMWireToolCallsScript: a finish_reason=="tool_calls" script is
// configured from the provider wire sample (the exact shape 13-04's tool
// loop must consume). Wave-1 boundary: the pre-13-02 mimo.ChatResponse has
// no ToolCalls/FinishReason fields yet, so today's assertions cover the
// fields that exist (role/content) and the wire retention; the moment
// 13-02's fields merge, the same lazy parse populates them and 13-04
// asserts them directly.
func TestFakesLLMWireToolCallsScript(t *testing.T) {
	llm := newFakeLLM(llmScript{wire: toolCallsWireSample})

	resp, err := llm.Chat(context.Background(), []mimo.Message{{Role: "user", Content: "写完了"}})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if len(resp.Choices) != 1 {
		t.Fatalf("choices = %d, want 1", len(resp.Choices))
	}
	if resp.Choices[0].Message.Role != "assistant" {
		t.Errorf("role = %q, want assistant", resp.Choices[0].Message.Role)
	}
	if resp.Choices[0].Message.Content != "" {
		t.Errorf("content = %q, want empty (tool_calls carry the payload)", resp.Choices[0].Message.Content)
	}
	if !strings.Contains(toolCallsWireSample, `"finish_reason":"tool_calls"`) {
		t.Error("wire sample lost its finish_reason=tool_calls marker")
	}
	if !strings.Contains(toolCallsWireSample, `"function":{"name":"advance_word"`) {
		t.Error("wire sample lost its advance_word tool_call")
	}
}

func TestFakesLLMScriptExhausted(t *testing.T) {
	llm := newFakeLLM()
	if _, err := llm.Chat(context.Background(), nil); err == nil {
		t.Fatal("empty script returned no error, want script-exhausted error")
	}
}

func TestFakesTTSProducesBarePCMWithFinalFlag(t *testing.T) {
	tts := newFakeTTS(3, 320)

	var mu sync.Mutex
	var chunks [][]byte
	var finals []bool
	_, err := tts.SynthesizeStream(context.Background(), "我们来写第一个词。", func(pcm []byte, isFinal bool) error {
		cp := make([]byte, len(pcm))
		copy(cp, pcm)
		mu.Lock()
		chunks = append(chunks, cp)
		finals = append(finals, isFinal)
		mu.Unlock()
		return nil
	})
	if err != nil {
		t.Fatalf("SynthesizeStream: %v", err)
	}

	if len(chunks) != 3 {
		t.Fatalf("chunks = %d, want 3", len(chunks))
	}
	wantFinals := []bool{false, false, true}
	for i, want := range wantFinals {
		if finals[i] != want {
			t.Errorf("finals[%d] = %v, want %v", i, finals[i], want)
		}
	}
	for i, c := range chunks {
		if len(c) != 320 {
			t.Errorf("chunk %d len = %d, want 320", i, len(c))
		}
		// Bare PCM assertion: no RIFF header, no base64 alphabet run —
		// the sawtooth pattern must pass through byte-identical.
		if len(c) >= 4 && string(c[:4]) == "RIFF" {
			t.Errorf("chunk %d starts with RIFF header, want bare PCM", i)
		}
		if c[0] != 0 || c[1] != 1 || c[2] != 2 {
			t.Errorf("chunk %d pattern mutated: %v", i, c[:4])
		}
	}
	if got := tts.recordedTexts(); len(got) != 1 || got[0] != "我们来写第一个词。" {
		t.Errorf("recorded texts = %v, want [我们来写第一个词。]", got)
	}
}

func TestFakesTTSFailWith(t *testing.T) {
	wantErr := fmt.Errorf("tts upstream boom")
	tts := newFakeTTS(2, 320)
	tts.failWith = wantErr

	_, err := tts.SynthesizeStream(context.Background(), "x", func([]byte, bool) error { return nil })
	if err != wantErr {
		t.Errorf("err = %v, want %v", err, wantErr)
	}
	if got := tts.recordedTexts(); len(got) != 1 {
		t.Errorf("failed call still recorded text: %v", got)
	}
}

// TestFakesTTSDelayHonorsContext: an injected delay aborts promptly when the
// context is cancelled — the interrupt path must never hang on a slow
// provider.
func TestFakesTTSDelayHonorsContext(t *testing.T) {
	tts := newFakeTTS(3, 320)
	tts.delay = 10 * time.Second

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	res, err := tts.SynthesizeStream(ctx, "x", func([]byte, bool) error { return nil })
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("cancelled context returned nil error, want ctx error")
	}
	if elapsed > 5*time.Second {
		t.Errorf("cancellation took %v, want prompt abort", elapsed)
	}
	if res != nil && res.ChunkCount != 0 {
		t.Errorf("chunk count after immediate cancel = %d, want 0", res.ChunkCount)
	}
}

// TestFakesTTSStreamUntilCancel: in open-ended mode chunks keep flowing with
// no isFinal until the context is cancelled — the shape 13-04's epoch
// cancellation tests need (stale-chunk discard on interrupt).
func TestFakesTTSStreamUntilCancel(t *testing.T) {
	tts := newFakeTTS(0, 320)
	tts.streamUntilCancel = true

	ctx, cancel := context.WithCancel(context.Background())
	var mu sync.Mutex
	var n int
	var sawFinal bool
	go func() {
		time.Sleep(120 * time.Millisecond)
		cancel()
	}()

	res, err := tts.SynthesizeStream(ctx, "长句子", func(pcm []byte, isFinal bool) error {
		mu.Lock()
		n++
		if isFinal {
			sawFinal = true
		}
		mu.Unlock()
		return nil
	})
	if err == nil {
		t.Fatal("streamUntilCancel returned nil error, want ctx error on cancel")
	}
	mu.Lock()
	gotN := n
	gotFinal := sawFinal
	mu.Unlock()
	if gotN < 2 {
		t.Errorf("chunks produced before cancel = %d, want a continuous stream (>=2)", gotN)
	}
	if gotFinal {
		t.Error("saw isFinal chunk in open-ended mode, want none before cancel")
	}
	if res.ChunkCount != gotN {
		t.Errorf("result chunk count = %d, want %d", res.ChunkCount, gotN)
	}
}
