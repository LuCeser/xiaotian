# 学习内容管理任务清单

日期：2026-10-11。父规格：[GitHub Issue #1](https://github.com/LuCeser/xiaotian/issues/1)。

状态：用户已确认拆分；14 个任务已发布为 GitHub Issues #2–#15，均带 ready-for-agent 标签。正文及 16 条原生阻塞关系已回读验证，父规格 #1 未改动。功能尚未实现。下方列表编号为原拆分序号，依赖均使用实际 Issue 编号。

## 拆分原则

每项交付一个家长或学生可操作验证的流程，包含所需数据、服务、界面和测试。兼容旧内容的必要整理先在任务 01 中完成，不另设大范围基础层重构。古诗、课文将“可导入管理”和“可完成课堂练习”分别交付，未接入课堂的类型不能误进入原有单词流程。

主要先实现照片词表与范围管理，再扩展 PDF、教学辅助和结构化诗文。清单只列直接阻塞，未将所有任务排成串行；这不表示当前已授权启动多个实施 Agent。

## 编号清单

1. **[#2 补充词汇参与开关贯通课堂选词](https://github.com/LuCeser/xiaotian/issues/2)**。依赖：无。家长在现有内容管理页按学科查找词汇，逐条或批量移出／恢复默写范围；学生下一次到期复习和加练都遵守该选择，既有学习历史继续有效。
   [完整验收条件](https://github.com/LuCeser/xiaotian/issues/2)

2. **[#3 英语词表照片识别、确认并投入默写](https://github.com/LuCeser/xiaotian/issues/3)**。依赖：[#2](https://github.com/LuCeser/xiaotian/issues/2)。家长上传一张 PNG 或 JPEG，在预览中核对单词、短语和原文释义，勾选修改后加入补充词汇，随后可用于真实默写。
   [完整验收条件](https://github.com/LuCeser/xiaotian/issues/3)

3. **[#4 教材归属与已学进度控制练习范围](https://github.com/LuCeser/xiaotian/issues/4)**。依赖：[#2](https://github.com/LuCeser/xiaotian/issues/2)。家长创建教材、给已有词汇批量指定课次并设置已学进度；系统自动限制课堂内容，主教材与其他教材各自推进。
   [完整验收条件](https://github.com/LuCeser/xiaotian/issues/4)

4. **[#5 照片导入自动识别教材与课次并批量校正](https://github.com/LuCeser/xiaotian/issues/5)**。依赖：[#3](https://github.com/LuCeser/xiaotian/issues/3)、[#4](https://github.com/LuCeser/xiaotian/issues/4)。家长上传教材词表时，系统自动填写能识别出的教材信息和课次；家长补充必要信息、批量校正后提交，直接按教材进度参与默写。
   [完整验收条件](https://github.com/LuCeser/xiaotian/issues/5)

5. **[#6 重复导入保留多来源并确认释义差异](https://github.com/LuCeser/xiaotian/issues/6)**。依赖：[#5](https://github.com/LuCeser/xiaotian/issues/5)。重复上传同一页或导入其他教材中的相同词时，家长看到合并结果和释义差异；系统保留课次关联与学习历史，并正确判断练习资格。
   [完整验收条件](https://github.com/LuCeser/xiaotian/issues/6)

6. **[#7 多页扫描 PDF 导入与逐页结果确认](https://github.com/LuCeser/xiaotian/issues/7)**。依赖：[#5](https://github.com/LuCeser/xiaotian/issues/5)。家长上传扫描 PDF 后查看各页处理结果，在同一预览中检查词汇与课程归属，批量确认已核对内容，无需拆文件。
   [完整验收条件](https://github.com/LuCeser/xiaotian/issues/7)

7. **[#8 缺失英语释义按教材语境补充并标明来源](https://github.com/LuCeser/xiaotian/issues/8)**。依赖：[#5](https://github.com/LuCeser/xiaotian/issues/5)。教材词表没有释义时，系统补充适合孩子当前教材的意思，在预览中明确标记；家长可修改后提交并用于课堂解释。
   [完整验收条件](https://github.com/LuCeser/xiaotian/issues/8)

8. **[#9 课堂按当前学习进度生成简单例句](https://github.com/LuCeser/xiaotian/issues/9)**。依赖：[#4](https://github.com/LuCeser/xiaotian/issues/4)。孩子在英语课堂请求解释或例句时，小天根据词条的已确认释义、教材和当前进度生成简单例句，随后继续原来的练习。
   [完整验收条件](https://github.com/LuCeser/xiaotian/issues/9)

9. **[#10 语文词语照片导入并完成默写记录](https://github.com/LuCeser/xiaotian/issues/10)**。依赖：[#3](https://github.com/LuCeser/xiaotian/issues/3)。家长从语文材料提取明确词语表，在统一页面确认后，孩子进行语文词语默写，结果回到家长学习记录。
   [完整验收条件](https://github.com/LuCeser/xiaotian/issues/10)

10. **[#11 整首古诗识别、核对与统一内容管理](https://github.com/LuCeser/xiaotian/issues/11)**。依赖：[#4](https://github.com/LuCeser/xiaotian/issues/4)、[#10](https://github.com/LuCeser/xiaotian/issues/10)。家长从混合语文页面识别一首古诗，核对标题、作者和完整原文后提交，在统一内容库中查看、管理课程归属和学习范围。
   [完整验收条件](https://github.com/LuCeser/xiaotian/issues/11)

11. **[#12 整首古诗背默、检查与学习记录](https://github.com/LuCeser/xiaotian/issues/12)**。依赖：[#11](https://github.com/LuCeser/xiaotian/issues/11)。孩子对已开放的一首古诗进行背默，按原文顺序检查并确认结果，家长看到这首诗的练习结果和真实保存状态。
   [完整验收条件](https://github.com/LuCeser/xiaotian/issues/12)

12. **[#13 课文完整导入并选择整篇或指定段落](https://github.com/LuCeser/xiaotian/issues/13)**。依赖：[#11](https://github.com/LuCeser/xiaotian/issues/11)。家长核对完整课文，在同一份原文中选择整篇或指定自然段作为练习范围，之后可调整范围而不用重复导入。
   [完整验收条件](https://github.com/LuCeser/xiaotian/issues/13)

13. **[#14 课文指定段落背默与独立掌握记录](https://github.com/LuCeser/xiaotian/issues/14)**。依赖：[#12](https://github.com/LuCeser/xiaotian/issues/12)、[#13](https://github.com/LuCeser/xiaotian/issues/13)。孩子背默指定段落或全文，检查结果只影响实际练习范围；家长能区分段落掌握与全文掌握，之后无需重新导入即可练全文。
   [完整验收条件](https://github.com/LuCeser/xiaotian/issues/14)

14. **[#15 诗文背诵与背默分开记录](https://github.com/LuCeser/xiaotian/issues/15)**。依赖：[#14](https://github.com/LuCeser/xiaotian/issues/14)。孩子对已开放的整首古诗或课文范围进行口头背诵，获得基于原文的反馈；记录与纸笔背默明确区分，并保持对应内容和范围。
   [完整验收条件](https://github.com/LuCeser/xiaotian/issues/15)

## 依赖说明（使用原拆分序号）

- 01 提供补充参与状态和现有课堂统一范围判断；02 的照片入口与 03 的教材管理可独立开展。
- 04 将照片预览接上教材管理，因而同时依赖 02、03。
- 05、06、07 分别交付来源合并、多页 PDF、补释义，均直接依赖 04；它们共享确认提交能力但不是彼此的前置条件。
- 08 可以利用已有释义生成例句，因此只依赖 03 提供的教材背景，不强制等待 07。
- 09 在 02 的导入流程扩展语文学科，不等待 PDF 或英语生成能力。
- 10 复用 09 的中文导入及 03 的教材进度；11 接整首古诗课堂。
- 12 复用 10 的结构化原文管理，扩展课文段落，不必等待古诗课堂 11。
- 13 同时需要 11 的结构化背默流程和 12 的课文范围；14 再增加独立口头背诵方式。

## 用户故事覆盖（使用原拆分序号）

- US-1 → 02
- US-2 → 06
- US-3 → 02、09
- US-4 → 02、06、09、12
- US-5 → 02
- US-6 → 02、07
- US-7 → 03、04
- US-8 → 03、04
- US-9 → 02、04、06、09
- US-10 → 02、09、10、12
- US-11 → 02、06、09、10、12
- US-12 → 02、04、09、10、12
- US-13 → 03、04、06
- US-14 → 02、06、09、10、12
- US-15 → 07
- US-16 → 02、08
- US-17 → 08
- US-18 → 08
- US-19 → 07、08
- US-20 → 07、08
- US-21 → 03、04、06
- US-22 → 03
- US-23 → 03、11、13
- US-24 → 03
- US-25 → 01、03、11、13
- US-26 → 03、04
- US-27 → 01
- US-28 → 01
- US-29 → 01
- US-30 → 01
- US-31 → 05
- US-32 → 05
- US-33 → 03、05
- US-34 → 01、05
- US-35 → 05
- US-36 → 01、09、10、14
- US-37 → 09、10、12
- US-38 → 10、12
- US-39 → 10、11
- US-40 → 11、14
- US-41 → 12、13
- US-42 → 13、14
- US-43 → 11、13、14
- US-44 → 12、13、14
- US-45 → 02、05、06

## 发布约定

已按依赖顺序逐项创建 GitHub Issue，全部应用 ready-for-agent 标签，正文引用父规格及实际阻塞 Issue。16 条 GitHub 原生阻塞关系已建立并回读核对。父规格 #1 的正文、状态、标签及更新时间经快照比对均未改变；各任务通过 Parent 引用关联规格。当前可立即开始的任务为 #2。

任务 01 的兼容处理使用新增能力与旧接口共存的方式，不预先进行数据库整体重建。具体识别提供方、文件限制、多义词存储、诗文范围调度等技术决策由对应任务在实现前落实，并通过其验收条件验证，不假定已被产品访谈选定。

