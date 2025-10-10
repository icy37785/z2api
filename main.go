package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"expvar"
	"fmt"
	"hash/fnv"
	"io"
	"log"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/bytedance/sonic"
	"github.com/bytedance/sonic/decoder"

	// 内部包
	"z2api/config"
	"z2api/internal/signature"

	// 第三方包
	"github.com/andybalholm/brotli"
	"github.com/google/uuid"
	"golang.org/x/sync/semaphore"
	"golang.org/x/sync/singleflight"
)

// 自定义 sonic 配置 - 针对不同场景的优化配置
var (
	// 默认配置：平衡性能和兼容性
	sonicDefault = sonic.ConfigDefault

	// 流式配置：优化SSE流处理
	sonicStream = sonic.Config{
		EscapeHTML:       false, // SSE不需要HTML转义
		SortMapKeys:      false, // 不排序键，提高性能
		CompactMarshaler: true,  // 紧凑输出
		CopyString:       false, // 避免字符串复制
		// 跳过原始消息验证以提高性能
	}.Froze()

	// 高性能配置：用于内部数据传输
	sonicFast = sonic.Config{
		EscapeHTML:            false,
		SortMapKeys:           false,
		CompactMarshaler:      true,
		CopyString:            false,
		UseInt64:              false, // 使用更快的整数处理
		UseNumber:             false, // 直接使用数字类型
		DisallowUnknownFields: false, // 忽略未知字段
		NoQuoteTextMarshaler:  true,  // 跳过文本引号处理
	}.Froze()

	// 兼容配置：用于外部API交互
	sonicCompatible = sonic.Config{
		EscapeHTML:       true,  // 保持HTML转义
		SortMapKeys:      true,  // 排序键以保持一致性
		CompactMarshaler: false, // 保持格式化
		CopyString:       true,  // 复制字符串以避免引用问题
		// 启用完整验证以确保兼容性
	}.Froze()
)

// sonic 编码器/解码器对象池 - 减少内存分配
var (
	// 流式解码器池
	streamDecoderPool = sync.Pool{
		New: func() interface{} {
			return decoder.NewStreamDecoder(nil)
		},
	}
)

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

// TokenCache Token缓存结构体
type TokenCache struct {
	token     string
	expiresAt time.Time
	mutex     sync.RWMutex
	sf        singleflight.Group
}

// getEnv 获取环境变量值，如果不存在则返回默认值
func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// hashString 计算字符串的哈希值，复刻 Python 的 hash() 行为
func hashString(s string) uint32 {
	h := fnv.New32a()
	h.Write([]byte(s))
	return h.Sum32()
}

// loadConfig 加载并验证配置
func loadConfig() (*Config, error) {
	maxConcurrent := 100 // 默认值
	if envVal := getEnv("MAX_CONCURRENT_REQUESTS", "100"); envVal != "100" {
		if parsed, err := strconv.Atoi(envVal); err == nil && parsed > 0 && parsed <= 1000 {
			maxConcurrent = parsed
		}
	}

	port := getEnv("PORT", "8080")
	// 端口格式规范化：确保以冒号开头
	if !strings.HasPrefix(port, ":") {
		port = ":" + port
	}

	config := &Config{
		UpstreamUrl:           getEnv("UPSTREAM_URL", "https://chat.z.ai/api/chat/completions"),
		DefaultKey:            getEnv("API_KEY", "sk-tbkFoKzk9a531YyUNNF5"),
		UpstreamToken:         getEnv("UPSTREAM_TOKEN", ""),
		Port:                  port,
		DebugMode:             getEnv("DEBUG_MODE", "true") == "true",
		ThinkTagsMode:         getEnv("THINK_TAGS_MODE", "think"), // strip, think, raw
		AnonTokenEnabled:      getEnv("ANON_TOKEN_ENABLED", "true") == "true",
		MaxConcurrentRequests: maxConcurrent,
	}

	// 配置验证
	if err := config.validate(); err != nil {
		return nil, err
	}

	return config, nil
}

// validate 验证配置合法性，增强端口范围检查
func (c *Config) validate() error {
	// 验证API密钥不能为空
	if c.DefaultKey == "" {
		return fmt.Errorf("API_KEY 环境变量是必需的，不能为空")
	}

	// 验证URL格式
	if !strings.HasPrefix(c.UpstreamUrl, "http") {
		return fmt.Errorf("UPSTREAM_URL 必须是有效的HTTP URL")
	}

	// 验证端口是否为有效数字和范围
	portNumStr := strings.TrimPrefix(c.Port, ":")
	portNum, err := strconv.Atoi(portNumStr)
	if err != nil {
		return fmt.Errorf("PORT 必须是一个有效的端口号")
	}

	// 检查端口范围 (1-65535)
	if portNum < 1 || portNum > 65535 {
		return fmt.Errorf("PORT 必须在1-65535范围内，当前值: %d", portNum)
	}

	// 检查常见的受限端口（系统保留端口）
	if portNum < 1024 {
		debugLog("警告: 端口 %d 是系统保留端口，可能需要管理员权限", portNum)
	}

	// 验证ThinkTagsMode
	validModes := []string{"strip", "think", "raw"}
	valid := false
	for _, mode := range validModes {
		if c.ThinkTagsMode == mode {
			valid = true
			break
		}
	}
	if !valid {
		return fmt.Errorf("THINK_TAGS_MODE 必须是以下值之一: %v", validModes)
	}

	// 验证并发数限制
	if c.MaxConcurrentRequests <= 0 || c.MaxConcurrentRequests > 1000 {
		return fmt.Errorf("MAX_CONCURRENT_REQUESTS 必须在 1-1000 之间")
	}

	// 如果未启用匿名令牌，且没有提供上游令牌，则报错
	if !c.AnonTokenEnabled && c.UpstreamToken == "" {
		return fmt.Errorf("当 ANON_TOKEN_ENABLED 为 false 时，UPSTREAM_TOKEN 环境变量是必需的")
	}

	return nil
}

// 模型常量
const (
	DefaultModelName        = "glm-4.5"
	ThinkingModelName       = "glm-4.5-thinking"
	SearchModelName         = "glm-4.5-search"
	GLMAirModelName         = "glm-4.5-air"
	GLMVision               = "glm-4.5v"
	MaxResponseSize   int64 = 10 * 1024 * 1024 // 10MB
)

// 全局配置和缓存实例
var (
	appConfig  *Config
	tokenCache *TokenCache
)

// 监控指标
var (
	totalRequests      = expvar.NewInt("total_requests")
	currentConcurrency = expvar.NewInt("current_concurrency")
	requestErrors      = expvar.NewMap("request_errors")
	tokenCacheHits     = expvar.NewInt("token_cache_hits")
	tokenCacheMisses   = expvar.NewInt("token_cache_misses")
)

// RequestStats 请求统计结构
type RequestStats struct {
	TotalRequests        int64
	SuccessfulRequests   int64
	FailedRequests       int64
	LastRequestTime      time.Time
	AverageResponseTime  float64
	HomePageViews        int64
	ApiCallsCount        int64
	ModelsCallsCount     int64
	StreamingRequests    int64
	NonStreamingRequests int64
	TotalTokensUsed      int64
	StartTime            time.Time
	FastestResponse      float64
	SlowestResponse      float64
	ModelUsage           map[string]int64
	mutex                sync.RWMutex
}

// LiveRequest 实时请求结构
type LiveRequest struct {
	ID        string    `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	Method    string    `json:"method"`
	Path      string    `json:"path"`
	Status    int       `json:"status"`
	Duration  float64   `json:"duration"`
	UserAgent string    `json:"userAgent"`
	Model     string    `json:"model,omitempty"`
}

// 全局统计实例
var (
	stats             *RequestStats
	liveRequests      []LiveRequest
	liveRequestsMutex sync.RWMutex
)

// 伪装前端头部（来自抓包） - now loaded from fingerprints.json
var (
	DefaultXFeVersion = "prod-fe-1.0.95"
	DefaultSecChUaMob = "?0"
)

const (
	OriginBase = "https://chat.z.ai"
)

// 全局HTTP客户端（连接池复用）
var (
	httpClient = &http.Client{
		Timeout: 0,
		Transport: &http.Transport{
			MaxIdleConns:          100,              // 优化：增加全局空闲连接数，减少高并发下建立新连接的开销
			MaxIdleConnsPerHost:   10,               // 优化：保持单主机空闲连接数
			MaxConnsPerHost:       50,               // 优化：减少单主机最大连接数
			IdleConnTimeout:       90 * time.Second, // 优化：缩短空闲连接超时，提高连接回收效率
			TLSHandshakeTimeout:   10 * time.Second, // TLS握手超时
			ExpectContinueTimeout: 1 * time.Second,  // Expect: 100-continue超时
			ResponseHeaderTimeout: 0,                // 响应头超时
			DisableKeepAlives:     false,            // 启用Keep-Alive
			DisableCompression:    false,            // 恢复自动压缩
		},
	}

	// 预编译的正则表达式模式
	summaryRegex      = regexp.MustCompile(`(?s)<summary>.*?</summary>`)
	detailsRegex      = regexp.MustCompile(`<details[^>]*>`)
	detailsCloseRegex = regexp.MustCompile(`(?i)\s*</details\s*>`)

	// 字符串替换器
	thinkingReplacer = strings.NewReplacer(
		"</thinking>", "",
		"<Full>", "",
		"</Full>", "",
		"\n> ", "\n",
		"</details>", "</think>", // 为think模式预设
	)

	// strip模式的替换器
	thinkingStripReplacer = strings.NewReplacer(
		"</thinking>", "",
		"<Full>", "",
		"</Full>", "",
		"\n> ", "\n",
		"</details>", "", // 为strip模式预设
	)

	// 对象池优化内存分配
	stringBuilderPool = sync.Pool{
		New: func() interface{} {
			return &strings.Builder{}
		},
	}

	// 添加更多对象池优化内存分配
	bytesBufferPool = sync.Pool{
		New: func() interface{} {
			return bytes.NewBuffer(make([]byte, 0, 1024)) // 预分配1KB
		},
	}
	/*
		// SSE 响应缓冲区对象池
		sseBufferPool = sync.Pool{
			New: func() interface{} {
				return make([]byte, 0, 1024) // 预分配1KB
			},
		}
	*/
	// 并发控制：限制同时处理的请求数量
	// 这可以防止在高并发时消耗过多资源
	// 注意：会在main函数中根据配置重新创建
	concurrencyLimiter *semaphore.Weighted
)

// gzipReadCloser 包装gzip.Reader和原始的io.ReadCloser
type gzipReadCloser struct {
	reader *gzip.Reader
	source io.ReadCloser
}

func (gz *gzipReadCloser) Read(p []byte) (n int, err error) {
	return gz.reader.Read(p)
}

func (gz *gzipReadCloser) Close() error {
	gzipErr := gz.reader.Close()
	sourceErr := gz.source.Close()
	if gzipErr != nil {
		return gzipErr // 优先返回 Gzip 错误
	}
	return sourceErr // 否则返回源的错误
}

// brotliReadCloser 包装brotli.Reader和原始的io.ReadCloser
type brotliReadCloser struct {
	reader *brotli.Reader
	source io.ReadCloser
}

func (br *brotliReadCloser) Read(p []byte) (n int, err error) {
	return br.reader.Read(p)
}

func (br *brotliReadCloser) Close() error {
	// Brotli reader没有Close方法，但仍需关闭原始的响应体
	return br.source.Close()
}

// ToolCallFunction 工具调用函数结构
type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ToolCall 工具调用结构 - 符合OpenAI API标准
type ToolCall struct {
	Index    int              `json:"-"`        // 内部使用，不序列化到JSON
	ID       string           `json:"id"`       // 必需字段
	Type     string           `json:"type"`     // 必需字段，通常为"function"
	Function ToolCallFunction `json:"function"` // 必需字段
}

// normalizeToolCall 规范化工具调用，确保符合OpenAI API标准
// 为缺失的必需字段生成默认值
func normalizeToolCall(tc ToolCall) ToolCall {
	// 确保有ID，如果没有则生成
	if tc.ID == "" {
		tc.ID = fmt.Sprintf("call_%s", uuid.New().String()[:8])
	}

	// 确保有Type，默认为"function"
	if tc.Type == "" {
		tc.Type = "function"
	}

	// 确保Function.Name不为空（如果为空，说明数据有问题）
	if tc.Function.Name == "" {
		debugLog("警告: 工具调用缺少function.name字段")
	}

	// 确保Arguments至少是空的JSON对象
	if tc.Function.Arguments == "" {
		tc.Function.Arguments = "{}"
	}

	return tc
}

// normalizeToolCalls 批量规范化工具调用
func normalizeToolCalls(calls []ToolCall) []ToolCall {
	if len(calls) == 0 {
		return calls
	}

	normalized := make([]ToolCall, len(calls))
	for i, call := range calls {
		normalized[i] = normalizeToolCall(call)
	}
	return normalized
}

// ToolFunction 工具函数结构
type ToolFunction struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters"` // 使用具体类型而不是interface{}
	// 添加更多工具函数参数以增强兼容性
	Strict   *bool                  `json:"strict,omitempty"`   // 严格模式
	Require  []string               `json:"require,omitempty"`  // 必需参数
	Optional []string               `json:"optional,omitempty"` // 可选参数
	Context  map[string]interface{} `json:"context,omitempty"`  // 上下文信息
}

// ToolChoiceFunction 工具选择函数结构
type ToolChoiceFunction struct {
	Name string `json:"name"`
}

// ToolChoice 工具选择结构
type ToolChoice struct {
	Type     string              `json:"type,omitempty"`
	Function *ToolChoiceFunction `json:"function,omitempty"`
}

// Tool 工具结构
type Tool struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
	// 添加更多工具参数以增强兼容性
	Description string                 `json:"description,omitempty"`
	Parameters  map[string]interface{} `json:"parameters,omitempty"`
	Strict      *bool                  `json:"strict,omitempty"`
	Context     map[string]interface{} `json:"context,omitempty"`
}

// OpenAIRequest OpenAI 请求结构
type OpenAIRequest struct {
	Model             string                 `json:"model"`
	Messages          []Message              `json:"messages"`
	Stream            bool                   `json:"stream,omitempty"`
	Temperature       *float64               `json:"temperature,omitempty"`       // 使用指针表示可选
	MaxTokens         *int                   `json:"max_tokens,omitempty"`        // 使用指针表示可选
	TopP              *float64               `json:"top_p,omitempty"`             // 使用指针表示可选
	N                 *int                   `json:"n,omitempty"`                 // 使用指针表示可选
	Stop              interface{}            `json:"stop,omitempty"`              // string or []string
	PresencePenalty   *float64               `json:"presence_penalty,omitempty"`  // 使用指针表示可选
	FrequencyPenalty  *float64               `json:"frequency_penalty,omitempty"` // 使用指针表示可选
	LogitBias         map[string]float64     `json:"logit_bias,omitempty"`        // 修正为float64
	User              string                 `json:"user,omitempty"`
	Tools             []Tool                 `json:"tools,omitempty"`
	ToolChoice        interface{}            `json:"tool_choice,omitempty"` // 保持interface{}以支持多种格式
	ResponseFormat    interface{}            `json:"response_format,omitempty"`
	Seed              *int                   `json:"seed,omitempty"` // 使用指针表示可选
	LogProbs          bool                   `json:"logprobs,omitempty"`
	TopLogProbs       *int                   `json:"top_logprobs,omitempty"`        // 使用指针，需要0-5验证
	ParallelToolCalls *bool                  `json:"parallel_tool_calls,omitempty"` // 使用指针表示可选
	ServiceTier       string                 `json:"service_tier,omitempty"`        // 新增：服务层级
	Store             *bool                  `json:"store,omitempty"`               // 新增：是否存储
	Metadata          map[string]interface{} `json:"metadata,omitempty"`            // 新增：元数据
	// 符合OpenAI标准的兼容性参数
	MaxCompletionTokens *int        `json:"max_completion_tokens,omitempty"` // 最大完成token数
	TopK                *int        `json:"top_k,omitempty"`                 // Top-k采样
	MinP                *float64    `json:"min_p,omitempty"`                 // Min-p采样
	BestOf              *int        `json:"best_of,omitempty"`               // 最佳结果数
	RepetitionPenalty   *float64    `json:"repetition_penalty,omitempty"`    // 重复惩罚
	Grammar             interface{} `json:"grammar,omitempty"`               // 语法约束
	GrammarType         string      `json:"grammar_type,omitempty"`          // 语法类型
	// 保持向后兼容的参数
	MaxInputTokens      *int `json:"max_input_tokens,omitempty"`      // 最大输入token数
	MinCompletionTokens *int `json:"min_completion_tokens,omitempty"` // 最小完成token数
	// 新增工具调用增强参数
	ToolChoiceObject *ToolChoice `json:"-"` // 内部使用的解析后的ToolChoice对象
}

// ImageURL 图像URL结构
type ImageURL struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"` // low, high, auto
}

// VideoURL 视频URL结构
type VideoURL struct {
	URL string `json:"url"`
}

// DocumentURL 文档URL结构
type DocumentURL struct {
	URL string `json:"url"`
}

// AudioURL 音频URL结构
type AudioURL struct {
	URL string `json:"url"`
}

// ContentPart 内容部分结构（用于多模态消息）
type ContentPart struct {
	Type        string       `json:"type"`
	Text        string       `json:"text,omitempty"`
	ImageURL    *ImageURL    `json:"image_url,omitempty"`
	VideoURL    *VideoURL    `json:"video_url,omitempty"`
	DocumentURL *DocumentURL `json:"document_url,omitempty"`
	AudioURL    *AudioURL    `json:"audio_url,omitempty"`
	// 兼容性字段
	URL      string `json:"url,omitempty"`       // 保持向后兼容
	AltText  string `json:"alt_text,omitempty"`  // 替代文本
	Size     int64  `json:"size,omitempty"`      // 文件大小
	MimeType string `json:"mime_type,omitempty"` // MIME类型
}

// Message 消息结构（支持多模态内容）
type Message struct {
	Role             string      `json:"role"`
	Content          interface{} `json:"content"` // 支持 string 或 []ContentPart
	ReasoningContent string      `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCall  `json:"tool_calls,omitempty"`
}

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

// OpenAIResponse OpenAI 响应结构
type OpenAIResponse struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage,omitempty"`
}

// Choice 选择结构
type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message,omitempty"`
	Delta        Delta   `json:"delta,omitempty"`
	FinishReason string  `json:"finish_reason,omitempty"`
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

// extractTextContent 从多模态内容中提取文本
func extractTextContent(content interface{}) string {
	switch v := content.(type) {
	case string:
		// 简单文本内容
		return v
	case []ContentPart:
		// 新格式的多模态内容数组
		var textParts []string
		for _, part := range v {
			if part.Type == "text" && part.Text != "" {
				textParts = append(textParts, part.Text)
			}
		}
		result := strings.Join(textParts, " ")
		// 如果有内容且末尾没有空格，则添加空格以保持一致性
		if len(result) > 0 && !strings.HasSuffix(result, " ") {
			result += " "
		}
		return result
	default:
		// 其他类型，尝试转换为字符串
		return fmt.Sprintf("%v", v)
	}
}

// extractLastUserContent 从 UpstreamRequest 中提取最后一条用户消息的内容
func extractLastUserContent(req UpstreamRequest) string {
	// 从后往前遍历消息，找到最后一条 role 为 "user" 的消息
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == "user" {
			return req.Messages[i].Content
		}
	}
	// 如果没有找到用户消息，返回空字符串
	return ""
}

// processMultimodalContent 处理全方位多模态内容，支持图像、视频、文档、音频等
func processMultimodalContent(parts []ContentPart, model string) string {
	textContent := stringBuilderPool.Get().(*strings.Builder)
	defer func() {
		textContent.Reset()
		stringBuilderPool.Put(textContent)
	}()

	var mediaStats = struct {
		text      int
		images    int
		videos    int
		documents int
		audios    int
		others    int
	}{}

	for i, part := range parts {
		switch part.Type {
		case "text":
			if part.Text != "" {
				textContent.WriteString(part.Text)
				textContent.WriteString(" ")
				mediaStats.text++
			}
		case "image_url":
			if part.ImageURL != nil && part.ImageURL.URL != "" {
				debugLog("检测到图像内容[%d]: %s (detail: %s)", i, part.ImageURL.URL, part.ImageURL.Detail)
				mediaStats.images++

				// 根据URL类型记录信息
				if strings.HasPrefix(part.ImageURL.URL, "data:image/") {
					// Base64编码的图像数据
					dataSize := len(part.ImageURL.URL)
					debugLog("图像数据大小: %d 字符 (~%dKB)", dataSize, dataSize*3/4/1024)
				} else {
					debugLog("图像URL: %s", part.ImageURL.URL)
				}
			}
		case "video_url":
			if part.VideoURL != nil && part.VideoURL.URL != "" {
				debugLog("检测到视频内容[%d]: %s", i, part.VideoURL.URL)
				mediaStats.videos++
			}
		case "document_url":
			if part.DocumentURL != nil && part.DocumentURL.URL != "" {
				debugLog("检测到文档内容[%d]: %s", i, part.DocumentURL.URL)
				mediaStats.documents++
			}
		case "audio_url":
			if part.AudioURL != nil && part.AudioURL.URL != "" {
				debugLog("检测到音频内容[%d]: %s", i, part.AudioURL.URL)
				mediaStats.audios++
			}
		default:
			debugLog("检测到未知内容类型[%d]: %s", i, part.Type)
			mediaStats.others++
		}
	}

	// 记录媒体内容统计信息
	totalMedia := mediaStats.images + mediaStats.videos + mediaStats.documents + mediaStats.audios
	if totalMedia > 0 {
		debugLog("多模态内容统计: 文本(%d) 图像(%d) 视频(%d) 文档(%d) 音频(%d) 其他(%d)",
			mediaStats.text, mediaStats.images, mediaStats.videos, mediaStats.documents, mediaStats.audios, mediaStats.others)
	}

	// 对于支持多模态的模型（如glm-4.5v），可以保留更多上下文
	if strings.Contains(strings.ToLower(model), "vision") || strings.Contains(strings.ToLower(model), "v") {
		debugLog("模型 %s 支持全方位多模态理解", model)
	} else {
		debugLog("模型 %s 可能不支持多模态，仅保留文本内容", model)
	}

	return textContent.String()
}

// normalizeMultimodalMessage 规范化多模态消息格式
func normalizeMultimodalMessage(msg Message, model string) UpstreamMessage {
	// 检查是否为多模态消息
	if contentParts, ok := msg.Content.([]ContentPart); ok {
		// 处理多模态内容
		textContent := processMultimodalContent(contentParts, model)
		return UpstreamMessage{
			Role:             normalizeRole(msg.Role),
			Content:          textContent,
			ReasoningContent: msg.ReasoningContent,
		}
	} else {
		// 普通文本消息
		textContent := extractTextContent(msg.Content)
		return UpstreamMessage{
			Role:             normalizeRole(msg.Role),
			Content:          textContent,
			ReasoningContent: msg.ReasoningContent,
		}
	}
}

// normalizeMessage 规范化消息格式，将多模态内容转换为上游可接受的格式

// normalizeRole 规范化角色名称，处理OpenAI和Z.ai之间的差异
func normalizeRole(role string) string {
	switch role {
	case "developer":
		return "system" // developer角色映射为system
	default:
		return role
	}
}

// parseToolChoice 解析ToolChoice参数，支持多种格式
func parseToolChoice(toolChoice interface{}) *ToolChoice {
	if toolChoice == nil {
		return nil
	}

	// 检查是否是字符串格式 ("none", "auto", "required")
	if choiceStr, ok := toolChoice.(string); ok {
		return &ToolChoice{
			Type: choiceStr,
		}
	}

	// 检查是否是对象格式
	if choiceMap, ok := toolChoice.(map[string]interface{}); ok {
		toolChoiceObj := &ToolChoice{}

		if choiceType, exists := choiceMap["type"]; exists {
			if typeStr, ok := choiceType.(string); ok {
				toolChoiceObj.Type = typeStr
			}
		}

		if function, exists := choiceMap["function"]; exists {
			if funcMap, ok := function.(map[string]interface{}); ok {
				toolChoiceObj.Function = &ToolChoiceFunction{}
				if name, exists := funcMap["name"]; exists {
					if nameStr, ok := name.(string); ok {
						toolChoiceObj.Function.Name = nameStr
					}
				}
			}
		}

		return toolChoiceObj
	}

	return nil
}

// buildUpstreamParams 构建上游请求参数
func buildUpstreamParams(req OpenAIRequest) map[string]interface{} {
	params := make(map[string]interface{})

	// 传递标准OpenAI参数（使用指针类型安全检查）
	if req.Temperature != nil {
		params["temperature"] = *req.Temperature
	}
	if req.MaxTokens != nil {
		params["max_tokens"] = *req.MaxTokens
	}
	if req.TopP != nil {
		params["top_p"] = *req.TopP
	}
	if req.N != nil {
		params["n"] = *req.N
	}
	if req.Stop != nil {
		params["stop"] = req.Stop
	}
	if req.PresencePenalty != nil {
		params["presence_penalty"] = *req.PresencePenalty
	}
	if req.FrequencyPenalty != nil {
		params["frequency_penalty"] = *req.FrequencyPenalty
	}
	if len(req.LogitBias) > 0 {
		params["logit_bias"] = req.LogitBias
	}
	if req.User != "" {
		params["user"] = req.User
	}
	if req.ResponseFormat != nil {
		params["response_format"] = req.ResponseFormat
	}
	if req.Seed != nil {
		params["seed"] = *req.Seed
	}
	if req.LogProbs {
		params["logprobs"] = req.LogProbs
	}
	if req.TopLogProbs != nil {
		// 验证 top_logprobs 必须在 0-5 范围内
		topLogProbs := *req.TopLogProbs
		if topLogProbs >= 0 && topLogProbs <= 5 {
			params["top_logprobs"] = topLogProbs
		} else {
			debugLog("top_logprobs 必须在0-5范围内，收到值: %d", topLogProbs)
		}
	}
	if req.ParallelToolCalls != nil {
		params["parallel_tool_calls"] = *req.ParallelToolCalls
	}
	if req.ServiceTier != "" {
		params["service_tier"] = req.ServiceTier
	}
	if req.Store != nil {
		params["store"] = *req.Store
	}
	if len(req.Metadata) > 0 {
		params["metadata"] = req.Metadata
	}

	// 符合OpenAI标准的兼容性参数
	if req.MaxCompletionTokens != nil {
		params["max_completion_tokens"] = *req.MaxCompletionTokens
	}
	if req.TopK != nil {
		params["top_k"] = *req.TopK
	}
	if req.MinP != nil {
		params["min_p"] = *req.MinP
	}
	if req.RepetitionPenalty != nil {
		params["repetition_penalty"] = *req.RepetitionPenalty
	}
	if req.BestOf != nil {
		params["best_of"] = *req.BestOf
	}
	if req.Grammar != nil {
		params["grammar"] = req.Grammar
	}
	if req.GrammarType != "" {
		params["grammar_type"] = req.GrammarType
	}
	if req.MaxInputTokens != nil {
		params["max_input_tokens"] = *req.MaxInputTokens
	}
	if req.MinCompletionTokens != nil {
		params["min_completion_tokens"] = *req.MinCompletionTokens
	}

	return params
}

// maskJSONForLogging masks sensitive fields in a JSON string for logging purposes.
// 优化：使用 sonic 对象池进行解析
func maskJSONForLogging(jsonStr string) string {
	var data map[string]interface{}
	// 使用 sonic 进行解析
	if err := sonicDefault.UnmarshalFromString(jsonStr, &data); err != nil {
		// If parsing fails, just truncate the raw string if it's too long.
		if len(jsonStr) > 512 {
			return jsonStr[:512] + "...(truncated)"
		}
		return jsonStr
	}

	// Truncate long message content
	if messages, ok := data["messages"].([]interface{}); ok {
		for _, item := range messages {
			if msg, ok := item.(map[string]interface{}); ok {
				if content, ok := msg["content"].(string); ok {
					if len(content) > 100 {
						msg["content"] = content[:100] + "...(truncated)"
					}
				}
			}
		}
	}

	// 使用 sonic 进行序列化
	maskedBytes, err := sonicFast.Marshal(data)
	if err != nil {
		// Fallback if marshaling fails
		if len(jsonStr) > 512 {
			return jsonStr[:512] + "...(truncated)"
		}
		return jsonStr
	}
	return string(maskedBytes)
}

// debug日志函数
func debugLog(format string, args ...interface{}) {
	if appConfig != nil && appConfig.DebugMode {
		log.Printf("[DEBUG] "+format, args...)
	}
}

// transformThinking 转换思考内容
func transformThinking(s string) string {
	// 去 <summary>…</summary> - 使用预编译的正则表达式
	s = summaryRegex.ReplaceAllString(s, "")

	// 根据配置的模式选择合适的替换器和处理策略
	switch appConfig.ThinkTagsMode {
	case "think":
		// 替换 <details> 为 <think>
		s = detailsRegex.ReplaceAllString(s, "<think>")
		// 应用替换器（包括 </details> 到 </think> 的替换）
		s = thinkingReplacer.Replace(s)
	case "strip":
		s = detailsRegex.ReplaceAllString(s, "")
		s = thinkingStripReplacer.Replace(s)
	case "raw":
		// 只做基本清理，保留原始标签
		s = strings.NewReplacer(
			"</thinking>", "",
			"<Full>", "",
			"</Full>", "",
			"\n> ", "\n",
		).Replace(s)
	default:
		// 默认使用think模式
		s = detailsRegex.ReplaceAllString(s, "<think>")
		s = thinkingReplacer.Replace(s)
	}

	// 处理起始位置的前缀
	s = strings.TrimPrefix(s, "> ")
	result := strings.TrimSpace(s)

	// 仅在检测到标签不匹配时记录错误
	finalThinkOpen := strings.Count(result, "<think>")
	finalThinkClose := strings.Count(result, "</think>")
	if finalThinkOpen != finalThinkClose {
		debugLog("[TRANSFORM_ERROR] 标签不匹配: <think>=%d, </think>=%d", finalThinkOpen, finalThinkClose)
	}

	return result
}

// StatsCollector 异步统计数据收集器
type StatsCollector struct {
	requestChan chan StatsUpdate
	quit        chan struct{}
	wg          sync.WaitGroup
}

// StatsUpdate 统计更新数据
type StatsUpdate struct {
	startTime   time.Time
	path        string
	status      int
	tokens      int64
	model       string
	isStreaming bool
	duration    float64
	userAgent   string
	method      string
}

// NewStatsCollector 创建新的统计收集器
func NewStatsCollector(bufferSize int) *StatsCollector {
	sc := &StatsCollector{
		requestChan: make(chan StatsUpdate, bufferSize),
		quit:        make(chan struct{}),
	}
	sc.start()
	return sc
}

// start 启动统计收集器
func (sc *StatsCollector) start() {
	sc.wg.Add(1)
	go func() {
		defer sc.wg.Done()
		batchUpdates := make([]StatsUpdate, 0, 10)       // 批量处理，最多10个
		ticker := time.NewTicker(100 * time.Millisecond) // 每100ms处理一次
		defer ticker.Stop()

		for {
			select {
			case update := <-sc.requestChan:
				batchUpdates = append(batchUpdates, update)
				// 如果批量达到上限，立即处理
				if len(batchUpdates) >= 10 {
					sc.processBatch(batchUpdates)
					batchUpdates = batchUpdates[:0] // 清空但保留容量
				}
			case <-ticker.C:
				// 定期处理剩余的更新
				if len(batchUpdates) > 0 {
					sc.processBatch(batchUpdates)
					batchUpdates = batchUpdates[:0]
				}
			case <-sc.quit:
				// 处理剩余的更新然后退出
				if len(batchUpdates) > 0 {
					sc.processBatch(batchUpdates)
				}
				// 处理通道中剩余的所有更新
				for {
					select {
					case update := <-sc.requestChan:
						batchUpdates = append(batchUpdates, update)
					default:
						if len(batchUpdates) > 0 {
							sc.processBatch(batchUpdates)
						}
						return
					}
				}
			}
		}
	}()
}

// processBatch 批量处理统计更新
func (sc *StatsCollector) processBatch(updates []StatsUpdate) {
	if stats == nil {
		return
	}

	stats.mutex.Lock()
	defer stats.mutex.Unlock()

	for _, update := range updates {
		// 更新基本统计
		stats.TotalRequests++
		stats.LastRequestTime = time.Now()

		if update.status >= 200 && update.status < 300 {
			stats.SuccessfulRequests++
		} else {
			stats.FailedRequests++
		}

		// 更新端点统计
		switch update.path {
		case "/v1/chat/completions":
			stats.ApiCallsCount++
		case "/v1/models":
			stats.ModelsCallsCount++
		case "/":
			stats.HomePageViews++
		}

		// 更新token统计
		if update.tokens > 0 {
			stats.TotalTokensUsed += update.tokens
		}

		// 更新模型使用统计
		if update.model != "" {
			stats.ModelUsage[update.model]++
		}

		// 更新流式统计
		if update.isStreaming {
			stats.StreamingRequests++
		} else {
			stats.NonStreamingRequests++
		}

		// 更新响应时间统计
		if update.duration < stats.FastestResponse {
			stats.FastestResponse = update.duration
		}
		if update.duration > stats.SlowestResponse {
			stats.SlowestResponse = update.duration
		}

		// 更新平均响应时间
		totalDuration := stats.AverageResponseTime*float64(stats.TotalRequests-1) + update.duration
		stats.AverageResponseTime = totalDuration / float64(stats.TotalRequests)

		// 添加实时请求记录（限制数量）
		if len(liveRequests) >= 100 {
			// 移除最旧的请求（简单的滑动窗口）
			liveRequests = liveRequests[1:]
		}
		liveRequests = append(liveRequests, LiveRequest{
			ID:        uuid.New().String(),
			Timestamp: time.Now(),
			Method:    update.method,
			Path:      update.path,
			Status:    update.status,
			Duration:  update.duration,
			UserAgent: update.userAgent,
			Model:     update.model,
		})
	}
}

// Record 记录统计更新
func (sc *StatsCollector) Record(update StatsUpdate) {
	select {
	case sc.requestChan <- update:
		// 成功发送到通道
	default:
		// 通道已满，丢弃这次更新以避免阻塞
		debugLog("统计通道已满，丢弃统计更新")
	}
}

// Stop 停止统计收集器
func (sc *StatsCollector) Stop() {
	close(sc.quit)
	sc.wg.Wait()
	close(sc.requestChan)
}

var statsCollector *StatsCollector

// recordRequestStats 异步记录请求统计信息
func recordRequestStats(startTime time.Time, path string, status int, tokens int64, model string, isStreaming bool) {
	duration := float64(time.Since(startTime)) / float64(time.Millisecond)

	if statsCollector != nil {
		statsCollector.Record(StatsUpdate{
			startTime:   startTime,
			path:        path,
			status:      status,
			tokens:      tokens,
			model:       model,
			isStreaming: isStreaming,
			duration:    duration,
		})
	}
}

// addLiveRequest 异步添加实时请求记录
func addLiveRequest(method string, path string, status int, duration float64, userAgent string, model string) {
	if statsCollector != nil {
		statsCollector.Record(StatsUpdate{
			path:      path,
			status:    status,
			model:     model,
			duration:  duration,
			userAgent: userAgent,
			method:    method,
		})
	}
}

// StatsManager 统计数据管理器，提供线程安全的统计操作
type StatsManager struct {
	stats *RequestStats
	mutex sync.RWMutex
}

// NewStatsManager 创建新的统计管理器
func NewStatsManager() *StatsManager {
	return &StatsManager{
		stats: &RequestStats{
			StartTime:       time.Now(),
			ModelUsage:      make(map[string]int64),
			FastestResponse: float64(time.Hour) / float64(time.Millisecond), // Initialize with a large value
			SlowestResponse: 0,
		},
	}
}

// GetTopModels 安全地获取热门模型，避免锁争用
func (sm *StatsManager) GetTopModels() []struct {
	Model string `json:"model"`
	Count int64  `json:"count"`
} {
	sm.mutex.RLock()
	defer sm.mutex.RUnlock()

	if sm.stats == nil || len(sm.stats.ModelUsage) == 0 {
		return []struct {
			Model string `json:"model"`
			Count int64  `json:"count"`
		}{}
	}

	// 快速复制数据以减少锁持有时间
	type modelCount struct {
		model string
		count int64
	}
	pairs := make([]modelCount, 0, len(sm.stats.ModelUsage))
	for model, count := range sm.stats.ModelUsage {
		pairs = append(pairs, modelCount{model, count})
	}

	// 释放读锁后进行排序
	sm.mutex.RUnlock()

	// Sort by count (descending)
	sort.Slice(pairs, func(i, j int) bool {
		return pairs[i].count > pairs[j].count
	})

	// Take top 3
	maxLen := min(3, len(pairs))
	result := make([]struct {
		Model string `json:"model"`
		Count int64  `json:"count"`
	}, maxLen)

	for i := 0; i < maxLen; i++ {
		result[i] = struct {
			Model string `json:"model"`
			Count int64  `json:"count"`
		}{Model: pairs[i].model, Count: pairs[i].count}
	}

	return result
}

// GetStats 安全地获取统计数据快照
func (sm *StatsManager) GetStats() *RequestStats {
	sm.mutex.RLock()
	defer sm.mutex.RUnlock()

	if sm.stats == nil {
		return &RequestStats{
			StartTime:  time.Now(),
			ModelUsage: make(map[string]int64),
		}
	}

	// 创建数据副本以避免外部修改
	statsCopy := &RequestStats{
		TotalRequests:        sm.stats.TotalRequests,
		SuccessfulRequests:   sm.stats.SuccessfulRequests,
		FailedRequests:       sm.stats.FailedRequests,
		LastRequestTime:      sm.stats.LastRequestTime,
		AverageResponseTime:  sm.stats.AverageResponseTime,
		HomePageViews:        sm.stats.HomePageViews,
		ApiCallsCount:        sm.stats.ApiCallsCount,
		ModelsCallsCount:     sm.stats.ModelsCallsCount,
		StreamingRequests:    sm.stats.StreamingRequests,
		NonStreamingRequests: sm.stats.NonStreamingRequests,
		TotalTokensUsed:      sm.stats.TotalTokensUsed,
		StartTime:            sm.stats.StartTime,
		FastestResponse:      sm.stats.FastestResponse,
		SlowestResponse:      sm.stats.SlowestResponse,
		ModelUsage:           make(map[string]int64),
	}

	// 复制 ModelUsage map
	for k, v := range sm.stats.ModelUsage {
		statsCopy.ModelUsage[k] = v
	}

	return statsCopy
}

// getClientIP 获取客户端IP

// validateAndSanitizeInput 验证和清理输入数据，支持基于模型的动态验证
func validateAndSanitizeInput(req *OpenAIRequest) error {
	// 验证消息数量
	if len(req.Messages) == 0 {
		return fmt.Errorf("messages不能为空")
	}
	if len(req.Messages) > 100 { // 限制消息数量
		return fmt.Errorf("消息数量过多，最多支持100条消息")
	}

	// 验证和清理每条消息
	totalContentLength := 0
	for i := range req.Messages {
		msg := &req.Messages[i]

		// 验证角色
		if msg.Role == "" {
			return fmt.Errorf("消息角色不能为空")
		}

		// 验证角色值
		validRoles := map[string]bool{
			"system": true, "user": true, "assistant": true, "developer": true, "tool": true,
		}
		if !validRoles[msg.Role] {
			return fmt.Errorf("无效的消息角色: %s", msg.Role)
		}

		// 验证内容长度
		contentText := extractTextContent(msg.Content)
		if len(contentText) > 500000 { // 单条消息最大500KB
			return fmt.Errorf("单条消息内容过长，最大支持500KB")
		}
		totalContentLength += len(contentText)

		// 验证工具调用
		if len(msg.ToolCalls) > 10 { // 限制工具调用数量
			return fmt.Errorf("单条消息工具调用数量过多，最多支持10个")
		}
	}

	// 验证数值参数
	if req.Temperature != nil {
		temp := *req.Temperature
		if temp < 0 || temp > 2.0 {
			return fmt.Errorf("temperature必须在0-2.0之间")
		}
	}

	if req.TopP != nil {
		topP := *req.TopP
		if topP < 0 || topP > 1.0 {
			return fmt.Errorf("top_p必须在0-1.0之间")
		}
	}

	if req.MaxTokens != nil {
		maxTokens := *req.MaxTokens
		if maxTokens <= 0 || maxTokens > 240000 {
			return fmt.Errorf("max_tokens必须在1-240000之间")
		}
	}

	if req.TopLogProbs != nil {
		topLogProbs := *req.TopLogProbs
		if topLogProbs < 0 || topLogProbs > 5 {
			return fmt.Errorf("top_logprobs必须在0-5之间")
		}
	}

	// 验证工具定义
	if len(req.Tools) > 50 { // 限制工具数量
		return fmt.Errorf("工具数量过多，最多支持50个工具")
	}

	for i, tool := range req.Tools {
		if tool.Type == "" {
			return fmt.Errorf("工具[%d]类型不能为空", i)
		}
		if tool.Function.Name == "" {
			return fmt.Errorf("工具[%d]名称不能为空", i)
		}
		if len(tool.Function.Name) > 64 {
			return fmt.Errorf("工具[%d]名称过长，最多64字符", i)
		}
	}

	return nil
}

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

// 获取匿名token（每次对话使用不同token，避免共享记忆）
// GetToken 从缓存或新获取Token，使用 singleflight 防止缓存击穿
func (tc *TokenCache) GetToken() (string, error) {
	// 先读锁检查缓存
	tc.mutex.RLock()
	if tc.token != "" && time.Now().Before(tc.expiresAt) {
		token := tc.token
		tc.mutex.RUnlock()
		debugLog("使用缓存的匿名token")
		tokenCacheHits.Add(1)
		return token, nil
	}
	tc.mutex.RUnlock()

	// 使用 singleflight 确保只有一个 goroutine 获取 token
	result, err, _ := tc.sf.Do("get_token", func() (interface{}, error) {
		// 再次检查缓存，可能在等待期间其他 goroutine 已经获取了
		tc.mutex.RLock()
		if tc.token != "" && time.Now().Before(tc.expiresAt) {
			token := tc.token
			tc.mutex.RUnlock()
			debugLog("在 singleflight 中使用缓存的匿名token")
			return token, nil
		}
		tc.mutex.RUnlock()

		// 获取新 token
		newToken, fetchErr := getAnonymousTokenDirect()
		if fetchErr != nil {
			debugLog("获取新的匿名token失败: %v", fetchErr)
			tokenCacheMisses.Add(1)
			return "", fetchErr
		}

		// 更新缓存
		tc.mutex.Lock()
		tc.token = newToken
		tc.expiresAt = time.Now().Add(5 * time.Minute) // 5分钟缓存
		tc.mutex.Unlock()

		debugLog("获取新的匿名token成功，缓存5分钟")
		tokenCacheMisses.Add(1)
		return newToken, nil
	})

	if err != nil {
		return "", err
	}

	return result.(string), nil
}

// InvalidateToken 立即将当前token标记为失效，强制获取新token
func (tc *TokenCache) InvalidateToken() {
	tc.mutex.Lock()
	defer tc.mutex.Unlock()
	tc.token = ""
	tc.expiresAt = time.Now() // 设置为已过期
	debugLog("匿名token已标记为失效，下次请求将获取新token")
}

// getAnonymousToken 兼容性方法，使用缓存
func getAnonymousToken() (string, error) {
	if tokenCache == nil {
		return "", fmt.Errorf("token cache not initialized")
	}
	return tokenCache.GetToken()
}

// generateBrowserHeaders generates dynamic and consistent browser headers for a session.
// 修复并发安全问题：每次调用都创建新的map，避免并发读写冲突
func generateBrowserHeaders(sessionID, chatID, authToken, scenario string) map[string]string {
	fp, ok := config.GetFingerprintForSession(sessionID)

	// 预分配足够的空间以提高性能
	dynamicHeaders := make(map[string]string, 20)

	if !ok {
		debugLog("未能从 fingerprints.json 加载指纹，回退到默认硬编码指纹")
		// Fallback to old logic if fingerprint system fails
		// Create a local random source for thread safety
		localRand := rand.New(rand.NewSource(time.Now().UnixNano()))

		// Generate more diverse browser fingerprints
		chromeVersions := []int{128, 129, 130, 131, 132}
		chromeVersion := chromeVersions[localRand.Intn(len(chromeVersions))]
		edgeVersion := chromeVersion

		// Expanded list of diverse User-Agent strings
		userAgents := []string{
			fmt.Sprintf("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/%d.0.0.0 Safari/537.36 Edg/%d.0.0.0", chromeVersion, edgeVersion),
			fmt.Sprintf("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/%d.0.0.0 Safari/537.36", chromeVersion),
			fmt.Sprintf("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/%d.0.0.0 Safari/537.36 Edg/%d.0.0.0", chromeVersion, edgeVersion),
			fmt.Sprintf("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/%d.0.0.0 Safari/537.36", chromeVersion),
			fmt.Sprintf("Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/%d.0.0.0 Safari/537.36", chromeVersion),
			fmt.Sprintf("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/%d.0.0.0 Safari/537.36 OPR/%d.0.0.0", chromeVersion, chromeVersion-10),
			fmt.Sprintf("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/%d.0 Safari/605.1.15", chromeVersion-80),
		}

		// Different sec-ch-ua values based on the chosen browser
		secChUas := []string{
			fmt.Sprintf(`"Google Chrome";v="%d", "Chromium";v="%d", "Not=A?Brand";v="%d"`, chromeVersion, chromeVersion, chromeVersion-20),
			fmt.Sprintf(`"Chromium";v="%d", "Not(A:Brand";v="24", "Microsoft Edge";v="%d"`, chromeVersion, edgeVersion),
			fmt.Sprintf(`"Not/A)Brand";v="%d", "Google Chrome";v="%d", "Chromium";v="%d"`, chromeVersion-20, chromeVersion, chromeVersion),
		}

		platforms := []string{"\"Windows\"", "\"macOS\"", "\"Linux\""}

		// Randomly select from the expanded lists
		selectedUA := userAgents[localRand.Intn(len(userAgents))]
		selectedSecChUa := secChUas[localRand.Intn(len(secChUas))]
		selectedPlatform := platforms[localRand.Intn(len(platforms))]

		dynamicHeaders["User-Agent"] = selectedUA
		dynamicHeaders["sec-ch-ua"] = selectedSecChUa
		dynamicHeaders["sec-ch-ua-mobile"] = DefaultSecChUaMob
		dynamicHeaders["sec-ch-ua-platform"] = selectedPlatform
		dynamicHeaders["X-FE-Version"] = DefaultXFeVersion
	} else {
		debugLog("使用会话指纹 (ID: %s) for chatID: %s", fp.ID, chatID)
		var sourceHeaders map[string]string
		switch scenario {
		case "xhr":
			sourceHeaders = fp.Headers.XHR
		case "js":
			sourceHeaders = fp.Headers.JS
		default: // Default to "html"
			sourceHeaders = fp.Headers.HTML
		}

		// 安全地复制headers，避免并发访问原始map
		for k, v := range sourceHeaders {
			dynamicHeaders[k] = v
		}
		dynamicHeaders["User-Agent"] = fp.UserAgent
	}

	// Set common headers - 所有写入操作都在同一个新创建的map上，天然并发安全
	dynamicHeaders["Accept"] = "*/*"
	if authToken != "" {
		dynamicHeaders["Authorization"] = "Bearer " + authToken
	}
	dynamicHeaders["Accept-Language"] = "zh-CN,zh;q=0.9,en;q=0.8,en-GB;q=0.7,en-US;q=0.6"
	dynamicHeaders["Accept-Encoding"] = "gzip, br" // 只声明实际支持的编码
	dynamicHeaders["sec-fetch-dest"] = "empty"
	dynamicHeaders["sec-fetch-mode"] = "cors"
	dynamicHeaders["sec-fetch-site"] = "same-origin"
	dynamicHeaders["Origin"] = OriginBase
	dynamicHeaders["Referer"] = OriginBase + "/c/" + chatID
	dynamicHeaders["Priority"] = "u=1, i"

	return dynamicHeaders
}

// getAnonymousTokenDirect 直接获取匿名token（原始方法，不使用缓存）
// 优化：使用 sonic 解析响应
func getAnonymousTokenDirect() (string, error) {
	// 如果禁用匿名token，直接返回错误
	if !appConfig.AnonTokenEnabled {
		return "", fmt.Errorf("anonymous token disabled")
	}

	req, err := http.NewRequest("GET", OriginBase+"/api/v1/auths/", nil)
	if err != nil {
		return "", err
	}
	// 使用动态指纹，但不设置Accept-Encoding让Transport自动处理
	headers := generateBrowserHeaders("", "", "", "html")
	for key, value := range headers {
		// 跳过Accept-Encoding以避免手动解压问题
		if key == "Accept-Encoding" {
			continue
		}
		req.Header.Set(key, value)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != StatusOK {
		return "", fmt.Errorf("anon token status=%d", resp.StatusCode)
	}
	var body struct {
		Token string `json:"token"`
	}
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	// 使用 sonic 进行解析
	if err := sonicDefault.Unmarshal(respBody, &body); err != nil {
		return "", err
	}
	if body.Token == "" {
		return "", fmt.Errorf("anon token empty")
	}
	return body.Token, nil
}

// 从文件读取仪表板 HTML
func loadDashboardHTML() (string, error) {
	content, err := os.ReadFile("assets/dashboard.html")
	if err != nil {
		return "", err
	}
	return string(content), nil
}

var dashboardHTML string

// initSonicPretouch 初始化 sonic JIT 预热 - 扩展预热范围
func initSonicPretouch() {
	debugLog("开始扩展的 sonic JIT 预热...")

	// 核心请求/响应结构
	coreTypes := []reflect.Type{
		reflect.TypeOf(OpenAIRequest{}),
		reflect.TypeOf(OpenAIResponse{}),
		reflect.TypeOf(UpstreamRequest{}),
		reflect.TypeOf(UpstreamData{}),
		reflect.TypeOf(Choice{}),
		reflect.TypeOf(Message{}),
		reflect.TypeOf(UpstreamMessage{}),
		reflect.TypeOf(Delta{}),
		reflect.TypeOf(Usage{}),
	}

	// 工具相关结构
	toolTypes := []reflect.Type{
		reflect.TypeOf(ToolCall{}),
		reflect.TypeOf(ToolCallFunction{}),
		reflect.TypeOf(Tool{}),
		reflect.TypeOf(ToolFunction{}),
		reflect.TypeOf(ToolChoice{}),
		reflect.TypeOf(ToolChoiceFunction{}),
	}

	// 多模态内容结构
	contentTypes := []reflect.Type{
		reflect.TypeOf(ContentPart{}),
		reflect.TypeOf(ImageURL{}),
		reflect.TypeOf(VideoURL{}),
		reflect.TypeOf(DocumentURL{}),
		reflect.TypeOf(AudioURL{}),
		reflect.TypeOf([]ContentPart{}),
	}

	// 错误和模型结构
	utilTypes := []reflect.Type{
		reflect.TypeOf(UpstreamError{}),
		reflect.TypeOf(ErrorResponse{}),
		reflect.TypeOf(ErrorDetail{}),
		reflect.TypeOf(ModelsResponse{}),
		reflect.TypeOf(Model{}),
		reflect.TypeOf([]Model{}),
		reflect.TypeOf(map[string]interface{}{}),
		reflect.TypeOf([]interface{}{}),
	}

	// 统计结构
	statsTypes := []reflect.Type{
		reflect.TypeOf(RequestStats{}),
		reflect.TypeOf(LiveRequest{}),
		reflect.TypeOf([]LiveRequest{}),
		reflect.TypeOf(StatsUpdate{}),
	}

	// 执行预热
	allTypes := append(append(append(append(coreTypes, toolTypes...), contentTypes...), utilTypes...), statsTypes...)

	successCount := 0
	failCount := 0

	for _, t := range allTypes {
		if err := sonic.Pretouch(t); err != nil {
			debugLog("预热 %v 失败: %v", t, err)
			failCount++
		} else {
			successCount++
		}
	}

	debugLog("sonic JIT 预热完成: 成功 %d 个类型，失败 %d 个类型", successCount, failCount)
}

// main is the entry point of the application
func main() {
	// 执行扩展的 JIT 预热
	initSonicPretouch()

	// 加载仪表板HTML
	var err error
	dashboardHTML, err = loadDashboardHTML()
	if err != nil {
		log.Printf("警告: 无法加载仪表板文件 'dashboard.html': %v. 仪表板功能可能不可用。", err)
		// 如果无法加载dashboard.html，使用一个简单的默认HTML
		dashboardHTML = `<html><body><h1>Dashboard Unavailable</h1><p>Dashboard HTML file not found.</p></body></html>`
	}

	// 加载模型配置
	if err := config.LoadModels("assets/models.json"); err != nil {
		log.Fatalf("错误: 无法加载模型配置文件 'assets/models.json': %v", err)
	}

	// 加载浏览器指纹配置
	if err := config.LoadFingerprints("assets/fingerprints.json"); err != nil {
		log.Printf("警告: 无法加载浏览器指纹文件 'assets/fingerprints.json': %v. 将使用默认指纹。", err)
	}

	// 加载和验证配置
	appConfig, err = loadConfig()
	if err != nil {
		log.Fatalf("配置加载失败: %v", err)
	}

	// 初始化Token缓存
	tokenCache = &TokenCache{}

	// 初始化统计信息
	stats = &RequestStats{
		StartTime:       time.Now(),
		ModelUsage:      make(map[string]int64),
		FastestResponse: float64(time.Hour) / float64(time.Millisecond), // Initialize with a large value
		SlowestResponse: 0,
	}

	// 初始化异步统计收集器
	statsCollector = NewStatsCollector(1000) // 1000个缓冲区大小

	// 初始化并发控制器
	concurrencyLimiter = semaphore.NewWeighted(int64(appConfig.MaxConcurrentRequests))

	// 设置 Gin 路由
	log.Println("初始化 Gin 路由...")
	log.Println("使用 Gin 原生处理器")

	router := setupRouter()

	// 初始化全局错误处理器

	log.Printf("OpenAI兼容API服务器启动在端口%s", appConfig.Port)
	log.Printf("模型: %s", DefaultModelName)
	log.Printf("上游: %s", appConfig.UpstreamUrl)
	log.Printf("Debug模式: %v", appConfig.DebugMode)
	log.Printf("匿名Token: %v", appConfig.AnonTokenEnabled)
	log.Printf("思考标签模式: %s", appConfig.ThinkTagsMode)
	log.Printf("并发限制: %d", appConfig.MaxConcurrentRequests)
	log.Printf("健康检查端点: http://localhost%s/health", appConfig.Port)
	log.Printf("Dashboard端点: http://localhost%s/dashboard", appConfig.Port)

	// 使用 Gin 的底层 http.Server 配置
	server := &http.Server{
		Addr:              appConfig.Port,
		Handler:           router,
		ReadTimeout:       300 * time.Second,
		WriteTimeout:      300 * time.Second, // 增加写超时，适应长流式响应
		IdleTimeout:       320 * time.Second, // IdleTimeout应该比WriteTimeout稍长
		ReadHeaderTimeout: 10 * time.Second,  // 添加请求头读取超时
		MaxHeaderBytes:    1 << 20,           // 1MB请求头限制
	}

	// 优雅关闭处理
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigChan
		log.Println("收到关闭信号，开始优雅关闭服务器...")

		// 停止统计收集器
		if statsCollector != nil {
			statsCollector.Stop()
		}

		// 设置关闭超时
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer shutdownCancel()

		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Printf("服务器关闭时出现错误: %v", err)
		} else {
			log.Println("服务器已优雅关闭")
		}
	}()

	log.Fatal(server.ListenAndServe())
}

// handleHealth 健康检查端点
// 优化：使用 sonic 编码健康检查响应

// getModelsList 获取模型列表，支持优雅降级
func getModelsList() ModelsResponse {
	// 从配置动态生成模型列表
	loadedModels := config.GetAllModels()
	apiModels := make([]Model, len(loadedModels))
	now := time.Now().Unix()

	for i, m := range loadedModels {
		apiModels[i] = Model{
			ID:      m.ID,
			Object:  "model",
			Created: now,
			OwnedBy: "z.ai",
		}
	}

	return ModelsResponse{
		Object: "list",
		Data:   apiModels,
	}
}

// callUpstreamWithHeaders 调用上游API
// 优化：使用 sonic 对象池进行序列化
func callUpstreamWithHeaders(upstreamReq UpstreamRequest, refererChatID string, authToken string, sessionID string) (*http.Response, context.CancelFunc, error) {
	// 创建带超时的上下文 - 根据请求类型动态调整超时时间
	var timeout time.Duration
	if upstreamReq.Stream {
		timeout = 300 * time.Second // 流式请求需要更长时间
	} else {
		timeout = 120 * time.Second // 非流式请求使用较短超时
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)

	// 使用对象池减少内存分配
	buf := bytesBufferPool.Get().(*bytes.Buffer)
	defer func() {
		buf.Reset()
		bytesBufferPool.Put(buf)
	}()

	// 使用 sonic 快速配置进行序列化（内部通信）
	data, err := sonicFast.Marshal(upstreamReq)
	if err != nil {
		debugLog("上游请求序列化失败: %v", err)
		cancel() // 手动取消上下文
		return nil, nil, err
	}
	buf.Write(data)

	debugLog("调用上游API: %s (超时: %v)", appConfig.UpstreamUrl, timeout)
	debugLog("上游请求体: %s", maskJSONForLogging(string(data)))

	// 生成签名所需的参数
	requestID := uuid.New().String()
	timestamp := time.Now().UnixMilli()
	userContent := extractLastUserContent(upstreamReq)

	// 从 authToken 中解析 user_id
	var userID string
	if jwtPayload, err := signature.DecodeJWT(authToken); err == nil {
		userID = jwtPayload.ID
		debugLog("从 JWT token 中成功解析 user_id: %s", userID)
	} else {
		// Fallback logic matching Python's abs(hash(token)) % 1000000
		hashVal := hashString(authToken)
		userID = fmt.Sprintf("guest-user-%d", hashVal%1000000)
		debugLog("解析 JWT token 失败: %v, 使用回退 user_id: %s", err, userID)
	}

	// 生成签名
	signatureResult, err := signature.GenerateZsSignature(userID, requestID, timestamp, userContent)
	if err != nil {
		debugLog("生成签名失败: %v", err)
		cancel() // 手动取消上下文
		return nil, nil, err
	}

	// 构建 current_url 和 pathname
	var currentURL, pathname string
	if refererChatID != "" {
		currentURL = fmt.Sprintf("https://chat.z.ai/c/%s", refererChatID)
		pathname = fmt.Sprintf("/c/%s", refererChatID)
	} else {
		currentURL = "https://chat.z.ai/"
		pathname = "/"
	}

	// 使用 net/url 包构建完整的查询参数
	parsedURL, err := url.Parse(appConfig.UpstreamUrl)
	if err != nil {
		debugLog("解析上游URL失败: %v", err)
		cancel() // 手动取消上下文
		return nil, nil, err
	}

	// 构建查询参数
	query := parsedURL.Query()
	query.Set("signature_timestamp", fmt.Sprintf("%d", timestamp))
	query.Set("requestId", requestID)
	query.Set("timestamp", fmt.Sprintf("%d", timestamp)) // 与 signature_timestamp 值相同
	query.Set("user_id", userID)
	query.Set("token", authToken)
	query.Set("current_url", currentURL) // net/url 会自动进行 URL 编码
	query.Set("pathname", pathname)

	parsedURL.RawQuery = query.Encode()
	upstreamURL := parsedURL.String()

	debugLog("构建的完整URL: %s", upstreamURL)
	debugLog("查询参数: user_id=%s, current_url=%s, pathname=%s", userID, currentURL, pathname)

	req, err := http.NewRequestWithContext(ctx, "POST", upstreamURL, bytes.NewReader(buf.Bytes()))
	if err != nil {
		debugLog("创建HTTP请求失败: %v", err)
		cancel() // 手动取消上下文
		return nil, nil, err
	}

	// 使用动态指纹
	headers := generateBrowserHeaders(sessionID, refererChatID, authToken, "xhr")
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	// 注入签名头
	req.Header.Set("X-Signature", signatureResult.Signature)

	// Add additional headers for SSE compatibility
	req.Header.Set("Content-Type", "application/json")
	if upstreamReq.Stream {
		req.Header.Set("Accept", "text/event-stream")
		req.Header.Set("Cache-Control", "no-cache")
	} else {
		req.Header.Set("Accept", "application/json")
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		debugLog("上游请求失败: %v", err)
		cancel() // 手动取消上下文
		return nil, nil, err
	}

	debugLog("上游响应状态: %d %s", resp.StatusCode, resp.Status)
	debugLog("响应头信息: Content-Encoding=%s, Content-Type=%s",
		resp.Header.Get("Content-Encoding"), resp.Header.Get("Content-Type"))

	// 检查响应是否被压缩，并支持多种编码(e.g., "gzip, br")
	contentEncodingHeader := resp.Header.Get("Content-Encoding")
	var selectedEncoding string
	for _, enc := range strings.Split(contentEncodingHeader, ",") {
		trimmedEnc := strings.TrimSpace(enc)
		if trimmedEnc == "br" {
			selectedEncoding = "br"
			break // 优先选择 br
		}
		if trimmedEnc == "gzip" {
			selectedEncoding = "gzip"
		}
	}
	debugLog("检测到Content-Encoding: '%s', 选择的解压方式: '%s'", contentEncodingHeader, selectedEncoding)

	switch selectedEncoding {
	case "gzip":
		debugLog("检测到gzip压缩响应，进行解压缩处理")
		// 创建一个解压缩的读取器
		gzipReader, err := gzip.NewReader(resp.Body)
		if err != nil {
			debugLog("创建gzip读取器失败: %v", err)
			resp.Body.Close()
			cancel()
			return nil, nil, err
		}

		// 创建一个新的响应体，使用解压缩的读取器
		resp.Body = &gzipReadCloser{
			reader: gzipReader,
			source: resp.Body,
		}
		debugLog("gzip解压缩处理完成")
	case "br":
		debugLog("检测到Brotli压缩响应，进行解压缩处理")
		// 创建一个Brotli解压缩的读取器
		brotliReader := brotli.NewReader(resp.Body)

		// 创建一个新的响应体，使用解压缩的读取器
		resp.Body = &brotliReadCloser{
			reader: brotliReader,
			source: resp.Body,
		}
		debugLog("Brotli解压缩处理完成")
	default:
		debugLog("响应未使用已知压缩格式（%s），直接处理", contentEncodingHeader)
	}

	return resp, cancel, nil
}

// isRetryableError 判断错误是否可重试
// 包括特殊的400错误（如"系统繁忙"）、超时错误、网络错误等
func isRetryableError(err error, statusCode int, responseBody []byte) bool {
	// 检查网络和超时错误
	if err != nil {
		// 检查context超时错误
		if errors.Is(err, context.DeadlineExceeded) {
			debugLog("检测到context超时错误，可重试")
			return true
		}

		// 检查网络错误
		var netErr net.Error
		if errors.As(err, &netErr) {
			if netErr.Timeout() {
				debugLog("检测到网络超时错误，可重试")
				return true
			}
			// 临时网络错误
			if netErr.Temporary() {
				debugLog("检测到临时网络错误，可重试")
				return true
			}
		}

		// 检查EOF错误（连接意外关闭）
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			debugLog("检测到EOF错误，可重试")
			return true
		}

		// 检查连接重置和拒绝错误
		errStr := err.Error()
		if strings.Contains(errStr, "connection reset") ||
			strings.Contains(errStr, "connection refused") ||
			strings.Contains(errStr, "broken pipe") {
			debugLog("检测到连接错误: %s，可重试", errStr)
			return true
		}
	}

	// 检查HTTP状态码
	switch statusCode {
	case StatusUnauthorized: // 401
		debugLog("401错误，需要重新认证，可重试")
		return true
	case StatusTooManyRequests: // 429
		debugLog("429限流错误，可重试")
		return true
	case StatusBadGateway, StatusServiceUnavailable, StatusGatewayTimeout: // 502, 503, 504
		debugLog("网关错误 %d，可重试", statusCode)
		return true
	case StatusInternalServerError: // 500
		debugLog("500服务器内部错误，可重试")
		return true
	case StatusRequestTimeout: // 408
		debugLog("408请求超时，可重试")
		return true
	case StatusBadRequest: // 400
		// 检查特殊的400错误情况
		if len(responseBody) > 0 {
			bodyStr := string(responseBody)
			// 检查"系统繁忙"等可重试的400错误
			if strings.Contains(bodyStr, "系统繁忙") ||
				strings.Contains(bodyStr, "system busy") ||
				strings.Contains(bodyStr, "rate limit") ||
				strings.Contains(bodyStr, "too many requests") ||
				strings.Contains(bodyStr, "temporarily unavailable") {
				debugLog("检测到特殊的400错误（%s），可重试", bodyStr)
				return true
			}
		}
	}

	return false
}

// calculateBackoffDelay 计算指数退避延迟时间（带抖动）
// attempt: 当前重试次数（从0开始）
// baseDelay: 基础延迟时间
// maxDelay: 最大延迟时间
func calculateBackoffDelay(attempt int, baseDelay time.Duration, maxDelay time.Duration) time.Duration {
	// 指数退避：baseDelay * 2^attempt
	delay := baseDelay * time.Duration(1<<uint(attempt))

	// 添加随机抖动（±25%），在限制最大延迟之前计算
	jitter := time.Duration(rand.Float64() * 0.5 * float64(delay))
	if rand.Intn(2) == 0 {
		delay = delay + jitter
	} else {
		delay = delay - jitter
		if delay < baseDelay {
			delay = baseDelay
		}
	}

	// 最后再限制最大延迟，确保不会超过maxDelay
	if delay > maxDelay {
		delay = maxDelay
	}

	debugLog("计算退避延迟：尝试 %d，基础延迟 %v，最终延迟 %v", attempt, baseDelay, delay)
	return delay
}

// callUpstreamWithRetry 调用上游API并处理重试，改进资源管理和超时控制
func callUpstreamWithRetry(upstreamReq UpstreamRequest, chatID string, authToken string, sessionID string) (*http.Response, context.CancelFunc, error) {
	var lastErr error
	maxRetries := 5 // 增加到5次重试
	baseDelay := 100 * time.Millisecond
	maxDelay := 10 * time.Second

	for attempt := 0; attempt < maxRetries; attempt++ {
		debugLog("开始第 %d/%d 次尝试调用上游API", attempt+1, maxRetries)

		// 每次重试都创建新的context，避免context污染
		resp, cancel, err := callUpstreamWithHeaders(upstreamReq, chatID, authToken, sessionID)

		// 检查是否需要重试（包括网络错误）
		if err != nil {
			debugLog("上游调用失败 (尝试 %d/%d): %v", attempt+1, maxRetries, err)
			if cancel != nil {
				cancel() // 立即取消context以释放资源
			}

			// 判断是否为可重试的错误
			if isRetryableError(err, 0, nil) {
				lastErr = err
				if attempt < maxRetries-1 {
					delay := calculateBackoffDelay(attempt, baseDelay, maxDelay)
					debugLog("网络错误，等待 %v 后重试", delay)
					time.Sleep(delay)

					// 如果是超时或认证错误，可能需要刷新token
					if strings.Contains(err.Error(), "timeout") || strings.Contains(err.Error(), "deadline") {
						debugLog("检测到超时错误，检查是否需要刷新token")
						if appConfig.AnonTokenEnabled {
							if newToken, tokenErr := getAnonymousTokenDirect(); tokenErr == nil {
								authToken = newToken
								debugLog("成功刷新匿名token")
								// 重新生成签名将在下次调用callUpstreamWithHeaders时自动完成
							}
						}
					}
					continue
				}
			} else {
				// 不可重试的错误，直接返回
				debugLog("不可重试的错误，停止重试: %v", err)
				return nil, nil, err
			}
		}

		// 如果请求成功，检查响应状态码
		if resp != nil {
			// 读取一些响应体用于错误分析（如果需要）
			var bodyBytes []byte
			if resp.StatusCode != StatusOK {
				// 尝试读取响应体用于错误分析
				bodyBytes, _ = io.ReadAll(io.LimitReader(resp.Body, 1024)) // 最多读1KB用于分析
				// 重新包装响应体，以便后续处理
				resp.Body = io.NopCloser(io.MultiReader(bytes.NewReader(bodyBytes), resp.Body))
			}

			// 检查状态码
			if resp.StatusCode == StatusOK {
				debugLog("上游调用成功 (尝试 %d/%d): %d", attempt+1, maxRetries, resp.StatusCode)
				return resp, cancel, nil // 成功，直接返回
			}

			// 检查是否为可重试的HTTP错误
			if isRetryableError(nil, resp.StatusCode, bodyBytes) {
				debugLog("收到可重试的HTTP状态码 %d (尝试 %d/%d)", resp.StatusCode, attempt+1, maxRetries)

				// 特殊处理401错误
				if resp.StatusCode == StatusUnauthorized {
					debugLog("收到401错误，尝试刷新token和重新生成签名")
					// 标记token为失效
					if tokenCache != nil {
						tokenCache.InvalidateToken()
					}
					// 如果启用了匿名token，尝试获取新的
					if appConfig.AnonTokenEnabled {
						if newToken, tokenErr := getAnonymousTokenDirect(); tokenErr == nil {
							authToken = newToken
							debugLog("成功获取新的匿名token，下次重试将使用新token和新签名")
							// 注意：签名会在下次调用callUpstreamWithHeaders时自动重新生成
						} else {
							debugLog("刷新匿名token失败: %v", tokenErr)
						}
					}
				}

				// 关闭当前响应
				cleanupResponse(resp, cancel)

				// 如果不是最后一次尝试，等待后重试
				if attempt < maxRetries-1 {
					// 对于429限流，使用更长的基础延迟
					if resp.StatusCode == StatusTooManyRequests {
						delay := calculateBackoffDelay(attempt, 1*time.Second, 180*time.Second)
						debugLog("限流错误，等待 %v 后重试", delay)
						time.Sleep(delay)
					} else {
						delay := calculateBackoffDelay(attempt, baseDelay, maxDelay)
						debugLog("等待 %v 后重试", delay)
						time.Sleep(delay)
					}
					continue
				}
			} else {
				// 不可重试的状态码，直接返回
				debugLog("收到不可重试的状态码 %d，停止重试", resp.StatusCode)
				return resp, cancel, nil
			}
		}

		// 如果是最后一次尝试，返回错误
		if attempt == maxRetries-1 {
			if resp != nil {
				lastErr = fmt.Errorf("上游API在 %d 次尝试后仍然失败，最后状态码: %d", maxRetries, resp.StatusCode)
			} else if lastErr != nil {
				lastErr = fmt.Errorf("上游API在 %d 次尝试后仍然失败: %w", maxRetries, lastErr)
			} else {
				lastErr = fmt.Errorf("上游API在 %d 次尝试后仍然失败", maxRetries)
			}
			return nil, nil, lastErr
		}
	}

	return nil, nil, fmt.Errorf("上游API在 %d 次尝试后仍然失败", maxRetries)
}

// cleanupResponse 清理HTTP响应和相关资源，改进连接复用
func cleanupResponse(resp *http.Response, cancel context.CancelFunc) {
	if resp.Body != nil {
		// 尝试读取并丢弃剩余数据，以便连接可以被重用
		// 增加读取量以更好地清空连接，提高连接复用效率
		discarded, err := io.Copy(io.Discard, io.LimitReader(resp.Body, 8192)) // 最多读取8KB
		if err != nil && err != io.EOF {
			debugLog("清理响应体数据失败: %v", err)
		} else if discarded > 0 {
			debugLog("清理响应体数据: %d 字节", discarded)
		}
		resp.Body.Close()
	}
	if cancel != nil {
		cancel() // 取消context
	}
}

// fixUnclosedThinkTags 修复未闭合的 <think> 标签
// 计算开启和闭合标签的数量差，并在末尾添加缺失的闭合标签
func fixUnclosedThinkTags(content string) string {
	if content == "" {
		return content
	}

	// 计算 <think> 和 </think> 标签的数量
	openCount := strings.Count(content, "<think>")
	closeCount := strings.Count(content, "</think>")

	debugLog("[FIX_TAGS] 检测标签数量: <think>=%d, </think>=%d", openCount, closeCount)

	// 如果开启标签多于闭合标签，添加缺失的闭合标签
	if openCount > closeCount {
		missingCount := openCount - closeCount
		debugLog("[FIX_TAGS] 检测到 %d 个未闭合的<think>标签，自动添加闭合标签", missingCount)

		// 在内容末尾添加缺失的闭合标签
		for i := 0; i < missingCount; i++ {
			content += "</think>"
		}

		debugLog("[FIX_TAGS] 已添加 %d 个</think>闭合标签", missingCount)

		// 验证修复结果
		newOpenCount := strings.Count(content, "<think>")
		newCloseCount := strings.Count(content, "</think>")
		debugLog("[FIX_TAGS] 修复后标签数量: <think>=%d, </think>=%d", newOpenCount, newCloseCount)

		if newOpenCount != newCloseCount {
			debugLog("[FIX_TAGS_ERROR] 修复失败，标签仍不平衡")
		}
	} else if openCount < closeCount {
		// 这种情况不太可能发生，但记录日志以便调试
		debugLog("[FIX_TAGS_WARNING] 闭合标签多于开启标签: <think>=%d, </think>=%d", openCount, closeCount)
	} else if openCount == closeCount && openCount > 0 {
		debugLog("[FIX_TAGS] 标签已平衡，无需修复: <think>=%d, </think>=%d", openCount, closeCount)
	}

	return content
}
