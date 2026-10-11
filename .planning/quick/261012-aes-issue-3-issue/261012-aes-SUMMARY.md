---
quick_id: 261012-aes
slug: issue-3-issue
status: complete
completed: 2026-10-12
commits: 0ab9ef6,0141274,7302f8f,f045edf
---

# Summary: Issue #3 英语词表照片识别收尾

## 做了什么

上一执行器（Orca dispatch，被 429/1308 五小时配额上限中断）遗留的 #3
半成品经逐文件盘点后整体采纳：识别管线、草稿端点、前端照片导入 tab、
e2e 四用例＋mimo 桩、live 实拍测试成色完好，无需修补即全绿。本次补齐
的只有收尾：四个内聚原子提交落库、GitHub Issue #3 勾选＋Completion
note＋关闭、GSD 留痕。

## 验证证据（全部本会话实跑）

- `go test ./...` 全绿（工作区态）；提交后 HEAD 在独立临时 worktree
  再跑 `go build ./...` + `go test ./...` 同样全绿（15 包 ok、0 失败）。
- `npm test` 全绿（含新增 test-parent-photo-classify.js）。
- `npm run e2e:parent` 9/9 通过（原 parent-management 5 条 + 新照片
  导入 4 条：编辑勾选提交真实落库且部分成功显示为部分成功、放弃识别
  结果 API 快照不变、失败显示原因同表单重试成功、真实 /rt session_ready
  选题含照片导入词）。
- `go test ./pkg/ocr/ -run TestWordlistLive -v`：真实 mimo 视觉服务读
  真实渲染词表照片，5/5 词条释义命中、正文/练习噪声零条目。

## 提交（feat/test(issue-3) 前缀，中文描述）

- 0ab9ef6 feat(issue-3): 家长端词表照片识别管线（PNG/JPEG→词条草稿）
- 0141274 feat(issue-3): POST /words/photo/recognize 草稿端点与服务装配
- 7302f8f feat(issue-3): 家长端照片导入 tab（预览核对→编辑勾选→确认添加）
- f045edf test(issue-3): 照片导入 e2e（只替换外部识别）＋Node 分类测试＋fixture 生成

## 边界纪律（混合工作区）

同工作区混有课堂音频诊断线（pkg/rt/*、tutor 三件、rt 测试两件、
debug-asr-device.js、qa-issues 文档等）及规格文档线（docs/LEARNING-
CONTENT-*、AGENTS.md、.planning/PROJECT/ROADMAP、photo-ocr 待办）：
全部原样保留未提交、未改动、未回滚。package.json 仅提交 #3 的
test-parent-photo-classify.js 挂载，rt 线两个测试挂载留在工作区。
全程明确路径逐文件 git add，未用 git add -A/`.`。常驻服务
bin/service.exe（:8080/:8081）未受任何影响；e2e 走既有 18080/18081
隔离基建＋18180 桩。

## Issue 收尾

GitHub Issue #3 验收项逐条勾选（证据映射见 261012-aes-PLAN.md 验收
对照表）、Completion note 附证据与提交列表，验收全部达成后关闭。
