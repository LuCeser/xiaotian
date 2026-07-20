# Phase 01 — Deferred Items (out-of-scope discoveries)

Out-of-scope issues encountered during phase execution. These are NOT caused
by the current plan's changes and are NOT fixed by it. They are logged for a
future plan / phase to address.

## Pre-existing pkg/ws build error

**Discovered during:** Plan 01-03 (logging + middleware) verification, while
running `go build ./...` to confirm no cross-package regressions.

**Symptom:**
```
# github.com/xiaotian/home-tutor-agent/pkg/ws
pkg\ws\server.go:180:7: DefaultHeartbeatInterval redeclared in this block
	pkg\ws\heartbeat.go:15:7: other declaration of DefaultHeartbeatInterval
```

**Source:** `pkg/ws/server.go` was already in a modified state at the start
of this worktree (initial `git status` showed `M pkg/ws/server.go`). Phase 2
(pkg/ws) was declared complete and merged prior to this plan; the prompt
explicitly instructed "do not touch it". The redeclaration is therefore a
pre-existing condition in the orchestrator-supplied worktree base, not a
regression introduced by Plan 03.

**Scope rationale:** Plan 03 only modifies `pkg/infra/` and `go.mod`/`go.sum`.
The pkg/infra package itself builds clean (`go build ./pkg/infra/` exits 0,
`go vet ./pkg/infra/` exits 0). The pkg/ws issue is a separate package owned
by a different wave.

**Suggested owner:** A future `/gsd-debug` or follow-up plan in Phase 02 (or
a Phase 02 cleanup plan). Until then, build `./pkg/infra/` directly rather
than `./...` to verify infra work.
