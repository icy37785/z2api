package main

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// StreamHandler 流式响应处理器
type StreamHandler struct {
	writer          http.ResponseWriter
	flusher         http.Flusher
	model           string
	toolCallMgr     *ToolCallManager
	inThinkingPhase bool
	sentFinish      bool
}

// NewStreamHandler 创建新的流式响应处理器
func NewStreamHandler(w http.ResponseWriter, model string) *StreamHandler {
	flusher, _ := w.(http.Flusher)
	return &StreamHandler{
		writer:      w,
		flusher:     flusher,
		model:       model,
		toolCallMgr: NewToolCallManager(),
	}
}

// WriteChunk 写入响应块
func (h *StreamHandler) WriteChunk(chunk OpenAIResponse) {
	writeSSEChunk(h.writer, chunk)
	if h.flusher != nil {
		h.flusher.Flush()
	}
}

// ProcessThinkingPhase 处理思考阶段
func (h *StreamHandler) ProcessThinkingPhase(data *UpstreamData) {
	if !h.inThinkingPhase {
		h.inThinkingPhase = true
	}

	if data.Data.DeltaContent != "" {
		// 处理思考内容中的特殊标签
		content := processThinkingContent(data.Data.DeltaContent)

		// 使用transformThinking进行额外转换（保留原有逻辑）
		content = transformThinking(content)

		if content != "" {
			chunk := createChatCompletionChunk(content, h.model, PhaseThinking, nil, "")
			h.WriteChunk(chunk)
		}
	}
}

// ProcessAnswerPhase 处理回答阶段
func (h *StreamHandler) ProcessAnswerPhase(data *UpstreamData) {
	content := data.Data.DeltaContent

	// 处理edit_content（如果存在）
	if data.Data.EditContent != "" {
		content = processAnswerContent(data.Data.DeltaContent, data.Data.EditContent)
	}

	if content != "" {
		chunk := createChatCompletionChunk(content, h.model, PhaseAnswer, nil, "")
		h.WriteChunk(chunk)
	}
}

// ProcessToolCallPhase 处理工具调用阶段
func (h *StreamHandler) ProcessToolCallPhase(data *UpstreamData) {
	if len(data.Data.ToolCalls) > 0 {
		// 添加工具调用到管理器
		h.toolCallMgr.AddToolCalls(data.Data.ToolCalls)

		// 创建工具调用响应块
		chunk := createToolCallChunk(data.Data.ToolCalls, h.model, "")
		h.WriteChunk(chunk)
	}

	// 处理工具调用相关的文本内容
	if data.Data.DeltaContent != "" {
		chunk := createChatCompletionChunk(data.Data.DeltaContent, h.model, PhaseToolCall, nil, "")
		h.WriteChunk(chunk)
	}
}

// ProcessOtherPhase 处理其他阶段
func (h *StreamHandler) ProcessOtherPhase(data *UpstreamData) {
	content := data.Data.DeltaContent
	var usage *Usage

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
		h.WriteChunk(chunk)
	}
}

// ProcessPhase 根据阶段处理数据
func (h *StreamHandler) ProcessPhase(data *UpstreamData) {
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
func (h *StreamHandler) ProcessDonePhase(data *UpstreamData) {
	if h.sentFinish {
		return
	}

	// 如果在思考阶段结束，发送闭合标签
	if h.inThinkingPhase {
		closingChunk := createChatCompletionChunk("</think>", h.model, PhaseThinking, nil, "")
		h.WriteChunk(closingChunk)
		h.inThinkingPhase = false
	}

	// 检查是否有工具调用需要完成
	if h.toolCallMgr.HasCalls() {
		finishChunk := createChatCompletionChunk("", h.model, PhaseDone, nil, "tool_calls")
		h.WriteChunk(finishChunk)
	} else {
		// 发送普通完成信号
		finishChunk := createChatCompletionChunk("", h.model, PhaseDone, nil, "stop")
		h.WriteChunk(finishChunk)
	}

	// 发送最终的[DONE]信号
	fmt.Fprintf(h.writer, "data: [DONE]\n\n")
	if h.flusher != nil {
		h.flusher.Flush()
	}

	h.sentFinish = true
}

// HandleStreamResponse 处理完整的流式响应
func HandleStreamResponse(w http.ResponseWriter, resp *http.Response, model string, checkClient func() bool) error {
	// 创建流处理器
	handler := NewStreamHandler(w, model)

	// 创建缓冲读取器
	bufReader := bufio.NewReader(resp.Body)

	debugLog("开始处理流式响应，模型：%s", model)

	for {
		// 检查客户端连接
		if !checkClient() {
			debugLog("客户端断开连接，停止处理")
			return nil
		}

		// 读取一行
		line, err := bufReader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				debugLog("到达流末尾")
				// 确保发送完成信号
				if !handler.sentFinish {
					handler.ProcessDonePhase(nil)
				}
				break
			}
			debugLog("读取SSE行失败: %v", err)
			return err
		}

		line = strings.TrimSpace(line)
		if line == "" {
			continue
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
				break
			}

			// 解析JSON数据
			var upstreamData UpstreamData
			if err := sonicStream.UnmarshalFromString(dataStr, &upstreamData); err != nil {
				debugLog("解析上游数据失败: %v, 原始数据: %s", err, dataStr)
				continue
			}

			// 处理数据
			handler.ProcessPhase(&upstreamData)

			// 检查是否完成
			if upstreamData.Data.Done || upstreamData.Data.Phase == "done" {
				debugLog("收到完成信号")
				if !handler.sentFinish {
					handler.ProcessDonePhase(&upstreamData)
				}
				break
			}
		}
	}

	return nil
}

// AggregateStreamResponse 聚合流式响应为非流式格式
type StreamAggregator struct {
	Content          strings.Builder
	ReasoningContent strings.Builder
	ToolCallMgr      *ToolCallManager
	Usage            *Usage
	Error            error
	ErrorDetail      string
}

// NewStreamAggregator 创建流聚合器
func NewStreamAggregator() *StreamAggregator {
	return &StreamAggregator{
		ToolCallMgr: NewToolCallManager(),
	}
}

// ProcessLine 处理单行SSE数据并聚合
func (a *StreamAggregator) ProcessLine(line string) bool {
	line = strings.TrimSpace(line)
	if line == "" {
		return true // 继续处理
	}

	if strings.HasPrefix(line, "data: ") {
		dataStr := strings.TrimPrefix(line, "data: ")

		if dataStr == "[DONE]" {
			return false // 结束处理
		}

		var upstreamData UpstreamData
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
func (a *StreamAggregator) GetResult() (string, string, []ToolCall, *Usage) {
	// 修复未闭合的think标签
	reasoningContent := a.ReasoningContent.String()
	if reasoningContent != "" {
		reasoningContent = fixUnclosedThinkTags(reasoningContent)
	}

	return a.Content.String(), reasoningContent, a.ToolCallMgr.GetSortedCalls(), a.Usage
}
