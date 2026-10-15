BINARY ?= bin/service
PKG ?= ./...
# Source-of-truth location of the upstream Python scripts. py-sync copies
# these into third_party/python/ so the Go repo carries a versioned snapshot
# (D-03 vendor). Override with `make py-sync UPSTREAM_SCRIPTS=/other/path`.
UPSTREAM_SCRIPTS ?= ../xiaotian-skills/shared/dictation/scripts
# Python venv location for py-bootstrap (D-04). Lives inside third_party/python
# so it travels with the vendored code; .gitignore MUST exclude it.
PY_VENV ?= third_party/python/.venv
# pip/uv index. Defaults to the Tsinghua mirror for the China-based dev/VPS
# host; override with `make py-bootstrap PY_INDEX=https://pypi.org/simple` or
# the UV_INDEX_URL env var. See memory: china-mirror-installs.
PY_INDEX ?= https://pypi.tuna.tsinghua.edu.cn/simple

.PHONY: build run test test-short lint fmt vet cover clean tidy py-bootstrap py-sync py-e2e e2e

build:
	mkdir -p bin
	CGO_ENABLED=0 go build -ldflags '-s -w' -o $(BINARY) ./cmd/service

# run starts the service from the repo root so the relative config.yaml
# path and staticDir (./pkg/web/static) resolve correctly (Phase 8, G-08-2).
# go run inherits the CWD of the parent make process (= repo root), keeping
# relative paths valid. If config.yaml is missing the service silently falls
# back to DefaultConfig (static serving disabled) — run `make config-init`
# first to copy the example, or set the values via HTA_* env vars.
run:
	go run ./cmd/service -config config.yaml

# config-init copies config.example.yaml to config.yaml if it does not exist.
# config.yaml is gitignored (machine-specific paths); this gives a working
# starting point with staticDir pre-enabled.
config-init:
	@test -f config.yaml || { \
		cp config.example.yaml config.yaml; \
		echo "Created config.yaml from config.example.yaml — edit python.interpreter for your venv"; }

# -race temporarily OFF: the first real CI run of -race (issue #26 for the
# full story — a cgo conflict had killed it instantly for months) surfaced
# 26 latent data races. Restored by the race-fix PR tracked in #26; until
# then `make cover` (manual, cgo-enabled) still runs -race locally.
test:
	go test -v $(PKG)

test-short:
	go test -v -short $(PKG)

# e2e runs the Playwright browser-E2E suite (tests/e2e) against the REAL
# service: the Playwright webServer boots run.ps1 (loads .env, frees :8080,
# go run ./cmd/service), then Chromium drives the student UI at
# http://localhost:8080. One-time setup: npx playwright install chromium.
e2e:
	npx playwright test -c tests/playwright.config.js

lint:
	golangci-lint run ./...

fmt:
	gofmt -s -w .
	goimports -w -local github.com/xiaotian/home-tutor-agent .

vet:
	go vet $(PKG)

# Coverage tracked per D-30; enforced-check threshold intentionally omitted for Phase 1.
cover:
	go test -race -coverprofile=coverage.out $(PKG)
	go tool cover -html=coverage.out -o coverage.html

clean:
	rm -rf bin/ coverage.out coverage.html

tidy:
	go mod tidy

# py-bootstrap creates the venv that the typed Config's python.interpreter
# field points at (D-04). Idempotent: re-running re-installs and re-freezes.
# After this target completes, third_party/python/requirements.txt is
# rewritten with pinned versions (uv pip freeze), so the lock is captured in
# git for reproducible deploys. Run on the VPS, not on every dev machine.
# Uses uv (repo-wide Python venv tool) + the configured PY_INDEX mirror.
py-bootstrap:
	@echo "==> Creating venv at $(PY_VENV) via uv (D-04)"
	uv venv --python 3.12 $(PY_VENV)
	@echo "==> Installing dependencies from third_party/python/requirements.txt (index $(PY_INDEX))"
	UV_INDEX_URL=$(PY_INDEX) uv pip install --python $(PY_VENV) -r third_party/python/requirements.txt
	@echo "==> Freezing pinned versions back to requirements.txt (lock)"
	uv pip freeze --python $(PY_VENV) > third_party/python/requirements.txt
	@echo "==> py-bootstrap complete. Configure config.yaml:"
	@echo "    python.interpreter: $$(cd $(PY_VENV) && pwd)/bin/python"

# py-e2e runs the real-Python end-to-end tests in pkg/pybridge (build tag e2e).
# The venv is bootstrapped by the test's TestMain (uv venv + uv pip install into
# a throwaway temp dir, never touching the deploy venv or requirements.txt);
# this target only injects the mirror index and invokes go test. Override the
# interpreter with HTA_PYTHON_INTERPRETER to reuse a pre-built venv.
py-e2e:
	@echo "==> Running pybridge real-Python E2E (tags=e2e)"
	UV_INDEX_URL=$(PY_INDEX) go test -tags=e2e -count=1 -v ./pkg/pybridge/

# py-sync refreshes the vendored Python scripts from the upstream
# xiaotian-skills checkout (D-03 vendor, T-03-03 tampering mitigation).
# After syncing, review the diff and run the Phase 03 plan's
# checkpoint:human-verify before relying on the new versions.
py-sync:
	@echo "==> Syncing vendored scripts from $(UPSTREAM_SCRIPTS)"
	@test -f $(UPSTREAM_SCRIPTS)/vocab_manager.py || { \
		echo "ERROR: $(UPSTREAM_SCRIPTS)/vocab_manager.py not found"; \
		echo "Override with: make py-sync UPSTREAM_SCRIPTS=/path/to/xiaotian-skills/shared/dictation/scripts"; \
		exit 1; }
	cp $(UPSTREAM_SCRIPTS)/vocab_manager.py third_party/python/vocab_manager.py
	cp $(UPSTREAM_SCRIPTS)/pregenerate_dictation.py third_party/python/pregenerate_dictation.py
	@echo "==> Synced. Review diff and re-run checkpoint:human-verify (Plan 03-01 Task 3)."
