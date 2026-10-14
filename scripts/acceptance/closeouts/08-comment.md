## 实现完成——自动化验收已对齐 AC

验收入口：

```bash
npm run e2e:accept -- 8
```

| AC | 证据 |
|----|------|
| AC-1 材料原文优先 | `tests/e2e/parent-meaning-supplement.spec.js`「fills only the blanks」 |
| AC-2 补充标记与落库来源 | 浏览器 GET `/words` `meaning_source`；`TestE2E_GeneratedMeaningStoredAndRidesSelection` |
| AC-3 教材语境提示词 | `node tests/test-parent-photo-meaning.js`；[ADR-0005](https://github.com/LuCeser/xiaotian/blob/main/docs/adr/0005-generated-meaning-source.md) |
| AC-4 编辑／取消 | 浏览器「parent edit…」；`TestE2E_EditKeepsSource` |
| AC-5 失败诚实＋保留缺失／重试 | 浏览器失败重试场景；`TestE2E_EmptyMeaningNeverMarked` |
| AC-6 自动测试全链 | unit＋browser＋go；另含 `TestE2E_LegacyDBMigratesMeaningSource` |

人工项：无（真实样例难度抽查可选）。可关单。
