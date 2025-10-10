package main

import (
	"sort"
	"strings"
	"time"
	"z2api/types"
	"z2api/utils"
	"github.com/gin-gonic/gin"
)

// SetSSEHeaders 设置SSE（Server-Sent Events）响应头
func SetSSEHeaders(c *gin.Context) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
}

// ResponsePhase 定义响应的不同阶段
type ResponsePhase string

const (
	PhaseThinking ResponsePhase = "thinking"
	PhaseAnswer   ResponsePhase = "answer"
	PhaseToolCall ResponsePhase = "tool_call"
	PhaseOther    ResponsePhase = "other"
	PhaseDone     ResponsePhase = "done"
)

// createChatCompletionChunk 统一创建聊天完成响应块
// 根据不同的phase创建对应格式的响应，保持响应格式的一致性
func createChatCompletionChunk(content string, model string, phase ResponsePhase, usage *types.Usage, finishReason string) types.OpenAIResponse{
	timestamp := time.Now().Unix()
	response := types.OpenAIResponse{
		ID:      utils.GenerateChatCompletionID(),
		Object:  "chat.completion.chunk",
		Created: timestamp,
		Model:   model,
		Choices: []types.Choice{{Index: 0}},
	}

	switch phase {
	case PhaseThinking:
		// 思考阶段：设置reasoning_content
		response.Choices[0].Delta = types.Delta{
			ReasoningContent: content,
			Role:             "assistant",
		}
	case PhaseAnswer:
		// 回答阶段：设置普通content
		response.Choices[0].Delta = types.Delta{
			Content: content,
			Role:    "assistant",
		}
	case PhaseToolCall:
		// 工具调用阶段：设置content（工具相关内容）
		response.Choices[0].Delta = types.Delta{
			Content: content,
			Role:    "assistant",
		}
	case PhaseOther:
		// 其他阶段：包含结束原因和使用统计
		response.Choices[0].Delta = types.Delta{
			Content: content,
			Role:    "assistant",
		}
		if finishReason != "" {
			response.Choices[0].FinishReason = finishReason
		}
		if usage != nil {
			response.Usage = *usage
		}
	case PhaseDone:
		// 完成阶段：仅设置结束原因
		response.Choices[0].Delta = types.Delta{}
		if finishReason != "" {
			response.Choices[0].FinishReason = finishReason
		}
	}

	return response
}

// createToolCallChunk 创建工具调用响应块
func createToolCallChunk(toolCalls []types.ToolCall, model string, finishReason string) types.OpenAIResponse{
	timestamp := time.Now().Unix()
	return types.OpenAIResponse{
		ID:      utils.GenerateChatCompletionID(),
		Object:  "chat.completion.chunk",
		Created: timestamp,
		Model:   model,
		Choices: []types.Choice{
			{
				Index: 0,
				Delta: types.Delta{
					Role:      "assistant",
					ToolCalls: normalizeToolCalls(toolCalls),
				},
				FinishReason: finishReason,
			},
		},
	}
}

// processThinkingContent 处理思考内容中的特殊标签
// 移除summary标签，转换details标签为think标签
func processThinkingContent(content string) string {
	if content == "" {
		return content
	}

	// 移除 summary 标签后的内容
	if idx := strings.Index(content, "</summary>\n"); idx != -1 {
		content = content[idx+len("</summary>\n"):]
	}

	// 处理 details 标签
	if strings.Contains(content, "</details>") {
		content = strings.ReplaceAll(content, "</details>", "</think>")
	}

	// 移除可能的Full标签
	content = strings.ReplaceAll(content, "<Full>", "")
	content = strings.ReplaceAll(content, "</Full>", "")

	// 清理多余的引用符号
	content = strings.ReplaceAll(content, "\n> ", "\n")

	return content
}

// processAnswerContent 处理回答内容中的特殊标签
func processAnswerContent(content string, editContent string) string {
	// 优先使用edit_content
	if editContent != "" {
		content = editContent
		// 如果edit_content包含summary标签，提取其后的内容
		if strings.Contains(content, "</summary>\n") {
			parts := strings.Split(content, "</details>")
			if len(parts) > 1 {
				content = parts[len(parts)-1]
			}
		}
	}

	// 清理特殊标签
	content = strings.TrimPrefix(content, "<Full>")
	content = strings.TrimSuffix(content, "</Full>")

	return content
}

// ToolCallManager 管理工具调用的状态
type ToolCallManager struct {
	calls map[int]*types.ToolCall
}

// NewToolCallManager 创建新的工具调用管理器
func NewToolCallManager() *ToolCallManager {
	return &ToolCallManager{
		calls: make(map[int]*types.ToolCall),
	}
}

// AddToolCall 添加或更新工具调用
func (m *ToolCallManager) AddToolCall(index int, call types.ToolCall) {
	m.calls[index] = &call
}

// AddToolCalls 批量添加工具调用
func (m *ToolCallManager) AddToolCalls(calls []types.ToolCall) {
	for i := range calls {
		m.AddToolCall(calls[i].Index, calls[i])
	}
}

// GetSortedCalls 获取排序后的工具调用列表
func (m *ToolCallManager) GetSortedCalls() []types.ToolCall{
	if len(m.calls) == 0 {
		return nil
	}

	// 转换为切片
	result := make([]types.ToolCall, 0, len(m.calls))
	for _, call := range m.calls {
		if call != nil {
			result = append(result, *call)
		}
	}

	// 使用标准库的高效排序
	sort.Slice(result, func(i, j int) bool {
		return result[i].Index < result[j].Index
	})

	return result
}

// HasCalls 检查是否有工具调用
func (m *ToolCallManager) HasCalls() bool {
	return len(m.calls) > 0
}

// Clear 清空工具调用
func (m *ToolCallManager) Clear() {
	m.calls = make(map[int]*types.ToolCall)
}
