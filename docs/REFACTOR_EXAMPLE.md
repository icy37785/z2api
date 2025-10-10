# 代码重构示例

## 使用新工具重构现有代码

### 1. 错误响应重构

**原代码** (gin_handlers_optimized.go:33-40):
```go
c.AbortWithStatusJSON(StatusUnauthorized, gin.H{
    "error": gin.H{
        "message": "Missing or invalid Authorization header",
        "type":    "invalid_request_error",
        "code":    401,
        "param":   "authorization",
    },
})
recordError(c, startTime, StatusUnauthorized, "invalid_api_key")
```

**新代码**:
```go
import "z2api/utils"

utils.ErrorResponse(c, StatusUnauthorized, "invalid_request_error",
    "Missing or invalid Authorization header", "authorization")
recordError(c, startTime, StatusUnauthorized, "invalid_api_key")
```

### 2. 日志系统重构

**原代码** (main.go):
```go
func debugLog(format string, args ...interface{}) {
    if appConfig.DebugMode {
        log.Printf("[DEBUG] "+format, args...)
    }
}

// 使用
debugLog("请求解析成功 - 模型: %s, 流式: %v", req.Model, req.Stream)
```

**新代码**:
```go
import "z2api/utils"

// 初始化时
utils.InitLogger(appConfig.DebugMode)

// 使用
utils.LogDebug("请求解析成功",
    "model", req.Model,
    "stream", req.Stream)
```

### 3. ID生成重构

**原代码** (gin_handlers_optimized.go:105-106):
```go
now := time.Now()
chatID := fmt.Sprintf("%d-%d", now.UnixNano(), now.Unix())
msgID := fmt.Sprintf("%d", now.UnixNano())
```

**新代码**:
```go
import "z2api/utils"

chatID := utils.GenerateChatID()
msgID := utils.GenerateMessageID()
```

### 4. 请求验证重构

**原代码** (gin_handlers_optimized.go:60-74):
```go
var req OpenAIRequest
if err := c.ShouldBindJSON(&req); err != nil {
    c.AbortWithStatusJSON(StatusBadRequest, gin.H{
        "error": gin.H{
            "message": "Invalid JSON format",
            "type":    "invalid_request_error",
            "code":    400,
            "param":   nil,
            "details": err.Error(),
        },
    })
    recordError(c, startTime, StatusBadRequest, "invalid_request_error")
    return
}
```

**新代码**:
```go
import "z2api/utils"

req, err := utils.ValidateChatRequest(c)
if err != nil {
    utils.ErrorResponseWithDetails(c, StatusBadRequest,
        "invalid_request_error", "Invalid request", nil, err.Error())
    recordError(c, startTime, StatusBadRequest, "invalid_request_error")
    return
}
```

### 5. 流式响应设置重构

**原代码** (gin_handlers_optimized.go:174-177):
```go
c.Header("Content-Type", "text/event-stream")
c.Header("Cache-Control", "no-cache")
c.Header("Connection", "keep-alive")
c.Header("X-Accel-Buffering", "no")
```

**新代码**:
```go
import "z2api/utils"

utils.SetupStreamResponse(c)
```

### 6. 完整的处理函数重构示例

**重构后的 GinHandleChatCompletions**:
```go
func GinHandleChatCompletions(c *gin.Context) {
    startTime := time.Now()

    // 设置上下文
    c.Set("start_time", startTime)
    c.Set("user_agent", c.GetHeader("User-Agent"))

    // 记录请求开始
    utils.LogRequestStart(c, "chat_completions")

    // 更新监控指标
    totalRequests.Add(1)
    currentConcurrency.Add(1)
    defer currentConcurrency.Add(-1)

    // API Key 验证
    if err := utils.ValidateAPIKey(c, appConfig.DefaultKey); err != nil {
        utils.ErrorResponse(c, StatusUnauthorized,
            "invalid_request_error", err.Error(), "api_key")
        utils.LogRequestEnd(c, "chat_completions", false, time.Since(startTime))
        return
    }

    // 请求验证
    req, err := utils.ValidateChatRequest(c)
    if err != nil {
        utils.ErrorResponseWithDetails(c, StatusBadRequest,
            "invalid_request_error", "Invalid request", nil, err.Error())
        utils.LogRequestEnd(c, "chat_completions", false, time.Since(startTime))
        return
    }

    utils.LogDebug("Request validated",
        "model", req.Model,
        "stream", req.Stream,
        "messages", len(req.Messages))

    // 生成IDs
    chatID := utils.GenerateChatID()
    msgID := utils.GenerateMessageID()
    sessionID := req.User
    if sessionID == "" {
        sessionID = c.ClientIP()
    }

    c.Set("session_id", sessionID)
    c.Set("chat_id", chatID)

    // 获取模型配置
    modelConfig := mapper.GetSimpleModelConfig(req.Model)
    c.Set("model_name", modelConfig.Name)

    // 构造上游请求
    upstreamReq := buildUpstreamRequest(*req, chatID, msgID, modelConfig)

    // 获取认证token
    authToken := getAuthToken()

    // 根据请求类型调用不同的处理函数
    if req.Stream {
        handleStreamResponseGin(c, upstreamReq, chatID, authToken, modelConfig.Name, sessionID)
    } else {
        handleNonStreamResponseGin(c, upstreamReq, chatID, authToken, modelConfig.Name, sessionID)
    }

    utils.LogRequestEnd(c, "chat_completions", true, time.Since(startTime))
}
```

## 迁移步骤

### 第一步：添加utils包
1. 创建 `utils/` 目录
2. 添加工具文件（response.go, logger.go, validation.go）
3. 更新 imports

### 第二步：逐个函数替换
1. 从错误处理开始（影响最小）
2. 替换ID生成函数
3. 统一日志系统
4. 最后替换验证逻辑

### 第三步：测试
1. 单元测试每个工具函数
2. 集成测试主要流程
3. 性能测试确保没有退化

## 收益

- **代码行数减少**: ~20%
- **重复代码减少**: ~40%
- **可读性提升**: 显著
- **维护性提升**: 显著
- **测试覆盖率**: 更容易达到高覆盖率