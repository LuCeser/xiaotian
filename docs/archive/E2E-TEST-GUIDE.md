# 端到端测试指南

## 词汇数据状态 ✅

**7个词汇已添加并可用于测试：**

| 单词 | 含义 | 状态 | 到期时间 |
|------|------|------|----------|
| hello | greeting | Learning | 2026-08-22 16:00 |
| world | earth | Learning | 2026-08-22 16:00 |
| water | liquid | Learning | 2026-08-22 16:00 |
| apple | fruit | Learning | 2026-08-22 16:00 |
| book | reading | Learning | 2026-08-22 16:00 |
| cat | animal | Learning | 2026-08-22 16:00 |
| dog | pet | Learning | 2026-08-22 16:00 |

**数据库位置:** `D:\workspace\ai\xiaotian\.hta-data\vocab.db`  
**单元:** `test`

---

## 测试步骤

### 1. 启动服务器

```bash
# 方法1: 使用已编译的二进制文件
./service.exe

# 方法2: 使用go run
go run ./cmd/service/
```

**预期输出:**
```
{"level":"info","msg":"public server listening","addr":"0.0.0.0:8080"}
```

### 2. 打开浏览器测试

访问: `http://localhost:8080/`

### 3. 点击 "I want to dictate 30 words" 按钮

---

## 预期结果

### ✅ Bug 1 修复验证（连接错误）
- **不应该看到** "connection error" 消息
- 按钮应该显示 "Starting session..." → "Session in progress..."

### ✅ Bug 2 修复验证（多词播放）
- **第一个词**: "hello" 应该播放音频
- **进度更新**: 进度条显示 "1 / 7 words"
- **第二个词**: "world" 应该播放音频  
- **进度更新**: 进度条显示 "2 / 7 words"
- **第三个词**: "water" 应该播放音频
- ...以此类推，所有7个词依次播放

### 🎯 成功标准
- [x] 无连接错误
- [x] 至少播放2个以上单词（不仅限于第一个）
- [x] 进度条正常更新
- [x] 单词按顺序播放，不重复

---

## 故障排除

### 如果仍然看到 "connection error"

1. **检查服务器日志** - 查看是否有 progress_update frame 发送
2. **检查浏览器控制台** - F12 查看WebSocket消息
3. **验证字段名** - 确认后端使用 camelCase (`currentWord` 而非 `current_word`)

### 如果只听到一个词

1. **检查前端代码** - `websocket-client.js:550` 应该使用 `this.stateManager.getState().completedWords`
2. **检查WebSocket消息** - 确认 `next_word` frame 包含正确的 word_id
3. **查看控制台** - 检查是否有JavaScript错误

---

## 手动测试命令

### 验证词汇可用性
```bash
# 查看所有词汇
third_party/python/.venv/Scripts/python.exe third_party/python/vocab_manager.py list

# 查看到期词汇
third_party/python/.venv/Scripts/python.exe third_party/python/vocab_manager.py due

# 查看统计信息
third_party/python/.venv/Scripts/python.exe third_party/python/vocab_manager.py stats
```

### 重置词汇状态（如需要）
```bash
# 解锁test单元
third_party/python/.venv/Scripts/python.exe third_party/python/vocab_manager.py unlock --unit "test"

# 重置为Learning状态
third_party/python/.venv/Scripts/python.exe third_party/python/vocab_manager.py reset --unit "test" --to learning
```

---

## 代码修复验证

### Bug 1 修复位置
`pkg/session/session.go:569-571`
```go
"currentWord": currentWord,      // ✅ camelCase
"completedWords": completedWords, // ✅ camelCase  
"totalWords": totalWords,        // ✅ camelCase
```

### Bug 2 修复位置
`pkg/web/static/js/websocket-client.js:550`
```javascript
// ✅ 从live state读取，而非stale snapshot
var completedNow = this.stateManager.getState().completedWords;
var nextWordIndex = completedNow;
```

---

## 测试完成后

### 清理测试词汇（可选）
```bash
# 删除test单元的所有词汇
third_party/python/.venv/Scripts/python.exe third_party/python/vocab_manager.py delete-words --unit "test"
```

### 恢复到生产数据（可选）
```bash
# 导出当前词汇
third_party/python/.venv/Scripts/python.exe third_party/python/vocab_manager.py export > vocab-backup.json

# 如需恢复
third_party/python/.venv/Scripts/python.exe third_party/python/vocab_manager.py add --file vocab-backup.json
```

---

**准备就绪！现在可以启动服务器进行测试。** 🚀

要测试的关键修复：
1. **连接错误** - 检查后端是否使用 camelCase 字段名
2. **多词播放** - 检查前端是否从 live state 读取 completedWords

祝测试顺利！ ✨
