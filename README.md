# Home Tutor Agent

A web-based real-time dictation tutor agent that provides natural, interactive learning experiences for elementary school students. The system uses conversational AI to deliver personalized vocabulary practice through voice dictation sessions, with parent feedback integration that drives FSRS (Free Spaced Repetition Scheduler) algorithm updates.

## Prerequisites

- Go 1.26+
- Make
- Git

## Build

```sh
make build
```

Produces a static binary at `bin/service`.

## Test

```sh
make test
```

Runs the full test suite with the race detector enabled. For a quick run:

```sh
make test-short
```

## Project Layout

- `cmd/service/` — Application entry point (main.go added in a later plan)
- `pkg/infra/` — Shared infrastructure package (logging, config, resource management)
- `internal/` — Private helpers reserved for future phases

## Dependencies

This project uses vetted Go ecosystem libraries (Viper, Zap, pkg/errors, testify, prometheus/client_golang) added in later plans after research-backed approval. No third-party dependencies are introduced in Plan 01.

Imports flow strictly `cmd -> pkg/infra -> external libs`; no package imports from `cmd`; `pkg/infra` has no internal cycles.

## Versioning

Semantic versioning (D-35). Phase 1 ships as v0.1.0 (pre-1.0 since downstream WebSocket/audio phases will evolve APIs).

## Documentation

Run `go doc github.com/xiaotian/home-tutor-agent/pkg/infra` for API docs. README files complement godoc per the combined-docs approach (D-34).

## Build Tags

`//go:build` tags reserved for future conditional compilation (D-41). None needed in Phase 1.

## Phase Status

Phase 1: Resource-Constrained Infrastructure — in progress
