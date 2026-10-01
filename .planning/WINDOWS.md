---
schema_version: 1
open_count: 1
waived_count: 0
fixed_count: 0
total_count: 1
last_updated: 2026-10-01T13:02:26.260Z
---

# Broken Windows Ledger

> Cross-phase defect register. With `workflow.windows_enforce` enabled, `/gsd-ship` blocks while `open_count > 0`.
> Waive with `gsd-tools windows waive <id> "<reason>"` (reason required).
> Mark fixed with `gsd-tools windows fixed <id>`.

| id | phase | kind | file | line | description | status | reason | recorded_at | resolved_at |
|----|-------|------|------|------|-------------|--------|--------|-------------|-------------|
| 1 | 13 | unrun-verify | pkg/mimo/chat_tools_live_test.go |  | Live-key run of TestLiveChatToolsThinkingDisabled deferred: no MIMO_API_KEY in executor env (env-gated skip by design; run with key for A4 evidence) | open |  | 2026-10-01T13:02:26.260Z |  |

````json
[
  {
    "id": 1,
    "kind": "unrun-verify",
    "phase": "13",
    "file": "pkg/mimo/chat_tools_live_test.go",
    "line": null,
    "description": "Live-key run of TestLiveChatToolsThinkingDisabled deferred: no MIMO_API_KEY in executor env (env-gated skip by design; run with key for A4 evidence)",
    "status": "open",
    "reason": "",
    "recorded_at": "2026-10-01T13:02:26.260Z",
    "resolved_at": null,
    "milestone": "v2.0"
  }
]
````
