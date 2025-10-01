package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	// 添加Brotli支持

	"z2api/config"

	"github.com/andybalholm/brotli"
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
}

// getEnv 获取环境变量值，如果不存在则返回默认值
func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// loadConfig 加载并验证配置
func loadConfig() (*Config, error) {
	maxConcurrent := 100 // 默认值
	if envVal := getEnv("MAX_CONCURRENT_REQUESTS", "100"); envVal != "100" {
		if parsed, err := strconv.Atoi(envVal); err == nil && parsed > 0 && parsed <= 1000 {
			maxConcurrent = parsed
		}
	}

	config := &Config{
		UpstreamUrl:           getEnv("UPSTREAM_URL", "https://chat.z.ai/api/chat/completions"),
		DefaultKey:            getEnv("API_KEY", "sk-tbkFoKzk9a531YyUNNF5"),
		UpstreamToken:         getEnv("UPSTREAM_TOKEN", ""),
		Port:                  getEnv("PORT", "8080"),
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

// validate 验证配置合法性
func (c *Config) validate() error {
	// 验证URL格式
	if !strings.HasPrefix(c.UpstreamUrl, "http") {
		return fmt.Errorf("UPSTREAM_URL 必须是有效的HTTP URL")
	}

	// 验证端口格式
	if !strings.HasPrefix(c.Port, ":") {
		c.Port = ":" + c.Port
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
	DefaultModelName  = "glm-4.5"
	ThinkingModelName = "glm-4.5-thinking"
	SearchModelName   = "glm-4.5-search"
	GLMAirModelName   = "glm-4.5-air"
	GLMVision         = "glm-4.5v"
)

// 全局配置和缓存实例
var (
	appConfig  *Config
	tokenCache *TokenCache
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
	DefaultXFeVersion = "prod-fe-1.0.70"
	DefaultSecChUaMob = "?0"
)

const (
	OriginBase = "https://chat.z.ai"
)

// 全局HTTP客户端（连接池复用）
var (
	httpClient = &http.Client{
		Timeout: 60 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:          50, // 优化：减少全局空闲连接数
			MaxIdleConnsPerHost:   10, // 优化：减少单主机空闲连接数（主要对接z.ai）
			MaxConnsPerHost:       50, // 优化：减少单主机最大连接数
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second, // TLS握手超时
			ExpectContinueTimeout: 1 * time.Second,  // Expect: 100-continue超时
			ResponseHeaderTimeout: 10 * time.Second, // 响应头超时
			DisableKeepAlives:     false,            // 启用Keep-Alive
			DisableCompression:    false,            // 恢复自动压缩
		},
	}

	// 预编译的正则表达式模式
	summaryRegex      = regexp.MustCompile(`(?s)<summary>.*?</summary>`)
	detailsRegex      = regexp.MustCompile(`<details[^>]*>`)
	detailsSplitRegex = regexp.MustCompile(`\</details\>`)

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

	// 并发控制：限制同时处理的请求数量
	// 这可以防止在高并发时消耗过多资源
	// 注意：会在main函数中根据配置重新创建
	concurrencyLimiter chan struct{}
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
	// 先关闭gzip读取器
	if err := gz.reader.Close(); err != nil {
		return err
	}
	// 再关闭原始的响应体
	return gz.source.Close()
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
	// Brotli reader没有Close方法，直接关闭原始的响应体
	return br.source.Close()
}

// ToolCallFunction 工具调用函数结构
type ToolCallFunction struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

// ToolCall 工具调用结构
type ToolCall struct {
	Index    int              `json:"index"`
	ID       string           `json:"id,omitempty"`
	Type     string           `json:"type,omitempty"`
	Function ToolCallFunction `json:"function,omitempty"`
}

// SSEToolCallHandler SSE工具调用处理器
type SSEToolCallHandler struct {
	ToolCalls []ToolCall
}

// process 处理工具调用的增量内容
func (h *SSEToolCallHandler) process(deltaContent string) ([]ToolCall, error) {
	// 如果没有内容，直接返回当前的工具调用列表
	if deltaContent == "" {
		return h.ToolCalls, nil
	}

	// 尝试解析工具调用
	// 使用正则表达式匹配工具调用的各个部分
	// 匹配工具调用的开始：{"index": <int>, "id": "<id>", "type": "function", "function": {"name": "<name>", "arguments": ""
	toolCallStartRegex := regexp.MustCompile(`\{"index":\s*(\d+),\s*"id":\s*"([^"]+)",\s*"type":\s*"function",\s*"function":\s*\{"name":\s*"([^"]+)",\s*"arguments":\s*"([^"]*)`)

	// 匹配参数的增量更新："arguments": "<chunk>"
	argUpdateRegex := regexp.MustCompile(`"arguments":\s*"([^"]*)`)

	// 查找所有可能的工具调用开始
	startMatches := toolCallStartRegex.FindAllStringSubmatch(deltaContent, -1)

	for _, match := range startMatches {
		if len(match) < 5 {
			continue
		}

		index, err := strconv.Atoi(match[1])
		if err != nil {
			continue
		}

		id := match[2]
		name := match[3]
		args := match[4]

		// 检查是否已存在该索引的工具调用
		found := false
		for i, toolCall := range h.ToolCalls {
			if toolCall.Index == index {
				// 更新现有工具调用的参数
				h.ToolCalls[i].Function.Arguments += args
				found = true
				break
			}
		}

		// 如果不存在，则创建新的工具调用
		if !found {
			newToolCall := ToolCall{
				Index: index,
				ID:    id,
				Type:  "function",
				Function: ToolCallFunction{
					Name:      name,
					Arguments: args,
				},
			}
			h.ToolCalls = append(h.ToolCalls, newToolCall)
		}
	}

	// 处理参数更新（不包含工具调用开始的情况）
	argMatches := argUpdateRegex.FindAllStringSubmatch(deltaContent, -1)
	for _, match := range argMatches {
		if len(match) < 2 {
			continue
		}

		args := match[1]
		// 如果没有找到工具调用开始，但有参数更新，假设是更新最后一个工具调用
		if len(h.ToolCalls) > 0 && len(startMatches) == 0 {
			lastIndex := len(h.ToolCalls) - 1
			h.ToolCalls[lastIndex].Function.Arguments += args
		}
	}

	return h.ToolCalls, nil
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
	case []interface{}:
		// 多模态内容数组 - 旧格式兼容
		var textParts []string
		for _, part := range v {
			if partMap, ok := part.(map[string]interface{}); ok {
				if partType, hasType := partMap["type"].(string); hasType && partType == "text" {
					if text, hasText := partMap["text"].(string); hasText {
						textParts = append(textParts, text)
					}
				}
			}
		}
		return strings.Join(textParts, " ")
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
	} else if contentSlice, ok := msg.Content.([]interface{}); ok {
		// 兼容旧格式的多模态内容
		var textParts []string
		for _, part := range contentSlice {
			if partMap, ok := part.(map[string]interface{}); ok {
				if partType, hasType := partMap["type"].(string); hasType && partType == "text" {
					if text, hasText := partMap["text"].(string); hasText {
						textParts = append(textParts, text)
					}
				}
			}
		}
		return UpstreamMessage{
			Role:             normalizeRole(msg.Role),
			Content:          strings.Join(textParts, " "),
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
		s = detailsRegex.ReplaceAllString(s, "<think>")
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
	return strings.TrimSpace(s)
}

// recordRequestStats 记录请求统计信息
func recordRequestStats(startTime time.Time, path string, status int, tokens int64, model string, isStreaming bool) {
	duration := float64(time.Since(startTime)) / float64(time.Millisecond)

	// 检查 stats 是否已初始化，防止在测试环境中出现空指针错误
	if stats == nil {
		return // 在测试环境中，如果 stats 未初始化，则跳过统计记录
	}

	stats.mutex.Lock()
	defer stats.mutex.Unlock()

	stats.TotalRequests++
	stats.LastRequestTime = time.Now()

	if status >= 200 && status < 300 {
		stats.SuccessfulRequests++
	} else {
		stats.FailedRequests++
	}

	// Track endpoint-specific stats
	if path == "/v1/chat/completions" {
		stats.ApiCallsCount++
	} else if path == "/v1/models" {
		stats.ModelsCallsCount++
	}

	// Track tokens
	if tokens > 0 {
		stats.TotalTokensUsed += tokens
	}

	// Track model usage
	if model != "" {
		stats.ModelUsage[model]++
	}

	// Track streaming vs non-streaming
	if isStreaming {
		stats.StreamingRequests++
	} else {
		stats.NonStreamingRequests++
	}

	// Update response time stats
	if duration < stats.FastestResponse {
		stats.FastestResponse = duration
	}
	if duration > stats.SlowestResponse {
		stats.SlowestResponse = duration
	}

	// Update average response time
	totalDuration := stats.AverageResponseTime*float64(stats.TotalRequests-1) + duration
	stats.AverageResponseTime = totalDuration / float64(stats.TotalRequests)
}

// addLiveRequest 添加实时请求
func addLiveRequest(method string, path string, status int, duration float64, userAgent string, model string) {
	// 检查 stats 是否已初始化，防止在测试环境中出现空指针错误
	if stats == nil {
		return // 在测试环境中，如果 stats 未初始化，则跳过统计记录
	}

	request := LiveRequest{
		ID:        fmt.Sprintf("%d%.3f", time.Now().Unix(), float64(time.Now().UnixNano()%1000000000)/1000000000.0),
		Timestamp: time.Now(),
		Method:    method,
		Path:      path,
		Status:    status,
		Duration:  duration,
		UserAgent: userAgent,
		Model:     model,
	}

	liveRequestsMutex.Lock()
	defer liveRequestsMutex.Unlock()

	liveRequests = append(liveRequests, request)

	// Keep only last 100 requests
	if len(liveRequests) > 100 {
		liveRequests = liveRequests[len(liveRequests)-100:]
	}
}

// getTopModels 获取热门模型
func getTopModels() []struct {
	Model string `json:"model"`
	Count int64  `json:"count"`
} {
	// 检查 stats 是否已初始化，防止在测试环境中出现空指针错误
	if stats == nil {
		// 返回空结果而不是崩溃
		return []struct {
			Model string `json:"model"`
			Count int64  `json:"count"`
		}{}
	}

	stats.mutex.RLock()
	defer stats.mutex.RUnlock()

	// Convert map to slice and sort
	type modelCount struct {
		model string
		count int64
	}
	pairs := make([]modelCount, 0, len(stats.ModelUsage))
	for model, count := range stats.ModelUsage {
		pairs = append(pairs, modelCount{model, count})
	}

	// Sort by count (descending) using a more efficient algorithm
	sort.Slice(pairs, func(i, j int) bool {
		return pairs[i].count > pairs[j].count
	})

	// Take top 3
	maxLen := 3
	if len(pairs) < maxLen {
		maxLen = len(pairs)
	}
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

// getClientIP 获取客户端IP
func getClientIP(r *http.Request) string {
	xForwardedFor := r.Header.Get("X-Forwarded-For")
	if xForwardedFor != "" {
		ips := strings.Split(xForwardedFor, ",")
		if len(ips) > 0 {
			return strings.TrimSpace(ips[0])
		}
	}

	xRealIP := r.Header.Get("X-Real-IP")
	if xRealIP != "" {
		return xRealIP
	}

	// Extract IP from remote address
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	return host
}

// handleError 统一错误处理
func handleError(w http.ResponseWriter, statusCode int, message string, logMsg string, args ...interface{}) {
	if logMsg != "" {
		debugLog(logMsg, args...)
	}
	http.Error(w, message, statusCode)
}

// handleAPIError 处理API格式的错误响应，借鉴Worker.js的错误格式
func handleAPIError(w http.ResponseWriter, statusCode int, errorType string, message string, logMsg string, args ...interface{}) {
	if logMsg != "" {
		debugLog(logMsg, args...)
	}

	errorResponse := map[string]interface{}{
		"error": map[string]interface{}{
			"message": message,
			"type":    errorType,
			"code":    statusCode,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(errorResponse)
}

// handleUpstreamError 统一处理上游错误响应
func handleUpstreamError(w http.ResponseWriter, err *UpstreamError, debugMode bool) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadGateway)

	errorResponse := map[string]interface{}{
		"error": map[string]interface{}{
			"message": err.Detail,
			"type":    "upstream_error",
			"code":    err.Code,
		},
	}

	if debugMode {
		errorResponse["error"].(map[string]interface{})["debug"] = err.Detail
	}

	json.NewEncoder(w).Encode(errorResponse)
}

// 获取匿名token（每次对话使用不同token，避免共享记忆）
// GetToken 从缓存或新获取Token
func (tc *TokenCache) GetToken() (string, error) {
	// 先读锁检查缓存
	tc.mutex.RLock()
	if tc.token != "" && time.Now().Before(tc.expiresAt) {
		token := tc.token
		tc.mutex.RUnlock()
		debugLog("使用缓存的匿名token")
		return token, nil
	}
	tc.mutex.RUnlock()

	// 需要获取新token，使用写锁
	tc.mutex.Lock()
	defer tc.mutex.Unlock()

	// 双重检查，防止并发重复获取
	if tc.token != "" && time.Now().Before(tc.expiresAt) {
		debugLog("双重检查：使用缓存的匿名token")
		return tc.token, nil
	}

	// 获取新token
	newToken, err := getAnonymousTokenDirect()
	if err == nil {
		tc.token = newToken
		tc.expiresAt = time.Now().Add(5 * time.Minute) // 5分钟缓存
		debugLog("获取新的匿名token成功，缓存到5分钟")
	} else {
		debugLog("获取新的匿名token失败: %v", err)
	}
	return newToken, err
}

// getAnonymousToken 兼容性方法，使用缓存
func getAnonymousToken() (string, error) {
	if tokenCache == nil {
		return "", fmt.Errorf("token cache not initialized")
	}
	return tokenCache.GetToken()
}

// generateBrowserHeaders generates dynamic and consistent browser headers for a session.
func generateBrowserHeaders(sessionID, chatID, authToken, scenario string) map[string]string {
	fp, ok := config.GetFingerprintForSession(sessionID)
	var dynamicHeaders map[string]string

	if !ok {
		debugLog("未能从 fingerprints.json 加载指纹，回退到默认硬编码指纹")
		// Fallback to old logic if fingerprint system fails
		headers := make(map[string]string)
		chromeVersion := 128 + (time.Now().UnixNano() % 3)
		edgeVersion := chromeVersion
		userAgents := []string{
			fmt.Sprintf("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/%d.0.0.0 Safari/537.36 Edg/%d.0.0.0", chromeVersion, edgeVersion),
			fmt.Sprintf("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/%d.0.0.0 Safari/537.36", chromeVersion),
		}
		platforms := []string{"\"Windows\"", "\"macOS\""}
		headers["User-Agent"] = userAgents[time.Now().UnixNano()%int64(len(userAgents))]
		headers["sec-ch-ua"] = fmt.Sprintf(`"Chromium";v="%d", "Not(A:Brand";v="24", "Microsoft Edge";v="%d"`, chromeVersion, edgeVersion)
		headers["sec-ch-ua-mobile"] = DefaultSecChUaMob
		headers["sec-ch-ua-platform"] = platforms[time.Now().UnixNano()%int64(len(platforms))]
		headers["X-FE-Version"] = DefaultXFeVersion
		dynamicHeaders = headers
	} else {
		debugLog("使用会话指纹 (ID: %s) for chatID: %s", fp.ID, chatID)
		switch scenario {
		case "xhr":
			dynamicHeaders = fp.Headers.XHR
		case "js":
			dynamicHeaders = fp.Headers.JS
		default: // Default to "html"
			dynamicHeaders = fp.Headers.HTML
		}
		dynamicHeaders["User-Agent"] = fp.UserAgent
	}

	// Set common headers
	dynamicHeaders["Accept"] = "*/*"
	dynamicHeaders["Authorization"] = "Bearer " + authToken
	dynamicHeaders["Accept-Language"] = "zh-CN,zh;q=0.9,en;q=0.8,en-GB;q=0.7,en-US;q=0.6"
	dynamicHeaders["Accept-Encoding"] = "gzip, deflate, br, zstd"
	dynamicHeaders["sec-fetch-dest"] = "empty"
	dynamicHeaders["sec-fetch-mode"] = "cors"
	dynamicHeaders["sec-fetch-site"] = "same-origin"
	dynamicHeaders["Origin"] = OriginBase
	dynamicHeaders["Referer"] = OriginBase + "/c/" + chatID
	dynamicHeaders["Priority"] = "u=1, i"

	return dynamicHeaders
}

// getAnonymousTokenDirect 直接获取匿名token（原始方法，不使用缓存）
func getAnonymousTokenDirect() (string, error) {
	// 如果禁用匿名token，直接返回错误
	if !appConfig.AnonTokenEnabled {
		return "", fmt.Errorf("anonymous token disabled")
	}

	req, err := http.NewRequest("GET", OriginBase+"/api/v1/auths/", nil)
	if err != nil {
		return "", err
	}
	// 使用动态指纹
	headers := generateBrowserHeaders("", "", "", "html")
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("anon token status=%d", resp.StatusCode)
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", err
	}
	if body.Token == "" {
		return "", fmt.Errorf("anon token empty")
	}
	return body.Token, nil
}

// 从文件读取仪表板 HTML
func loadDashboardHTML() (string, error) {
	content, err := os.ReadFile("dashboard.html")
	if err != nil {
		return "", err
	}
	return string(content), nil
}

var dashboardHTML string

// main is the entry point of the application
func main() {
	// 加载仪表板HTML
	var err error
	dashboardHTML, err = loadDashboardHTML()
	if err != nil {
		log.Printf("警告: 无法加载仪表板文件 'dashboard.html': %v. 仪表板功能可能不可用。", err)
		// 如果无法加载dashboard.html，使用一个简单的默认HTML
		dashboardHTML = `<html><body><h1>Dashboard Unavailable</h1><p>Dashboard HTML file not found.</p></body></html>`
	}

	// 加载模型配置
	if err := config.LoadModels("models.json"); err != nil {
		log.Fatalf("错误: 无法加载模型配置文件 'models.json': %v", err)
	}

	// 加载浏览器指纹配置
	if err := config.LoadFingerprints("fingerprints.json"); err != nil {
		log.Printf("警告: 无法加载浏览器指纹文件 'fingerprints.json': %v. 将使用默认指纹。", err)
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

	// 初始化并发控制器
	concurrencyLimiter = make(chan struct{}, appConfig.MaxConcurrentRequests)

	http.HandleFunc("/v1/models", handleModels)
	http.HandleFunc("/v1/chat/completions", handleChatCompletions)
	http.HandleFunc("/health", handleHealth)                  // 健康检查端点
	http.HandleFunc("/dashboard", handleDashboard)            // Dashboard endpoint
	http.HandleFunc("/dashboard/stats", handleDashboardStats) // Stats endpoint
	http.HandleFunc("/", handleOptions)

	log.Printf("OpenAI兼容API服务器启动在端口%s", appConfig.Port)
	log.Printf("模型: %s", DefaultModelName)
	log.Printf("上游: %s", appConfig.UpstreamUrl)
	log.Printf("Debug模式: %v", appConfig.DebugMode)
	log.Printf("匿名Token: %v", appConfig.AnonTokenEnabled)
	log.Printf("思考标签模式: %s", appConfig.ThinkTagsMode)
	log.Printf("并发限制: %d", appConfig.MaxConcurrentRequests)
	log.Printf("健康检查端点: http://localhost%s/health", appConfig.Port)
	log.Printf("Dashboard端点: http://localhost%s/dashboard", appConfig.Port)

	server := &http.Server{
		Addr:              appConfig.Port,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      90 * time.Second, // 增加写超时，适应长流式响应
		IdleTimeout:       120 * time.Second,
		ReadHeaderTimeout: 10 * time.Second, // 添加请求头读取超时
		MaxHeaderBytes:    1 << 20,          // 1MB请求头限制
	}

	log.Fatal(server.ListenAndServe())
}

// handleDashboard handles the dashboard page
func handleDashboard(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, dashboardHTML)
}

// handleDashboardStats handles the dashboard stats endpoint
func handleDashboardStats(w http.ResponseWriter, r *http.Request) {
	// 检查 stats 是否已初始化，防止在测试环境中出现空指针错误
	if stats == nil {
		http.Error(w, "Stats not initialized", http.StatusInternalServerError)
		return
	}

	stats.mutex.RLock()
	defer stats.mutex.RUnlock()

	// Get top 3 models
	topModels := getTopModels()

	// Create a serializable stats response
	statsResponse := map[string]interface{}{
		"totalRequests":        stats.TotalRequests,
		"successfulRequests":   stats.SuccessfulRequests,
		"failedRequests":       stats.FailedRequests,
		"lastRequestTime":      stats.LastRequestTime,
		"averageResponseTime":  stats.AverageResponseTime,
		"homePageViews":        stats.HomePageViews,
		"apiCallsCount":        stats.ApiCallsCount,
		"modelsCallsCount":     stats.ModelsCallsCount,
		"streamingRequests":    stats.StreamingRequests,
		"nonStreamingRequests": stats.NonStreamingRequests,
		"totalTokensUsed":      stats.TotalTokensUsed,
		"startTime":            stats.StartTime,
		"fastestResponse":      stats.FastestResponse,
		"slowestResponse":      stats.SlowestResponse,
		"topModels":            topModels,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(statsResponse)
}

func handleOptions(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w)
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}
	// Track home page views
	if r.URL.Path == "/" {
		// 检查 stats 是否已初始化，防止在测试环境中出现空指针错误
		if stats != nil {
			stats.mutex.Lock()
			stats.HomePageViews++
			stats.mutex.Unlock()
		}
		fmt.Fprintf(w, "<h1>ZtoApi</h1><p>OpenAI compatible API for Z.ai.</p>")
		return
	}
	w.WriteHeader(http.StatusNotFound)
}

// handleHealth 健康检查端点
func handleHealth(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w)
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	// 检查上游服务可用性（简化版）
	status := "healthy"
	statusCode := http.StatusOK

	// 可以添加更多检查项，如数据库连接、上游API可用性等

	healthResponse := map[string]interface{}{
		"status":    status,
		"timestamp": time.Now().Unix(),
		"version":   "1.0.0",
		"config": map[string]interface{}{
			"debug_mode":              appConfig.DebugMode,
			"think_tags_mode":         appConfig.ThinkTagsMode,
			"anon_token_enabled":      appConfig.AnonTokenEnabled,
			"max_concurrent_requests": appConfig.MaxConcurrentRequests,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(healthResponse)
}

func setCORSHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
	w.Header().Set("Access-Control-Allow-Credentials", "true")
}

func handleModels(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	userAgent := r.Header.Get("User-Agent")

	setCORSHeaders(w)
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	// 优雅降级: 如果上游服务不可用，仍返回基本模型列表
	response := getModelsList()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)

	duration := float64(time.Since(startTime)) / float64(time.Millisecond)
	recordRequestStats(startTime, r.URL.Path, http.StatusOK, 0, "", false)
	addLiveRequest(r.Method, r.URL.Path, http.StatusOK, duration, userAgent, "")
}

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

func handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now() // 记录请求开始时间
	userAgent := r.Header.Get("User-Agent")

	// 并发控制：获取信号量
	select {
	case concurrencyLimiter <- struct{}{}:
		defer func() { <-concurrencyLimiter }()
	case <-time.After(5 * time.Second): // 5秒超时
		duration := float64(time.Since(startTime)) / float64(time.Millisecond)
		recordRequestStats(startTime, r.URL.Path, http.StatusTooManyRequests, 0, "", false)
		addLiveRequest(r.Method, r.URL.Path, http.StatusTooManyRequests, duration, userAgent, "")
		handleAPIError(w, http.StatusTooManyRequests, "rate_limit_exceeded", "Server overloaded", "服务器过载，请求超时")
		return
	}

	setCORSHeaders(w)
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	debugLog("收到chat completions请求")

	// 验证API Key
	authHeader := r.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, "Bearer ") {
		duration := float64(time.Since(startTime)) / float64(time.Millisecond)
		recordRequestStats(startTime, r.URL.Path, http.StatusUnauthorized, 0, "", false)
		addLiveRequest(r.Method, r.URL.Path, http.StatusUnauthorized, duration, userAgent, "")
		handleAPIError(w, http.StatusUnauthorized, "invalid_api_key", "Missing or invalid Authorization header", "缺少或无效的Authorization头")
		return
	}

	apiKey := strings.TrimPrefix(authHeader, "Bearer ")
	if apiKey != appConfig.DefaultKey {
		duration := float64(time.Since(startTime)) / float64(time.Millisecond)
		recordRequestStats(startTime, r.URL.Path, http.StatusUnauthorized, 0, "", false)
		addLiveRequest(r.Method, r.URL.Path, http.StatusUnauthorized, duration, userAgent, "")
		handleAPIError(w, http.StatusUnauthorized, "invalid_api_key", "Invalid API key", "无效的API key: %s", apiKey)
		return
	}

	debugLog("API key验证通过")

	// 解析请求
	var req OpenAIRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		duration := float64(time.Since(startTime)) / float64(time.Millisecond)
		recordRequestStats(startTime, r.URL.Path, http.StatusBadRequest, 0, "", false)
		addLiveRequest(r.Method, r.URL.Path, http.StatusBadRequest, duration, userAgent, "")
		handleAPIError(w, http.StatusBadRequest, "invalid_request_error", "Invalid JSON format", "JSON解析失败: %v", err)
		return
	}

	debugLog("请求解析成功 - 模型: %s, 流式: %v, 消息数: %d", req.Model, req.Stream, len(req.Messages))

	// 为指纹会话生成会话ID
	var sessionID string
	if req.User != "" {
		sessionID = req.User
	} else {
		sessionID = getClientIP(r)
	}

	// 生成会话相关ID - 优化时间戳获取
	now := time.Now()
	chatID := fmt.Sprintf("%d-%d", now.UnixNano(), now.Unix())
	msgID := fmt.Sprintf("%d", now.UnixNano())

	modelConfig, modelFound := config.GetModelConfig(req.Model)
	if !modelFound {
		debugLog("警告: 模型 '%s' 在 models.json 中未找到，将使用默认模型 '%s'", req.Model, modelConfig.ID)
	}

	isThing := modelConfig.Capabilities.Thinking
	isSearch := strings.Contains(req.Model, "search") // 保留search模型的特殊判断
	searchMcp := ""
	if isSearch {
		searchMcp = "deep-web-search"
	}

	modelID := modelConfig.UpstreamID
	modelName := modelConfig.Name

	// 规范化消息格式（处理多模态内容）
	normalizedMessages := make([]UpstreamMessage, len(req.Messages))
	for i, msg := range req.Messages {
		normalizedMessages[i] = normalizeMultimodalMessage(msg, req.Model)
	}

	// 解析ToolChoice参数，支持多种格式
	req.ToolChoiceObject = parseToolChoice(req.ToolChoice)

	// 构造上游请求
	upstreamReq := UpstreamRequest{
		Stream:   true, // 总是使用流式从上游获取
		ChatID:   chatID,
		ID:       msgID,
		Model:    modelID, // 上游实际模型ID
		Messages: normalizedMessages,
		Params:   buildUpstreamParams(req),
		Features: map[string]interface{}{
			"enable_thinking": isThing,
			"web_search":      isSearch,
			"auto_web_search": isSearch,
			"vision":          modelConfig.Capabilities.Vision,
		},
		BackgroundTasks: map[string]bool{
			"title_generation": false,
			"tags_generation":  false,
		},
		MCPServers: func() []string {
			if searchMcp != "" {
				return []string{searchMcp}
			}
			return []string{}
		}(),
		ModelItem: struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			OwnedBy string `json:"owned_by"`
		}{ID: modelConfig.ID, Name: modelName, OwnedBy: "openai"},
		ToolServers: []string{},
		Variables: map[string]string{
			"{{USER_NAME}}":        "User",
			"{{USER_LOCATION}}":    "Unknown",
			"{{CURRENT_DATETIME}}": time.Now().Format("2006-01-02 15:04:05"),
		},
		Tools:      req.Tools,      // 传递工具定义
		ToolChoice: req.ToolChoice, // 传递工具选择
	}

	// 选择本次对话使用的token - 增加重试机制
	authToken := appConfig.UpstreamToken
	if appConfig.AnonTokenEnabled {
		// 重试获取匿名token，最多3次
		for retry := 0; retry < 3; retry++ {
			if t, err := getAnonymousToken(); err == nil {
				authToken = t
				debugLog("匿名token获取成功")
				break
			} else {
				debugLog("匿名token获取失败 (第%d次): %v", retry+1, err)
				if retry < 2 {
					time.Sleep(time.Duration(retry+1) * 100 * time.Millisecond) // 指数退避
				}
			}
		}
		if authToken == appConfig.UpstreamToken {
			debugLog("所有匿名token获取尝试失败，使用固定token")
		}
	}

	// 调用上游API
	if req.Stream {
		handleStreamResponseWithIDs(w, r, upstreamReq, chatID, authToken, modelName, startTime, sessionID)
	} else {
		handleNonStreamResponseWithIDs(w, r, upstreamReq, chatID, authToken, modelName, startTime, sessionID)
	}
}

func callUpstreamWithHeaders(upstreamReq UpstreamRequest, refererChatID string, authToken string, sessionID string) (*http.Response, context.CancelFunc, error) {
	// 创建带超时的上下文 - 增加超时时间 for SSE streams
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)

	// 使用对象池减少内存分配
	buf := bytesBufferPool.Get().(*bytes.Buffer)
	defer func() {
		buf.Reset()
		bytesBufferPool.Put(buf)
	}()

	if err := json.NewEncoder(buf).Encode(upstreamReq); err != nil {
		debugLog("上游请求序列化失败: %v", err)
		cancel() // 手动取消上下文
		return nil, nil, err
	}

	debugLog("调用上游API: %s", appConfig.UpstreamUrl)
	debugLog("上游请求体: %s", buf.String())

	req, err := http.NewRequestWithContext(ctx, "POST", appConfig.UpstreamUrl, bytes.NewReader(buf.Bytes()))
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

	// Add additional headers for SSE compatibility
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")

	resp, err := httpClient.Do(req)
	if err != nil {
		debugLog("上游请求失败: %v", err)
		cancel() // 手动取消上下文
		return nil, nil, err
	}

	debugLog("上游响应状态: %d %s", resp.StatusCode, resp.Status)
	debugLog("响应头信息: Content-Encoding=%s, Content-Type=%s",
		resp.Header.Get("Content-Encoding"), resp.Header.Get("Content-Type"))

	// 检查响应是否被压缩
	contentEncoding := resp.Header.Get("Content-Encoding")
	debugLog("检测到Content-Encoding: %s", contentEncoding)

	switch contentEncoding {
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
		debugLog("响应未使用已知压缩格式（%s），直接处理", contentEncoding)
	}

	return resp, cancel, nil
}

func handleStreamResponseWithIDs(w http.ResponseWriter, r *http.Request, upstreamReq UpstreamRequest, chatID string, authToken string, modelName string, startTime time.Time, sessionID string) {
	userAgent := r.Header.Get("User-Agent")
	debugLog("开始处理流式响应 (chat_id=%s, model=%s)", chatID, upstreamReq.Model)

	resp, cancel, err := callUpstreamWithHeaders(upstreamReq, chatID, authToken, sessionID)
	if err != nil {
		duration := float64(time.Since(startTime)) / float64(time.Millisecond)
		recordRequestStats(startTime, r.URL.Path, http.StatusBadGateway, 0, modelName, true)
		addLiveRequest(r.Method, r.URL.Path, http.StatusBadGateway, duration, userAgent, modelName)
		handleError(w, http.StatusBadGateway, "Failed to call upstream", "调用上游失败: %v", err)
		return
	}

	// 确保在函数结束时取消上下文和关闭响应体
	defer func() {
		cancel()          // 取消上下文
		resp.Body.Close() // 关闭响应体
	}()

	if resp.StatusCode != http.StatusOK {
		duration := float64(time.Since(startTime)) / float64(time.Millisecond)
		recordRequestStats(startTime, r.URL.Path, http.StatusBadGateway, 0, modelName, true)
		addLiveRequest(r.Method, r.URL.Path, http.StatusBadGateway, duration, userAgent, modelName)
		if appConfig.DebugMode {
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				handleAPIError(w, http.StatusBadGateway, "upstream_error", "Upstream error", "上游返回错误状态: %d, 读取响应体失败: %v", resp.StatusCode, err)
			} else {
				handleAPIError(w, http.StatusBadGateway, "upstream_error", "Upstream error", "上游返回错误状态: %d, 响应: %s", resp.StatusCode, string(body))
			}
		} else {
			handleAPIError(w, http.StatusBadGateway, "upstream_error", "Upstream error", "上游返回错误状态: %d", resp.StatusCode)
		}
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		duration := float64(time.Since(startTime)) / float64(time.Millisecond)
		recordRequestStats(startTime, r.URL.Path, http.StatusInternalServerError, 0, modelName, true)
		addLiveRequest(r.Method, r.URL.Path, http.StatusInternalServerError, duration, userAgent, modelName)
		handleAPIError(w, http.StatusInternalServerError, "streaming_unsupported", "Streaming not supported by server", "Streaming不受支持")
		return
	}

	firstChunk := OpenAIResponse{
		ID:      fmt.Sprintf("chatcmpl-%d", time.Now().Unix()),
		Object:  "chat.completion.chunk",
		Created: time.Now().Unix(),
		Model:   upstreamReq.Model,
		Choices: []Choice{
			{
				Index: 0,
				Delta: Delta{Role: "assistant"},
			},
		},
	}
	writeSSEChunk(w, firstChunk)
	flusher.Flush()

	debugLog("开始读取上游SSE流")
	// 使用更健壮的扫描器方式，类似旧版本
	scanner := bufio.NewScanner(resp.Body)
	lineCount := 0
	var lastUsage map[string]interface{}
	var sentInitialAnswer bool

	// 创建工具调用处理器
	toolCallHandler := &SSEToolCallHandler{}

	for scanner.Scan() {
		line := scanner.Text()
		lineCount++

		// 更健壮的SSE数据行处理
		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "data: ") {
			continue
		}

		dataStr := strings.TrimPrefix(line, "data: ")
		dataStr = strings.TrimSpace(dataStr)
		if dataStr == "" || dataStr == "[DONE]" {
			if dataStr == "[DONE]" {
				debugLog("收到[DONE]信号，结束流处理")
				break
			}
			continue
		}
		var upstreamData UpstreamData
		if err := json.Unmarshal([]byte(dataStr), &upstreamData); err != nil {
			debugLog("SSE数据解析失败: %v", err)
			continue
		}

		if (upstreamData.Error != nil) || (upstreamData.Data.Error != nil) || (upstreamData.Data.Inner != nil && upstreamData.Data.Inner.Error != nil) {
			errObj := upstreamData.Error
			if errObj == nil {
				errObj = upstreamData.Data.Error
			}
			if errObj == nil && upstreamData.Data.Inner != nil {
				errObj = upstreamData.Data.Inner.Error
			}
			if errObj != nil {
				debugLog("上游错误: code=%d, detail=%s", errObj.Code, errObj.Detail)
				// 向客户端发送错误响应
				errorChunk := OpenAIResponse{
					ID:      fmt.Sprintf("chatcmpl-%d", time.Now().Unix()),
					Object:  "chat.completion.chunk",
					Created: time.Now().Unix(),
					Model:   upstreamReq.Model,
					Choices: []Choice{
						{
							Index:        0,
							Delta:        Delta{Content: ""},
							FinishReason: "error",
						},
					},
				}
				writeSSEChunk(w, errorChunk)
				flusher.Flush()
			}
			break
		}

		if upstreamData.Data.Usage.TotalTokens > 0 {
			lastUsage = map[string]interface{}{
				"prompt_tokens":     upstreamData.Data.Usage.PromptTokens,
				"completion_tokens": upstreamData.Data.Usage.CompletionTokens,
				"total_tokens":      upstreamData.Data.Usage.TotalTokens,
			}
		}

		if !sentInitialAnswer && upstreamData.Data.EditContent != "" && upstreamData.Data.Phase == "answer" {
			var out = upstreamData.Data.EditContent
			var parts = detailsSplitRegex.Split(out, -1)
			var contentToUse string
			if len(parts) > 1 {
				contentToUse = parts[1]
			} else {
				contentToUse = out
			}
			if contentToUse != "" {
				chunk := createSSEChunk(contentToUse, false, upstreamReq.Model)
				writeSSEChunk(w, chunk)
				flusher.Flush()
				sentInitialAnswer = true
			}
		}

		if upstreamData.Data.DeltaContent != "" {
			var out = upstreamData.Data.DeltaContent

			// 处理工具调用
			if upstreamData.Data.Phase == "tool_call" {
				// 使用工具调用处理器处理增量内容
				toolCalls, err := toolCallHandler.process(out)
				if err == nil && len(toolCalls) > 0 {
					// 创建包含工具调用的响应块
					chunk := OpenAIResponse{
						ID:      fmt.Sprintf("chatcmpl-%d", time.Now().Unix()),
						Object:  "chat.completion.chunk",
						Created: time.Now().Unix(),
						Model:   upstreamReq.Model,
						Choices: []Choice{
							{
								Index: 0,
								Delta: Delta{
									Role:      "assistant",
									ToolCalls: toolCalls,
								},
							},
						},
					}
					writeSSEChunk(w, chunk)
					flusher.Flush()
				}
			} else if upstreamData.Data.Phase == "thinking" {
				out = transformThinking(out)
				if out != "" {
					chunk := createSSEChunk(out, true, upstreamReq.Model)
					writeSSEChunk(w, chunk)
					flusher.Flush()
				}
			} else {
				if out != "" {
					chunk := createSSEChunk(out, false, upstreamReq.Model)
					writeSSEChunk(w, chunk)
					flusher.Flush()
				}
			}
		}

		if upstreamData.Data.Done || upstreamData.Data.Phase == "done" {
			// 检查是否有工具调用需要完成
			if len(toolCallHandler.ToolCalls) > 0 {
				// 发送工具调用完成信号
				endChunk := OpenAIResponse{
					ID:      fmt.Sprintf("chatcmpl-%d", time.Now().Unix()),
					Object:  "chat.completion.chunk",
					Created: time.Now().Unix(),
					Model:   upstreamReq.Model,
					Choices: []Choice{{Index: 0, Delta: Delta{}, FinishReason: "tool_calls"}},
				}
				writeSSEChunk(w, endChunk)
				flusher.Flush()
			}
			break
		}
	}

	if err := scanner.Err(); err != nil {
		debugLog("扫描器错误: %v", err)
	}

	// 根据是否有工具调用决定结束原因
	finishReason := "stop"
	if len(toolCallHandler.ToolCalls) > 0 {
		finishReason = "tool_calls"
	}

	endChunk := OpenAIResponse{
		ID:      fmt.Sprintf("chatcmpl-%d", time.Now().Unix()),
		Object:  "chat.completion.chunk",
		Created: time.Now().Unix(),
		Model:   upstreamReq.Model,
		Choices: []Choice{{Index: 0, Delta: Delta{}, FinishReason: finishReason}},
	}
	writeSSEChunk(w, endChunk)
	flusher.Flush()

	fmt.Fprintf(w, "data: [DONE]\n\n")
	flusher.Flush()
	debugLog("流式响应完成，共处理%d行", lineCount)

	duration := float64(time.Since(startTime)) / float64(time.Millisecond)
	var tokens int64
	if lastUsage != nil {
		if total, ok := lastUsage["total_tokens"]; ok {
			if totalVal, ok := total.(float64); ok {
				tokens = int64(totalVal)
			}
		}
	}
	recordRequestStats(startTime, r.URL.Path, 200, tokens, modelName, true)
	addLiveRequest(r.Method, r.URL.Path, 200, duration, userAgent, modelName)
}

// createSSEChunk 创建SSE响应块
func createSSEChunk(content string, isReasoning bool, model string) OpenAIResponse {
	now := time.Now()
	chunk := OpenAIResponse{
		ID:      fmt.Sprintf("chatcmpl-%d", now.Unix()),
		Object:  "chat.completion.chunk",
		Created: now.Unix(),
		Model:   model,
		Choices: []Choice{{Index: 0}},
	}

	if isReasoning {
		chunk.Choices[0].Delta = Delta{ReasoningContent: content}
	} else {
		chunk.Choices[0].Delta = Delta{Content: content}
	}

	return chunk
}

func writeSSEChunk(w http.ResponseWriter, chunk OpenAIResponse) {
	data, _ := json.Marshal(chunk)
	fmt.Fprintf(w, "data: %s\n\n", data)
}

func handleNonStreamResponseWithIDs(w http.ResponseWriter, r *http.Request, upstreamReq UpstreamRequest, chatID string, authToken string, modelName string, startTime time.Time, sessionID string) {
	userAgent := r.Header.Get("User-Agent")
	debugLog("开始处理非流式响应 (chat_id=%s)", chatID)

	resp, cancel, err := callUpstreamWithHeaders(upstreamReq, chatID, authToken, sessionID)
	if err != nil {
		duration := float64(time.Since(startTime)) / float64(time.Millisecond)
		recordRequestStats(startTime, r.URL.Path, http.StatusBadGateway, 0, modelName, false)
		addLiveRequest(r.Method, r.URL.Path, http.StatusBadGateway, duration, userAgent, modelName)
		debugLog("调用上游失败: %v", err)
		http.Error(w, "Failed to call upstream", http.StatusBadGateway)
		return
	}

	// 确保在函数结束时取消上下文和关闭响应体
	defer func() {
		cancel()          // 取消上下文
		resp.Body.Close() // 关闭响应体
	}()

	if resp.StatusCode != http.StatusOK {
		// 读取错误响应体用于调试
		duration := float64(time.Since(startTime)) / float64(time.Millisecond)
		recordRequestStats(startTime, r.URL.Path, http.StatusBadGateway, 0, modelName, false)
		addLiveRequest(r.Method, r.URL.Path, http.StatusBadGateway, duration, userAgent, modelName)
		if appConfig.DebugMode {
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				handleAPIError(w, http.StatusBadGateway, "upstream_error", "Upstream error", "上游返回错误状态: %d, 读取响应体失败: %v", resp.StatusCode, err)
			} else {
				handleAPIError(w, http.StatusBadGateway, "upstream_error", "Upstream error", "上游返回错误状态: %d, 响应: %s", resp.StatusCode, string(body))
			}
		} else {
			handleAPIError(w, http.StatusBadGateway, "upstream_error", "Upstream error", "上游返回错误状态: %d", resp.StatusCode)
		}
		return
	}

	// 收集完整响应（策略2：thinking与answer都纳入，thinking转换）
	// 使用更健壮的方法读取SSE流，类似TypeScript版本
	var fullContent strings.Builder
	var finalToolCalls []ToolCall // 用于存储工具调用
	lastUsage := Usage{}

	debugLog("开始收集完整响应内容")

	// 使用更健壮的扫描器方式，类似旧版本和流式响应
	scanner := bufio.NewScanner(resp.Body)
	lineCount := 0
	var upstreamError *UpstreamError // 用于存储上游错误信息

	for scanner.Scan() {
		line := scanner.Text()
		lineCount++

		// 更健壮的SSE数据行处理
		line = strings.TrimSpace(line)
		debugLog("处理第%d行原始数据: %s", lineCount, line)

		if line == "" || !strings.HasPrefix(line, "data: ") {
			debugLog("跳过非数据行: %s", line)
			continue
		}

		dataStr := strings.TrimPrefix(line, "data: ")
		dataStr = strings.TrimSpace(dataStr)
		debugLog("提取的数据字符串: %s", dataStr)

		if dataStr == "" || dataStr == "[DONE]" {
			if dataStr == "[DONE]" {
				debugLog("收到[DONE]信号，结束收集")
				break
			}
			continue
		}

		var upstreamData UpstreamData
		if err := json.Unmarshal([]byte(dataStr), &upstreamData); err != nil {
			debugLog("SSE数据解析失败 (第%d行): %v, 数据: %s", lineCount, err, dataStr)
			continue
		}

		debugLog("成功解析SSE数据: Type=%s, Phase=%s, DeltaContent=%s, EditContent=%s, Done=%v",
			upstreamData.Type, upstreamData.Data.Phase, upstreamData.Data.DeltaContent, upstreamData.Data.EditContent, upstreamData.Data.Done)

		// Check for error in upstream response
		if (upstreamData.Error != nil) || (upstreamData.Data.Error != nil) || (upstreamData.Data.Inner != nil && upstreamData.Data.Inner.Error != nil) {
			upstreamError = upstreamData.Error
			if upstreamError == nil {
				upstreamError = upstreamData.Data.Error
			}
			if upstreamError == nil && upstreamData.Data.Inner != nil {
				upstreamError = upstreamData.Data.Inner.Error
			}
			if upstreamError != nil {
				debugLog("上游返回错误 (第%d行): code=%d, detail=%s", lineCount, upstreamError.Code, upstreamError.Detail)
				// 设置错误标志，跳出循环
				break
			}
			continue
		}

		if upstreamData.Data.Usage.TotalTokens > 0 {
			lastUsage = upstreamData.Data.Usage
		}

		// 检查并处理工具调用
		if len(upstreamData.Data.ToolCalls) > 0 {
			debugLog("检测到工具调用，数量: %d", len(upstreamData.Data.ToolCalls))
			finalToolCalls = append(finalToolCalls, upstreamData.Data.ToolCalls...)
		}

		// Capture content from multiple possible sources to ensure we get the response
		var contentToUse string
		if upstreamData.Data.DeltaContent != "" {
			contentToUse = upstreamData.Data.DeltaContent
			debugLog("从DeltaContent提取内容: %s", contentToUse)
		} else if upstreamData.Data.EditContent != "" && upstreamData.Data.Phase == "answer" {
			// Process EditContent similar to streaming version
			var out = upstreamData.Data.EditContent
			var parts = detailsSplitRegex.Split(out, -1)
			if len(parts) > 1 {
				contentToUse = parts[1]
				debugLog("从EditContent提取内容: %s", contentToUse)
			} else {
				contentToUse = out
				debugLog("从EditContent提取内容 (未分割): %s", contentToUse)
			}
		}

		// Apply transformations based on phase
		if contentToUse != "" {
			finalContentToUse := contentToUse
			if upstreamData.Data.Phase == "thinking" {
				finalContentToUse = transformThinking(contentToUse)
			}
			if finalContentToUse != "" {
				fullContent.WriteString(finalContentToUse)
				debugLog("写入内容: %s", finalContentToUse)
			}
		}

		if upstreamData.Data.Done || upstreamData.Data.Phase == "done" {
			debugLog("检测到完成信号，停止收集 (第%d行)", lineCount)
			break
		}
	}

	if err := scanner.Err(); err != nil {
		debugLog("扫描器错误: %v", err)
	}

	finalContent := fullContent.String()
	debugLog("内容收集完成，总共处理%d行，最终长度: %d", lineCount, len(finalContent))

	// 检查是否有错误发生
	if upstreamError != nil {
		// 使用统一的错误处理函数
		handleUpstreamError(w, upstreamError, appConfig.DebugMode)
		debugLog("非流式错误响应发送完成: %s", upstreamError.Detail)
	} else {
		// 构造完整响应
		// "chatcmpl" 是 OpenAI API 的标准 ID 前缀（chat completion 的缩写）
		response := OpenAIResponse{
			ID:      fmt.Sprintf("chatcmpl-%d", time.Now().Unix()),
			Object:  "chat.completion",
			Created: time.Now().Unix(),
			Model:   upstreamReq.Model, // Use the actual upstream model instead of default
			Usage:   lastUsage,
		}

		// 根据是否存在工具调用来构造不同的响应
		if len(finalToolCalls) > 0 {
			debugLog("响应包含工具调用，数量: %d", len(finalToolCalls))
			response.Choices = []Choice{
				{
					Index: 0,
					Message: Message{
						Role:      "assistant",
						Content:   "", // 工具调用时内容为空
						ToolCalls: finalToolCalls,
					},
					FinishReason: "tool_calls",
				},
			}
		} else {
			debugLog("响应为普通文本内容，长度: %d", len(finalContent))
			response.Choices = []Choice{
				{
					Index: 0,
					Message: Message{
						Role:    "assistant",
						Content: finalContent,
					},
					FinishReason: "stop",
				},
			}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
		debugLog("非流式响应发送完成")
	}

	// 记录请求统计信息
	duration := float64(time.Since(startTime)) / float64(time.Millisecond)
	recordRequestStats(startTime, r.URL.Path, 200, int64(lastUsage.TotalTokens), modelName, false)
	addLiveRequest(r.Method, r.URL.Path, 200, duration, userAgent, modelName)
}
