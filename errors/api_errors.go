package errors

import (
	"fmt"
	"net/http"
)

// APIError 标准API错误结构
type APIError struct {
	Type       string `json:"type"`
	Message    string `json:"message"`
	Code       int    `json:"code"`
	Param      string `json:"param,omitempty"`
	Details    string `json:"details,omitempty"`
	Debug      string `json:"debug,omitempty"` // 仅在调试模式下显示
	StatusCode int    `json:"-"`              // HTTP状态码，不序列化到JSON
}

// Error 实现error接口
func (e APIError) Error() string {
	if e.Param != "" {
		return fmt.Sprintf("%s: %s (param: %s)", e.Type, e.Message, e.Param)
	}
	return fmt.Sprintf("%s: %s", e.Type, e.Message)
}

// WithDetails 添加详细信息
func (e APIError) WithDetails(details string) APIError {
	e.Details = details
	return e
}

// WithDebug 添加调试信息
func (e APIError) WithDebug(debug string) APIError {
	e.Debug = debug
	return e
}

// WithParam 添加参数信息
func (e APIError) WithParam(param string) APIError {
	e.Param = param
	return e
}

// predefined error types
var (
	// 请求相关错误
	ErrInvalidRequest = APIError{
		Type:       "invalid_request_error",
		Message:    "Invalid request",
		Code:       http.StatusBadRequest,
		StatusCode: http.StatusBadRequest,
	}

	ErrInvalidAPIKey = APIError{
		Type:       "invalid_request_error",
		Message:    "Invalid API key provided",
		Code:       http.StatusUnauthorized,
		StatusCode: http.StatusUnauthorized,
		Param:      "api_key",
	}

	ErrInsufficientQuota = APIError{
		Type:       "insufficient_quota",
		Message:    "Insufficient quota",
		Code:       http.StatusForbidden,
		StatusCode: http.StatusForbidden,
	}

	ErrModelNotFound = APIError{
		Type:       "invalid_request_error",
		Message:    "Model not found",
		Code:       http.StatusNotFound,
		StatusCode: http.StatusNotFound,
		Param:      "model",
	}

	ErrInvalidModel = APIError{
		Type:       "invalid_request_error",
		Message:    "Invalid model",
		Code:       http.StatusBadRequest,
		StatusCode: http.StatusBadRequest,
		Param:      "model",
	}

	// 验证相关错误
	ErrValidationFailed = APIError{
		Type:       "invalid_request_error",
		Message:    "Validation failed",
		Code:       http.StatusBadRequest,
		StatusCode: http.StatusBadRequest,
	}

	ErrInvalidJSON = APIError{
		Type:       "invalid_request_error",
		Message:    "Invalid JSON format",
		Code:       http.StatusBadRequest,
		StatusCode: http.StatusBadRequest,
	}

	ErrMissingRequiredField = APIError{
		Type:       "invalid_request_error",
		Message:    "Missing required field",
		Code:       http.StatusBadRequest,
		StatusCode: http.StatusBadRequest,
	}

	ErrInvalidFieldValue = APIError{
		Type:       "invalid_request_error",
		Message:    "Invalid field value",
		Code:       http.StatusBadRequest,
		StatusCode: http.StatusBadRequest,
	}

	// 内容相关错误
	ErrContentTooLong = APIError{
		Type:       "invalid_request_error",
		Message:    "Content too long",
		Code:       http.StatusBadRequest,
		StatusCode: http.StatusBadRequest,
	}

	ErrTooManyMessages = APIError{
		Type:       "invalid_request_error",
		Message:    "Too many messages",
		Code:       http.StatusBadRequest,
		StatusCode: http.StatusBadRequest,
	}

	ErrMessageTooLong = APIError{
		Type:       "invalid_request_error",
		Message:    "Message too long",
		Code:       http.StatusBadRequest,
		StatusCode: http.StatusBadRequest,
	}

	// 工具相关错误
	ErrInvalidTool = APIError{
		Type:       "invalid_request_error",
		Message:    "Invalid tool definition",
		Code:       http.StatusBadRequest,
		StatusCode: http.StatusBadRequest,
		Param:      "tools",
	}

	ErrTooManyTools = APIError{
		Type:       "invalid_request_error",
		Message:    "Too many tools",
		Code:       http.StatusBadRequest,
		StatusCode: http.StatusBadRequest,
		Param:      "tools",
	}

	ErrToolCallFailed = APIError{
		Type:       "invalid_request_error",
		Message:    "Tool call failed",
		Code:       http.StatusBadRequest,
		StatusCode: http.StatusBadRequest,
	}

	// 上游相关错误
	ErrUpstreamError = APIError{
		Type:       "upstream_error",
		Message:    "Upstream service error",
		Code:       http.StatusBadGateway,
		StatusCode: http.StatusBadGateway,
	}

	ErrUpstreamTimeout = APIError{
		Type:       "upstream_error",
		Message:    "Upstream service timeout",
		Code:       http.StatusGatewayTimeout,
		StatusCode: http.StatusGatewayTimeout,
	}

	ErrUpstreamUnavailable = APIError{
		Type:       "upstream_error",
		Message:    "Upstream service unavailable",
		Code:       http.StatusServiceUnavailable,
		StatusCode: http.StatusServiceUnavailable,
	}

	// 系统相关错误
	ErrInternalError = APIError{
		Type:       "internal_error",
		Message:    "Internal server error",
		Code:       http.StatusInternalServerError,
		StatusCode: http.StatusInternalServerError,
	}

	ErrRateLimited = APIError{
		Type:       "rate_limit_error",
		Message:    "Rate limit exceeded",
		Code:       http.StatusTooManyRequests,
		StatusCode: http.StatusTooManyRequests,
	}

	ErrServiceUnavailable = APIError{
		Type:       "api_error",
		Message:    "Service temporarily unavailable",
		Code:       http.StatusServiceUnavailable,
		StatusCode: http.StatusServiceUnavailable,
	}
)

// NewInvalidRequestError 创建无效请求错误
func NewInvalidRequestError(message string) APIError {
	return APIError{
		Type:       "invalid_request_error",
		Message:    message,
		Code:       http.StatusBadRequest,
		StatusCode: http.StatusBadRequest,
	}
}

// NewInvalidRequestErrorWithParam 创建带参数的无效请求错误
func NewInvalidRequestErrorWithParam(message, param string) APIError {
	return APIError{
		Type:       "invalid_request_error",
		Message:    message,
		Code:       http.StatusBadRequest,
		StatusCode: http.StatusBadRequest,
		Param:      param,
	}
}

// NewValidationError 创建验证错误
func NewValidationError(message string) APIError {
	return APIError{
		Type:       "invalid_request_error",
		Message:    message,
		Code:       http.StatusBadRequest,
		StatusCode: http.StatusBadRequest,
	}
}

// NewValidationErrorWithParam 创建带参数的验证错误
func NewValidationErrorWithParam(message, param string) APIError {
	return APIError{
		Type:       "invalid_request_error",
		Message:    message,
		Code:       http.StatusBadRequest,
		StatusCode: http.StatusBadRequest,
		Param:      param,
	}
}

// NewUpstreamError 创建上游错误
func NewUpstreamError(message string) APIError {
	return APIError{
		Type:       "upstream_error",
		Message:    message,
		Code:       http.StatusBadGateway,
		StatusCode: http.StatusBadGateway,
	}
}

// NewInternalError 创建内部错误
func NewInternalError(message string) APIError {
	return APIError{
		Type:       "internal_error",
		Message:    message,
		Code:       http.StatusInternalServerError,
		StatusCode: http.StatusInternalServerError,
	}
}

// WrapError 包装标准错误为APIError
func WrapError(err error) APIError {
	if apiErr, ok := err.(APIError); ok {
		return apiErr
	}
	return NewInternalError(err.Error())
}

// IsAPIError 检查是否为API错误
func IsAPIError(err error) bool {
	_, ok := err.(APIError)
	return ok
}