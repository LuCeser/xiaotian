## 实现完成——自动化验收已对齐 AC

验收入口（本仓库）：

```bash
npm run e2e:accept -- 6
# 等价：node scripts/acceptance/run-issue.js 6
```

覆盖：

| AC | 证据 |
|----|------|
| AC-1 重复导入幂等 | `tests/e2e/parent-photo-reimport.spec.js` 重传场景；`go test -tags=e2e -run TestE2E_ReimportIsIdempotent ./pkg/pybridge/` |
| AC-2 多来源资格 | 浏览器「同词两本」；`TestE2E_MultiSourceAtLeastOneLearned` |
| AC-3 释义差异确认 | 浏览器「definition diff」；`TestE2E_ConfirmedMeaningRidesSelection` |
| AC-4 移出／加入粘性 | 浏览器同词两本后半；`TestE2E_MultiSourceAtLeastOneLearned` |
| AC-5 身份先定义 | [ADR-0003](https://github.com/LuCeser/xiaotian/blob/main/docs/adr/0003-content-identity-multi-source.md) Accepted，e2e 在迁移后真实库验证 |
| AC-6 四类回执 | 浏览器 `awaitReceipt`／`result-banner`（新增／合并／待处理差异） |
| AC-7 隔离课堂选题 | 浏览器 `practiceSelection`；Go 确认释义进入选题 |

人工项：无。可关单。
