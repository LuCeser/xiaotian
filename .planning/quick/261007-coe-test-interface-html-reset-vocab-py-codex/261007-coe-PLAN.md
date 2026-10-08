---
phase: 261007-coe
plan: 01
type: execute
wave: 1
depends_on: []
files_modified:
  - test-interface.html
  - scripts/archive/test-interface.html
  - cmd/service/main.go
  - cmd/service/debug_serving_test.go
  - reset-vocab.py
  - scripts/reset-vocab.py
  - .claude/settings.local.json
  - README.md
  - cmd/service/.gitkeep
  - internal/.gitkeep
  - pkg/infra/.gitkeep
  - pkg/web/static/js/.gitkeep
autonomous: true
requirements: ["QUICK-261007-COE"]

estimate:
  tokens: 26000
  raw_tokens: 17000
  tasks: 3
  confidence: low

must_haves:
  truths:
    - "仓库根不再有 test-interface.html 与 reset-vocab.py（分别位于 scripts/archive/ 与 scripts/），且 URL /test-interface.html 仍返回 200（服务从归档路径供给，URL 契约不变）"
    - "scripts/reset-vocab.py 的 DB_PATH 以 __file__ 锚定仓库根 .hta-data/vocab.db，从任意 CWD 运行都能定位同一数据库（对齐 scripts/check_*.py 范式）"
    - ".codex-network-log/ 与 test-results/ 已删除；server-8443.out.log 尽力删除（若仍被 8443 服务锁定则记录跳过）；三者本就未跟踪/被忽略，git 状态不受影响"
    - "4 个残留 .gitkeep（cmd/service、internal、pkg/infra、pkg/web/static/js）删除后对应目录内容仍完整跟踪；README Project Layout 补上 pkg/ws、pkg/crc16、pkg/rt、pkg/grading、pkg/lesson、pkg/ocr 且 internal/ 描述与实际一致"
    - "证据件 rt-timing-20261006-202012.jsonl 与 server-8443.err.log 原位未动、未入库（13-UAT-ISSUES-20261006.md L102 证据索引不断链）"
    - "go build ./... 与 go test ./... -short 通过且与执行前基线一致；根 git status --porcelain 除本计划目录外为空"
  artifacts:
    - "scripts/archive/test-interface.html（自仓库根 git mv 而来，59KB 内容不变）"
    - "scripts/reset-vocab.py（DB_PATH 已 pathlib 锚定）"
    - "cmd/service/debug_serving_test.go（httptest + t.Chdir 仓库根，断言归档路径 200/缺失 404）"
    - "更新后的 cmd/service/main.go（调试页路由指向归档路径、死页路由移除、fmt 占位删除）"
    - "更新后的 README.md（Project Layout 与 pkg/ 实况一致）"
  key_links:
    - "main.go 调试页注册（staticDir 分支的单条 HandleFunc）+ serveDebugRoot ↔ scripts/archive/test-interface.html（URL 保持 /test-interface.html，run.ps1/README/DEPLOYMENT.md 的 URL 级引用因此无需修改）"
    - "scripts/reset-vocab.py DB_PATH ↔ 仓库根 .hta-data/vocab.db（__file__ 向上两级锚定，与 scripts/check_db.py 同一写法）"
    - "run.ps1 entryUrl http://localhost:8080/test-interface.html ↔ main.go 路由（URL 契约保持，run.ps1 不改）"
    - "13-UAT-ISSUES-20261006.md L102 证据索引 ↔ 仓库根 rt-timing-20261006-202012.jsonl + server-8443.err.log（路径不可移动、不入库）"
---

<objective>
根目录梳理第二轮：把 round-1 遗留的 test-interface.html（→ scripts/archive/）与 reset-vocab.py（→ scripts/）归位——搬前引用已查清、搬后同提交改引用；清理 .codex-network-log/ 与 test-results/ 工具产物（server-8443.out.log 尽力删）；收尾旧卫生 todo 的 README 布局漂移与残留 .gitkeep（含 main.go fmt 占位）。台账证据件不动不入库。

Purpose: round-1 建立的分类约定（活脚本 scripts/、死件 scripts/archive/、分析文档 docs/archive/）落到最后两个根级跟踪文件上；2026-07-25 卫生总纲 todo 的 README/.gitkeep 项就此关闭。
Output: 3 个原子提交（归位×2 + 卫生收尾×1）、删除的未跟踪工具产物、更新后的 main.go/README、新增的调试页路由测试。
</objective>

<execution_context>
@D:/workspace/ai/xiaotian/.claude/gsd-core/workflows/execute-plan.md
@D:/workspace/ai/xiaotian/.claude/gsd-core/templates/summary.md
</execution_context>

<context>
@.planning/STATE.md
@.planning/quick/261006-u7o-scratch/261006-u7o-PLAN.md
@.planning/todos/pending/2026-07-25-repo-hygiene-readme-gitignore-cleanup.md
@.planning/phases/13-realtime-minimal-loop/13-UAT-ISSUES-20261006.md

## 规划期已核实的活体事实（executor 直接采信，不必重查；执行前仅按 precondition 复核快照仍成立）

1. **test-interface.html（跟踪，59KB，仓库根）的全部引用盘点**（全仓 grep，排除 node_modules/.git/.claude/worktrees）：
   - **文件路径级（必须同提交改）**：`cmd/service/main.go` 四处——`debugInterfaces` 列表首项（L861-866）、`serveDebugRoot` 的 `case "/", "/test-interface.html"` 分支（L889-890）、以及两处注释。机制：staticDir 非空时 `publicMux.HandleFunc("/"+dbg, serveRepoRootFile(dbg))`（L680-682，URL=路径=CWD 相对）；staticDir 为空时 `/` 落到 `serveDebugRoot`。`serveRepoRootFile` 对缺失文件 os.Stat 后 404，天然优雅降级。
   - **URL 级（无需改——URL 契约保持后仍为真）**：`run.ps1` L25 注释与 L59 entryUrl（HTTP 模式入口；HTTPS 模式入口是 /tutor/，L62）、`README.md` L58、`docs/DEPLOYMENT.md` L262-274（描述 serveDebugRoot 兜底行为）。
   - **溯源注释级（无需改——历史事实陈述）**：`pkg/tts/types.go` L35、`pkg/web/static/js/audio-controller.js` L6、`tests/test-pcm-conversion.js` L2/L8。
   - **历史记录（不动）**：.planning 各阶段文档、`scripts/archive/test-audio-bug-frontend.html`（round-1 已归档的死页）。
   - **`debugInterfaces` 其余三项**（test-interface-debug.html、test-audio-sequence-bug.html、test-audio-bug-frontend.html）的文件 round-1 已归档，路由当前实际 404——本计划顺路移除这三条死引用，URL 行为不变（仍 404）。
2. **reset-vocab.py（跟踪，仓库根，261006-tnr 刚扩展过）**：L17 `DB_PATH = "./.hta-data/vocab.db"` 为 CWD 相对——搬到 scripts/ 后必须改为 `pathlib.Path(__file__).resolve().parent.parent / ".hta-data" / "vocab.db"`（与 scripts/check_db.py 完全同范式）。活引用仅 `.claude/settings.local.json` L63 权限规则 `"Bash(./reset-vocab.py *)"`（本地未跟踪文件，改了不入库）；.planning 内引用全是历史记录，不改。**该脚本会清空学习状态并删当天课堂行——验证只许 importlib 加载断言 DB_PATH，绝不对真实库执行。**
3. **工具产物**：`.codex-network-log/` 未跟踪、全仓零引用、仅含 `codex-tui.log`（已被 .gitignore L32 `*.log` 覆盖，故 git status 不显）→ 整目录删除，无需新增 gitignore 规则。`test-results/` 被 L44 `test-results/` 忽略、仅含 `.last-run.json`（playwright 默认输出目录，下次运行自建）→ 删除，忽略规则保留。`server-8443.out.log` 0 字节、被 `*.log` 忽略——round-1 Task 2 已列删但仍存在（当时被 8443 服务锁定或事后重建）→ 本轮尽力删。
4. **卫生 todo 残项核验**：README 的阶段状态与依赖列表漂移已被 2026-09-30 重写修掉，唯 Project Layout 仍缺 `pkg/ws`、`pkg/crc16`、`pkg/rt`、`pkg/grading`、`pkg/lesson`、`pkg/ocr`（pkg/ 实际 15 个子目录），且 `internal/` 描述"reserved for future phases"失实（实际含 pytest 假件与 wstest 辅助）。残留 .gitkeep 共 4 个（cmd/service、internal、pkg/infra、pkg/web/static/js——规划期 git ls-files 逐一核实目录均有其他跟踪文件；.planning 下的 .gitkeep 属 GSD 工件，不在本轮范围）。`cmd/service/main.go` L836-838 的 ensure-fmt 注释两行 + 占位 var 声明仍在，而 fmt 在 L592 有真实调用（删占位后 import 保留）。
5. **round-1 硬约束继续有效**：CRLF 幻影用 `git diff --numstat` 验真伪；绝不 `git push`（本地 main 领先已改写历史的 origin 230+ 提交）；`python` 是商店stub 用 `py -3`；run.ps1/stop.ps1 引用对不拆散；playwright testDirs（tests/e2e、tests/e2e-rt）不动；新入库内容先扫 sk-/api_key/Bearer 密钥模式；台账证据件（rt-timing-20261006-202012.jsonl、server-8443.err.log）原位不入库。当前根 git status --porcelain 为空（干净基线）。
</context>

<tasks>

<task type="auto">
  <name>Task 1: test-interface.html 与 reset-vocab.py 归位（git mv + 同提交改引用 + 路径锚定）</name>
  <files>test-interface.html, scripts/archive/test-interface.html, cmd/service/main.go, cmd/service/debug_serving_test.go, reset-vocab.py, scripts/reset-vocab.py, .claude/settings.local.json</files>
  <precondition>仓库根 = D:/workspace/ai/xiaotian，`git status --porcelain` 除 .planning/quick/261007-coe-* 外为空且 `git diff --numstat` 无输出（干净基线，无 CRLF 幻影掩盖）；先跑 `go build ./... && go test ./... -short` 记录基线退出码（本任务结束后不得变差）。若任一不成立，停下单出报告。</precondition>
  <action>
  1. **搬**：`git mv test-interface.html scripts/archive/test-interface.html`；`git mv reset-vocab.py scripts/reset-vocab.py`。
  2. **改 main.go 路由（URL 契约不变，仅文件路径换向归档位置）**：
     - staticDir 非空分支（原 L680-682 的 debugInterfaces 循环）替换为单条注册：`publicMux.HandleFunc("/test-interface.html", serveRepoRootFile("scripts/archive/test-interface.html"))`，原注释保留并补一句说明该调试页已归档至 scripts/archive/、ServeMux 长模式优先于 "/" 前缀的理由不变。
     - 删除 `var debugInterfaces = []string{...}` 整个变量及其注释块（列表四项中三项 round-1 已归档、本轮一项也归档，列表已无活件）。
     - `serveDebugRoot`（staticDir 为空时的兜底）：switch 收敛为 `case "/", "/test-interface.html"` 服务 `scripts/archive/test-interface.html`，删除三个已归档死页的 case；函数头注释同步改写（说明兜底页现位于 scripts/archive/）。
     - `serveRepoRootFile` 注释中 "repo-root" 措辞改为 "repository-relative"（它现在服务仓库相对路径而非仅根目录文件）。
  3. **新增测试** `cmd/service/debug_serving_test.go`：用 `t.Chdir(filepath.Join("..", ".."))` 把 CWD 切到仓库根（Go 1.26 自带 t.Chdir；不得调 t.Parallel），httptest 断言四件事——serveRepoRootFile("scripts/archive/test-interface.html") 返回 200 且 body 超过 1000 字节；serveRepoRootFile 对不存在的文件名返回 404；serveDebugRoot 对 "/" 返回 200 且 body 超过 1000 字节；serveDebugRoot 对已移除的死页路径（如 /test-interface-debug.html）落到 default 返回 404。
  4. 跑 `go build ./... && go test ./cmd/service -run 'TestServeRepoRootFile|TestServeDebugRoot' -count=1` 通过后，对 staged 内容跑密钥模式扫描（sk- 长串/api_key 赋值/Bearer 长串，round-1 T-u7o-01 约定），零命中才提交。提交 1：`chore(261007-coe): test-interface.html 归位 scripts/archive——/test-interface.html 路由改指归档路径并移除已归档死页路由`（含 git mv、main.go、新测试文件，同一提交）。
  5. **改 reset-vocab.py 路径锚定**：`scripts/reset-vocab.py` 加 `import pathlib`，L17 的 `DB_PATH = "./.hta-data/vocab.db"` 改为 `DB_PATH = str(pathlib.Path(__file__).resolve().parent.parent / ".hta-data" / "vocab.db")`（与 scripts/check_db.py 同一写法）；文件头 docstring 不动。
  6. **改本地权限规则**：`.claude/settings.local.json` L63 的 `"Bash(./reset-vocab.py *)"` 替换为 `"Bash(py -3 scripts/reset-vocab.py *)"` 与 `"Bash(./scripts/reset-vocab.py *)"` 两条（覆盖 Windows py 启动器与 bash 直跑两种形态；该文件未跟踪、不入库）。
  7. 提交 2：`chore(261007-coe): reset-vocab.py 归位 scripts/，DB 路径按脚本位置锚定仓库根（对齐 check_*.py 范式）`。**验证严禁运行该脚本本身**——它会重置全部卡片并删当天课堂行（破坏活跃学习数据）；路径正确性用 importlib 加载断言（见 verify，模块级只做赋值，__main__ 守卫拦住执行）。
  </action>
  <verify>
    <automated>go build ./... && go test ./cmd/service -run 'TestServeRepoRootFile|TestServeDebugRoot' -count=1 && py -3 -c "import importlib.util,pathlib; spec=importlib.util.spec_from_file_location('rv','scripts/reset-vocab.py'); m=importlib.util.module_from_spec(spec); spec.loader.exec_module(m); p=pathlib.Path(m.DB_PATH); assert p.is_absolute() and p==pathlib.Path('.hta-data/vocab.db').resolve() and p.exists(), p; print('DB-PATH-OK')" && test ! -e test-interface.html && test ! -e reset-vocab.py && test -e scripts/archive/test-interface.html && test -e scripts/reset-vocab.py && grep -q 'scripts/reset-vocab.py' .claude/settings.local.json && test -e rt-timing-20261006-202012.jsonl && test -e server-8443.err.log && echo TASK1-OK</automated>
  </verify>
  <done>两个提交落库（git log 前缀 261007-coe）；test-interface.html 只存在于 scripts/archive/、reset-vocab.py 只存在于 scripts/；URL /test-interface.html 的供给路径经 httptest 断言 200；reset-vocab.py 的 DB_PATH 解析为仓库根 .hta-data/vocab.db 绝对路径且存在；settings.local.json 规则指向新路径；两个台账证据件仍在仓库根未入库。</done>
  <reversibility rating="reversible">两个 git mv 均可 `git mv` 回退，main.go 路由改动有测试锁定；唯一不可逆操作（删未跟踪件）在 Task 2。</reversibility>
</task>

<task type="auto">
  <name>Task 2: 工具产物清理（.codex-network-log、test-results、server-8443.out.log 尽力删）</name>
  <files>.codex-network-log/, test-results/, server-8443.out.log</files>
  <precondition>Task 1 完成（工作树回到干净态，只剩本计划目录未跟踪）。</precondition>
  <action>
  1. 删除 `.codex-network-log/` 整目录（未跟踪、全仓零引用、仅含 codex-tui.log——已核实无 .codex/config.toml 或其他配置指向它；删除后无需新增 gitignore 规则，`*.log` 已覆盖其内容）。
  2. 删除 `test-results/` 整目录（playwright 默认输出目录，仅含 .last-run.json，下次 e2e 运行自建；.gitignore L44 `test-results/` 继续覆盖）。
  3. **尽力删** `server-8443.out.log`：先试 `rm -f server-8443.out.log`；若因 8443 服务进程锁定失败（Windows 文件锁），不重试不停单——在 SUMMARY 记录"仍被锁定，0 字节且被 *.log 忽略，留待服务停止后自然可删"。round-1 已出现过一次锁定，失败是预期分支不是事故。
  4. **绝不动**：`rt-timing-20261006-202012.jsonl` 与 `server-8443.err.log`（台账 L102 证据索引按仓库根路径引用，13-UAT 未收口前原地保留）。
  5. 三项均为未跟踪/被忽略文件，本任务不产生提交；结束后确认 git status 仍干净。
  </action>
  <verify>
    <automated>test ! -e .codex-network-log && test ! -e test-results && test -e rt-timing-20261006-202012.jsonl && test -e server-8443.err.log && test "$(git status --porcelain | grep -vE '^\?\? \.planning/quick/261007-coe' | wc -l)" = 0 && echo TASK2-OK</automated>
  </verify>
  <done>.codex-network-log/ 与 test-results/ 不存在；server-8443.out.log 已删或已记录锁定跳过；两个台账证据件原位；git status 除本计划目录外无任何条目（删除未忽略文件的失误会在此显形）。</done>
  <reversibility rating="costly">删除的是未跟踪可再生工具产物（codex TUI 日志、playwright last-run 标记、0 字节服务日志），不可恢复但均可再生或本就无留存价值；规划期已逐项查过引用与 gitignore 覆盖。</reversibility>
</task>

<task type="auto">
  <name>Task 3: 卫生 todo 收尾（README 布局补全 + 4 个残留 .gitkeep + main.go fmt 占位）+ 终验</name>
  <files>README.md, cmd/service/main.go, cmd/service/.gitkeep, internal/.gitkeep, pkg/infra/.gitkeep, pkg/web/static/js/.gitkeep</files>
  <precondition>Task 1 已提交（main.go 的路由改动先行落库，避免本任务混入同一 diff）。</precondition>
  <action>
  1. **README Project Layout 修正**（对照 `ls pkg/` 实况，英文行文与现有列表一致）：`internal/` 行描述改为私有测试辅助（pytest shell 假件、wstest WebSocket 测试件）；在 pkg 行列表中补三行——`pkg/ws/`、`pkg/crc16/`（WebSocket 库与流式音频帧校验）、`pkg/rt/`（v2.0 实时家教会话状态机）、`pkg/grading/`、`pkg/lesson/`、`pkg/ocr/`（批改、课堂记录、拍照 OCR）。只动 Project Layout 一节与 internal 行，其余章节（Phase Status、Versioning 等）不在本 todo 漂移清单内，不碰。
  2. **删 4 个残留 .gitkeep**：对 cmd/service、internal、pkg/infra、pkg/web/static/js 四目录先跑守卫 `git ls-files <目录> | grep -v '\.gitkeep' | wc -l`，输出非零（目录另有跟踪文件，规划期已逐一核实）才 `git rm <目录>/.gitkeep`；守卫输出为零则跳过该目录并在 SUMMARY 记录。.planning 下的 .gitkeep 是 GSD 工件，不在范围。
  3. **删 main.go fmt 占位**：删除 ensure-fmt 两行注释与紧随的占位 var 声明（原 L836-838；fmt 在 rt-%d 会话号格式化处仍有真实调用，import 保留——go build 即证）。
  4. 对 staged 内容跑密钥模式扫描（同 Task 1 约定），零命中后提交 3：`chore(261007-coe): 卫生 todo 收尾——删 4 个残留 .gitkeep 与 main.go fmt 占位，README 项目布局补全`。
  5. **终验**：`go build ./... && go test ./... -short` 与 Task 1 记录的基线一致；根 `git status --porcelain` 除本计划目录外为空；`py -3 scripts/reset-vocab.py` 的新调用形态写入 SUMMARY 的使用说明（供 13-13 期间复用，提醒以后从仓库根以 `py -3 scripts/reset-vocab.py` 调用）。
  </action>
  <verify>
    <automated>! grep -q 'ensure fmt is referenced' cmd/service/main.go && ! grep -q 'var debugInterfaces' cmd/service/main.go && grep -q 'pkg/ws' README.md && grep -q 'pkg/crc16' README.md && grep -q 'pkg/rt' README.md && grep -q 'pkg/grading' README.md && test "$(git ls-files -- 'cmd/service/.gitkeep' 'internal/.gitkeep' 'pkg/infra/.gitkeep' 'pkg/web/static/js/.gitkeep' | wc -l)" = 0 && test "$(git ls-files cmd/service/main.go internal/pytest/fake_vocab.sh pkg/infra/logging.go pkg/web/static/js/app.js | wc -l)" = 4 && go build ./... && go test ./... -short && test -e rt-timing-20261006-202012.jsonl && test -e server-8443.err.log && echo TASK3-OK</automated>
  </verify>
  <done>第 3 个提交落库；4 个 .gitkeep 退库而四目录代表文件仍在跟踪；README 布局含 ws/crc16/rt/grading/lesson/ocr；main.go 无 fmt 占位、无 debugInterfaces 残骸；全量 build/test 与基线一致；根 git status 干净；台账证据件在位。完成后卫生 todo 的 README 漂移与 .gitkeep 两项可勾销（todo 文件本身的勾选更新由 /gsd-capture 或用户处理，不在本计划内）。</done>
</task>

</tasks>

<threat_model>
## Trust Boundaries

| Boundary | Description |
|----------|-------------|
| 归档路径 → 运行时服务 | main.go 从仓库相对路径供给调试页：路径改动直接改变 HTTP 响应 |
| 验证操作 → 活跃学习数据 | reset-vocab.py 连的是真实 .hta-data/vocab.db（13-13 验收在即） |
| 工作区 → git 历史 | 本轮新入库内容仅新测试文件与文档/路由改动 |

## STRIDE Threat Register

| Threat ID | Category | Component | Severity | Disposition | Mitigation Plan |
|-----------|----------|-----------|----------|-------------|-----------------|
| T-coe-01 | Tampering | main.go 调试页路由（/test-interface.html → 归档路径） | medium | mitigate | URL 契约保持（run.ps1/README/DEPLOYMENT 引用不动即证明）；新增 httptest + t.Chdir 断言 200/404；go build 全量编译 |
| T-coe-02 | Tampering | reset-vocab.py 验证方式（误执行会清空卡片、删当天课堂行） | high | mitigate | 验证仅用 importlib 加载断言 DB_PATH（__main__ 守卫拦执行），verify 命令不含任何运行脚本本体形态；action 明文禁止执行 |
| T-coe-03 | Information Disclosure | 证据件 rt-timing-20261006-202012.jsonl、server-8443.err.log（含本机会话与 LAN 信息） | low | mitigate | 每个 verify 都断言两件存在且从未进入 staged；它们已被 gitignore 覆盖，删除清单与它们物理隔离 |
| T-coe-04 | Denial of Service | server-8443.out.log 删除撞上 8443 服务文件锁 | low | accept | 尽力删 + 失败记录跳过（0 字节、被忽略，无实际影响）；不杀服务进程换删除成功 |
| T-coe-SC | Tampering | 供应链（npm/pip/cargo 安装） | high | accept | 本计划零包安装、零依赖变更，供应链面无暴露 |
</threat_model>

<verification>
1. `git log --oneline -5` 可见 3 个 261007-coe 前缀提交（chore 分型）。
2. `git status --porcelain` 全空（允许仅剩 `.planning/quick/261007-coe-*`——由 gsd-quick 流程自身收尾提交）。
3. `go build ./...` 与 `go test ./... -short` 与 Task 1 基线一致（不为红、不新增失败）。
4. 台账证据链完好：仓库根存在 rt-timing-20261006-202012.jsonl 与 server-8443.err.log，`git ls-files` 不含两者。
5. 根目录一级不再出现 test-interface.html、reset-vocab.py、.codex-network-log、test-results（server-8443.out.log 允许锁定残留）。
6. URL 契约：`go test ./cmd/service -run 'TestServeRepoRootFile|TestServeDebugRoot' -count=1` 绿——/test-interface.html 仍从归档路径供给。
</verification>

<success_criteria>
- 两个根级跟踪文件归位且零断链：scripts/archive/test-interface.html（URL /test-interface.html 经测试证明仍 200）、scripts/reset-vocab.py（DB 路径锚定仓库根，从任意 CWD 调用同库）。
- 工具产物清零：.codex-network-log 与 test-results 删除，gitignore 覆盖无缺口，git 状态零扰动。
- 2026-07-25 卫生 todo 两残项关闭：README Project Layout 与 pkg/ 实况一致（15 个子目录无一缺席）、4 个残留 .gitkeep 退库、main.go fmt 占位清除。
- 证据件与 round-1 约定全部无损；绝不 push。
</success_criteria>

<output>
Create `.planning/quick/261007-coe-test-interface-html-reset-vocab-py-codex/261007-coe-SUMMARY.md` when done
</output>
