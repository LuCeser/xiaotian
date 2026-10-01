// vad.go — energy VAD segmenter (13-04 Task 1).
//
// RED-phase compile stub: the types and signatures the behavior tests in
// vad_test.go compile against. Feed never closes a segment and FrameRMS
// returns 0, so every behavior test fails on its assertion (never on a
// compile or discovery error). The GREEN phase replaces the bodies.
package rt

// VADConfig tunes the energy segmenter.
type VADConfig struct {
	StartRMS     float64
	StopRMS      float64
	FrameMs      int
	StartFrames  int
	HangoverMs   int
	PreRollMs    int
	MaxSegmentMs int
}

// DefaultVADConfig returns the initial R0 tuning. These are starting
// defaults: if real-device false triggers run high, retune or upgrade to
// rnnoise-go (13-RESEARCH Alternatives).
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

// VAD is the stateful segmenter. Not yet implemented (RED).
type VAD struct {
	cfg VADConfig
}

// NewVAD builds a VAD.
func NewVAD(cfg VADConfig) *VAD {
	return &VAD{cfg: cfg}
}

// Feed is not yet implemented (RED stub).
func (v *VAD) Feed(pcm []byte) (segment []byte, closed bool) {
	_ = pcm
	return nil, false
}

// Reset is not yet implemented (RED stub).
func (v *VAD) Reset() {}

// FrameRMS is not yet implemented (RED stub).
func FrameRMS(frame []byte) float64 {
	_ = frame
	return 0
}
