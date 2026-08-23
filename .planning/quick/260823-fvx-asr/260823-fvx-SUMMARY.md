---
task_id: 260823-fvx
slug: asr
status: complete
date: 2026-08-23
---

# 快速任务完成摘要：重构小米ASR实现

## 完成概要

✅ 成功重构了 pkg/mimo/ 包的 ASR 实现，使其符合小米官方 OpenAI 兼容 API 规范。

## 关键变更

### 1. ASR 请求结构重构 (`types.go`)

**重构前：**
```go
type ASRRequest struct {
    Model    string     `json:"model"`
    Audio    AudioInput `json:"audio"`
    Language string     `json:"language,omitempty"`
    Options  ASROptions `json:"options,omitempty"`
}
```

**重构后（符合官方规范）：**
```go
type ASRRequest struct {
    Model     string       `json:"model"`
    Messages  []ASRMessage `json:"messages"`
    ExtraBody ASRExtraBody `json:"extra_body,omitempty"`
}

type ASRMessage struct {
    Role    string           `json:"role"` // "user"
    Content []MessageContent `json:"content"`
}

type MessageContent struct {
    Type       string      `json:"type"` // "input_audio"
    InputAudio *AudioInput `json:"input_audio,omitempty"`
}

type ASRExtraBody struct {
    ASROptions ASROptions `json:"asr_options"`
}
```

### 2. 音频编码格式修复 (`client.go`)

**重构前（错误格式）：**
```go
encoded := base64.StdEncoding.EncodeToString(audio)
req.Audio.Data = encoded  // ❌ 缺少 Data URL 前缀
```

**重构后（正确格式）：**
```go
mimeType := c.getMimeType(format)
encoded := base64.StdEncoding.EncodeToString(audio)
dataURL := fmt.Sprintf("data:%s;base64,%s", mimeType, encoded)  // ✅ 正确的 Data URL 格式
```

### 3. API 端点修复 (`client.go`)

**重构前（错误端点）：**
```go
httpReq, err := http.NewRequestWithContext(ctx, "POST", c.config.BaseURL+"/audio/transcriptions", ...)
```

**重构后（正确端点）：**
```go
httpReq, err := http.NewRequestWithContext(ctx, "POST", c.config.BaseURL+"/chat/completions", ...)
```

### 4. 语言参数位置修复

**重构前（错误位置）：**
```go
req.Language = "zh"  // ❌ 直接在请求体中
```

**重构后（正确位置）：**
```go
req.ExtraBody.ASROptions.Language = "zh"  // ✅ 在 asr_options 中
```

### 5. ASR 响应格式更新

**重构后（OpenAI 兼容格式）：**
```go
type ASRResponse struct {
    Choices []ASRChoice `json:"choices"`
    Usage   Usage       `json:"usage,omitempty"`
}

type ASRChoice struct {
    Message ASRMessageContent `json:"message"`
    Index   int               `json:"index"`
}

type ASRMessageContent struct {
    Role    string `json:"role"` // "assistant"
    Content string `json:"content"`
}

// 辅助方法：提取转录文本
func (r *ASRResponse) ExtractText() string {
    if len(r.Choices) == 0 {
        return ""
    }
    return r.Choices[0].Message.Content
}
```

## 文件变更清单

| 文件 | 变更类型 | 描述 |
|------|----------|------|
| `pkg/mimo/types.go` | 重构 | ASR 请求/响应结构重构为 OpenAI 兼容格式 |
| `pkg/mimo/client.go` | 修复 | API 端点、请求构建、音频编码修复 |
| `pkg/mimo/client_test.go` | 更新 | 单元测试适配新结构 |
| `pkg/mimo/integration_test.go` | 更新 | 集成测试适配新响应格式 |

## 测试结果

- ✅ 所有单元测试通过 (`go test ./pkg/mimo/... -count=1`)
- ✅ 编译成功 (`go build ./pkg/mimo/...`)
- ✅ ASR 选项函数测试通过
- ✅ ASR 客户端成功转录测试通过
- ✅ ASR 客户端错误处理测试通过

## 与官方文档的合规性

| 检查项 | 重构前 | 重构后 |
|--------|--------|--------|
| API 端点 | ❌ `/audio/transcriptions` | ✅ `/chat/completions` |
| 请求格式 | ❌ 自定义格式 | ✅ OpenAI 兼容格式 |
| 音频编码 | ❌ 纯 Base64 | ✅ Data URL 格式 |
| 语言参数 | ❌ 直接字段 | ✅ `asr_options.language` |
| 响应解析 | ❌ 旧格式 | ✅ OpenAI choices 格式 |

## 已知限制

1. 当前实现专注于基本 ASR 功能，未包含流式 ASR 支持
2. 未实现音频标签控制等高级风格功能
3. 音色复刻功能未实现（需要 `mimo-v2.5-tts-voiceclone` 模型）

## 下一步建议

1. **启用集成测试**：设置 `MIMO_API_KEY` 环境变量进行真实 API 测试
2. **添加流式 ASR**：根据官方文档添加流式识别支持
3. **实现高级功能**：如需支持音频标签控制或音色设计，可后续扩展
4. **内存监控**：添加 150MB Go 服务限制的内存跟踪

## 验证方法

运行以下命令验证重构成功：

```bash
# 编译测试
go build ./pkg/mimo/...

# 运行单元测试
go test ./pkg/mimo/... -v

# 运行 ASR 相关测试
go test ./pkg/mimo/... -v -run "ASR"

# 运行集成测试（需要 MIMO_API_KEY）
MIMO_API_KEY="your-api-key" go test ./pkg/mimo/... -v -run "TestIntegration"
```

## 完成时间

重构完成于 2026-08-23，所有测试通过，代码符合小米官方 API 规范。
