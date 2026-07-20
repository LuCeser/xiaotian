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
	"syscall"
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
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
