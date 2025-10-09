# main.go 改进建议 - 基于 chat_service.py 的优秀实践

## 分析总结

通过对比 `chat_service.py` 的实现，发现了以下可以借鉴的优秀实践点：

## 1. 统一的响应格式创建函数

### Python 实现的优点
`chat_service.py` 使用了统一的 `create_chat_completion_data()` 函数来创建所有响应格式：

```python
def create_chat_completion_data(content, model, timestamp, phase, usage=None, finish_reason=None)
```

### 建议改进
在 Go 中创建类似的统一响应创建函数：

```go
// createChatCompletionChunk 统一创建聊天完成响应块
func createChatCompletionChunk(content string, model string, phase string, usage *Usage, finishReason string) OpenAIResponse {
    timestamp := time.Now().Unix()
    response := OpenAIResponse{
        ID:      fmt.Sprintf("chatcmpl-%d", timestamp),
        Object:  "chat.completion.chunk",
        Created: timestamp,
        Model:   model,
        Choices: []Choice{{Index: 0}},
    }

    switch phase {
    case "thinking":
        response.Choices[0].Delta = Delta{ReasoningContent: content, Role: "assistant"}
    case "answer":
        response.Choices[0].Delta = Delta{Content: content, Role: "assistant"}
    case "tool_call":
        response.Choices[0].Delta = Delta{Content: content, Role: "assistant"}
    case "other":
        response.Choices[0].Delta = Delta{Content: content, Role: "assistant"}
        response.Choices[0].FinishReason = finishReason
        response.Usage = usage
    }

    return response
}
```

## 2. 更清晰的流式响应相位处理

### Python 实现的优点
Python 版本对不同相位的处理逻辑非常清晰，每个相位有明确的处理流程：

- thinking 相位：处理 reasoning_content
- answer 相位：处理主要内容
- other 相位：处理使用统计
- tool_call 相位：处理工具调用

### 建议改进
重构 Go 的流式处理逻辑，使相位处理更清晰：

```go
// processPhase 处理不同的响应相位
func processPhase(phase string, data *UpstreamData, w http.ResponseWriter, flusher http.Flusher, model string) {
    switch phase {
    case "thinking":
        processThinkingPhase(data, w, flusher, model)
    case "answer":
        processAnswerPhase(data, w, flusher, model)
    case "tool_call":
        processToolCallPhase(data, w, flusher, model)
    case "other":
        processOtherPhase(data, w, flusher, model)
    case "done":
        processDonePhase(w, flusher, model)
    }
}
```

## 3. 多模态消息转换的优化

### Python 实现的优点
`convert_messages()` 函数清晰地处理了多模态内容，分离了文本和图像：

```python
def convert_messages(messages):
    trans_messages = []
    image_urls = []
    # 分别处理文本和图像内容
```

### 建议改进
优化 Go 中的消息转换函数：

```go
type ConvertedMessage struct {
    Messages  []UpstreamMessage
    ImageURLs []string
    Files     []File
}

func convertMultimodalMessages(messages []Message) ConvertedMessage {
    var result ConvertedMessage

    for _, msg := range messages {
        // 处理字符串内容
        if content, ok := msg.Content.(string); ok {
            result.Messages = append(result.Messages, UpstreamMessage{
                Role:    msg.Role,
                Content: content,
            })
            continue
        }

        // 处理数组内容（多模态）
        if parts, ok := msg.Content.([]interface{}); ok {
            for _, part := range parts {
                if partMap, ok := part.(map[string]interface{}); ok {
                    switch partMap["type"] {
                    case "text":
                        // 处理文本
                    case "image_url":
                        // 处理图像URL
                    }
                }
            }
        }
    }

    return result
}
```

## 4. 特殊标签的精确处理

### Python 实现的优点
Python 版本对 thinking 相位中的 `</summary>` 和 `</details>` 标签有精确的处理：

```python
if "</summary>\n" in content:
    content = content.split("</summary>\n")[-1]
```

### 建议改进
在 Go 中添加更精确的标签处理：

```go
// processThinkingContent 处理思考内容中的特殊标签
func processThinkingContent(content string) string {
    // 移除 summary 标签后的内容
    if idx := strings.Index(content, "</summary>\n"); idx != -1 {
        content = content[idx+len("</summary>\n"):]
    }

    // 处理 details 标签
    if strings.Contains(content, "</details>") {
        content = strings.ReplaceAll(content, "</details>", "</think>")
    }

    return content
}
```

## 5. 动态特性配置

### Python 实现的优点
`getfeatures()` 函数根据模型动态返回特性配置，结构清晰：

```python
def getfeatures(model: str, streaming: bool):
    features = {
        "enable_thinking": True,
        "web_search": False,
        # ...
    }
```

### 建议改进
创建更灵活的特性管理系统：

```go
type Features struct {
    ImageGeneration bool   `json:"image_generation"`
    WebSearch      bool   `json:"web_search"`
    AutoWebSearch  bool   `json:"auto_web_search"`
    PreviewMode    bool   `json:"preview_mode"`
    EnableThinking bool   `json:"enable_thinking"`
    MCPServers     []string `json:"mcp_servers,omitempty"`
}

func getModelFeatures(modelID string, streaming bool) Features {
    features := Features{
        EnableThinking: streaming, // 流式默认启用思考
    }

    switch modelID {
    case "glm-4.6-search", "glm-4.6-advanced-search":
        features.WebSearch = true
        features.AutoWebSearch = true
        features.PreviewMode = true
    case "glm-4.6-nothinking":
        features.EnableThinking = false
    }

    return features
}
```

## 6. 工具调用的结构化处理

### 建议改进
虽然 Python 版本注释了工具调用部分，但其结构值得借鉴：

```go
// ToolCallManager 管理工具调用的状态
type ToolCallManager struct {
    calls map[int]*ToolCall
    mu    sync.Mutex
}

func (m *ToolCallManager) AddToolCall(index int, call *ToolCall) {
    m.mu.Lock()
    defer m.mu.Unlock()
    m.calls[index] = call
}

func (m *ToolCallManager) GetSortedCalls() []ToolCall {
    m.mu.Lock()
    defer m.mu.Unlock()

    var result []ToolCall
    for _, call := range m.calls {
        result = append(result, *call)
    }

    sort.Slice(result, func(i, j int) bool {
        return result[i].Index < result[j].Index
    })

    return result
}
```

## 7. 错误处理和重试机制的优化

### 建议改进
结合两个实现的优点，创建更健壮的错误处理：

```go
type RetryConfig struct {
    MaxRetries  int
    BaseDelay   time.Duration
    MaxDelay    time.Duration
    RetryableErrors []int  // HTTP状态码
}

func withRetry(config RetryConfig, fn func() (*http.Response, error)) (*http.Response, error) {
    // 实现统一的重试逻辑
}
```

## 实施优先级

1. **高优先级**：
   - 创建统一的响应格式创建函数
   - 优化流式响应的相位处理逻辑
   - 改进特殊标签的处理

2. **中优先级**：
   - 重构多模态消息转换
   - 实现动态特性配置系统

3. **低优先级**：
   - 优化工具调用的管理
   - 统一错误处理和重试机制

## 性能考虑

在实施这些改进时，需要保持 Go 版本的性能优势：
- 继续使用 sonic JSON 库
- 保持对象池的使用
- 维护并发控制机制
- 保留压缩支持

这些改进将使代码更加清晰、可维护，同时保持高性能的特点。