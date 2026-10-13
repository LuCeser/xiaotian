---
quick_id: 261013-h5c
slug: issue-5-photo-textbook
status: complete
completed: 2026-10-13
commits: a2e9959,f58a95e,9d5da07,e675d7a,dd1940c
issue: "https://github.com/LuCeser/xiaotian/issues/5"
---

# Summary: Issue #5 照片导入自动识别教材与课次并批量校正

## 做了什么

/implement #05 定时任务（10-13 02:00）续跑。前置 #3/#4 已关闭，依赖
解锁。服务端只做一件事：识别管线契约升级为 {textbook,entries}（视觉
提示词读取照片真实印出的教材头，读不出为 null 不编造；词条课次继承
页级；裸数组旧契约兼容解析）。其余全部复用 #2/#4 既有端点：前端提交
编排＝先建/补教材（确认课次集建书；既有书补课次表＋回填空白背景字
段，绝不覆盖）→ 普通 POST /words 加词 → GET /words 快照解析 id →
按（课次,加入范围）分组走 assign。不指定教材时 #3 补充词汇流原样。

## 设计要点

- **诚实预选**：matchTextbookForMeta 按元信息非空字段全等匹配已有书，
  匹配不上绝不猜；识别头无匹配→预选「新建」并预填名称。
- **课次输入是 datalist 自由文本**（非 select）：纠正识别错课时目标
  课次通常不在候选里，select 会把家长困死。
- **失败有序诚实**：教材建立失败→零写入；中途失败→回执点名已建立部
  分并把面板重指向既有书，重试复用不撞重名；指定失败→其余组继续，
  回执列词与原因，词库批量指定为重试路径。
- **e2e 隔离**：每 run 共享库且预选按元信息匹配——各测试用不同教材
  元信息族（人教PEP/冀教/译林）防互劫持；PEP 书跨测试 1/2 有意共享。
- **真 bug（审查重构中揪出）**：字符串数组喂给行对象去重助手，课次
  表被清成 []。拆 distinctLessons（字符串）/distinctRowLessons（行）
  分别导出＋形状钉测。教训：新纯函数必须随做随测。

## 验证证据（本会话实跑）

- go test ./... 15 包全绿；npm test 全过（新增 test-parent-photo-
  textbook.js 28 检查）。
- npm run e2e:parent 14/14（原 11＋新三场景：错误课次批量修正落库、
  两教材进度独立、未学课次排除＋解锁＋重拍预选）。
- 双轴 code-review：Standards 零硬违规（7 条消重已落实）；Spec 三发
  现（中途失败掩盖持久化、既有书不回填背景、追加提示滞留）全修复。
- Issue #5 六项验收勾选＋Completion note＋关闭。

## 未做／边界

- cmd/service 的 TestSubmitAnswers_BatchToFeedback 单跑失败为既有测
  试相互依赖（干净 HEAD 复现，整包跑通过），与本任务无关。
- 真机 iPad Safari 验收与真实识别服务实拍（规格测试边界第三层）待
  用户日常使用回归，同 #3 处理方式。
