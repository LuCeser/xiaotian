package voice

import (
	"sort"
	"sync"
	"time"
)

// Assembler reassembles a voice-command recording from streamed 640-byte PCM
// chunks. The ws Router dequeues frames of one connection with up to
// WorkerConcurrency workers (no per-connection ordering guarantee), so chunks
// are keyed by their frame Sequence and concatenated in sequence order at
// assembly time — duplicate sequences are absorbed by the map, and reordering
// is invisible to the caller.
type Assembler struct {
	mu       sync.Mutex
	chunks   map[uint32][]byte
	bytes    int
	maxBytes int
}

// NewAssembler builds an assembler capped at maxBytes of PCM (the 1C1G
// memory guard; a voice command is a short utterance, ~2-5s).
func NewAssembler(maxBytes int) *Assembler {
	return &Assembler{
		chunks:   make(map[uint32][]byte),
		maxBytes: maxBytes,
	}
}

// Add stores one chunk. It returns ErrAudioOverflow (wrapped by the caller's
// decision to drop or reject) when the cap would be exceeded — the recording
// is then considered spoiled and should be Reset. Duplicate sequences are
// ignored (counted once).
func (a *Assembler) Add(seq uint32, chunk []byte) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, dup := a.chunks[seq]; dup {
		return nil
	}
	if a.bytes+len(chunk) > a.maxBytes {
		return ErrAudioOverflow
	}
	// Copy: the router reuses/transfers the payload slice ownership after the
	// handler returns, so we cannot alias it.
	c := make([]byte, len(chunk))
	copy(c, chunk)
	a.chunks[seq] = c
	a.bytes += len(c)
	return nil
}

// Len returns the total PCM bytes accumulated so far.
func (a *Assembler) Len() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.bytes
}

// Complete reports whether exactly expected bytes have accumulated.
func (a *Assembler) Complete(expected int) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.bytes == expected
}

// Assemble concatenates the chunks in sequence order. Returns nil when
// nothing was accumulated.
func (a *Assembler) Assemble() []byte {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.chunks) == 0 {
		return nil
	}
	seqs := make([]int, 0, len(a.chunks))
	for seq := range a.chunks {
		seqs = append(seqs, int(seq))
	}
	sort.Ints(seqs)
	out := make([]byte, 0, a.bytes)
	for _, seq := range seqs {
		out = append(out, a.chunks[uint32(seq)]...)
	}
	return out
}

// Reset drops all accumulated chunks (called after a command is consumed or
// a recording is spoiled).
func (a *Assembler) Reset() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.chunks = make(map[uint32][]byte)
	a.bytes = 0
}

// bufferEntry pairs an Assembler with its last-write stamp for idle expiry.
type bufferEntry struct {
	asm       *Assembler
	lastWrite time.Time
}

// Buffers is the connection-keyed registry of per-connection Assemblers.
// Entries are created lazily on the first audio frame and expired by an
// opportunistic janitor: every Add attempts a sweep, but sweep() rate-limits
// itself to one pass per idleTTL (a map walk over ≤ hubLimit entries —
// negligible against the 50 frames/s recording cadence). This bounds memory
// on the 1C1G VPS even when connections disconnect mid-recording without
// sending the voice_command trigger (there is no router-level disconnect
// hook): an abandoned entry and its capped PCM payload live at most one
// idleTTL past its last audio frame.
type Buffers struct {
	mu        sync.Mutex
	entries   map[string]*bufferEntry
	maxBytes  int
	idleTTL   time.Duration
	lastSweep time.Time
	now       func() time.Time
}

// NewBuffers builds the registry.
func NewBuffers(maxBytes int, idleTTL time.Duration) *Buffers {
	return &Buffers{
		entries:  make(map[string]*bufferEntry),
		maxBytes: maxBytes,
		idleTTL:  idleTTL,
		now:      time.Now,
	}
}

// Add routes a chunk into the connection's assembler, creating it lazily.
// Returns the assembler-level error (ErrAudioOverflow) so the handler can
// reject the recording.
func (b *Buffers) Add(connID string, seq uint32, chunk []byte) error {
	b.mu.Lock()
	entry, ok := b.entries[connID]
	if !ok {
		entry = &bufferEntry{asm: NewAssembler(b.maxBytes)}
		b.entries[connID] = entry
	}
	entry.lastWrite = b.now()
	b.mu.Unlock()

	b.sweep()
	return entry.asm.Add(seq, chunk)
}

// Get returns the connection's assembler (nil when no audio arrived yet).
func (b *Buffers) Get(connID string) *Assembler {
	b.mu.Lock()
	defer b.mu.Unlock()
	if entry, ok := b.entries[connID]; ok {
		return entry.asm
	}
	return nil
}

// Drop removes the connection's entry entirely (called after a command is
// fully consumed).
func (b *Buffers) Drop(connID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.entries, connID)
}

// sweep drops idle entries. Rate-limited to one pass per idleTTL so the hot
// Add path (50 frames/s during recording) never turns into a per-frame map
// walk. Uses the b.now seam (not time.Since) so injected-clock tests observe
// the same cadence as production.
func (b *Buffers) sweep() {
	b.mu.Lock()
	if b.now().Sub(b.lastSweep) < b.idleTTL {
		b.mu.Unlock()
		return
	}
	b.lastSweep = b.now()
	cutoff := b.now().Add(-b.idleTTL)
	for connID, entry := range b.entries {
		if entry.lastWrite.Before(cutoff) {
			delete(b.entries, connID)
		}
	}
	b.mu.Unlock()
}
