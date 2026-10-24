#!/usr/bin/env bash
# ci-local.sh —— 本机复刻 .github/workflows/ci.yml（本机没有 make）：
#   vet → golangci-lint v2.14.0（固定路径，勿重装）→ race test → build
# 与 CI 的一致性：GOFLAGS=-trimpath；vet/build 用 CGO_ENABLED=0，test 步 CGO_ENABLED=1。
# 用法：scripts/harness/ci-local.sh [包模式，默认 ./...]
set -uo pipefail
cd "$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
PKGS="${1:-./...}"

# go.mod 要求 1.26；发行版 go 常太旧，优先用 /usr/local 的安装
GO_BIN="${GO_BIN:-}"
if [ -z "$GO_BIN" ]; then
  for c in /usr/local/go/bin/go /usr/local/bin/go go; do
    command -v "$c" >/dev/null 2>&1 && GO_BIN="$(command -v "$c")" && break
  done
fi
[ -n "$GO_BIN" ] || { echo "FAIL: 找不到 go"; exit 1; }
GCL="${GCL:-/tmp/gcl-bin/golangci-lint}"

export GOFLAGS=-trimpath
fail=0
step() { printf '\n== %s ==\n' "$1"; }

step "go mod download"
"$GO_BIN" mod download || fail=1

step "go vet $PKGS"
CGO_ENABLED=0 "$GO_BIN" vet "$PKGS" || fail=1

step "golangci-lint v2.14.0 (固定路径 $GCL)"
if [ -x "$GCL" ]; then
  "$GCL" run --config=.golangci.yml "$PKGS" || fail=1
else
  echo "FAIL: $GCL 不存在。一次性安装（勿每次重装）："
  echo "  install -d /tmp/gcl-bin && curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/HEAD/install.sh | sh -s -- -b /tmp/gcl-bin v2.14.0"
  fail=1
fi

step "go test -race $PKGS （CI 的 make test 步）"
CGO_ENABLED=1 "$GO_BIN" test -race -count=1 "$PKGS" || fail=1

step "go build（CI 的 make build 步）"
mkdir -p bin
CGO_ENABLED=0 "$GO_BIN" build -ldflags '-s -w' -o bin/service ./cmd/service || fail=1

printf '\n== ci-local 总结: '
[ "$fail" -eq 0 ] && echo "PASS（与 CI 等价）" || echo "FAIL（上面有失败步骤）"
exit "$fail"
