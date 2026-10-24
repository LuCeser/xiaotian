package rt

import (
	"testing"
	"time"
)

func TestAudioObservationDistinguishesDoubledStream(t *testing.T) {
	for _, rate := range []int{1, 2} {
		var o audioObservation
		start := time.Unix(1, 0)
		o.add(start, 640, false)
		for i := 1; i <= 250; i++ {
			fields := o.add(start.Add(time.Duration(i)*20*time.Millisecond), 640*rate, i <= 100)
			if i < 250 {
				if fields != nil {
					t.Fatal("logged before the five-second window")
				}
				continue
			}
			entry := captureEntry{fields: fields}
			if got := fieldValue(t, entry, "pcm_ms"); got != 5000*rate {
				t.Fatalf("PCM duration = %v, rate %d", got, rate)
			}
			if got := fieldValue(t, entry, "gated_pcm_ms"); got != 2000*rate {
				t.Fatalf("gated duration = %v", got)
			}
		}
		if o.bytes != 0 || o.gatedBytes != 0 {
			t.Fatal("window counters not reset")
		}
	}
}

func TestVADObservationDistinguishesSilenceFromHardCut(t *testing.T) {
	cases := []struct {
		name   string
		pcm    []byte
		reason string
		quiet  int
	}{
		{"normal", append(vadSine(5000, 300), vadSilence(720)...), "silence", 720},
		{"continuous_noise", vadSine(5000, 30000), "max_duration", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := NewVAD(DefaultVADConfig())
			var closed bool
			var report vadObservation
			for offset := 0; offset < len(tc.pcm); offset += vadTestFrameBytes {
				_, closed, report = v.feedObserved(tc.pcm[offset : offset+vadTestFrameBytes])
				if closed {
					break
				}
			}
			if !closed || report.Reason != tc.reason || report.MaxQuietMs != tc.quiet {
				t.Fatalf("unexpected report: closed=%v %+v", closed, report)
			}
			if report.DurationMs != len(tc.pcm)*1000/(2*requiredSampleRate) {
				t.Fatalf("wrong duration: %+v", report)
			}
			if v.observation != (vadObservation{}) {
				t.Fatal("closed segment leaked statistics")
			}
			v.feedObserved(vadSine(5000, 300))
			v.Reset()
			if v.observation != (vadObservation{}) {
				t.Fatal("Reset leaked statistics")
			}
		})
	}
}
