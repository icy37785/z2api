# OpenAI 兼容 API 代理 for Z.ai

一个为 Z.ai GLM 模型提供 OpenAI 兼容 API 接口的高性能代理服务器。

## ✨ 特性

- 🔄 完全兼容 OpenAI API 格式
- 🚀 支持流式和非流式响应
- 🧠 支持多种 GLM 模型（GLM-4.5, GLM-4.5-thinking, GLM-4.5-search, GLM-4.5v 等）
- 🖼️ 支持多模态内容（文本+图片）
- 🛠️ 支持函数调用（Function Calling）
- 🔍 支持联网搜索功能
- 💪 高性能优化（连接池、对象池、并发控制）
- 📊 内置性能监控和日志系统

## 🚀 快速开始

### 环境变量

| 变量名 | 描述 | 默认值 | 必需 |
|--------|------|--------|------|
| `UPSTREAM_TOKEN` | Z.ai 访问令牌 | - | ❌ |
| `API_KEY` | 客户端 API 密钥 | `sk-tbkFoKzk9a531YyUNNF5` | ❌ |
| `PORT` | 服务监听端口 | `8080` | ❌ |
| `DEBUG_MODE` | 调试模式 | `true` | ❌ |

### 本地运行

```bash
# 设置环境变量
export UPSTREAM_TOKEN="你的Z.ai访问令牌"

# 运行服务器
go run main.go
```

### Docker 部署

```dockerfile
FROM golang:1.21-alpine AS builder
WORKDIR /app
COPY . .
RUN go build -o main .

FROM alpine:latest
RUN apk --no-cache add ca-certificates
WORKDIR /root/
COPY --from=builder /app/main .
CMD ["./main"]
```

### 使用打包好的 Docker 镜像部署

`docker pull ghcr.io/icy37785/openai-compatible-api-proxy-for-z:main`

## 📖 支持的模型

| 模型名称 | 说明 |
|---------|------|
| `glm-4.5` | 标准对话模型 |
| `glm-4.5-thinking` | 支持思考过程的模型 |
| `glm-4.5-search` | 支持联网搜索的模型 |
| `glm-4.5-air` | 轻量版模型 |
| `glm-4.5v` | 多模态模型（支持图片） |

## 💡 使用示例

### Python (OpenAI SDK)

```python
import openai

client = openai.OpenAI(
    api_key="sk-tbkFoKzk9a531YyUNNF5",  # 使用配置的API密钥
    base_url="http://localhost:8080/v1"  # 代理服务器地址
)

# 基础对话
response = client.chat.completions.create(
    model="glm-4.5",
    messages=[{"role": "user", "content": "你好，请介绍一下自己"}]
)
print(response.choices[0].message.content)

# 流式响应
stream = client.chat.completions.create(
    model="glm-4.5",
    messages=[{"role": "user", "content": "写一首关于春天的诗"}],
    stream=True
)

for chunk in stream:
    if chunk.choices[0].delta.content:
        print(chunk.choices[0].delta.content, end="")
```

### JavaScript/Node.js

```javascript
import OpenAI from 'openai';

const client = new OpenAI({
  apiKey: 'sk-tbkFoKzk9a531YyUNNF5',
  baseURL: 'http://localhost:8080/v1'
});

const completion = await client.chat.completions.create({
  model: 'glm-4.5',
  messages: [{ role: 'user', content: '你好' }],
});

console.log(completion.choices[0].message.content);
```

### cURL

```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer sk-tbkFoKzk9a531YyUNNF5" \
  -d '{
    "model": "glm-4.5",
    "messages": [{"role": "user", "content": "你好"}],
    "stream": true
  }'
```

## 🛠️ 高级功能

### 多模态对话 (GLM-4.5v)

```python
response = client.chat.completions.create(
    model="glm-4.5v",
    messages=[{
        "role": "user",
        "content": [
            {"type": "text", "text": "这张图片里有什么？"},
            {"type": "image_url", "image_url": {"url": "https://example.com/image.jpg"}}
        ]
    }]
)
```

### 思考模式 (GLM-4.5-thinking)

```python
response = client.chat.completions.create(
    model="glm-4.5-thinking",
    messages=[{"role": "user", "content": "解释一下量子计算的原理"}]
)

# 响应包含推理过程
print("思考过程:", response.choices[0].message.reasoning_content)
print("最终回答:", response.choices[0].message.content)
```

### 联网搜索 (GLM-4.5-search)

```python
response = client.chat.completions.create(
    model="glm-4.5-search",
    messages=[{"role": "user", "content": "最近有什么重要的科技新闻？"}]
)
```

### 函数调用

```python
response = client.chat.completions.create(
    model="glm-4.5",
    messages=[{"role": "user", "content": "今天北京天气如何？"}],
    tools=[{
        "type": "function",
        "function": {
            "name": "get_weather",
            "description": "获取指定城市的天气",
            "parameters": {
                "type": "object",
                "properties": {
                    "city": {"type": "string", "description": "城市名称"}
                }
            }
        }
    }]
)
```

## ⚡ 性能特性

- **连接池复用**: 优化的 HTTP 客户端配置，支持高并发
- **内存优化**: 对象池减少 GC 压力，预分配缓冲区
- **并发控制**: 智能限流，防止资源耗尽
- **流式处理**: 高效的 SSE 流处理，实时响应
- **监控日志**: 内置性能统计和分层日志系统

## 🔄 重试机制

### 概述

本项目实现了一个强大而智能的重试机制，确保在面对网络波动、临时服务不可用或认证过期等问题时，API 请求能够自动恢复并成功完成。该机制采用指数退避算法，结合随机抖动和特殊错误处理，最大程度地提高了请求成功率。

### 核心特性

- ⚡ **智能错误识别**: 自动识别可重试和不可重试的错误类型
- 🔐 **401 错误特殊处理**: 自动刷新 token 并重新生成签名
- 📈 **指数退避策略**: 避免雪崩效应，减轻服务器压力
- 🎲 **随机抖动算法**: 防止重试风暴，分散请求时间
- 🚦 **最大重试限制**: 防止无限重试，默认最多 5 次
- 📊 **详细日志记录**: 完整的重试过程追踪，便于调试

### 支持的重试错误类型

#### 网络和连接错误
- `context.DeadlineExceeded` - 上下文超时
- `io.EOF` / `io.ErrUnexpectedEOF` - 连接意外关闭
- `connection reset by peer` - 连接被重置
- `connection refused` - 连接被拒绝
- `broken pipe` - 管道破裂
- 网络超时错误（`net.Error` 的 `Timeout()` 为 true）
- 临时网络错误（`net.Error` 的 `Temporary()` 为 true）

#### HTTP 状态码
| 状态码 | 错误类型 | 处理策略 |
|--------|----------|----------|
| 401 | Unauthorized | 刷新 token，重新生成签名后重试 |
| 408 | Request Timeout | 直接重试 |
| 429 | Too Many Requests | 使用更长的延迟时间重试 |
| 500 | Internal Server Error | 直接重试 |
| 502 | Bad Gateway | 直接重试 |
| 503 | Service Unavailable | 直接重试 |
| 504 | Gateway Timeout | 直接重试 |

#### 特殊 400 错误
某些 400 错误在特定情况下也会被重试：
- 响应体包含 `"系统繁忙"` 或 `"system busy"`
- 响应体包含 `"rate limit"`
- 响应体包含 `"too many requests"`
- 响应体包含 `"temporarily unavailable"`

### 重试策略

#### 指数退避算法
```
延迟时间 = baseDelay * 2^(重试次数)
```

- **基础延迟**: 100ms
- **最大延迟**: 10s
- **429 限流特殊处理**: 基础延迟增加到 1s，最大延迟 30s

#### 抖动策略
为避免重试风暴，每次延迟会添加 ±25% 的随机抖动：
```
实际延迟 = 计算延迟 ± (计算延迟 * 0.25 * 随机值)
```

#### 重试次数限制
- **默认最大重试次数**: 5 次
- **包括初次请求在内**: 总共最多 5 次请求

### 401 错误的特殊处理流程

当遇到 401 未授权错误时，系统会执行以下特殊处理：

1. **立即标记当前 token 为失效**
   ```go
   tokenCache.InvalidateToken()
   ```

2. **获取新的匿名 token**（如果启用）
   ```go
   if appConfig.AnonTokenEnabled {
       newToken, _ := getAnonymousTokenDirect()
   }
   ```

3. **重新生成请求签名**
   - 使用新 token 的 user_id
   - 重新计算时间戳
   - 生成新的 HMAC-SHA256 签名

4. **使用新凭证重试请求**

### 配置参数

虽然重试机制是自动的，但以下环境变量会影响其行为：

| 环境变量 | 描述 | 默认值 | 影响 |
|----------|------|--------|------|
| `ANON_TOKEN_ENABLED` | 启用匿名 token | `true` | 影响 401 错误的处理方式 |
| `DEBUG_MODE` | 调试模式 | `true` | 控制重试日志的详细程度 |

### 使用示例

#### 日志示例 - 成功重试

```log
[DEBUG] 开始第 1/5 次尝试调用上游API
[DEBUG] 上游响应状态: 503 Service Unavailable
[DEBUG] 收到可重试的HTTP状态码 503 (尝试 1/5)
[DEBUG] 网关错误 503，可重试
[DEBUG] 计算退避延迟：尝试 0，基础延迟 100ms，最终延迟 125ms
[DEBUG] 等待 125ms 后重试

[DEBUG] 开始第 2/5 次尝试调用上游API
[DEBUG] 上游响应状态: 200 OK
[DEBUG] 上游调用成功 (尝试 2/5): 200
```

#### 日志示例 - 401 错误处理

```log
[DEBUG] 开始第 1/5 次尝试调用上游API
[DEBUG] 上游响应状态: 401 Unauthorized
[DEBUG] 收到401错误，尝试刷新token和重新生成签名
[DEBUG] 匿名token已标记为失效，下次请求将获取新token
[DEBUG] 成功获取新的匿名token，下次重试将使用新token和新签名
[DEBUG] 等待 100ms 后重试

[DEBUG] 开始第 2/5 次尝试调用上游API
[DEBUG] 从 JWT token 中成功解析 user_id: user-123456
[DEBUG] 构建的完整URL: https://chat.z.ai/api/chat/completions?signature_timestamp=...
[DEBUG] 上游响应状态: 200 OK
```

#### 日志示例 - 达到最大重试次数

```log
[DEBUG] 开始第 1/5 次尝试调用上游API
[DEBUG] 上游响应状态: 500 Internal Server Error
[DEBUG] 500服务器内部错误，可重试
...
[DEBUG] 开始第 5/5 次尝试调用上游API
[DEBUG] 上游响应状态: 500 Internal Server Error
[ERROR] 上游API在 5 次尝试后仍然失败，最后状态码: 500
```

### 最佳实践

1. **监控重试日志**: 定期检查重试日志，识别潜在的系统问题
2. **调整超时设置**: 根据实际网络环境调整请求超时时间
3. **token 管理**: 确保 `UPSTREAM_TOKEN` 或匿名 token 机制正常工作
4. **错误分析**: 分析不可重试的错误，改进请求参数验证

### 实现细节

重试机制的核心实现位于以下函数：

- [`isRetryableError()`](main.go:2586) - 判断错误是否可重试
- [`calculateBackoffDelay()`](main.go:2667) - 计算退避延迟时间
- [`callUpstreamWithRetry()`](main.go:2692) - 带重试的上游调用
- [`cleanupResponse()`](main.go:2820) - 清理失败响应，优化连接复用

### 测试覆盖

重试机制包含全面的单元测试和集成测试：

- **单元测试** ([`retry_test.go`](retry_test.go))：测试错误判断和延迟计算
- **集成测试** ([`retry_integration_test.go`](retry_integration_test.go))：模拟真实场景的重试行为

测试覆盖包括：
- ✅ 各种错误类型的识别
- ✅ 指数退避算法正确性
- ✅ 401 错误的 token 刷新
- ✅ 最大重试次数限制
- ✅ 网络错误和超时处理
- ✅ 特殊 400 错误的重试

## 📊 监控

服务器提供详细的性能监控信息：

```
[INFO] 请求完成 - 模型: glm-4.5, 模式: streaming, 耗时: 2.1s, tokens: 150
```

## 🔧 部署建议

### Render 部署

1. Fork 此仓库
2. 在 Render 创建新的 Web Service
3. 连接 GitHub 仓库
4. 设置环境变量 `UPSTREAM_TOKEN`
5. 部署完成

### Railway 部署

```bash
# 安装 Railway CLI
npm install -g @railway/cli

# 部署
railway login
railway init
railway add
railway deploy
```

## 🤝 贡献

欢迎提交 Issue 和 Pull Request！

1. Fork 项目
2. 创建功能分支
3. 提交更改
4. 推送分支
5. 创建 Pull Request

## 📄 许可证

MIT License

## ⚠️ 免责声明

本项目为第三方开发，与 Z.ai 官方无关。使用前请确保遵守相关服务条款。

---

**🔗 相关链接**
- [Z.ai 官网](https://chat.z.ai)
- [OpenAI API 文档](https://platform.openai.com/docs/api-reference)