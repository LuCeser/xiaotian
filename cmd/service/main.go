// Package main is the Home Tutor Agent Go service entry point (D-42, D-47).
//
// main wires every infra component via constructors (no globals, no
// package-level mutable state), runs the assembled Server until the
// process receives SIGTERM or SIGINT, then drains gracefully inside
// cfg.Shutdown.Timeout. Single-instance posture (D-20): one process, one
// Server, no service discovery or leader election.
//
// The testable entry point is run(ctx, configPath, healthAddrOverride).
// main() parses flags, builds the signal-bound context, and delegates.
// This separation lets tests exercise the wiring without depending on
// process-level signal handling (which is platform-specific — Windows
// dev hosts do not support syscall.Kill with SIGTERM).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/xiaotian/home-tutor-agent/pkg/database"
	"github.com/xiaotian/home-tutor-agent/pkg/fsrs"
	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/pybridge"
	"github.com/xiaotian/home-tutor-agent/pkg/tts"
	"github.com/xiaotian/home-tutor-agent/pkg/ws"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to YAML config file (skipped silently if missing)")
	healthAddr := flag.String("health-addr", "", "override server.healthAddr (host:port); empty keeps config value")
	flag.Parse()

	// signal.NotifyContext cancels the returned ctx on SIGTERM/SIGINT (D-21).
	// The stop() func restores the default signal behavior; deferring it
	// keeps the cancellation channel live for the duration of run().
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	os.Exit(run(ctx, *configPath, *healthAddr))
}

// run is the testable entry point. It loads config, builds the Server,
// starts the health HTTP server, blocks until ctx is cancelled, then
// drains. Returns the process exit code.
//
// ctx is the lifetime context — typically from signal.NotifyContext in
// main, but tests pass a cancellable context directly. When ctx is
// cancelled, run shuts down the health server and Server and returns 0.
//
// configPath is passed to infra.LoadConfig; missing files are silently
// skipped so the service boots without an operator-supplied file. An
// invalid YAML or a validation failure is fatal.
//
// healthAddrOverride, when non-empty, replaces cfg.Server.HealthAddr. Tests
// use this to inject an ephemeral port.
func run(ctx context.Context, configPath, healthAddrOverride string) int {
	// Bootstrap log writes plain lines to stderr. Used ONLY for the
	// pre-config error path (config load / validate / logger build). Once
	// the production infra.Logger is built we switch to it for everything
	// else.
	const bootstrapPrefix = "main: "

	cfg, err := infra.LoadConfig(configPath)
	if err != nil {
		log.SetPrefix(bootstrapPrefix)
		log.SetFlags(0)
		log.Printf("config load failed: %v", err)
		return 1
	}
	if err := cfg.Validate(); err != nil {
		log.SetPrefix(bootstrapPrefix)
		log.SetFlags(0)
		log.Printf("config validation failed: %v", err)
		return 1
	}

	logger, err := infra.NewLogger(cfg.Log)
	if err != nil {
		log.SetPrefix(bootstrapPrefix)
		log.SetFlags(0)
		log.Printf("logger build failed: %v", err)
		return 1
	}
	defer func() { _ = logger.Sync() }()

	if healthAddrOverride != "" {
		cfg.Server.HealthAddr = healthAddrOverride
	}

	server, err := infra.NewServer(cfg, logger)
	if err != nil {
		logger.Error("server construction failed",
			infra.F("component", "infra.main"),
			infra.ErrField(err),
		)
		return 1
	}

	// Phase 3 (D-03/D-04/D-05): construct the Go <-> Python subprocess bridge.
	// NewBridge is a pure constructor -- it spawns no subprocess and performs
	// no file IO, so it is safe to call at boot even when the configured
	// Python interpreter is not yet installed (e.g. main_test.go boot tests
	// use the literal string "python3"). The pyBridge reference is held for
	// Phase 5 (FSRS orchestration) and Phase 7 (session flow), which reach
	// the bridge via RunDue/RunReview/RunPregenerate.
	pyBridge, err := pybridge.NewBridge(cfg.Python, logger)
	if err != nil {
		logger.Error("pybridge construction failed",
			infra.F("component", "infra.main"),
			infra.ErrField(err),
		)
		return 1
	}

	// Phase 5 (D-01): FSRS orchestration service. Composes pyBridge via the
	// Executor interface. NewFSRSService is a pure constructor — no subprocess
	// spawned, same contract as NewBridge.
	fsrsSvc, err := fsrs.NewFSRSService(pyBridge, logger, cfg.FSRS)
	if err != nil {
		logger.Error("fsrs service construction failed",
			infra.F("component", "infra.main"),
			infra.ErrField(err),
		)
		return 1
	}

	// Phase 6 (TTS audio streaming): TTS component construction.
	// NewSessionCache is a pure constructor (creates directories if needed).
	cache, err := tts.NewSessionCache(cfg.TTS.SessionDir, logger)
	if err != nil {
		logger.Error("TTS session cache construction failed",
			infra.F("component", "infra.main"),
			infra.ErrField(err),
		)
		return 1
	}

	// NewGenerator is a pure constructor (composes pyBridge, cache, logger).
	gen, err := tts.NewGenerator(pyBridge, cache, logger, cfg.TTS)
	if err != nil {
		logger.Error("TTS generator construction failed",
			infra.F("component", "infra.main"),
			infra.ErrField(err),
		)
		return 1
	}

	// NewStreamer is a pure constructor (composes cache, logger).
	streamer, err := tts.NewStreamer(cache, logger, cfg.TTS)
	if err != nil {
		logger.Error("TTS streamer construction failed",
			infra.F("component", "infra.main"),
			infra.ErrField(err),
		)
		return 1
	}

	// Phase 4 (D-09): open the shared SQLite database.
	// The path comes from Config.Database.Path; when empty, resolve to
	// $HOME/.home-tutor-agent/vocab.db (mirrors PythonConfig default philosophy).
	// sql.DB lifetime = service lifetime. Closed during shutdown via defer.
	dbPath := cfg.Database.Path
	if dbPath == "" {
		dbPath = filepath.Join(homeDir(), ".home-tutor-agent", "vocab.db")
	}
	db, err := database.Open(dbPath)
	if err != nil {
		logger.Error("database open failed",
			infra.F("component", "infra.main"),
			infra.F("path", dbPath),
			infra.ErrField(err),
		)
		return 1
	}
	defer func() {
		if err := db.Close(); err != nil {
			logger.Warn("database close error",
				infra.F("component", "infra.main"),
				infra.ErrField(err),
			)
		}
	}()

	// Run idempotent migration for Go-owned tables (D-08).
	// Does NOT touch Python-owned cards/review_logs.
	if err := database.Migrate(ctx, db); err != nil {
		logger.Error("database migration failed",
			infra.F("component", "infra.main"),
			infra.ErrField(err),
		)
		return 1
	}

	// Construct repositories (D-07, D-42 constructor DI).
	cardRepo, err := database.NewCardRepository(db, logger)
	if err != nil {
		logger.Error("card repository construction failed",
			infra.F("component", "infra.main"),
			infra.ErrField(err),
		)
		return 1
	}
	sessionRepo, err := database.NewSessionRepository(db, logger)
	if err != nil {
		logger.Error("session repository construction failed",
			infra.F("component", "infra.main"),
			infra.ErrField(err),
		)
		return 1
	}
	_ = cardRepo    // consumed by Phase 5 FSRS orchestration
	_ = sessionRepo // consumed by Phase 7 session management

	if err := infra.RegisterMetrics(server); err != nil {
		logger.Error("metrics registration failed",
			infra.F("component", "infra.main"),
			infra.ErrField(err),
		)
		return 1
	}

	if err := server.Start(ctx); err != nil {
		logger.Error("server start failed",
			infra.F("component", "infra.main"),
			infra.ErrField(err),
		)
		return 1
	}

	// ------------------------------------------------------------------
	// Public WebSocket server (P0-1). Phase 2 delivered the pkg/ws library
	// (Hub/Client/Server/Router); this is the runtime connective tissue that
	// makes the service actually listen on cfg.Server.Addr (:8080) and serve
	// the /ws upgrade route. Handlers are no-op (Phase 6/7 placeholders); the
	// upgrade, Hub registration, heartbeat, frame decode, and Router dispatch
	// all go live here. Double validation: infra WSConfig owns defaults/
	// overrides, the ws package constructors (NewHub/NewRouter/NewServer) own
	// semantic defaults and re-validate.
	// ------------------------------------------------------------------
	hubCfg := ws.HubConfig{
		Limit:            cfg.WS.HubLimit,
		RegisterBuffer:   cfg.WS.HubRegisterBuffer,
		UnregisterBuffer: cfg.WS.HubUnregisterBuffer,
	}
	routerCfg := ws.RouterConfig{
		RouteBuffer:       cfg.WS.RouteBuffer,
		WorkerConcurrency: cfg.WS.WorkerConcurrency,
	}
	// Map cfg.WS → ws.ServerConfig. The infra layer uses 0 as the "use ws
	// package default" sentinel for the size/buffer fields. ReadBufferSize and
	// WriteBufferSize pass ws.ServerConfig.Validate (which only rejects < 0) so
	// NewServer's own 0→4096 fill branch handles them. MaxMessageBytes is
	// different: ws.ServerConfig.Validate rejects <= 0 BEFORE NewServer's
	// 0→DefaultMaxMessageBytes fill branch can run, so the fill must happen
	// here at the wiring layer (double validation: infra owns the override
	// layer, main.go bridges the semantic default, ws re-validates).
	maxMsg := cfg.WS.MaxMessageBytes
	if maxMsg == 0 {
		maxMsg = ws.DefaultMaxMessageBytes // 65551 (HeaderSize + max uint16 payload)
	}
	serverCfg := ws.ServerConfig{
		AllowedOrigins:    cfg.WS.AllowedOrigins,
		MaxMessageBytes:   maxMsg,
		ReadBufferSize:    cfg.WS.ReadBufferSize,    // 0 → ws.NewServer fills 4096
		WriteBufferSize:   cfg.WS.WriteBufferSize,   // 0 → ws.NewServer fills 4096
		HeartbeatInterval: cfg.WS.HeartbeatInterval,
	}

	// pkg/ws takes *zap.Logger directly (it predates the infra.Logger
	// abstraction). Bridge the two: extract the configured zap logger from the
	// production ZapLogger, falling back to a nop logger for the test/no-op
	// path. The level/format/output configured in NewLogger are preserved.
	wsLogger := zap.NewNop()
	if zl, ok := logger.(*infra.ZapLogger); ok {
		wsLogger = zl.Zap()
	}

	hub, err := ws.NewHub(hubCfg, wsLogger)
	if err != nil {
		logger.Error("ws hub construction failed",
			infra.F("component", "infra.main"),
			infra.ErrField(err),
		)
		return 1
	}
	router, err := ws.NewRouter(ttsHandlers(fsrsSvc, gen, streamer, logger), routerCfg, wsLogger)
	if err != nil {
		logger.Error("ws router construction failed",
			infra.F("component", "infra.main"),
			infra.ErrField(err),
		)
		return 1
	}
	wsServer, err := ws.NewServer(hub, router, serverCfg, wsLogger)
	if err != nil {
		logger.Error("ws server construction failed",
			infra.F("component", "infra.main"),
			infra.ErrField(err),
		)
		return 1
	}

	// Hub.Run blocks until ctx.Done then defers Close (CR-01-safe). Bound to
	// the lifetime ctx so it exits as soon as shutdown begins.
	go hub.Run(ctx)

	// Public mux carries ONLY /ws. /health and /metrics stay on the separate
	// localhost healthSrv below (probe/session traffic separation is the
	// established design).
	publicMux := http.NewServeMux()
	publicMux.HandleFunc("/ws", wsServer.HandleWebSocket)
	publicSrv := &http.Server{
		Addr:              cfg.Server.Addr,
		Handler:           publicMux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       cfg.Server.ReadTimeout,
		WriteTimeout:      cfg.Server.WriteTimeout,
	}
	go func() {
		logger.Info("public server listening",
			infra.F("component", "infra.main"),
			infra.F("addr", cfg.Server.Addr),
		)
		if err := publicSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("public server error",
				infra.F("component", "infra.main"),
				infra.ErrField(err),
			)
		}
	}()

	// Health/metrics HTTP server on the dedicated health listener
	// (default 127.0.0.1:8081). Bound separately from the public service
	// addr so probe traffic does not compete with session traffic.
	healthMux := http.NewServeMux()
	healthMux.Handle("/health", infra.RecoveryMiddleware(logger, infra.HealthHandler(server, logger)))
	healthMux.Handle("/metrics", infra.RecoveryMiddleware(logger, infra.MetricsHandler(server, logger)))
	healthSrv := &http.Server{
		Addr:              cfg.Server.HealthAddr,
		Handler:           healthMux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
	}
	go func() {
		logger.Info("health server listening",
			infra.F("component", "infra.main"),
			infra.F("addr", cfg.Server.HealthAddr),
		)
		if err := healthSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("health server error",
				infra.F("component", "infra.main"),
				infra.ErrField(err),
			)
		}
	}()

	// Wait for signal (or test-driven ctx cancellation).
	<-ctx.Done()
	logger.Info("shutdown signal received",
		infra.F("component", "infra.main"),
	)

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), cfg.Shutdown.Timeout)
	defer shutdownCancel()

	// CR-01-safe WS shutdown order (documented contract in
	// pkg/ws/server.go Server.Shutdown): stop the public HTTP listener FIRST so
	// no new HandleWebSocket goroutines start while the Hub is torn down, THEN
	// close the ws subsystem (Hub.Close + Router.Close with its bounded worker
	// wait). The Router internally bounds its own wait at routerShutdownTimeout
	// (5s), so the total stays within cfg.Shutdown.Timeout.
	if err := publicSrv.Shutdown(shutdownCtx); err != nil {
		logger.Warn("public server shutdown error",
			infra.F("component", "infra.main"),
			infra.ErrField(err),
		)
	}
	if err := wsServer.Shutdown(shutdownCtx); err != nil {
		logger.Warn("ws server shutdown error",
			infra.F("component", "infra.main"),
			infra.ErrField(err),
		)
	}
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Warn("server shutdown error",
			infra.F("component", "infra.main"),
			infra.ErrField(err),
		)
	}
	if err := healthSrv.Shutdown(shutdownCtx); err != nil {
		logger.Warn("health server shutdown error",
			infra.F("component", "infra.main"),
			infra.ErrField(err),
		)
	}

	logger.Info("shutdown complete",
		infra.F("component", "infra.main"),
	)
	return 0
}

// ensure fmt is referenced even if future refactors remove the only call
// site. Cheap to keep, painful to forget.
var _ = fmt.Sprintf

// homeDir returns the user's home directory. On error (e.g. HOME not set),
// it returns "." as a safe fallback. Used by the database bootstrap to
// resolve the default vocab.db path when cfg.Database.Path is empty.
func homeDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return "."
}
