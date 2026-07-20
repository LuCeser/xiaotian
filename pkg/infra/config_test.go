// Package infra's tests exercise the typed Config layer (D-28 table-driven,
// D-44 centralized validation). These tests intentionally use only the
// standard library testing package — testify was unavailable at plan-execution
// time (network blocked) and the plan explicitly permits stdlib testing as a
// fallback. Phase 2 (pkg/ws) established plain testing as the repo pattern.
package infra

import (
	"testing"
	"time"
)

// TestDefaultConfig asserts the phase-1 baseline configuration (D-52 lowest
// precedence layer). Every value here is what LoadConfig MUST return when no
// file or environment override is supplied.
func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	t.Run("memory defaults per RES-01 and D-70", func(t *testing.T) {
		if cfg.Memory.LimitBytes != MemoryBytes(150*1024*1024) {
			t.Errorf("Memory.LimitBytes = %d, want %d (150MB on a 1C/1G VPS, RES-01)",
				cfg.Memory.LimitBytes, MemoryBytes(150*1024*1024))
		}
		if cfg.Memory.MonitorInterval != 5*time.Second {
			t.Errorf("Memory.MonitorInterval = %v, want 5s", cfg.Memory.MonitorInterval)
		}
		wantThresholds := []Percent{80, 90, 95}
		if len(cfg.Memory.Thresholds) != len(wantThresholds) {
			t.Fatalf("Memory.Thresholds length = %d, want %d (%v)",
				len(cfg.Memory.Thresholds), len(wantThresholds), wantThresholds)
		}
		for i, want := range wantThresholds {
			if cfg.Memory.Thresholds[i] != want {
				t.Errorf("Memory.Thresholds[%d] = %d, want %d", i, cfg.Memory.Thresholds[i], want)
			}
		}
		if cfg.Memory.LoadShedAtPercent != Percent(95) {
			t.Errorf("Memory.LoadShedAtPercent = %d, want 95 (D-18)", cfg.Memory.LoadShedAtPercent)
		}
	})

	t.Run("session defaults per RES-03 and D-57", func(t *testing.T) {
		if cfg.Session.MaxConcurrent != 100 {
			t.Errorf("Session.MaxConcurrent = %d, want 100 (RES-03, D-56)", cfg.Session.MaxConcurrent)
		}
		if cfg.Session.IdleTimeout != 5*time.Minute {
			t.Errorf("Session.IdleTimeout = %v, want 5m", cfg.Session.IdleTimeout)
		}
		if cfg.Session.CleanupInterval != 30*time.Second {
			t.Errorf("Session.CleanupInterval = %v, want 30s (D-57)", cfg.Session.CleanupInterval)
		}
	})

	t.Run("buffer defaults per D-71 and D-73", func(t *testing.T) {
		if cfg.Buffer.AudioFrameBytes != 640 {
			t.Errorf("Buffer.AudioFrameBytes = %d, want 640 (20ms@16kHz PCM, D-71)",
				cfg.Buffer.AudioFrameBytes)
		}
		if cfg.Buffer.PoolSize != 1000 {
			t.Errorf("Buffer.PoolSize = %d, want 1000", cfg.Buffer.PoolSize)
		}
		if cfg.Buffer.Strategy != StrategyDropOldest {
			t.Errorf("Buffer.Strategy = %q, want %q (D-73)", cfg.Buffer.Strategy, StrategyDropOldest)
		}
	})

	t.Run("server defaults per D-22", func(t *testing.T) {
		if cfg.Server.Addr != "0.0.0.0:8080" {
			t.Errorf("Server.Addr = %q, want %q", cfg.Server.Addr, "0.0.0.0:8080")
		}
		if cfg.Server.ReadTimeout != 10*time.Second {
			t.Errorf("Server.ReadTimeout = %v, want 10s (D-22)", cfg.Server.ReadTimeout)
		}
		if cfg.Server.WriteTimeout != 10*time.Second {
			t.Errorf("Server.WriteTimeout = %v, want 10s", cfg.Server.WriteTimeout)
		}
		if cfg.Server.HealthAddr != "127.0.0.1:8081" {
			t.Errorf("Server.HealthAddr = %q, want %q", cfg.Server.HealthAddr, "127.0.0.1:8081")
		}
	})

	t.Run("log defaults", func(t *testing.T) {
		if cfg.Log.Level != "info" {
			t.Errorf("Log.Level = %q, want %q", cfg.Log.Level, "info")
		}
		if cfg.Log.Format != "json" {
			t.Errorf("Log.Format = %q, want %q", cfg.Log.Format, "json")
		}
		if cfg.Log.Output != "stderr" {
			t.Errorf("Log.Output = %q, want %q", cfg.Log.Output, "stderr")
		}
	})

	t.Run("worker defaults — pool scales with NumCPU", func(t *testing.T) {
		if cfg.Worker.PoolSize <= 0 {
			t.Errorf("Worker.PoolSize = %d, want > 0 (2*runtime.NumCPU())", cfg.Worker.PoolSize)
		}
		if cfg.Worker.QueueSize != 256 {
			t.Errorf("Worker.QueueSize = %d, want 256", cfg.Worker.QueueSize)
		}
	})

	t.Run("connection defaults per D-22 and D-66", func(t *testing.T) {
		if cfg.Connection.ThrottleRatePerSec != 100 {
			t.Errorf("Connection.ThrottleRatePerSec = %d, want 100 (D-66)",
				cfg.Connection.ThrottleRatePerSec)
		}
		if cfg.Connection.ThrottleBurst != 10 {
			t.Errorf("Connection.ThrottleBurst = %d, want 10", cfg.Connection.ThrottleBurst)
		}
		if cfg.Connection.DialTimeout != 5*time.Second {
			t.Errorf("Connection.DialTimeout = %v, want 5s (D-22)", cfg.Connection.DialTimeout)
		}
	})

	t.Run("shutdown defaults per D-21", func(t *testing.T) {
		if cfg.Shutdown.Timeout != 30*time.Second {
			t.Errorf("Shutdown.Timeout = %v, want 30s (graceful drain, D-21)",
				cfg.Shutdown.Timeout)
		}
	})
}
