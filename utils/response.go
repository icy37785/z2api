package utils

import (
	"fmt"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// ErrorResponse 统一的错误响应处理
func ErrorResponse(c *gin.Context, statusCode int, errorType, message string, param interface{}) {
	c.AbortWithStatusJSON(statusCode, gin.H{
		"error": gin.H{
			"message": message,
			"type":    errorType,
			"code":    statusCode,
			"param":   param,
		},
	})
}

// ErrorResponseWithDetails 带详细信息的错误响应
func ErrorResponseWithDetails(c *gin.Context, statusCode int, errorType, message string, param interface{}, details string) {
	response := gin.H{
		"error": gin.H{
			"message": message,
			"type":    errorType,
			"code":    statusCode,
			"param":   param,
		},
	}

	if details != "" {
		if errorMap, ok := response["error"].(gin.H); ok {
			errorMap["details"] = details
		}
	}

	c.AbortWithStatusJSON(statusCode, response)
}

// SuccessResponse 统一的成功响应
func SuccessResponse(c *gin.Context, data interface{}) {
	c.JSON(200, data)
}

// StreamResponse 统一的流式响应设置
func SetupStreamResponse(c *gin.Context) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no") // 禁用 Nginx 缓冲
}

// GenerateChatCompletionID 生成聊天完成ID
func GenerateChatCompletionID() string {
	return fmt.Sprintf("chatcmpl-%d", time.Now().Unix())
}

// GenerateChatID 生成聊天会话ID
func GenerateChatID() string {
	now := time.Now()
	return fmt.Sprintf("%d-%d", now.UnixNano(), now.Unix())
}

// GenerateMessageID 生成消息ID
func GenerateMessageID() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}

// GenerateUUID 生成UUID
func GenerateUUID() string {
	return uuid.New().String()
}

// GenerateShortUUID 生成短UUID
func GenerateShortUUID() string {
	return uuid.New().String()[:8]
}

// GenerateToolCallID 生成工具调用ID
func GenerateToolCallID() string {
	return fmt.Sprintf("call_%s", GenerateShortUUID())
}

// GenerateRequestID 生成请求ID
func GenerateRequestID() string {
	return uuid.New().String()
}