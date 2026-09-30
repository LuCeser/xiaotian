---
status: issues_found
files_reviewed: 28
critical: 1
warning: 5
info: 4
total: 10
depth: deep
phase: "09"
phase_name: "parent-vocabulary-management"
commits: c481633..52ea8ab (09-01 through 09-08)
lanes:
  - A: pkg/web/api (REST handlers, validation, FSRS settings file, 5 files)
  - B: pkg/pybridge + third_party/python (argv builders, sentinels, Python engine, 6 files)
  - C: pkg/web/static parent UI (HTML/CSS/4 JS modules, 6 files)
  - D: pkg/database + pkg/infra + cmd/service + tests/e2e + node tests (glue, 11 files)
---

# Code Review: Phase 9 — Parent Vocabulary Management

## Summary

Phase 9 的整体工程质量相当高：XSS 契约（createElement/textContent-only）在全部四个 parent JS 文件中零违反（负向 grep 通过）；argv 全部走 `exec.CommandContext` 切片、无任何 shell 拼接；SQL 全参数化且 LIKE 通配符转义正确；fsrs_config.json 的 temp+Sync+Rename 原子写实现正确，并有按 GOOS 分支的 mid-write 失败测试；API 层的 64KiB body cap、DisallowUnknownFields、entry/rune 上限在每条解码路径上都已落地。测试金字塔（fake 单测 → 真解释器 e2e → Playwright 全流程）覆盖面广。

**但存在 1 个 Critical 缺陷，必须在发布前修复：** `deleteWordsArgs` 用逗号 join 词表，而 Python `cmd_delete_words` 用逗号 split 回词表（vocab_manager.py:781）。含 ASCII 逗号的单词可以正常通过 add/edit 全链路入库（无任何校验拒绝），但删除它时会分裂成多个词，**连带删除家长从未在确认对话框中看到的其他单词**（并级联删除 review_logs）——这正是威胁登记表 T-09-05（destructive misfire）要防的事故，且当前没有任何测试覆盖该路径。另有 5 个 Warning：add 路径缺少空词校验导致 502 误报、GET settings 对越界文件值不做回退（与 Python loader 语义分歧）、删除不存在目标映射为 502 而非 404、e2e 会覆写全局 fsrs_config.json 而不快照恢复、`python.dataDir` 无非空校验（留空时静默破坏 Pitfall 8 单一解析规则）。**建议：修复 CR-01 后本阶段方可视为 ship-ready；WR-01..WR-05 应在 verify 前处理。**

### Lane health

- **Lane A (pkg/web/api):** 错误信封、sentinel 映射、原子写实现正确且测试充分。主要问题是验证不一致：edit 路径拒绝空 word 而 add 路径不拒绝（把校验责任漏给 Python 的 exit-1 → 502）；`readFSRSConfig` 不镜像 `load_scheduler` 的越界回退语义。
- **Lane B (pybridge + Python):** argv 构造器是纯函数、契约文档详尽、`--confirm` 结构性保证成立；但 comma-join 编码与 Python comma-split 解码之间存在破坏性歧义（CR-01）。`cmd_edit`/`load_scheduler` 本身干净：COALESCE 语义正确、fsrs_card 未被触碰、warn-and-fallback 捕获了正确的异常族。
- **Lane C (parent UI):** XSS 零违反，modal 焦点圈/取消按钮优先聚焦/搜索防抖 + stale-response 序列号守卫等细节到位。未发现前端自身缺陷；唯一问题是 CR-01 的触发入口（单词删除按钮发送 `words:[word]`）。
- **Lane D (glue + tests):** e2e 的单元级清理（finally + API backstop + 预清理）做得很认真，双次连跑幂等成立；但 settings 是全局文件，测试没有快照/恢复（WR-04）。另有 mimo 测试与 run.ps1 的计划外改动混入本阶段提交（IN-03）。

## Threat Register Mitigation Audit (STRIDE 登记表落地核查)

| Threat | 威胁 | 声称的缓解 | 落地情况 |
|--------|------|-----------|---------|
| T-09-01 | XSS（DOM 渲染） | createElement/textContent-only + 负向 grep | ✅ 四个 JS 文件 0 命中 innerHTML/outerHTML/insertAdjacentHTML/document.write/eval；toast/modal/表格全部节点构造 |
| T-09-02 | SQLi（CountByUnit） | 参数化 + handler 层枚举校验 | ✅ card_repo.go:318-322 参数化 WHERE；handlers.go:409-415 枚举前置 |
| T-09-03 | 命令注入（argv） | argv 切片、无 shell | ✅ 三个新 builder 均为纯变换；`--db` 在子命令前；b.run 未改 |
| T-09-04 | DoS（body 无界） | MaxBytesReader + caps | ✅ validate.go:216 64KiB；所有 POST/PUT/DELETE 路径经 decodeJSON；批量 100 条上限前后端都有 |
| T-09-05 | 破坏性误删 | --confirm 结构保证 + XOR + 计数对话框 | ⚠️ **部分落地**：--confirm 无条件追加（vocab_mgmt.go:238）✅、XOR 校验 ✅、精确计数对话框 ✅ —— 但 comma-join/split 歧义使"删除的恰是所确认的"这一保证失效（CR-01） |
| T-09-06 | 半写配置文件 | temp+Sync+Rename 原子替换 | ✅ fsrs_config.go:95-128 实现正确；失败测试按 GOOS 分支（Windows 用只读目标文件强制 Rename 失败），原文件保持原样 |
| T-09-07 | 信息泄露 | 有界 stdout、仅 code+message | ✅ 300 rune 截断（handlers.go:47,101-103）；客户端永不见 stderr/堆栈。残留：子串启发式可被用户数据误触发（IN-01，低危） |
| T-09-10 | 文件替换路径 | 路径纯配置派生 | ✅ 无用户输入到达路径；但 `dataDir` 留空时路径退化为相对路径且与 Python 解析分叉（WR-05） |
| T-09-12 | 测试数据污染 | e2e 专用单元 + finally 清理 | ⚠️ **部分落地**：e2e-full/-tracer/-backstop 单元均有预清理+finally ✅；但 settings 是全局文件，测试覆写后只恢复到"文档默认值"而非测试前值（WR-04） |
| T-09-SC | 包安装 | 零安装 | ✅ package.json 仅新增测试脚本链入口，无新依赖 |

---

## Critical

### CR-01: 逗号 join/split 歧义 — 删除含逗号的单词会连带删除未确认的其他单词（数据丢失）

**File:** `pkg/pybridge/vocab_mgmt.go:233` + `third_party/python/vocab_manager.py:781` + `pkg/web/api/validate.go:245-255` + `pkg/web/static/js/parent-browse.js:970`
**Severity:** Critical
**Lane:** B (pybridge + Python)

**Issue:** Go 侧 `deleteWordsArgs` 把词表编码为 `strings.Join(opts.Words, ",")`，Python 侧 `cmd_delete_words` 解码为 `word_list = [w.strip() for w in args.words.split(",") if w.strip()]`。整条链路上**没有任何一层校验拒绝含 ASCII 逗号的单词**：

1. `validateAddRequest`（validate.go:245-255）只查 rune 长度，不查 `e.Word` 内容 → `POST /api/v1/words` 可入库单词 `"a,b"`；
2. `parseBatch`（parent-batch-parser.js:94-101）同样只查空与长度；
3. `validateEditRequest` 允许把单词改成 `"a,b"`；
4. `validateDeleteRequest`（validate.go:131-138）对 `words[i]` 也只查空与长度。

后果（真实 UI 路径）：家长在删除对话框看到精确文案 `确认删除「a,b」？` 并确认，前端发送 `{category, words:["a,b"]}`（parent-browse.js:970）→ Go join 成 `--words "a,b"` → Python split 成 `["a","b"]` → **在同 category 下删除单词 "a" 和 "b"，连同它们的 review_logs** —— 这两个词从未出现在任何确认对话框里。批量删除更甚：`["x","a,b"]` → `"x,a,b"` → 删除 x、a、b 三个词。T-09-05 威胁登记声称的"server path is ALWAYS the confirmed delete"保证被打破，且 `pkg/pybridge/vocab_mgmt_test.go` / `vocab_mgmt_e2e_test.go` / `handlers_test.go` 中没有任何逗号单词测试覆盖此路径。

**Fix:** 最小修复是在 API 边界拒绝含 ASCII 逗号的单词（与 Python 的 split 契约对齐），三层同改：

```go
// pkg/web/api/validate.go — validateAddRequest 循环内、rune 检查前:
if strings.Contains(e.Word, ",") {
    return fmt.Sprintf("entries[%d].word must not contain commas", i)
}
// validateEditRequest / validateDeleteRequest 同样加 word/words[i] 逗号检查
```

```js
// pkg/web/static/js/parent-batch-parser.js — word === '' 检查后:
if (word.indexOf(',') !== -1) {
  errors.push({ line: lineNumber, reason: 'word-comma' });
  continue;
}
```

（长期方案：给 `delete-words` 增加类似 add 的 `--file` JSON 词表模式，消除编码歧义；同时清理存量库中已含逗号的单词。）

---

## Warning

### WR-01: add 路径缺少空词校验 — 空单词请求返回 502 python_failed 而非 400 invalid_request

**File:** `pkg/web/api/validate.go:245-255`
**Severity:** Warning
**Lane:** A

**Issue:** `validateEditRequest` 拒绝空 word（"word is required"），但 `validateAddRequest` 的 entries 循环只检查 rune 上限、从不检查 `e.Word == ""`。空词请求穿过 API 层到达 Python 后被 `cmd_add` 拒绝（`Error: item 0 missing or empty 'word' field`，exit 1）→ `ExitError(ErrPythonExit)` → `stdoutNotFoundMsg` 不匹配 → **502 python_failed "failed to save words, please retry"**。用户按提示重试永远失败；错误码也违反了本阶段自己锁定的错误契约（校验错误必须是 400 invalid_request）。`handlers_test.go` 的 TestAddWords 同样没有空词用例（对比 TestEditWord 有 "empty word returns 400"）。

**Fix:** 在 `validateAddRequest` 循环开头加：
```go
if e.Word == "" {
    return fmt.Sprintf("entries[%d].word must not be empty", i)
}
```

### WR-02: readFSRSConfig 不做越界回退 — GET 对被篡改/手改的配置返回调度器实际不会使用的值

**File:** `pkg/web/api/fsrs_config.go:78-88` vs `third_party/python/vocab_manager.py:97-127`
**Severity:** Warning
**Lane:** A / B

**Issue:** Python `load_scheduler` 对合法 JSON 但越界的值（如 `desired_retention: 1.7` 或部分字段非法）**整体回退到默认 0.85/180 并打 Warning**；而 Go `readFSRSConfig` 只要 `json.Unmarshal` 成功就原样返回 —— 包括 `maximum_interval: 0`（字段缺失时）或 1.7 这类越界值。一旦 `fsrs_config.json` 被手改/半损坏（T-09-06 明确考虑了 corrupted/tampered file 场景），GET /settings/fsrs 会向 UI 报告一组**调度器实际并未使用的"当前值"**，与 Python 侧真实行为分歧；SettingsCard 的 slider 读数随后误导家长。写路径（PUT）有界校验，所以该分歧只能经手动编辑进入，但读侧语义应与读者（load_scheduler）保持一致。

**Fix:** `readFSRSConfig` 解码后复用 `ValidateFSRSSettings`，失败即返回 `fsrsSettingsDefaults()`（与 Python 的整文件回退语义对齐）：
```go
var s FSRSSettings
if err := json.Unmarshal(raw, &s); err != nil {
    return fsrsSettingsDefaults()
}
if ValidateFSRSSettings(s) != nil {
    return fsrsSettingsDefaults()
}
return s
```

### WR-03: 删除不存在的目标（词/单元）映射为 502 python_failed 而非 404/幂等成功

**File:** `third_party/python/vocab_manager.py:759,792`（exit 0 + 非 JSON stdout）+ `pkg/pybridge/vocab_mgmt.go:284-292` + `pkg/web/api/handlers.go:116-143`
**Severity:** Warning
**Lane:** B

**Issue:** `cmd_delete_words` 在 unit 模式无可删行时打印 `No cards found with unit '...'` 并 **exit 0**（words 模式的 `No matching cards found` 同样 exit 0）。`classifyMgmtRun` 对 exit 0 + 非 JSON stdout 走 `extractTrailingJSON` 失败 → `ErrPythonJSONParse` → **502 python_failed "operation failed, please retry"**。对比：rename-unit 的 not-found 是 exit 1（vocab_manager.py:735）→ 正确映射 404。多标签页并发下（A 页删完 B 页再删）家长会看到"删除失败，请重试"，重试再失败 —— 对一个天然的幂等无操作报"可重试的服务器故障"，误导且违反错误契约。（e2e 的 `deleteUnit` 用 `.catch(()=>{})` 吞掉了此错误，掩盖了该映射缺陷。）

**Fix:** 任选其一：(a) 在 `stdoutNotFoundMsg` 之外增加对 exit-0 分支的处理 —— `classifyMgmtRun` 在 `extractTrailingJSON` 失败前检查 stdout 是否为纯诊断文本（"No cards found"/"No matching cards"）并返回一个可映射 404 的 typed error；(b) 改 Python：not-found 时打印 `{"deleted":0}` 正常退出（保持 e2e 清理幂等）或 exit 1 对齐 rename-unit。方案 (b) 改动最小。

### WR-04: e2e 全流程测试覆写全局 fsrs_config.json 且不快照测试前值；失败时 finally 不恢复 settings

**File:** `tests/e2e/parent-management.spec.js:224-253,279-285`
**Severity:** Warning
**Lane:** D

**Issue:** 步骤 7 把 `desired_retention` 存为 0.90 再"恢复默认"存回 0.85/180 —— 恢复目标是**文档默认值**而非**测试前快照**。两个后果：(1) 对着真实 VPS 跑套件（webServer 即真实服务、真实 DataDir），家长此前保存的如 0.92/200 会被静默重置为 0.85/180 —— settings 是全局文件，不属于 e2e-prefixed 单元隔离范围（T-09-12 的隔离承诺未覆盖它）；(2) 测试在保存 0.90 之后、恢复之前失败时，finally 块只清理三个 unit，**不恢复 settings**，库上留下 0.90。

**Fix:** 测试开头快照、finally 恢复：
```js
const before = await (await request.get('/api/v1/settings/fsrs')).json();
try {
  // ...原流程...
} finally {
  await deleteUnit(request, UNIT_FULL);
  await deleteUnit(request, UNIT_FULL_RENAMED);
  await deleteUnit(request, UNIT_TRACER);
  await request.put('/api/v1/settings/fsrs', { data: before }).catch(() => {});
}
```

### WR-05: python.dataDir 无非空校验 — 留空时 settings 文件写入 CWD 而 Python 读 SKILL_DIR/data，Pitfall 8 单一解析规则静默失效

**File:** `pkg/infra/config.go:381-383`（无 Validate 分支）+ `pkg/web/api/fsrs_config.go:132-134` + `third_party/python/vocab_manager.py:68` + `pkg/pybridge/bridge.go:212-213`
**Severity:** Warning
**Lane:** D

**Issue:** `Config.Validate()` 校验了 `mgmtTimeout > 0` 等所有 Python 字段，唯独不要求 `python.dataDir` 非空；`DefaultConfig()` 也不给默认值。若配置文件漏掉 `dataDir`：(1) `bridge.childEnv` 因 `b.cfg.DataDir != ""` 为假而**不转发** `DICTATION_DATA_DIR` → Python 回退到 `SKILL_DIR/data`（脚本自身目录下）；(2) Go 侧 `fsrsConfigPath()` = `filepath.Join("", "fsrs_config.json")` = **相对当前工作目录的路径**。两端写读分家：家长在 学习设置 卡保存成功、看到"已保存，下次听写生效"，但调度器永远读不到该文件 —— 静默功能失效，且无任何日志提示。（两份发布模板 config.yaml / config.example.yaml 都设置了 dataDir，故标准部署不触发；但 Validate 是本仓库钦定的 fail-fast 层，此处缺口恰好绕过了它。）

**Fix:** `Config.Validate()` 增加：
```go
if c.Python.DataDir == "" {
    return errors.Wrap(ErrConfigInvalid, "python.dataDir must not be empty (anchors fsrs_config.json writer and DICTATION_DATA_DIR reader on one directory)")
}
```

---

## Info

### IN-01: stdoutNotFoundMsg 子串启发式可被用户数据误触发

**File:** `pkg/web/api/handlers.go:92-105`
**Issue:** 以 `strings.Contains(stdout, "not found")` 判定 404。exit-1 场景下若 stdout 恰含用户输入的词/单元名（如单元名 `...not found...` 出现在 traceback 回显中），一次真实的 Python 故障会被误报为 404 not_found 并把 stdout 原文回给客户端（300 rune 内）。私有部署下影响有限。
**Fix:** 匹配收紧为行首锚定的脚本诊断模板（`Card \d+ not found` / `^No cards found with unit`），或让 Python 输出结构化错误码。

### IN-02: 客户端取消请求被归类为 ErrPythonTimeout

**File:** `pkg/pybridge/vocab_mgmt.go:271-274`
**Issue:** `context.Canceled`（客户端断开）与 `DeadlineExceeded` 一样映射到 `ErrPythonTimeout`，日志/指标把家长关页面计成"Python 超时"，干扰 1C1G 上的超时监控信号。
**Fix:** 拆开分支：`DeadlineExceeded → ErrPythonTimeout`；`Canceled → errors.Wrap(ErrPythonCanceled, ...)`（新增 sentinel）或至少在 logResult 里区分。

### IN-03: 计划外改动混入阶段提交 — mimo 测试与 run.ps1

**File:** `pkg/mimo/client_test.go:220`、`pkg/mimo/integration_test.go:66-69,98,149,...`、`run.ps1:88-93`
**Issue:** mimo 测试的 `RetryDelay 2s→1ms`、integration 测试的 `-short` 跳过、run.ps1 从 `go run` 改为 `go build` 均不在任何 09-0x-PLAN 的 `files_modified` 里。改动本身良性（测试提速、防误跑 live 服务），但绕过了计划文件清单，review 时差点漏扫。
**Fix:** 无需改代码；建议在 09-08-SUMMARY 或 STATE 里补记这三处 out-of-plan 改动及理由，保持 GSD 计划-提交可对账。

### IN-04: 三个 parent JS 模块各自复制 clearContainer / buildLoadingButton / setLoading / toast 帮助函数

**File:** `pkg/web/static/js/parent-modes.js:43-101` + `pkg/web/static/js/parent-browse.js:44-116`
**Issue:** 代码注释已声明"local copy keeps the module independent"是刻意为之，且 parent-app.js 只做装配不复制 —— 当前尚可接受。但 toast 单例逻辑（`document.querySelector('.toast')` 互斥）分散在两处，未来改 toast 行为容易只改一半。
**Fix:** 后续把 DOM 帮助函数提到一个 parent-dom.js 共享模块（非本阶段必须）。

---

## Verified Clean（重点核查过、未发现问题的区域）

- **XSS（T-09-01）**：四个 parent JS 文件对 `innerHTML|outerHTML|insertAdjacentHTML|document.write|eval|new Function` 的负向 grep 全部 0 命中；所有用户数据渲染路径（preview 表格、word 行、modal 文案、toast、datalist option、localStorage 回读）均为 `createElement` + `textContent`；`setAttribute('data-unit', ...)`、`name.title` 属安全属性赋值。parent.css 无 `url()`/`expression()`/内联事件。
- **原子写（T-09-06）**：`writeFSRSConfig` 的 CreateTemp(同目录)→Write→Sync→Close→Rename 顺序正确，`defer os.Remove(tmpName)` 覆盖所有失败出口，成功 rename 后的 Remove 错误被有意忽略；键序由 struct 声明序保证，测试断言原始字节。
- **argv/超时（T-09-03）**：`--db` 位于子命令 token 之前（argparse 顶层契约），三个新 builder 为纯函数，`--confirm` 在 deleteWordsArgs 成功路径上无条件追加（无 dry-run 逃逸面），`MgmtTimeout` 的声明/默认值/Validate/bridge 构造四层齐备，`b.run`（信号量+进程组+超时）未被改动。
- **SQL（T-09-02）**：`List` 的 LIKE 用 `escapeLike` 转义 `\ % _` 且参数化；`CountByUnit` 参数化 GROUP BY；card_repo 无写方法、无 os/exec。
- **编辑不碰 FSRS（D-07）**：`cmd_edit` 的 UPDATE 仅触及四个文本列（COALESCE None/空串语义正确），e2e 有 fsrs_card 字节级前后对比测试。
- **前端状态机**：fetchSeq stale-response 守卫、300ms 防抖、localStorage 防御性 try/catch、modal 焦点圈与"取消按钮优先聚焦"（防误触确认）均正确。

---

_Reviewed: 2026-09-30T00:00:00Z_
_Reviewer: gsd-code-reviewer (local general lane stand-in)_
_Depth: deep (cross-file: Go↔Python argv/JSON 契约、前端↔API、e2e↔真实服务)_
