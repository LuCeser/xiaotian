package voice

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"
)

// TestAssembler_OutOfOrderChunks verifies the core reassembly contract:
// chunks added out of sequence order (router workers race) come back
// concatenated in sequence order.
func TestAssembler_OutOfOrderChunks(t *testing.T) {
	a := NewAssembler(1024)

	chunk := func(seq byte, fill byte) []byte { return bytes.Repeat([]byte{fill}, 4) }

	// Add out of order: seq 2, 0, 1.
	if err := a.Add(2, chunk(0, 0x03)); err != nil {
		t.Fatalf("Add(2): %v", err)
	}
	if err := a.Add(0, chunk(0, 0x01)); err != nil {
		t.Fatalf("Add(0): %v", err)
	}
	if err := a.Add(1, chunk(0, 0x02)); err != nil {
		t.Fatalf("Add(1): %v", err)
	}

	got := a.Assemble()
	want := append(bytes.Repeat([]byte{0x01}, 4), append(bytes.Repeat([]byte{0x02}, 4), bytes.Repeat([]byte{0x03}, 4)...)...)
	if !bytes.Equal(got, want) {
		t.Errorf("Assemble() = %x, want %x", got, want)
	}
	if a.Len() != 12 || !a.Complete(12) {
		t.Errorf("Len/Complete = %d/%v, want 12/true", a.Len(), a.Complete(12))
	}
}

// TestAssembler_DuplicateSequenceIgnored guards the bytes accounting against
// double-counting a re-delivered frame.
func TestAssembler_DuplicateSequenceIgnored(t *testing.T) {
	a := NewAssembler(1024)
	_ = a.Add(1, []byte{0xAA, 0xBB})
	_ = a.Add(1, []byte{0xAA, 0xBB})
	if a.Len() != 2 {
		t.Errorf("Len() = %d, want 2", a.Len())
	}
	got := a.Assemble()
	if !bytes.Equal(got, []byte{0xAA, 0xBB}) {
		t.Errorf("Assemble() = %x, want aabb once", got)
	}
}

// TestAssembler_Overflow verifies the memory cap.
func TestAssembler_Overflow(t *testing.T) {
	a := NewAssembler(640)
	if err := a.Add(0, bytes.Repeat([]byte{0x00}, 640)); err != nil {
		t.Fatalf("first chunk: %v", err)
	}
	err := a.Add(1, bytes.Repeat([]byte{0x00}, 1))
	if !errors.Is(err, ErrAudioOverflow) {
		t.Fatalf("expected ErrAudioOverflow, got %v", err)
	}
}

// TestAssembler_Reset verifies post-consumption cleanup.
func TestAssembler_Reset(t *testing.T) {
	a := NewAssembler(1024)
	_ = a.Add(0, []byte{1, 2, 3})
	a.Reset()
	if a.Len() != 0 || a.Assemble() != nil {
		t.Error("Reset() did not empty the assembler")
	}
}

// TestAssembler_PayloadCopied guards against aliasing the router's payload
// buffer: mutating the input after Add must not change the assembled bytes.
func TestAssembler_PayloadCopied(t *testing.T) {
	a := NewAssembler(1024)
	chunk := []byte{1, 2, 3}
	_ = a.Add(0, chunk)
	chunk[0] = 0xFF
	got := a.Assemble()
	if got[0] != 1 {
		t.Errorf("Assemble() aliased the caller's slice: %x", got)
	}
}

// TestBuffers_LifecycleAndSweep verifies lazy creation, Drop, and the idle
// janitor with an injected clock.
func TestBuffers_LifecycleAndSweep(t *testing.T) {
	now := time.Now()
	b := NewBuffers(1024, 2*time.Minute)
	b.now = func() time.Time { return now }

	if err := b.Add("conn-a", 0, []byte{1}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if b.Get("conn-a") == nil {
		t.Fatal("expected lazy entry for conn-a")
	}
	if b.Get("conn-b") != nil {
		t.Fatal("expected nil for unknown conn-b")
	}

	// Advance past the idle TTL; the next Add's opportunistic sweep must drop
	// idle conn-a while keeping the freshly-written conn-z.
	now = now.Add(3 * time.Minute)
	if err := b.Add("conn-z", 0, []byte{1}); err != nil && !errors.Is(err, ErrAudioOverflow) {
		t.Fatalf("Add: %v", err)
	}
	if b.Get("conn-a") != nil {
		t.Error("idle conn-a survived the janitor sweep")
	}
	if b.Get("conn-z") == nil {
		t.Error("active conn-z was swept")
	}

	b.Drop("conn-z")
	if b.Get("conn-z") != nil {
		t.Error("Drop() did not remove conn-z")
	}
}

// TestWrapWAV verifies the 44-byte header fields against the RIFF spec and
// that the payload is appended unchanged.
func TestWrapWAV(t *testing.T) {
	pcm := bytes.Repeat([]byte{0xAB}, 100)
	wav := WrapWAV(pcm, 16000, 1)

	if len(wav) != 44+100 {
		t.Fatalf("len = %d, want 144", len(wav))
	}
	if string(wav[0:4]) != "RIFF" || string(wav[8:12]) != "WAVE" {
		t.Error("RIFF/WAVE magic missing")
	}
	if got := le32(wav, 4); got != 36+100 {
		t.Errorf("riff size = %d, want %d", got, 36+100)
	}
	if string(wav[12:16]) != "fmt " {
		t.Error("fmt chunk missing")
	}
	if got := le32(wav, 16); got != 16 {
		t.Errorf("fmt chunk size = %d, want 16", got)
	}
	if got := le16(wav, 20); got != 1 {
		t.Errorf("audio format = %d, want 1 (PCM)", got)
	}
	if got := le16(wav, 22); got != 1 {
		t.Errorf("channels = %d, want 1", got)
	}
	if got := le32(wav, 24); got != 16000 {
		t.Errorf("sample rate = %d, want 16000", got)
	}
	if got := le32(wav, 28); got != 32000 {
		t.Errorf("byte rate = %d, want 32000", got)
	}
	if got := le16(wav, 32); got != 2 {
		t.Errorf("block align = %d, want 2", got)
	}
	if got := le16(wav, 34); got != 16 {
		t.Errorf("bits per sample = %d, want 16", got)
	}
	if string(wav[36:40]) != "data" {
		t.Error("data chunk missing")
	}
	if got := le32(wav, 40); got != 100 {
		t.Errorf("data size = %d, want 100", got)
	}
	if !bytes.Equal(wav[44:], pcm) {
		t.Error("payload not appended unchanged")
	}
}

func le16(b []byte, off int) uint16 {
	return uint16(b[off]) | uint16(b[off+1])<<8
}

func le32(b []byte, off int) uint32 {
	return uint32(b[off]) | uint32(b[off+1])<<8 | uint32(b[off+2])<<16 | uint32(b[off+3])<<24
}

// TestClassifierInterfaceConformance pins the constructors to the Classifier
// interface at compile time.
func TestClassifierInterfaceConformance(t *testing.T) {
	var _ Classifier = NewRuleClassifier()
	var _ Classifier = NewLLMClassifier(nil)
	var _ Classifier = NewHybridClassifier(nil)
	_ = context.Background()
}
