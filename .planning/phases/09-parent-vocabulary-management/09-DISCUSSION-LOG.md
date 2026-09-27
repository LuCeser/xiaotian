# Phase 9: Parent Vocabulary Management - Discussion Log

> **Audit trail only.** Do not use as input to planning, research, or execution agents.
> Decisions are captured in CONTEXT.md — this log preserves the alternatives considered.

**Date:** 2026-09-27
**Phase:** 09-parent-vocabulary-management
**Areas discussed:** API 通道与页面入口, 词汇字段与发音缺口, 单元组织与录入流程, FSRS 参数暴露范围

---

## API 通道与页面入口

### Q1: 家长端词汇管理操作(增/删/改/查)走哪条通道与后端通信?

| Option | Description | Selected |
|--------|-------------|----------|
| HTTP REST + fetch(推荐) | CRUD 天然请求/响应,publicMux 挂 /api/v1/* JSON 端点,WS 保留给学生实时会话 | ✓ |
| WebSocket 控制帧 | 复用 ws.MessageType handler map,但 CRUD 走 WS 需自建关联 ID/超时/重试 | |
| 混合:读 HTTP 写 WS | 读走 HTTP GET,写复用 WS;两套通道并存维护成本高 | |

**User's choice:** HTTP REST + fetch(推荐)
**Notes:** —

### Q2: 家长管理页面挂在哪里?

| Option | Description | Selected |
|--------|-------------|----------|
| 同静态目录 /parent.html(推荐) | 放入同一 static 目录,StaticFileServer 直接提供,零新增基础设施 | ✓ |
| 独立 /parent/ 路由 | 新前缀路由指向独立目录,需新增配置与挂载逻辑 | |
| 合并为单页应用 | 学生/家长同一 SPA Tab 切换,学生端误触风险高 | |

**User's choice:** 同静态目录 /parent.html(推荐)
**Notes:** —

### Q3: 家长主要用什么设备访问管理界面?

| Option | Description | Selected |
|--------|-------------|----------|
| iPad 为主 | 与学生端一致,大触控目标单栏布局 | |
| 手机为主 | 碎片时间录入方便但键盘小 | |
| 电脑为主 | 批量文本录入方便 | |
| 响应式全覆盖(推荐) | 手机→平板→桌面,纯 CSS 媒体查询 | ✓ |

**User's choice:** 响应式全覆盖(推荐)
**Notes:** —

### Q4: 学生页与家长页的入口隔离怎么做?

| Option | Description | Selected |
|--------|-------------|----------|
| 学生页无入口(推荐) | 学生页不显示任何链接,家长直接输 URL;零实现成本 | ✓ |
| 学生页有小入口 | 方便找到但孩子可能误进误删 | |
| 加简单 PIN 门 | 防孩子乱改但增加状态管理与配置项 | |

**User's choice:** 学生页无入口(推荐)
**Notes:** 家庭私用 VPS,无鉴权需求。

---

## 词汇字段与发音缺口

### Q1: VOC-01 的 pronunciation(发音)怎么处理?

| Option | Description | Selected |
|--------|-------------|----------|
| 不做发音列(推荐) | TTS 朗读 word 字段即发音,VOC-01 重新解读为已交付的 TTS 能力;零改动 | ✓ |
| 扩展 Python schema | cards 加 pronunciation 列;需 ADR 更新+Go 列单同步 | |
| 并入 meaning 字段 | 约定格式存"释义+音标";格式脆弱 | |

**User's choice:** 不做发音列(推荐)
**Notes:** —

### Q2: 中文词表支持怎么做?(UAT 缺口 C4:现库仅 7 英文词)

| Option | Description | Selected |
|--------|-------------|----------|
| 类型必选(推荐) | 添加时必选 english/chinese,列表按类型筛选;category 参数已存在 | ✓ |
| 默认英语可切换 | 默认英语,高级选项里改;易忘切类型导致 TTS 读错语言 | |
| 暂只做英语 | 缩小范围但 C4 缺口继续悬置 | |

**User's choice:** 类型必选(推荐)
**Notes:** —

### Q3: 单词编辑怎么实现?(vocab_manager.py 无 edit 命令)

| Option | Description | Selected |
|--------|-------------|----------|
| 加 edit 命令(推荐) | Python 新增 edit 子命令按 id 更新 word/meaning/example/unit,保留 fsrs_card | ✓ |
| 删+重加模拟 | 零 Python 改动但 FSRS 学习历史全丢 | |
| 暂不做编辑 | VOC-01 部分交付 | |

**User's choice:** 加 edit 命令(推荐)
**Notes:** —

### Q4: 添加单词时哪些字段必填?

| Option | Description | Selected |
|--------|-------------|----------|
| 仅 word 必填(推荐) | 录入门槛最低,释义例句后续可补;DB 层已支持 | ✓ |
| word+meaning 必填 | 保证反馈阶段能看到词义,录入成本升高 | |
| 全部必填 | 数据最完整但录入负担最重 | |

**User's choice:** 仅 word 必填(推荐)
**Notes:** —

---

## 单元组织与录入流程

### Q1: 单元/课程(VOC-02)怎么组织?

| Option | Description | Selected |
|--------|-------------|----------|
| 扁平安命名约定(推荐) | unit 保持单层字符串(如"三年级上-L10"),与现有 schema/命令完全兼容 | ✓ |
| 两层树(存储仍扁平) | 前端 unit→lesson 树,后端斜杠拼接;需维护拆分/拼接规则 | |
| 真层级表结构 | 新增 units/lessons 表;schema 大改+ADR+多命令改造,收益不成比例 | |

**User's choice:** 扁平安命名约定(推荐)
**Notes:** —

### Q2: 单词录入交互怎么做?(典型:一课 10-20 词批量)

| Option | Description | Selected |
|--------|-------------|----------|
| 逐词+批量双模式(推荐) | 批量粘贴一行一词,解析预览确认后入库(add --words 已支持) | ✓ |
| 仅逐词表单 | 一课提交 10-20 次,体验差 | |
| 仅批量粘贴 | 快但改单词也要走批量流程 | |

**User's choice:** 逐词+批量双模式(推荐)
**Notes:** —

### Q3: 单元组织操作暴露哪些?

| Option | Description | Selected |
|--------|-------------|----------|
| 改属+重命名+删词(推荐) | edit.unit / rename_unit / delete_words;merge_units 不进界面 | ✓ |
| 全部含合并 | 能力最全但合并是危险批量操作,确认流程成本高 | |
| 仅改属+删词 | 单元重命名不做,VOC-02 交付不完整 | |

**User's choice:** 改属+重命名+删词(推荐)
**Notes:** —

### Q4: 词表浏览的默认视图怎么设计?(1C1G VPS,Python 子进程有开销)

| Option | Description | Selected |
|--------|-------------|----------|
| 按单元分组+搜索(推荐) | 可折叠分组+词数(Go 直读 cards 零开销)+搜索+类型筛选 | ✓ |
| 分组+FSRS状态统计 | 信息更全但需 Python stats 增强,每次进页多子进程调用 | |
| 平铺分页 | 实现最简但难按课本单元定位 | |

**User's choice:** 按单元分组+搜索(推荐)
**Notes:** —

---

## FSRS 参数暴露范围

### Q1: VOC-03 的 FSRS 参数配置暴露哪些?(difficulty 是每卡涌现状态,非全局旋钮)

| Option | Description | Selected |
|--------|-------------|----------|
| 保持率+最大间隔(推荐) | desired_retention(0.7-0.95 滑杆)+ maximum_interval;学习步骤不暴露 | ✓ |
| 全部调度参数 | 灵活但家长易调出极端配置,UI 复杂 | |
| 仅保持率 | 最简但少一个实用杠杆 | |

**User's choice:** 保持率+最大间隔(推荐)
**Notes:** 参数变更只影响后续调度,不改已有卡片状态。

### Q2: FSRS 参数存哪里、怎么传给 Python?

| Option | Description | Selected |
|--------|-------------|----------|
| JSON 配置文件(推荐) | fsrs_config.json(缺省=当前硬编码值);Python 启动读入构造 SCHEDULER;无 schema 改动 | ✓ |
| SQLite 配置表+CLI | 新表+config 子命令+ADR 更新,流程重 | |
| 环境变量传递 | 零文件/表但不透明、难查看当前值 | |

**User's choice:** JSON 配置文件(推荐)
**Notes:** 子进程无状态,每次调用读文件即生效,无需重启。

### Q3: FSRS 参数配置的界面形式?

| Option | Description | Selected |
|--------|-------------|----------|
| 内嵌设置卡片(推荐) | 滑杆+推荐区间标注+当前值/默认值对照+恢复默认+通俗文案 | ✓ |
| 独立设置页 | 空间宽裕但为低频功能多一层导航 | |
| 仅配置文件 | 违背 VOC-03 界面配置要求 | |

**User's choice:** 内嵌设置卡片(推荐)
**Notes:** —

---

## the agent's Discretion

- REST 路由命名与 JSON 请求/响应形状(建议 /api/v1/ 前缀)
- HTTP 错误响应格式(结构化错误码 vs 消息)
- 批量粘贴解析细节(分隔符规则、错误行容错/报告)
- edit 子命令 CLI 参数面与 JSON 输出契约
- fsrs_config.json 确切路径与非法内容兜底行为
- 搜索实现(SQL LIKE vs 内存过滤)
- 分组折叠与全部视觉细节(走 /gsd-ui-phase 9)

## Deferred Ideas

- 发音覆盖列(多音字/专有名词)— 未来 ADR 门控的 schema 扩展
- merge_units 进界面 — 已拒绝,必要时可用两次重命名替代
- 每单元 FSRS 状态统计(到期数)— Phase 10 FEED-04 天然归属
- 家长页 PIN/鉴权 — 已拒绝;部署环境变化再议
- 真 unit→lesson 层级模型 — 已拒绝,命名约定承载语义
