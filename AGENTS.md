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

## Cursor Cloud specific instructions

- 开机时 `start` 在仓库根执行 `./bin/service -config config.yaml`。页面在 `http://127.0.0.1:8080/`，家长页在 `/parent.html`，健康检查在 `http://127.0.0.1:8081/health`。改过 Go 代码后先 `make build`，开机跑的是已编译的 `bin/service`。
- `config.yaml` 由 install 从 `config.example.yaml` 生成，已在 `.gitignore`。`python.vocabScript` 与 `python.pregenScript` 必须写成相对仓库根的脚本路径（`third_party/python/vocab_manager.py`、`third_party/python/pregenerate_dictation.py`）。Python 桥接不会进入 `scriptsDir`，只按该路径启动脚本。
- Go 与 Python 共用 `$HOME/.home-tutor-agent/vocab.db`。解释器是 `third_party/python/.venv/bin/python`（`uv` + Python 3.12）。
- 发行版自带的 Go 1.22 拉不到 `go.mod` 要求的 1.26 工具链。环境使用 `/usr/local/go` 的 Go 1.26.9，`/usr/local/bin/go` 指向它。
- 未设置 `MIMO_API_KEY` 时，页面和词库 API 可用；语音指令、照片识别和 `/rt` 语音家教会降级。
- 已验证的检查：`make vet`、`npm test`、`make build`。CI 的 `make test` 暂不带 `-race`（见 issue #26）；需要竞态检测时用 `make cover`。
