# Go版本SSE工具调用处理器实现文档

## 概述

本文档描述了Go版本的SSE工具调用处理器的实现，该处理器用于处理Z.AI的特殊`edit_index`/`edit_content`机制和`<glm_block>`格式，将其转换为OpenAI标准格式。

## 架构设计

### 组件结构

```
internal/toolhandler/
├── content_buffer.go          # 内容缓冲区
├── glm_parser.go              # GLM块解析器
├── completeness_checker.go    # 完整性检查器
└── sse_tool_handler.go        # 主控制器
```

### 核心组件

#### 1. ContentBuffer（内容缓冲区）

**文件**: `internal/toolhandler/content_buffer.go`

**功能**: 按`edit_index`位置组装和更新内容片段

**核心方法**:
- `ApplyEdit(editIndex int, editContent string)` - 在指定位置应用编辑
- `GetContent() string` - 获取当前完整内容
- `Reset()` - 重置缓冲区
- `Clear()` - 清空缓冲区并释放内存

**实现特点**:
- 使用字节切片（`[]byte`）提供高效的随机访问
- 支持覆盖模式（而非插入模式）
- 自动扩展缓冲区以容纳新内容
- 线程安全（使用`sync.Mutex`）

**参考Python实现**:
```python
# Python版本使用bytearray
self.content_buffer = bytearray()
self.content_buffer[edit_index:end_index] = edit_bytes
```

**Go实现**:
```go
// Go版本使用[]byte
buffer []byte
copy(cb.buffer[editIndex:], editBytes)
```

#### 2. GlmBlockParser（GLM块解析器）

**文件**: `internal/toolhandler/glm_parser.go`

**功能**: 解析`<glm_block>`标签内的JSON

**核心方法**:
- `ExtractBlocks(content string) []GlmBlock` - 提取所有GLM块
- `ParseToolCall(block GlmBlock) (*ToolCallInfo, error)` - 解析工具调用信息
- `ParsePartialToolCall(blockContent string) (*ToolCallInfo, error)` - 解析部分工具调用

**实现特点**:
- 使用正则表达式匹配`<glm_block>`标签
- 支持不完整块的解析
- 自动修复JSON结构问题
- 使用sonic进行高性能JSON序列化

**正则表达式**:
```go
pattern := regexp.MustCompile(`(?s)<glm_block\s*>(.*?)(?:</glm_block>|$)`)
```

**JSON修复逻辑**:
- 平衡括号数量
- 处理转义字符
- 补全不完整的JSON结构

#### 3. CompletenessChecker（完整性检查器）

**文件**: `internal/toolhandler/completeness_checker.go`

**功能**: 检查工具调用参数是否完整

**核心方法**:
- `IsArgumentsComplete(arguments, argumentsRaw) bool` - 检查参数完整性
- `IsSignificantImprovement(oldArgs, newArgs, oldRaw, newRaw) bool` - 检查是否有显著改进
- `ShouldSendArgumentUpdate(lastSent, newArgs) bool` - 判断是否应该发送参数更新

**检查逻辑**:
1. 检查参数是否为空
2. 检查原始字符串是否以`}`或`"`结尾
3. 检查URL是否完整
4. 检查是否有截断迹象（如以`.`、`/`、`:`、`=`结尾）

**显著改进判断**:
- 新参数有更多键
- 值长度显著增长（>5个字符）
- 旧值看起来被截断，新值更完整

#### 4. SSEToolHandler（主控制器）

**文件**: `internal/toolhandler/sse_tool_handler.go`

**功能**: 协调各组件，处理工具调用流程

**核心方法**:
- `ProcessToolCallPhase(data *types.UpstreamData) []string` - 处理tool_call阶段
- `ProcessOtherPhase(data *types.UpstreamData) []string` - 处理other阶段
- `CompleteActiveTools() []string` - 完成所有活跃工具

**工作流程**:

```
1. 接收edit_index/edit_content
   ↓
2. 更新ContentBuffer
   ↓
3. 从缓冲区提取GLM块
   ↓
4. 解析工具调用信息
   ↓
5. 检查参数完整性
   ↓
6. 决定是否发送/更新
   ↓
7. 生成OpenAI格式的SSE chunk
```

**状态管理**:
```go
type ActiveToolInfo struct {
    ID           string                 // 工具ID
    Name         string                 // 工具名称
    Arguments    map[string]interface{} // 当前参数
    ArgsRaw      string                 // 原始参数字符串
    Status       string                 // 状态: active, completed
    SentStart    bool                   // 是否已发送开始信号
    LastSentArgs map[string]interface{} // 上次发送的参数
    ArgsComplete bool                   // 参数是否完整
    PendingSend  bool                   // 是否待发送
}
```

## 集成到stream_handler_gin.go

### 修改点

1. **导入新包**:
```go
import (
    "z2api/internal/toolhandler"
    "time"
)
```

2. **添加SSEToolHandler字段**:
```go
type GinStreamHandler struct {
    // ... 其他字段
    sseToolHandler *toolhandler.SSEToolHandler
}
```

3. **初始化SSEToolHandler**:
```go
func NewGinStreamHandler(c *gin.Context, model string) *GinStreamHandler {
    chatID := c.GetString("RequestID")
    if chatID == "" {
        chatID = fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
    }
    
    return &GinStreamHandler{
        // ...
        sseToolHandler: toolhandler.NewSSEToolHandler(chatID, model, debugLog),
    }
}
```

4. **修改ProcessToolCallPhase**:
```go
func (h *GinStreamHandler) ProcessToolCallPhase(data *types.UpstreamData) {
    // 使用新的SSEToolHandler处理工具调用
    chunks := h.sseToolHandler.ProcessToolCallPhase(data)
    for _, chunk := range chunks {
        h.ctx.Writer.WriteString(chunk + "\n\n")
        h.ctx.Writer.Flush()
    }
    // ... 向后兼容逻辑
}
```

5. **修改ProcessOtherPhase**:
```go
func (h *GinStreamHandler) ProcessOtherPhase(data *types.UpstreamData) {
    // 使用新的SSEToolHandler处理other阶段
    chunks := h.sseToolHandler.ProcessOtherPhase(data)
    for _, chunk := range chunks {
        h.ctx.Writer.WriteString(chunk + "\n\n")
        h.ctx.Writer.Flush()
    }
    // ... 原有逻辑
}
```

## 关键技术点

### 1. edit_index机制

Z.AI使用`edit_index`和`edit_content`来增量更新内容：

```json
{
  "edit_index": 0,
  "edit_content": "partial content"
}
```

我们的实现：
- 使用字节切片按位置组装内容
- 支持覆盖模式（而非插入）
- 自动扩展缓冲区

### 2. glm_block解析

Z.AI的工具调用格式：

```xml
<glm_block>
{
  "data": {
    "metadata": {
      "id": "call_xxx",
      "name": "tool_name",
      "arguments": "{\"param\":\"value\"}"
    }
  }
}
</glm_block>
```

我们的解析策略：
- 正则表达式提取块
- JSON解析和修复
- 支持部分块解析

### 3. 参数完整性检查

避免发送不完整的工具调用：

**检查点**:
1. 参数是否为空
2. 原始字符串格式是否正确
3. URL是否完整
4. 是否有截断迹象

**发送策略**:
- 只有参数完整时才发送开始信号
- 参数有显著改进时才发送更新
- 避免频繁的微小更新

### 4. OpenAI格式转换

生成符合OpenAI标准的SSE chunk：

```go
chunk := map[string]interface{}{
    "choices": []map[string]interface{}{
        {
            "delta": map[string]interface{}{
                "role": "assistant",
                "tool_calls": []map[string]interface{}{
                    {
                        "id":   toolID,
                        "type": "function",
                        "function": map[string]interface{}{
                            "name":      toolName,
                            "arguments": argsStr,
                        },
                    },
                },
            },
            "finish_reason": nil,
            "index":         0,
        },
    },
    "created": time.Now().Unix(),
    "id":      chatID,
    "model":   model,
    "object":  "chat.completion.chunk",
}
```

## 性能优化

### 1. 内存管理
- 使用字节切片而非字符串拼接
- 预分配缓冲区容量（4KB）
- 及时释放不需要的内存

### 2. 并发安全
- ContentBuffer使用互斥锁保护
- 避免数据竞争

### 3. JSON处理
- 使用sonic进行高性能序列化
- 缓存解析结果

## 测试建议

### 1. 单元测试

测试各组件的独立功能：

```go
// ContentBuffer测试
func TestContentBuffer_ApplyEdit(t *testing.T) {
    buffer := NewContentBuffer()
    buffer.ApplyEdit(0, "Hello")
    buffer.ApplyEdit(5, " World")
    assert.Equal(t, "Hello World", buffer.GetContent())
}

// GlmBlockParser测试
func TestGlmBlockParser_ExtractBlocks(t *testing.T) {
    parser := NewGlmBlockParser()
    content := "<glm_block>{...}</glm_block>"
    blocks := parser.ExtractBlocks(content)
    assert.Len(t, blocks, 1)
}

// CompletenessChecker测试
func TestCompletenessChecker_IsArgumentsComplete(t *testing.T) {
    checker := NewCompletenessChecker()
    args := map[string]interface{}{"url": "https://example.com"}
    raw := `{"url":"https://example.com"}`
    assert.True(t, checker.IsArgumentsComplete(args, raw))
}
```

### 2. 集成测试

测试完整的工具调用流程：

```bash
# 使用现有的测试脚本
./scripts/test_tool_format.sh
./scripts/test_tool_comprehensive.sh
```

### 3. 边界情况测试

- 空内容
- 不完整的JSON
- 超大内容
- 并发访问
- 网络中断

## 故障排查

### 常见问题

1. **工具调用未被识别**
   - 检查GLM块格式是否正确
   - 查看调试日志中的解析错误
   - 确认edit_index是否正确

2. **参数不完整**
   - 检查完整性检查逻辑
   - 查看原始参数字符串
   - 确认是否有截断迹象

3. **重复发送工具调用**
   - 检查SentStart标志
   - 确认显著改进判断逻辑
   - 查看LastSentArgs记录

### 调试日志

启用调试模式查看详细日志：

```
🔧 进入工具调用阶段
🎯 发现新工具: tool_name(id=call_xxx), 参数完整性: true
📤 发送完整工具开始: tool_name(id=call_xxx)
🔄 工具参数有实质性改进: tool_name(id=call_xxx)
📤 发送参数更新: tool_name(id=call_xxx)
🏁 检测到工具调用结束
✅ 完成工具调用: tool_name(id=call_xxx)
```

## 未来改进

1. **性能优化**
   - 使用对象池减少内存分配
   - 优化正则表达式性能
   - 实现增量JSON解析

2. **功能增强**
   - 支持更多工具调用格式
   - 添加工具调用超时机制
   - 实现工具调用重试逻辑

3. **可观测性**
   - 添加Prometheus指标
   - 实现分布式追踪
   - 增强错误报告

## 总结

本实现成功将Python版本的SSE工具调用处理器移植到Go，保持了核心逻辑的一致性，同时利用Go的特性进行了优化：

- ✅ 使用字节切片提高性能
- ✅ 使用sonic加速JSON处理
- ✅ 实现线程安全
- ✅ 保持代码简洁和可维护性
- ✅ 完全兼容OpenAI API标准

所有组件都经过了编译检查和静态分析，确保代码质量。