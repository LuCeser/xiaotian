# 08 — Complete End-to-End Integration Testing

**What to build:** Full system validation across all components with real student workflows on target platforms, especially iPad Safari. This ensures the complete dictation experience works reliably before production deployment.

**Blocked by:** 07 — Multi-User Session Isolation and Scaling

**Status:** implemented — automated slice done, human UAT items tracked in UAT-CHECKLIST.md (2026-09-11, commits 见 git log 'ticket 08'; review 修复见 note)

- [ ] Complete dictation flow works end-to-end on actual iPad Safari devices — 人工 UAT A1-A8（清单见 ../UAT-CHECKLIST.md）；自动化侧 TestE2E_Story_PerWordVoiceFlow + BatchFrameOrder 已钉死流程骨架与帧序
- [ ] Voice command recognition tested with children's voices (ages 6-12) — 人工 UAT B1-B4；既有 live 测试（voice_live_test，真实 Mimo ASR）覆盖成人音，儿童音需真机样本
- [ ] OCR accuracy validated with real elementary student handwriting samples — 人工 UAT C1-C4；既有 photo_live_test 已验证印刷体/清晰笔迹，真实作业样本待采
- [ ] Performance benchmarks meet latency (<1.5s) and memory (150MB) requirements — 真正的门槛是 UAT D1-D6（VPS 实测 RSS/延迟），未跑；自动化只提供回归绊线：TestE2E_Story_BatchFrameOrder 的 <1.5s 提交→完结护栏（假件下仅测进程内开销，防同步工作混入）与 TestLoad_50ConcurrentSessions 的 100MB 堆上限（观测 1.9MB/50 会话）
- [ ] Error scenarios and edge cases handled gracefully with appropriate fallbacks — 协议级四场景已自动化（TestE2E_Story_ErrorScenarios：坏 JSON/未知类型/空转写/提前批量提交 → 错误帧 + 会话存活）；边界半边（断连/刷新/词尽/双设备）归 UAT E1-E5，未跑
- [ ] System ready for production deployment with single student — 人工 UAT F1-F4（冒烟 + 备份 + 密钥 + 日志轮转）；代码侧无阻塞项
- [x] Integration tests cover all user stories from specification — 全套件视角：TestE2E_Story_*（逐词语音转写流含 near_miss/diff、批量流严格帧序、双学生并发词组互斥+范围解锁包含性、判分档位线上语义）+ 既有 voice_test/voice_fsm_test（语音命令→FSM）+ photo_test（拍照判分）+ batch/analysis/capacity 套件；外部接缝假件保持确定性，真实外部服务由 MIMO_API_KEY 门控的 live 测试另行覆盖
- [ ] Real-world usage patterns validated under expected conditions — 人工 UAT（A7/A8 锁屏回流/连场 + E 项）；1C/1G 真机压测为 50 并发自动化测试无法替代的最后一公里

## Completion note

- 自动化切片：`go test ./cmd/service/ -run TestE2E_Story -count=1` 五故事全绿；storiesFSRS 有状态假件按 SelectDueCards 调用发互斥词组（多用户契约内嵌进测试）；会话退出驱动断连（FEEDBACK 不自退场，D-10 链在断连/超时触发——断言解锁包含性前显式 close）
- 人工切片：../UAT-CHECKLIST.md 按验收条目逐项列步（iPad Safari/儿童语音/真实笔迹/VPS 性能/错误恢复/生产就绪），完成后回填本工单勾选
- 范围取舍：语音/OCR 的 live 测试已存在于 voice_live/photo_live（真实 API，MIMO_API_KEY 门控）；本票不重复造，E2E 故事测试用假件保持确定性（live-API 抖动不进 CI）
- 部署提醒：上线前 UAT D（性能）必须在目标 VPS 实测——本地 Windows 的内存/延迟数据不可外推
- 双轴 code review（2026-09-11，Standards + Spec 并行子代理，6f3cc6e...HEAD）：Standards 2 硬伤——goroutine 内经 helper 间接 t.Fatalf（FailNow 只杀学生协程、wg.Wait 永挂；双学生故事改为测试协程交错驱动——会话事件循环本就并发，重叠语义不变）；nearMissOf 单字词回退分支违背"保证 near-miss"契约（改为 panic 并注明词池约束）。Spec——复选框语义修正：性能与错误场景两格从 [x] 降回 [ ]（自动化仅是回归绊线/协议级半边，真门槛在 UAT D/E 未跑）；"覆盖所有用户故事"的归属改为全套件视角（语音命令/拍照故事由既有 voice/photo 套件覆盖）；"1.9MB 护栏"措辞更正（护栏实为 100MB，1.9MB 为观测值）。判断项清理：三处 session_ready 解码错误不再吞、vestigial import 卫语句删除。修复后故事测试 + 全套件复测全绿
