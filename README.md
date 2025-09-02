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