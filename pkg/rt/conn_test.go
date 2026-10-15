package rt

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pkg/errors"
)

// ---------------------------------------------------------------------------
// Test doubles
// ---------------------------------------------------------------------------

// fakeManager captures everything the transport hands a session manager.
type fakeManager struct {
	onControl func(s Sender, data []byte)
	controls  [][]byte
	audio     [][]byte
	mu        sync.Mutex
	closed    bool
}

func newFakeManager() *fakeManager { return &fakeManager{} }

func (f *fakeManager) HandleControl(s Sender, data []byte) {
	f.mu.Lock()
	f.controls = append(f.controls, append([]byte(nil), data...))
	hook := f.onControl
	f.mu.Unlock()
	if hook != nil {
		hook(s, data)
	}
}

func (f *fakeManager) IngestAudio(_ Sender, data []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.audio = append(f.audio, append([]byte(nil), data...))
}

func (f *fakeManager) Close(Sender) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
}

func (f *fakeManager) setOnControl(hook func(Sender, []byte)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onControl = hook
}

func (f *fakeManager) controlCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.controls)
}

func (f *fakeManager) audioCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.audio)
}

func (f *fakeManager) isClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

func (f *fakeManager) firstControl() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.controls) == 0 {
		return nil
	}
	return f.controls[0]
}

func (f *fakeManager) firstAudio() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.audio) == 0 {
		return nil
	}
	return f.audio[0]
}

// waitFor polls cond until it holds or the timeout elapses.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}

// newRTServer mounts a /rt handler on an httptest server with the given
// config mutation and fake manager.
func newRTServer(t *testing.T, mutate func(*ServerConfig), fm *fakeManager) *httptest.Server {
	t.Helper()
	cfg := ServerConfig{AllowedOrigins: []string{"http://origin.test"}}
	if mutate != nil {
		mutate(&cfg)
	}
	h, err := NewHandler(cfg, func() SessionManager { return fm }, nil)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(h.HandleWebSocket))
	t.Cleanup(srv.Close)
	return srv
}

// dialRT dials the /rt endpoint with an explicit Origin ("" sends none).
func dialRT(t *testing.T, srvURL, origin string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	d := websocket.Dialer{HandshakeTimeout: 2 * time.Second}
	header := http.Header{}
	if origin != "" {
		header.Set("Origin", origin)
	}
	u := "ws" + strings.TrimPrefix(srvURL, "http")
	return d.Dial(u, header)
}

// newConnPair upgrades a throwaway server connection and dials its client
// counterpart, for unit tests that need a raw *websocket.Conn.
func newConnPair(t *testing.T) (serverConn, clientConn *websocket.Conn) {
	t.Helper()
	ch := make(chan *websocket.Conn, 1)
	up := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		ch <- c
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	cl, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial conn pair: %v", err)
	}
	t.Cleanup(func() { _ = cl.Close() })
	select {
	case sc := <-ch:
		t.Cleanup(func() { _ = sc.Close() })
		return sc, cl
	case <-time.After(2 * time.Second):
		t.Fatal("conn pair: server side never upgraded")
		return nil, nil
	}
}

// ---------------------------------------------------------------------------
// Origin enforcement (T-13-01)
// ---------------------------------------------------------------------------

func TestOriginEnforcement(t *testing.T) {
	t.Run("empty origin rejected with 403", func(t *testing.T) {
		srv := newRTServer(t, nil, newFakeManager())
		conn, resp, err := dialRT(t, srv.URL, "")
		if err == nil {
			_ = conn.Close()
			t.Fatal("dial without Origin header succeeded, want rejection")
		}
		if resp == nil || resp.StatusCode != http.StatusForbidden {
			t.Fatalf("status = %v, want 403", resp)
		}
	})

	t.Run("exact whitelist match allowed", func(t *testing.T) {
		srv := newRTServer(t, nil, newFakeManager())
		conn, _, err := dialRT(t, srv.URL, "http://origin.test")
		if err != nil {
			t.Fatalf("dial with exact origin: %v", err)
		}
		_ = conn.Close()
	})

	t.Run("port-suffixed origin allowed via strip fallback", func(t *testing.T) {
		srv := newRTServer(t, nil, newFakeManager())
		conn, _, err := dialRT(t, srv.URL, "http://origin.test:8443")
		if err != nil {
			t.Fatalf("dial with port-suffixed origin: %v", err)
		}
		_ = conn.Close()
	})

	t.Run("unlisted origin rejected with 403", func(t *testing.T) {
		srv := newRTServer(t, nil, newFakeManager())
		conn, resp, err := dialRT(t, srv.URL, "http://evil.example")
		if err == nil {
			_ = conn.Close()
			t.Fatal("dial with unlisted origin succeeded, want rejection")
		}
		if resp == nil || resp.StatusCode != http.StatusForbidden {
			t.Fatalf("status = %v, want 403", resp)
		}
	})
}

func TestConnConfigValidation(t *testing.T) {
	factory := func() SessionManager { return newFakeManager() }

	if _, err := NewHandler(ServerConfig{AllowedOrigins: []string{"*"}}, factory, nil); err == nil {
		t.Error("wildcard origin accepted, want rejection")
	}
	if _, err := NewHandler(ServerConfig{}, factory, nil); err == nil {
		t.Error("empty origin list accepted, want rejection")
	}
	if _, err := NewHandler(ServerConfig{AllowedOrigins: []string{"http://x.test"}}, nil, nil); !errors.Is(err, ErrNilManagerFactory) {
		t.Errorf("nil factory err = %v, want ErrNilManagerFactory", err)
	}
	if _, err := NewHandler(ServerConfig{AllowedOrigins: []string{"http://x.test"}, HeartbeatInterval: -time.Second}, factory, nil); err == nil {
		t.Error("negative heartbeat interval accepted, want rejection")
	}
	// All-zero non-origin fields mean defaults — handler builds fine.
	if _, err := NewHandler(ServerConfig{AllowedOrigins: []string{"http://x.test"}}, factory, nil); err != nil {
		t.Errorf("defaulted config rejected: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Read limit (T-13-02)
// ---------------------------------------------------------------------------

func TestReadLimit(t *testing.T) {
	fm := newFakeManager()
	srv := newRTServer(t, func(c *ServerConfig) { c.MaxMessageBytes = 64 }, fm)

	conn, _, err := dialRT(t, srv.URL, "http://origin.test")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))

	// 200 bytes against a 64-byte limit: the server's read loop must abort
	// and tear the connection down.
	if err := conn.WriteMessage(websocket.BinaryMessage, make([]byte, 200)); err != nil {
		t.Fatalf("write oversized frame: %v", err)
	}
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("connection still readable after oversized frame, want close")
	}

	if !waitFor(t, 2*time.Second, fm.isClosed) {
		t.Error("manager.Close not called after read-limit disconnect")
	}
}

// ---------------------------------------------------------------------------
// Routing: text → HandleControl, binary → IngestAudio, close → Close
// ---------------------------------------------------------------------------

func TestRoutingToManager(t *testing.T) {
	fm := newFakeManager()
	srv := newRTServer(t, nil, fm)

	conn, _, err := dialRT(t, srv.URL, "http://origin.test")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	ctrl := []byte(`{"type":"ping","t_client_ms":123}`)
	pcm := []byte{0x01, 0x02, 0x03, 0xFE}
	if err := conn.WriteMessage(websocket.TextMessage, ctrl); err != nil {
		t.Fatalf("write control: %v", err)
	}
	if err := conn.WriteMessage(websocket.BinaryMessage, pcm); err != nil {
		t.Fatalf("write audio: %v", err)
	}

	if !waitFor(t, 2*time.Second, func() bool { return fm.controlCount() >= 1 && fm.audioCount() >= 1 }) {
		t.Fatalf("frames not routed: controls=%d audio=%d", fm.controlCount(), fm.audioCount())
	}
	if got := fm.firstControl(); string(got) != string(ctrl) {
		t.Errorf("HandleControl payload = %q, want %q", got, ctrl)
	}
	if got := fm.firstAudio(); !equalBytes(got, pcm) {
		t.Errorf("IngestAudio payload = %v, want %v", got, pcm)
	}

	// Client disconnect → read loop exits → session released.
	_ = conn.Close()
	if !waitFor(t, 2*time.Second, fm.isClosed) {
		t.Error("manager.Close not called after client disconnect")
	}
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Writer delivery + copy semantics
// ---------------------------------------------------------------------------

// TestWriterDelivers: a manager replying through the Sender delivers a JSON
// TextMessage and a PCM BinaryMessage to the client, and SendAudio's copy
// semantics preserve the payload after the caller mutates its slice.
func TestWriterDelivers(t *testing.T) {
	fm := newFakeManager()
	fm.setOnControl(func(s Sender, data []byte) {
		var probe struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(data, &probe) != nil || probe.Type != TypePing {
			return
		}
		_ = s.SendControl(PongMsg{Type: TypePong, TClientMs: 777, TServerMonoNs: 42})
		pcm := []byte{0x11, 0x22, 0x33}
		_ = s.SendAudio(pcm)
		pcm[0] = 0xFF // mutate after send — writer must have copied
	})
	srv := newRTServer(t, nil, fm)

	conn, _, err := dialRT(t, srv.URL, "http://origin.test")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))

	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"ping"}`)); err != nil {
		t.Fatalf("write ping: %v", err)
	}

	got := map[int][]byte{}
	for len(got) < 2 {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read reply: %v (got so far: %v)", err, got)
		}
		got[mt] = data
	}

	// Order between control and audio is unspecified (select); classify.
	text, ok := got[websocket.TextMessage]
	if !ok {
		t.Fatalf("no TextMessage reply delivered (got %v)", got)
	}
	var pong PongMsg
	if err := json.Unmarshal(text, &pong); err != nil {
		t.Fatalf("text reply is not a pong: %v (%s)", err, text)
	}
	if pong.Type != TypePong || pong.TClientMs != 777 || pong.TServerMonoNs != 42 {
		t.Errorf("pong = %+v, want echoed t_client_ms=777 t_server_mono_ns=42", pong)
	}

	bin, ok := got[websocket.BinaryMessage]
	if !ok {
		t.Fatalf("no BinaryMessage audio delivered (got %v)", got)
	}
	if !equalBytes(bin, []byte{0x11, 0x22, 0x33}) {
		t.Errorf("audio payload = %v, want [17 34 51] (post-send mutation leaked)", bin)
	}
}

// ---------------------------------------------------------------------------
// Writer backpressure (T-13-02): audio drops, control errors, neither blocks
// ---------------------------------------------------------------------------

func TestWriterBackpressure(t *testing.T) {
	sc, _ := newConnPair(t)
	w := newConnWriter(sc, 2, nil) // queue size 2; pump deliberately NOT started

	// Audio: fill 2, overflow 2 — every call returns nil, 2 counted drops,
	// and the loop never blocks.
	pcm := make([]byte, 8)
	for i := 0; i < 4; i++ {
		if err := w.SendAudio(pcm); err != nil {
			t.Fatalf("SendAudio #%d: %v (drop path must return nil)", i, err)
		}
	}
	if got := w.droppedAudio.Load(); got != 2 {
		t.Errorf("droppedAudio = %d, want 2", got)
	}

	// Control: fill 2, third call errors — control never vanishes silently.
	for i := 0; i < 2; i++ {
		if err := w.SendControl(PongMsg{Type: TypePong}); err != nil {
			t.Fatalf("SendControl #%d: %v", i, err)
		}
	}
	if err := w.SendControl(PongMsg{Type: TypePong}); !errors.Is(err, ErrControlQueueFull) {
		t.Errorf("saturated SendControl err = %v, want ErrControlQueueFull", err)
	}

	// Marshal failures surface at the call site.
	if err := w.SendControl(make(chan int)); err == nil {
		t.Error("unmarshalable control value returned nil error")
	}

	// Clean shutdown through the real pump path.
	w.start()
	w.shutdown()
}

// ---------------------------------------------------------------------------
// Heartbeat rolls the read deadline (T-13-02)
// ---------------------------------------------------------------------------

// TestHeartbeatRollsDeadline: with a 50ms heartbeat (read deadline 100ms),
// a connection left application-silent for 300ms must STILL be alive and
// routing — the client's automatic pongs (emitted while it reads, so the
// test keeps a reader goroutine draining) roll the deadline forward.
// Without rolling, the read loop would have exited at ~100ms and the final
// frame would never arrive.
func TestHeartbeatRollsDeadline(t *testing.T) {
	fm := newFakeManager()
	srv := newRTServer(t, func(c *ServerConfig) { c.HeartbeatInterval = 50 * time.Millisecond }, fm)

	conn, _, err := dialRT(t, srv.URL, "http://origin.test")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// Reader goroutine: processes the server's pings (auto-pong) during the
	// silent window. gorilla only answers control frames while reading.
	go func() {
		for {
			_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	time.Sleep(300 * time.Millisecond) // application silence — pings/pongs only

	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"ping","t_client_ms":9}`)); err != nil {
		t.Fatalf("write after silence: %v", err)
	}
	if !waitFor(t, 2*time.Second, func() bool { return fm.controlCount() >= 1 }) {
		t.Fatal("read loop dead after 300ms silence — deadline did not roll on pongs")
	}

	_ = conn.Close()
	if !waitFor(t, 2*time.Second, fm.isClosed) {
		t.Error("manager.Close not called after disconnect")
	}
}
