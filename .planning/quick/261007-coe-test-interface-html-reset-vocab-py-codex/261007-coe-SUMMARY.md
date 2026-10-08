---
phase: 261007-coe
plan: 01
status: complete
subsystem: repo-hygiene
tags: [cleanup, archive, routing, python-scripts, gitkeep, readme]
requires: []
provides:
  - "scripts/archive/test-interface.html —— URL /test-interface.html 契约不变（staticDir 开/关两条供给路径均有 httptest 断言 200）"
  - "scripts/reset-vocab.py —— DB_PATH 以 __file__ 锚定仓库根 .hta-data/vocab.db，任意 CWD 运行同一库"
  - "cmd/service/debug_serving_test.go —— 调试页路由回归测试（4 用例：200/404/兜底根/死页 404）"
  - "README Project Layout 与 pkg/ 15 个子目录实况一致；4 个残留 .gitkeep 退库；main.go fmt 占位清除"
  - "工具产物清零：.codex-network-log/、test-results/、server-8443.out.log 已删（本轮无文件锁）"
affects: []
key-files:
  created:
    - scripts/archive/test-interface.html（自仓库根 git mv，59KB 内容不变）
    - scripts/reset-vocab.py（自仓库根 git mv + DB_PATH 锚定）
    - cmd/service/debug_serving_test.go
  modified:
    - cmd/service/main.go（调试页路由改指归档路径、debugInterfaces 与死页路由删除、fmt 占位删除）
    - README.md（Project Layout 补 pkg/ws、pkg/crc16、pkg/rt、pkg/grading、pkg/lesson、pkg/ocr；internal/ 描述改实况）
  deleted:
    - cmd/service/.gitkeep、internal/.gitkeep、pkg/infra/.gitkeep、pkg/web/static/js/.gitkeep
    - .codex-network-log/、test-results/、server-8443.out.log（均未跟踪/被忽略，不产生提交）
decisions:
  - "serveDebugRoot 兜底与 staticDir 注册都改读 scripts/archive/test-interface.html，URL 契约 /test-interface.html 不变——run.ps1/README/DEPLOYMENT 的 URL 级引用零修改"
  - "reset-vocab.py DB_PATH 锚定写法与 scripts/check_db.py 完全同范式（pathlib.Path(__file__).resolve().parent.parent）；验证仅 importlib 加载断言，绝不执行脚本本体（T-coe-02）"
  - "commit 拆分按计划意图修正：首轮提交误含 reset-vocab.py 纯重命名，soft reset 本方未推送提交后重拆为 79471e6（html+路由+测试）/ fd278f9（py+DB_PATH）"
metrics:
  duration: ~8m（01:19–01:27 UTC）
  completed: 2026-10-07
  tasks: 3
  commits: 3
actuals:
  tokens: 2400（diff 9512 chars ÷ 4，3 提交 9 文件 +100/-39）
  tasks: 3
  commits: 3
commits:
  - "79471e6 chore(261007-coe): test-interface.html 归位 scripts/archive——/test-interface.html 路由改指归档路径并移除已归档死页路由"
  - "fd278f9 chore(261007-coe): reset-vocab.py 归位 scripts/，DB 路径按脚本位置锚定仓库根（对齐 check_*.py 范式）"
  - "60c129a chore(261007-coe): 卫生 todo 收尾——删 4 个残留 .gitkeep 与 main.go fmt 占位，README 项目布局补全"
plan_head_before: 7ebb45e
plan_head_after: 60c129a
---

# Quick Task 261007-coe Summary

根目录梳理第二轮收官：3 个原子提交把最后两个根级跟踪文件归位（test-interface.html → scripts/archive/，URL 契约 /test-interface.html 经 httptest 证明不变；reset-vocab.py → scripts/，DB 路径按脚本位置锚定），工具产物三个清零，2026-07-25 卫生 todo 的 README 漂移与 .gitkeep 两残项关闭。台账证据件原位未动未入库。

## What Was Done

### Task 1 — test-interface.html 与 reset-vocab.py 归位
- 基线：`go build ./...` exit 0；`go test ./... -short` exit 0（17 包全绿）；`git status --porcelain` 空、`git diff --numstat` 空（无 CRLF 幻影）。
- 提交 1 `79471e6`：`git mv test-interface.html scripts/archive/`（100% 相似度，59KB 不变）+ main.go 四处改动（staticDir 分支单条注册 `serveRepoRootFile("scripts/archive/test-interface.html")`、`debugInterfaces` 变量整删、`serveDebugRoot` switch 收敛为 `"/", "/test-interface.html"` 且死页 case 删除、`serveRepoRootFile` 注释改 "repository-relative"）+ 新测试 `debug_serving_test.go`（t.Chdir 仓库根，4 用例全绿）。
- 提交 2 `fd278f9`：`git mv reset-vocab.py scripts/` + `import pathlib` + DB_PATH 锚定（4 行改动，96% 相似度）。验证仅用 `importlib.util.spec_from_file_location` 加载断言 `DB_PATH` 解析为仓库根 `.hta-data/vocab.db` 绝对路径且存在（DB-PATH-OK），**脚本本体从未执行**（`__main__` 守卫拦截）。
- 三个提交的 staged 内容均跑密钥模式扫描（sk- 长串 / api_key 赋值 / Bearer 长串）零命中。

### Task 2 — 工具产物清理（无提交）
- `.codex-network-log/`（仅 codex-tui.log）、`test-results/`、`server-8443.out.log`（0 字节）全部删除成功——本轮 8443 服务未运行，round-1 出现过的文件锁没有复现，无需记录跳过分支。
- 台账证据件 `rt-timing-20261006-202012.jsonl` 与 `server-8443.err.log` 原位未动、`git ls-files` 不含两者（13-UAT L102 证据索引不断链）。
- 终态 `git status --porcelain` 空。

### Task 3 — 卫生 todo 收尾 + 终验
- README Project Layout：`internal/` 描述改为私有测试辅助（pytest shell 假件、wstest WebSocket 测试件）；补 3 行覆盖 6 个缺失包——`pkg/ws/`、`pkg/crc16/`（WebSocket 库与流式音频帧校验）、`pkg/rt/`（v2.0 实时家教会话状态机）、`pkg/grading/`、`pkg/lesson/`、`pkg/ocr/`（批改、课堂记录、拍照 OCR）。pkg/ 15 个子目录逐一核对无缺席。其余章节未碰。
- 4 个 .gitkeep（cmd/service、internal、pkg/infra、pkg/web/static/js）删除前守卫通过（另有跟踪文件 21/3/20/15 个），`git rm` 退库。
- main.go fmt 占位（ensure-fmt 两行注释 + `var _ = fmt.Sprintf`）删除，import 经真实调用点（rt-%d 会话号）保留——build + vet 双证。
- 提交 3 `60c129a`。

## Verification（终验对照基线）

| 项 | 结果 |
|---|---|
| git log | 3 个 261007-coe 前缀 chore 提交（79471e6 / fd278f9 / 60c129a）全部落库 |
| git status --porcelain | **0 条目**（PLAN.md 已由流程入库，工作树全净） |
| go build + test vs 基线 | build exit 0 / test -short exit 0，与 Task 1 基线完全一致；新测试包 cmd/service 2.6s 全绿 |
| URL 契约 | `go test ./cmd/service -run 'TestServeRepoRootFile\|TestServeDebugRoot' -count=1` 4 用例全 PASS |
| DB-PATH | importlib 加载断言 `scripts/reset-vocab.py` 的 DB_PATH 为仓库根 `.hta-data/vocab.db` 绝对路径且存在 |
| 证据链 | 两证据件在仓库根原位、未跟踪；根级 4 个目标（test-interface.html / reset-vocab.py / .codex-network-log / test-results）+ server-8443.out.log 全部不存在 |
| 密钥扫描 | 三次 staged 内容扫描零命中 |

## reset-vocab.py 新调用形态（13-13 期间复用）

脚本已从仓库根迁至 `scripts/`，且 DB 路径按脚本位置锚定——**从任意 CWD 运行都指向同一 `.hta-data/vocab.db`**。今后请统一用：

```
py -3 scripts/reset-vocab.py     # 从仓库根调用（Windows py 启动器）
./scripts/reset-vocab.py         # bash 直跑形态
```

（根目录 `./reset-vocab.py` 旧形态已失效。）脚本会重置全部 FSRS 卡片并删除当天课堂行——只在本机调试重置学习状态时使用。

## Deviations from Plan

### 1. settings.local.json 权限规则未更新（被权限分类器拦截）
- **发现于：** Task 1 第 6 步
- **问题：** 计划要求把 `.claude/settings.local.json` L63 的 `"Bash(./reset-vocab.py *)"` 替换为 `"Bash(py -3 scripts/reset-vocab.py *)"` 与 `"Bash(./scripts/reset-vocab.py *)"` 两条。对该文件的 Edit 被会话权限分类器拒绝（该文件未跟踪、不入库，属本地权限配置），按规则不得绕过。
- **影响：** 仅本地便利性——旧规则 `./reset-vocab.py *` 对新调用形态永不匹配（无害的死规则）；下次实际调用 `py -3 scripts/reset-vocab.py` 时会照常弹出权限询问，批准一次即可。
- **需人工处理（可选）：** 手动把 L63 一行换成上述两行。
- **其余执行完全按计划。**

### 2. 提交 1 首轮误含 reset-vocab.py 纯重命名（已自行修正）
- **发现于：** Task 1 提交阶段
- **问题：** 计划 action 第 1 步把两个 git mv 排在一起，首个提交因此多带了一个 100% 相似度的重命名，与提交 2 的消息（"reset-vocab.py 归位 scripts/"）意图不符。
- **修正：** 对本方刚创建、未推送的提交做 `git reset --soft` + 选择性 unstage，重拆为计划意图的两个原子提交（79471e6 / fd278f9）。最终历史与计划一致。

## Self-Check: PASSED

- scripts/archive/test-interface.html 存在且仓库根无该文件：已验证（git show rename 100%）。
- scripts/reset-vocab.py 存在且 DB_PATH 锚定：已验证（DB-PATH-OK）。
- cmd/service/debug_serving_test.go 存在且 4 用例 PASS：已验证。
- 3 个提交 79471e6 / fd278f9 / 60c129a 存在：`git log` 已验证。
- 4 个 .gitkeep 退库、四目录代表文件仍在跟踪：`git ls-files` 计数 4 已验证。
- 全量 build/test 与基线一致（exit 0/0）：已验证。
