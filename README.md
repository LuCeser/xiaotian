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

## Running the Service

The service loads `config.yaml` from the **current working directory**. You
MUST run it from the repository root so the config file and the relative
`staticDir` path resolve correctly.

### First-time setup

```sh
make config-init   # copies config.example.yaml to config.yaml (gitignored)
```

Edit `config.yaml` to set `python.interpreter` to your venv path.

### Start the service

```sh
make run           # Linux/macOS — runs from repo root
./run.ps1          # Windows — sets CWD to repo root automatically
```

### Verify the student interface loaded

Check the logs for `"static interface directory verified"`. Then open
`http://localhost:8080/` in a browser — the student dictation interface
should render with styling.

> **Troubleshooting:** If the page loads without CSS/JS (404 errors), the
> service is not finding `config.yaml` or `server.staticDir` is empty. See
> [docs/DEPLOYMENT.md](docs/DEPLOYMENT.md) for detailed diagnosis.

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
