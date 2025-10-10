// Package types 定义了所有的公共数据类型
package types

import (
	"sync"
	"time"
)

// ============================================
// OpenAI API 相关类型
// ============================================

// OpenAIRequest OpenAI 请求结构
type OpenAIRequest struct {
	Model             string                 `json:"model" binding:"required,oneof=glm-4.5 glm-4.5-thinking glm-4.5-search glm-4.5v glm-4.5-air glm-4.6 gpt-4 gpt-4-turbo gpt-3.5-turbo claude-3-opus claude-3-sonnet claude-3-haiku deepseek-chat deepseek-coder"`
	Messages          []Message              `json:"messages" binding:"required,min=1,max=50"`
	Stream            bool                   `json:"stream,omitempty"`
	Temperature       *float64               `json:"temperature,omitempty" binding:"omitempty,gte=0,lte=2"`       // 使用指针表示可选
	MaxTokens         *int                   `json:"max_tokens,omitempty" binding:"omitempty,gte=1,lte=240000"`        // 使用指针表示可选
	TopP              *float64               `json:"top_p,omitempty" binding:"omitempty,gte=0,lte=1"`             // 使用指针表示可选
	N                 *int                   `json:"n,omitempty" binding:"omitempty,gte=1,lte=10"`                 // 使用指针表示可选
	Stop              interface{}            `json:"stop,omitempty" binding:"omitempty"`              // string or []string
	PresencePenalty   *float64               `json:"presence_penalty,omitempty" binding:"omitempty,gte=-2,lte=2"`  // 使用指针表示可选
	FrequencyPenalty  *float64               `json:"frequency_penalty,omitempty" binding:"omitempty,gte=-2,lte=2"` // 使用指针表示可选
	LogitBias         map[string]float64     `json:"logit_bias,omitempty" binding:"omitempty"`        // 修正为float64
	User              string                 `json:"user,omitempty" binding:"omitempty,max=100"`
	Tools             []Tool                 `json:"tools,omitempty" binding:"omitempty,max=20"`
	ToolChoice        interface{}            `json:"tool_choice,omitempty" binding:"omitempty"` // 保持interface{}以支持多种格式
	ResponseFormat    interface{}            `json:"response_format,omitempty" binding:"omitempty"`
	Seed              *int                   `json:"seed,omitempty" binding:"omitempty,gte=0"` // 使用指针表示可选
	LogProbs          bool                   `json:"logprobs,omitempty"`
	TopLogProbs       *int                   `json:"top_logprobs,omitempty" binding:"omitempty,gte=0,lte=5"`        // 使用指针，需要0-5验证
	ParallelToolCalls *bool                  `json:"parallel_tool_calls,omitempty"` // 使用指针表示可选
	ServiceTier       string                 `json:"service_tier,omitempty" binding:"omitempty,oneof=auto default"`        // 新增：服务层级
	Store             *bool                  `json:"store,omitempty"`               // 新增：是否存储
	Metadata          map[string]interface{} `json:"metadata,omitempty" binding:"omitempty"`            // 新增：元数据
	// 符合OpenAI标准的兼容性参数
	MaxCompletionTokens *int        `json:"max_completion_tokens,omitempty" binding:"omitempty,gte=1,lte=240000"` // 最大完成token数
	TopK                *int        `json:"top_k,omitempty" binding:"omitempty,gte=1,lte=100"`                 // Top-k采样
	MinP                *float64    `json:"min_p,omitempty" binding:"omitempty,gte=0,lte=1"`                 // Min-p采样
	BestOf              *int        `json:"best_of,omitempty" binding:"omitempty,gte=1,lte=5"`               // 最佳结果数
	RepetitionPenalty   *float64    `json:"repetition_penalty,omitempty" binding:"omitempty,gte=0,lte=2"`    // 重复惩罚
	Grammar             interface{} `json:"grammar,omitempty" binding:"omitempty"`               // 语法约束
	GrammarType         string      `json:"grammar_type,omitempty" binding:"omitempty,oneof=bnf gbnf regex"`          // 语法类型
	// 保持向后兼容的参数
	MaxInputTokens      *int `json:"max_input_tokens,omitempty" binding:"omitempty,gte=1,lte=1000000"`      // 最大输入token数
	MinCompletionTokens *int `json:"min_completion_tokens,omitempty" binding:"omitempty,gte=0,lte=240000"` // 最小完成token数
	// 新增工具调用增强参数
	ToolChoiceObject *ToolChoice `json:"-"` // 内部使用的解析后的ToolChoice对象
}

// OpenAIResponse OpenAI 响应结构
type OpenAIResponse struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   *Usage   `json:"usage,omitempty"` // 使用指针，只在需要时设置
}

// Choice 选择结构
type Choice struct {
	Index        int      `json:"index"`
	Message      *Message `json:"message,omitempty"` // 改为指针类型，流式响应时为nil
	Delta        Delta    `json:"delta,omitempty"`
	FinishReason string   `json:"finish_reason,omitempty"`
}

// Delta 增量结构
type Delta struct {
	Role             string     `json:"role,omitempty"`
	Content          string     `json:"content,omitempty"`
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
}

// Usage 用量结构
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ============================================
// 消息和内容相关类型
// ============================================

// Message 消息结构（支持多模态内容）
type Message struct {
	Role             string      `json:"role" binding:"required,oneof=system user assistant developer tool"`
	Content          interface{} `json:"content" binding:"required"` // 支持 string 或 []ContentPart
	ReasoningContent string      `json:"reasoning_content,omitempty" binding:"omitempty,max=10000"`
	ToolCalls        []ToolCall  `json:"tool_calls,omitempty" binding:"omitempty,max=10"`
}

// ContentPart 内容部分结构（用于多模态消息）
type ContentPart struct {
	Type        string       `json:"type" binding:"required,oneof=text image_url video_url document_url audio_url file"`
	Text        string       `json:"text,omitempty" binding:"omitempty,max=100000"`
	ImageURL    *ImageURL    `json:"image_url,omitempty"`
	VideoURL    *VideoURL    `json:"video_url,omitempty"`
	DocumentURL *DocumentURL `json:"document_url,omitempty"`
	AudioURL    *AudioURL    `json:"audio_url,omitempty"`
	// 兼容性字段
	URL      string `json:"url,omitempty" binding:"omitempty,url"`       // 保持向后兼容
	AltText  string `json:"alt_text,omitempty" binding:"omitempty,max=500"`  // 替代文本
	Size     int64  `json:"size,omitempty" binding:"omitempty,gte=0,lte=104857600"`     // 文件大小 (最大100MB)
	MimeType string `json:"mime_type,omitempty" binding:"omitempty,max=100"` // MIME类型
}

// ImageURL 图像URL结构
type ImageURL struct {
	URL    string `json:"url" binding:"required,url"`
	Detail string `json:"detail,omitempty" binding:"omitempty,oneof=low high auto"`
}

// VideoURL 视频URL结构
type VideoURL struct {
	URL string `json:"url" binding:"required,url"`
}

// DocumentURL 文档URL结构
type DocumentURL struct {
	URL string `json:"url" binding:"required,url"`
}

// AudioURL 音频URL结构
type AudioURL struct {
	URL string `json:"url" binding:"required,url"`
}

// ============================================
// 工具调用相关类型
// ============================================

// Tool 工具结构
type Tool struct {
	Type     string       `json:"type" binding:"required,oneof=function"`
	Function ToolFunction `json:"function" binding:"required"`
	// 添加更多工具参数以增强兼容性
	Description string                 `json:"description,omitempty" binding:"omitempty,max=500"`
	Parameters  map[string]interface{} `json:"parameters,omitempty" binding:"omitempty"`
	Strict      *bool                  `json:"strict,omitempty"`
	Context     map[string]interface{} `json:"context,omitempty" binding:"omitempty"`
}

// ToolFunction 工具函数结构
type ToolFunction struct {
	Name        string                 `json:"name" binding:"required,max=64"`
	Description string                 `json:"description" binding:"required,max=500"`
	Parameters  map[string]interface{} `json:"parameters" binding:"required"` // 使用具体类型而不是interface{}
	// 添加更多工具函数参数以增强兼容性
	Strict   *bool                  `json:"strict,omitempty"`   // 严格模式
	Require  []string               `json:"require,omitempty" binding:"omitempty,max=20"`  // 必需参数
	Optional []string               `json:"optional,omitempty" binding:"omitempty,max=20"` // 可选参数
	Context  map[string]interface{} `json:"context,omitempty" binding:"omitempty"`  // 上下文信息
}

// ToolCall 工具调用结构 - 符合OpenAI API标准
type ToolCall struct {
	Index    int              `json:"-"`        // 内部使用，不序列化到JSON
	ID       string           `json:"id" binding:"required"`       // 必需字段
	Type     string           `json:"type" binding:"required,oneof=function"`     // 必需字段，通常为"function"
	Function ToolCallFunction `json:"function" binding:"required"` // 必需字段
}

// ToolCallFunction 工具调用函数结构
type ToolCallFunction struct {
	Name      string `json:"name" binding:"required,max=64"`
	Arguments string `json:"arguments" binding:"required"`
}

// ToolChoice 工具选择结构
type ToolChoice struct {
	Type     string              `json:"type,omitempty"`
	Function *ToolChoiceFunction `json:"function,omitempty"`
}

// ToolChoiceFunction 工具选择函数结构
type ToolChoiceFunction struct {
	Name string `json:"name"`
}

// ============================================
// 上游请求/响应相关类型
// ============================================

// UpstreamMessage 上游消息结构（简化格式，仅支持字符串内容）
type UpstreamMessage struct {
	Role             string `json:"role"`
	Content          string `json:"content"`
	ReasoningContent string `json:"reasoning_content,omitempty"`
}

// UpstreamRequest 上游请求结构
type UpstreamRequest struct {
	Stream          bool                   `json:"stream"`
	Model           string                 `json:"model"`
	Messages        []UpstreamMessage      `json:"messages"`
	Params          map[string]interface{} `json:"params"`
	Features        map[string]interface{} `json:"features"`
	BackgroundTasks map[string]bool        `json:"background_tasks,omitempty"`
	ChatID          string                 `json:"chat_id,omitempty"`
	ID              string                 `json:"id,omitempty"`
	MCPServers      []string               `json:"mcp_servers,omitempty"`
	ModelItem       struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		OwnedBy string `json:"owned_by"`
	} `json:"model_item,omitempty"`
	ToolServers []string          `json:"tool_servers,omitempty"`
	Variables   map[string]string `json:"variables,omitempty"`
	Tools       []Tool            `json:"tools,omitempty"`
	ToolChoice  interface{}       `json:"tool_choice,omitempty"`
}

// UpstreamData 上游SSE响应结构
type UpstreamData struct {
	Type string `json:"type"`
	Data struct {
		DeltaContent string         `json:"delta_content"`
		EditContent  string         `json:"edit_content"`
		Phase        string         `json:"phase"`
		EditIndex    int            `json:"edit_index"`
		Done         bool           `json:"done"`
		Usage        Usage          `json:"usage,omitempty"`
		Error        *UpstreamError `json:"error,omitempty"`
		ToolCalls    []ToolCall     `json:"tool_calls,omitempty"`
		Inner        *struct {
			Error *UpstreamError `json:"error,omitempty"`
		} `json:"data,omitempty"`
	} `json:"data"`
	Error *UpstreamError `json:"error,omitempty"`
}

// UpstreamError 上游错误结构
type UpstreamError struct {
	Detail string `json:"detail"`
	Code   int    `json:"code"`
}

// ============================================
// 模型相关类型
// ============================================

// ModelsResponse 模型列表响应
type ModelsResponse struct {
	Object string  `json:"object"`
	Data   []Model `json:"data"`
}

// Model 模型结构
type Model struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

// ============================================
// 错误相关类型
// ============================================

// ErrorResponse 标准错误响应格式
type ErrorResponse struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail 错误详情
type ErrorDetail struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    int    `json:"code"`
	Debug   string `json:"debug,omitempty"` // 仅在调试模式下显示
}

// ============================================
// 统计相关类型
// ============================================

// RequestStats 请求统计结构
type RequestStats struct {
	TotalRequests        int64
	SuccessfulRequests   int64
	FailedRequests       int64
	LastRequestTime      time.Time // 修改回 time.Time
	AverageResponseTime  float64
	HomePageViews        int64
	ApiCallsCount        int64
	ModelsCallsCount     int64
	StreamingRequests    int64
	NonStreamingRequests int64
	TotalTokensUsed      int64
	StartTime            time.Time // 修改回 time.Time
	FastestResponse      float64
	SlowestResponse      float64
	ModelUsage           map[string]int64
	Mutex                sync.RWMutex // 改为公开字段
}

// LiveRequest 实时请求结构
type LiveRequest struct {
	ID        string    `json:"id"`
	Timestamp time.Time `json:"timestamp"` // 修改回 time.Time
	Method    string    `json:"method"`
	Path      string    `json:"path"`
	Status    int       `json:"status"`
	Duration  float64   `json:"duration"`
	UserAgent string    `json:"userAgent"`
	Model     string    `json:"model,omitempty"`
}

// ============================================
// 配置相关类型
// ============================================

// Config 配置结构体
type Config struct {
	UpstreamUrl           string
	DefaultKey            string
	UpstreamToken         string
	Port                  string
	DebugMode             bool
	ThinkTagsMode         string
	AnonTokenEnabled      bool
	MaxConcurrentRequests int
}

// ============================================
// 特征配置相关类型
// ============================================

// Features 模型特征配置
type Features struct {
	Vision              bool     `json:"vision"`
	Reasoning           bool     `json:"reasoning"`
	Artifacts           bool     `json:"artifacts"`
	JSON                bool     `json:"json"`
	PDF                 bool     `json:"pdf"`
	Audio               bool     `json:"audio"`
	FileGeneration      bool     `json:"file_generation"`
	WriteAndExecute     bool     `json:"write_and_execute"`
	CanvasEdit          bool     `json:"canvas_edit"`
	HiddenReasoning     bool     `json:"hidden_reasoning"`
	MCPServers          []string `json:"mcp_servers,omitempty"`
	HideToolIndicator   bool     `json:"hide_tool_indicator,omitempty"`
	SystemPromptSupport bool     `json:"system_prompt_support,omitempty"`
}

// ToMap 将Features转换为map[string]interface{}
func (f Features) ToMap() map[string]interface{} {
	return map[string]interface{}{
		"vision":                f.Vision,
		"reasoning":             f.Reasoning,
		"artifacts":             f.Artifacts,
		"json":                  f.JSON,
		"pdf":                   f.PDF,
		"audio":                 f.Audio,
		"file_generation":       f.FileGeneration,
		"write_and_execute":     f.WriteAndExecute,
		"canvas_edit":           f.CanvasEdit,
		"hidden_reasoning":      f.HiddenReasoning,
		"hide_tool_indicator":   f.HideToolIndicator,
		"system_prompt_support": f.SystemPromptSupport,
	}
}

// FeatureConfig 特征配置
type FeatureConfig struct {
	Features        Features        `json:"features"`
	BackgroundTasks map[string]bool `json:"background_tasks"`
	ToolServers     []string        `json:"tool_servers"`
}

// ============================================
// 监控相关类型
// ============================================

// StatsUpdate 统计更新数据
type StatsUpdate struct {
	StartTime   time.Time // 修改回 time.Time
	Path        string
	Status      int
	Tokens      int64
	Model       string
	IsStreaming bool
	Duration    float64
	UserAgent   string
	Method      string
}

// Float64Ptr 返回float64值的指针
func Float64Ptr(v float64) *float64 {
	return &v
}

// IntPtr 返回int值的指针
func IntPtr(v int) *int {
	return &v
}

// BoolPtr 返回bool值的指针
func BoolPtr(v bool) *bool {
	return &v
}

// StringPtr 返回string值的指针
func StringPtr(v string) *string {
	return &v
}
