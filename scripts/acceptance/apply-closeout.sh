#!/usr/bin/env bash
# Apply a prepared closeout to a GitHub issue (body checkboxes + comment +
# optional close). Requires a token with issues:write — the Cloud Agent
# integration token is read-only and will fail here; run this from a
# workstation where `gh auth status` can edit LuCeser/xiaotian issues.
#
# Usage:
#   bash scripts/acceptance/apply-closeout.sh 6
#   bash scripts/acceptance/apply-closeout.sh 7   # body+comment only (stays open)
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
ISSUE="${1:-}"
REPO="${ACCEPTANCE_REPO:-LuCeser/xiaotian}"

if [[ -z "$ISSUE" ]]; then
  echo "Usage: $0 <6|7|8>" >&2
  exit 2
fi

PAD="$(printf '%02d' "$ISSUE")"
BODY="$ROOT/scripts/acceptance/closeouts/${PAD}-body.md"
COMMENT="$ROOT/scripts/acceptance/closeouts/${PAD}-comment.md"
MANIFEST="$ROOT/scripts/acceptance/manifest.json"

[[ -f "$BODY" ]] || { echo "missing $BODY" >&2; exit 1; }
[[ -f "$COMMENT" ]] || { echo "missing $COMMENT" >&2; exit 1; }

echo "==> Updating #$ISSUE body from $BODY"
gh issue edit "$ISSUE" --repo "$REPO" --body-file "$BODY"

echo "==> Commenting closeout on #$ISSUE"
gh issue comment "$ISSUE" --repo "$REPO" --body-file "$COMMENT"

CLOSE="$(python3 - <<PY
import json
m=json.load(open("$MANIFEST"))
print("yes" if m.get("$ISSUE",{}).get("close") else "no")
PY
)"

if [[ "$CLOSE" == "yes" ]]; then
  echo "==> Closing #$ISSUE"
  gh issue close "$ISSUE" --repo "$REPO"
  # Prefer ready-for-human removed / leave labels as-is; strip ready-for-agent if present.
  gh issue edit "$ISSUE" --repo "$REPO" --remove-label ready-for-agent 2>/dev/null || true
else
  echo "==> Leaving #$ISSUE OPEN (human remaining)"
  gh issue edit "$ISSUE" --repo "$REPO" --remove-label ready-for-agent 2>/dev/null || true
  gh issue edit "$ISSUE" --repo "$REPO" --add-label ready-for-human 2>/dev/null || true
fi

echo "Done #$ISSUE"
