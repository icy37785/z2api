package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"z2api/internal/signature"
)

// MessageConverter 消息转换器，参考 Python 版本的 prepare_data 函数
type MessageConverter struct {
	authToken     string
	uploader      *ImageUploader
	featureConfig FeatureConfig
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
func (mc *MessageConverter) PrepareData(req OpenAIRequest, sessionID string) (UpstreamRequest, map[string]string, error) {
	// 生成会话相关ID
	chatID := uuid.New().String()
	msgID := uuid.New().String()

	// 转换消息并处理图片
	processedMessages, files, err := mc.processMessages(req.Messages)
	if err != nil {
		return UpstreamRequest{}, nil, fmt.Errorf("处理消息失败: %w", err)
	}

	// 构建上游请求
	upstreamReq := UpstreamRequest{
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

// processMessages 处理消息列表，转换多模态内容并上传图片
func (mc *MessageConverter) processMessages(messages []Message) ([]UpstreamMessage, []map[string]interface{}, error) {
	var processedMessages []UpstreamMessage
	var files []map[string]interface{}

	for _, msg := range messages {
		// 处理纯文本消息
		if content, ok := msg.Content.(string); ok {
			processedMessages = append(processedMessages, UpstreamMessage{
				Role:             normalizeRole(msg.Role),
				Content:          content,
				ReasoningContent: msg.ReasoningContent,
			})
			continue
		}

		// 处理多模态消息
		textContent, imageFiles, err := mc.processMultimodalContent(msg)
		if err != nil {
			debugLog("处理多模态内容失败: %v", err)
			// 继续处理，但不包含图片
			processedMessages = append(processedMessages, UpstreamMessage{
				Role:             normalizeRole(msg.Role),
				Content:          textContent,
				ReasoningContent: msg.ReasoningContent,
			})
			continue
		}

		// 添加处理后的消息
		processedMessages = append(processedMessages, UpstreamMessage{
			Role:             normalizeRole(msg.Role),
			Content:          textContent,
			ReasoningContent: msg.ReasoningContent,
		})

		// 收集文件信息
		files = append(files, imageFiles...)
	}

	return processedMessages, files, nil
}

// processMultimodalContent 处理多模态内容
func (mc *MessageConverter) processMultimodalContent(msg Message) (string, []map[string]interface{}, error) {
	var textContent strings.Builder
	var files []map[string]interface{}

	// 处理 interface{} 类型的数组
	if parts, ok := msg.Content.([]interface{}); ok {
		for _, part := range parts {
			partMap, ok := part.(map[string]interface{})
			if !ok {
				continue
			}

			partType, _ := partMap["type"].(string)

			switch partType {
			case "text":
				if text, ok := partMap["text"].(string); ok {
					if textContent.Len() > 0 {
						textContent.WriteString(" ")
					}
					textContent.WriteString(text)
				}

			case "image_url":
				if imageURL, ok := partMap["image_url"].(map[string]interface{}); ok {
					if url, ok := imageURL["url"].(string); ok {
						fileID, err := mc.uploadImage(url)
						if err != nil {
							debugLog("上传图片失败: %v", err)
							continue
						}
						files = append(files, map[string]interface{}{
							"type": "image",
							"id":   fileID,
						})
					}
				}
			}
		}
	}

	// 处理 ContentPart 数组
	if parts, ok := msg.Content.([]ContentPart); ok {
		for _, part := range parts {
			switch part.Type {
			case "text":
				if textContent.Len() > 0 {
					textContent.WriteString(" ")
				}
				textContent.WriteString(part.Text)

			case "image_url":
				if part.ImageURL != nil && part.ImageURL.URL != "" {
					fileID, err := mc.uploadImage(part.ImageURL.URL)
					if err != nil {
						debugLog("上传图片失败: %v", err)
						continue
					}
					files = append(files, map[string]interface{}{
						"type": "image",
						"id":   fileID,
					})
				}
			}
		}
	}

	return textContent.String(), files, nil
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
	requestID := uuid.New().String()
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
	usage *Usage,
	finishReason string,
) OpenAIResponse {
	timestamp := time.Now().Unix()
	responseID := fmt.Sprintf("chatcmpl-%s", uuid.New().String())

	var delta Delta

	switch phase {
	case "thinking":
		delta = Delta{
			ReasoningContent: content,
			Role:             "assistant",
		}
		finishReason = ""

	case "answer":
		delta = Delta{
			Content: content,
			Role:    "assistant",
		}
		finishReason = ""

	case "tool_call":
		delta = Delta{
			Content: content,
			Role:    "assistant",
		}
		finishReason = ""

	case "other":
		delta = Delta{
			Content: content,
			Role:    "assistant",
		}
		// finishReason 保持传入的值

	default:
		delta = Delta{
			Content: content,
			Role:    "assistant",
		}
	}

	response := OpenAIResponse{
		ID:      responseID,
		Object:  "chat.completion.chunk",
		Created: timestamp,
		Model:   model,
		Choices: []Choice{
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
