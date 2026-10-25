# Issue 与实际交付状态同步

日期：2026-10-15（北京时间）。仓库：LuCeser/xiaotian。

> 历史快照：以下反映 2026-10-15 首次核对时的状态。最新进展见 [2026-10-16 状态同步](ISSUE-STATUS-2026-10-16.md)。

本次核对全部 19 个 GitHub Issues，对照本地代码、远端 main、已接受 ADR、历史验收记录与当前回归。仅更新事项与状态文档，未实施新功能或推送既有代码。

## 当前交付

| Issue | 实际情况 | 本次处理 |
| --- | --- | --- |
| [#1](https://github.com/LuCeser/xiaotian/issues/1) | 英语部分已有实现，语文和结构化诗文尚未完成 | 保持开放；补充子事项状态、交付位置及剩余验收 |
| [#2](https://github.com/LuCeser/xiaotian/issues/2) | 已关闭且有既有人工验收通过记录，但仅勾选 2/6 项并误带 needs-triage | 保持关闭；补齐 6/6 勾选并移除待评估标签 |
| [#3](https://github.com/LuCeser/xiaotian/issues/3)～[#5](https://github.com/LuCeser/xiaotian/issues/5) | 照片导入、教材范围和照片归属已交付并关闭 | 保持关闭，移除 ready-for-agent 标签 |
| [#6](https://github.com/LuCeser/xiaotian/issues/6) | 重复导入、多来源及释义差异已实现，七项验收有对应证据 | 勾选 7/7，关闭为 completed，移除 ready-for-agent |
| [#7](https://github.com/LuCeser/xiaotian/issues/7) | 扫描 PDF 已实现；已有评论明确保留真机/真服务验收 | 勾选前 6 项；AC-7 保持未完成，改为 ready-for-human |
| [#8](https://github.com/LuCeser/xiaotian/issues/8) | 补充释义、来源落库与旧库迁移已实现 | 勾选前 5 项；真实释义质量待验，改为 ready-for-human |
| [#9](https://github.com/LuCeser/xiaotian/issues/9) | 本地按需例句已实现，真实课堂回归通过；实现尚未推送 | 按本地证据勾选前 5 项；明确交付与真实样例/设备验收待办，改为 ready-for-human |
| [#10](https://github.com/LuCeser/xiaotian/issues/10) | 语文照片导入尚未实现；现有照片提示与提交仍固定为英语 | 保持开放与 ready-for-agent；前置 #3 已完成，可开始 |
| [#11](https://github.com/LuCeser/xiaotian/issues/11)～[#15](https://github.com/LuCeser/xiaotian/issues/15) | 古诗、课文段落、范围掌握与独立背诵流程尚未实现 | 保持开放与现有标签，保留依赖关系 |
| [#16](https://github.com/LuCeser/xiaotian/issues/16) | tutor.css 仍有六处 #2f6fed，未解释与家庭强调色的差异 | 保持开放与 ready-for-agent |
| [#17](https://github.com/LuCeser/xiaotian/issues/17) | clear/loading 等重复逻辑仍存在；photo 仍有两处结果横幅构建 | 保持开放与 needs-triage，尚需限定重构范围 |
| [#18](https://github.com/LuCeser/xiaotian/issues/18) | 行内裸复选框仍为 18×18px，未新增扩大点击区域的结构 | 保持开放与 ready-for-agent；未据 CSS 推定实际触控验收通过 |
| [#19](https://github.com/LuCeser/xiaotian/issues/19) | save-chip 仍为 padding 2px 10px、gap 6px，行底部 margin-top 6px | 保持开放；修正规范出处、错误刻度表及属性归属 |

完成同步后为 5 个已关闭、14 个开放。父规格的 14 个子任务中，5 个已关闭、3 个待人工验收或交付、6 个未实现。分诊标签与 GitHub 开闭状态分别表达实施条件和完成情况。

## 交付位置

核对时远端 main 为 `4c40a9da2ec2526547c9aa535603df78b11bf170`，本地 main 为 `e2f9527`。本地比远端多三个提交，均属于 #9：

- `4791478`：按需例句实现与单元测试。
- `892cd4e`：真实 Python/SQLite 与 WebSocket 课堂 e2e。
- `e2f9527`：按需例句领域术语。

#6～#8 的实现及测试提交已经包含在远端 main。#9 的 ADR 已在远端，但上述实现、测试与术语尚未推送；本次仅在 Issue 如实记录。

## 本次验证

- `npm test`：16 个脚本全部通过。
- `go test ./... -skip Live`：全部包通过；明确排除依赖真实外部服务的 Live 测试。
- `npm run e2e:parent`：23 通过、2 跳过；跳过的是默认不开启的 PDF 页图捕获工具用例，不是产品验收场景。
- 定向真实业务 e2e：`go test -tags=e2e ./pkg/pybridge ./pkg/rt -run 'TestE2E_(Participation|Textbook|Reimport|MultiSource|ConfirmedMeaning|GeneratedMeaning|EditKeepsSource|EmptyMeaning|LegacyDBMigratesMeaningSource|ClassroomExample)' -v -count=1`，使用 `third_party/python/.venv/Scripts/python.exe`；11 个 pybridge 用例和 3 个 rt 用例通过，无跳过。

隔离浏览器回归使用独立端口与临时数据库，真实 Go/Python/SQLite；仅外部识别和生成返回固定响应。rt e2e 使用真实 WebSocket、学习业务与隔离库，外部语音模型为固定响应。

初次沙箱运行遇到 Windows 目录恢复/原子文件写入权限失败及 Chromium 启动限制；在正常权限下复跑后上述自动回归通过。初次未排除 Live 的全量运行中，`TestWordlistLive_RealVision` 返回 `invalid API response`，本次未获得成功的真实视觉调用证据，也未将其写成通过。真实 PDF、释义和例句质量以及 iPad Safari 验收继续保留在 #7～#9。

## 后续工作

1. 完成 #7～#9 已明确列出的真实材料、真实服务与设备验收，并记录结果；#9 还需交付三个既有提交。
2. 下一项可实施的内容功能是 #10，再按 #11→#12/#13→#14→#15 的依赖推进。
3. #16～#19 独立处理，不以其完成代替内容功能或产品整体验收。

Phase 13/13.1 的关闭决策、尚未执行的历史人工验收、Phase 13.2 的搁置及后续阶段安排不因本次 Issue 同步改变。
