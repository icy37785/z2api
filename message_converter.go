package main

import (
	"fmt"
	"strings"
	"time"

	"z2api/internal/signature"
	"z2api/types"
	"z2api/utils"
)

// MessageConverter 消息转换器，参考 Python 版本的 prepare_data 函数
type MessageConverter struct {
	authToken     string
	uploader      *ImageUploader
	featureConfig FeatureConfig // 使用本地的 FeatureConfig 类型
}

// NewMessageConverter 创建新的消息转换器
func NewMessageConverter(authToken string, modelID string, streaming bool) *MessageConverter {
	return &MessageConverter{
		authToken:     authToken,
		uploader:      NewImageUploader(authToken),
		featureConfig: getModelFeatures(modelID, streaming),
	}
}

// PrepareData 准备上游请求数据
// 参考 Python 版本的 prepare_data 函数
func (mc *MessageConverter) PrepareData(req types.OpenAIRequest, sessionID string) (types.UpstreamRequest, map[string]string, error) {
	// 生成会话相关ID
	chatID := utils.GenerateUUID()
	msgID := utils.GenerateUUID()

	// 转换消息并处理图片
	processedMessages, files, err := mc.processMessages(req.Messages)
	if err != nil {
		return types.UpstreamRequest{}, nil, fmt.Errorf("处理消息失败: %w", err)
	}

	// 构建上游请求
	upstreamReq := types.UpstreamRequest{
		Stream:          true, // 总是使用流式从上游获取
		Model:           req.Model,
		Messages:        processedMessages,
		ChatID:          chatID,
		ID:              msgID,
		Params:          buildUpstreamParams(req),
		Features:        mc.featureConfig.Features.ToMap(),
		BackgroundTasks: mc.featureConfig.BackgroundTasks,
		MCPServers:      mc.featureConfig.Features.MCPServers,
		ToolServers:     mc.featureConfig.ToolServers,
		Variables:       mc.getVariables(),
		Tools:           req.Tools,
		ToolChoice:      req.ToolChoice,
	}

	// 如果有图片文件，添加到请求中
	if len(files) > 0 {
		upstreamReq.Params["files"] = files
		debugLog("添加 %d 个文件到请求中", len(files))
	}

	// 构建请求参数
	params := mc.buildRequestParams(chatID, sessionID)

	return upstreamReq, params, nil
}

// processMessages 处理消息列表，转换多模态内容并上传图片（使用统一的多模态处理器）
func (mc *MessageConverter) processMessages(messages []types.Message) ([]types.UpstreamMessage, []map[string]interface{}, error) {
	var processedMessages []types.UpstreamMessage
	var files []map[string]interface{}
	
	// 创建多模态处理器
	processor := utils.NewMultimodalProcessor("")
	processor.EnableDebugLog = appConfig.DebugMode

	for _, msg := range messages {
		// 使用统一处理器处理内容
		result, err := processor.ProcessContent(msg.Content)
		
		var textContent string
		if err == nil {
			textContent = result.Text
			
			// 处理图片文件上传
			for _, imageURL := range result.Images {
				fileID, uploadErr := mc.uploadImage(imageURL)
				if uploadErr != nil {
					debugLog("上传图片失败: %v", uploadErr)
					continue
				}
				files = append(files, map[string]interface{}{
					"type": "image",
					"id":   fileID,
				})
			}
			
			// 处理其他已有文件ID的文件
			for _, file := range result.Files {
				if file.FileID != "" {
					files = append(files, map[string]interface{}{
						"type": file.Type,
						"id":   file.FileID,
					})
				}
			}
		} else {
			// 如果处理失败，尝试将内容转换为字符串
			if content, ok := msg.Content.(string); ok {
				textContent = content
			} else {
				textContent = ""
			}
		}

		// 添加处理后的消息
		processedMessages = append(processedMessages, types.UpstreamMessage{
			Role:             normalizeRole(msg.Role),
			Content:          textContent,
			ReasoningContent: msg.ReasoningContent,
		})
	}

	return processedMessages, files, nil
}

// processMultimodalContent 处理多模态内容（已被processMessages方法替代，保留作为向后兼容）
func (mc *MessageConverter) processMultimodalContent(msg types.Message) (string, []map[string]interface{}, error) {
	processor := utils.NewMultimodalProcessor("")
	result, err := processor.ProcessContent(msg.Content)
	
	if err != nil {
		return "", nil, err
	}
	
	var files []map[string]interface{}
	
	// 处理图片上传
	for _, imageURL := range result.Images {
		fileID, uploadErr := mc.uploadImage(imageURL)
		if uploadErr != nil {
			debugLog("上传图片失败: %v", uploadErr)
			continue
		}
		files = append(files, map[string]interface{}{
			"type": "image",
			"id":   fileID,
		})
	}
	
	return result.Text, files, nil
}

// uploadImage 上传图片并返回文件ID
func (mc *MessageConverter) uploadImage(url string) (string, error) {
	if strings.HasPrefix(url, "data:image/") {
		// Base64 编码的图片
		return mc.uploader.UploadBase64Image(url)
	} else if strings.HasPrefix(url, "http") {
		// URL 图片
		return mc.uploader.UploadImageFromURL(url)
	}

	return "", fmt.Errorf("不支持的图片URL格式: %s", url)
}

// buildRequestParams 构建请求参数
// 参考 Python 版本的参数构建逻辑
func (mc *MessageConverter) buildRequestParams(chatID, sessionID string) map[string]string {
	requestID := utils.GenerateRequestID()
	timestamp := time.Now().UnixMilli()

	params := map[string]string{
		"requestId": requestID,
		"timestamp": fmt.Sprintf("%d", timestamp),
		"user_id":   mc.getUserID(),
	}

	return params
}

// getUserID 获取用户ID
func (mc *MessageConverter) getUserID() string {
	// 尝试从 JWT token 中解析 user_id
	if jwtPayload, err := signature.DecodeJWT(mc.authToken); err == nil {
		return jwtPayload.ID
	}

	// 使用 fallback 逻辑
	hashVal := hashString(mc.authToken)
	return fmt.Sprintf("guest-user-%d", hashVal%1000000)
}

// getVariables 获取变量映射
func (mc *MessageConverter) getVariables() map[string]string {
	now := time.Now()
	return map[string]string{
		"{{USER_NAME}}":        "User",
		"{{USER_LOCATION}}":    "Unknown",
		"{{CURRENT_DATE}}":     now.Format("2006-01-02"),
		"{{CURRENT_TIME}}":     now.Format("15:04:05"),
		"{{CURRENT_DATETIME}}": now.Format("2006-01-02 15:04:05"),
	}
}

// CreateChatCompletionData 创建聊天完成数据
// 参考 Python 版本的 create_chat_completion_data 函数
func CreateChatCompletionData(
	content string,
	model string,
	phase string,
	usage *types.Usage,
	finishReason string,
) types.OpenAIResponse{
	timestamp := time.Now().Unix()
	responseID := utils.GenerateChatCompletionID()

	var delta types.Delta

	switch phase {
	case "thinking":
		delta = types.Delta{
			ReasoningContent: content,
			Role:             "assistant",
		}
		finishReason = ""

	case "answer":
		delta = types.Delta{
			Content: content,
			Role:    "assistant",
		}
		finishReason = ""

	case "tool_call":
		delta = types.Delta{
			Content: content,
			Role:    "assistant",
		}
		finishReason = ""

	case "other":
		delta = types.Delta{
			Content: content,
			Role:    "assistant",
		}
		// finishReason 保持传入的值

	default:
		delta = types.Delta{
			Content: content,
			Role:    "assistant",
		}
	}

	response := types.OpenAIResponse{
		ID:      responseID,
		Object:  "chat.completion.chunk",
		Created: timestamp,
		Model:   model,
		Choices: []types.Choice{
			{
				Index:        0,
				Delta:        delta,
				FinishReason: finishReason,
			},
		},
	}

	// 添加使用统计（如果有）
	if usage != nil {
		response.Usage = *usage
	}

	return response
}
