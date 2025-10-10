package utils

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"z2api/errors"
)

// ErrorResponse 统一的错误响应处理
func ErrorResponse(c *gin.Context, err errors.APIError) {
	// 检查是否为调试模式
	debugMode := c.GetBool("debug_mode")

	response := gin.H{
		"error": gin.H{
			"message": err.Message,
			"type":    err.Type,
			"code":    err.Code,
		},
	}

	// 添加可选字段
	if err.Param != "" {
		response["error"].(gin.H)["param"] = err.Param
	}

	if err.Details != "" {
		response["error"].(gin.H)["details"] = err.Details
	}

	if debugMode && err.Debug != "" {
		response["error"].(gin.H)["debug"] = err.Debug
	}

	c.AbortWithStatusJSON(err.StatusCode, response)
}

// ErrorResponseWithMessage 直接使用消息创建错误响应
func ErrorResponseWithMessage(c *gin.Context, statusCode int, errorType, message string) {
	err := errors.NewInvalidRequestError(message)
	err.StatusCode = statusCode
	err.Code = statusCode
	err.Type = errorType
	ErrorResponse(c, err)
}

// ErrorResponseWithParam 使用带参数的错误响应
func ErrorResponseWithParam(c *gin.Context, statusCode int, errorType, message, param string) {
	err := errors.NewInvalidRequestErrorWithParam(message, param)
	err.StatusCode = statusCode
	err.Code = statusCode
	err.Type = errorType
	ErrorResponse(c, err)
}

// ValidationError 响应验证错误
func ValidationError(c *gin.Context, message string) {
	ErrorResponse(c, errors.NewValidationError(message))
}

// ValidationErrorWithParam 响应带参数的验证错误
func ValidationErrorWithParam(c *gin.Context, message, param string) {
	ErrorResponse(c, errors.NewValidationErrorWithParam(message, param))
}

// SuccessResponse 统一的成功响应
func SuccessResponse(c *gin.Context, data interface{}) {
	c.JSON(http.StatusOK, data)
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
