# Issue tracker: GitHub

本仓库的规格与实施事项使用 LuCeser/xiaotian 的 GitHub Issues，通过 gh CLI 操作。

- 发布规格：创建 Issue，按技能要求应用标签；回填本地需求记录的远端链接。
- 创建或更新多行正文：先保存 UTF-8 Markdown 文件，再使用 --body-file，保留真实换行。
- 读取事项：使用 gh issue view，包含正文、标签和评论。
- 列出事项：使用 gh issue list，按状态及标签筛选。
- 改标签、评论或关闭事项：使用相应 gh issue 子命令，并遵循当前任务授权范围。
- 远端固定为 LuCeser/xiaotian，命令显式指定 --repo，避免误用其他仓库。
- 未登录或发布失败时保留本地草案，如实报告；不能把未发布草案称为已发布 Issue。

PRs as a request surface: no.

未来使用 wayfinder 时，以单个地图 Issue 关联子事项，依赖使用 GitHub 原生能力；不可用时明确记录关联及阻塞关系，不能隐藏依赖。
