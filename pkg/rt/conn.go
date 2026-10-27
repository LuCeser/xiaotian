package rt

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pkg/errors"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
)

// Transport defaults, aligned with the pkg/ws constants this endpoint's
// governance parameters are copied from (the legacy frame protocol itself is
// NOT reused — ADR-0002 froze pkg/ws as the degraded fallback).
const (
	// DefaultMaxMessageBytes caps one inbound WebSocket message (read
	// limit). 65551 keeps parity with pkg/ws's largest expressible frame;
	// it is generous for both JSON control messages and PCM chunks
	// (20ms@16kHz mono = 640 bytes).
	DefaultMaxMessageBytes int64 = 65551

	defaultReadBufferSize  = 4096
	defaultWriteBufferSize = 4096

	defaultHandshakeTimeout = 10 * time.Second

	// DefaultHeartbeatInterval matches pkg/ws (30s, D-14): frequent enough
	// to roll read deadlines through NAT/idle timeouts, rare enough for a
	// 1C1G VPS.
	DefaultHeartbeatInterval = 30 * time.Second

	// defaultSendQueue is the per-connection outbound frame queue capacity
	// (control and PCM share ONE ordered queue). A bounded queue keeps
	// per-connection memory inside the 150MB budget (T-13-02): audio
	// overflows are dropped (real-time streams tolerate loss), control
	// overflows error (control must not silently vanish).
	defaultSendQueue = 256

	// perMessageWriteTimeout bounds one outbound write; a stalled client
	// cannot pin a writer goroutine forever.
	perMessageWriteTimeout = 10 * time.Second

	// drainGrace bounds the teardown flush after the read loop exits: the
	// session's final control messages (e.g. session_ended) get a short
	// window to reach the wire before the close frame.
	drainGrace = 500 * time.Millisecond
)

// Sentinel errors for the /rt transport.
var (
	// ErrInvalidServerConfig wraps every ServerConfig validation failure.
	ErrInvalidServerConfig = errors.New("rt: invalid server config")

	// ErrNilManagerFactory is returned by NewHandler for a nil factory —
	// every connection needs a session manager; there is no silent default.
	ErrNilManagerFactory = errors.New("rt: nil manager factory")

	// ErrControlQueueFull is returned by Sender.SendControl when the
	// connection's control queue is saturated (the caller decides how to
	// surface the failure — control messages are never dropped silently).
	ErrControlQueueFull = errors.New("rt: control send queue full")

	// ErrWriterTerminated rejects every enqueue after transport termination.
	ErrWriterTerminated = errors.New("rt: writer terminated")
)

// ServerConfig configures the /rt WebSocket endpoint. Zero values mean "use
// the default" for every field except AllowedOrigins, which has no default
// and must be a non-empty, wildcard-free whitelist.
type ServerConfig struct {
	// AllowedOrigins is the Origin whitelist (D-24 semantics copied from
	// pkg/ws): entries are matched first exactly (scheme+host+port), then
	// port-stripped. "*" is forbidden; an absent Origin header is always
	// rejected (CR-05: non-browser clients get no path in).
	AllowedOrigins []string
	// MaxMessageBytes caps one inbound message (SetReadLimit). 0 →
	// DefaultMaxMessageBytes.
	MaxMessageBytes int64
	// ReadBufferSize is the WebSocket read buffer size. 0 → 4096.
	ReadBufferSize int
	// WriteBufferSize is the WebSocket write buffer size. 0 → 4096.
	WriteBufferSize int
	// HandshakeTimeout bounds the upgrade handshake. 0 → 10s.
	HandshakeTimeout time.Duration
	// HeartbeatInterval is the server ping interval; the read deadline is
	// its double and rolls forward on every pong. 0 → 30s.
	HeartbeatInterval time.Duration
}

// Validate rejects configurations NewHandler cannot safely run with:
// missing or wildcard origins, and negative values that would silently
// disable a protection (WR-07 lesson: treat only 0 as "default", reject
// negatives explicitly).
func (c ServerConfig) Validate() error {
	if len(c.AllowedOrigins) == 0 {
		return errors.Wrap(ErrInvalidServerConfig, "allowed origins required")
	}
	for _, origin := range c.AllowedOrigins {
		if origin == "*" {
			return errors.Wrap(ErrInvalidServerConfig, "wildcard origin forbidden")
		}
	}
	if c.MaxMessageBytes < 0 {
		return errors.Wrap(ErrInvalidServerConfig, "max message bytes must be >= 0")
	}
	if c.ReadBufferSize < 0 || c.WriteBufferSize < 0 {
		return errors.Wrap(ErrInvalidServerConfig, "buffer sizes must be >= 0")
	}
	if c.HandshakeTimeout < 0 {
		return errors.Wrap(ErrInvalidServerConfig, "handshake timeout must be >= 0")
	}
	if c.HeartbeatInterval < 0 {
		return errors.Wrap(ErrInvalidServerConfig, "heartbeat interval must be >= 0")
	}
	return nil
}

// Sender is the write half a session manager sees: control messages (JSON,
// TextMessage) and teacher audio (bare PCM, BinaryMessage). The transport
// owns serialization and the single-writer pump; the manager never touches
// the websocket.Conn.
type Sender interface {
	// SendControl queues one control message for delivery. It returns
	// ErrControlQueueFull when the control queue is saturated — control is
	// never dropped silently.
	SendControl(v any) error
	// SendAudio queues one PCM chunk for delivery. When the audio queue is
	// saturated the chunk is DROPPED and counted (real-time audio tolerates
	// loss; blocking or buffering unboundedly does not) and nil is
	// returned. The buffer is copied; callers may reuse their slice.
	SendAudio(b []byte) error
	// Terminate stops accepting frames and asks the send loop to exit. The
	// first call owns the reason; later calls are no-ops.
	Terminate(reason error)
	// Done closes once after the send loop has exited.
	Done() <-chan struct{}
	// TerminationReason reports the first termination reason. Nil means the
	// transport was ended normally.
	TerminationReason() error
}

// SessionManager owns one connection's session state (implemented by the
// turn manager, 13-04). The transport only moves bytes: it hands control
// payloads and audio up as they arrive and calls Close exactly once when the
// connection ends — the session-release half of RT-10/SC7.
type SessionManager interface {
	// HandleControl consumes one JSON control payload (TextMessage body).
	HandleControl(s Sender, data []byte)
	// IngestAudio consumes one PCM chunk (BinaryMessage body).
	IngestAudio(s Sender, data []byte)
	// Close releases the session. Called when the read loop exits for any
	// reason (client disconnect, end_session-driven close, read error,
	// deadline expiry).
	Close(s Sender)
}

// ManagerFactory builds one SessionManager per connection (R0: single
// student, one manager per /rt connection — the simplest correct model).
type ManagerFactory func() SessionManager

// Handler is the /rt WebSocket endpoint. Mount with
// mux.HandleFunc("/rt", h.HandleWebSocket).
type Handler struct {
	logger         infra.Logger
	factory        ManagerFactory
	allowedOrigins map[string]bool
	upgrader       websocket.Upgrader
	cfg            ServerConfig
}

// NewHandler validates the configuration, fills defaults for zero fields,
// and prepares the upgrader. A nil factory is rejected (ErrNilManagerFactory);
// a nil logger is replaced with the no-op logger (D-42).
func NewHandler(cfg ServerConfig, factory ManagerFactory, logger infra.Logger) (*Handler, error) {
	if factory == nil {
		return nil, ErrNilManagerFactory
	}
	if logger == nil {
		logger = infra.NewNoOpLogger()
	}

	// Fill defaults before validating so "0" means default, never "broken".
	if cfg.MaxMessageBytes == 0 {
		cfg.MaxMessageBytes = DefaultMaxMessageBytes
	}
	if cfg.ReadBufferSize == 0 {
		cfg.ReadBufferSize = defaultReadBufferSize
	}
	if cfg.WriteBufferSize == 0 {
		cfg.WriteBufferSize = defaultWriteBufferSize
	}
	if cfg.HandshakeTimeout == 0 {
		cfg.HandshakeTimeout = defaultHandshakeTimeout
	}
	if cfg.HeartbeatInterval == 0 {
		cfg.HeartbeatInterval = DefaultHeartbeatInterval
	}
	if err := cfg.Validate(); err != nil {
		return nil, errors.Wrap(err, "server config validation failed")
	}

	// Origin whitelist with the same scheme-flexibility pkg/ws applies:
	// a bare host entry matches both http and https forms of itself.
	allowedOrigins := make(map[string]bool, len(cfg.AllowedOrigins)*3)
	for _, origin := range cfg.AllowedOrigins {
		allowedOrigins[origin] = true
		if !strings.HasPrefix(origin, "http://") && !strings.HasPrefix(origin, "https://") {
			allowedOrigins["http://"+origin] = true
			allowedOrigins["https://"+origin] = true
		}
	}

	h := &Handler{
		cfg:            cfg,
		factory:        factory,
		logger:         logger,
		allowedOrigins: allowedOrigins,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  cfg.ReadBufferSize,
			WriteBufferSize: cfg.WriteBufferSize,
			// Safari: with permessage-deflate enabled, Safari 17.6 throws
			// "Protocol error" when a TEXT control frame is followed by a
			// BINARY audio frame — fatal for a JSON+PCM mixed stream (same
			// D-02/D-09 mitigation pkg/ws ships; Apple Developer Forum
			// 744018, uWebSockets#1347).
			EnableCompression: false,
			CheckOrigin: func(r *http.Request) bool {
				origin := r.Header.Get("Origin")
				if origin == "" {
					// CR-05: browsers always send Origin on cross-origin
					// WebSocket requests; an empty Origin means a
					// non-browser client. Reject rather than weaken the
					// browser path (D-24 forbids '*').
					return false
				}
				// Exact whitelist match first (scheme+host+port).
				if allowedOrigins[origin] {
					return true
				}
				// Port-tolerant fallback: whitelist entries conventionally
				// omit the port while browsers include it. Security is
				// preserved — the port-less origin must still be listed.
				if stripped := stripOriginPort(origin); stripped != origin && allowedOrigins[stripped] {
					return true
				}
				return false
			},
			HandshakeTimeout: cfg.HandshakeTimeout,
		},
	}
	return h, nil
}

// HandleWebSocket upgrades an HTTP request to the /rt WebSocket connection
// and runs its read loop to completion.
//
// Timeouts and h2 notes (Pitfall 8, assumption A2): after the hijack the
// connection is no longer subject to http.Server WriteTimeout — the legacy
// /ws endpoint has run for months under a 10s WriteTimeout on this very
// property. WebSocket-level timeouts are handled here per message (write)
// and via the heartbeat-rolled read deadline. If wss handshake issues ever
// appear under TLS, the documented first排查 step is disabling h2 by setting
// TLSNextProto to an empty map on the http.Server — see 13-06 TLS wiring.
//
// Lifecycle: one ManagerFactory call per connection; the read loop
// dispatches frames until ReadMessage fails (disconnect, deadline, read
// limit); teardown then (1) lets the session close itself through the
// writer, (2) stops the writer with a bounded flush plus a WebSocket close
// frame, (3) closes the connection.
func (h *Handler) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		// gorilla already wrote the HTTP error response.
		h.logger.Warn("rt upgrade failed", infra.ErrField(err))
		return
	}

	// Read governance: hard cap per message, idle timeout rolled forward by
	// pongs (CR-02/CR-03 semantics from pkg/ws). SetReadDeadline is an
	// absolute time, so it is refreshed on every pong — a healthy client
	// that answers pings never hits it; two consecutive missed pings
	// (readTimeout = 2×HeartbeatInterval) mark the connection dead.
	conn.SetReadLimit(h.cfg.MaxMessageBytes)
	readTimeout := h.cfg.HeartbeatInterval * 2
	if readTimeout <= 0 {
		readTimeout = 60 * time.Second
	}
	_ = conn.SetReadDeadline(time.Now().Add(readTimeout))
	conn.SetPongHandler(func(string) error {
		_ = conn.SetReadDeadline(time.Now().Add(readTimeout))
		return nil
	})

	// ctx binds the heartbeat goroutine to this handler's lifetime.
	ctx, cancel := context.WithCancel(r.Context())
	var heartbeatDone chan struct{}

	if h.cfg.HeartbeatInterval > 0 {
		heartbeatDone = make(chan struct{})
		go func() {
			defer close(heartbeatDone)
			h.heartbeat(ctx, conn, h.cfg.HeartbeatInterval)
		}()
	}

	manager := h.factory()
	writer := newConnWriter(conn, defaultSendQueue, h.logger)
	writer.start()

	// Transport only moves bytes: binary → audio, text → control
	// (Pattern 1). Any decision beyond routing belongs to the manager.
	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			break
		}
		switch mt {
		case websocket.BinaryMessage:
			manager.IngestAudio(writer, data)
		case websocket.TextMessage:
			manager.HandleControl(writer, data)
		}
	}

	// Teardown order matters: the session closes first so its final control
	// messages (session_ended and friends) are queued; the writer then
	// drains with a bounded grace and sends the close frame; only then is
	// the connection itself closed.
	manager.Close(writer)
	writer.shutdown()
	_ = conn.Close()
	cancel()
	if heartbeatDone != nil {
		<-heartbeatDone
	}
}

// heartbeat pings the client every interval until ctx is cancelled or a
// ping write fails. Pings use WriteControl, which gorilla permits alongside
// the writer pump's single WriteMessage caller (WR-06).
func (h *Handler) heartbeat(ctx context.Context, conn *websocket.Conn, interval time.Duration) {
	if interval <= 0 {
		// WR-06: time.NewTicker panics on non-positive intervals; the
		// config guard above makes this unreachable, validate anyway.
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Half-interval write deadline: never block longer than one
			// tick on a stalled client.
			if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(interval/2)); err != nil {
				h.logger.Info("rt heartbeat ping failed; exiting", infra.ErrField(err))
				return
			}
		}
	}
}

// stripOriginPort removes the port from an Origin header value, preserving
// scheme and host (pkg/ws's port-tolerant fallback, copied verbatim in
// behavior). Unparseable or port-less values are returned unchanged.
func stripOriginPort(origin string) string {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return origin
	}
	if u.Port() == "" {
		return origin
	}
	u.Host = u.Hostname()
	return u.String()
}

// outboundFrame is one queued wire message: a control message (TextMessage)
// or one PCM chunk (BinaryMessage). 单元 1 前移的最小顺序保证（路径 A 复盘）：
// 同一播报的控制帧与 PCM 按入队顺序写出——两条独立通道的 select 会让零延迟
// 供应商的第一块 PCM 抢在 audio_start 之前（页面丢块）。容量与配额语义留给
// 单元 2 的统一帧队列，这里只做最小的 FIFO 有序发送。
type outboundFrame struct {
	mt   int
	data []byte
}

// connWriter is the per-connection single-writer send pump. Control and
// audio flow through ONE bounded FIFO queue — the wire order is the enqueue
// order, so an utterance's audio_start can never trail its first PCM chunk
// (the double-select reordering defect the zero-delay fixture exposed); one
// goroutine owns every WriteMessage call (the only gorilla-permitted
// writer), so the session manager can call SendControl/SendAudio from any
// goroutine without locks.
type connWriter struct {
	ctx           context.Context
	logger        infra.Logger
	reason        error
	frames        chan outboundFrame
	cancel        context.CancelFunc
	done          chan struct{}
	conn          *websocket.Conn
	droppedAudio  atomic.Int64
	startOnce     sync.Once
	terminateOnce sync.Once
	terminationMu sync.Mutex
	terminated    bool
}

// newConnWriter builds the writer. The pump is NOT started here — call
// start() once (HandleWebSocket does; tests exercise enqueue semantics
// without the pump before starting it).
func newConnWriter(conn *websocket.Conn, queueSize int, logger infra.Logger) *connWriter {
	if queueSize <= 0 {
		queueSize = defaultSendQueue
	}
	if logger == nil {
		logger = infra.NewNoOpLogger()
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &connWriter{
		conn:    conn,
		frames:  make(chan outboundFrame, queueSize),
		logger:  logger,
		ctx:     ctx,
		cancel:  cancel,
		done:    make(chan struct{}),
	}
}

// start launches the pump goroutine. Call exactly once.
func (w *connWriter) start() {
	w.startOnce.Do(func() {
		go func() {
			defer close(w.done)
			w.run()
		}()
	})
}

// shutdown stops the pump after a bounded drain and waits for it to exit.
func (w *connWriter) shutdown() {
	w.Terminate(nil)
	<-w.done
}

func (w *connWriter) Terminate(reason error) {
	w.terminateOnce.Do(func() {
		w.terminationMu.Lock()
		w.terminated, w.reason = true, reason
		w.terminationMu.Unlock()
		if reason != nil && w.conn != nil {
			_ = w.conn.Close()
		}
		w.cancel()
	})
}

func (w *connWriter) Done() <-chan struct{} { return w.done }

func (w *connWriter) TerminationReason() error {
	w.terminationMu.Lock()
	defer w.terminationMu.Unlock()
	return w.reason
}

// run is the pump body: serialize every outbound message through one
// WriteMessage caller, exit on the first write error or on cancellation
// (after draining). The pre-frame hook is a no-op outside the rtbrowser
// test build (speech_fault_default.go).
func (w *connWriter) run() {
	for {
		speechFaultPumpGateWait(w.ctx)
		select {
		case f := <-w.frames:
			if err := w.write(f.mt, f.data); err != nil {
				w.failSend(frameKind(f.mt), err)
				return
			}
		case <-w.ctx.Done():
			if w.TerminationReason() != nil {
				return
			}
			w.drainAndClose()
			return
		}
	}
}

// frameKind names a frame for failure logging by its WebSocket message type.
func frameKind(mt int) string {
	if mt == websocket.BinaryMessage {
		return "audio"
	}
	return "control"
}

func (w *connWriter) write(mt int, data []byte) error {
	_ = w.conn.SetWriteDeadline(time.Now().Add(perMessageWriteTimeout))
	return w.conn.WriteMessage(mt, data)
}

// drainAndClose flushes what teardown enqueued (bounded by drainGrace, and
// by queue emptiness), then sends the WebSocket close frame.
func (w *connWriter) drainAndClose() {
	deadline := time.Now().Add(drainGrace)
	for time.Now().Before(deadline) {
		select {
		case f := <-w.frames:
			if err := w.write(f.mt, f.data); err != nil {
				w.publishWriteFailure(errors.Wrap(err, "rt: drain "+frameKind(f.mt)+" frame"))
				return
			}
		default:
			// Queue empty — no reason to burn the rest of the grace.
			_ = w.conn.WriteControl(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
				time.Now().Add(time.Second))
			return
		}
	}
	// Grace exhausted mid-flush: still attempt a close frame, best effort.
	_ = w.conn.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
		time.Now().Add(time.Second))
}

// failSend is the send pump's ONE write-failure egress (both frame kinds,
// pump loop and drain alike): first-wins termination, then the
// UNCONDITIONAL propagation. 单元 1 review P1: Terminate alone is not
// enough — when the writer was ALREADY terminated normally (Terminate(nil)
// consumed terminateOnce), Terminate(err) is a no-op; the failure must still
// upgrade the nil reason and close the underlying conn so the read loop
// wakes. That is publishWriteFailure's job here.
func (w *connWriter) failSend(kind string, err error) {
	w.Terminate(errors.Wrap(err, "rt: write "+kind+" frame"))
	w.publishWriteFailure(errors.Wrap(err, "rt: write "+kind+" frame"))
	w.logWriteFailure(kind, err)
}

// publishWriteFailure propagates one send-side write failure unconditionally
// — even when Terminate already ran (first-wins keeps terminateOnce, but a
// normal Terminate(nil) must never swallow a subsequent write failure: the
// nil reason upgrades to the error and the underlying connection closes so
// the read loop wakes 单元 1 review P1).
func (w *connWriter) publishWriteFailure(err error) {
	w.terminationMu.Lock()
	if w.reason == nil {
		w.reason = err
	}
	w.terminationMu.Unlock()
	if w.conn != nil {
		_ = w.conn.Close()
	}
}

func (w *connWriter) logWriteFailure(kind string, err error) {
	w.logger.Warn("rt write failed; stopping send pump",
		infra.F("component", "pkg/rt.conn"),
		infra.F("kind", kind),
		infra.ErrField(err),
	)
}

// SendControl implements Sender. The message is marshaled immediately so
// serialization errors surface at the call site; a saturated queue returns
// ErrControlQueueFull instead of blocking (control is too important to drop
// and too small to deserve unbounded buffering — and it never displaces
// already-queued PCM: the queue is FIFO).
func (w *connWriter) SendControl(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return errors.Wrap(err, "rt: marshal control message")
	}
	// Test-build fault point at the real enqueue site (no-op in production).
	if ferr := speechFaultInterceptSend("control", data); ferr != nil {
		return ferr
	}
	w.terminationMu.Lock()
	defer w.terminationMu.Unlock()
	if w.terminated {
		return ErrWriterTerminated
	}
	select {
	case w.frames <- outboundFrame{mt: websocket.TextMessage, data: data}:
		return nil
	default:
		return ErrControlQueueFull
	}
}

// SendAudio implements Sender. The chunk is copied (audio buffers are
// pooled and reused upstream); a saturated queue drops the chunk, counts
// it, and returns nil — the real-time stream prefers a gap to a stall, and
// the 150MB budget prefers a bounded queue to an unbounded one (T-13-02).
func (w *connWriter) SendAudio(b []byte) error {
	// Test-build fault point at the real enqueue site (no-op in production).
	if ferr := speechFaultInterceptSend("audio", b); ferr != nil {
		return ferr
	}
	cp := make([]byte, len(b))
	copy(cp, b)
	w.terminationMu.Lock()
	defer w.terminationMu.Unlock()
	if w.terminated {
		return ErrWriterTerminated
	}
	select {
	case w.frames <- outboundFrame{mt: websocket.BinaryMessage, data: cp}:
		return nil
	default:
		n := w.droppedAudio.Add(1)
		w.logger.Warn("rt audio queue full; dropping chunk",
			infra.F("component", "pkg/rt.conn"),
			infra.F("dropped_total", n),
		)
		return nil
	}
}
