package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"z2api/config"
	"z2api/errors"
	"z2api/internal/mapper"
	"z2api/types"
	"z2api/utils"

	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"
)

// GinHandleChatCompletions 充分利用 Gin 特性的处理器
func GinHandleChatCompletions(c *gin.Context) {
	startTime := time.Now()

	// 使用 context 超时控制
	ctx := c.Request.Context()
	timeoutCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	// 使用 Gin 的上下文存储
	c.Set("start_time", startTime)
	c.Set("user_agent", c.GetHeader("User-Agent"))
	c.Set("debug_mode", appConfig.DebugMode)

	// 更新监控指标
	totalRequests.Add(1)
	currentConcurrency.Add(1)
	defer currentConcurrency.Add(-1)

	// 并发控制 - 在中间件中已处理，这里跳过

	// API Key 验证 - 使用新的错误处理
	authHeader := c.GetHeader("Authorization")
	if !strings.HasPrefix(authHeader, "Bearer ") {
		utils.ErrorResponse(c, errors.ErrInvalidAPIKey.WithParam("authorization"))
		recordError(c, startTime, errors.ErrInvalidAPIKey.StatusCode, "invalid_api_key")
		return
	}

	apiKey := strings.TrimPrefix(authHeader, "Bearer ")
	if apiKey != appConfig.DefaultKey {
		utils.ErrorResponse(c, errors.ErrInvalidAPIKey.WithParam("api_key"))
		recordError(c, startTime, errors.ErrInvalidAPIKey.StatusCode, "invalid_api_key")
		return
	}

	// 使用 Gin 的 JSON 绑定 - 自动解析和验证
	var req types.OpenAIRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		// 检查是否为验证错误
		if validationErrors, ok := err.(validator.ValidationErrors); ok {
			// 处理具体的验证错误
			for _, e := range validationErrors {
				utils.ValidationErrorWithParam(c, getValidationErrorMessage(e), e.Field())
				recordError(c, startTime, http.StatusBadRequest, "validation_error")
				return
			}
		}

		utils.ErrorResponse(c, errors.ErrInvalidJSON.WithDetails(err.Error()))
		recordError(c, startTime, http.StatusBadRequest, "invalid_request_error")
		return
	}

	debugLog("请求解析成功 - 模型: %s, 流式: %v, 消息数: %d", req.Model, req.Stream, len(req.Messages))

	// 设置默认参数（如果需要）
	setDefaultParams(&req)

	// 验证输入（额外的业务逻辑验证）
	if err := validateBusinessRules(&req); err != nil {
		utils.ErrorResponse(c, err.(errors.APIError))
		recordError(c, startTime, http.StatusBadRequest, "validation_error")
		return
	}

	// 生成会话ID
	sessionID := req.User
	if sessionID == "" {
		sessionID = c.ClientIP()
	}

	// 存储到上下文
	c.Set("session_id", sessionID)

	// 生成会话相关ID - 使用utils工具函数
	chatID := utils.GenerateChatID()
	msgID := utils.GenerateMessageID()

	// 获取模型配置
	modelConfig := mapper.GetSimpleModelConfig(req.Model)
	c.Set("model_name", modelConfig.Name)

	// 构造上游请求
	upstreamReq := buildUpstreamRequest(req, chatID, msgID, modelConfig)

	// 获取认证token
	authToken := getAuthToken()

	// 根据请求类型调用不同的处理函数
	if req.Stream {
		handleStreamResponseGin(timeoutCtx, c, upstreamReq, chatID, authToken, modelConfig.Name, sessionID)
	} else {
		handleNonStreamResponseGin(timeoutCtx, c, upstreamReq, chatID, authToken, modelConfig.Name, sessionID)
	}
}

// handleStreamResponseGin 使用 Gin 的流式响应处理
func handleStreamResponseGin(ctx context.Context, c *gin.Context, upstreamReq types.UpstreamRequest, chatID string, authToken string, modelName string, sessionID string) {
	startTime := c.GetTime("start_time")
	debugLog("开始处理流式响应 (Gin版) (chat_id=%s, model=%s)", chatID, upstreamReq.Model)

	// 调用上游API，传递context
	resp, cancel, err := callUpstreamWithContext(ctx, upstreamReq, chatID, authToken, sessionID)
	if err != nil {
		utils.ErrorResponse(c, errors.NewUpstreamError(err.Error()))
		recordError(c, startTime, http.StatusBadGateway, "upstream_error")
		return
	}
	defer func() {
		cancel()
		resp.Body.Close()
	}()

	// 检查响应状态
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		utils.ErrorResponse(c, errors.NewUpstreamError(fmt.Sprintf("状态: %d, 响应: %s", resp.StatusCode, string(body))))
		recordError(c, startTime, http.StatusBadGateway, "upstream_error")
		return
	}

	// 发送初始块
	firstChunk := types.OpenAIResponse{
		ID:      utils.GenerateChatCompletionID(),
		Object:  "chat.completion.chunk",
		Created: time.Now().Unix(),
		Model:   modelName,
		Choices: []types.Choice{{
			Index: 0,
			Delta: types.Delta{Role: "assistant"},
		}},
	}

	// 设置 SSE 响应头
	SetSSEHeaders(c)

	// 写入第一个块
	if data, err := sonicStream.Marshal(firstChunk); err == nil {
		c.Writer.WriteString(fmt.Sprintf("data: %s\n\n", string(data)))
		c.Writer.Flush()
	}

	// 使用新的 Gin 流式处理器，传递context
	if err := HandleGinStreamResponseWithContext(ctx, c, &resp.Body, modelName); err != nil {
		debugLog("流式响应处理错误: %v", err)
	}

	// 记录统计
	recordSuccess(c, startTime, modelName, true)
	debugLog("流式响应处理完成")
}

// handleNonStreamResponseGin 使用 Gin 的非流式响应处理
func handleNonStreamResponseGin(ctx context.Context, c *gin.Context, upstreamReq types.UpstreamRequest, chatID string, authToken string, modelName string, sessionID string) {
	startTime := c.GetTime("start_time")
	debugLog("开始处理非流式响应 (Gin版) (chat_id=%s, model=%s)", chatID, upstreamReq.Model)

	// 强制使用流式从上游获取
	upstreamReq.Stream = true

	resp, cancel, err := callUpstreamWithContext(ctx, upstreamReq, chatID, authToken, sessionID)
	if err != nil {
		utils.ErrorResponse(c, errors.NewUpstreamError(err.Error()))
		recordError(c, startTime, http.StatusBadGateway, "upstream_error")
		return
	}
	defer func() {
		cancel()
		resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		utils.ErrorResponse(c, errors.NewUpstreamError(fmt.Sprintf("状态: %d, 响应: %s", resp.StatusCode, string(body))))
		recordError(c, startTime, http.StatusBadGateway, "upstream_error")
		return
	}

	// 聚合流式响应，传递context
	aggregator := NewGinStreamAggregator()
	bufReader := bufio.NewReader(resp.Body)

	debugLog("开始聚合流式响应为非流式格式 (Gin版)")

	lineCount := 0
	totalSize := int64(0)

	for {
		// 检查客户端是否断开或context超时
		select {
		case <-ctx.Done():
			debugLog("context取消或超时，停止处理: %v", ctx.Err())
			return
		case <-c.Request.Context().Done():
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
			debugLog("响应大小超出限制")
			utils.ErrorResponse(c, errors.ErrContentTooLong)
			return
		}

		// 处理行数据
		if !aggregator.ProcessLine(line) {
			break
		}
	}

	// 检查错误
	if aggregator.Error != nil {
		utils.ErrorResponse(c, errors.NewValidationError(aggregator.ErrorDetail))
		recordError(c, startTime, http.StatusInternalServerError, "aggregation_error")
		return
	}

	// 获取聚合结果
	content, reasoningContent, toolCalls, usage := aggregator.GetResult()

	// 构建响应
	openAIResp := buildNonStreamResponse(content, reasoningContent, toolCalls, usage, modelName)

	// 使用 Gin 的 JSON 方法发送响应
	c.JSON(http.StatusOK, openAIResp)

	// 记录统计
	recordSuccess(c, startTime, modelName, false)
	debugLog("非流式响应完成，处理了 %d 行SSE数据", lineCount)
}

// GinHandleModels 模型列表 (Gin 原生实现)
func GinHandleModels(c *gin.Context) {
	models := []gin.H{
		{
			"id":       "glm-4.5",
			"object":   "model",
			"created":  1686935002,
			"owned_by": "openai",
		},
		{
			"id":       "glm-4.5-air",
			"object":   "model",
			"created":  1686935002,
			"owned_by": "openai",
		},
		{
			"id":       "glm-4.6",
			"object":   "model",
			"created":  1686935002,
			"owned_by": "openai",
		},
	}

	c.JSON(http.StatusOK, gin.H{
		"object": "list",
		"data":   models,
	})
}

// GinHandleHealth 健康检查 (Gin 原生实现)
func GinHandleHealth(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status":    "healthy",
		"timestamp": time.Now().Unix(),
		"version":   "1.0.0",
		"config": gin.H{
			"debug_mode":              appConfig.DebugMode,
			"think_tags_mode":         appConfig.ThinkTagsMode,
			"anon_token_enabled":      appConfig.AnonTokenEnabled,
			"max_concurrent_requests": appConfig.MaxConcurrentRequests,
		},
	})
}

// 辅助函数

func setDefaultParams(req *types.OpenAIRequest) {
	if req.Temperature == nil {
		req.Temperature = types.Float64Ptr(0.7)
	}
	if req.TopP == nil {
		req.TopP = types.Float64Ptr(0.9)
	}
	if req.MaxTokens == nil {
		req.MaxTokens = types.IntPtr(120000)
	}
}

func buildUpstreamRequest(req types.OpenAIRequest, chatID, msgID string, modelConfig config.ModelConfig) types.UpstreamRequest {
	featureConfig := getModelFeatures(modelConfig.ID, req.Stream)
	featureConfig = mergeWithModelConfig(featureConfig, modelConfig)

	converted := convertMultimodalMessages(req.Messages)
	req.ToolChoiceObject = parseToolChoice(req.ToolChoice)

	upstreamReq := types.UpstreamRequest{
		Stream:          true,
		ChatID:          chatID,
		ID:              msgID,
		Model:           modelConfig.UpstreamID,
		Messages:        converted.Messages,
		Params:          buildUpstreamParams(req),
		Features:        featureConfig.Features.ToMap(),
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

	if len(req.Tools) > 0 {
		upstreamReq.Tools = req.Tools
	}
	if req.ToolChoiceObject != nil {
		upstreamReq.ToolChoice = req.ToolChoiceObject
	}

	return upstreamReq
}

func getAuthToken() string {
	authToken := appConfig.UpstreamToken
	if appConfig.AnonTokenEnabled {
		token, err := tokenCache.GetToken()
		if err != nil {
			debugLog("获取认证token失败: %v", err)
		} else {
			authToken = token
		}
	}
	return authToken
}

func buildNonStreamResponse(content, reasoningContent string, toolCalls []types.ToolCall, usage *types.Usage, modelName string) types.OpenAIResponse {
	finishReason := "stop"
	if len(toolCalls) > 0 {
		finishReason = "tool_calls"
	}

	message := types.Message{
		Role:    "assistant",
		Content: content,
	}

	if reasoningContent != "" {
		message.ReasoningContent = reasoningContent
	}

	if len(toolCalls) > 0 {
		message.ToolCalls = normalizeToolCalls(toolCalls)
	}

	resp := types.OpenAIResponse{
		ID:      utils.GenerateChatCompletionID(),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   modelName,
		Choices: []types.Choice{{
			Index:        0,
			Message:      &message, // 改为指针
			FinishReason: finishReason,
		}},
	}

	if usage != nil && usage.TotalTokens > 0 {
		resp.Usage = usage
	}

	return resp
}

func recordError(c *gin.Context, startTime time.Time, statusCode int, errorType string) {
	duration := float64(time.Since(startTime)) / float64(time.Millisecond)
	userAgent := c.GetString("user_agent")
	recordRequestStats(startTime, c.Request.URL.Path, statusCode, 0, "", false)
	addLiveRequest(c.Request.Method, c.Request.URL.Path, statusCode, duration, userAgent, "")
	requestErrors.Add(errorType, 1)
}

func recordSuccess(c *gin.Context, startTime time.Time, modelName string, isStream bool) {
	duration := float64(time.Since(startTime)) / float64(time.Millisecond)
	userAgent := c.GetString("user_agent")
	recordRequestStats(startTime, c.Request.URL.Path, http.StatusOK, 0, modelName, isStream)
	addLiveRequest(c.Request.Method, c.Request.URL.Path, http.StatusOK, duration, userAgent, modelName)
}

// getValidationErrorMessage 转换验证错误为用户友好的消息
func getValidationErrorMessage(e validator.FieldError) string {
	switch e.Tag() {
	case "required":
		return fmt.Sprintf("字段 %s 是必需的", e.Field())
	case "min":
		return fmt.Sprintf("字段 %s 的最小值为 %s", e.Field(), e.Param())
	case "max":
		return fmt.Sprintf("字段 %s 的最大值为 %s", e.Field(), e.Param())
	case "gte":
		return fmt.Sprintf("字段 %s 必须大于或等于 %s", e.Field(), e.Param())
	case "lte":
		return fmt.Sprintf("字段 %s 必须小于或等于 %s", e.Field(), e.Param())
	case "oneof":
		return fmt.Sprintf("字段 %s 必须是以下值之一: %s", e.Field(), e.Param())
	case "url":
		return fmt.Sprintf("字段 %s 必须是有效的URL", e.Field())
	case "email":
		return fmt.Sprintf("字段 %s 必须是有效的邮箱地址", e.Field())
	default:
		return fmt.Sprintf("字段 %s 验证失败: %s", e.Field(), e.Tag())
	}
}

// validateBusinessRules 验证业务规则（超出基本binding验证的额外逻辑）
func validateBusinessRules(req *types.OpenAIRequest) error {
	// 检查消息内容长度
	totalContentLength := 0
	for _, msg := range req.Messages {
		if contentStr, ok := msg.Content.(string); ok {
			totalContentLength += len(contentStr)
		}
		// 可以在这里添加对复杂内容的长度检查
	}

	if totalContentLength > 1000000 { // 1MB 总限制
		return errors.ErrContentTooLong.WithDetails("总消息内容过长")
	}

	// 检查工具定义的合理性
	if len(req.Tools) > 10 {
		return errors.ErrTooManyTools.WithDetails("工具数量不能超过10个")
	}

	// 验证模型特殊要求
	if strings.Contains(req.Model, "vision") {
		// vision模型可以处理图片，这里只做日志记录
		debugLog("检测到vision模型请求")
		// 注意：vision模型也可以处理纯文本，所以不做强制要求
	}

	return nil
}

// callUpstreamWithContext 带context的上游调用
func callUpstreamWithContext(ctx context.Context, upstreamReq types.UpstreamRequest, chatID, authToken, sessionID string) (*http.Response, context.CancelFunc, error) {
	return callUpstreamWithHeaders(ctx, upstreamReq, chatID, authToken, sessionID)
}

// HandleGinStreamResponseWithContext 带context的流式响应处理
func HandleGinStreamResponseWithContext(ctx context.Context, c *gin.Context, resp *io.ReadCloser, model string) error {
	// 设置 SSE 响应头
	SetSSEHeaders(c)

	// 创建流处理器
	handler := NewGinStreamHandler(c, model)

	// 创建缓冲读取器
	bufReader := bufio.NewReader(*resp)

	debugLog("开始处理流式响应 (Gin版)，模型：%s", model)

	// 使用 Gin 的 Stream 方法处理流式数据，传递context
	c.Stream(func(w io.Writer) bool {
		// 检查context是否取消
		select {
		case <-ctx.Done():
			debugLog("context取消，停止处理: %v", ctx.Err())
			return false
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
