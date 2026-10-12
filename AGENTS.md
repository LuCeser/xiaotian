# 英文技术隐喻直译

触发条件：
中文表达中出现明显来自英文技术写作的空间、道路、建筑或物理隐喻，
且该表达在中文工程交流中不自然。

典型但非穷举：
- seam → 接缝
- lane → 车道
- track → 轨道
- surface → 表面积
- blast radius → 爆炸半径
- wiring → 布线
- escape hatch → 逃生口
- happy path → 快乐路径

处理规则：

1. 不进行简单词语替换。
2. 判断隐喻实际表达的工程关系：
   - 模块边界
   - 替换点
   - 数据流
   - 工作流
   - 并行任务
   - 依赖关系
   - 影响范围
   - 限制条件
3. 优先直接表达该工程关系。
4. 若中文技术社区已普遍接受该术语，则保留。
5. 不改变技术含义，不增加解释。

## Agent skills

### Issue tracker

规格和实施事项记录在 LuCeser/xiaotian 的 GitHub Issues。见 docs/agents/issue-tracker.md。

### Triage labels

使用五种标准分诊标签。见 docs/agents/triage-labels.md。

### Domain docs

使用根目录 CONTEXT.md 和 docs/adr/ 的单领域布局。见 docs/agents/domain.md。
