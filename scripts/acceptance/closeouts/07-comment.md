## 实现完成——自动化验收已对齐 AC（人工项仍开放）

验收入口：

```bash
npm run e2e:accept -- 7
```

| AC | 证据 |
|----|------|
| AC-1～AC-2 按页预览／不乱序不重复 | `tests/e2e/parent-pdf-import.spec.js`「整本跨课次」 |
| AC-3 状态与超限反馈 | 「部分页失败」「limits」两场景 |
| AC-4 失败页保留并重试 | 「部分页失败与重试」 |
| AC-5 受控批量 | 同上＋[ADR-0004](https://github.com/LuCeser/xiaotian/blob/main/docs/adr/0004-pdf-client-side-page-split.md)（40MB／100 页） |
| AC-6 教材背景一次确认＋按页批量课次 | 「整本跨课次」 |
| AC-7 前半（入库＋课堂范围） | 「整本跨课次」`practiceSelection`；**后半人工见下** |

以下两项需要真机／真服务，完成时勾选 AC-7：

- [ ] **iPad Safari 实机检查**：选文件、长列表预览、本页批量课次、提交。材料：`tests/e2e/fixtures/pdf-textbook.pdf`
- [ ] **真实识别服务验收**：同一 PDF 走真实 MiMo 视觉（需 `MIMO_API_KEY`），核对漏识别、释义、页归属

建议标签：去掉 `ready-for-agent`，改 `ready-for-human`；issue 保持 OPEN直至人工项勾完。
