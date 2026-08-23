# Dictation Loop Testing Guide (macOS)

## 预期成果
完整的听写闭环已实现：
```
play_word → audio streaming → word_complete → client plays audio → playback ends 
→ 300ms delay → speech recognition → submit_answer → server compares → answer_result 
→ show feedback (correct 1.2s / incorrect 2.5s) → next_word → repeat...
→ last word → submit_review (batch FSRS update) → session_complete
```

## 测试步骤

### 1. 启动服务
```bash
cd D:\workspace\ai\xiaotian
./service.exe
```
确认服务运行在 `http://localhost:8080/`

### 2. 浏览器测试
打开 `http://localhost:8080/` 并：

1. **基础连接测试**
   - 打开浏览器开发者工具 (Cmd+Option+I)
   - 切换到 Console 标签
   - 检查 WebSocket 连接成功日志

2. **完整听写流程**
   - 点击 "I want to dictate 30 words"
   - 观察完整流程：
     - ✅ 单词通过 TTS 播放
     - ✅ 播放结束后，300ms 延迟
     - ✅ 录音指示器出现（脉冲动画）
     - ✅ 说出答案后，识别结果出现
     - ✅ 正确答案显示1.2秒绿色反馈
     - ✅ 错误答案显示2.5秒红色反馈
     - ✅ 自动进入下一个单词
     - ✅ 最后一个词完成后，提交评审

### 3. 关键验证点

#### A. Category 字段验证
在 Console 中查找 `session_ready` 消息，确认包含：
```javascript
{
  "type": "session_ready",
  "session_id": "...",
  "words": ["word1", "word2", ...],
  "category": "chinese"  // ← 新增字段
}
```

#### B. 语音识别语言验证
- **中文词汇**：应该使用 `zh-CN` 识别
- **英文词汇**：应该使用 `en-US` 识别

检查 Console 日志：
```
[speech] SpeechController initialized with lang: zh-CN
[dictation] Starting dictation for word with category: chinese
```

#### C. 完整流程日志
在 Console 中应该看到类似的日志序列：
```
[audio] _scheduleWordPlayback: word_123
[audio] onPlaybackEnd fired
[dictation] startDictationForWord: word_123 (300ms iOS delay)
[speech] startListening for word_123 with zh-CN
[speech] onResult: heard "你好" for word_123
[dictation] handleAnswerResult: correct=true, expected="你好", heard="你好"
[dictation] Showing feedback: CORRECT (1.2s)
[dictation] sendNextWord after feedback
```

### 4. 预期行为测试

#### 正确答案测试
- 说出正确单词 → 应该看到绿色反馈 → 1.2秒后下一词

#### 错误答案测试  
- 说出错误单词 → 应该看到红色反馈 → 2.5秒后下一词

#### 跳过测试
- 保持沉默8秒 → 应该看到黄色"跳过"反馈 → 自动下一词

#### 完成测试
- 完成最后一个词 → 应该看到 `session_complete` 消息
- 检查 Console 中的 `submit_review` 调用

### 5. 已知问题检查

#### 问题 1: Category 字段缺失
**状态**: ✅ **已修复**
- 验证方法：检查 `session_ready` 中包含 `category` 字段

#### 问题 2: 字符串匹配严格
**状态**: 🔵 **设计如此**
- 拼写错误 = 判定错误（适合小学生听写）
- 中文：仅删除标点/空格
- 英文：小写 + 删除非字母字符

#### 问题 3: 订阅者递归防护
**状态**: ✅ **工作正常**
- 通过 `needsReset` 脏检查防止无限循环

### 6. 调试技巧

#### 如果语音识别不工作
检查浏览器是否支持 `webkitSpeechRecognition`：
```javascript
const recognition = new webkitSpeechRecognition();
console.log('Speech recognition supported:', !!recognition);
```

#### 如果音频不播放
检查 Console 中的 `[audio]` 日志，确认：
- `play_word` 帧已接收
- 音频 URL 正确生成
- `onPlaybackEnd` 事件触发

#### 如果没有反馈显示
检查 Console 中的 `[dictation]` 日志，确认：
- `answer_result` 帧已接收
- `handleAnswerResult` 被调用
- `DictationFeedback` 组件已挂载

### 7. 成功标准

✅ **基础功能**：
- WebSocket 连接成功
- TTS 音频播放流畅
- 语音识别工作正常
- 答案判断正确
- 反馈显示正常
- 自动进入下一词

✅ **Category 修复验证**：
- 中英文词汇使用对应语言识别
- `session_ready` 包含 `category` 字段

✅ **完整流程**：
- 30个词完整听写
- 最终提交 FSRS 评审
- 会话正常结束

## 报告问题

如果发现任何问题，请记录：
1. 问题描述
2. 复现步骤  
3. Console 日志
4. 预期行为 vs 实际行为
5. 测试环境 (macOS 版本, 浏览器版本)

---

**测试完成后，我们可以继续 iPad Safari 的关键测试**