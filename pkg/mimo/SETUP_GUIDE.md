# 小米 Mimo API Key 配置指南

## 如何获取小米 Mimo API Key

1. 访问小米 Mimo 开发者平台：https://developer.xiaomimimo.com
2. 注册/登录开发者账号
3. 创建新应用或使用现有应用
4. 在应用管理页面获取 API Key
5. 记录你的 API Key（格式类似：`sk-xxxxxxxxxxxxxxxxxxxxx`）

## 配置方式

### 方式 1：环境变量（推荐用于生产环境）

```bash
# Linux/Mac
export MIMO_API_KEY="your-api-key-here"
export MIMO_BASE_URL="https://api.xiaomimimo.com/v1"  # 可选，默认为生产环境

# Windows (PowerShell)
$env:MIMO_API_KEY="your-api-key-here"
$env:MIMO_BASE_URL="https://api.xiaomimimo.com/v1"

# Windows (CMD)
set MIMO_API_KEY=your-api-key-here
set MIMO_BASE_URL=https://api.xiaomimimo.com/v1
```

### 方式 2：.env 文件（推荐用于开发环境）

在项目根目录创建 `.env` 文件：

```env
MIMO_API_KEY=your-api-key-here
MIMO_BASE_URL=https://api.xiaomimimo.com/v1
```

⚠️ **重要**：确保 `.env` 文件已添加到 `.gitignore` 中，避免提交敏感信息到代码仓库。

### 方式 3：直接在测试中设置（仅用于快速测试）

```bash
# 临时设置并运行测试
MIMO_API_KEY="your-api-key-here" go test ./pkg/mimo/... -v -run TestIntegration
```

## 运行集成测试

### 运行所有集成测试
```bash
# 确保 MIMO_API_KEY 已设置
go test ./pkg/mimo/... -v -run TestIntegration
```

### 运行特定集成测试
```bash
# 只测试 TTS 合成
go test ./pkg/mimo/... -v -run TestIntegration_Synthesize

# 只测试 ASR 转录
go test ./pkg/mimo/... -v -run TestIntegration_Transcribe

# 只测试健康检查
go test ./pkg/mimo/... -v -run TestIntegration_HealthCheck
```

### 运行所有测试（包括单元测试）
```bash
go test ./pkg/mimo/... -v
```

## 测试覆盖范围

集成测试覆盖以下功能：

1. **TTS 语音合成** (`TestIntegration_Synthesize_RealAPI`)
   - 基础语音合成
   - 儿童语音优化
   - 不同声音选择
   - 自定义采样率

2. **流式 TTS** (`TestIntegration_SynthesizeStream_RealAPI`)
   - 实时流式语音生成
   - 分块接收音频数据

3. **ASR 语音识别** (`TestIntegration_Transcribe_RealAPI`)
   - 中文语音识别
   - 儿童语音优化
   - 英文语音识别
   - 标点符号处理

4. **健康检查** (`TestIntegration_HealthCheck_RealAPI`)
   - API 连接性验证
   - 服务可用性检查

5. **端到端工作流** (`TestIntegration_EndToEnd_RealAPI`)
   - 完整的听写流程测试
   - 老师 TTS → 学生 ASR → 反馈 TTS

6. **并发请求处理** (`TestIntegration_ConcurrentRequests_RealAPI`)
   - 5 个并发请求测试
   - 连接池验证

7. **错误处理** (`TestIntegration_ErrorHandling_RealAPI`)
   - 无效 API key
   - 空音频数据处理

## 常见问题

### Q: 测试被跳过，显示 "Skipping integration test: MIMO_API_KEY not set"
**A**: 确保已正确设置 `MIMO_API_KEY` 环境变量。

### Q: 测试失败，显示认证错误
**A**: 检查 API key 是否正确，确保账户有足够的使用配额。

### Q: 测试超时
**A**: 检查网络连接，确认可以访问小米 Mimo API 服务。

### Q: 如何在 CI/CD 环境中配置？
**A**: 在 CI/CD 平台的环境变量设置中添加 `MIMO_API_KEY`，不要在代码中硬编码。

## 安全提醒

⚠️ **重要安全注意事项**：
- 永远不要将 API key 提交到代码仓库
- 定期轮换 API key
- 为不同的环境（开发、测试、生产）使用不同的 API key
- 监控 API 使用情况，及时发现异常
- 在生产环境中使用密钥管理服务（如 AWS Secrets Manager、Azure Key Vault）

## 测试费用说明

小米 Mimo API 通常按调用量收费：
- 集成测试会产生实际的 API 调用费用
- 建议设置使用配额限制
- 可以使用测试环境的 API key（如果小米提供）

## 下一步

配置完成后，建议按以下顺序运行测试：

1. 首先运行健康检查测试验证连接：
   ```bash
   go test ./pkg/mimo/... -v -run TestIntegration_HealthCheck
   ```

2. 然后运行基础功能测试：
   ```bash
   go test ./pkg/mimo/... -v -run TestIntegration_Synthesize_RealAPI/basic
   ```

3. 最后运行完整的集成测试套件：
   ```bash
   go test ./pkg/mimo/... -v -run TestIntegration
   ```
