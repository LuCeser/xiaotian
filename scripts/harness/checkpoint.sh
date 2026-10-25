#!/usr/bin/env bash
# checkpoint.sh —— WIP 检查点：提交当前改动 + 写 handoff 文件（供下一会话接手/防 429 丢工作）。
# 用法：scripts/harness/checkpoint.sh ["说明文字"] [--phase finalize|iterate]
#   --phase 同时更新 .harness/phase 标记（hook 的 phase 判定用）
set -euo pipefail
cd "$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
NOTE="${1:-}"
PHASE=""
for a in "$@"; do
  case "$a" in
    --phase) : ;;
    finalize|iterate) PHASE="$a" ;;
  esac
done
TS="$(date '+%F %T%z')"
BRANCH="$(git branch --show-current 2>/dev/null || echo '?')"

if [ -n "$PHASE" ]; then
  mkdir -p .harness && echo "$PHASE" > .harness/phase
  echo "phase → $PHASE (.harness/phase)"
fi

if [ -z "$(git status --porcelain 2>/dev/null | grep -v ' \?\.harness/' || true)" ]; then
  echo "checkpoint: 工作树干净，无需 WIP 提交"
else
  git add -A -- ':!.harness'
  if git commit -q -m "WIP(checkpoint): ${NOTE:-进度快照} @ ${TS}"; then
    echo "checkpoint: 已提交 WIP（$(git rev-parse --short HEAD)）"
  fi
fi

mkdir -p .harness
cat > .harness/HANDOFF.md <<EOF
# Handoff @ ${TS}

- 分支：${BRANCH}
- 最近提交：
$(git log --oneline -5 2>/dev/null | sed 's/^/  /' || true)
- 工作树：$(git status --porcelain | wc -l) 个改动路径
- 说明：${NOTE:-（未填写）}

## 下一会话接手要点

1. \`git log -3\` + 本文件了解进度；不要推倒重来。
2. 迭代阶段只跑相关测试：\`scripts/harness/related-tests.sh <改动文件…>\`。
3. 收尾：\`echo finalize > .harness/phase\` 后跑一次 \`scripts/harness/ci-local.sh\`。
4. 额度紧张（zhipu-quota ≥80%）先跑本脚本再做别的。
EOF
echo "checkpoint: handoff 已写入 .harness/HANDOFF.md"
