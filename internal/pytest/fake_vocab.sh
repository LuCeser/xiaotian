#!/bin/sh
# fake_vocab.sh -- POSIX shell fake for vocab_manager.py.
#
# Purpose (D-48 manual fake, shell variant): lets the bridge's vocab_manager
# parsing / timeout / error-classification logic be exercised end-to-end on
# Windows dev hosts that have no Python venv. The fake is invoked INSTEAD of
# the real vocab_manager.py by pointing Bridge.run at this script via the
# interpreter + script-path config (or by replacing cfg.VocabScript with the
# path to this file). It emits the same stdout contract vocab_manager.py
# uses (see RESEARCH Pattern 2 / Pitfall 2):
#   - due   -> JSON array of due cards on stdout, exit 0
#   - review -> optional "Warning:" prefix lines + trailing {"updated":[...]}
#               JSON on stdout, exit 0 (Pitfall 2 mixed-stream contract)
#   - unknown subcommand or error -> "Error: ..." on stdout, exit 1
#
# The fake deliberately writes JSON with leading/trailing whitespace and
# (for review) Warning prefix lines so the bridge's extractTrailingJSON
# parser is genuinely exercised. It also accepts a --lock-test flag for the
# pregenerate path (see fake_prege.sh for the canonical flock simulation).
#
# Portability: POSIX sh only (no bash-isms). Tested on git-bash on Windows
# and dash/bash on Linux. The bridge's //go:build linux integration test
# invokes this via exec.CommandContext; on Windows it is invoked manually
# for smoke checks (not as part of `go test -short`).
#
# This file is INTENTIONALLY test infrastructure, not production code -- it
# has no effect on the running service.

set -u

subcommand="${1:-}"
[ $# -gt 0 ] && shift

case "$subcommand" in
    due)
        # Emit a JSON array mirroring vocab_manager.py:255-265 contract.
        # indent=2 mirrors the real script's json.dumps(..., indent=2).
        cat <<'EOF'
[
  {
    "id": 1,
    "word": "hello",
    "meaning": "greeting",
    "example": "Hello, world!",
    "unit": "unit-1",
    "state": "Review",
    "due": "2026-07-25",
    "stability": 1.5,
    "difficulty": 0.2
  }
]
EOF
        exit 0
        ;;
    review)
        # Mirror the Pitfall 2 mixed-stream contract: Warning prefix lines
        # BEFORE the trailing {"updated": [...]} JSON, all on stdout.
        echo "Warning: unknown rating 'maybe' for 'mystery', skipping"
        cat <<'EOF'
{
  "updated": [
    {
      "word": "hello",
      "rating": "good",
      "next_due": "2026-07-26",
      "state": "Review",
      "stability": 2.0
    }
  ]
}
EOF
        exit 0
        ;;
    "")
        echo "Error: missing subcommand (due|review)" >&2
        exit 1
        ;;
    *)
        echo "Error: unknown subcommand '$subcommand'" >&2
        exit 1
        ;;
esac
