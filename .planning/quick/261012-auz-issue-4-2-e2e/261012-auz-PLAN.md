---
quick_id: 261012-auz
slug: issue-4-2-e2e
created: 2026-10-12
description: 实现 Issue #4 教材归属与已学进度控制练习范围（教材实体、批量指定课次、独立进度、选题范围限制、与 #2 正交、e2e 证明）
status: complete
completed: 2026-10-12
---

# Plan: Issue #4 教材归属与已学进度控制练习范围

## 设计决策（GSD 留痕）

**数据模型（Python 唯一写入者，沿用 participates 先例）**

- 新表 `textbooks`：name+category 唯一；描述性元数据 edition/stage/grade/volume/module
  全可选（US-7「不要求填满所有层级」）；`lessons` 为 JSON 数组＝显式课次顺序
  （验收 3「不以字典顺序判断先后」）；`learned_lessons` 整数＝已学完第 N 课
  （含第 N 课，验收 4）；`is_primary`（Change 1「设主教材」，同 category 唯一）。
- 新表 `card_assignments`：UNIQUE(card_id, textbook_id)（同教材一词一课次；
  多来源多课次是 #6 的 US-32，不提前做）；`lesson=''`＝待确认（验收 5，不当
  已学）；`include_in_scope`＝家长明确加入范围（绕过课次进度判断）。
- delete-words 同事务清理 card_assignments（不留悬空引用）。

**资格规则（验收 4/6，与 #2 正交）**

词可被自动选题 iff `participates=1` AND 教材范围内：
`(无任何归属) OR (某归属 include_in_scope=1) OR (某归属课次 ∈ 所属教材前
learned_lessons 课)`。三处执行点：Python cmd_due/cmd_new/cmd_stats 与 Go
CardFilter 新增 ScopeEligibleOnly（practice.go 两处 List 开启）。允许课次集
在查询时计算（Python 解析 JSON / Go repo 加载 textbooks 后拼 IN），不依赖
SQLite JSON1；两侧行政形状一致。旧库无新表时探测降级＝全eligible（#2 先例）。

**接口**

- GET/POST /textbooks、PUT /textbooks/{id}（元数据/课次表/进度/主教材一次
  更新，Python 侧校验 learned_lessons ≤ 课次数）、POST /textbooks/{id}/assign
  （cardIds 1..500 去重，lesson 须在教材课次表内或空＝待确认，category 与词
  匹配，all-or-nothing）、POST /textbooks/{id}/unassign。
- GET /words 每词附 textbooks:[{id,name,lesson,includeInScope}]（浏览端归属
  chip 与待确认提示的数据源）。

**选题立即生效**：due 路径每请求新进程读进度；practice 路径 Go repo 每次查
询实时读——无缓存，天然满足「调整进度后新课堂立即遵守」。

**测试切分（课次缓存约束）**：/rt 课次行 first-content-wins，浏览器 e2e 的
固定 lessonId 已被 photo spec 占用 → 浏览器 spec 用 practice-init（r0x 时间戳
课次 id，天然新鲜）证明真实课堂选题；due 路径用 Go e2e 证明：pybridge e2e
（真 Python RunDue 直证）+ rt e2e（自备课次 id 的真 WS session_init，工厂
按连接轮换 lesson id 证明进度调整后第二个课堂立即遵守）。

## Tasks

1. Python：schema＋textbook-create/update、assign-words/unassign-words＋
   due/new/stats 范围谓词＋delete-words 清理。
2. pybridge：Executor 四方法＋argv＋单测。
3. pkg/database：TextbookRepository（读）＋card_repo ScopeEligibleOnly＋单测
   （字典序陷阱、US-33 多归属、待确认、include、正交）。
4. pkg/web/api：路由＋校验＋wordJSON 归属＋单测（fakeExecutor/fake repo）。
5. pkg/rt：practice/no-due 两处 List 加 ScopeEligibleOnly。
6. 家长 UI：parent-textbook.js（教材管理模态＋批量指定模态）＋browse 挂钩
   （chip、按钮）＋wiring＋样式＋Node 纯函数测试。
7. e2e：pybridge 真 Python 教材套件；rt 真 WS 教材范围套件；浏览器
   parent-textbook-scope.spec.js（两教材不同顺序课名、浏览器改进度、真课堂
   practice 选题证明、参与开关正交）。
8. 收尾：全量回归、issue #4 勾选关闭、SUMMARY/STATE。

## 验收对照

见 gh issue view 4 七条；测试映射实现后回填 SUMMARY。
