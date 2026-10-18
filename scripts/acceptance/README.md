# 按 Issue 运行自动验收

针对 #6（重复导入）、#7（PDF）、#8（补充释义），复用既有隔离浏览器和真实 Python/SQLite 测试：

```sh
npm run e2e:accept:6
npm run e2e:accept:7
npm run e2e:accept:8
node scripts/acceptance/run-issue.js 8 --dry-matrix
```

`--dry-matrix` 只展示 AC 与证据对应表，不运行测试或创建报告。定向入口使用独立端口与临时数据库，不修改 GitHub Issues。

与全量回归（main 的 `npm run regress`，见 [E2E-COVERAGE-MATRIX.md](../../docs/E2E-COVERAGE-MATRIX.md)）互补：regress 是全量门禁，本入口只针对单个 Issue 复验其验收条件并落盘证据。

## 依赖与跨平台使用

需要仓库依赖、Go、已安装的 Playwright Chromium，以及包含 `third_party/python/requirements.txt` 依赖的真实 Python venv。

Python 按以下顺序查找：`HTA_PYTHON_INTERPRETER`、`HTA_E2E_VENV`、仓库 `venv`/`.venv`/`third_party/python/.venv`。同时支持 Windows 的 `Scripts/python.exe` 与 Linux 的 `bin/python`。指定的覆盖路径不存在时明确失败，不静默换解释器或跳过真实业务测试。

隔离 worktree 可以复用已安装的解释器：

```powershell
$env:HTA_PYTHON_INTERPRETER = 'C:\path\to\venv\Scripts\python.exe'
npm run e2e:accept:8
```

```sh
HTA_PYTHON_INTERPRETER=/path/to/venv/bin/python npm run e2e:accept:8
```

Playwright 的 JS CLI 通过当前 Node 运行，不依赖 POSIX `.bin` 脚本。使用该配置的浏览器套件需顺序执行，因为隔离服务使用同一组本机端口。

## 结果与限制

每次结果写入 `.scratch/acceptance/issue-N/<时间>-<进程号>/`，可用 `ACCEPTANCE_LOG_DIR` 指定其他目录。该目录默认被 Git 忽略。

- `summary.json`：本次提交号、工作区是否有修改、执行命令、实际通过/失败/跳过数量、未执行步骤、错误和剩余人工验收。
- 每个步骤的 `.log`：原始 stdout 和 stderr，便于定位缺依赖、服务启动或测试失败。

Go 使用 JSON 测试事件，Playwright 使用 JSON 报告；清单里的必需测试名称逐个核对。零匹配、必要用例被跳过、未完成或失败、报告不可解析、子进程启动失败均返回非零。Go 计数包含实际执行的子测试；Node 单测脚本按脚本数记录，不冒充断言数。失败后不继续启动后续步骤，已执行结果仍落盘。

#8 同时运行前端补充流程单测、服务端教材提示词测试、四项真实 Python 写入测试及浏览器测试。`manifest.json` 记录必需用例和 AC 对应关系；测试重命名时需要同时更新清单，否则入口会报告缺失证据。

自动测试通过只证明清单中的自动验证部分：

- #6 已关闭，仍可用此入口回归。
- #7 等待真实扫描 PDF 的识别质量与 iPad Safari 文件/长预览操作验收。
- #8 等待真实服务生成释义的准确性、教材适配与难度验收。

人工待验不计为自动测试失败，也不会因自动测试通过而变为已完成。Issue 正文、标签和开闭状态由实际验收记录另行维护。
