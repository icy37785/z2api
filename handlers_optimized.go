package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"z2api/internal/mapper"
)

// handleStreamResponseOptimized 优化版的流式响应处理函数
func handleStreamResponseOptimized(w http.ResponseWriter, r *http.Request, upstreamReq UpstreamRequest, chatID string, authToken string, modelName string, startTime time.Time, sessionID string) {
	userAgent := r.Header.Get("User-Agent")
	debugLog("开始处理流式响应 (优化版) (chat_id=%s, model=%s)", chatID, upstreamReq.Model)

	// 调用上游API
	resp, cancel, err := callUpstreamWithRetry(upstreamReq, chatID, authToken, sessionID)
	if err != nil {
		duration := float64(time.Since(startTime)) / float64(time.Millisecond)
		recordRequestStats(startTime, r.URL.Path, http.StatusBadGateway, 0, modelName, true)
		addLiveRequest(r.Method, r.URL.Path, http.StatusBadGateway, duration, userAgent, modelName)
		globalErrorHandler.HandleAPIError(w, http.StatusBadGateway, "upstream_error",
			"Failed to call upstream after retries", "调用上游失败: %v", err)
		return
	}
	defer func() {
		cancel()
		resp.Body.Close()
	}()

	// 检查响应状态
	if resp.StatusCode != http.StatusOK {
		duration := float64(time.Since(startTime)) / float64(time.Millisecond)
		recordRequestStats(startTime, r.URL.Path, http.StatusBadGateway, 0, modelName, true)
		addLiveRequest(r.Method, r.URL.Path, http.StatusBadGateway, duration, userAgent, modelName)

		body, _ := io.ReadAll(resp.Body)
		globalErrorHandler.HandleAPIError(w, http.StatusBadGateway, "upstream_error",
			"Upstream error", "上游返回错误状态: %d, 响应: %s", resp.StatusCode, string(body))
		return
	}

	// 设置SSE响应头
	setCORSHeaders(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		duration := float64(time.Since(startTime)) / float64(time.Millisecond)
		recordRequestStats(startTime, r.URL.Path, http.StatusInternalServerError, 0, modelName, true)
		addLiveRequest(r.Method, r.URL.Path, http.StatusInternalServerError, duration, userAgent, modelName)
		globalErrorHandler.HandleAPIError(w, http.StatusInternalServerError, "streaming_unsupported",
			"Streaming not supported by server", "Streaming不受支持")
		return
	}

	// 发送初始块
	firstChunk := OpenAIResponse{
		ID:      fmt.Sprintf("chatcmpl-%d", time.Now().Unix()),
		Object:  "chat.completion.chunk",
		Created: time.Now().Unix(),
		Model:   upstreamReq.Model,
		Choices: []Choice{{
			Index: 0,
			Delta: Delta{Role: "assistant"},
		}},
	}
	writeSSEChunk(w, firstChunk)
	flusher.Flush()

	// 使用新的流处理器处理响应
	checkClient := func() bool {
		select {
		case <-r.Context().Done():
			return false
		default:
			return true
		}
	}

	if err := HandleStreamResponse(w, resp, modelName, checkClient); err != nil {
		debugLog("流式响应处理错误: %v", err)
		// 尝试发送错误信息给客户端
		globalErrorHandler.HandleStreamError(w, flusher, modelName, fmt.Sprintf("流处理错误: %v", err))
	}

	// 记录统计
	duration := float64(time.Since(startTime)) / float64(time.Millisecond)
	recordRequestStats(startTime, r.URL.Path, http.StatusOK, 0, modelName, true)
	addLiveRequest(r.Method, r.URL.Path, http.StatusOK, duration, userAgent, modelName)

	debugLog("流式响应处理完成")
}

// handleNonStreamResponseOptimized 优化版的非流式响应处理函数
func handleNonStreamResponseOptimized(w http.ResponseWriter, r *http.Request, upstreamReq UpstreamRequest, chatID string, authToken string, modelName string, startTime time.Time, sessionID string) {
	userAgent := r.Header.Get("User-Agent")
	debugLog("开始处理非流式响应 (优化版) (chat_id=%s, model=%s) - 内部使用流式请求并聚合", chatID, upstreamReq.Model)

	// 重要：将上游请求改为流式（解决Z.ai API返回SSE格式的问题）
	upstreamReq.Stream = true

	resp, cancel, err := callUpstreamWithRetry(upstreamReq, chatID, authToken, sessionID)
	if err != nil {
		duration := float64(time.Since(startTime)) / float64(time.Millisecond)
		recordRequestStats(startTime, r.URL.Path, http.StatusBadGateway, 0, modelName, false)
		addLiveRequest(r.Method, r.URL.Path, http.StatusBadGateway, duration, userAgent, modelName)
		globalErrorHandler.HandleAPIError(w, http.StatusBadGateway, "upstream_error",
			"Failed to call upstream", "调用上游失败: %v", err)
		return
	}
	defer func() {
		cancel()
		resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		duration := float64(time.Since(startTime)) / float64(time.Millisecond)
		recordRequestStats(startTime, r.URL.Path, http.StatusBadGateway, 0, modelName, false)
		addLiveRequest(r.Method, r.URL.Path, http.StatusBadGateway, duration, userAgent, modelName)

		body, _ := io.ReadAll(resp.Body)
		globalErrorHandler.HandleAPIError(w, http.StatusBadGateway, "upstream_error",
			"Upstream error", "上游返回错误状态: %d, 响应: %s", resp.StatusCode, string(body))
		return
	}

	// 使用新的聚合器
	aggregator := NewStreamAggregator()
	bufReader := bufio.NewReader(resp.Body)

	debugLog("开始聚合流式响应为非流式格式")

	lineCount := 0
	totalSize := int64(0)

	for {
		// 检查客户端是否断开
		select {
		case <-r.Context().Done():
			debugLog("客户端断开连接，停止处理")
			return
		default:
		}

		// 读取一行
		line, err := bufReader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				debugLog("到达流末尾，共处理 %d 行", lineCount)
				break
			}
			debugLog("读取SSE行失败: %v", err)
			break
		}

		lineCount++
		totalSize += int64(len(line))

		// 检查大小限制
		if totalSize > MaxResponseSize {
			debugLog("响应大小超出限制 (%d > %d)，停止处理", totalSize, MaxResponseSize)
			aggregator.Error = fmt.Errorf("响应大小超出限制")
			aggregator.ErrorDetail = fmt.Sprintf("响应大小超出限制 (%d bytes)", MaxResponseSize)
			break
		}

		// 处理行数据
		if !aggregator.ProcessLine(line) {
			break // 处理完成
		}
	}

	// 检查是否有错误
	if aggregator.Error != nil {
		duration := float64(time.Since(startTime)) / float64(time.Millisecond)
		recordRequestStats(startTime, r.URL.Path, http.StatusInternalServerError, 0, modelName, false)
		addLiveRequest(r.Method, r.URL.Path, http.StatusInternalServerError, duration, userAgent, modelName)
		globalErrorHandler.HandleAPIError(w, http.StatusInternalServerError, "aggregation_error",
			aggregator.ErrorDetail, "聚合响应时出错: %v", aggregator.Error)
		return
	}

	// 获取聚合结果
	content, reasoningContent, toolCalls, usage := aggregator.GetResult()

	// 确定结束原因
	finishReason := "stop"
	if len(toolCalls) > 0 {
		finishReason = "tool_calls"
	}

	// 构建响应
	openAIResp := OpenAIResponse{
		ID:      fmt.Sprintf("chatcmpl-%d", time.Now().Unix()),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   modelName,
	}

	// 构建消息
	message := AssistantMessage{
		Role:    "assistant",
		Content: content,
	}

	// 添加推理内容（如果有）
	if reasoningContent != "" {
		message.ReasoningContent = reasoningContent
	}

	// 添加工具调用（如果有）
	if len(toolCalls) > 0 {
		message.ToolCalls = normalizeToolCalls(toolCalls)
	}

	// 转换为 Message 类型
	var responseMessage Message
	responseMessage.Role = message.Role
	responseMessage.Content = message.Content
	responseMessage.ReasoningContent = message.ReasoningContent
	responseMessage.ToolCalls = message.ToolCalls

	openAIResp.Choices = append(openAIResp.Choices, Choice{
		Index:        0,
		Message:      responseMessage,
		FinishReason: finishReason,
	})

	// 添加usage信息
	if usage != nil && usage.TotalTokens > 0 {
		openAIResp.Usage = *usage
	}

	// 发送响应
	setCORSHeaders(w)
	w.Header().Set("Content-Type", "application/json")

	// 使用sonic编码响应
	data, err := sonicDefault.Marshal(openAIResp)
	if err != nil {
		debugLog("编码响应失败: %v", err)
		globalErrorHandler.HandleAPIError(w, http.StatusInternalServerError, "encode_error",
			"Failed to encode response", "编码响应失败: %v", err)
		return
	}

	// 写入响应
	if _, err := w.Write(data); err != nil {
		debugLog("写入响应失败: %v", err)
		return
	}

	// 确保flush
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}

	// 记录统计
	duration := float64(time.Since(startTime)) / float64(time.Millisecond)
	totalTokens := int64(0)
	if usage != nil {
		totalTokens = int64(usage.TotalTokens)
	}
	recordRequestStats(startTime, r.URL.Path, http.StatusOK, totalTokens, modelName, false)
	addLiveRequest(r.Method, r.URL.Path, http.StatusOK, duration, userAgent, modelName)

	debugLog("非流式响应（通过流式聚合）完成，处理了 %d 行SSE数据", lineCount)
}

// handleChatCompletionsOptimized 优化版的聊天完成处理函数
func handleChatCompletionsOptimized(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	userAgent := r.Header.Get("User-Agent")

	// 更新监控指标
	totalRequests.Add(1)
	currentConcurrency.Add(1)
	defer currentConcurrency.Add(-1)

	// 并发控制
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	if err := concurrencyLimiter.Acquire(ctx, 1); err != nil {
		duration := float64(time.Since(startTime)) / float64(time.Millisecond)
		recordRequestStats(startTime, r.URL.Path, http.StatusServiceUnavailable, 0, "", false)
		addLiveRequest(r.Method, r.URL.Path, http.StatusServiceUnavailable, duration, userAgent, "")
		globalErrorHandler.HandleAPIError(w, http.StatusServiceUnavailable, "service_unavailable",
			"Service temporarily unavailable, please try again later",
			"服务暂时不可用，并发限制达到上限，等待超时")
		requestErrors.Add("service_unavailable", 1)
		return
	}
	defer concurrencyLimiter.Release(1)

	setCORSHeaders(w)
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	// API Key验证
	authHeader := r.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, "Bearer ") {
		duration := float64(time.Since(startTime)) / float64(time.Millisecond)
		recordRequestStats(startTime, r.URL.Path, http.StatusUnauthorized, 0, "", false)
		addLiveRequest(r.Method, r.URL.Path, http.StatusUnauthorized, duration, userAgent, "")
		globalErrorHandler.HandleAPIError(w, http.StatusUnauthorized, "invalid_api_key",
			"Missing or invalid Authorization header", "缺少或无效的Authorization头")
		requestErrors.Add("invalid_api_key", 1)
		return
	}

	apiKey := strings.TrimPrefix(authHeader, "Bearer ")
	if apiKey != appConfig.DefaultKey {
		duration := float64(time.Since(startTime)) / float64(time.Millisecond)
		recordRequestStats(startTime, r.URL.Path, http.StatusUnauthorized, 0, "", false)
		addLiveRequest(r.Method, r.URL.Path, http.StatusUnauthorized, duration, userAgent, "")
		globalErrorHandler.HandleAPIError(w, http.StatusUnauthorized, "invalid_api_key",
			"Invalid API key", "API密钥验证失败")
		requestErrors.Add("invalid_api_key", 1)
		return
	}

	// 解析请求
	var req OpenAIRequest
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		duration := float64(time.Since(startTime)) / float64(time.Millisecond)
		recordRequestStats(startTime, r.URL.Path, http.StatusBadRequest, 0, "", false)
		addLiveRequest(r.Method, r.URL.Path, http.StatusBadRequest, duration, userAgent, "")
		globalErrorHandler.HandleAPIError(w, http.StatusBadRequest, "invalid_request_error",
			"Failed to read request body", "读取请求体失败: %v", err)
		requestErrors.Add("invalid_request_error", 1)
		return
	}

	if err := sonicDefault.Unmarshal(bodyBytes, &req); err != nil {
		duration := float64(time.Since(startTime)) / float64(time.Millisecond)
		recordRequestStats(startTime, r.URL.Path, http.StatusBadRequest, 0, "", false)
		addLiveRequest(r.Method, r.URL.Path, http.StatusBadRequest, duration, userAgent, "")
		globalErrorHandler.HandleAPIError(w, http.StatusBadRequest, "invalid_request_error",
			"Invalid JSON format", "JSON解析失败: %v", err)
		requestErrors.Add("invalid_request_error", 1)
		return
	}

	debugLog("请求解析成功 - 模型: %s, 流式: %v, 消息数: %d", req.Model, req.Stream, len(req.Messages))

	// 设置默认参数
	if req.Temperature == nil {
		defaultTemp := 0.7
		req.Temperature = &defaultTemp
	}
	if req.TopP == nil {
		defaultTopP := 0.9
		req.TopP = &defaultTopP
	}
	if req.MaxTokens == nil {
		defaultMaxTokens := 120000
		req.MaxTokens = &defaultMaxTokens
	}

	// 验证输入
	if err := validateAndSanitizeInput(&req); err != nil {
		duration := float64(time.Since(startTime)) / float64(time.Millisecond)
		recordRequestStats(startTime, r.URL.Path, http.StatusBadRequest, 0, "", false)
		addLiveRequest(r.Method, r.URL.Path, http.StatusBadRequest, duration, userAgent, "")
		globalErrorHandler.HandleAPIError(w, http.StatusBadRequest, "invalid_request_error",
			err.Error(), "输入验证失败: %v", err)
		requestErrors.Add("invalid_request_error", 1)
		return
	}

	// 生成会话ID
	var sessionID string
	if req.User != "" {
		sessionID = req.User
	} else {
		sessionID = getClientIP(r)
	}

	// 生成会话相关ID
	now := time.Now()
	chatID := fmt.Sprintf("%d-%d", now.UnixNano(), now.Unix())
	msgID := fmt.Sprintf("%d", now.UnixNano())

	// 获取模型配置 - 简洁优雅的方式
	modelConfig := mapper.GetSimpleModelConfig(req.Model)

	// 使用新的特性配置系统
	featureConfig := getModelFeatures(modelConfig.ID, req.Stream)
	featureConfig = mergeWithModelConfig(featureConfig, modelConfig)

	// 转换多模态消息
	converted := convertMultimodalMessages(req.Messages)

	// 解析ToolChoice参数
	req.ToolChoiceObject = parseToolChoice(req.ToolChoice)

	// 构造上游请求
	upstreamReq := UpstreamRequest{
		Stream:          true, // 总是使用流式从上游获取
		ChatID:          chatID,
		ID:              msgID,
		Model:           modelConfig.UpstreamID,
		Messages:        converted.Messages,
		Params:          buildUpstreamParams(req),
		Features:        featureConfig.Features.ToMap(), // 转换为 map
		BackgroundTasks: featureConfig.BackgroundTasks,
		MCPServers:      featureConfig.Features.MCPServers,
		ModelItem: struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			OwnedBy string `json:"owned_by"`
		}{
			ID:      modelConfig.ID,
			Name:    modelConfig.Name,
			OwnedBy: "openai",
		},
		ToolServers: featureConfig.ToolServers,
		Variables:   featureConfig.Variables,
	}

	// 添加工具（如果有）
	if len(req.Tools) > 0 {
		upstreamReq.Tools = req.Tools
	}
	if req.ToolChoiceObject != nil {
		upstreamReq.ToolChoice = req.ToolChoiceObject
	}

	// 获取认证token
	authToken := appConfig.UpstreamToken
	if appConfig.AnonTokenEnabled {
		token, err := tokenCache.GetToken()
		if err != nil {
			debugLog("获取认证token失败: %v", err)
		} else {
			authToken = token
		}
	}

	// 根据请求类型调用不同的处理函数
	if req.Stream {
		handleStreamResponseOptimized(w, r, upstreamReq, chatID, authToken, modelConfig.Name, startTime, sessionID)
	} else {
		handleNonStreamResponseOptimized(w, r, upstreamReq, chatID, authToken, modelConfig.Name, startTime, sessionID)
	}
}
