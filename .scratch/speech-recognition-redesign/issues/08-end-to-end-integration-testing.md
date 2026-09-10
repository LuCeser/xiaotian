# 08 — Complete End-to-End Integration Testing

**What to build:** Full system validation across all components with real student workflows on target platforms, especially iPad Safari. This ensures the complete dictation experience works reliably before production deployment.

**Blocked by:** 07 — Multi-User Session Isolation and Scaling

**Status:** implemented — automated slice done, human UAT items tracked in UAT-CHECKLIST.md (2026-09-11, commits 见 git log 'ticket 08'; review pending)

- [ ] Complete dictation flow works end-to-end on actual iPad Safari devices — 人工 UAT A1-A8（清单见 ../UAT-CHECKLIST.md）；自动化侧 TestE2E_Story_PerWordVoiceFlow + BatchFrameOrder 已钉死流程骨架与帧序
- [ ] Voice command recognition tested with children's voices (ages 6-12) — 人工 UAT B1-B4；既有 live 测试（voice_live_test，真实 Mimo ASR）覆盖成人音，儿童音需真机样本
- [ ] OCR accuracy validated with real elementary student handwriting samples — 人工 UAT C1-C4；既有 photo_live_test 已验证印刷体/清晰笔迹，真实作业样本待采
- [x] Performance benchmarks meet latency (<1.5s) and memory (150MB) requirements — 自动化：TestE2E_Story_BatchFrameOrder 内置 <1.5s 提交→完结护栏；TestLoad_50ConcurrentSessions 堆护栏（1.9MB/50 会话）；真机/VPS 实测归 UAT D1-D6（清单）
- [x] Error scenarios and edge cases handled gracefully with appropriate fallbacks — TestE2E_Story_ErrorScenarios：坏 JSON/未知类型/空转写/提前批量提交 → 错误帧 + 会话存活可继续完成；断连/刷新/词尽/双设备（UAT E1-E5 人工复核）
- [ ] System ready for production deployment with single student — 人工 UAT F1-F4（冒烟 + 备份 + 密钥 + 日志轮转）；代码侧无阻塞项
- [x] Integration tests cover all user stories from specification — TestE2E_Story_*：逐词语音流（含 near_miss/diff 上行）、批量流（batch_result→analysis_result→session_complete 严格帧序）、双学生并发（词组互斥 + 范围解锁包含性断言）、错误场景、判分档位线上语义；外部接缝（FSRS/TTS/流）假件，真实外部服务由既有 live 测试覆盖
- [ ] Real-world usage patterns validated under expected conditions — 人工 UAT（A7/A8 锁屏回流/连场 + E 项）；1C/1G 真机压测为 50 并发自动化测试无法替代的最后一公里

## Completion note

- 自动化切片：`go test ./cmd/service/ -run TestE2E_Story -count=1` 五故事全绿；storiesFSRS 有状态假件按 SelectDueCards 调用发互斥词组（多用户契约内嵌进测试）；会话退出驱动断连（FEEDBACK 不自退场，D-10 链在断连/超时触发——断言解锁包含性前显式 close）
- 人工切片：../UAT-CHECKLIST.md 按验收条目逐项列步（iPad Safari/儿童语音/真实笔迹/VPS 性能/错误恢复/生产就绪），完成后回填本工单勾选
- 范围取舍：语音/OCR 的 live 测试已存在于 voice_live/photo_live（真实 API，MIMO_API_KEY 门控）；本票不重复造，E2E 故事测试用假件保持确定性（live-API 抖动不进 CI）
- 部署提醒：上线前 UAT D（性能）必须在目标 VPS 实测——本地 Windows 的内存/延迟数据不可外推
