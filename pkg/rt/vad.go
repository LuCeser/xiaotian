// vad.go — energy VAD segmenter for the realtime tutor loop (13-04 Task 1).
//
// The segmenter turns the continuous 16 kHz s16le mono uplink (the only
// format ValidateAudioInit accepts) into complete utterances for the ASR
// provider's whole-utterance API. It is deliberately an energy method
// (~100 lines, zero dependencies): R0 speech is a child speaking close to
// the mic in a quiet room, where hysteresis + hangover + pre-roll is enough
// (13-RESEARCH Alternatives). If real-device false triggers run high,
// retune VADConfig or upgrade to rnnoise-go — the config surface is the seam.
package rt

import (
	"encoding/binary"
	"math"
	"sync"
)

// VADConfig tunes the energy segmenter. All thresholds are normalized RMS
// values in [0,1) (int16 samples scaled by 1/32768).
type VADConfig struct {
	// StartRMS is the frame energy at or above which a frame counts as
	// speech while hunting for an onset.
	StartRMS float64
	// StopRMS is the frame energy below which a frame counts as silence
	// while a segment is open (counts down the hangover).
	StopRMS float64
	// FrameMs is the analysis frame length in milliseconds (16 kHz).
	FrameMs int
	// StartFrames is the count of consecutive speech frames that opens a
	// segment (hysteresis: brief clicks never trigger a turn).
	StartFrames int
	// HangoverMs is the silence that must elapse inside an open segment
	// before it closes (keeps short pauses inside one utterance).
	HangoverMs int
	// PreRollMs is the audio buffered before the onset and prepended to the
	// segment so the first syllable is never clipped.
	PreRollMs int
	// MaxSegmentMs hard-cuts an open segment no matter what. 30 s at
	// 16 kHz s16le is ~960 KB of PCM, far below the ASR DataURL 10 MB limit
	// (Pitfall 6: a chatty child must not blow the upload cap).
	MaxSegmentMs int
}

// DefaultVADConfig returns the initial R0 tuning. These are starting
// defaults, not tuned constants: if real-device false triggers run high,
// adjust here or upgrade to rnnoise-go (13-RESEARCH Alternatives; the
// package was verified to exist on the Go proxy and is cgo-free).
func DefaultVADConfig() VADConfig {
	return VADConfig{
		StartRMS:     0.02,
		StopRMS:      0.012,
		FrameMs:      30,
		StartFrames:  3,
		HangoverMs:   700,
		PreRollMs:    300,
		MaxSegmentMs: 30000,
	}
}

// VAD is the stateful utterance segmenter. Feed it uplink PCM as it arrives
// (any chunk sizes); it returns complete utterances. Feed and Reset are safe
// to call from different goroutines: the turn manager feeds from the
// transport read loop while close/interrupt paths Reset from announcement
// goroutines, so the segmenter serializes itself with its own leaf mutex
// (held only across pure in-memory state work — no calls out, no deadlock).
type VAD struct {
	// pending holds bytes not yet consumed to whole frames: a partial tail
	// of a Feed call, plus whole frames that arrived after a segment closed
	// mid-call (the single-segment return contract — see Feed).
	pending []byte

	// ring holds the most recent preRollFrames frames of audio as copies.
	ring [][]byte
	// segment accumulates the open segment's bytes; segmentMs its length.
	segment     []byte
	observation vadObservation
	cfg         VADConfig

	// frameBytes is one analysis frame in bytes; frameMs the cfg value
	// normalized to >= 1.
	frameBytes int
	frameMs    int
	// preRollFrames is the ring capacity in frames (>= 0).
	preRollFrames int
	consec        int // consecutive StartRMS frames while hunting
	hangoverMs    int // silence budget left inside an open segment
	segmentMs     int

	inSegment bool

	// mu serializes every field above across Feed/Reset callers.
	mu sync.Mutex
}

// NewVAD builds a segmenter. Non-positive tuning fields fall back to the
// DefaultVADConfig value for that field so a zero-valued field can never
// produce a degenerate frame size or an unbounded segment.
func NewVAD(cfg VADConfig) *VAD {
	def := DefaultVADConfig()
	if cfg.FrameMs <= 0 {
		cfg.FrameMs = def.FrameMs
	}
	if cfg.StartFrames <= 0 {
		cfg.StartFrames = def.StartFrames
	}
	if cfg.HangoverMs <= 0 {
		cfg.HangoverMs = def.HangoverMs
	}
	if cfg.PreRollMs < 0 {
		cfg.PreRollMs = def.PreRollMs
	}
	if cfg.MaxSegmentMs <= 0 {
		cfg.MaxSegmentMs = def.MaxSegmentMs
	}
	if cfg.StartRMS <= 0 {
		cfg.StartRMS = def.StartRMS
	}
	if cfg.StopRMS <= 0 {
		cfg.StopRMS = def.StopRMS
	}
	// requiredSampleRate is the only negotiated uplink rate (protocol.go):
	// 30 ms at 16 kHz = 480 samples = 960 bytes.
	frameBytes := 2 * requiredSampleRate * cfg.FrameMs / 1000
	return &VAD{
		cfg:           cfg,
		frameBytes:    frameBytes,
		frameMs:       cfg.FrameMs,
		preRollFrames: cfg.PreRollMs / cfg.FrameMs,
		hangoverMs:    cfg.HangoverMs,
	}
}

// Feed consumes one chunk of uplink PCM and returns the first utterance that
// closed during this call, if any.
//
// Contract:
//   - Input need not be frame-aligned: a partial frame's bytes are buffered
//     and combined with the next call. No sample is ever dropped or doubled
//     (the no-sample-loss invariant is test-anchored against odd chunk
//     sizes).
//   - At most ONE closed segment is returned per call. Audio that follows
//     the closing frame within the same call stays buffered and is processed
//     by the next Feed — callers that keep streaming (the read loop does)
//     see it then.
func (v *VAD) Feed(pcm []byte) ([]byte, bool) {
	segment, closed, _ := v.feedObserved(pcm)
	return segment, closed
}

// vadObservation contains only aggregate levels and durations, never audio.
// Temporary investigation of device-dependent utterance closure delays.
type vadObservation struct {
	Reason     string
	DurationMs int
	MaxQuietMs int
	QuietMs    int
	Frames     int
	RMSSum     float64
	RMSMax     float64
}

// feedObserved returns metrics with the corresponding segment under the same
// lock; Reset cannot detach a report from the audio it describes.
func (v *VAD) feedObserved(pcm []byte) ([]byte, bool, vadObservation) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.pending = append(v.pending, pcm...)
	for len(v.pending) >= v.frameBytes {
		frame := v.pending[:v.frameBytes]
		if v.processFrame(frame) {
			// Compact the unconsumed remainder out of the consumed region
			// (re-slicing alone would pin the whole array).
			rest := append([]byte(nil), v.pending[v.frameBytes:]...)
			v.pending = rest

			seg := v.segment
			report := v.observation
			report.DurationMs = v.segmentMs
			v.closeSegmentState()
			return seg, true, report
		}
		v.pending = v.pending[v.frameBytes:]
	}
	if len(v.pending) == 0 {
		v.pending = nil
	}
	return nil, false, vadObservation{}
}

// Reset clears all state: partial frames, ring, open segment, counters.
// Called on interrupt (the in-flight utterance is abandoned) and on session
// teardown so a reused VAD starts clean. Safe to call concurrently with an
// in-flight Feed — whichever wins the mutex decides the serialization, and
// either order leaves the state machine consistent.
func (v *VAD) Reset() {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.pending = nil
	v.inSegment = false
	v.consec = 0
	v.hangoverMs = v.cfg.HangoverMs
	v.ring = nil
	v.segment = nil
	v.segmentMs = 0
	v.observation = vadObservation{}
}

// closeSegmentState resets the per-segment bookkeeping after a close. The
// hunt state (consec) also restarts — post-close audio must re-trigger the
// onset detector before a new segment opens.
func (v *VAD) closeSegmentState() {
	v.observation = vadObservation{}
	v.inSegment = false
	v.consec = 0
	v.hangoverMs = v.cfg.HangoverMs
	v.segment = nil
	v.segmentMs = 0
}

// processFrame applies one complete frame to the state machine and reports
// whether the open segment just closed. frame must be exactly frameBytes
// long; its bytes are copied wherever retained.
func (v *VAD) processFrame(frame []byte) bool {
	rms := FrameRMS(frame)

	if !v.inSegment {
		// Hunt: every frame rides the pre-roll ring (silence included), and
		// hysteresis counts consecutive speech frames.
		v.pushRing(frame)
		if rms >= v.cfg.StartRMS {
			v.consec++
		} else {
			v.consec = 0
		}
		if v.consec >= v.cfg.StartFrames {
			v.observation = vadObservation{}
			// Onset: the ring's contents (recent silence + the trigger
			// frames) become the segment head — nothing before speech is
			// clipped. The ring restarts for the next hunt.
			for _, f := range v.ring {
				v.segment = append(v.segment, f...)
				v.segmentMs += v.frameMs
			}
			v.ring = nil
			v.inSegment = true
			v.consec = 0
			v.hangoverMs = v.cfg.HangoverMs
		}
		return false
	}

	// Open segment: append first so the hangover tail is INSIDE the segment
	// (the ASR gets the pause that ended the utterance).
	v.segment = append(v.segment, frame...)
	v.segmentMs += v.frameMs
	v.observation.Frames++
	v.observation.RMSSum += rms
	if rms > v.observation.RMSMax {
		v.observation.RMSMax = rms
	}

	if rms < v.cfg.StopRMS {
		v.observation.QuietMs += v.frameMs
		if v.observation.QuietMs > v.observation.MaxQuietMs {
			v.observation.MaxQuietMs = v.observation.QuietMs
		}
		v.hangoverMs -= v.frameMs
		if v.hangoverMs <= 0 {
			v.observation.Reason = "silence"
			return true // hangover expired: utterance complete
		}
	} else {
		v.observation.QuietMs = 0
		v.hangoverMs = v.cfg.HangoverMs // speech continues the segment
	}

	if v.segmentMs >= v.cfg.MaxSegmentMs {
		v.observation.Reason = "max_duration"
		return true // hard cut (Pitfall 6)
	}
	return false
}

// pushRing adds one frame copy to the pre-roll ring, dropping the oldest
// frame beyond capacity.
func (v *VAD) pushRing(frame []byte) {
	if v.preRollFrames == 0 {
		return
	}
	cp := append([]byte(nil), frame...)
	v.ring = append(v.ring, cp)
	if len(v.ring) > v.preRollFrames {
		// Copy-down instead of repeated re-slice so the dropped head's array
		// is not pinned by the surviving tail.
		v.ring = append(v.ring[:0], v.ring[1:]...)
	}
}

// FrameRMS returns the normalized RMS of one frame of s16le mono PCM in
// [0,1) — sqrt(mean(sample^2)) scaled by 1/32768. A full frame of a sine
// with int16 amplitude A yields A/(sqrt(2)*32768). Empty input returns 0; a
// trailing odd byte is ignored. Pure function.
func FrameRMS(frame []byte) float64 {
	n := len(frame) / 2
	if n == 0 {
		return 0
	}
	var sum float64
	for i := 0; i+1 < len(frame); i += 2 {
		s := float64(int16(binary.LittleEndian.Uint16(frame[i:]))) //nolint:gosec // G115: s16le PCM sample; the high bit is the sign
		sum += s * s
	}
	return math.Sqrt(sum/float64(n)) / 32768
}
