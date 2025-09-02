package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
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
	// 验证必需的配置
	if c.UpstreamToken == "" {
		return fmt.Errorf("UPSTREAM_TOKEN 环境变量是必需的")
	}

	// 验证URL格式
	if _, err := http.Get(c.UpstreamUrl); err != nil {
		// 这里只验证URL格式，不实际请求
		if !strings.HasPrefix(c.UpstreamUrl, "http") {
			return fmt.Errorf("UPSTREAM_URL 必须是有效的HTTP URL")
		}
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

	return nil
}

// 全局配置和缓存实例
var (
	appConfig  *Config
	tokenCache *TokenCache
)

// 模型常量
const (
	DefaultModelName  = "glm-4.5"
	ThinkingModelName = "glm-4.5-thinking"
	SearchModelName   = "glm-4.5-search"
	GLMAirModelName   = "glm-4.5-air"
	GLMVision         = "glm-4.5v"
)

// 伪装前端头部（来自抓包）
const (
	XFeVersion  = "prod-fe-1.0.70"
	BrowserUa   = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/139.0.0.0 Safari/537.36 Edg/139.0.0.0"
	SecChUa     = "\"Not;A=Brand\";v=\"99\", \"Microsoft Edge\";v=\"139\", \"Chromium\";v=\"139\""
	SecChUaMob  = "?0"
	SecChUaPlat = "\"Windows\""
	OriginBase  = "https://chat.z.ai"
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
			DisableCompression:    false,            // 启用压缩
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

	openAIResponsePool = sync.Pool{
		New: func() interface{} {
			return &OpenAIResponse{}
		},
	}

	// 添加更多对象池优化内存分配
	bytesBufferPool = sync.Pool{
		New: func() interface{} {
			return bytes.NewBuffer(make([]byte, 0, 1024)) // 预分配1KB
		},
	}

	upstreamRequestPool = sync.Pool{
		New: func() interface{} {
			return &UpstreamRequest{}
		},
	}

	// SSE缓冲区对象池，减少大缓冲区内存占用
	sseBufferPool = sync.Pool{
		New: func() interface{} {
			return make([]byte, 0, 8*1024) // 8KB初始缓冲区
		},
	}

	// 并发控制：限制同时处理的请求数量
	// 这可以防止在高并发时消耗过多资源
	// 注意：会在main函数中根据配置重新创建
	concurrencyLimiter chan struct{}
)

// ToolFunction 工具函数结构
type ToolFunction struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Parameters  interface{} `json:"parameters"`
}

// Tool 工具结构
type Tool struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
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
	ToolChoice        interface{}            `json:"tool_choice,omitempty"`
	ResponseFormat    interface{}            `json:"response_format,omitempty"`
	Seed              *int                   `json:"seed,omitempty"` // 使用指针表示可选
	LogProbs          bool                   `json:"logprobs,omitempty"`
	TopLogProbs       *int                   `json:"top_logprobs,omitempty"`        // 使用指针，需要0-5验证
	ParallelToolCalls *bool                  `json:"parallel_tool_calls,omitempty"` // 使用指针表示可选
	ServiceTier       string                 `json:"service_tier,omitempty"`        // 新增：服务层级
	Store             *bool                  `json:"store,omitempty"`               // 新增：是否存储
	Metadata          map[string]interface{} `json:"metadata,omitempty"`            // 新增：元数据
}

// ContentPart 内容部分结构（用于多模态消息）
type ContentPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL *struct {
		URL string `json:"url"`
	} `json:"image_url,omitempty"`
}

// Message 消息结构（支持多模态内容）
type Message struct {
	Role             string      `json:"role"`
	Content          interface{} `json:"content"` // 支持 string 或 []ContentPart
	ReasoningContent string      `json:"reasoning_content,omitempty"`
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
	Role             string `json:"role,omitempty"`
	Content          string `json:"content,omitempty"`
	ReasoningContent string `json:"reasoning_content,omitempty"`
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
		// 多模态内容数组
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
	default:
		// 其他类型，尝试转换为字符串
		return fmt.Sprintf("%v", v)
	}
}

// normalizeMessage 规范化消息格式，将多模态内容转换为上游可接受的格式
func normalizeMessage(msg Message) UpstreamMessage {
	// 对于上游API，我们提取文本内容并保持简单格式
	textContent := ""
	if msg.Content != nil {
		textContent = extractTextContent(msg.Content)
	}

	return UpstreamMessage{
		Role:             normalizeRole(msg.Role), // 规范化角色名称
		Content:          textContent,
		ReasoningContent: msg.ReasoningContent,
	}
}

// normalizeRole 规范化角色名称，处理OpenAI和Z.ai之间的差异
func normalizeRole(role string) string {
	switch role {
	case "developer":
		return "system" // developer角色映射为system
	default:
		return role
	}
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

	return params
}

// debug日志函数
func debugLog(format string, args ...interface{}) {
	if appConfig != nil && appConfig.DebugMode {
		log.Printf("[DEBUG] "+format, args...)
	}
}

// infoLog 记录重要信息（不受DEBUG模式限制）
func infoLog(format string, args ...interface{}) {
	log.Printf("[INFO] "+format, args...)
}

// RequestMetrics 请求指标结构
type RequestMetrics struct {
	Model         string
	IsStream      bool
	Duration      time.Duration
	TokenUsage    map[string]interface{}
	StatusCode    int
	ErrorType     string
	ContentLength int64
}

// 记录请求统计信息 - 增强版
func logRequestStats(model string, isStream bool, startTime time.Time, tokenUsage map[string]interface{}) {
	logRequestStatsWithCode(model, isStream, startTime, tokenUsage, 200, "")
}

// logRequestStatsWithCode 记录带状态码的请求统计信息
func logRequestStatsWithCode(model string, isStream bool, startTime time.Time, tokenUsage map[string]interface{}, statusCode int, errorType string) {
	duration := time.Since(startTime)
	mode := "non-streaming"
	if isStream {
		mode = "streaming"
	}

	// 改进的usage信息格式化 - 借鉴Worker.js
	usageStr := "no usage info"
	if tokenUsage != nil {
		if prompt, hasPrompt := tokenUsage["prompt_tokens"]; hasPrompt {
			if completion, hasCompletion := tokenUsage["completion_tokens"]; hasCompletion {
				if total, hasTotal := tokenUsage["total_tokens"]; hasTotal {
					usageStr = fmt.Sprintf("tokens(p:%v/c:%v/t:%v)", prompt, completion, total)
				}
			}
		} else if total, ok := tokenUsage["total_tokens"]; ok {
			usageStr = fmt.Sprintf("tokens: %v", total)
		} else if length, ok := tokenUsage["content_length"]; ok {
			usageStr = fmt.Sprintf("content_length: %v", length)
		}
	}

	// 增加状态码和错误信息
	statusInfo := fmt.Sprintf("status:%d", statusCode)
	if errorType != "" {
		statusInfo += fmt.Sprintf(" error:%s", errorType)
	}

	infoLog("请求完成 - 模型:%s 模式:%s 耗时:%v %s %s", model, mode, duration, statusInfo, usageStr)
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
		debugLog("获取新的匿名token成功，缓存到30分钟")
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
	// 伪装浏览器头
	req.Header.Set("User-Agent", BrowserUa)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	req.Header.Set("X-FE-Version", XFeVersion)
	req.Header.Set("sec-ch-ua", SecChUa)
	req.Header.Set("sec-ch-ua-mobile", SecChUaMob)
	req.Header.Set("sec-ch-ua-platform", SecChUaPlat)
	req.Header.Set("Origin", OriginBase)
	req.Header.Set("Referer", OriginBase+"/")

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

func main() {
	// 加载和验证配置
	var err error
	appConfig, err = loadConfig()
	if err != nil {
		log.Fatalf("配置加载失败: %v", err)
	}

	// 初始化Token缓存
	tokenCache = &TokenCache{}

	// 初始化并发控制器
	concurrencyLimiter = make(chan struct{}, appConfig.MaxConcurrentRequests)

	http.HandleFunc("/v1/models", handleModels)
	http.HandleFunc("/v1/chat/completions", handleChatCompletions)
	http.HandleFunc("/health", handleHealth) // 健康检查端点
	http.HandleFunc("/", handleOptions)

	log.Printf("OpenAI兼容API服务器启动在端口%s", appConfig.Port)
	log.Printf("模型: %s", DefaultModelName)
	log.Printf("上游: %s", appConfig.UpstreamUrl)
	log.Printf("Debug模式: %v", appConfig.DebugMode)
	log.Printf("匿名Token: %v", appConfig.AnonTokenEnabled)
	log.Printf("思考标签模式: %s", appConfig.ThinkTagsMode)
	log.Printf("并发限制: %d", appConfig.MaxConcurrentRequests)
	log.Printf("健康检查端点: http://localhost%s/health", appConfig.Port)

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

func handleOptions(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w)
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
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
	setCORSHeaders(w)
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	// 优雅降级: 如果上游服务不可用，仍返回基本模型列表
	response := getModelsList()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// getModelsList 获取模型列表，支持优雅降级
func getModelsList() ModelsResponse {
	// 首先尝试从上游获取模型列表（可选功能）
	// 这里简化处理，直接返回默认列表

	return ModelsResponse{
		Object: "list",
		Data: []Model{
			{
				ID:      DefaultModelName,
				Object:  "model",
				Created: time.Now().Unix(),
				OwnedBy: "z.ai",
			},
			{
				ID:      ThinkingModelName,
				Object:  "model",
				Created: time.Now().Unix(),
				OwnedBy: "z.ai",
			},
			{
				ID:      SearchModelName,
				Object:  "model",
				Created: time.Now().Unix(),
				OwnedBy: "z.ai",
			},
			{
				ID:      GLMAirModelName,
				Object:  "model",
				Created: time.Now().Unix(),
				OwnedBy: "z.ai",
			},
			{
				ID:      GLMVision,
				Object:  "model",
				Created: time.Now().Unix(),
				OwnedBy: "z.ai",
			},
		},
	}
}

func handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now() // 记录请求开始时间

	// 并发控制：获取信号量
	select {
	case concurrencyLimiter <- struct{}{}:
		defer func() { <-concurrencyLimiter }()
	case <-time.After(5 * time.Second): // 5秒超时
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
		handleAPIError(w, http.StatusUnauthorized, "invalid_api_key", "Missing or invalid Authorization header", "缺少或无效的Authorization头")
		return
	}

	apiKey := strings.TrimPrefix(authHeader, "Bearer ")
	if apiKey != appConfig.DefaultKey {
		handleAPIError(w, http.StatusUnauthorized, "invalid_api_key", "Invalid API key", "无效的API key: %s", apiKey)
		return
	}

	debugLog("API key验证通过")

	// 解析请求
	var req OpenAIRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		handleAPIError(w, http.StatusBadRequest, "invalid_request_error", "Invalid JSON format", "JSON解析失败: %v", err)
		return
	}

	debugLog("请求解析成功 - 模型: %s, 流式: %v, 消息数: %d", req.Model, req.Stream, len(req.Messages))

	// 生成会话相关ID - 优化时间戳获取
	now := time.Now()
	chatID := fmt.Sprintf("%d-%d", now.UnixNano(), now.Unix())
	msgID := fmt.Sprintf("%d", now.UnixNano())

	var (
		modelID   string
		modelName string
		isThing   bool
		isSearch  bool
		searchMcp string
	)

	switch req.Model {
	case ThinkingModelName:
		modelID = "0727-360B-API"
		modelName = "GLM-4.5"
		isThing = true
	case SearchModelName:
		modelID = "0727-360B-API"
		modelName = "GLM-4.5"
		isThing = true
		isSearch = true
		searchMcp = "deep-web-search"
	case DefaultModelName:
		// 默认模型不需要特殊处理
		modelID = "0727-360B-API"
		modelName = "GLM-4.5"
	case GLMAirModelName:
		modelID = "0727-106B-API"
		modelName = "GLM-4.5-Air"
	case GLMVision:
		modelID = GLMVision
		modelName = "GLM-4.5V"
	default:
		// 未知模型，使用默认处理
		debugLog("未知模型: %s, 使用默认处理", req.Model)
	}

	// 规范化消息格式（处理多模态内容）
	normalizedMessages := make([]UpstreamMessage, len(req.Messages))
	for i, msg := range req.Messages {
		normalizedMessages[i] = normalizeMessage(msg)
	}

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
		}{ID: modelID, Name: modelName, OwnedBy: "openai"},
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
		handleStreamResponseWithIDs(w, upstreamReq, chatID, authToken, req.Model, startTime)
	} else {
		handleNonStreamResponseWithIDs(w, upstreamReq, chatID, authToken, req.Model, startTime)
	}
}

func callUpstreamWithHeaders(upstreamReq UpstreamRequest, refererChatID string, authToken string) (*http.Response, error) {
	// 创建带超时的上下文
	ctx, cancel := context.WithTimeout(context.Background(), 55*time.Second)
	defer cancel()
	// 使用对象池减少内存分配
	buf := bytesBufferPool.Get().(*bytes.Buffer)
	defer func() {
		buf.Reset()
		bytesBufferPool.Put(buf)
	}()

	if err := json.NewEncoder(buf).Encode(upstreamReq); err != nil {
		debugLog("上游请求序列化失败: %v", err)
		return nil, err
	}

	debugLog("调用上游API: %s", appConfig.UpstreamUrl)
	debugLog("上游请求体: %s", buf.String())

	req, err := http.NewRequestWithContext(ctx, "POST", appConfig.UpstreamUrl, bytes.NewReader(buf.Bytes()))
	if err != nil {
		debugLog("创建HTTP请求失败: %v", err)
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("User-Agent", BrowserUa)
	req.Header.Set("Authorization", "Bearer "+authToken)
	req.Header.Set("Accept-Language", "zh-CN")
	req.Header.Set("sec-ch-ua", SecChUa)
	req.Header.Set("sec-ch-ua-mobile", SecChUaMob)
	req.Header.Set("sec-ch-ua-platform", SecChUaPlat)
	req.Header.Set("X-FE-Version", XFeVersion)
	req.Header.Set("Origin", OriginBase)
	req.Header.Set("Referer", OriginBase+"/c/"+refererChatID)

	resp, err := httpClient.Do(req)
	if err != nil {
		debugLog("上游请求失败: %v", err)
		return nil, err
	}

	debugLog("上游响应状态: %d %s", resp.StatusCode, resp.Status)
	return resp, nil
}

func handleStreamResponseWithIDs(w http.ResponseWriter, upstreamReq UpstreamRequest, chatID string, authToken string, model string, startTime time.Time) {
	debugLog("开始处理流式响应 (chat_id=%s)", chatID)

	resp, err := callUpstreamWithHeaders(upstreamReq, chatID, authToken)
	if err != nil {
		handleError(w, http.StatusBadGateway, "Failed to call upstream", "调用上游失败: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// 读取错误响应体用于调试
		if appConfig.DebugMode {
			body, _ := io.ReadAll(resp.Body)
			handleAPIError(w, http.StatusBadGateway, "upstream_error", "Upstream error", "上游返回错误状态: %d, 响应: %s", resp.StatusCode, string(body))
		} else {
			handleAPIError(w, http.StatusBadGateway, "upstream_error", "Upstream error", "上游返回错误状态: %d", resp.StatusCode)
		}
		return
	}

	// 设置SSE头部
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		handleAPIError(w, http.StatusInternalServerError, "streaming_unsupported", "Streaming not supported by server", "Streaming不受支持")
		return
	}

	// 发送第一个chunk（role）
	// "chatcmpl" 是 OpenAI API 的标准 ID 前缀（chat completion 的缩写）
	firstChunk := OpenAIResponse{
		ID:      fmt.Sprintf("chatcmpl-%d", time.Now().Unix()),
		Object:  "chat.completion.chunk",
		Created: time.Now().Unix(),
		Model:   DefaultModelName,
		Choices: []Choice{
			{
				Index: 0,
				Delta: Delta{Role: "assistant"},
			},
		},
	}
	writeSSEChunk(w, firstChunk)
	flusher.Flush()

	// 读取上游SSE流 - 使用优化的缓冲处理
	debugLog("开始读取上游SSE流")
	scanner := bufio.NewScanner(resp.Body)
	// 使用对象池的缓冲区，减少内存分配
	buf := sseBufferPool.Get().([]byte)
	defer func() {
		// 重置缓冲区并放回对象池
		buf = buf[:0]
		sseBufferPool.Put(buf)
	}()
	scanner.Buffer(buf, 512*1024) // 减少最大token大小到512KB
	lineCount := 0

	// 标记是否已发送最初的 answer 片段（来自 EditContent）
	var sentInitialAnswer bool
	var lastUsage map[string]interface{}

	for scanner.Scan() {
		line := scanner.Text()
		lineCount++

		// 更健墮的SSE数据行处理
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

		// 只记录重要的SSE事件，避免日志噪音
		if lineCount%20 == 1 || strings.Contains(dataStr, "done") || strings.Contains(dataStr, "error") {
			debugLog("处理SSE数据 (第%d行)", lineCount)
		}

		var upstreamData UpstreamData
		if err := json.Unmarshal([]byte(dataStr), &upstreamData); err != nil {
			debugLog("SSE数据解析失败: %v", err)
			continue
		}

		// 错误检测（data.error 或 data.data.error 或 顶层error）
		if (upstreamData.Error != nil) || (upstreamData.Data.Error != nil) || (upstreamData.Data.Inner != nil && upstreamData.Data.Inner.Error != nil) {
			errObj := upstreamData.Error
			if errObj == nil {
				errObj = upstreamData.Data.Error
			}
			if errObj == nil && upstreamData.Data.Inner != nil {
				errObj = upstreamData.Data.Inner.Error
			}
			debugLog("上游错误: code=%d, detail=%s", errObj.Code, errObj.Detail)
			// 结束下游流
			// "chatcmpl" 是 OpenAI API 的标准 ID 前缀（chat completion 的缩写）
			endChunk := OpenAIResponse{
				ID:      fmt.Sprintf("chatcmpl-%d", time.Now().Unix()),
				Object:  "chat.completion.chunk",
				Created: time.Now().Unix(),
				Model:   DefaultModelName,
				Choices: []Choice{{Index: 0, Delta: Delta{}, FinishReason: "stop"}},
			}
			writeSSEChunk(w, endChunk)
			fmt.Fprintf(w, "data: [DONE]\n\n")
			flusher.Flush()
			break
		}

		// 记录usage信息 - 借鉴Worker.js的详细处理
		if upstreamData.Data.Usage.TotalTokens > 0 {
			lastUsage = map[string]interface{}{
				"prompt_tokens":     upstreamData.Data.Usage.PromptTokens,
				"completion_tokens": upstreamData.Data.Usage.CompletionTokens,
				"total_tokens":      upstreamData.Data.Usage.TotalTokens,
			}
			debugLog("Token使用统计 - Prompt: %d, Completion: %d, Total: %d",
				upstreamData.Data.Usage.PromptTokens,
				upstreamData.Data.Usage.CompletionTokens,
				upstreamData.Data.Usage.TotalTokens)
		}

		// 只记录重要阶段变化，减少噪音
		if upstreamData.Data.Phase == "thinking" || upstreamData.Data.Done {
			debugLog("阶段变更 - 类型: %s, 阶段: %s, 内容长度: %d, 完成: %v",
				upstreamData.Type, upstreamData.Data.Phase, len(upstreamData.Data.DeltaContent), upstreamData.Data.Done)
		}

		// 策略2：总是展示thinking + answer
		// 处理EditContent在最初的answer信息（只发送一次）
		if !sentInitialAnswer && upstreamData.Data.EditContent != "" && upstreamData.Data.Phase == "answer" {
			var out = upstreamData.Data.EditContent
			if out != "" {
				var parts = detailsSplitRegex.Split(out, -1)
				if len(parts) > 1 {
					var content = parts[1]
					if content != "" {
						debugLog("发送普通内容: %s", content)
						// "chatcmpl" 是 OpenAI API 的标准 ID 前缀（chat completion 的缩写）
						chunk := createSSEChunk(content, false)
						writeSSEChunk(w, chunk)
						flusher.Flush()
						sentInitialAnswer = true
					}
				}
			}
		}

		if upstreamData.Data.DeltaContent != "" {
			var out = upstreamData.Data.DeltaContent
			if upstreamData.Data.Phase == "thinking" {
				out = transformThinking(out)
				// 思考内容使用 reasoning_content 字段
				if out != "" {
					debugLog("发送思考内容: %s", out)
					// "chatcmpl" 是 OpenAI API 的标准 ID 前缀（chat completion 的缩写）
					chunk := createSSEChunk(out, true)
					writeSSEChunk(w, chunk)
					flusher.Flush()
				}
			} else {
				// 普通内容使用 content 字段
				if out != "" {
					debugLog("发送普通内容: %s", out)
					// "chatcmpl" 是 OpenAI API 的标准 ID 前缀（chat completion 的缩写）
					chunk := createSSEChunk(out, false)
					writeSSEChunk(w, chunk)
					flusher.Flush()
				}
			}
		}

		// 检查是否结束
		if upstreamData.Data.Done || upstreamData.Data.Phase == "done" {
			debugLog("检测到流结束信号")
			// 发送结束chunk
			// "chatcmpl" 是 OpenAI API 的标准 ID 前缀（chat completion 的缩写）
			endChunk := OpenAIResponse{
				ID:      fmt.Sprintf("chatcmpl-%d", time.Now().Unix()),
				Object:  "chat.completion.chunk",
				Created: time.Now().Unix(),
				Model:   DefaultModelName,
				Choices: []Choice{
					{
						Index:        0,
						Delta:        Delta{},
						FinishReason: "stop",
					},
				},
			}
			writeSSEChunk(w, endChunk)
			flusher.Flush()

			// 发送[DONE]
			fmt.Fprintf(w, "data: [DONE]\n\n")
			flusher.Flush()
			debugLog("流式响应完成，共处理%d行", lineCount)
			// 记录请求统计信息
			logRequestStats(model, true, startTime, lastUsage)
			break
		}
	}

	if err := scanner.Err(); err != nil {
		debugLog("扫描器错误: %v", err)
	}
}

// createSSEChunk 创建SSE响应块
func createSSEChunk(content string, isReasoning bool) OpenAIResponse {
	now := time.Now()
	chunk := OpenAIResponse{
		ID:      fmt.Sprintf("chatcmpl-%d", now.Unix()),
		Object:  "chat.completion.chunk",
		Created: now.Unix(),
		Model:   DefaultModelName,
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

func handleNonStreamResponseWithIDs(w http.ResponseWriter, upstreamReq UpstreamRequest, chatID string, authToken string, model string, startTime time.Time) {
	debugLog("开始处理非流式响应 (chat_id=%s)", chatID)

	resp, err := callUpstreamWithHeaders(upstreamReq, chatID, authToken)
	if err != nil {
		debugLog("调用上游失败: %v", err)
		http.Error(w, "Failed to call upstream", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// 读取错误响应体用于调试
		if appConfig.DebugMode {
			body, _ := io.ReadAll(resp.Body)
			handleAPIError(w, http.StatusBadGateway, "upstream_error", "Upstream error", "上游返回错误状态: %d, 响应: %s", resp.StatusCode, string(body))
		} else {
			handleAPIError(w, http.StatusBadGateway, "upstream_error", "Upstream error", "上游返回错误状态: %d", resp.StatusCode)
		}
		return
	}

	// 收集完整响应（策略2：thinking与answer都纳入，thinking转换）
	// 使用对象池减少内存分配
	sb := stringBuilderPool.Get().(*strings.Builder)
	defer func() {
		sb.Reset()
		stringBuilderPool.Put(sb)
	}()
	fullContent := sb
	scanner := bufio.NewScanner(resp.Body)
	debugLog("开始收集完整响应内容")

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		dataStr := strings.TrimPrefix(line, "data: ")
		if dataStr == "" {
			continue
		}

		var upstreamData UpstreamData
		if err := json.Unmarshal([]byte(dataStr), &upstreamData); err != nil {
			continue
		}

		if upstreamData.Data.DeltaContent != "" {
			out := upstreamData.Data.DeltaContent
			if upstreamData.Data.Phase == "thinking" {
				out = transformThinking(out)
			}
			if out != "" {
				fullContent.WriteString(out)
			}
		}

		if upstreamData.Data.Done || upstreamData.Data.Phase == "done" {
			debugLog("检测到完成信号，停止收集")
			break
		}
	}

	finalContent := fullContent.String()
	debugLog("内容收集完成，最终长度: %d", len(finalContent))

	// 构造完整响应
	// "chatcmpl" 是 OpenAI API 的标准 ID 前缀（chat completion 的缩写）
	response := OpenAIResponse{
		ID:      fmt.Sprintf("chatcmpl-%d", time.Now().Unix()),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   DefaultModelName,
		Choices: []Choice{
			{
				Index: 0,
				Message: Message{
					Role:    "assistant",
					Content: finalContent,
				},
				FinishReason: "stop",
			},
		},
		Usage: Usage{
			PromptTokens:     0,
			CompletionTokens: 0,
			TotalTokens:      0,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
	debugLog("非流式响应发送完成")

	// 记录请求统计信息
	var usage map[string]interface{}
	if fullContent.Len() > 0 {
		// 非流式响应没有直接的usage信息，可以估算或留空
		usage = map[string]interface{}{"content_length": fullContent.Len()}
	}
	logRequestStats(model, false, startTime, usage)
}
