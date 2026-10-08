---
phase: 261007-drn
plan: 01
type: execute
wave: 1
depends_on: []
files_modified:
  - playwright.config.js
  - playwright.rt.config.js
  - Makefile
  - package.json
  - tests/e2e-rt/rt-loop.spec.js
autonomous: true
requirements: ["QUICK-261007-DRN"]

estimate:
  tokens: 30000
  raw_tokens: 20000
  tasks: 2
  confidence: low

must_haves:
  truths:
    - "两个 playwright 配置（playwright.config.js、playwright.rt.config.js）以 git mv 迁入 tests/（历史保留），仓库根不再有任何 playwright 配置；npm 三件套（package.json、package-lock.json、node_modules）原位不动，只改 package.json 两行脚本串"
    - "迁移后配置内路径自洽：testDir 改为相对配置文件的 ./e2e 与 ./e2e-rt（实测 Playwright 1.63.0 相对 testDir 按配置文件所在目录解析）；两个 webServer 各新增 cwd 指回仓库根（实测 webServer.command 默认 cwd = 配置文件所在目录，不指回则 run.ps1 与 mock-rt-server.js 必然找不到），command 字符串本身零改动"
    - "干跑双门全绿（零服务启动、零端口占用、不碰 .hta-data）：npx playwright test -c tests/playwright.config.js --list 恰好 7 tests / 4 files 且输出零 rt-loop；npx playwright test -c tests/playwright.rt.config.js --list 恰好 4 tests / 1 file 且零存量 spec（H-1 目录隔离搬家后复验成立）"
    - "全部活调用方同提交 -c 化并通过 npm 脚本干跑：npm run e2e -- --list 与 npm run e2e:rt -- --list 各自枚举成功；Makefile e2e 目标与 package.json e2e/e2e:rt 中每处 playwright test 均带 -c tests/（裸调用在根目录找不到配置会回落默认配置把两套 spec 混跑——rt 用例打真实 Go 后端，H-1 污染场景复活，grep 门拦截）"
    - "cwd 盲区有直接断言：node -e require 两个配置，断言 webServer.cwd 解析结果 === 仓库根绝对路径（--list 不启动 webServer，cwd 配错时干跑仍全绿、真跑 e2e 才炸——这是 --list 测不到的唯一盲区，必须用加载期断言补上）"
    - "仓库根冻结证据件 server-8443.err.log（12099 字节）与 rt-timing-20261006-202012.jsonl（7538 字节）前后字节数不变、原位、不入库；.planning 历史记录与 .scratch 冻结记录零改动"
  artifacts:
    - "tests/playwright.config.js（git mv 自仓库根 + 编辑）：testDir './e2e'；webServer 新增 cwd: require('node:path').resolve(__dirname, '..')；头注释 L9 调用示例改 npx playwright test -c tests/playwright.config.js；webServer 注释块补半句 cwd 钉在仓库根（run.ps1/.env/logs/server.log 均按仓库根解析）"
    - "tests/playwright.rt.config.js（git mv + 编辑）：testDir './e2e-rt'；webServer 同样新增 cwd 指回仓库根（command 'node tests/e2e-rt/mock-rt-server.js --port 8099' 从仓库根解析，零改动）；L12 示例改 -c tests/playwright.rt.config.js；L9 隔离断言的调用形态改 npx playwright test -c tests/playwright.config.js --list"
    - "Makefile e2e 目标（L49）：npx playwright test → npx playwright test -c tests/playwright.config.js"
    - "package.json：e2e 与 e2e:rt 两行脚本串改带 -c tests/ 前缀路径（npm 脚本 cwd = 仓库根，相对路径成立；test/rt-report 脚本不动）"
    - "tests/e2e-rt/rt-loop.spec.js：仅 L7 注释中裸 npx playwright test --list 引用同步 -c 形态（L4-L5 配置名级引用仍真，不动）"
  key_links:
    - "package.json e2e / e2e:rt 脚本串 ↔ tests/playwright.config.js 与 tests/playwright.rt.config.js（-c 相对路径按 npm 脚本 cwd=仓库根解析）"
    - "Makefile:49 npx playwright test -c ↔ tests/playwright.config.js（make 从仓库根调用，相对路径成立）"
    - "tests/playwright.config.js webServer.cwd（resolve(__dirname,'..')=仓库根）↔ run.ps1（加载 .env、释放 :8080、go run ./cmd/service、日志 tees 到 logs/server.log——全部按仓库根相对解析，cwd 不指回则整套 e2e 真跑必断）"
    - "tests/playwright.rt.config.js webServer.cwd（同上指回仓库根）↔ tests/e2e-rt/mock-rt-server.js（node tests/e2e-rt/... 路径按仓库根解析）"
    - "两配置 testDir（./e2e、./e2e-rt）↔ 配置文件新家 tests/（同目录共居，H-1 目录隔离语义不变：legacy 套件枚举永远不含 rt-loop）"
---

<objective>
playwright 配置归位 tests/：playwright.config.js 与 playwright.rt.config.js 由仓库根 git mv 到 tests/，配置内 testDir 改为相对配置文件的 ./e2e、./e2e-rt，两个 webServer 显式 cwd 指回仓库根（保 run.ps1 与 mock-rt-server.js 调用不断）；全仓 grep 两个配置名的引用逐条分类，活调用方（Makefile、package.json、注释中的调用示例）同提交 -c 化；npx playwright test --list 干跑验证两套配置搬家后都能枚举到测试——不实际跑用例、不启服务、不占端口。

**规划期已实测的 Playwright 1.63.0 语义（沙箱探针，非文档转述）——本计划的所有路径决策以此为准：**
1. `--list` 不启动 webServer（探针：--list 后无 cwd 捕获文件产生）→ 干跑验证安全。
2. 相对 testDir 按配置文件所在目录解析（探针：config 在 sub/、testDir './e2e' 只命中 sub/e2e，根目录诱饵 spec 未命中）。
3. webServer.command 默认 cwd = 配置文件所在目录（探针捕获到 …\sub），不是调用方 cwd → 配置搬进 tests/ 后 `pwsh -NoProfile -File run.ps1` 与 `node tests/e2e-rt/mock-rt-server.js` 都会按 tests/ 找文件、必然失败，必须显式 cwd。
4. 裸 `npx playwright test` 只在 process.cwd() 找配置文件；找不到不报错而是回落默认配置（testDir=cwd + 默认 testMatch 扫全仓）→ 会把 tests/e2e 与 tests/e2e-rt 两套 spec 一起捡进、无 webServer 配置地混跑——全部活调用方必须 -c 化，一个不留。

Purpose: 根目录梳理主题收口——本任务后仓库根只剩 npm 三件套与标准项目文件，两个 playwright 配置与其 testDir 在 tests/ 同居。
Output: 1 个原子提交（git mv 双配置 + 同提交编辑 testDir/webServer.cwd/注释 + 三个调用方文件同步）；干跑双门全绿的证据记录。
</objective>

<execution_context>
@D:/workspace/ai/xiaotian/.claude/gsd-core/workflows/execute-plan.md
@D:/workspace/ai/xiaotian/.claude/gsd-core/templates/summary.md
</execution_context>

<context>
@.planning/STATE.md
@playwright.config.js
@playwright.rt.config.js
@Makefile
@package.json
</context>

<tasks>

<task type="auto">
  <name>Task 1: git mv 双配置入 tests/ 并同提交改齐配置路径与全部活调用方</name>
  <files>playwright.config.js, playwright.rt.config.js, Makefile, package.json, tests/e2e-rt/rt-loop.spec.js</files>
  <action>
  0. **前提自查**（cwd=仓库根）：两配置在根、git status 干净、冻结证据件在位（wc -c server-8443.err.log rt-timing-20261006-202012.jsonl = 12099/7538）。任何一条不满足即停下报告，不动手。
  1. **搬家**（历史保留，绝不用 cp+rm）：`git mv playwright.config.js tests/playwright.config.js` 与 `git mv playwright.rt.config.js tests/playwright.rt.config.js`。
  2. **tests/playwright.config.js 编辑**（用 Edit 工具做局部替换，不整文件重写，保持 LF）：
     - testDir：'tests/e2e' → './e2e'（语义 2：相对配置文件目录，配置与 testDir 同居 tests/）。
     - webServer 块新增一行 `cwd: require('node:path').resolve(__dirname, '..'),`（语义 3：默认 cwd=配置目录，run.ps1 在仓库根；__dirname 绝对解析与 .scratch/pw-real-1312.config.js 的 __dirname 范式一致，不受调用目录影响）；command 'pwsh -NoProfile -File run.ps1' 零改动；url/timeout/reuseExistingServer 不动。
     - 头注释 L9 调用示例：`npx playwright test` → `npx playwright test -c tests/playwright.config.js`（语义 4：裸调用搬家后回落默认配置混跑）。
     - webServer 注释块（L33-41）补一句：cwd 显式钉在仓库根，run.ps1/.env/logs/server.log 均按仓库根解析（d9t 刚改过的 logs/server.log 指引继续为真）。L5-7 对 run.ps1 的描述仍准确，不动。
  3. **tests/playwright.rt.config.js 编辑**（同上局部替换）：
     - testDir：'tests/e2e-rt' → './e2e-rt'。
     - webServer 新增同一行 `cwd: require('node:path').resolve(__dirname, '..'),`；command 'node tests/e2e-rt/mock-rt-server.js --port 8099' 从仓库根解析，零改动。
     - L12 示例：`-c playwright.rt.config.js` → `-c tests/playwright.rt.config.js`。
     - L9 隔离断言的调用形态：`npx playwright test --list` → `npx playwright test -c tests/playwright.config.js --list`（隔离语义不变，只改可跑形态）。
     - L4 "DELIBERATELY SEPARATE from playwright.config.js" 名字级引用仍真（同居 tests/ 仍是两份独立配置），不动。NO_PROXY 守卫块（L25-28）零改动。
  4. **调用方 -c 化**：
     - Makefile L49：`npx playwright test` → `npx playwright test -c tests/playwright.config.js`（L44-47 注释无调用形态，不动）。
     - package.json：`"e2e": "playwright test"` → `"playwright test -c tests/playwright.config.js"`；`"e2e:rt": "playwright test -c playwright.rt.config.js"` → `-c tests/playwright.rt.config.js`。test 与 rt-report 脚本零改动；dependencies/devDependencies 零改动。
     - tests/e2e-rt/rt-loop.spec.js 仅 L7 注释里的裸 `npx playwright test --list` 同步为 `-c tests/playwright.config.js --list`（L4-5 配置名级引用不动）。
  5. **分类清单（规划期全仓 grep 已完成，执行期只复核不重查）**——不动项：.planning/** 全部（硬约束）、.scratch/plan13-verify-paths.json 与 plan13-failing-directions.json 与 e2e-1312/track2/track2-results.json（冻结历史记录）、.scratch/pw-real-1312.config.js L4（名字级仍真）、README.md L95（散文无路径）、.gitignore L44 `test-results/`（无锚点任意深度，已覆盖搬家后 tests/test-results 落点，无需新规则）。.github/workflows 无 playwright 引用。
  </action>
  <verify>
  语法与加载门（此时不提交）：`node --check tests/playwright.config.js && node --check tests/playwright.rt.config.js && node --check tests/e2e-rt/rt-loop.spec.js`；随后 `git diff HEAD --numstat` 复核真实改动（对 HEAD 比对，覆盖暂存 rename 与未暂存编辑两层；CRLF 幻影不进 numstat，出现计划外文件即停下排查）。
  </verify>
  <done>仓库根无两个 playwright 配置、tests/ 下有之且 git status 呈现两个 rename 暂存；testDir 已相对化、两个 webServer 已有 cwd 指回仓库根；Makefile/package.json/rt-loop 注释四处调用形态全部带 -c tests/；三个文件 node --check 通过；numstat 只含本任务 5 个路径。</done>
</task>

<task type="auto">
  <name>Task 2: 干跑双门 + cwd 断言 + 调用方门，全绿后原子提交</name>
  <files>（零新文件；验证与提交 Task 1 的 5 个路径）</files>
  <action>
  全程 DRY-RUN：只 --list 与 node -e，绝不实际跑用例、绝不启动 run.ps1/mock 服务、不占 8080/8099、不碰 .hta-data（语义 1：--list 不启动 webServer）。
  1. **cwd 盲区断言（--list 测不到，必须先做）**：node -e 依次 require ./tests/playwright.config.js 与 ./tests/playwright.rt.config.js，断言各自 testDir 为 './e2e' 与 './e2e-rt'，且 `require('node:path').resolve(cfg.webServer.cwd) === require('node:path').resolve(process.cwd())`（cwd 指回仓库根；cwd 配错时干跑全绿、真跑才断——本断言是唯一防线）。任一不等即停。
  2. **干跑枚举双门**：`npx playwright test -c tests/playwright.config.js --list` → 恰好 7 tests / 4 files（audio-unlock、dictation-flow、ipad-layout、parent-management），输出零 rt-loop；`npx playwright test -c tests/playwright.rt.config.js --list` → 恰好 4 tests / 1 file（rt-loop.spec.js），零存量 spec。计数不符不放过：停下来排查（不得改口验收）。
  3. **调用方门**：`npm run e2e -- --list` 与 `npm run e2e:rt -- --list` 各自枚举成功（直接证明脚本串改对）；grep 门——Makefile 与 package.json 中每处 `playwright test` 均带 `-c tests/`（防裸调用回落默认配置）；若 make 在 PATH 则 `make -n e2e` 干打印复核含 -c tests/playwright.config.js（make 不在则以 grep 结果为准，不装工具）。
  4. **证据件守卫**：`wc -c server-8443.err.log rt-timing-20261006-202012.jsonl` = 12099/7538，与 Task 1 前一致。
  5. **提交**（全绿后一轮做完）：git add 恰好 5 个路径（两配置的 rename 已由 git mv 暂存，连同 Makefile、package.json、tests/e2e-rt/rt-loop.spec.js）；对 `git diff --cached` 做密钥模式扫描（sk-、api_key/api-key、Bearer、secret——本提交全是路径与注释串，预期零命中）；`git status --porcelain` 恰好 5 条：2 条 R（playwright.config.js → tests/playwright.config.js、playwright.rt.config.js → tests/playwright.rt.config.js）+ 3 条 M（Makefile、package.json、tests/e2e-rt/rt-loop.spec.js），多一条即停下排查；若 rename 因相似度显示为 D+A 而非 R，核实内容完整后放行并在 SUMMARY 记录。提交信息：`chore(261007-drn): playwright 配置归位 tests/（git mv 双配置+testDir 相对化+webServer.cwd 指回仓库根+调用方 -c 同步）`，结尾按仓库约定加 Co-Authored-By: Claude Code 行；pre-commit 钩子正常跑（绝不 --no-verify）；提交后 git status --porcelain 为空；绝不 git push。
  </action>
  <verify>
  <automated>node -e "const p=require('path'),r=process.cwd();for(const [f,td] of [['./tests/playwright.config.js','./e2e'],['./tests/playwright.rt.config.js','./e2e-rt']]){const c=require(p.resolve(r,f));if(c.testDir!==td)throw new Error(f+' testDir='+c.testDir);if(p.resolve(c.webServer.cwd)!==r)throw new Error(f+' cwd!=repo root');}console.log('cwd+testDir OK')" && npx playwright test -c tests/playwright.config.js --list | tail -2 && npx playwright test -c tests/playwright.rt.config.js --list | tail -2 && npm run e2e -- --list | tail -2 && npm run e2e:rt -- --list | tail -2 && wc -c server-8443.err.log rt-timing-20261006-202012.jsonl</automated>
  </verify>
  <done>cwd/testDir 断言通过；干跑双门计数精确命中（7/4 与 4/1）且互不串门（H-1 隔离搬家后成立）；npm 两脚本干跑枚举成功；grep 门确认无裸 playwright test 残留于 Makefile/package.json；证据件字节数不变；恰好 5 条 porcelain 记录入库为一个原子提交、工作树净、未 push。</done>
</task>

</tasks>

<threat_model>
## Trust Boundaries

| Boundary | Description |
|----------|-------------|
| 配置文件目录 ↔ webServer 进程 cwd | 配置搬入 tests/ 后，webServer.command 的解析基准从仓库根变为 tests/（除非显式 cwd）——跨目录执行边界 |
| 裸 CLI 调用 ↔ 默认配置回落 | 根目录不再有配置后，任何裸 `npx playwright test` 都回落默认配置并全仓扫描 spec——调用约定边界 |

## STRIDE Threat Register

| Threat ID | Category | Component | Severity | Disposition | Mitigation Plan |
|-----------|----------|-----------|----------|-------------|-----------------|
| T-drn-01 | Tampering | tests/*.config.js webServer.cwd | high | mitigate | cwd 盲区：--list 不启动 webServer，cwd 配错时干跑全绿、真跑 e2e 才断（run.ps1 找不到→webServer 超时）。Task 2 第 1 步 node -e 断言 resolve(cwd)===仓库根，加载期直接证明 |
| T-drn-02 | Tampering | Makefile:49 / package.json e2e,e2e:rt | high | mitigate | 残留裸 `playwright test` 回落默认配置：两套 spec 混跑、rt 用例无 mock 服务打真实 Go 后端（H-1 污染场景复活）。三处调用方同提交 -c 化 + grep 门（Makefile/package.json 每处 playwright test 均带 -c tests/）+ npm run 干跑直接验证脚本串 |
| T-drn-03 | Repudiation | git 暂存区 | medium | mitigate | 提交夹带冻结证据件或 .planning/.scratch 历史记录。porcelain 恰好 5 条（2R+3M）门 + 证据件字节数前后守卫（12099/7538） |
| T-drn-SC | Tampering | npm/pip/cargo installs | high | accept | 本任务零包安装（无 npm install，@playwright/test 1.63.0 已在 node_modules 且为既有依赖）；npm 三件套原地不动，仅改脚本字符串。无新增 [ASSUMED]/[SUS] 包，无阻塞检查点 |
</threat_model>

<verification>
1. 仓库根 `ls playwright.config.js playwright.rt.config.js` 两件均不存在，`git log --follow tests/playwright.config.js` 能追到根时期历史（git mv 生效）。
2. 干跑双门计数精确：7 tests / 4 files（零 rt-loop）与 4 tests / 1 file（零存量 spec）。
3. cwd/testDir 加载期断言通过（--list 唯一测不到的盲区已补）。
4. `npm run e2e -- --list` 与 `npm run e2e:rt -- --list` 通过脚本串枚举成功；Makefile/package.json 无裸 playwright test。
5. 冻结证据件字节数不变；恰好 5 条记录单原子提交；未 push。
</verification>

<success_criteria>
- 两个 playwright 配置以 rename 历史迁入 tests/，仓库根只剩 npm 三件套与标准项目文件（根目录梳理主题就此收口）。
- 两套配置搬家后 --list 干跑均能枚举到测试且互不串门；webServer cwd 指回仓库根有加载期断言背书。
- 全部活调用方（Makefile、package.json×2、三处注释调用示例）同提交 -c 化，无裸调用残留。
- 一个原子提交，工作树净，绝不 push。
</success_criteria>

<output>
Create `.planning/quick/261007-drn-playwright-tests-playwright-config-js-pl/261007-drn-SUMMARY.md` when done
</output>
