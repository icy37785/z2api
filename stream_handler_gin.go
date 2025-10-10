package main

import (
	"bufio"
	"fmt"
	"io"
	"strings"
	"time"

	"z2api/internal/toolhandler"
	"z2api/types"

	"github.com/gin-gonic/gin"
)

// GinStreamHandler 基于 Gin 的流式响应处理器
type GinStreamHandler struct {
	ctx             *gin.Context
	model           string
	toolCallMgr     *ToolCallManager
	sseToolHandler  *toolhandler.SSEToolHandler // 新增：SSE工具处理器
	inThinkingPhase bool
	sentFinish      bool
}

// NewGinStreamHandler 创建新的 Gin 流式响应处理器
func NewGinStreamHandler(c *gin.Context, model string) *GinStreamHandler {
	// 生成chatID（使用请求ID或生成新的）
	chatID := c.GetString("RequestID")
	if chatID == "" {
		chatID = fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	}

	return &GinStreamHandler{
		ctx:            c,
		model:          model,
		toolCallMgr:    NewToolCallManager(),
		sseToolHandler: toolhandler.NewSSEToolHandler(chatID, model, debugLog),
	}
}

// WriteChunk 使用 Gin 的 SSEvent 写入响应块
func (h *GinStreamHandler) WriteChunk(chunk types.OpenAIResponse) {
	// 使用 Gin 的流式写入
	h.ctx.SSEvent("message", chunk)
}

// WriteSSEData 写入原始 SSE 数据
func (h *GinStreamHandler) WriteSSEData(data string) {
	h.ctx.Writer.WriteString(fmt.Sprintf("data: %s\n\n", data))
	h.ctx.Writer.Flush()
}

// ProcessThinkingPhase 处理思考阶段
func (h *GinStreamHandler) ProcessThinkingPhase(data *types.UpstreamData) {
	if !h.inThinkingPhase {
		h.inThinkingPhase = true
	}

	if data.Data.DeltaContent != "" {
		// 处理思考内容中的特殊标签
		content := processThinkingContent(data.Data.DeltaContent)
		content = transformThinking(content)

		if content != "" {
			chunk := createChatCompletionChunk(content, h.model, PhaseThinking, nil, "")
			// 转换为 SSE 格式
			if jsonData, err := sonicStream.Marshal(chunk); err == nil {
				h.WriteSSEData(string(jsonData))
			}
		}
	}
}

// ProcessAnswerPhase 处理回答阶段
func (h *GinStreamHandler) ProcessAnswerPhase(data *types.UpstreamData) {
	content := data.Data.DeltaContent

	// 处理edit_content（如果存在）
	if data.Data.EditContent != "" {
		content = processAnswerContent(data.Data.DeltaContent, data.Data.EditContent)
	}

	if content != "" {
		chunk := createChatCompletionChunk(content, h.model, PhaseAnswer, nil, "")
		if jsonData, err := sonicStream.Marshal(chunk); err == nil {
			h.WriteSSEData(string(jsonData))
		}
	}
}

// ProcessToolCallPhase 处理工具调用阶段
func (h *GinStreamHandler) ProcessToolCallPhase(data *types.UpstreamData) {
	// 使用新的SSEToolHandler处理工具调用
	chunks := h.sseToolHandler.ProcessToolCallPhase(data)
	for _, chunk := range chunks {
		h.ctx.Writer.WriteString(chunk + "\n\n")
		h.ctx.Writer.Flush()
	}

	// 如果有原生的tool_calls（向后兼容）
	if len(data.Data.ToolCalls) > 0 {
		h.toolCallMgr.AddToolCalls(data.Data.ToolCalls)
	}

	// 处理工具调用相关的文本内容
	if data.Data.DeltaContent != "" {
		chunk := createChatCompletionChunk(data.Data.DeltaContent, h.model, PhaseToolCall, nil, "")
		if jsonData, err := sonicStream.Marshal(chunk); err == nil {
			h.WriteSSEData(string(jsonData))
		}
	}
}

// ProcessOtherPhase 处理其他阶段
func (h *GinStreamHandler) ProcessOtherPhase(data *types.UpstreamData) {
	// 使用新的SSEToolHandler处理other阶段（可能包含工具调用结束信号）
	chunks := h.sseToolHandler.ProcessOtherPhase(data)
	hasToolFinish := false
	for _, chunk := range chunks {
		h.ctx.Writer.WriteString(chunk + "\n\n")
		h.ctx.Writer.Flush()
		// 检查是否包含工具完成信号
		if strings.Contains(chunk, `"finish_reason":"tool_calls"`) {
			hasToolFinish = true
		}
	}

	// 如果SSEToolHandler已经处理了工具调用结束，标记为已完成
	if hasToolFinish {
		h.sentFinish = true
		return
	}

	// 如果SSEToolHandler返回了其他chunks，也直接返回
	if len(chunks) > 0 {
		return
	}

	// 否则按原逻辑处理
	content := data.Data.DeltaContent
	var usage *types.Usage

	// 提取使用统计
	if data.Data.Usage.TotalTokens > 0 {
		usage = &data.Data.Usage
	}

	// 确定结束原因
	finishReason := ""
	if data.Data.Phase == "done" || data.Data.Done {
		finishReason = "stop"
		if h.toolCallMgr.HasCalls() {
			finishReason = "tool_calls"
		}
	}

	if content != "" || usage != nil || finishReason != "" {
		chunk := createChatCompletionChunk(content, h.model, PhaseOther, usage, finishReason)
		if jsonData, err := sonicStream.Marshal(chunk); err == nil {
			h.WriteSSEData(string(jsonData))
		}
	}
}

// ProcessPhase 根据阶段处理数据
func (h *GinStreamHandler) ProcessPhase(data *types.UpstreamData) {
	if data == nil {
		return
	}

	phase := data.Data.Phase

	switch phase {
	case "thinking":
		h.ProcessThinkingPhase(data)
	case "answer":
		h.ProcessAnswerPhase(data)
	case "tool_call":
		h.ProcessToolCallPhase(data)
	case "done":
		h.ProcessDonePhase(data)
	default:
		// 其他阶段或未知阶段
		if phase == "other" || data.Data.DeltaContent != "" {
			h.ProcessOtherPhase(data)
		}
	}
}

// ProcessDonePhase 处理完成阶段
func (h *GinStreamHandler) ProcessDonePhase(data *types.UpstreamData) {
	if h.sentFinish {
		return
	}

	// 如果在思考阶段结束，不再单独发送闭合标签
	// 闭合标签应该已经在思考内容中包含
	if h.inThinkingPhase {
		h.inThinkingPhase = false
	}

	// 检查是否有工具调用需要完成
	finishReason := "stop"
	if h.toolCallMgr.HasCalls() {
		finishReason = "tool_calls"
	}

	finishChunk := createChatCompletionChunk("", h.model, PhaseDone, nil, finishReason)
	if jsonData, err := sonicStream.Marshal(finishChunk); err == nil {
		h.WriteSSEData(string(jsonData))
	}

	// 发送最终的[DONE]信号
	h.WriteSSEData("[DONE]")
	h.sentFinish = true
}

// HandleGinStreamResponse 使用 Gin Context 处理完整的流式响应
func HandleGinStreamResponse(c *gin.Context, resp *io.ReadCloser, model string) error {
	// 设置 SSE 响应头
	SetSSEHeaders(c)

	// 创建流处理器
	handler := NewGinStreamHandler(c, model)

	// 创建缓冲读取器
	bufReader := bufio.NewReader(*resp)

	debugLog("开始处理流式响应 (Gin版)，模型：%s", model)

	// 使用 Gin 的 Stream 方法处理流式数据
	c.Stream(func(w io.Writer) bool {
		// 检查客户端连接
		select {
		case <-c.Request.Context().Done():
			debugLog("客户端断开连接，停止处理")
			return false
		default:
		}

		// 读取一行
		line, err := bufReader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				debugLog("到达流末尾")
				if !handler.sentFinish {
					handler.ProcessDonePhase(nil)
				}
				return false
			}
			debugLog("读取SSE行失败: %v", err)
			return false
		}

		line = strings.TrimSpace(line)
		if line == "" {
			return true // 继续处理
		}

		// 处理SSE数据行
		if strings.HasPrefix(line, "data: ") {
			dataStr := strings.TrimPrefix(line, "data: ")

			// 检查是否为结束标记
			if dataStr == "[DONE]" {
				debugLog("收到[DONE]标记")
				if !handler.sentFinish {
					handler.ProcessDonePhase(nil)
				}
				return false
			}

			// 解析JSON数据
			var upstreamData types.UpstreamData
			if err := sonicStream.UnmarshalFromString(dataStr, &upstreamData); err != nil {
				debugLog("解析上游数据失败: %v", err)
				return true // 继续处理
			}

			// 处理数据
			handler.ProcessPhase(&upstreamData)

			// 检查是否完成
			if upstreamData.Data.Done || upstreamData.Data.Phase == "done" {
				debugLog("收到完成信号")
				if !handler.sentFinish {
					handler.ProcessDonePhase(&upstreamData)
				}
				return false
			}
		}

		return true // 继续处理
	})

	return nil
}

// GinStreamAggregator 基于 Gin 的流聚合器（用于非流式响应）
type GinStreamAggregator struct {
	Content          strings.Builder
	ReasoningContent strings.Builder
	ToolCallMgr      *ToolCallManager
	Usage            *types.Usage
	Error            error
	ErrorDetail      string
}

// NewGinStreamAggregator 创建 Gin 流聚合器
func NewGinStreamAggregator() *GinStreamAggregator {
	return &GinStreamAggregator{
		ToolCallMgr: NewToolCallManager(),
	}
}

// ProcessLine 处理单行SSE数据并聚合
func (a *GinStreamAggregator) ProcessLine(line string) bool {
	line = strings.TrimSpace(line)
	if line == "" {
		return true // 继续处理
	}

	if strings.HasPrefix(line, "data: ") {
		dataStr := strings.TrimPrefix(line, "data: ")

		if dataStr == "[DONE]" {
			return false // 结束处理
		}

		var upstreamData types.UpstreamData
		if err := sonicStream.UnmarshalFromString(dataStr, &upstreamData); err != nil {
			debugLog("解析上游数据失败: %v", err)
			return true
		}

		// 根据阶段聚合数据
		switch upstreamData.Data.Phase {
		case "thinking":
			if upstreamData.Data.DeltaContent != "" {
				content := processThinkingContent(upstreamData.Data.DeltaContent)
				a.ReasoningContent.WriteString(content)
			}
		case "answer":
			content := upstreamData.Data.DeltaContent
			if upstreamData.Data.EditContent != "" {
				content = processAnswerContent(content, upstreamData.Data.EditContent)
			}
			if content != "" {
				a.Content.WriteString(content)
			}
		case "tool_call":
			if len(upstreamData.Data.ToolCalls) > 0 {
				a.ToolCallMgr.AddToolCalls(upstreamData.Data.ToolCalls)
			}
		case "other":
			if upstreamData.Data.DeltaContent != "" {
				a.Content.WriteString(upstreamData.Data.DeltaContent)
			}
			if upstreamData.Data.Usage.TotalTokens > 0 {
				a.Usage = &upstreamData.Data.Usage
			}
		}

		// 检查是否完成
		if upstreamData.Data.Done || upstreamData.Data.Phase == "done" {
			if upstreamData.Data.Usage.TotalTokens > 0 {
				a.Usage = &upstreamData.Data.Usage
			}
			return false
		}
	}

	return true // 继续处理
}

// GetResult 获取聚合结果
func (a *GinStreamAggregator) GetResult() (string, string, []types.ToolCall, *types.Usage) {
	// 修复未闭合的think标签
	reasoningContent := a.ReasoningContent.String()
	if reasoningContent != "" {
		reasoningContent = fixUnclosedThinkTags(reasoningContent)
	}

	return a.Content.String(), reasoningContent, a.ToolCallMgr.GetSortedCalls(), a.Usage
}
