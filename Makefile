BINARY ?= bin/service
PKG ?= ./...
# Source-of-truth location of the upstream Python scripts. py-sync copies
# these into third_party/python/ so the Go repo carries a versioned snapshot
# (D-03 vendor). Override with `make py-sync UPSTREAM_SCRIPTS=/other/path`.
UPSTREAM_SCRIPTS ?= ../xiaotian-skills/shared/dictation/scripts
# Python venv location for py-bootstrap (D-04). Lives inside third_party/python
# so it travels with the vendored code; .gitignore MUST exclude it.
PY_VENV ?= third_party/python/.venv

.PHONY: build test test-short lint fmt vet cover clean tidy py-bootstrap py-sync

build:
	mkdir -p bin
	go build -ldflags '-s -w' -o $(BINARY) ./cmd/service

test:
	go test -race -v $(PKG)

test-short:
	go test -v -short $(PKG)

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
# rewritten with pinned versions (pip freeze), so the lock is captured in
# git for reproducible deploys. Run on the VPS, not on every dev machine.
py-bootstrap:
	@echo "==> Creating venv at $(PY_VENV) (D-04)"
	python3 -m venv $(PY_VENV)
	@echo "==> Installing dependencies from third_party/python/requirements.txt"
	$(PY_VENV)/bin/pip install --upgrade pip
	$(PY_VENV)/bin/pip install -r third_party/python/requirements.txt
	@echo "==> Freezing pinned versions back to requirements.txt (lock)"
	$(PY_VENV)/bin/pip freeze > third_party/python/requirements.txt
	@echo "==> py-bootstrap complete. Configure config.yaml:"
	@echo "    python.interpreter: $$(cd $(PY_VENV) && pwd)/bin/python"

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
