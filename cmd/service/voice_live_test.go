package main

// Live voice-pipeline verification against the REAL Mimo services
// (Ticket 03 final gap). voice_test.go fakes the Transcriber and runs the
// classifier rules-only; this file builds the production stack — mimo client
// + mimoTranscriber + mimoChatter + the real hybrid classifier WITH the LLM
// fallback — and drives real synthesized speech through the real
// voice_command handler:
//
//	TTS("暂停") → PCM chunks over the socket → real ASR → rule fast path
//	TTS("老师我觉得这个有点难") → real ASR → real LLM classification
//
// Gated on MIMO_API_KEY (repo-root .env is loaded the same way pkg/mimo's
// TestMain does); without a key the test skips, so offline runs stay green.
// Each run costs ~3 API calls per case (1 TTS + 1 ASR + optionally 1 LLM).

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/mimo"
	"github.com/xiaotian/home-tutor-agent/pkg/voice"
	"github.com/xiaotian/home-tutor-agent/pkg/ws"
)

// liveAPIKey loads the repo-root .env and returns the Mimo key, or "" when
// absent — the caller skips. Twin of pkg/mimo's TestMain loader (live env
// wins), diverging only on empty-set vars: overridden here (Getenv == ""),
// kept by that loader (LookupEnv).
func liveAPIKey(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("live test requires real services; skipped in -short mode")
	}
	if f, err := os.Open("../../.env"); err == nil {
		defer f.Close()
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			key, value, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			key, value = strings.TrimSpace(key), strings.TrimSpace(value)
			value = strings.Trim(value, `"'`)
			if key == "MIMO_API_KEY" && os.Getenv(key) == "" {
				os.Setenv(key, value)
			}
		}
	}
	apiKey := os.Getenv("MIMO_API_KEY")
	if apiKey == "" {
		t.Skip("MIMO_API_KEY not set — live voice-pipeline test skipped")
	}
	return apiKey
}

// synthesizeCommandPCM synthesizes spoken command audio via the real TTS and
// returns raw PCM resampled to the wire format (16kHz mono signed 16-bit LE)
// that the voice pipeline's WAV wrapper declares.
func synthesizeCommandPCM(t *testing.T, client mimo.Client, text string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	// wav (not pcm16) so the true sample rate travels in the RIFF header —
	// the pcm16 path pins 24kHz in docs and the decoder trusts that.
	audio, err := client.Synthesize(ctx, text,
		mimo.WithVoice("冰糖"),
		mimo.WithAudioFormat("wav"),
	)
	if err != nil {
		t.Fatalf("live TTS for %q failed: %v", text, err)
	}
	pcm := wavPCM(audio.Samples)
	if len(pcm) == 0 {
		t.Fatalf("live TTS returned no parsable WAV (%d bytes)", len(audio.Samples))
	}
	// parseWAV already put the true header rate on the decoded result.
	if rate := audio.SampleRate; rate != 16000 {
		pcm = resamplePCM(t, pcm, rate, 16000)
	}
	return pcm
}

// wavPCM extracts the data-chunk PCM from a RIFF/WAVE byte stream. Twin of
// mimo.parseWAV's chunk walk, kept self-contained because parseWAV discards
// the data payload; the sample rate travels in DecodedAudioData.SampleRate.
func wavPCM(wav []byte) []byte {
	if len(wav) < 12 || string(wav[0:4]) != "RIFF" || string(wav[8:12]) != "WAVE" {
		return nil
	}
	pos := 12
	for pos+8 <= len(wav) {
		id := string(wav[pos : pos+4])
		size := int(binary.LittleEndian.Uint32(wav[pos+4 : pos+8]))
		body := pos + 8
		if size < 0 || body+size > len(wav) {
			break
		}
		if id == "data" {
			return wav[body : body+size]
		}
		pos = body + size + (size & 1)
	}
	return nil
}

// resamplePCM linearly resamples 16-bit LE mono PCM between rates.
func resamplePCM(t *testing.T, in []byte, from, to int) []byte {
	t.Helper()
	n := len(in) / 2
	ratio := float64(from) / float64(to)
	outN := int(float64(n) / ratio)
	out := make([]byte, outN*2)
	for j := 0; j < outN; j++ {
		src := float64(j) * ratio
		i0 := int(src)
		frac := src - float64(i0)
		s0 := int16(binary.LittleEndian.Uint16(in[i0*2:])) //nolint:gosec // G115: s16le PCM sample; the high bit is the sign
		var s1 int16
		if (i0+1)*2+2 <= len(in) {
			s1 = int16(binary.LittleEndian.Uint16(in[(i0+1)*2:])) //nolint:gosec // G115: s16le PCM sample; the high bit is the sign
		} else {
			s1 = s0
		}
		v := int16(float64(s0) + (float64(s1)-float64(s0))*frac)
		binary.LittleEndian.PutUint16(out[j*2:], uint16(v)) //nolint:gosec // G115: s16le PCM bit pattern, not a numeric range check
	}
	return out
}

// streamPCMChunks ships PCM as 640-byte audio frames (final chunk padded),
// exactly the wire contract the frontend VoiceCommandController implements.
func streamPCMChunks(t *testing.T, conn *websocket.Conn, pcm []byte, startSeq uint32) {
	t.Helper()
	for off, seq := 0, startSeq; off < len(pcm); off, seq = off+ws.AudioFrameSize, seq+1 {
		end := min(off+ws.AudioFrameSize, len(pcm))
		chunk := pcm[off:end]
		if len(chunk) < ws.AudioFrameSize {
			padded := make([]byte, ws.AudioFrameSize)
			copy(padded, chunk)
			chunk = padded
		}
		sendAudioFrame(t, conn, seq, chunk)
	}
}

// runLiveVoiceCommand wires the PRODUCTION voice stack (real mimo client,
// real transcriber/chatter adapters, hybrid classifier WITH LLM) and runs
// one voice command whose speech the real TTS synthesizes. Returns the
// voice_result frame.
func runLiveVoiceCommand(t *testing.T, spokenText string) voiceResultResponse {
	t.Helper()
	apiKey := liveAPIKey(t)

	client, err := mimo.NewClient(mimo.ClientConfig{
		APIKey:  apiKey,
		BaseURL: infra.DefaultConfig().Voice.BaseURL,
		Timeout: 45 * time.Second,
	}, infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("mimo client: %v", err)
	}
	defer client.Close()

	// The exact composition main.go performs. RequestTimeout is raised far
	// above the production 15s default: mimoChatter disables thinking (issue
	// #45), so a chat reply takes ~1s, but the TTS→ASR legs of this pipeline
	// still dominate, and this test must not mistake a cancelled call for a
	// classification result (the 2s in testVoiceConfig exists for the fast
	// offline tests, not for live AI).
	cfg := testVoiceConfig(0)
	cfg.RequestTimeout = 45 * time.Second
	vs := newVoiceService(
		mimoTranscriber(client),
		voice.NewHybridClassifier(voice.NewLLMClassifier(mimoChatter(client, infra.DefaultConfig().Voice.ChatModel))),
		&recordingPusher{},
		infra.NewNoOpLogger(),
		cfg,
	)

	conn, teardown := dialVoiceTestServer(t, ttsHandlers(nil, nil, vs, nil, infra.NewNoOpLogger()))
	defer teardown()

	pcm := synthesizeCommandPCM(t, client, spokenText)
	streamPCMChunks(t, conn, pcm, 1)
	sendControlJSON(t, conn, `{"type":"voice_command","params":{}}`)

	header, payload := readFrameWithTimeout(t, conn, 60*time.Second)
	if header.MsgType != ws.MsgTypeControl {
		t.Fatalf("reply msg type = %s, want control", header.MsgType)
	}
	var result voiceResultResponse
	if err := json.Unmarshal(payload, &result); err != nil {
		t.Fatalf("voice_result decode: %v", err)
	}
	return result
}

// TestVoiceCommand_LiveRulePath drives real TTS("暂停一下") through the real
// pipeline and expects the rule fast path to recognize it as pause. The
// longer phrasing matters: a bare two-syllable "暂停" round-trips through
// TTS→ASR unstably (observed transcribed as "正题" once) — more syllables
// give the recognizer context, matching how children actually speak.
func TestVoiceCommand_LiveRulePath(t *testing.T) {
	result := runLiveVoiceCommand(t, "暂停一下")
	t.Logf("live rule path: transcript=%q action=%q source=%q", result.Transcript, result.Action, result.Source)
	if result.Action != string(voice.ActionPause) {
		t.Errorf("live action = %q (transcript %q, source %q), want pause",
			result.Action, result.Transcript, result.Source)
	}
	if result.Source != "rule" {
		t.Errorf("live source = %q, want rule (keyword fast path)", result.Source)
	}
	if result.Transcript == "" {
		t.Error("live transcript empty — real ASR returned nothing")
	}
}

// TestVoiceCommand_LiveLLMPath drives a varied natural expression the
// keyword tables do not cover; only the real LLM fallback can classify it.
func TestVoiceCommand_LiveLLMPath(t *testing.T) {
	result := runLiveVoiceCommand(t, "老师我觉得这个有点难")
	t.Logf("live LLM path: transcript=%q action=%q source=%q", result.Transcript, result.Action, result.Source)
	if result.Source != "llm" {
		t.Fatalf("live source = %q, want llm (expression must miss the keyword tables; got transcript %q)",
			result.Source, result.Transcript)
	}
	if result.Action != string(voice.ActionCannotWrite) {
		t.Errorf("live action = %q, want cannot_write (LLM understood the complaint)", result.Action)
	}
}
