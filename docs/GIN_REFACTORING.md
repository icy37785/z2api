# Gin 框架重构指南

## 已完成的重构

### 1. 流式处理器重构 ✅
- **新文件**: `stream_handler_gin.go`
- **主要改进**:
  - `GinStreamHandler` - 完全基于 gin.Context
  - `HandleGinStreamResponse` - 使用 Gin 的 Stream API
  - `GinStreamAggregator` - 用于非流式响应聚合

### 2. Dashboard 处理器重构 ✅
- **新文件**: `gin_dashboard_handlers.go`
- **重构的处理器**:
  - `GinHandleDashboard` - 替代 handleDashboard
  - `GinHandleDashboardStats` - 替代 handleDashboardStats
  - `GinHandleDashboardRequests` - 替代 handleDashboardRequests
  - `GinHandleOptions` - 替代 handleOptions
  - `GinHandleHome` - 处理根路径
  - `GinHandleNotFound` - 404 处理

### 3. 路由更新 ✅
- **更新文件**: `router.go`
- **改进**:
  - 使用纯 Gin 处理器
  - 移除 http.Handler 适配器
  - 添加 NoRoute 处理器

### 4. 流式响应优化 ✅
- **更新文件**: `gin_handlers_optimized.go`
- **改进**:
  - 使用 HandleGinStreamResponse
  - 使用 GinStreamAggregator
  - 移除 http.ResponseWriter 直接使用

## Gin 最佳实践应用

### 1. 中间件使用
```go
// 推荐的中间件链
router.Use(ginLogger())      // 自定义日志
router.Use(gin.Recovery())   // 恢复中间件
router.Use(requestid.New())  // Request ID
router.Use(setupCORS())      // CORS
router.Use(rateLimitMiddleware()) // 限流
```

### 2. 上下文存储
```go
// 使用 Gin Context 存储请求数据
c.Set("start_time", time.Now())
c.Set("user_agent", c.GetHeader("User-Agent"))
c.Set("session_id", sessionID)

// 获取存储的值
startTime := c.GetTime("start_time")
userAgent := c.GetString("user_agent")
```

### 3. 错误处理
```go
// 统一的错误响应格式
c.AbortWithStatusJSON(statusCode, gin.H{
    "error": gin.H{
        "message": errorMessage,
        "type":    errorType,
        "code":    errorCode,
    },
})
```

### 4. JSON 绑定和验证
```go
// 自动解析和验证请求
var req OpenAIRequest
if err := c.ShouldBindJSON(&req); err != nil {
    c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
        "error": gin.H{
            "message": "Invalid JSON format",
            "details": err.Error(),
        },
    })
    return
}
```

### 5. 流式响应
```go
// 使用 Gin 的 Stream API
c.Stream(func(w io.Writer) bool {
    // 处理流式数据
    line, err := reader.ReadString('\n')
    if err != nil {
        return false // 结束流
    }
    // 处理数据...
    return true // 继续流
})
```

## 性能优化

### 1. 对象池使用
- 使用 sync.Pool 减少内存分配
- 复用 strings.Builder 和 bytes.Buffer

### 2. 并发控制
- 使用 semaphore 限制并发请求数
- 中间件级别的限流控制

### 3. JSON 处理优化
- 使用 sonic 高性能 JSON 库
- 针对不同场景的配置（stream、fast、compatible）

## 迁移检查清单

### 需要更新的导入
```go
// 移除
import "net/http"

// 添加
import "github.com/gin-gonic/gin"
```

### 函数签名更改
```go
// 旧
func handleXXX(w http.ResponseWriter, r *http.Request) {}

// 新
func GinHandleXXX(c *gin.Context) {}
```

### 响应方式更改
```go
// 旧
w.Header().Set("Content-Type", "application/json")
json.NewEncoder(w).Encode(data)

// 新
c.JSON(http.StatusOK, data)
```

### 流式响应更改
```go
// 旧
fmt.Fprintf(w, "data: %s\n\n", data)

// 新
c.Writer.WriteString(fmt.Sprintf("data: %s\n\n", data))
c.Writer.Flush()
```

## 测试建议

### 1. 功能测试
```bash
# 测试流式响应
./scripts/test_quick.sh

# 测试非流式响应
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer $API_KEY" \
  -d '{"model":"glm-4.5","messages":[{"role":"user","content":"Hello"}],"stream":false}'

# 测试Dashboard
curl http://localhost:8080/dashboard/stats
```

### 2. 性能测试
```bash
# 使用 wrk 或 ab 进行压力测试
wrk -t12 -c400 -d30s http://localhost:8080/health
```

### 3. 兼容性测试
- 确保与现有客户端兼容
- 验证 CORS 设置正确
- 检查 SSE 流式响应格式

## 注意事项

1. **保持向后兼容**：确保 API 响应格式不变
2. **错误处理一致性**：使用统一的错误响应格式
3. **监控和日志**：确保所有请求都被正确记录
4. **性能监控**：观察重构后的性能指标

## 后续优化建议

1. **请求验证器**：使用 Gin 的验证器进行更严格的输入验证
2. **响应压缩**：启用 Gin 的 gzip 中间件
3. **请求限流**：实现更细粒度的限流策略
4. **缓存策略**：添加响应缓存中间件
5. **链路追踪**：集成 OpenTelemetry 或 Jaeger