---
schema_version: 1
open_count: 2
waived_count: 0
fixed_count: 0
total_count: 2
last_updated: 2026-11-08T08:03:07.490Z
---

# Broken Windows Ledger

> Cross-phase defect register. With `workflow.windows_enforce` enabled, `/gsd-ship` blocks while `open_count > 0`.
> Waive with `gsd-tools windows waive <id> "<reason>"` (reason required).
> Mark fixed with `gsd-tools windows fixed <id>`.

| id | phase | kind | file | line | description | status | reason | recorded_at | resolved_at |
|----|-------|------|------|------|-------------|--------|--------|-------------|-------------|
| 1 | 13 | unrun-verify | pkg/mimo/chat_tools_live_test.go |  | Live-key run of TestLiveChatToolsThinkingDisabled deferred: no MIMO_API_KEY in executor env (env-gated skip by design; run with key for A4 evidence) | open |  | 2026-10-01T13:02:26.260Z |  |
| 2 | 261108-lm8 | unmet-truth | internal/e2ecorpus/prepare_test.go |  | ci-local.sh 全量门 Windows 上无法总结 PASS：prepare_test.go 无平台标签走 lock_other.go 哨兵全 FAIL＋prepare.go:57 SA4023（!unix 恒真比较）；先存且与本任务改动无关，修法需授权（补 //go:build unix 或 skip） | open |  | 2026-11-08T08:03:07.490Z |  |

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
  },
  {
    "id": 2,
    "kind": "unmet-truth",
    "phase": "261108-lm8",
    "file": "internal/e2ecorpus/prepare_test.go",
    "line": null,
    "description": "ci-local.sh 全量门 Windows 上无法总结 PASS：prepare_test.go 无平台标签走 lock_other.go 哨兵全 FAIL＋prepare.go:57 SA4023（!unix 恒真比较）；先存且与本任务改动无关，修法需授权（补 //go:build unix 或 skip）",
    "status": "open",
    "reason": "",
    "recorded_at": "2026-11-08T08:03:07.490Z",
    "resolved_at": null,
    "milestone": "v2.0"
  }
]
````
