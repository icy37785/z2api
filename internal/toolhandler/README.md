# SSE工具调用处理器

## 简介

这个包实现了对Z.AI特殊SSE格式的工具调用处理，将其转换为OpenAI标准格式。

## 核心功能

- ✅ 处理`edit_index`/`edit_content`增量更新机制
- ✅ 解析`<glm_block>`格式的工具调用
- ✅ 参数完整性检查，避免发送不完整的工具调用
- ✅ 转换为OpenAI标准SSE格式
- ✅ 线程安全
- ✅ 高性能（使用sonic JSON库）

## 快速开始

### 基本使用

```go
import "z2api/internal/toolhandler"

// 创建处理器
handler := toolhandler.NewSSEToolHandler(chatID, model, debugLog)

// 处理tool_call阶段
chunks := handler.ProcessToolCallPhase(upstreamData)
for _, chunk := range chunks {
    // 发送SSE chunk到客户端
    writer.WriteString(chunk + "\n\n")
    writer.Flush()
}

// 处理other阶段（检测工具调用结束）
chunks = handler.ProcessOtherPhase(upstreamData)
for _, chunk := range chunks {
    writer.WriteString(chunk + "\n\n")
    writer.Flush()
}

// 完成所有活跃工具
chunks = handler.CompleteActiveTools()
for _, chunk := range chunks {
    writer.WriteString(chunk + "\n\n")
    writer.Flush()
}
```

### 集成示例

参见`stream_handler_gin.go`中的集成示例：

```go
type GinStreamHandler struct {
    sseToolHandler *toolhandler.SSEToolHandler
    // ...
}

func (h *GinStreamHandler) ProcessToolCallPhase(data *types.UpstreamData) {
    chunks := h.sseToolHandler.ProcessToolCallPhase(data)
    for _, chunk := range chunks {
        h.ctx.Writer.WriteString(chunk + "\n\n")
        h.ctx.Writer.Flush()
    }
}
```

## 组件说明

### ContentBuffer
按`edit_index`位置组装内容片段，支持覆盖模式。

### GlmBlockParser
解析`<glm_block>`标签内的JSON，支持不完整块的解析。

### CompletenessChecker
检查工具调用参数是否完整，决定是否发送工具调用。

### SSEToolHandler
主控制器，协调各组件完成工具调用处理。

## 工作流程

```
Z.AI SSE响应
    ↓
edit_index/edit_content
    ↓
ContentBuffer组装
    ↓
提取<glm_block>
    ↓
解析工具调用
    ↓
完整性检查
    ↓
OpenAI格式转换
    ↓
SSE chunk输出
```

## 调试

启用调试日志查看详细处理过程：

```go
handler := toolhandler.NewSSEToolHandler(chatID, model, func(format string, args ...interface{}) {
    log.Printf("[ToolHandler] "+format, args...)
})
```

日志示例：
```
🔧 进入工具调用阶段
🎯 发现新工具: search(id=call_123), 参数完整性: true
📤 发送完整工具开始: search(id=call_123)
🏁 检测到工具调用结束
✅ 完成工具调用: search(id=call_123)
```

## 性能特点

- 使用字节切片进行高效内存操作
- 使用sonic进行快速JSON序列化
- 线程安全的并发访问
- 预分配缓冲区减少内存分配

## 测试

```bash
# 运行单元测试
go test ./internal/toolhandler/...

# 运行集成测试
./scripts/test_tool_format.sh
./scripts/test_tool_comprehensive.sh
```

## 详细文档

完整的实现文档请参见：[SSE_TOOL_HANDLER_IMPLEMENTATION.md](../../docs/SSE_TOOL_HANDLER_IMPLEMENTATION.md)