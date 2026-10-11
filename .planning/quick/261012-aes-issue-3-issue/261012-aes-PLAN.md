---
quick_id: 261012-aes
slug: issue-3-issue
created: 2026-10-12
description: 完成 Issue #3 英语词表照片识别收尾：半成品盘点采纳、回归验证、原子提交、issue 勾选
status: complete
---

# Plan: Issue #3 收尾（照片识别 → 确认 → 投入默写）

## 背景

GitHub Issue #3（英语词表照片识别、确认并投入默写）的实现中盘被 API 配额
（429/1308 五小时上限）中断，工作区遗留大量未提交半成品。上一执行器（Orca
dispatch）已完成：识别管线（pkg/ocr/wordlist.go）、草稿端点
（pkg/web/api/photo_recognize.go）、前端照片导入 tab（parent-photo.js）、
e2e 四用例与 mimo 桩、live 实拍测试。本快速任务负责盘点采纳 + 回归验证 +
原子落库 + issue 收尾。沿用 Plan 09-04/09-06 resume 先例：逐文件核对后
采纳，不推倒重写。

## Tasks

### Task 1: 盘点半成品并全量回归验证

- 逐文件核对 #3 相关改动（后端管线/端点/前端/e2e），确认无缺口。
- `go test ./...` 全绿；`npm test` 全绿；`npm run e2e:parent` 9/9 绿
  （含照片导入 4 用例）；`go test ./pkg/ocr/ -run TestWordlistLive` 实拍
  识别 5/5 命中、零噪声条目。
- files: 无（只读验证）
- verify: 上述四条命令输出
- done: 全部通过（2026-10-12 本会话执行）

### Task 2: 原子提交 #3 改动到 main

- 四个内聚提交（feat(issue-3) 前缀、中文描述）：
  1. pkg/ocr 词表识别管线 + 测试 + errors 哨兵
  2. /words/photo/recognize 端点 + 服务装配（handlers/main/photo）
  3. 家长端照片导入 UI（parent-photo.js + tab 挂载 + 样式）
  4. e2e（spec/fixtures/mimo 桩/隔离配置）+ Node 分类测试 + fixture 生成脚本
- package.json 只提交 test-parent-photo-classify.js 挂载；rt 音频诊断线
  的两个测试挂载与全部 rt/tutor 改动原样留在工作区（另一条线，不属 #3）。
- 明确路径逐文件 git add，绝不用 git add -A。
- verify: git log --oneline 四条 feat(issue-3) 提交；git status 中 #3 文件
  全部落库、非 #3 改动保持未提交
- done: 见 SUMMARY

### Task 3: GitHub Issue #3 收尾

- 逐条勾选验收项（附测试名证据）、写 Completion note（证据 + commit 列表）、
  关闭 issue。
- verify: gh issue view 3 状态 CLOSED、验收项全勾选
- done: 见 SUMMARY

## 验收对照（Issue #3 acceptance criteria）

| 验收项 | 证据 |
|---|---|
| PNG/JPEG 经真实识别服务提取词表；正文词/练习题不作词条 | TestWordlistLive_RealVision（实拍 5/5、噪声零命中）；wordlist_test.go「clean array parses with phrase integrity」等 |
| 预览展示全部词条及原文释义，可编辑勾选；短语含空格仍一条 | e2e「edit → check → commit…」用例；classify 测试 1 |
| 确认前只是草稿；取消不产生课堂内容；首流程不要求例句 | e2e「放弃识别结果 writes nothing」；photo_recognize.go 无任何 DB 写入；表单无 example 字段（提交带 example:''） |
| 提交遵循最终编辑，逐项成功/已有/失败原因及汇总；部分成功不显示全成功 | e2e banner 断言（result-banner-warning、已添加 2/未提交 4） |
| 已有词条不重置历史；不同释义显示冲突保留原值 | e2e conflict 用例（apple 保留词库释义）；classify 测试 3 |
| 识别服务可替换、落定初始提供方与单图限制；失败显示原因可重试；超限明确反馈 | mimo ListReader 接缝；单图限制（photo_recognize.go file comment）；e2e「failure shows the reason and retry succeeds」；413 image_too_large 测试 |
| 浏览器测试只替换外部识别响应，真实写入并启动课堂验证选题；真实照片验证识别 | mimo-stub 只替换视觉服务，写入走真实 REST→Python→SQLite；e2e「classroom: photo-imported words are selected for dictation」；TestWordlistLive_RealVision |
