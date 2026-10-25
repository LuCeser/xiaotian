package rt

import (
	"context"
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
)

// Temporary diagnostics for the 2026-10-18 real-device regression. Only
// counters and elapsed times are logged; no waveform, transcript or device ID.
type audioObservation struct {
	start      time.Time
	bytes      int
	gatedBytes int
	chunks     int
}

// add is called with TurnManager.mu held. Received audio duration / elapsed
// time distinguishes a doubled PCM stream from normal-rate noisy input.
func (o *audioObservation) add(now time.Time, size int, gated bool) []infra.Field {
	if o.start.IsZero() {
		o.start = now
		return nil // the first chunk precedes the measurement interval
	}
	o.bytes += size
	o.chunks++
	if gated {
		o.gatedBytes += size
	}
	elapsed := now.Sub(o.start)
	if elapsed < 5*time.Second {
		return nil
	}
	fields := []infra.Field{
		infra.F("elapsed_ms", elapsed.Milliseconds()),
		infra.F("pcm_ms", o.bytes*1000/(2*requiredSampleRate)),
		infra.F("gated_pcm_ms", o.gatedBytes*1000/(2*requiredSampleRate)),
		infra.F("chunks", o.chunks),
	}
	*o = audioObservation{start: now}
	return fields
}

func (tm *TurnManager) transcribeObserved(ctx context.Context, segment []byte, turnID uint64, attempt int) (string, error) {
	started := time.Now()
	text, err := tm.asr.Transcribe(ctx, segment)
	tm.recorder.Emit("[DEBUG-asr-recurrence]asr",
		infra.F("turn_id", turnID), infra.F("attempt", attempt),
		infra.F("duration_ms", time.Since(started).Milliseconds()),
		infra.F("pcm_ms", len(segment)*1000/(2*requiredSampleRate)),
		infra.F("failed", err != nil), infra.F("empty", text == ""))
	return text, err
}
