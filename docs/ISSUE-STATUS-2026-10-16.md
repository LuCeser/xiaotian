# PR 与 Issue 状态同步

日期：2026-10-16（北京时间）。本次读取 GitHub PR、Issue 正文/标签/评论、最新 main CI，并核对 origin/main 中的代码与提交祖先关系。仅同步记录，未重跑功能测试或合并 PR。

## PR 当前状态

共 13 个 PR：11 个已合并、2 个开放。合并时间均为北京时间。

| PR | 内容 | 状态 | 合并时间 | PR 最新检查 |
| --- | --- | --- | --- | --- |
| [#20](https://github.com/LuCeser/xiaotian/pull/20) | 记录 Cloud Agent 的本地启动约定 | 已合并 | 10-16 00:11 | SUCCESS |
| [#21](https://github.com/LuCeser/xiaotian/pull/21) | feat(e2e): browser-first full regression and 13.3 family journey coverage | 已合并 | 10-16 02:42 | SUCCESS |
| [#22](https://github.com/LuCeser/xiaotian/pull/22) | test(acceptance): 按 Issue 复验并汇总实际执行证据 | 已合并 | 10-16 04:03 | SUCCESS |
| [#23](https://github.com/LuCeser/xiaotian/pull/23) | ci: 让 golangci-lint 能在 Go 1.26 下加载配置 | 已合并 | 10-15 20:47 | FAILURE |
| [#24](https://github.com/LuCeser/xiaotian/pull/24) | fix: 清掉 golangci-lint v2 在 Go 1.26 下报出的发现 | 已合并 | 10-15 20:47 | SUCCESS |
| [#25](https://github.com/LuCeser/xiaotian/pull/25) | test(issue-2,4,5,6): 补 UAT 缺口 e2e——课次待确认范围、提交时建教材、保留词库释义与失败回滚 | 已合并 | 10-15 20:51 | SUCCESS |
| [#27](https://github.com/LuCeser/xiaotian/pull/27) | fix: 限制取词与锁定时长，并修正会话推送和 Python 超时分类 | 已合并 | 10-16 02:41 | SUCCESS |
| [#28](https://github.com/LuCeser/xiaotian/pull/28) | test: 逐词听写故事等进入待家长点评后再提交 | 已合并 | 10-15 23:50 | SUCCESS |
| [#30](https://github.com/LuCeser/xiaotian/pull/30) | fix(ui): 学习记录 save-chip 间距回归 4 倍数刻度（#19） | 已合并 | 10-16 08:22 | SUCCESS |
| [#31](https://github.com/LuCeser/xiaotian/pull/31) | fix(race): 修复 -race 暴露的存量数据竞态并恢复 CI 竞态检测 | 已合并 | 10-16 08:23 | SUCCESS |
| [#32](https://github.com/LuCeser/xiaotian/pull/32) | fix(ui): 行内复选框触控区提升到 44px（#18） | 已合并 | 10-16 08:24 | SUCCESS |
| [#33](https://github.com/LuCeser/xiaotian/pull/33) | fix(ui): 统一 tutor.css 强调色为全家 --accent，消除双蓝（#16） | 开放草稿 | — | FAILURE |
| [#34](https://github.com/LuCeser/xiaotian/pull/34) | fix(session): 先登记再启动课堂协程 | 开放，待评审/合并 | — | SUCCESS |

#23 的 FAILURE 是该配置 PR 当时的历史检查，后续 #24 等修复已合并。当前 main 为 `19a2f791306339dedd5b5e7af7f5e3fb47af4acc`；其 [CI run 38829282267](https://github.com/LuCeser/xiaotian/actions/runs/38829282267) 成功。已合并 PR #22 不再是草稿或等待 #23，#21 的全量回归入口也已交付。

剩余 PR：#33 是强调色统一，草稿且 CI 失败；日志确认失败于 `TestLoad_50ConcurrentSessions`（#29），而非 lint。#34 是课堂先登记再启动协程的修复，CI 成功、GitHub 显示 CLEAN/MERGEABLE，仍待评审/合并；CI 成功不等于本次已完成代码审查。

## Issue 同步

| Issue | 当前情况 | 本次更新 |
| --- | --- | --- |
| #1 | 父规格开放；内容范围部分完成 | 更新 #9 已交付及 #21/#22 工具已合并，保留人工验收和语文待实施项 |
| #2～#6 | 已完成并关闭 | 保持 |
| #7～#8 | 实现已交付，真实 PDF/释义质量与 iPad 等验收待完成 | 保持开放、ready-for-human |
| #9 | 实现、测试、术语及审查修复均已在 main | 勾选代码交付项；保持开放、ready-for-human，真实例句质量/试听仍待验 |
| #10～#15 | 语文词语、古诗、课文和背诵未交付 | 保持；#10 可开始，后续按已有依赖推进 |
| #16 | 已有 PR #33，未合并 | 回填 PR 与真实 CI 失败原因，保持开放 |
| #17 | UI 工具函数去重待评估 | 保持 needs-triage |
| #18 | PR #32 已合并，原 Issue 已自动关闭 | 按 PR 测试证据补齐 6 项验收，清理 ready-for-agent |
| #19 | PR #30 已合并，原 Issue 已自动关闭 | 补齐 6 项验收与完成记录，清理 ready-for-agent |
| #26 | PR #31 已合并，原 Issue 已关闭 | 补齐 3 项验收；-race 与 CI cgo 已恢复 |
| #29 | 语音故事已修，50 并发负载超时仍在 | 区分 PR #28 已解决部分与负载测试待诊断部分，加入 PR #33 CI 的新证据，标 needs-triage |

共 21 个 Issue：8 个关闭、13 个开放。内容父规格的 14 个子事项仍为 5 个关闭、3 个待人工验收、6 个未交付；工程 PR 合并不改变这些产品验收结论。

## 证据与边界

- #18：PR #32 记录命中区、角落点击、键盘焦点、行高与窄屏的浏览器断言及回退反证；PR CI 和合并后 main CI 成功。
- #19：PR #30 记录 8px margin/gap、4px 12px padding、等量行高变化、对比度与 390px/控制台检查，代码和测试已在 origin/main。
- #26：PR #31 记录两轮全量 -race 无竞态，且 Makefile 的 test 目标和 CI cgo 配置已实际恢复。AGENTS.md 中临时停用 -race 的旧说明已修正为本地执行测试时设置 CGO_ENABLED=1。
- #29：PR #28 记录语音故事修复前 50 次失败 22 次、修复后普通/-race 各 50/50 通过；原“负载抖动”归因被控制帧状态顺序问题取代。负载测试仍在 PR #33 run 38830125711 超时（10.04 秒），继续跟踪。
- #9：`4791478`、`892cd4e`、`e2f9527`、`8123939` 均确认是 origin/main 的祖先。

以上测试数字来自已发布的 PR/CI 证据，本次没有将它们写成本机新测试结果。#7～#9 的真实材料/模型与设备验收保持未完成，历史阶段及部署安排保持原决策。

## 本地工作区

本地 HEAD 为 `bea0b53`，远端 main 为 `19a2f79`：远端独有 22 个提交、本地独有 4 个提交。四个本地提交为既有 AGENTS.md 过时提醒清理及相关规划记录。另有 Makefile/CI、竞态修复等未提交改动。

本次只 fetch 远端引用并编辑状态文档及一条过时 Agent 检查说明；没有 pull、reset、stash 或覆盖这些代码。此前的 [2026-10-15 状态记录](ISSUE-STATUS-2026-10-15.md) 保留为历史快照，当前情况以本记录为准。
