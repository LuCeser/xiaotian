#!/usr/bin/env bash
# related-tests.sh —— 改动文件 → 应跑的相关测试（迭代阶段用这个，不跑全量）。
# 用法：
#   scripts/harness/related-tests.sh <文件1> <文件2> ...
#   scripts/harness/related-tests.sh            # 无参数时取 git 里已改动/暂存的文件
# 输出：去重后的测试命令（直接可复制执行），一行一条。
set -euo pipefail
cd "$(git rev-parse --show-toplevel 2>/dev/null || pwd)"

if [ $# -eq 0 ]; then
  set -- $(git status --porcelain -uall 2>/dev/null | awk '{print $NF}' | grep -v '^\.harness/' || true)
  if [ $# -eq 0 ]; then
    echo "related-tests: 没有改动文件（也没传参数）" >&2
    exit 0
  fi
fi

declare -A seen=()
emit() {  # emit <命令>
  [ -n "${seen[$1]:-}" ] && return
  seen[$1]=1
  echo "$1"
}

for f in "$@"; do
  case "$f" in
    pkg/web/static/*|*.css|*.html)
      emit "go test ./pkg/infra/            # 静态资源/CSS token 由 infra 测试断言" ;;
    tests/test-*.js)
      emit "node $f" ;;
    tests/*.spec.js|tests/*/*.spec.js)
      case "$f" in
        *rt*)      emit "npx playwright test $f -c tests/playwright.rt.config.js" ;;
        *parent*)  emit "npx playwright test $f -c tests/playwright.parent.config.js" ;;
        *family*)  emit "npx playwright test $f -c tests/playwright.family.config.js" ;;
        *)         emit "npx playwright test $f -c tests/playwright.config.js" ;;
      esac ;;
    tests/playwright.*.config.js)
      emit "# 配置变更影响整套 e2e：迭代期先跑 -g '<相关用例>'，全量留给收尾" ;;
    cmd/*.go|internal/*.go|internal/*/*.go|pkg/*/*.go|pkg/*/*/*.go)
      dir="$(dirname "$f")"
      emit "go test ./${dir}/" ;;
    .github/workflows/*|Makefile|.golangci.yml|go.mod|go.sum)
      emit "# CI/构建配置变更：本地等价检查请跑 scripts/harness/ci-local.sh（收尾阶段）" ;;
    scripts/acceptance/*|tests/test-acceptance-runner.js)
      emit "node --test tests/test-acceptance-runner.js" ;;
    docs/*|*.md)
      emit "# 文档改动：无自动测试" ;;
    *.mjs|*.js)
      emit "# 脚本改动：无自动映射，请说明如何验证" ;;
  esac
done
