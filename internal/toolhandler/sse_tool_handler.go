package toolhandler

import (
	"fmt"
	"time"

	"z2api/types"

	"github.com/bytedance/sonic"
)

// ActiveToolInfo 活跃工具信息
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

// SSEToolHandler SSE工具调用处理器
// 协调ContentBuffer、GlmBlockParser和CompletenessChecker
type SSEToolHandler struct {
	chatID        string                                   // 聊天ID
	model         string                                   // 模型名称
	buffer        *ContentBuffer                           // 内容缓冲区
	parser        *GlmBlockParser                          // GLM块解析器
	checker       *CompletenessChecker                     // 完整性检查器
	activeTools   map[string]*ActiveToolInfo               // 活跃的工具调用
	toolCallUsage *types.Usage                             // 工具调用的usage信息
	hasToolCall   bool                                     // 是否有工具调用
	debugLog      func(format string, args ...interface{}) // 调试日志函数
}

// NewSSEToolHandler 创建新的SSE工具处理器
func NewSSEToolHandler(chatID, model string, debugLog func(format string, args ...interface{})) *SSEToolHandler {
	return &SSEToolHandler{
		chatID:      chatID,
		model:       model,
		buffer:      NewContentBuffer(),
		parser:      NewGlmBlockParser(),
		checker:     NewCompletenessChecker(),
		activeTools: make(map[string]*ActiveToolInfo),
		debugLog:    debugLog,
	}
}

// ProcessToolCallPhase 处理tool_call阶段
// 返回需要发送的SSE数据行
func (h *SSEToolHandler) ProcessToolCallPhase(data *types.UpstreamData) []string {
	if !h.hasToolCall {
		h.hasToolCall = true
		h.log("🔧 进入工具调用阶段")
	}

	editContent := data.Data.EditContent
	editIndex := data.Data.EditIndex

	if editContent == "" {
		return nil
	}

	// 更新内容缓冲区
	h.buffer.ApplyEdit(editIndex, editContent)

	// 尝试解析和处理工具调用
	return h.processToolCallsFromBuffer()
}

// ProcessOtherPhase 处理other阶段
// 检测工具调用结束和状态更新
func (h *SSEToolHandler) ProcessOtherPhase(data *types.UpstreamData) []string {
	editContent := data.Data.EditContent
	editIndex := data.Data.EditIndex
	usage := data.Data.Usage

	var results []string

	// 保存usage信息
	if h.hasToolCall && usage.TotalTokens > 0 {
		h.toolCallUsage = &usage
		h.log("💾 保存工具调用usage: %+v", usage)
	}

	// 如果有edit_content，继续更新内容缓冲区
	if editContent != "" {
		h.buffer.ApplyEdit(editIndex, editContent)
		// 继续处理可能的工具调用更新
		results = append(results, h.processToolCallsFromBuffer()...)
	}

	// 检测工具调用结束
	if h.hasToolCall && h.isToolCallFinished(editContent) {
		h.log("🏁 检测到工具调用结束")

		// 完成所有活跃的工具
		results = append(results, h.CompleteActiveTools()...)

		// 不在这里发送[DONE]，让外层流处理器负责
		// 重置工具调用状态
		h.hasToolCall = false
	}

	return results
}

// processToolCallsFromBuffer 从内容缓冲区中解析和处理工具调用
func (h *SSEToolHandler) processToolCallsFromBuffer() []string {
	var results []string

	// 获取当前内容
	content := h.buffer.GetContent()
	if content == "" {
		return nil
	}

	// 提取所有GLM块
	blocks := h.parser.ExtractBlocks(content)

	for _, block := range blocks {
		// 尝试解析工具调用
		toolInfo, err := h.parser.ParseToolCall(block)
		if err != nil {
			// 尝试部分解析
			toolInfo, err = h.parser.ParsePartialToolCall(block.RawContent)
			if err != nil {
				h.log("📦 工具块解析失败: %v", err)
				continue
			}
		}

		// 处理工具更新
		chunks := h.handleToolUpdate(toolInfo)
		results = append(results, chunks...)
	}

	return results
}

// handleToolUpdate 处理工具的创建或更新
func (h *SSEToolHandler) handleToolUpdate(toolInfo *ToolCallInfo) []string {
	var results []string

	// 检查参数是否完整
	isArgsComplete := h.checker.IsArgumentsComplete(toolInfo.Arguments, toolInfo.ArgsRaw)

	// 检查是否是新工具
	activeTool, exists := h.activeTools[toolInfo.ID]

	if !exists {
		// 新工具
		h.log("🎯 发现新工具: %s(id=%s), 参数完整性: %v", toolInfo.Name, toolInfo.ID, isArgsComplete)

		activeTool = &ActiveToolInfo{
			ID:           toolInfo.ID,
			Name:         toolInfo.Name,
			Arguments:    toolInfo.Arguments,
			ArgsRaw:      toolInfo.ArgsRaw,
			Status:       "active",
			SentStart:    false,
			LastSentArgs: make(map[string]interface{}),
			ArgsComplete: isArgsComplete,
			PendingSend:  true,
		}
		h.activeTools[toolInfo.ID] = activeTool

		// 只有在参数看起来完整时才发送工具开始信号
		if isArgsComplete {
			chunk := h.createToolStartChunk(toolInfo.ID, toolInfo.Name, toolInfo.Arguments)
			results = append(results, chunk)
			activeTool.SentStart = true
			activeTool.LastSentArgs = copyMap(toolInfo.Arguments)
			activeTool.PendingSend = false
			h.log("📤 发送完整工具开始: %s(id=%s)", toolInfo.Name, toolInfo.ID)
		}
	} else {
		// 更新现有工具
		// 检查是否有实质性改进
		if h.checker.IsSignificantImprovement(
			activeTool.Arguments,
			toolInfo.Arguments,
			activeTool.ArgsRaw,
			toolInfo.ArgsRaw,
		) {
			h.log("🔄 工具参数有实质性改进: %s(id=%s)", toolInfo.Name, toolInfo.ID)

			activeTool.Arguments = toolInfo.Arguments
			activeTool.ArgsRaw = toolInfo.ArgsRaw
			activeTool.ArgsComplete = isArgsComplete

			// 如果之前没有发送过开始信号，且现在参数完整，发送开始信号
			if !activeTool.SentStart && isArgsComplete {
				chunk := h.createToolStartChunk(toolInfo.ID, toolInfo.Name, toolInfo.Arguments)
				results = append(results, chunk)
				activeTool.SentStart = true
				activeTool.LastSentArgs = copyMap(toolInfo.Arguments)
				activeTool.PendingSend = false
				h.log("📤 发送延迟的工具开始: %s(id=%s)", toolInfo.Name, toolInfo.ID)
			} else if activeTool.SentStart && isArgsComplete {
				// 如果已经发送过开始信号，且参数有显著改进，发送参数更新
				if h.checker.ShouldSendArgumentUpdate(activeTool.LastSentArgs, toolInfo.Arguments) {
					chunk := h.createToolArgumentsChunk(toolInfo.ID, toolInfo.Arguments)
					results = append(results, chunk)
					activeTool.LastSentArgs = copyMap(toolInfo.Arguments)
					h.log("📤 发送参数更新: %s(id=%s)", toolInfo.Name, toolInfo.ID)
				}
			}
		}
	}

	return results
}

// CompleteActiveTools 完成所有活跃的工具调用
func (h *SSEToolHandler) CompleteActiveTools() []string {
	var results []string

	for toolID, tool := range h.activeTools {
		// 如果工具还没有发送过且参数看起来完整，现在发送
		if tool.PendingSend && !tool.SentStart && tool.ArgsComplete {
			h.log("📤 完成时发送待发送工具: %s(id=%s)", tool.Name, toolID)
			chunk := h.createToolStartChunk(toolID, tool.Name, tool.Arguments)
			results = append(results, chunk)
			tool.SentStart = true
			tool.PendingSend = false
		} else if tool.PendingSend {
			h.log("⚠️ 跳过不完整的工具: %s(id=%s)", tool.Name, toolID)
		}

		tool.Status = "completed"
		h.log("✅ 完成工具调用: %s(id=%s)", tool.Name, toolID)
	}

	// 发送工具完成信号
	if len(h.activeTools) > 0 {
		chunk := h.createToolFinishChunk()
		results = append(results, chunk)
	}

	return results
}

// isToolCallFinished 检测工具调用是否结束
func (h *SSEToolHandler) isToolCallFinished(editContent string) bool {
	if editContent == "" {
		return false
	}

	// 检测各种结束标记
	endMarkers := []string{
		"null,",
		`"status": "completed"`,
		`"is_error": false`,
	}

	for _, marker := range endMarkers {
		if contains(editContent, marker) {
			h.log("🔍 检测到结束标记: %s", marker)
			return true
		}
	}

	// 检查是否所有工具都有完整的结构
	content := h.buffer.GetContent()
	if len(h.activeTools) > 0 && contains(content, `"status": "completed"`) {
		return true
	}

	return false
}

// createToolStartChunk 创建工具调用开始的chunk
func (h *SSEToolHandler) createToolStartChunk(toolID, toolName string, arguments map[string]interface{}) string {
	argsStr, _ := sonic.MarshalString(arguments)

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
		"id":      h.chatID,
		"model":   h.model,
		"object":  "chat.completion.chunk",
	}

	jsonData, _ := sonic.MarshalString(chunk)
	return fmt.Sprintf("data: %s", jsonData)
}

// createToolArgumentsChunk 创建工具参数的chunk
func (h *SSEToolHandler) createToolArgumentsChunk(toolID string, arguments map[string]interface{}) string {
	argsStr, _ := sonic.MarshalString(arguments)

	chunk := map[string]interface{}{
		"choices": []map[string]interface{}{
			{
				"delta": map[string]interface{}{
					"tool_calls": []map[string]interface{}{
						{
							"id": toolID,
							"function": map[string]interface{}{
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
		"id":      h.chatID,
		"model":   h.model,
		"object":  "chat.completion.chunk",
	}

	jsonData, _ := sonic.MarshalString(chunk)
	return fmt.Sprintf("data: %s", jsonData)
}

// createToolFinishChunk 创建工具调用完成的chunk
func (h *SSEToolHandler) createToolFinishChunk() string {
	chunk := map[string]interface{}{
		"choices": []map[string]interface{}{
			{
				"delta":         map[string]interface{}{},
				"finish_reason": "tool_calls",
				"index":         0,
			},
		},
		"created": time.Now().Unix(),
		"id":      h.chatID,
		"model":   h.model,
		"object":  "chat.completion.chunk",
	}

	// 只在有finish_reason时添加usage
	if h.toolCallUsage != nil {
		chunk["usage"] = h.toolCallUsage
	}

	jsonData, _ := sonic.MarshalString(chunk)
	return fmt.Sprintf("data: %s", jsonData)
}

// log 调试日志
func (h *SSEToolHandler) log(format string, args ...interface{}) {
	if h.debugLog != nil {
		h.debugLog(format, args...)
	}
}

// Reset 重置处理器状态
func (h *SSEToolHandler) Reset() {
	h.buffer.Reset()
	h.activeTools = make(map[string]*ActiveToolInfo)
	h.toolCallUsage = nil
	h.hasToolCall = false
}

// 辅助函数

// copyMap 复制map
func copyMap(src map[string]interface{}) map[string]interface{} {
	dst := make(map[string]interface{}, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

// contains 检查字符串是否包含子串
func contains(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 &&
		(s == substr || len(s) >= len(substr) &&
			(s[:len(substr)] == substr || s[len(s)-len(substr):] == substr ||
				findSubstring(s, substr)))
}

// findSubstring 查找子串
func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
