# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## 项目概述

这是一个高性能的 OpenAI 兼容 API 代理服务器，为 Z.ai GLM 模型提供标准化接口。代理支持流式/非流式响应、多模态内容、函数调用、联网搜索等功能。

## 开发命令

### 构建和运行
```bash
# 构建二进制文件
go build -o z2api main.go

# 直接运行（开发模式）
go run main.go

# 运行时必需的环境变量
export UPSTREAM_TOKEN="你的Z.ai访问令牌"  # 可选，支持匿名token
export API_KEY="sk-tbkFoKzk9a531YyUNNF5"   # 默认值
export PORT=8080                           # 默认值
export DEBUG_MODE=true                     # 默认值
```

### 测试命令
```bash
# 运行所有单元测试
go test -v ./...

# 运行特定测试
go test -v -run TestRetry     # 重试机制测试
go test -v -run TestNonStream # 非流式响应测试
go test -v -run TestSignature # 签名验证测试

# 功能测试脚本
./scripts/test_quick.sh         # 快速功能验证（最常用）
./scripts/test_essential.sh     # 基础功能测试
./scripts/test_comprehensive.sh # 完整测试套件
./scripts/test_optimized.sh     # 性能优化测试

# 运行覆盖率测试
go test -v -cover ./...
```

## 核心架构

### 请求流程
1. **入口处理** (`main.go:handleChatCompletions`)
   - 请求验证和API密钥检查
   - 请求体解析和预处理
   - 路由到流式或非流式处理器

2. **消息转换** (`message_converter.go`)
   - OpenAI格式到GLM格式转换
   - 多模态内容处理（文本+图片）
   - 工具调用参数转换

3. **签名生成** (`signature.go`)
   - JWT token解析获取user_id
   - HMAC-SHA256签名计算
   - 时间戳和请求参数编码

4. **上游调用** (`main.go:callUpstreamWithRetry`)
   - 智能重试机制（指数退避+抖动）
   - 401错误自动刷新token
   - 连接池复用优化

5. **响应处理**
   - **流式**: `stream_handler.go` - SSE流解析和转发
   - **非流式**: `handlers_optimized.go` - 完整响应处理
   - 特殊功能处理（thinking模式、搜索结果等）

### 关键特性实现

#### 重试机制 (`retry_*.go`)
- 可重试错误类型识别（网络、超时、5xx、429等）
- 指数退避算法：`delay = baseDelay * 2^attempts`
- 401错误特殊处理：自动刷新token并重新签名
- 最大重试5次，避免无限循环

#### 性能优化
- **对象池**: 减少GC压力（buffers、decoders）
- **连接池**: HTTP客户端连接复用
- **并发控制**: Semaphore限流（默认100并发）
- **Sonic JSON**: 高性能JSON编解码
- **压缩支持**: Gzip/Brotli透明处理

#### 模型映射 (`model_mapper.go`)
- OpenAI模型名到GLM模型名映射
- 支持别名（gpt-4 → glm-4.5等）
- 特殊模型识别（thinking、search、vision）

## 重要配置

### 环境变量
- `UPSTREAM_TOKEN`: Z.ai访问令牌（可选）
- `ANON_TOKEN_ENABLED`: 启用匿名token（默认true）
- `USE_OPTIMIZED_HANDLERS`: 使用优化处理器（默认true）
- `MAX_CONCURRENT_REQUESTS`: 最大并发数（默认100）
- `REQUEST_TIMEOUT`: 请求超时（默认120s）

### 模型支持
- `glm-4.5`: 标准对话模型
- `glm-4.5-thinking`: 支持思考过程
- `glm-4.5-search`: 联网搜索
- `glm-4.5v`: 多模态（图片+文本）
- `glm-4.5-air`: 轻量版模型
- 映射支持：`gpt-4*`, `claude*`, `deepseek*`等

## 调试技巧

### 日志级别
```bash
export DEBUG_MODE=true  # 详细日志
export DEBUG_MODE=false # 生产模式
```

### 常见问题调试
1. **401错误**: 检查token是否有效，查看token刷新日志
2. **签名错误**: 验证signature.go中的密钥和算法
3. **流式中断**: 检查SSE解析和缓冲区大小
4. **性能问题**: 查看并发数和连接池配置

### 测试特定功能
```bash
# 测试流式响应
curl -N -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer $API_KEY" \
  -d '{"model":"glm-4.5","messages":[{"role":"user","content":"Hello"}],"stream":true}'

# 测试工具调用
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer $API_KEY" \
  -d '{"model":"glm-4.5","messages":[{"role":"user","content":"Weather?"}],"tools":[...]}'
```

## 代码规范

- 使用 `go fmt` 格式化代码
- 错误处理：优先返回错误而非panic
- 日志：使用分级日志（DEBUG/INFO/ERROR）
- 测试：新功能必须包含单元测试
- 性能：注意内存分配和goroutine泄漏