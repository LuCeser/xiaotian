# 快速任务：重构小米ASR实现

## 概述

根据小米官方文档分析，当前 pkg/mimo/ 包的 ASR 实现存在严重的API兼容性问题，需要重构以符合官方规范。

## 问题分析

### 当前实现的问题
1. **API 端点错误**：使用 `/audio/transcriptions` 而不是 `/chat/completions`
2. **请求格式错误**：不兼容 OpenAI 格式
3. **音频编码错误**：缺少 Data URL 前缀格式
4. **参数位置错误**：language 参数应该在 `asr_options` 中

### 影响范围
- `pkg/mimo/client.go` - ASR 请求构建和执行
- `pkg/mimo/types.go` - ASR 请求结构体
- `pkg/mimo/integration_test.go` - ASR 集成测试

## 执行计划

### 任务 1: 重构 ASR 请求结构体
**文件**: `pkg/mimo/types.go`

**操作**:
- 重构 `ASRRequest` 结构体以符合官方 OpenAI 兼容格式
- 添加新的消息结构体支持
- 更新音频输入格式为 Data URL 格式

**详细变更**:
```go
// 新增消息内容结构
type MessageContent struct {
    Type       string     `json:"type"` // "input_audio"
    InputAudio *AudioInput `json:"input_audio,omitempty"`
}

// 重构后的消息结构
type Message struct {
    Role    string          `json:"role"` // "user"
    Content []MessageContent `json:"content"`
}

// 重构后的 ASR 请求
type ASRRequest struct {
    Model     string             `json:"model"` // "mimo-v2.5-asr"
    Messages  []Message          `json:"messages"`
    ExtraBody ASRExtraBody       `json:"extra_body,omitempty"`
}

// 新增 extra_body 结构
type ASRExtraBody struct {
    ASROptions ASROptions `json:"asr_options"`
}

// 更新音频输入结构，支持 Data URL
type AudioInput struct {
    Data string `json:"data"` // "data:audio/wav;base64,xxx"
}
```

**验证**:
- 编译通过
- 新结构体符合官方文档格式

### 任务 2: 重构 ASR 请求构建逻辑
**文件**: `pkg/mimo/client.go`

**操作**:
- 更新 `buildASRRequest` 方法以使用新结构
- 修改 API 端点从 `/audio/transcriptions` 到 `/chat/completions`
- 实现正确的 Data URL 音频编码

**详细变更**:
```go
func (c *mimoClient) buildASRRequest(audio []byte, format string, opts ...ASROption) ASRRequest {
    // 确定 MIME 类型
    mimeType := c.getMimeType(format)
    
    // 编码为 Data URL 格式
    encoded := base64.StdEncoding.EncodeToString(audio)
    dataURL := fmt.Sprintf("data:%s;base64,%s", mimeType, encoded)
    
    req := ASRRequest{
        Model: "mimo-v2.5-asr",
        Messages: []Message{
            {
                Role: "user",
                Content: []MessageContent{
                    {
                        Type: "input_audio",
                        InputAudio: &AudioInput{
                            Data: dataURL,
                        },
                    },
                },
            },
        },
    }
    
    // 应用选项到 extra_body.asr_options
    for _, opt := range opts {
        opt(&req)
    }
    
    return req
}

// 新增辅助方法
func (c *mimoClient) getMimeType(format string) string {
    switch format {
    case "wav":
        return "audio/wav"
    case "mp3":
        return "audio/mpeg"
    default:
        return "audio/wav" // 默认
    }
}
```

**更新 ASROption 函数**:
```go
func WithASRLanguage(lang string) ASROption {
    return func(req *ASRRequest) {
        if req.ExtraBody.ASROptions.Language == "" {
            req.ExtraBody.ASROptions.Language = "zh" // 默认中文
        }
        req.ExtraBody.ASROptions.Language = lang
    }
}

func WithPunctuation() ASROption {
    return func(req *ASRRequest) {
        req.ExtraBody.ASROptions.EnablePunctuation = true
    }
}

func WithChildrenVoice() ASROption {
    return func(req *ASRRequest) {
        req.ExtraBody.ASROptions.ChildrenVoiceMode = true
    }
}
```

**更新 API 端点**:
```go
// 在 executeASRRequest 中
httpReq, err := http.NewRequestWithContext(ctx, "POST", c.config.BaseURL+"/chat/completions", bytes.NewReader(body))
```

**验证**:
- 请求格式符合官方文档
- API 端点正确
- 音频编码格式正确

### 任务 3: 更新集成测试
**文件**: `pkg/mimo/integration_test.go`

**操作**:
- 验证现有测试仍然兼容
- 确保测试使用正确的参数配置

**验证**:
- 所有 ASR 集成测试通过
- 测试覆盖功能不变

## 成功标准

- [ ] ASR 请求符合官方 OpenAI 兼容格式
- [ ] API 端点正确使用 `/chat/completions`
- [ ] 音频编码使用正确的 Data URL 格式
- [ ] 语言参数正确放置在 `asr_options` 中
- [ ] 所有集成测试通过
- [ ] 代码编译无错误

## 完成条件

重构完成当：
1. 新的 ASR 实现与官方文档完全一致
2. 所有集成测试通过
3. 代码质量保持现有标准
4. 向后兼容性得到维护