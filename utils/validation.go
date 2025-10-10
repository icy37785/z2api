package utils

import (
	"errors"
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"
)

// CustomValidator 自定义验证器
type CustomValidator struct {
	validator *validator.Validate
}

// NewCustomValidator 创建自定义验证器
func NewCustomValidator() *CustomValidator {
	v := validator.New()

	// 注册自定义验证规则
	v.RegisterValidation("openai_model", validateOpenAIModel)
	v.RegisterValidation("temperature", validateTemperature)
	v.RegisterValidation("top_p", validateTopP)

	return &CustomValidator{validator: v}
}

// validateOpenAIModel 验证模型名称
func validateOpenAIModel(fl validator.FieldLevel) bool {
	model := fl.Field().String()
	validModels := []string{
		"glm-4.5", "glm-4.5-air", "glm-4.5-thinking",
		"glm-4.5-search", "glm-4.5v", "glm-4.6",
		"gpt-4", "gpt-3.5-turbo", "claude-3", // 兼容映射
	}

	for _, valid := range validModels {
		if model == valid || strings.HasPrefix(model, valid) {
			return true
		}
	}
	return false
}

// validateTemperature 验证temperature参数
func validateTemperature(fl validator.FieldLevel) bool {
	temp := fl.Field().Float()
	return temp >= 0.0 && temp <= 2.0
}

// validateTopP 验证top_p参数
func validateTopP(fl validator.FieldLevel) bool {
	topP := fl.Field().Float()
	return topP >= 0.0 && topP <= 1.0
}

// ValidateRequest 验证请求结构体
func (cv *CustomValidator) ValidateRequest(c *gin.Context, req interface{}) error {
	if err := c.ShouldBindJSON(req); err != nil {
		return fmt.Errorf("invalid JSON format: %w", err)
	}

	if err := cv.validator.Struct(req); err != nil {
		return cv.formatValidationError(err)
	}

	return nil
}

// formatValidationError 格式化验证错误
func (cv *CustomValidator) formatValidationError(err error) error {
	var validationErrors validator.ValidationErrors
	if errors.As(err, &validationErrors) {
		var errorMessages []string

		for _, e := range validationErrors {
			switch e.Tag() {
			case "required":
				errorMessages = append(errorMessages, fmt.Sprintf("Field '%s' is required", e.Field()))
			case "min":
				errorMessages = append(errorMessages, fmt.Sprintf("Field '%s' must be at least %s", e.Field(), e.Param()))
			case "max":
				errorMessages = append(errorMessages, fmt.Sprintf("Field '%s' must be at most %s", e.Field(), e.Param()))
			case "openai_model":
				errorMessages = append(errorMessages, fmt.Sprintf("Invalid model: %s", e.Value()))
			case "temperature":
				errorMessages = append(errorMessages, "Temperature must be between 0.0 and 2.0")
			case "top_p":
				errorMessages = append(errorMessages, "Top_p must be between 0.0 and 1.0")
			default:
				errorMessages = append(errorMessages, fmt.Sprintf("Field '%s' failed validation: %s", e.Field(), e.Tag()))
			}
		}

		return fmt.Errorf("validation failed: %s", strings.Join(errorMessages, "; "))
	}

	return err
}

// ValidatedOpenAIRequest 带验证标签的OpenAI请求
// TODO: Move type definitions from main package or create shared types package
/*
type ValidatedOpenAIRequest struct {
	Model       string      `json:"model" binding:"required,openai_model"`
	Messages    []Message   `json:"messages" binding:"required,min=1,dive"`
	Temperature *float64    `json:"temperature,omitempty" binding:"omitempty,temperature"`
	TopP        *float64    `json:"top_p,omitempty" binding:"omitempty,top_p"`
	MaxTokens   *int        `json:"max_tokens,omitempty" binding:"omitempty,min=1,max=150000"`
	Stream      bool        `json:"stream"`
	N           int         `json:"n,omitempty" binding:"omitempty,min=1,max=10"`
	Stop        interface{} `json:"stop,omitempty"`
	Tools       []Tool      `json:"tools,omitempty"`
	ToolChoice  interface{} `json:"tool_choice,omitempty"`
	User        string      `json:"user,omitempty"`
}
*/

// ValidatedMessage 带验证的消息
type ValidatedMessage struct {
	Role    string      `json:"role" binding:"required,oneof=system user assistant tool"`
	Content interface{} `json:"content" binding:"required"`
	Name    string      `json:"name,omitempty"`
}

// ValidateChatRequest 验证聊天请求的快捷方法
// TODO: Uncomment when types are moved to shared package
/*
func ValidateChatRequest(c *gin.Context) (*ValidatedOpenAIRequest, error) {
	var req ValidatedOpenAIRequest

	if err := c.ShouldBindJSON(&req); err != nil {
		// 解析错误信息，返回更友好的错误
		if strings.Contains(err.Error(), "EOF") {
			return nil, fmt.Errorf("empty request body")
		}
		if strings.Contains(err.Error(), "invalid character") {
			return nil, fmt.Errorf("invalid JSON format")
		}
		return nil, fmt.Errorf("request validation failed: %w", err)
	}

	// 额外的业务逻辑验证
	if err := validateBusinessRules(&req); err != nil {
		return nil, err
	}

	return &req, nil
}

// validateBusinessRules 业务规则验证
func validateBusinessRules(req *ValidatedOpenAIRequest) error {
	// 检查消息内容
	if len(req.Messages) == 0 {
		return fmt.Errorf("messages cannot be empty")
	}

	// 检查工具调用的一致性
	if len(req.Tools) > 0 && req.Stream {
		// 流式模式下的工具调用需要特殊处理
		// 这里可以添加警告或特殊逻辑
	}

	// 检查token限制
	if req.MaxTokens != nil && *req.MaxTokens > 150000 {
		return fmt.Errorf("max_tokens exceeds maximum limit of 150000")
	}

	return nil
}
*/

// ValidateAPIKey 验证API密钥
func ValidateAPIKey(c *gin.Context, validKey string) error {
	authHeader := c.GetHeader("Authorization")

	if authHeader == "" {
		return fmt.Errorf("missing Authorization header")
	}

	if !strings.HasPrefix(authHeader, "Bearer ") {
		return fmt.Errorf("invalid Authorization header format")
	}

	apiKey := strings.TrimPrefix(authHeader, "Bearer ")
	if apiKey != validKey {
		return fmt.Errorf("invalid API key")
	}

	return nil
}