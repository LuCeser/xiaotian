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
	"github.com/xiaotian/home-tutor-agent/pkg/mimo"
	"github.com/xiaotian/home-tutor-agent/pkg/pybridge"
	"github.com/xiaotian/home-tutor-agent/pkg/session"
	"github.com/xiaotian/home-tutor-agent/pkg/tts"
	"github.com/xiaotian/home-tutor-agent/pkg/voice"
	"github.com/xiaotian/home-tutor-agent/pkg/web/api"
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

	// Phase 8 Plan 01: validate the static interface directory exists when
	// configured. Fail-fast at boot (Rule 2: no silent 404 storm later) so a
	// misconfigured server.staticDir aborts startup with a clear log line
	// rather than serving an empty shell to every student. An empty StaticDir
	// means static serving is disabled (Database.Path precedent) and is NOT
	// an error.
	if cfg.Server.StaticDir != "" {
		info, err := os.Stat(cfg.Server.StaticDir)
		if err != nil {
			logger.Error("static directory not accessible",
				infra.F("component", "infra.main"),
				infra.F("dir", cfg.Server.StaticDir),
				infra.ErrField(err),
			)
			return 1
		}
		if !info.IsDir() {
			logger.Error("staticDir is not a directory",
				infra.F("component", "infra.main"),
				infra.F("dir", cfg.Server.StaticDir),
			)
			return 1
		}
		logger.Info("static interface directory verified",
			infra.F("component", "infra.main"),
			infra.F("dir", cfg.Server.StaticDir),
			infra.F("route", cfg.Server.StaticRoute),
		)
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

	// Start periodic cleanup goroutine to prevent disk exhaustion on 1C/1G VPS
	// Each session writes ~4MB of PCM files; without cleanup, crashed sessions
	// leave orphan files that fill /tmp within hours (CR-1 fix)
	go func() {
		ticker := time.NewTicker(10 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := cache.CleanupStale(30 * time.Minute); err != nil {
					logger.Warn("TTS cache stale cleanup error", infra.ErrField(err))
				}
			}
		}
	}()

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
	// The path comes from Config.Database.Path. When empty, prefer the
	// Python-owned vocab DB (python.vocabDBPath): the cards table Go reads
	// via CardRepository lives THERE (D-12 read path, ADR write-separation —
	// Python writes, Go reads the same file via multi-process WAL).
	// Phase 9 fix: the old unconditional $HOME/.home-tutor-agent/vocab.db
	// default pointed the read path at a different file than the Python
	// bridge writes, so GET /api/v1/words always saw "no such table: cards".
	// The legacy default remains only as a last resort when BOTH paths are
	// empty. sql.DB lifetime = service lifetime. Closed during shutdown.
	dbPath := cfg.Database.Path
	if dbPath == "" {
		dbPath = cfg.Python.VocabDBPath
	}
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

	// CR-2 fix: Wrap post-Start construction in a closure that defers server.Shutdown.
	// This prevents goroutine leaks (MemoryMonitor + WorkerPool) when any component
	// constructed after server.Start fails. If server.Start succeeds, we MUST call
	// server.Shutdown on any early-return, otherwise those goroutines run forever.
	// The HTTP/WS server handles are declared in the outer scope so the shutdown
	// sequence below can reference them after the closure returns.
	var (
		wsServer   *ws.Server
		publicSrv  *http.Server
		healthSrv  *http.Server
		mimoCloser func() error
	)

	shutdownErr := func() error {
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
			ReadBufferSize:    cfg.WS.ReadBufferSize,  // 0 → ws.NewServer fills 4096
			WriteBufferSize:   cfg.WS.WriteBufferSize, // 0 → ws.NewServer fills 4096
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
			return err
		}

		// Phase 7: construct the session Manager (first real WorkerPool Submit caller — D-06, P1-3).
		_, _, _, workers := server.Components()
		manager, err := session.NewManager(hub, fsrsSvc, gen, streamer, sessionRepo, workers, logger, cfg)
		if err != nil {
			logger.Error("session manager construction failed",
				infra.F("component", "infra.main"),
				infra.ErrField(err),
			)
			return err
		}

		// D-04: wire Hub-backed connection stats into the Server snapshot.
		server.SetConnectionStatsProvider(func() infra.ConnectionStats {
			return hub.Stats()
		})

		go manager.Start(ctx)

		// ------------------------------------------------------------------
		// Phase 8.2 (Ticket 03): voice-command pipeline — streamed mic audio
		// → Mimo ASR → intent classification → session FSM dispatch.
		// Phase 8.2 (Ticket 04): photo-answer OCR pipeline — streamed photo
		// chunks → Mimo vision → answer judging. Both share ONE mimo client
		// (same endpoint and env-only MIMO_API_KEY, D-05/D-24); no key ⇒ both
		// disabled, the service boots and the button controls + manual input
		// remain the full fallback.
		// ------------------------------------------------------------------
		var voiceCmd *voiceService
		var photoCmd *photoService
		if apiKey := os.Getenv("MIMO_API_KEY"); apiKey != "" {
			// The shared client's HTTP timeout must cover the slowest caller:
			// photo's vision OCR (20s) over voice's ASR+LLM sequence (15s).
			// Each pipeline still bounds its own calls via its request ctx.
			mimoTimeout := cfg.Voice.RequestTimeout
			if cfg.Photo.RequestTimeout > mimoTimeout {
				mimoTimeout = cfg.Photo.RequestTimeout
			}
			mimoClient, err := mimo.NewClient(mimo.ClientConfig{
				APIKey:  apiKey,
				BaseURL: cfg.Voice.BaseURL,
				Timeout: mimoTimeout,
			}, logger)
			if err != nil {
				logger.Error("mimo client construction failed",
					infra.F("component", "infra.main"),
					infra.ErrField(err),
				)
				return err
			}
			// Lifetime fix (2026-09-13 UAT): this wiring closure RETURNS while the
			// HTTP/WS servers keep running in goroutines, so a defer HERE would
			// close the client seconds after boot — every session then hit
			// "client is closed" and silently fell back to Volcengine (voice
			// commands / photo OCR would fail the same way). Bind Close to the
			// real shutdown path below via mimoCloser instead.
			mimoCloser = mimoClient.Close

			// DICT-03 (Tickets 01/02): the SAME client doubles as the
			// dictation word audio source — attach the per-word synthesizer
			// to the generator. Mimo produces a single bare-word
			// pronunciation per track (the frontend schedules the double
			// play off word_complete's audio_plays hint); on the first
			// per-word failure the generator falls back to the Volcengine
			// whole-batch path internally, so this wiring cannot break
			// session init when Mimo degrades.
			wordSynth, err := tts.NewMimoWordSynthesizer(mimoClient, logger)
			if err != nil {
				logger.Error("mimo word synthesizer construction failed",
					infra.F("component", "infra.main"),
					infra.ErrField(err),
				)
				return err
			}
			gen.SetWordSynthesizer(wordSynth)
			logger.Info("mimo dictation word source enabled (volcengine whole-batch fallback)",
				infra.F("component", "infra.main"),
			)

			voiceCmd = newVoiceService(
				mimoTranscriber(mimoClient),
				voice.NewHybridClassifier(voice.NewLLMClassifier(mimoChatter(mimoClient, cfg.Voice.ChatModel))),
				manager,
				logger,
				cfg.Voice,
			)
			photoCmd = newPhotoService(
				mimoReader(mimoClient, cfg.Photo.ChatModel),
				func(connID, transcript string) (answerJudgement, string) {
					return judgeAnswer(manager, connID, transcript)
				},
				logger,
				cfg.Photo,
			)
			logger.Info("voice command pipeline enabled",
				infra.F("component", "infra.main"),
				infra.F("chat_model", cfg.Voice.ChatModel),
			)
			logger.Info("photo OCR pipeline enabled",
				infra.F("component", "infra.main"),
				infra.F("chat_model", cfg.Photo.ChatModel),
				infra.F("confidence_threshold", cfg.Photo.ConfidenceThreshold),
			)
		} else {
			logger.Warn("MIMO_API_KEY not set — voice commands and photo OCR disabled; buttons and manual input remain",
				infra.F("component", "infra.main"),
			)
		}

		router, err := ws.NewRouter(ttsHandlers(manager, fsrsSvc, voiceCmd, photoCmd, logger), routerCfg, wsLogger)
		if err != nil {
			logger.Error("ws router construction failed",
				infra.F("component", "infra.main"),
				infra.ErrField(err),
			)
			return err
		}
		wsServer, err = ws.NewServer(hub, router, serverCfg, wsLogger)
		if err != nil {
			logger.Error("ws server construction failed",
				infra.F("component", "infra.main"),
				infra.ErrField(err),
			)
			return err
		}

		// Hub.Run blocks until ctx.Done then defers Close (CR-01-safe). Bound to
		// the lifetime ctx so it exits as soon as shutdown begins.
		go hub.Run(ctx)

		// Public mux carries /ws plus the student web interface static file
		// server (Phase 8 Plan 01). /health and /metrics stay on the separate
		// localhost healthSrv below (probe/session traffic separation is the
		// established design).
		publicMux := http.NewServeMux()
		publicMux.HandleFunc("/ws", wsServer.HandleWebSocket)

		// Phase 9 (WEB-01): the /api/v1 REST surface, mounted ONCE here.
		// All route logic (method patterns, validation, error envelope)
		// lives inside pkg/web/api — future Phase 9 routes register in
		// that package's internal mux, not in main.go.
		publicMux.Handle("/api/v1/", http.StripPrefix("/api/v1",
			api.NewHandler(pyBridge, cardRepo, cfg.Python, logger)))
		logger.Info("api v1 mounted",
			infra.F("component", "infra.main"),
			infra.F("route", "/api/v1/"),
		)

		// Phase 8 Plan 01: mount the student interface. When server.staticDir is
		// configured, an http.FileServer serves index.html, /styles/*, /js/* from
		// that directory at StaticRoute (default "/"). ServeMux matches the more
		// specific "/ws" pattern first, so the WebSocket upgrade endpoint does
		// not conflict with the static root. When staticDir is empty (disabled),
		// fall back to serving the legacy repo-root debug HTML files so existing
		// developer workflows keep working.
		if cfg.Server.StaticDir != "" {
			publicMux.Handle(cfg.Server.StaticRoute, infra.StaticFileServer(cfg.Server.StaticDir, logger))
			logger.Info("public server static files mounted",
				infra.F("component", "infra.main"),
				infra.F("route", cfg.Server.StaticRoute),
				infra.F("dir", cfg.Server.StaticDir),
			)
			// Debug interfaces remain reachable at their explicit paths; ServeMux
			// prefers these longer patterns over the "/" prefix so they do not
			// collide with the static FileServer.
			for _, dbg := range debugInterfaces {
				publicMux.HandleFunc("/"+dbg, serveRepoRootFile(dbg))
			}
		} else {
			publicMux.HandleFunc("/", serveDebugRoot)
		}
		publicSrv = &http.Server{
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
		healthSrv = &http.Server{
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

		return nil
	}()

	// If any post-Start construction failed, shut down server immediately.
	if shutdownErr != nil {
		logger.Error("post-Start construction failed, shutting down server",
			infra.F("component", "infra.main"),
			infra.ErrField(shutdownErr),
		)
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = server.Shutdown(shutdownCtx)
		return 1
	}

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
	// mimoClient lives until real process shutdown (see the lifetime note at
	// its construction site).
	if mimoCloser != nil {
		if err := mimoCloser(); err != nil {
			logger.Warn("mimo client close error",
				infra.F("component", "infra.main"),
				infra.ErrField(err),
			)
		}
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

// debugInterfaces lists the repo-root HTML debug harnesses that should remain
// reachable even when the student interface is mounted at "/". These are
// developer-facing pages (Phase 7.1 audio regression harnesses, interface
// debug pages); they are registered at their explicit paths so ServeMux
// prefers them over the static "/" prefix handler.
var debugInterfaces = []string{
	"test-interface.html",
	"test-interface-debug.html",
	"test-audio-sequence-bug.html",
	"test-audio-bug-frontend.html",
}

// serveRepoRootFile returns a handler that serves a single file from the
// process CWD, responding 404 if the file is absent. Used to keep debug
// harnesses reachable without exposing them through the static FileServer
// (which is rooted at the student interface directory).
func serveRepoRootFile(filename string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, err := os.Stat(filename); err != nil {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, filename)
	}
}

// serveDebugRoot is the fallback root handler used ONLY when static serving
// is disabled (server.staticDir empty). It preserves the pre-Phase-8 root
// behaviour: serve test-interface.html at "/" plus the other debug harnesses
// at their named paths, so existing developer workflows survive the
// introduction of the student interface config.
func serveDebugRoot(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/", "/test-interface.html":
		http.ServeFile(w, r, "test-interface.html")
	case "/test-interface-debug.html":
		http.ServeFile(w, r, "test-interface-debug.html")
	case "/test-audio-sequence-bug.html":
		http.ServeFile(w, r, "test-audio-sequence-bug.html")
	case "/test-audio-bug-frontend.html":
		http.ServeFile(w, r, "test-audio-bug-frontend.html")
	default:
		http.NotFound(w, r)
	}
}
