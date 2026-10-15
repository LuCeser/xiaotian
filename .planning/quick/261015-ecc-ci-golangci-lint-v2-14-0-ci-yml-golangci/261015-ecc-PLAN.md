---
phase: 261015-ecc
plan: 01
type: execute
wave: 1
depends_on: []
files_modified:
  - .golangci.yml
  - .github/workflows/ci.yml
  - .pre-commit-config.yaml
autonomous: true
requirements: []
estimate:
  tokens: 120000
  raw_tokens: 85000
  tasks: 3
  confidence: low
must_haves:
  truths:
    - 'golangci-lint v2.14.0 官方二进制（built with go1.27.0）在仓库根目录执行 `run --config=.golangci.yml ./...` 退出码 0、零发现——与 CI lint 步骤同构的命令必绿（规划期实测基线：同一二进制对当前主树报 531 项，全部清偿后归零）'
    - 'lint 姿态零缩水：.golangci.yml 保留全部 7 个 linter（errcheck/gosec/govet/ineffassign/misspell/staticcheck/unused）＋ 2 个 formatter（gofmt/goimports）、govet enable-all、max-issues-per-linter: 0、max-same-issues: 0——不删 linter、不设问题上限、不新增消音排除（migrate 物化的 v1 默认排除等价物除外）'
    - 'ci.yml 仅 lint 步骤变更：golangci/golangci-lint-action@v9 ＋ version: v2.14.0（v 前缀精确钉版），args 不变，其余步骤（permissions/checkout/setup-go/make vet/test/build/upload-artifact）逐字节不变；.pre-commit-config.yaml 的 golangci-lint rev 同步钉 v2.14.0'
    - 'CI 同款本地门禁全绿：lint 0 发现 ＋ go vet ./... ＋ CGO_ENABLED=0 go build -o bin/service ./cmd/service ＋ go test -count=1 ./...（make 本机不可用，按 Makefile 底层命令镜像）'
    - '提交卫生：分支上只含本任务刻意文件（.golangci.yml、ci.yml、.pre-commit-config.yaml、lint 修复触及的 *.go）；外来工作区改动（docs/LEARNING-CONTENT-TICKETS.md、.planning/notes/ 等清单见 context F）绝不入栈；.golangci.bck.yml 复核后删除不提交；不 push、不开 PR（归 orchestrator）'
  artifacts:
    - '4 个左右原子提交：迁移配置 1、fmt 格式化 1、存量发现清偿 1~N（按包分批）、CI 钉版 1；主题行按任务规定，均以 Co-Authored-By: Claude Code 结尾'
    - '.planning/quick/261015-ecc-ci-golangci-lint-v2-14-0-ci-yml-golangci/261015-ecc-SUMMARY.md（执行器写盘不提交——docs 提交归 orchestrator）'
  key_links:
    - 'action@v9 ＋ version: v2.14.0 ↔ .golangci.yml version: "2" ↔ .pre-commit-config.yaml rev: v2.14.0——三者必须同一次任务内原子推进：@v6 装出的 v1.64.8 连 v2 配置都无法解析；pre-commit 钉旧版同样会在 go 1.26 模块上复现加载失败'
    - '二进制 built with go1.27.0 ≥ go.mod 的 go 1.26——原失败类别（"构建 golangci-lint 的 Go 版本低于目标 Go 版本"）在 CI（setup-go 1.26）与本机（go1.27.1）双侧消除'
    - 'migrate 物化的排除（presets: comments/common-false-positives/legacy/std-error-handling ＋ paths: third_party$/builtin$/examples$）↔ v1 默认排除的等价物——只复核不增删'
---

# 快速任务：修复 CI——golangci-lint 升级 v2.14.0（action v9 钉版 ＋ .golangci.yml 迁移 v2 ＋ 存量发现清偿归零）

<objective>
CI 自 2026-10-12 起在 lint 步骤红：`.github/workflows/ci.yml` 用 `golangci/golangci-lint-action@v6` ＋ `version: latest`，解析到 v1.64.8（最后 v1，built with go1.24），而 go.mod 目标 `go 1.26`——二进制在加载配置阶段即报 `can't load config: the Go language version (go1.24) used to build golangci-lint is lower than the targeted Go version (1.26)`，`make test`/`make build` 从未跑到。

本任务在 `chore/ci-golangci-lint-v2` 分支（origin/main 的干净分叉）上完成：① ci.yml 升 `golangci-lint-action@v9` 并钉 `version: v2.14.0`；② `.golangci.yml` 用 `golangci-lint migrate` 迁移到 v2 配置格式并人工复核；③ 用 v2.14.0 二进制本地实跑，把 531 项存量发现（规划期已实测普查，见 context D）全数清偿到 `run ./...` 零退出；④ `.pre-commit-config.yaml` 的 golangci-lint rev 同步钉版。本地 CI 同款门禁全绿后收工——push/PR/盯 CI 归 orchestrator，不并入 PR #25。

Purpose: main 的 CI 恢复绿——lint 修复后 make test/build 才能重新为后续所有合入把关。
Output: 4 个左右原子提交（配置迁移、fmt 格式化、发现清偿、CI 钉版）；SUMMARY 写盘不提交。
</objective>

<execution_context>
@D:/workspace/ai/xiaotian/.claude/gsd-core/workflows/execute-plan.md
@D:/workspace/ai/xiaotian/.claude/gsd-core/templates/summary.md
</execution_context>

<context>
@.planning/STATE.md

仓库现状文件（执行器开工前重读一遍即可，勿凭记忆）：
- `.github/workflows/ci.yml`（L32-35 为失败步骤）
- `.golangci.yml`（v1 schema：run.go "1.26" / disable-all / linters-settings / gofmt+goimports 混在 linters 里）
- `.pre-commit-config.yaml`（L13-14：golangci-lint repo，rev: v1.64.8）
- `go.mod`（L3：`go 1.26`）、`Makefile`（lint/vet/build/test 目标——本机无 make，按底层命令镜像）

## 规划期实证（planner 2026-10-15 已逐项做过，执行器不必重考，直接采信）

### A. 配对验证（已核实，勿改结论）
- action 最新 major 为 **v9**（v9.3.0，Node24 运行时）；v7.0.0 起只支持 golangci-lint v2，v8.0.0 起要求 lint ≥ v2.1.0。v2.14.0 满足。`version` 输入接受 v 前缀写法（README 示例 `v2.13`，文档原文 "v2.3, v2.3.4, or latest"）——本任务用完整补丁钉版 `v2.14.0`。`install-mode` 默认 `binary`（保持默认，勿改 goinstall——官方标注 not recommended）。
- v2.14.0 发布于 2026-09-24，官方二进制 `built with go1.27.0`（planner 本机实测 `version` 输出原文），≥ go.mod 的 1.26，原失败类别双侧（CI setup-go 1.26 / 本机 go1.27.1）消除。

### B. 二进制获取与校验（planner 实测通过的序列）
gh 已认证、GitHub 可达。资产名（gh api 实查）：`golangci-lint-2.14.0-windows-amd64.zip` 与 `golangci-lint-2.14.0-checksums.txt`。zip 解压出一层目录 `golangci-lint-2.14.0-windows-amd64/`，内含 `golangci-lint.exe`。工具落 `$HOME/.gsd-tools/`（仓库外、跨 bash 调用稳定——agent 的 bash 环境变量不跨调用持久，只有文件系统持久）：

```bash
mkdir -p "$HOME/.gsd-tools"
gh release download v2.14.0 -R golangci/golangci-lint \
  -p 'golangci-lint-2.14.0-windows-amd64.zip' \
  -p 'golangci-lint-2.14.0-checksums.txt' -D "$HOME/.gsd-tools" --clobber
cd "$HOME/.gsd-tools" && grep 'windows-amd64.zip$' golangci-lint-2.14.0-checksums.txt | sha256sum -c -
unzip -o -q golangci-lint-2.14.0-windows-amd64.zip
"$HOME/.gsd-tools/golangci-lint-2.14.0-windows-amd64/golangci-lint.exe" version
# 预期输出含：golangci-lint has version 2.14.0 built with go1.27.0
```

下文 `$EXE` 一律指 `$HOME/.gsd-tools/golangci-lint-2.14.0-windows-amd64/golangci-lint.exe`（在每条命令里先赋值再使用）。

### C. migrate 实测行为与预期产物（planner 已在临时目录干跑过）
- `migrate` 无参即可：自动发现仓库根 `.golangci.yml`，先按 v1 schema 校验，原地重写为 v2，并在旁边落备份 `.golangci.bck.yml`（该名**不在 .gitignore**，复核后必须删除）。
- 警告原文三条：注释不迁移；`run.timeout` 被忽略（v2 默认关闭超时）；迁移指南 https://golangci-lint.run/docs/product/migration-guide/ 。
- **预期迁移产物（planner 干跑的实际输出，逐行对照；除空白外有偏差即停——说明起点配置与规划期不同）**：

```yaml
version: "2"
run:
  go: "1.26"
output:
  formats:
    text:
      path: stdout
linters:
  default: none
  enable:
    - errcheck
    - gosec
    - govet
    - ineffassign
    - misspell
    - staticcheck
    - unused
  settings:
    govet:
      enable-all: true
  exclusions:
    generated: lax
    presets:
      - comments
      - common-false-positives
      - legacy
      - std-error-handling
    paths:
      - third_party$
      - builtin$
      - examples$
issues:
  max-issues-per-linter: 0
  max-same-issues: 0
formatters:
  enable:
    - gofmt
    - goimports
  settings:
    goimports:
      local-prefixes:
        - github.com/xiaotian/home-tutor-agent
  exclusions:
    generated: lax
    paths:
      - third_party$
      - builtin$
      - examples$
```

要点：`run.go: "1.26"` **保留**（v2 仍认）；`disable-all` → `default: none`；gofmt/goimports 拆到顶层 `formatters`（goimports 的 local-prefixes 随迁）；`linters-settings` → `linters.settings`；v1 默认排除被物化为 `exclusions`（presets ＋ 三个 path 正则）——这是 v1 行为的等价物，只复核、不增删；输出格式 `colored-line-number` 变为 `text`（CI 日志少颜色，预期内）。

### D. 存量发现普查（planner 用该二进制 ＋ 迁移后配置对主树实跑的完整输出统计）
```
531 issues:
* errcheck: 27   * gofmt: 44   * goimports: 21   * gosec: 63
* govet: 339     * ineffassign: 1   * misspell: 9
* staticcheck: 15   * unused: 12
govet 明细：fieldalignment 239 ｜ shadow ~45（91 行含 related 声明行）｜ unusedwrite 7 ｜ nilness 2 ｜ 其余零星
gosec 明细：G115 38 ｜ G306 7 ｜ G404 6 ｜ G118 4 ｜ G202 2 ｜ G201 2 ｜ G402/G204/G122/G107 各 1
```
其中 gofmt 44 ＋ goimports 21 ＝ 65 项可由 `$EXE fmt` 一键清偿；其余 ~466 项为真实修复工作量。并行会话同口径清理曾触及约 95 个文件（见 F）——按包分批推进、分批提交。v1.64.8 的 `allAnalyzers`（enable-all 所用注册表，v1.64.8 源码 pkg/golinters/govet/govet.go L62-77 实查含 fieldalignment）确认：这些发现是 lint 失明窗口期攒下的债，**不是 v2 新姿态加码**——不存在"借升级关掉分析器"的正当理由。

### E. 本机工具链事实（planner 实查）
- `make` **不在 PATH**——所有验证命令用底层 go 命令镜像 Makefile 目标（vet=`go vet ./...`；build=`CGO_ENABLED=0 go build -ldflags '-s -w' -o bin/service ./cmd/service`，bin/ 已 gitignore；test 的 CI 形态含 -race，本地门禁用 `go test -count=1 ./...`）。
- `go version go1.27.1 windows/amd64`；`sha256sum`/`unzip`/`curl`/`gh`（Git Bash）可用；本机无任何已装的 golangci-lint。
- worktree 无 .env 密钥（gitignored），live/real-API 测试与 CI 同条件自跳过——测试输出里的大面积 skip 属预期，不是缺陷。

### F. 并行会话边界与提交卫生（MUTABLE-SCOPE：外来项以执行时 git status 实查为准）
- **已存在两个开着的平行 PR**（另一 cursor 会话）：#23 `ci: 让 golangci-lint 能在 Go 1.26 下加载配置`、#24 叠在其上清 v2 发现。其中 0147581 拥有 "`make test` -race 与 job 级 CGO_ENABLED=0" 的 CI 缺陷修复——**该缺陷不在本任务范围**，本任务不改 make test 步骤；合并顺序冲突由 orchestrator/用户裁决。禁止 fetch/摘 cherry-pick 平行分支的提交——独立从 origin/main 谱系重做（用户拍板的独立 PR 结构）。ci.yml 钉版步骤的注释措辞刻意与平行提交保持一致，降低最终合并冲突面。
- 外来工作区资产**绝不暂存、绝不吸收**：`docs/LEARNING-CONTENT-TICKETS.md`（已修改）、未跟踪 `.planning/notes/`、`.planning/phases/13.3-parent-tutor-fusion/.review-diagnostics/`、`.planning/ui-reviews/`、`docs/ISSUE-STATUS-2026-10-15.md`、`docs/agents/`、`logs/`。暂存一律显式按路径 `git add <file>...`，禁用 `git add -A` / `git add .`。
- 分支纪律：留在 harness worktree（forked from origin/main），不 push、不开 PR——归 orchestrator。
</context>

<tasks>

<task type="auto">
  <name>Task 1: 落地 v2.14.0 二进制＋migrate 迁移 .golangci.yml＋fmt 全量格式化</name>
  <files>.golangci.yml；`$EXE fmt` 触及的 *.go（批量格式化，以 git status 实查清单为准）</files>
  <action>
  1. 按 context B 的序列把二进制落进 `$HOME/.gsd-tools/` 并过 sha256 校验，`$EXE version` 必须打出 2.14.0 built with go1.27.0。
  2. 仓库根运行 `$EXE migrate`。用 `git diff .golangci.yml` 逐行对照 context C 的预期产物（除空白外一致才算过）。随后删除 `.golangci.bck.yml`（未 gitignore，不得滞留工作区）。
  3. 基线普查留证：`$EXE run ./... 2>&1 | tail -12`，与 context D 的 531 分布核对（±15% 以内视为同基线；大幅偏离说明树动了，记录进 SUMMARY 再继续）。
  4. `$EXE fmt` 一键清偿 gofmt/goimports 共 ~65 项。`git diff --stat` 复核：只允许空白/换行/导入序变化——出现任何语义形态的 hunk 立即停下细看，不许盲提。
  5. `go build ./...` 编译 sanity。
  6. 两个原子提交（显式按路径暂存，主题行照抄，均以 Co-Authored-By: Claude Code 结尾）：
     a. `ci(lint): .golangci.yml 迁移到 golangci-lint v2 配置格式`——只含 .golangci.yml；
     b. `style(lint): golangci-lint v2 fmt 全量格式化（gofmt/goimports）`——恰为 fmt 触及的文件清单。
  </action>
  <verify>
    <automated>EXE="$HOME/.gsd-tools/golangci-lint-2.14.0-windows-amd64/golangci-lint.exe"; grep -c '^version: "2"$' .golangci.yml; grep -c '^formatters:$' .golangci.yml; grep -c '^  default: none$' .golangci.yml; "$EXE" fmt; git diff --stat --quiet && echo FMT-IDEMPOTENT; "$EXE" run ./... > "$HOME/.gsd-tools/lint-after-fmt.txt" 2>&1; tail -12 "$HOME/.gsd-tools/lint-after-fmt.txt"</automated>
  </verify>
  <done>三个 grep 各输出 1；FMT-IDEMPOTENT 打印（fmt 无残留 diff，格式化已全部提交）；`run` 的普查尾部已无 gofmt/goimports 行、余 ~466 项；两个提交落库且工作区无未提交的 fmt 残渣。</done>
</task>

<task type="auto">
  <name>Task 2: 清偿全部存量发现直至 run ./... 零退出</name>
  <files>按实跑输出授权（live scope）：$EXE 报告涉及的 *.go——以 Task 1 记录的基线为准（约 12 个包；并行会话同口径曾触 ~95 文件）</files>
  <action>
  1. 按包推进：`$EXE run ./pkg/<name>/...` → 修完该包 → `go build ./... && go test -count=1 ./pkg/<name>/...` → 下一包。分批原子提交，主题 `fix(lint)(<pkg>): 清偿 golangci-lint v2.14.0 存量发现`（最后收尾若有余量，可并一个总结提交 `fix(lint): 清偿 golangci-lint v2.14.0 存量发现（531→0）`）。
  2. 分类手册（对应 context D 明细）：
     - fieldalignment（239）：按分析器建议序重排 struct 字段。纯布局改动，每改完一个文件/包立即编译；无键复合字面量会编译报错——补字段键，不回退重排。
     - shadow（~45）：改内层声明名（最小 diff），测试文件居多；不扩大作用域。
     - gosec G115（38，整型转换溢出）：优先显式边界检查/加宽转换；确有不变量兜底的转换保留并加行级 `//nolint:gosec // <一行不变量依据>`。
     - G404（6，math/rand）：非加密用途（jitter/洗牌）加 `//nolint:gosec // 非加密用途`；不盲换 crypto/rand（VPS 内存约束）。
     - G306/G118/G202/G201/G402/G204/G122/G107（各 1-7）：收紧文件权限/关闭模式/SQL 参数化/TLS MinVersion 等，逐条按 gosec 语义真修。
     - errcheck（27）：真处理；确可忽略的显式 `_ =` ＋ 注释说明。
     - staticcheck（15）：ST1005 去掉错误串尾部标点；QF1002 改 tagged switch；其余按报告语义。
     - unused（12）：删除点名的死 const/func/method；与 STATE 在册待办（ConnectionRegistry 死代码等）重叠时只删 lint 点名项，不扩大范围。
     - misspell（9）/ ineffassign（1）：逐条照报修。
  3. 硬规则（姿态零缩水）：不从 enable 列表删 linter/formatter；不动 max-issues-per-linter/max-same-issues（保持 0/0）；不新增 exclusions paths/presets/rules 消音；`//nolint` 必须行级＋具名 linter＋理由；不动 go.mod 依赖。
  4. 收口三门禁：`$EXE run ./...` 退出码 0；`go vet ./...`；`go test -count=1 ./...`（大面积 skip 属预期，见 context E）。
  5. 暂存恰为触达文件，外来资产清单（context F）零吸收。
  </action>
  <verify>
    <automated>EXE="$HOME/.gsd-tools/golangci-lint-2.14.0-windows-amd64/golangci-lint.exe"; "$EXE" run ./... && echo LINT-GREEN; go vet ./... && echo VET-GREEN</automated>
  </verify>
  <done>LINT-GREEN 与 VET-GREEN 均打印（run 零发现零退出）；`go test -count=1 ./...` 全绿（skip 不算失败）；分批提交落库、暂存清单干净。</done>
</task>

<task type="auto">
  <name>Task 3: ci.yml 升 action@v9＋钉版 v2.14.0（含 .pre-commit rev），CI 同款本地门禁全绿</name>
  <files>.github/workflows/ci.yml, .pre-commit-config.yaml</files>
  <action>
  1. ci.yml 只改 lint 步骤（其余逐字节不动：permissions、checkout、setup-go "1.26"、GOFLAGS/CGO 环境块、make vet/test/build、upload-artifact）。目标形态（注释措辞与平行提交保持一致以降低合并冲突面）：

```yaml
      # Action v6 treats "latest" as the newest v1 release. That binary is
      # v1.64.8, built with go1.24, and it refuses a module whose go
      # directive is 1.26. v9 installs golangci-lint v2; pin a release
      # whose official binary is built with Go >= 1.26 (v2.14.0: go1.27).
      - uses: golangci/golangci-lint-action@v9
        with:
          version: v2.14.0
          args: --config=.golangci.yml
```

     `make test` 的 -race 与 CGO 交互问题归平行 PR #23 所有——本任务不碰该步骤。
  2. `.pre-commit-config.yaml`：golangci-lint repo 的 `rev: v1.64.8` → `rev: v2.14.0`，其余不动。
  3. CI 同款本地门禁（make 不可用，按 Makefile 底层命令镜像，见 context E）：`$EXE run --config=.golangci.yml ./...`（与 action 默认调用同构）；`go vet ./...`；`CGO_ENABLED=0 go build -ldflags '-s -w' -o bin/service ./cmd/service`；`go test -count=1 ./...`。
  4. 单提交：`ci(lint): golangci-lint-action 升 v9 并钉住 golangci-lint v2.14.0`——恰含两个文件，Co-Authored-By: Claude Code 结尾。
  </action>
  <verify>
    <automated>EXE="$HOME/.gsd-tools/golangci-lint-2.14.0-windows-amd64/golangci-lint.exe"; grep -n 'golangci-lint-action@v9' .github/workflows/ci.yml && grep -n 'version: v2.14.0' .github/workflows/ci.yml && grep -n 'rev: v2.14.0' .pre-commit-config.yaml && "$EXE" run --config=.golangci.yml ./... && go vet ./... && CGO_ENABLED=0 go build -ldflags '-s -w' -o bin/service ./cmd/service && go test -count=1 ./... && echo CI-GATE-GREEN</automated>
  </verify>
  <done>三个 grep 命中钉版点；CI-GATE-GREEN 打印（lint/vet/build/test 四门禁与 CI 步骤同构全绿）；恰一个提交含恰两个文件。</done>
</task>

</tasks>

<threat_model>
## Trust Boundaries

| Boundary | Description |
|----------|-------------|
| GitHub Releases → 本机工具链 | golangci-lint v2.14.0 官方二进制经下载进入开发机并被执行（lint 分析读取全部源码） |
| workflow 编辑面 | ci.yml 变更可能改变 CI 触发面/权限面 |
| 批量机械改动 | ~466 项发现涉及 ~95 文件的 fieldalignment/shadow/转换修复，可能引入语义回归 |

## STRIDE Threat Register

| Threat ID | Category | Component | Severity | Disposition | Mitigation Plan |
|-----------|----------|-----------|----------|-------------|-----------------|
| T-261015-01 | Tampering | 下载二进制（供应链） | high | mitigate | 只经已认证 gh 从官方 golangci/golangci-lint Releases 拉取，`grep 'windows-amd64.zip$' checksums \| sha256sum -c -` 强校验通过才执行；工具落仓库外 `$HOME/.gsd-tools/`，不进提交 |
| T-261015-02 | Tampering / Elevation of Privilege | .github/workflows/ci.yml 编辑 | medium | mitigate | 只许改 lint 步骤两行＋注释；verify 门禁 grep 钉住 @v9 与 v2.14.0；permissions/on 触发面零改动（Task 3 action 明令"其余逐字节不动"） |
| T-261015-03 | Tampering（语义回归） | fieldalignment/shadow/转换批量修复 | medium | mitigate | 每包修后即 `go build`＋`go test -count=1 ./pkg/...`；fmt 改动限空白/导入序（Task 1 第 4 步语义 diff 检查）；收口跑全量 test ＋ vet |
| T-261015-SC | Tampering | 包管理器安装 | high | mitigate | 本计划零 npm/pip/cargo 安装、零 go.mod 依赖变更；唯一外部制品为 lint 二进制，走 T-261015-01 的来源＋校验和门（legitimacy 检查点仅适用包管理器渠道） |
</threat_model>

<verification>
- 三任务各自 <automated> 门禁全绿（Task 1 形态断言＋fmt 幂等；Task 2 LINT-GREEN/VET-GREEN；Task 3 CI-GATE-GREEN 四连）。
- `git log --oneline origin/main..HEAD` 核对提交序列与主题行；`git status --porcelain` 确认外来资产（context F 清单）原样在工作区、零吸收、`.golangci.bck.yml` 不存在。
- SUMMARY 落盘：含 531→0 的分类清偿账、实跑证据（命令＋输出要点）、以及"make test -race/CGO 缺陷归 PR #23，合并顺序需 orchestrator 裁决"的交接说明。
</verification>

<success_criteria>
- v2.14.0 二进制对迁移后配置在仓库根 `run ./...` 零退出（CI lint 步骤同构命令必绿）。
- lint 姿态零缩水：9 个 linter/formatter 全保留、govet enable-all、问题上限 0/0、无新增消音排除。
- ci.yml action@v9 ＋ version: v2.14.0、.pre-commit rev: v2.14.0，其余步骤零改动。
- CI 同款本地门禁（lint/vet/build/test）全绿；提交原子、暂存干净、平行会话资产零吸收。
</success_criteria>

<output>
Create `.planning/quick/261015-ecc-ci-golangci-lint-v2-14-0-ci-yml-golangci/261015-ecc-SUMMARY.md` when done
</output>
