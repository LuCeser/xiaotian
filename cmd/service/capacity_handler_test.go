package main

// Ticket 07 capacity tests at the handler level: a start_session beyond the
// Manager's cap answers a friendly error frame (graceful degradation), and
// the client can retry once a slot frees.

import (
	"context"
	"testing"

	"go.uber.org/zap"

	"github.com/xiaotian/home-tutor-agent/pkg/fsrs"
	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/session"
	"github.com/xiaotian/home-tutor-agent/pkg/ws"
)

// TestStartSession_AtCapacityFriendlyError: cap=1; the second connection's
// start_session gets the friendly busy message (not a raw error string).
func TestStartSession_AtCapacityFriendlyError(t *testing.T) {
	logger := infra.NewNoOpLogger()
	zapLogger := zap.NewNop()

	hub, err := ws.NewHub(ws.HubConfig{Limit: 10, RegisterBuffer: 32, UnregisterBuffer: 32}, zapLogger)
	if err != nil {
		t.Fatalf("hub: %v", err)
	}

	cfg := infra.DefaultConfig()
	cfg.Session.MaxConcurrent = 1
	pool, err := infra.NewWorkerPool(cfg.Worker, logger)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(func() { _ = pool.Shutdown(context.Background()) })

	fakeFSRS := &batchFSRS{}
	manager, err := session.NewManager(hub, fakeFSRS, &fsmGen{}, &fsmStreamer{}, &fsmRepo{}, pool, logger, cfg)
	if err != nil {
		t.Fatalf("manager: %v", err)
	}
	svcCtx, svcCancel := context.WithCancel(context.Background())
	t.Cleanup(svcCancel)
	go hub.Run(svcCtx)
	go func() {
		if startErr := manager.Start(svcCtx); startErr != nil {
			logger.Error("session manager start failed", infra.ErrField(startErr))
		}
	}()

	handlers := ttsHandlers(manager, fakeFSRS, nil, nil, logger)

	// Connection 1: fill the single slot.
	conn1, teardown1 := dialVoiceTestServerOnHub(t, hub, handlers)
	defer teardown1()
	sendControlJSON(t, conn1, `{"type":"start_session","params":{"limit":2,"category":"chinese"}}`)
	if ready := readControlUntil(t, conn1, "session_ready"); ready == "" {
		t.Fatal("first session did not start")
	}

	// Connection 2: rejected with the friendly message.
	conn2, teardown2 := dialVoiceTestServerOnHub(t, hub, handlers)
	defer teardown2()
	sendControlJSON(t, conn2, `{"type":"start_session","params":{"limit":2,"category":"chinese"}}`)
	if payload := readErrorUntil(t, conn2, "稍等一分钟"); payload == "" {
		t.Fatal("no friendly capacity error on second connection")
	}
}

// The FSRS fake stays interface-complete (compile-time guard mirrors the
// package convention).
var _ fsrs.FSRSService = (*batchFSRS)(nil)
