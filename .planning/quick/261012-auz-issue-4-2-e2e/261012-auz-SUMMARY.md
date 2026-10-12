---
quick_id: 261012-auz
slug: issue-4-2-e2e
status: complete
completed: 2026-10-12
commits: ba13300,3d2e52d,a3bbe22,5505fef,8656539,cb20f97,47f1e97,68ca62a,ae51201,8ff8390
---

# Summary: Issue #4 教材归属与已学进度控制练习范围

## 做了什么

前一会话（Orca #4 worker，2026-10-12 15:29 撞 429/1308 五小时配额中断）
已完成 #4 后端主体共 6 个提交，前端 tab 写到一半。本次按交接文档续起：
逐文件盘点前端半成品（parent-textbook.js 三模态、browse 挂钩、app
wiring、样式、纯函数测试——与已提交 REST 形状逐字段核对无缺口）后落
库；随后按 PLAN Task 7 切分约束补齐三套 e2e（pybridge 真 Python、rt 真
WebSocket、浏览器隔离真服务）；e2e 揪出并修复一个前端真 bug（创建教材
按钮在 form 外的 actions 行、type=submit 无 form 属性＝点击静默无效）；
Task 8 收尾：全量回归、issue #4 七条验收逐条勾选关闭。

## 设计要点（以 261012-auz-PLAN.md 为权威，此处只记新增发现）

- **学生模式掩码**：浏览器裸 WS 探针必须带 `mode: 'test'` 才能看到
  session_ready 全显词表——学生投影把未默词遮成空字符串（13.1-02）。
  首版探针拿到 5 个空串即此因，非选题缺陷。
- **r0x 同秒撞 id**：practice 课次 id 按秒生成（r0x-YYYYMMDD-HHMMSS），
  两个 practice init 落在同一秒会撞同一行、first-content-wins 回放首个
  的选题。探针间隔须 ≥1.1s（浏览器 spec 与 rt e2e 都显式跨秒）。
- **due 抓词带当日 fetch 锁**：rt e2e 里第二个 due 课堂只新选出改进度后
  解锁的词——已抓词当日不重默是正确语义，「立即遵守」的证明形态是
  新解锁词出现且界外词从不出现。
- **browse 勾选跨刷新保留**：批量指定前必须清空残留勾选，否则后批会
  把仍勾选的词一起覆盖指定（UNIQUE(card,textbook) 语义是替换）。

## 验证证据（全部本会话实跑）

- `go test ./...` 全绿；`HTA_E2E_VENV=third_party/python/.venv go test
  -tags=e2e ./pkg/...` 全绿（含新增 pybridge 教材三用例、rt 教材四连
  接用例）。
- `npm test` 全绿（含 test-parent-textbook-form.js）。
- `npm run e2e:parent` 11/11（原 management 5＋photo 4＋新
  parent-textbook-scope 1：两教材不同顺序课名、UI 建教材/批量指定/
  浏览器改进度、三次真课堂 practice 探针 5→7→6 与资格规则逐步吻合、
  参与开关正交）。
- issue #4 七条验收勾选＋Completion note＋关闭（提交清单附于 issue）。

## 提交（feat/test/fix(issue-4) 前缀，中文描述）

- ba13300 feat: 教材实体与已学进度落库（Python 写入者＋选题范围谓词）
- 3d2e52d feat: pybridge 教材写入四方法（Executor 接口＋argv＋预校验）
- a3bbe22 fix: 教材预校验测试补齐 Bridge 必需配置项
- 5505fef fix: 预校验测试补全 Bridge 必需配置（超时/并发/双脚本）
- 8656539 feat: Go 读侧教材仓库与选题范围谓词（practice 路径）
- cb20f97 feat: 教材 REST 路由与加练选题范围接通
- 47f1e97 feat: 家长端教材管理与批量指定前端（模态＋词库挂钩＋纯函数测试）
- 68ca62a test: pybridge 真 Python 教材 e2e 套件（选题范围谓词直证）
- ae51201 test: rt 真 WebSocket 教材范围 e2e（四连接选题遵守范围）
- 8ff8390 test: 浏览器教材范围 e2e＋修复创建按钮 form 关联缺失

## 工作区归属说明

本任务全程未触碰 rt 音频诊断线（pkg/rt 生产码、tutor 四件、
audio_diagnostics 等）与文档线（LEARNING-CONTENT-*、qa-issues 等）的未
提交改动；package.json 仅取走自己的 test 链挂载行（暂存区手术），rt 线
两个挂载仍留在工作区。
