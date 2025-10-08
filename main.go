package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
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
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	// 添加Brotli支持
	"z2api/config"

	"github.com/andybalholm/brotli"
	"github.com/google/uuid"
	"golang.org/x/sync/semaphore"
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
	token       string
	expiresAt   time.Time
	mutex       sync.RWMutex
	fetching    bool      // 标记是否正在获取token
	fetchErr    error     // 记录最后一次获取错误
	fetchTime   time.Time // 记录获取时间，用于超时控制
	invalidated bool      // 标记token是否已失效，需要强制刷新
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
	DefaultXFeVersion = "prod-fe-1.0.70"
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
	// 工具调用相关的正则表达式
	glmBlockRegex = regexp.MustCompile(`(?s)<glm_block\b[^>]*>(.*?)(?:</glm_block>|$)`)

	// 预编译的键值对提取正则表达式
	stringPattern = regexp.MustCompile(`"([^"]+)":\s*"([^"]*)"`)
	numberPattern = regexp.MustCompile(`"([^"]+)":\s*(\d+)`)
	boolPattern   = regexp.MustCompile(`"([^"]+)":\s*(true|false)`)

	// 预编译的工具调用提取正则表达式
	idRegex   = regexp.MustCompile(`"id"\s*:\s*"([^"]*)"`)
	nameRegex = regexp.MustCompile(`"name"\s*:\s*"([^"]*)"`)
	argsRegex = regexp.MustCompile(`"arguments"\s*:\s*"((?:\\.|[^"])*)"`)

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

	// JSON 解码器对象池
	jsonDecoderPool = sync.Pool{
		New: func() interface{} {
			return json.NewDecoder(strings.NewReader(""))
		},
	}

	// SSE 响应缓冲区对象池
	sseBufferPool = sync.Pool{
		New: func() interface{} {
			return make([]byte, 0, 1024) // 预分配1KB
		},
	}

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
	buffer         []byte
	toolCalls      []ToolCall
	expansionCount int // 跟踪缓冲区扩展次数
	maxExpansions  int // 最大允许扩展次数
	maxBufferSize  int // 最大缓冲区大小
}

// NewSSEToolCallHandler 创建新的SSE工具调用处理器
func NewSSEToolCallHandler() *SSEToolCallHandler {
	return &SSEToolCallHandler{
		buffer:         make([]byte, 0, 4096), // 预分配4KB
		toolCalls:      make([]ToolCall, 0),
		expansionCount: 0,
		maxExpansions:  50,                       // 减少最大扩展次数
		maxBufferSize:  int(MaxResponseSize / 2), // 限制单个handler最大缓冲区为响应大小的一半
	}
}

// isArgumentsComplete checks if a JSON string appears to be complete.
// 参考Python实现，增强JSON完整性检查机制
func (h *SSEToolCallHandler) isArgumentsComplete(args string) bool {
	args = strings.TrimSpace(args)
	if args == "" {
		return false
	}

	// Basic structure check: must end with } or "
	if !strings.HasSuffix(args, "}") && !strings.HasSuffix(args, "\"") {
		debugLog("参数不完整: 没有正确的结束符号")
		return false
	}

	// Enhanced JSON structure validation
	args = h.fixIncompleteJSON(args)

	// Stack-based balance check
	var stack []rune
	inString := false
	isEscaped := false

	for _, char := range args {
		if isEscaped {
			isEscaped = false
			continue
		}

		if char == '\\' {
			isEscaped = true
			continue
		}

		if char == '"' {
			inString = !inString
		}

		if !inString {
			switch char {
			case '{', '[':
				stack = append(stack, char)
			case '}':
				if len(stack) == 0 || stack[len(stack)-1] != '{' {
					return false // Unmatched closing brace
				}
				stack = stack[:len(stack)-1]
			case ']':
				if len(stack) == 0 || stack[len(stack)-1] != '[' {
					return false // Unmatched closing bracket
				}
				stack = stack[:len(stack)-1]
			}
		}
	}

	structurallyComplete := len(stack) == 0 && !inString
	if !structurallyComplete {
		return false
	}

	// Enhanced semantic completeness checks
	// Try to parse as JSON to get structured data, with more lenient approach
	var argsMap map[string]interface{}
	if err := json.Unmarshal([]byte(args), &argsMap); err != nil {
		debugLog("参数JSON解析失败，但可能是部分有效参数: %v", err)
		// 不立即返回false，而是尝试更多验证
		// 如果是基本结构完整但内容可能不完整的情况，继续验证
		return h.isBasicStructureValid(args)
	}

	// Check for incomplete URLs (common truncation indicator)
	for key, value := range argsMap {
		if strValue, ok := value.(string); ok {
			// Check URL completeness with more flexible rules
			if strings.Contains(strings.ToLower(strValue), "http") {
				// URL too short is suspicious, but allow for query parameters and paths
				if len(strValue) < 8 { // http://a is minimum valid URL
					debugLog("参数不完整: URL过短 (%s: %s)", key, strValue)
					return false
				}
				// More comprehensive check for incomplete domain patterns
				if h.isIncompleteURL(strValue) {
					debugLog("参数不完整: URL域名未完成 (%s: %s)", key, strValue)
					return false
				}
			}

			if len(strValue) > 0 && h.isTruncatedValue(strValue) {
				lastTenChars := ""
				if len(strValue) >= 10 {
					lastTenChars = strValue[len(strValue)-10:]
				} else {
					lastTenChars = strValue
				}
				debugLog("参数不完整: 値可能被截断 (%s: ...%s)", key, lastTenChars)
				return false
			}
		}
	}

	return true
}

// isBasicStructureValid 检查基本的JSON结构是否有效
func (h *SSEToolCallHandler) isBasicStructureValid(args string) bool {
	// 检查是否至少有基本的JSON结构
	if !strings.HasPrefix(strings.TrimSpace(args), "{") {
		return false
	}
	if !strings.HasSuffix(strings.TrimSpace(args), "}") {
		return false
	}

	// 检查引号是否平衡
	quoteCount := 0
	escaped := false
	for _, r := range args {
		if escaped {
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		if r == '"' {
			quoteCount++
		}
	}

	// 引号应该是偶数个
	return quoteCount%2 == 0
}

// isIncompleteURL 检查URL是否不完整
func (h *SSEToolCallHandler) isIncompleteURL(url string) bool {
	// 常见的不完整URL模式
	incompletePatterns := []string{
		".go", ".goo", ".goog", // Google相关的不完整域名
		".co", ".net", ".or", ".ed", // 常见TLD的不完整形式
		"://", "http", "https", // 协议不完整
	}

	for _, pattern := range incompletePatterns {
		if strings.HasSuffix(url, pattern) {
			return true
		}
	}

	// 检查是否以域名分隔符结尾但没有完整TLD
	if strings.HasSuffix(url, ".com/") || strings.HasSuffix(url, ".net/") {
		return false // 这些是完整的
	}

	if strings.HasSuffix(url, ".") && !strings.HasSuffix(url, "./") {
		return true // 以点结尾但不是相对路径
	}

	return false
}

// isTruncatedValue 检查值是否可能被截断
func (h *SSEToolCallHandler) isTruncatedValue(value string) bool {
	if len(value) == 0 {
		return false
	}

	lastChar := value[len(value)-1]

	// 检查明显的截断标记
	truncationChars := []rune{'/', ':', '=', '&', '?', ','}
	for _, char := range truncationChars {
		if rune(lastChar) == char {
			return true
		}
	}

	// 特殊处理点号：如果不是句子结尾，可能是截断
	if lastChar == '.' && len(value) > 1 {
		// 如果前一个字符不是空格，可能是截断（如域名、扩展名等）
		prevChar := value[len(value)-2]
		if prevChar != ' ' && prevChar != '\n' && prevChar != '\t' {
			// 进一步检查：如果看起来像文件扩展名或域名，可能是截断
			if strings.Contains(value, "http") || strings.Contains(value, "www") ||
				strings.Contains(value, ".com") || strings.Contains(value, ".js") {
				return true
			}
		}
	}

	return false
}

// fixIncompleteJSON 修复不完整的JSON字符串，参考Python实现
func (h *SSEToolCallHandler) fixIncompleteJSON(jsonStr string) string {
	if jsonStr == "" {
		return "{}"
	}

	// Remove leading/trailing whitespace
	jsonStr = strings.TrimSpace(jsonStr)

	// Handle special cases
	if strings.ToLower(jsonStr) == "null" || jsonStr == "\"null\"" {
		return "{}"
	}

	// Handle escaped JSON strings
	if strings.HasPrefix(jsonStr, "{\\\"") && strings.HasSuffix(jsonStr, "\\\"}") {
		// This is an escaped JSON string, need to unescape
		jsonStr = strings.ReplaceAll(jsonStr, "\\\"", "\"")
	} else if strings.HasPrefix(jsonStr, "\"{\\\"") && strings.HasSuffix(jsonStr, "\\\"}\"") {
		// Double-escaped case
		jsonStr = jsonStr[1 : len(jsonStr)-1] // Remove outer quotes
		jsonStr = strings.ReplaceAll(jsonStr, "\\\"", "\"")
	} else if strings.HasPrefix(jsonStr, "\"") && strings.HasSuffix(jsonStr, "\"") {
		// Simple quote wrapping, remove outer quotes
		jsonStr = jsonStr[1 : len(jsonStr)-1]
	}

	// Ensure starts with {
	if !strings.HasPrefix(jsonStr, "{") {
		jsonStr = "{" + jsonStr
	}

	// Handle incomplete string values - fix unmatched quotes
	quoteCount := strings.Count(jsonStr, "\"") - strings.Count(jsonStr, "\\\"")
	if quoteCount%2 != 0 {
		// Odd number of quotes, may have unclosed string
		jsonStr += "\""
	}

	// Ensure ends with }
	if !strings.HasSuffix(jsonStr, "}") {
		jsonStr += "}"
	}

	return jsonStr
}

// cleanArgumentsString 清理和标准化参数字符串，参考Python实现
func (h *SSEToolCallHandler) cleanArgumentsString(argumentsRaw string) string {
	if argumentsRaw == "" {
		return "{}"
	}

	// Remove leading/trailing whitespace
	cleaned := strings.TrimSpace(argumentsRaw)

	// Handle special values
	if strings.ToLower(cleaned) == "null" {
		return "{}"
	}

	// Handle escaped JSON strings
	if strings.HasPrefix(cleaned, "{\\\"") && strings.HasSuffix(cleaned, "\\\"}") {
		// This is an escaped JSON string, need to unescape
		cleaned = strings.ReplaceAll(cleaned, "\\\"", "\"")
	} else if strings.HasPrefix(cleaned, "\"{\\\"") && strings.HasSuffix(cleaned, "\\\"}\"") {
		// Double-escaped case
		cleaned = cleaned[1 : len(cleaned)-1] // Remove outer quotes
		cleaned = strings.ReplaceAll(cleaned, "\\\"", "\"")
	} else if strings.HasPrefix(cleaned, "\"") && strings.HasSuffix(cleaned, "\"") {
		// Simple quote wrapping, remove outer quotes
		cleaned = cleaned[1 : len(cleaned)-1]
	}

	// Fix incomplete JSON
	cleaned = h.fixIncompleteJSON(cleaned)

	// Try to parse and re-serialize for normalization
	var parsed interface{}
	if err := json.Unmarshal([]byte(cleaned), &parsed); err == nil {
		if parsed == nil {
			return "{}"
		}
		if normalized, err := json.Marshal(parsed); err == nil {
			return string(normalized)
		}
	} else {
		debugLog("JSON标准化失败，保持原样: %s...", cleaned[:min(50, len(cleaned))])
	}

	return cleaned
}

// extractKeyValuePairs 从文本中提取键值对，作为最后的解析尝试
func (h *SSEToolCallHandler) extractKeyValuePairs(text string) map[string]interface{} {
	result := make(map[string]interface{})

	// Match "key": "value" or "key": value patterns
	matches := stringPattern.FindAllStringSubmatch(text, -1)
	for _, match := range matches {
		if len(match) >= 3 {
			result[match[1]] = match[2]
		}
	}

	// Match number values
	matches = numberPattern.FindAllStringSubmatch(text, -1)
	for _, match := range matches {
		if len(match) >= 3 {
			if num, err := strconv.Atoi(match[2]); err == nil {
				result[match[1]] = num
			} else {
				result[match[1]] = match[2]
			}
		}
	}

	// Match boolean values
	matches = boolPattern.FindAllStringSubmatch(text, -1)
	for _, match := range matches {
		if len(match) >= 3 {
			result[match[1]] = match[2] == "true"
		}
	}

	return result
}

// parsePartialArguments 解析不完整的参数字符串，尽可能提取有效信息
func (h *SSEToolCallHandler) parsePartialArguments(argumentsRaw string) map[string]interface{} {
	if argumentsRaw == "" || strings.TrimSpace(argumentsRaw) == "" || strings.ToLower(strings.TrimSpace(argumentsRaw)) == "null" {
		return make(map[string]interface{})
	}

	// Try cleaning first
	cleaned := h.cleanArgumentsString(argumentsRaw)
	var result map[string]interface{}
	if err := json.Unmarshal([]byte(cleaned), &result); err == nil {
		if result == nil {
			return make(map[string]interface{})
		}
		return result
	}

	// Try fixing common JSON issues
	fixed := strings.TrimSpace(argumentsRaw)

	// Handle escape characters
	if strings.Contains(fixed, "\\") {
		fixed = strings.ReplaceAll(fixed, "\\\"", "\"")
	}

	// If doesn't start with {, add it
	if !strings.HasPrefix(fixed, "{") {
		fixed = "{" + fixed
	}

	// If doesn't end with }, try adding it
	if !strings.HasSuffix(fixed, "}") {
		// Count unmatched quotes and brackets
		quoteCount := strings.Count(fixed, "\"") - strings.Count(fixed, "\\\"")
		if quoteCount%2 != 0 {
			fixed += "\""
		}
		fixed += "}"
	}

	if err := json.Unmarshal([]byte(fixed), &result); err == nil {
		return result
	}

	// Last resort: extract key-value pairs
	return h.extractKeyValuePairs(argumentsRaw)
}

// min helper function
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// shouldSendArgumentUpdate decides if a tool call update is significant enough to be sent.
func (h *SSEToolCallHandler) shouldSendArgumentUpdate(oldArgs, newArgs string) bool {
	if newArgs == oldArgs {
		return false
	}
	// If the new arguments are complete, send the update.
	if h.isArgumentsComplete(newArgs) {
		return true
	}
	// If the new arguments are a significant improvement (e.g., much longer), send the update.
	if len(newArgs) > len(oldArgs)+10 { // Arbitrary threshold for "significant"
		return true
	}
	return false
}

// process handles incremental content for tool calls and returns updates to be sent.
func (h *SSEToolCallHandler) process(editIndex int, editContent string) ([]ToolCall, error) {
	if editContent == "" {
		return nil, nil // Return nil for no updates
	}

	h.applyEditToBuffer(editIndex, editContent)
	return h.processToolCallsFromBuffer()
}

// applyEditToBuffer updates the content buffer at a specific position.
// 改进缓冲区管理：增强输入验证和更安全的內存控制
func (h *SSEToolCallHandler) applyEditToBuffer(editIndex int, editContent string) {
	editBytes := []byte(editContent)

	// 增强的输入验证和边界检查
	if editIndex < 0 {
		debugLog("无效的editIndex: %d，跳过处理", editIndex)
		return
	}

	// 检查内容大小是否合理
	if len(editBytes) > h.maxBufferSize/2 {
		debugLog("单次编辑内容过大: %d > %d，截断处理", len(editBytes), h.maxBufferSize/2)
		editBytes = editBytes[:h.maxBufferSize/2]
	}

	// 防护恶意请求：检查editIndex是否异常大
	maxReasonableIndex := len(h.buffer) + 1024*1024 // 允许1MB的合理跳跃
	if editIndex > maxReasonableIndex {
		debugLog("editIndex异常大: %d > %d，可能是恶意请求，强制使用追加模式", editIndex, maxReasonableIndex)
		editIndex = len(h.buffer) // 强制使用追加模式
	}

	// 重新计算所需长度
	requiredLength := editIndex + len(editBytes)

	// 检查editIndex是否超出绝对最大限制
	if editIndex > h.maxBufferSize {
		debugLog("editIndex超出最大限制: %d > %d，使用追加模式", editIndex, h.maxBufferSize)
		editIndex = len(h.buffer)
		requiredLength = editIndex + len(editBytes)
	}

	// 检查所需总长度是否超出限制
	if requiredLength > h.maxBufferSize {
		debugLog("工具调用缓冲区将超出最大限制: required=%d, max=%d", requiredLength, h.maxBufferSize)
		// 计算可容纳的最大内容长度
		maxContentLength := h.maxBufferSize - editIndex
		if maxContentLength <= 0 {
			debugLog("缓冲区已满，无法添加更多内容")
			return
		}
		// 截断内容以适应缓冲区限制
		editBytes = editBytes[:maxContentLength]
		requiredLength = editIndex + len(editBytes)
		debugLog("截断内容以适应缓冲区: 截断后长度=%d", len(editBytes))
	}

	// 检查扩展次数限制，防止无限扩展
	if len(h.buffer) < requiredLength {
		h.expansionCount++
		if h.expansionCount > h.maxExpansions {
			debugLog("工具调用缓冲区扩展次数超限: count=%d, max=%d", h.expansionCount, h.maxExpansions)
			return
		}

		// 使用更保守的扩展策略，减少内存占用
		var newCapacity int
		currentLen := len(h.buffer)

		// 优先使用所需长度，避免过度分配
		newCapacity = requiredLength

		// 只有在合理范围内才考虑适量的额外容量
		if requiredLength < currentLen*2 && currentLen < h.maxBufferSize/4 {
			// 为未来扩展预留25%的空间，但不超过当前需求的1.5倍
			extraSpace := min(requiredLength/4, 4096) // 最多4KB的额外空间
			newCapacity = requiredLength + extraSpace
		}

		// 确保不超过最大限制
		if newCapacity > h.maxBufferSize {
			newCapacity = h.maxBufferSize
		}

		// 验证新容量的合理性
		if newCapacity < requiredLength {
			debugLog("计算的新容量不足: %d < %d", newCapacity, requiredLength)
			newCapacity = requiredLength
		}

		// 分配新缓冲区
		newBuffer := make([]byte, newCapacity)
		copy(newBuffer, h.buffer)
		h.buffer = newBuffer[:requiredLength] // 设置实际长度
		debugLog("工具调用缓冲区扩展: 第%d次, 新大小=%d, 容量=%d", h.expansionCount, requiredLength, newCapacity)
	} else {
		// 如果缓冲区足够大，只需调整长度
		if requiredLength > len(h.buffer) {
			h.buffer = h.buffer[:requiredLength]
		}
	}

	// 安全复制数据，防止越界
	if editIndex < len(h.buffer) && len(editBytes) > 0 {
		endIndex := editIndex + len(editBytes)
		if endIndex > len(h.buffer) {
			endIndex = len(h.buffer)
			editBytes = editBytes[:endIndex-editIndex]
		}
		copy(h.buffer[editIndex:endIndex], editBytes)
	}
}

// processToolCallsFromBuffer parses tool calls from the buffer and returns updates to be sent.
// 优化性能：减少字符串/字节数组转换
func (h *SSEToolCallHandler) processToolCallsFromBuffer() ([]ToolCall, error) {
	// 避免每次都创建新的string，优化性能
	if len(h.buffer) == 0 {
		return nil, nil
	}

	// 使用更高效的字节操作，避免不必要的内存分配
	cleanBuffer := make([]byte, 0, len(h.buffer))
	for _, b := range h.buffer {
		if b != 0 {
			cleanBuffer = append(cleanBuffer, b)
		}
	}

	contentStr := string(cleanBuffer)
	matches := glmBlockRegex.FindAllStringSubmatch(contentStr, -1)

	var updates []ToolCall
	for _, match := range matches {
		if len(match) > 1 {
			blockContent := match[1]
			if updatedTool, shouldSend := h.processSingleToolBlock(blockContent); shouldSend {
				updates = append(updates, updatedTool)
			}
		}
	}
	return updates, nil
}

// processSingleToolBlock handles a single tool block and decides if an update should be sent.
func (h *SSEToolCallHandler) processSingleToolBlock(blockContent string) (ToolCall, bool) {
	var toolData struct {
		Data struct {
			Metadata struct {
				ID        string `json:"id"`
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"metadata"`
		} `json:"data"`
	}

	if err := json.Unmarshal([]byte(blockContent), &toolData); err == nil {
		metadata := toolData.Data.Metadata
		if metadata.ID != "" && metadata.Name != "" {
			return h.handleToolUpdate(metadata.ID, metadata.Name, metadata.Arguments)
		}
	} else {
		return h.extractToolDataFromPartialJSON(blockContent)
	}
	return ToolCall{}, false
}

// extractToolDataFromPartialJSON extracts tool information from partial JSON data.
// 使用增强的JSON解析方法
func (h *SSEToolCallHandler) extractToolDataFromPartialJSON(blockContent string) (ToolCall, bool) {

	var id, name, args string
	if idMatches := idRegex.FindStringSubmatch(blockContent); len(idMatches) > 1 {
		id = idMatches[1]
	}
	if nameMatches := nameRegex.FindStringSubmatch(blockContent); len(nameMatches) > 1 {
		name = nameMatches[1]
	}
	if argsMatches := argsRegex.FindStringSubmatch(blockContent); len(argsMatches) > 1 {
		// 使用增强的参数解析
		args = h.cleanArgumentsString(argsMatches[1])
	}

	if id != "" && name != "" {
		return h.handleToolUpdate(id, name, args)
	}
	return ToolCall{}, false
}

// handleToolUpdate handles the creation or update of a tool call and decides if an update should be sent.
func (h *SSEToolCallHandler) handleToolUpdate(toolID, toolName, argumentsRaw string) (ToolCall, bool) {
	// Find if the tool call already exists.
	for i, tc := range h.toolCalls {
		if tc.ID == toolID {
			if h.shouldSendArgumentUpdate(tc.Function.Arguments, argumentsRaw) {
				h.toolCalls[i].Function.Arguments = argumentsRaw
				return h.toolCalls[i], true
			}
			return h.toolCalls[i], false // Return existing tool, but don't send
		}
	}

	// If it doesn't exist, add a new tool call.
	newToolCall := ToolCall{
		Index: len(h.toolCalls),
		ID:    toolID,
		Type:  "function",
		Function: ToolCallFunction{
			Name:      toolName,
			Arguments: argumentsRaw,
		},
	}
	h.toolCalls = append(h.toolCalls, newToolCall)
	return newToolCall, true
}

// GetToolCalls 获取当前解析的工具调用列表
func (h *SSEToolCallHandler) GetToolCalls() []ToolCall {
	return h.toolCalls
}

// Reset 重置解析器状态，优化内存使用
func (h *SSEToolCallHandler) Reset() {
	// 优化内存管理：根据使用情况动态调整缓冲区大小
	currentCap := cap(h.buffer)

	// 如果缓冲区过大，重新分配较小的缓冲区
	if currentCap > 16384 { // 如果容量超过16KB
		h.buffer = make([]byte, 0, 4096) // 重新分配4KB
		debugLog("重置工具调用处理器: 重新分配缓冲区，从%d字节减少到4096字节", currentCap)
	} else if currentCap > 8192 { // 如果容量超过8KB
		h.buffer = make([]byte, 0, 4096) // 重新分配4KB
		debugLog("重置工具调用处理器: 重新分配缓冲区，从%d字节减少到4096字节", currentCap)
	} else {
		h.buffer = h.buffer[:0] // 重用现有缓冲区但清空内容
	}

	// 重用toolCalls切片，避免重新分配
	if cap(h.toolCalls) > 16 { // 如果容量过大
		h.toolCalls = make([]ToolCall, 0, 4) // 重新分配较小容量
	} else {
		h.toolCalls = h.toolCalls[:0] // 重用现有切片
	}

	h.expansionCount = 0
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

// maskJSONForLogging masks sensitive fields in a JSON string for logging purposes.
func maskJSONForLogging(jsonStr string) string {
	var data map[string]interface{}
	if err := json.Unmarshal([]byte(jsonStr), &data); err != nil {
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

	maskedBytes, err := json.Marshal(data)
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

// transformThinking 转换思考内容 - 使用对象池优化
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

// getTopModels 获取热门模型（保持向后兼容）
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
			"system": true, "user": true, "assistant": true, "developer": true,
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

	// 验证总内容长度
	//if totalContentLength > 1000000 { // 总内容最大1M
	//	return fmt.Errorf("总消息内容过长，最大支持1M")
	//}

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

// ErrorHandler 统一错误处理器，提供一致的错误处理机制
type ErrorHandler struct {
	debugMode bool
}

// NewErrorHandler 创建新的错误处理器
func NewErrorHandler(debugMode bool) *ErrorHandler {
	return &ErrorHandler{debugMode: debugMode}
}

// HandleAPIError 处理API格式的错误响应，统一错误格式和日志记录
func (eh *ErrorHandler) HandleAPIError(w http.ResponseWriter, statusCode int, errorType string, message string, logMsg string, args ...interface{}) {
	// 统一日志记录
	if logMsg != "" {
		debugLog(logMsg, args...)
	}

	// 设置CORS头，确保错误响应也符合CORS要求
	setCORSHeaders(w)

	errorDetail := ErrorDetail{
		Message: message,
		Type:    errorType,
		Code:    statusCode,
	}

	// 仅在调试模式下添加调试信息
	if eh.debugMode && logMsg != "" {
		errorDetail.Debug = fmt.Sprintf(logMsg, args...)
	}

	errorResponse := ErrorResponse{
		Error: errorDetail,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	if err := json.NewEncoder(w).Encode(errorResponse); err != nil {
		debugLog("编码错误响应失败: %v", err)
		// 降级为简单错误响应
		http.Error(w, message, statusCode)
	}
}

// HandleUpstreamError 统一处理上游错误响应
func (eh *ErrorHandler) HandleUpstreamError(w http.ResponseWriter, err *UpstreamError) {
	setCORSHeaders(w)

	errorDetail := ErrorDetail{
		Message: err.Detail,
		Type:    "upstream_error",
		Code:    err.Code,
	}

	// 仅在调试模式下添加调试信息
	if eh.debugMode {
		errorDetail.Debug = err.Detail
	}

	errorResponse := ErrorResponse{
		Error: errorDetail,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadGateway)
	if encodeErr := json.NewEncoder(w).Encode(errorResponse); encodeErr != nil {
		debugLog("编码上游错误响应失败: %v", encodeErr)
		// 降级为简单错误响应
		http.Error(w, err.Detail, http.StatusBadGateway)
	}
}

// HandleStreamError 处理流式响应中的错误
func (eh *ErrorHandler) HandleStreamError(w http.ResponseWriter, flusher http.Flusher, model string, errMsg string) {
	errorChunk := OpenAIResponse{
		ID:      fmt.Sprintf("chatcmpl-%d", time.Now().Unix()),
		Object:  "chat.completion.chunk",
		Created: time.Now().Unix(),
		Model:   model,
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

	// 发送[DONE]信号
	fmt.Fprintf(w, "data: [DONE]\n\n")
	flusher.Flush()

	debugLog("流式错误响应已发送: %s", errMsg)
}

// RecoverFromPanic 恢复panic并返回适当的错误响应
func (eh *ErrorHandler) RecoverFromPanic(w http.ResponseWriter, r *http.Request) {
	if r := recover(); r != nil {
		debugLog("捕获到panic: %v", r)
		eh.HandleAPIError(w, http.StatusInternalServerError, "internal_server_error",
			"Internal server error", "服务内部错误: %v", r)
	}
}

// 全局错误处理器实例
var globalErrorHandler *ErrorHandler

// 获取匿名token（每次对话使用不同token，避免共享记忆）
// GetToken 从缓存或新获取Token，改进等待逻辑和错误处理
func (tc *TokenCache) GetToken() (string, error) {
	// 先读锁检查缓存
	tc.mutex.RLock()
	if tc.token != "" && time.Now().Before(tc.expiresAt) && !tc.fetching && !tc.invalidated {
		token := tc.token
		tc.mutex.RUnlock()
		debugLog("使用缓存的匿名token")
		tokenCacheHits.Add(1)
		return token, nil
	}
	isFetching := tc.fetching
	lastFetchTime := tc.fetchTime
	lastFetchErr := tc.fetchErr
	tc.mutex.RUnlock()

	// 如果正在获取中，使用更好的等待机制
	if isFetching {
		// 检查是否已经超时（避免死锁）
		if time.Since(lastFetchTime) > 30*time.Second {
			debugLog("检测到token获取超时，尝试重新获取")
			// 尝试重置状态
			tc.mutex.Lock()
			if tc.fetching && time.Since(tc.fetchTime) > 30*time.Second {
				tc.fetching = false
				debugLog("重置超时的token获取状态")
			}
			tc.mutex.Unlock()
		} else {
			// 使用更短的等待时间和退避策略
			timeout := time.NewTimer(10 * time.Second) // 最多等待10秒
			defer timeout.Stop()

			ticker := time.NewTicker(50 * time.Millisecond) // 更频繁的检查
			defer ticker.Stop()

			for {
				select {
				case <-timeout.C:
					debugLog("等待token获取超时")
					// 返回上次的错误或创建新错误
					if lastFetchErr != nil {
						return "", fmt.Errorf("token获取超时，上次错误: %v", lastFetchErr)
					}
					return "", fmt.Errorf("token获取超时")
				case <-ticker.C:
					tc.mutex.RLock()
					if tc.token != "" && time.Now().Before(tc.expiresAt) && !tc.fetching && !tc.invalidated {
						token := tc.token
						tc.mutex.RUnlock()
						debugLog("等待后使用缓存的匿名token")
						return token, nil
					}
					stillFetching := tc.fetching
					currentFetchErr := tc.fetchErr
					tc.mutex.RUnlock()

					if !stillFetching {
						// 获取完成，检查是否有有效token
						tc.mutex.RLock()
						if tc.token != "" && time.Now().Before(tc.expiresAt) && !tc.invalidated {
							token := tc.token
							tc.mutex.RUnlock()
							return token, nil
						}
						tc.mutex.RUnlock()

						// 没有有效token，返回获取错误
						if currentFetchErr != nil {
							return "", currentFetchErr
						}
						break // 退出等待，尝试自己获取
					}
				}
			}
		}
	}

	// 尝试获取写锁来获取新token
	tc.mutex.Lock()
	defer tc.mutex.Unlock()

	// 双重检查，可能在等待锁的时候其他goroutine已经获取了
	if tc.token != "" && time.Now().Before(tc.expiresAt) && !tc.invalidated {
		debugLog("双重检查：使用缓存的匿名token")
		tokenCacheHits.Add(1)
		return tc.token, nil
	}

	// 如果已经有goroutine在获取且未超时，则返回错误
	if tc.fetching && time.Since(tc.fetchTime) < 30*time.Second {
		return "", fmt.Errorf("另一个goroutine正在获取token")
	}

	// 设置获取标记
	tc.fetching = true
	tc.fetchTime = time.Now()
	tc.fetchErr = nil
	tc.invalidated = false // 清除失效标记
	defer func() {
		tc.fetching = false
	}()

	// 临时释放锁来获取token（避免长时间持有锁）
	tc.mutex.Unlock()
	newToken, fetchErr := getAnonymousTokenDirect()
	tc.mutex.Lock()

	// 记录获取结果
	tc.fetchErr = fetchErr

	if fetchErr == nil {
		tc.token = newToken
		tc.expiresAt = time.Now().Add(5 * time.Minute) // 5分钟缓存
		debugLog("获取新的匿名token成功，缓存5分钟")
		tokenCacheMisses.Add(1)
	} else {
		debugLog("获取新的匿名token失败: %v", fetchErr)
		tokenCacheMisses.Add(1)
		// 清理可能的过期token
		if time.Now().After(tc.expiresAt) {
			tc.token = ""
		}
	}

	return newToken, fetchErr
}

// InvalidateToken 立即将当前token标记为失效，强制获取新token
func (tc *TokenCache) InvalidateToken() {
	tc.mutex.Lock()
	defer tc.mutex.Unlock()
	tc.invalidated = true
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

	// 初始化异步统计收集器
	statsCollector = NewStatsCollector(1000) // 1000个缓冲区大小

	// 初始化并发控制器
	concurrencyLimiter = semaphore.NewWeighted(int64(appConfig.MaxConcurrentRequests))

	http.HandleFunc("/v1/models", handleModels)
	http.HandleFunc("/v1/chat/completions", handleChatCompletions)
	http.HandleFunc("/health", handleHealth)                        // 健康检查端点
	http.HandleFunc("/dashboard", handleDashboard)                  // Dashboard endpoint
	http.HandleFunc("/dashboard/stats", handleDashboardStats)       // Stats endpoint
	http.HandleFunc("/dashboard/requests", handleDashboardRequests) // Live requests endpoint
	http.HandleFunc("/", handleOptions)

	// 初始化全局错误处理器
	globalErrorHandler = NewErrorHandler(appConfig.DebugMode)

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

// handleDashboardRequests handles the dashboard live requests endpoint
func handleDashboardRequests(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w)
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	liveRequestsMutex.RLock()
	// Create a copy of the slice to avoid holding the lock while encoding
	requests := make([]LiveRequest, len(liveRequests))
	copy(requests, liveRequests)
	liveRequestsMutex.RUnlock()

	// Reverse the slice so the newest requests are first
	for i, j := 0, len(requests)-1; i < j; i, j = i+1, j-1 {
		requests[i], requests[j] = requests[j], requests[i]
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(requests); err != nil {
		debugLog("Failed to encode live requests: %v", err)
		http.Error(w, "Failed to encode requests", http.StatusInternalServerError)
	}
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
	// 移除 Access-Control-Allow-Credentials 以与 * origin 兼容
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

	// 更新监控指标
	totalRequests.Add(1)
	currentConcurrency.Add(1)
	defer currentConcurrency.Add(-1)

	// 改进的并发控制：使用带上下文的超时机制
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second) // 10秒等待超时
	defer cancel()

	// 使用 semaphore 进行并发控制
	if err := concurrencyLimiter.Acquire(ctx, 1); err != nil {
		duration := float64(time.Since(startTime)) / float64(time.Millisecond)
		recordRequestStats(startTime, r.URL.Path, http.StatusServiceUnavailable, 0, "", false)
		addLiveRequest(r.Method, r.URL.Path, http.StatusServiceUnavailable, duration, userAgent, "")
		// 使用503状态码表示服务暂时不可用，而不是429
		globalErrorHandler.HandleAPIError(w, http.StatusServiceUnavailable, "service_unavailable",
			"Service temporarily unavailable, please try again later",
			"服务暂时不可用，并发限制达到上限，等待超时")
		requestErrors.Add("service_unavailable", 1)
		return
	}
	defer concurrencyLimiter.Release(1)

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
		globalErrorHandler.HandleAPIError(w, http.StatusUnauthorized, "invalid_api_key", "Missing or invalid Authorization header", "缺少或无效的Authorization头")
		requestErrors.Add("invalid_api_key", 1)
		return
	}

	apiKey := strings.TrimPrefix(authHeader, "Bearer ")
	if apiKey != appConfig.DefaultKey {
		duration := float64(time.Since(startTime)) / float64(time.Millisecond)
		recordRequestStats(startTime, r.URL.Path, http.StatusUnauthorized, 0, "", false)
		addLiveRequest(r.Method, r.URL.Path, http.StatusUnauthorized, duration, userAgent, "")
		globalErrorHandler.HandleAPIError(w, http.StatusUnauthorized, "invalid_api_key", "Invalid API key", "API密钥验证失败")
		requestErrors.Add("invalid_api_key", 1)
		return
	}

	debugLog("API key验证通过")

	// 解析请求 - 使用对象池优化
	var req OpenAIRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		duration := float64(time.Since(startTime)) / float64(time.Millisecond)
		recordRequestStats(startTime, r.URL.Path, http.StatusBadRequest, 0, "", false)
		addLiveRequest(r.Method, r.URL.Path, http.StatusBadRequest, duration, userAgent, "")
		globalErrorHandler.HandleAPIError(w, http.StatusBadRequest, "invalid_request_error", "Invalid JSON format", "JSON解析失败: %v", err)
		requestErrors.Add("invalid_request_error", 1)
		return
	}

	debugLog("请求解析成功 - 模型: %s, 流式: %v, 消息数: %d", req.Model, req.Stream, len(req.Messages))

	// 验证和清理输入数据
	if err := validateAndSanitizeInput(&req); err != nil {
		duration := float64(time.Since(startTime)) / float64(time.Millisecond)
		recordRequestStats(startTime, r.URL.Path, http.StatusBadRequest, 0, "", false)
		addLiveRequest(r.Method, r.URL.Path, http.StatusBadRequest, duration, userAgent, "")
		globalErrorHandler.HandleAPIError(w, http.StatusBadRequest, "invalid_request_error", err.Error(), "输入验证失败: %v", err)
		requestErrors.Add("invalid_request_error", 1)
		return
	}

	debugLog("输入验证通过")

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
		defaultModel, defaultFound := config.GetDefaultModel()
		if defaultFound {
			debugLog("警告: 模型 '%s' 在 models.json 中未找到，将使用默认模型 '%s'", req.Model, defaultModel.ID)
			modelConfig = defaultModel
		} else {
			// 如果连默认模型都找不到，这是一个严重问题
			globalErrorHandler.HandleAPIError(w, http.StatusInternalServerError, "model_not_found", "Model not found and no default model configured", "模型 '%s' 未找到且无默认模型", req.Model)
			requestErrors.Add("model_not_found", 1)
			return
		}
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

	if err := json.NewEncoder(buf).Encode(upstreamReq); err != nil {
		debugLog("上游请求序列化失败: %v", err)
		cancel() // 手动取消上下文
		return nil, nil, err
	}

	debugLog("调用上游API: %s (超时: %v)", appConfig.UpstreamUrl, timeout)
	debugLog("上游请求体: %s", maskJSONForLogging(buf.String()))

	// 生成签名所需的参数
	requestID := uuid.New().String()
	timestamp := time.Now().UnixMilli()
	userContent := extractLastUserContent(upstreamReq)

	// 从 authToken 中解析 user_id
	var userID string
	if jwtPayload, err := decodeJWT(authToken); err == nil {
		userID = jwtPayload.ID
		debugLog("从 JWT token 中成功解析 user_id: %s", userID)
	} else {
		// Fallback logic matching Python's abs(hash(token)) % 1000000
		hashVal := hashString(authToken)
		userID = fmt.Sprintf("guest-user-%d", hashVal%1000000)
		debugLog("解析 JWT token 失败: %v, 使用回退 user_id: %s", err, userID)
	}

	// 生成签名
	signature, err := generateZsSignature(userID, requestID, timestamp, userContent)
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
	req.Header.Set("X-Signature", signature.Signature)

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

// callUpstreamWithRetry 调用上游API并处理重试，改进资源管理和超时控制
func callUpstreamWithRetry(upstreamReq UpstreamRequest, chatID string, authToken string, sessionID string) (*http.Response, context.CancelFunc, error) {
	var lastErr error
	maxRetries := 3

	for attempt := 0; attempt < maxRetries; attempt++ {
		// 每次重试都创建新的context，避免context污染
		resp, cancel, err := callUpstreamWithHeaders(upstreamReq, chatID, authToken, sessionID)
		if err != nil {
			debugLog("上游调用失败 (尝试 %d/%d): %v", attempt+1, maxRetries, err)
			if cancel != nil {
				cancel() // 立即取消context以释放资源
			}
			lastErr = err
			if attempt < maxRetries-1 {
				// 指数退避：100ms, 200ms, 400ms
				backoffDelay := time.Duration(100*(1<<attempt)) * time.Millisecond
				time.Sleep(backoffDelay)
				continue
			}
			return nil, nil, fmt.Errorf("上游API在 %d 次尝试后仍然失败: %w", maxRetries, lastErr)
		}

		// 检查可重试的状态码
		switch resp.StatusCode {
		case http.StatusOK:
			debugLog("上游调用成功 (尝试 %d/%d): %d", attempt+1, maxRetries, resp.StatusCode)
			return resp, cancel, nil // 成功，直接返回

		case http.StatusUnauthorized:
			debugLog("收到401错误 (尝试 %d/%d)", attempt+1, maxRetries)
			// 如果是401且启用了匿名token，尝试刷新token
			if appConfig.AnonTokenEnabled {
				debugLog("尝试刷新匿名token")
				if newToken, tokenErr := getAnonymousTokenDirect(); tokenErr == nil {
					authToken = newToken
					debugLog("成功获取新的匿名token，将在下次重试中使用")
				} else {
					debugLog("刷新匿名token失败: %v", tokenErr)
				}
			}
			// 关闭当前响应并重试
			cleanupResponse(resp, cancel)

		case http.StatusTooManyRequests:
			debugLog("收到429限流错误 (尝试 %d/%d)", attempt+1, maxRetries)
			// 对于限流，使用更长的等待时间
			cleanupResponse(resp, cancel)
			if attempt < maxRetries-1 {
				// 限流等待时间：1s, 2s, 4s
				rateLimitDelay := time.Duration(1<<attempt) * time.Second
				debugLog("限流等待 %v", rateLimitDelay)
				time.Sleep(rateLimitDelay)
			}

		case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			debugLog("收到网关错误 %d (尝试 %d/%d)", resp.StatusCode, attempt+1, maxRetries)
			cleanupResponse(resp, cancel)

		default:
			// 对于其他不可重试的错误，直接返回
			debugLog("收到不可重试的状态码 %d，不再重试", resp.StatusCode)
			return resp, cancel, nil
		}

		// 如果是最后一次尝试，返回错误
		if attempt == maxRetries-1 {
			lastErr = fmt.Errorf("上游API在 %d 次尝试后仍然失败，最后状态码: %d", maxRetries, resp.StatusCode)
			return nil, nil, lastErr
		}

		// 等待后重试（除限流外的普通重试）
		if resp.StatusCode != http.StatusTooManyRequests {
			backoffDelay := time.Duration(100*(1<<attempt)) * time.Millisecond
			debugLog("等待 %v 后重试", backoffDelay)
			time.Sleep(backoffDelay)
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

func handleStreamResponseWithIDs(w http.ResponseWriter, r *http.Request, upstreamReq UpstreamRequest, chatID string, authToken string, modelName string, startTime time.Time, sessionID string) {
	userAgent := r.Header.Get("User-Agent")
	debugLog("开始处理流式响应 (chat_id=%s, model=%s)", chatID, upstreamReq.Model)

	resp, cancel, err := callUpstreamWithRetry(upstreamReq, chatID, authToken, sessionID)
	if err != nil {
		duration := float64(time.Since(startTime)) / float64(time.Millisecond)
		recordRequestStats(startTime, r.URL.Path, http.StatusBadGateway, 0, modelName, true)
		addLiveRequest(r.Method, r.URL.Path, http.StatusBadGateway, duration, userAgent, modelName)
		globalErrorHandler.HandleAPIError(w, http.StatusBadGateway, "upstream_error", "Failed to call upstream after retries", "调用上游失败: %v", err)
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
				globalErrorHandler.HandleAPIError(w, http.StatusBadGateway, "upstream_error", "Upstream error", "上游返回错误状态: %d, 读取响应体失败: %v", resp.StatusCode, err)
			} else {
				globalErrorHandler.HandleAPIError(w, http.StatusBadGateway, "upstream_error", "Upstream error", "上游返回错误状态: %d, 响应: %s", resp.StatusCode, string(body))
			}
		} else {
			globalErrorHandler.HandleAPIError(w, http.StatusBadGateway, "upstream_error", "Upstream error", "上游返回错误状态: %d", resp.StatusCode)
		}
		return
	}

	setCORSHeaders(w) // 确保流式响应也包含CORS头
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		duration := float64(time.Since(startTime)) / float64(time.Millisecond)
		recordRequestStats(startTime, r.URL.Path, http.StatusInternalServerError, 0, modelName, true)
		addLiveRequest(r.Method, r.URL.Path, http.StatusInternalServerError, duration, userAgent, modelName)
		globalErrorHandler.HandleAPIError(w, http.StatusInternalServerError, "streaming_unsupported", "Streaming not supported by server", "Streaming不受支持")
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
	// 使用更健壮的扫描器方式，类似旧版本，但增加缓冲区限制
	scanner := bufio.NewScanner(resp.Body)
	// 优化缓冲区大小：初始512KB，最大5MB (降低内存占用)
	// 在100并发时，最多占用500MB而不是1GB
	scanner.Buffer(make([]byte, 0, 512*1024), 5*1024*1024)
	lineCount := 0
	var totalSize int64
	var lastUsage map[string]interface{}
	var sentInitialAnswer bool
	var sentFinish bool // 追踪是否已发送结束块

	// 创建工具调用处理器
	toolCallHandler := NewSSEToolCallHandler()

	for scanner.Scan() {
		line := scanner.Text()
		lineCount++

		// 检查累积大小
		totalSize += int64(len(line))
		if totalSize > MaxResponseSize {
			debugLog("流式响应大小超出限制 (%d > %d)，停止处理", totalSize, MaxResponseSize)
			break
		}

		// 检查客户端是否断开连接
		select {
		case <-r.Context().Done():
			debugLog("客户端断开连接，停止处理流")
			return
		default:
			// 继续处理
		}

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

				// 检查特定错误类型，如签名错误，需要刷新匿名token
				if strings.Contains(errObj.Detail, "Missing signature header") ||
					strings.Contains(errObj.Detail, "signature") ||
					errObj.Code == 400 {
					debugLog("检测到可能的token签名错误，标记token为失效")
					if tokenCache != nil {
						tokenCache.InvalidateToken()
					}
				}

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

				// 发送[DONE]信号
				fmt.Fprintf(w, "data: [DONE]\n\n")
				flusher.Flush()

				// 记录统计信息
				duration := float64(time.Since(startTime)) / float64(time.Millisecond)
				recordRequestStats(startTime, r.URL.Path, 200, 0, modelName, true)
				addLiveRequest(r.Method, r.URL.Path, 200, duration, userAgent, modelName)

				// 确保资源清理：defer中的cancel和Body.Close会自动执行
				// 但我们需要立即释放并发槽位
				debugLog("错误处理完成，释放资源")
				return
			}
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
			var parts = detailsCloseRegex.Split(out, -1)
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

		// 优先按Phase分流处理
		switch upstreamData.Data.Phase {
		case "tool_call":
			// 无论DeltaContent是否为空，都处理工具调用
			toolCalls, err := toolCallHandler.process(upstreamData.Data.EditIndex, upstreamData.Data.EditContent)
			if err == nil && len(toolCalls) > 0 {
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
		case "thinking":
			if upstreamData.Data.DeltaContent != "" {
				out := transformThinking(upstreamData.Data.DeltaContent)
				if out != "" {
					chunk := createSSEChunk(out, true, upstreamReq.Model)
					writeSSEChunk(w, chunk)
					flusher.Flush()
				}
			}
		default: // 包括 "answer" 和其他情况
			if upstreamData.Data.DeltaContent != "" {
				out := upstreamData.Data.DeltaContent
				if out != "" {
					chunk := createSSEChunk(out, false, upstreamReq.Model)
					writeSSEChunk(w, chunk)
					flusher.Flush()
				}
			}
		}

		if upstreamData.Data.Done || upstreamData.Data.Phase == "done" {
			// 检查是否有工具调用需要完成
			if len(toolCallHandler.GetToolCalls()) > 0 && !sentFinish {
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
				sentFinish = true
			}
			break
		}
	}

	if err := scanner.Err(); err != nil {
		debugLog("扫描器错误: %v", err)
	}

	// 根据是否有工具调用决定结束原因
	finishReason := "stop"
	if len(toolCallHandler.GetToolCalls()) > 0 {
		finishReason = "tool_calls"
	}

	if !sentFinish {
		endChunk := OpenAIResponse{
			ID:      fmt.Sprintf("chatcmpl-%d", time.Now().Unix()),
			Object:  "chat.completion.chunk",
			Created: time.Now().Unix(),
			Model:   upstreamReq.Model,
			Choices: []Choice{{Index: 0, Delta: Delta{}, FinishReason: finishReason}},
		}
		writeSSEChunk(w, endChunk)
		flusher.Flush()
		sentFinish = true
	}

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
		chunk.Choices[0].Delta = Delta{
			Content:          content, // 镜像到 content 以提高兼容性
			ReasoningContent: content,
		}
	} else {
		chunk.Choices[0].Delta = Delta{Content: content}
	}

	return chunk
}

func writeSSEChunk(w http.ResponseWriter, chunk OpenAIResponse) {
	// 使用对象池优化 SSE 块写入
	buf := sseBufferPool.Get().([]byte)
	defer func() {
		buf = buf[:0] // 重置长度但保留容量
		sseBufferPool.Put(buf)
	}()

	buf = append(buf, "data: "...)
	data, _ := json.Marshal(chunk)
	buf = append(buf, data...)
	buf = append(buf, "\n\n"...)

	w.Write(buf)
}

func handleNonStreamResponseWithIDs(w http.ResponseWriter, r *http.Request, upstreamReq UpstreamRequest, chatID string, authToken string, modelName string, startTime time.Time, sessionID string) {
	userAgent := r.Header.Get("User-Agent")
	debugLog("开始处理非流式响应 (chat_id=%s, model=%s)", chatID, upstreamReq.Model)

	resp, cancel, err := callUpstreamWithRetry(upstreamReq, chatID, authToken, sessionID)
	if err != nil {
		duration := float64(time.Since(startTime)) / float64(time.Millisecond)
		recordRequestStats(startTime, r.URL.Path, http.StatusBadGateway, 0, modelName, false)
		addLiveRequest(r.Method, r.URL.Path, http.StatusBadGateway, duration, userAgent, modelName)
		globalErrorHandler.HandleAPIError(w, http.StatusBadGateway, "upstream_error", "Failed to call upstream after retries", "调用上游失败: %v", err)
		return
	}

	// 确保在函数结束时取消上下文和关闭响应体
	defer func() {
		cancel()          // 取消上下文
		resp.Body.Close() // 关闭响应体
	}()

	if resp.StatusCode != http.StatusOK {
		duration := float64(time.Since(startTime)) / float64(time.Millisecond)
		recordRequestStats(startTime, r.URL.Path, http.StatusBadGateway, 0, modelName, false)
		addLiveRequest(r.Method, r.URL.Path, http.StatusBadGateway, duration, userAgent, modelName)
		if appConfig.DebugMode {
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				globalErrorHandler.HandleAPIError(w, http.StatusBadGateway, "upstream_error", "Upstream error", "上游返回错误状态: %d, 读取响应体失败: %v", resp.StatusCode, err)
			} else {
				globalErrorHandler.HandleAPIError(w, http.StatusBadGateway, "upstream_error", "Upstream error", "上游返回错误状态: %d, 响应: %s", resp.StatusCode, string(body))
			}
		} else {
			globalErrorHandler.HandleAPIError(w, http.StatusBadGateway, "upstream_error", "Upstream error", "上游返回错误状态: %d", resp.StatusCode)
		}
		return
	}

	var contentBuffer []byte
	toolCallHandler := NewSSEToolCallHandler() // 使用工具调用处理器以实现鲁棒的合并
	lastUsage := Usage{}

	debugLog("开始收集完整响应内容")

	// 使用更健壮的扫描器方式，类似旧版本和流式响应，但增加缓冲区限制
	scanner := bufio.NewScanner(resp.Body)
	// 优化缓冲区大小：初始512KB，最大5MB (与流式响应保持一致)
	scanner.Buffer(make([]byte, 0, 512*1024), 5*1024*1024)
	lineCount := 0
	var upstreamError *UpstreamError // 用于存储上游错误信息

	for scanner.Scan() {
		line := scanner.Text()
		lineCount++

		// 检查客户端是否断开连接
		select {
		case <-r.Context().Done():
			debugLog("客户端断开连接，停止处理非流式响应")
			return
		default:
			// 继续处理
		}

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

				// 检查特定错误类型，如签名错误，需要刷新匿名token
				if strings.Contains(upstreamError.Detail, "Missing signature header") ||
					strings.Contains(upstreamError.Detail, "signature") ||
					upstreamError.Code == 400 {
					debugLog("检测到可能的token签名错误，标记token为失效")
					if tokenCache != nil {
						tokenCache.InvalidateToken()
					}
				}

				// 设置错误标志，跳出循环
				break
			}
			continue
		}

		if upstreamData.Data.Usage.TotalTokens > 0 {
			lastUsage = upstreamData.Data.Usage
		}

		// 使用SSEToolCallHandler处理工具调用，以确保参数的最终状态
		if upstreamData.Data.Phase == "tool_call" && upstreamData.Data.EditContent != "" {
			toolCallHandler.process(upstreamData.Data.EditIndex, upstreamData.Data.EditContent)
		}

		// Capture content from multiple possible sources to ensure we get the response
		var contentToUse string
		var editIndex int = -1 // 默认值，表示追加到末尾
		if upstreamData.Data.DeltaContent != "" {
			contentToUse = upstreamData.Data.DeltaContent
			// DeltaContent should be appended to the end, so use the current buffer length as the index
			editIndex = len(contentBuffer)
			debugLog("从DeltaContent提取内容 (追加到索引 %d): %s", editIndex, contentToUse)
		} else if upstreamData.Data.EditContent != "" && upstreamData.Data.Phase == "answer" {
			// Process EditContent similar to streaming version
			var out = upstreamData.Data.EditContent
			var parts = detailsCloseRegex.Split(out, -1)
			if len(parts) > 1 {
				contentToUse = parts[1]
				editIndex = upstreamData.Data.EditIndex
				debugLog("从EditContent提取内容 (编辑索引 %d): %s", editIndex, contentToUse)
			} else {
				contentToUse = out
				editIndex = upstreamData.Data.EditIndex
				debugLog("从EditContent提取内容 (编辑索引 %d, 未分割): %s", editIndex, contentToUse)
			}
		}

		// Apply transformations based on phase
		if contentToUse != "" {
			finalContentToUse := contentToUse
			if upstreamData.Data.Phase == "thinking" {
				finalContentToUse = transformThinking(contentToUse)
			}
			if finalContentToUse != "" {
				// Use edit_index based approach for all content to maintain consistency
				editBytes := []byte(finalContentToUse)

				// 增强EditIndex边界验证
				if editIndex < 0 {
					debugLog("警告: EditIndex为负数(%d)，强制设置为追加模式", editIndex)
					editIndex = len(contentBuffer)
				}

				// 验证EditIndex是否过大(可能是上游错误)
				if editIndex > len(contentBuffer)+1024*1024 { // 允许1MB的合理跳跃
					debugLog("警告: EditIndex过大(%d > %d)，可能是上游错误，使用追加模式", editIndex, len(contentBuffer))
					editIndex = len(contentBuffer)
				}

				requiredLength := editIndex + len(editBytes)

				// 安全检查：防止内存分配过大
				if requiredLength > int(MaxResponseSize) {
					debugLog("EditIndex + content长度过大，跳过: index=%d, content_len=%d", editIndex, len(editBytes))
					continue
				}

				// 扩展缓冲区到所需长度
				if len(contentBuffer) < requiredLength {
					newBuffer := make([]byte, requiredLength)
					copy(newBuffer, contentBuffer)
					contentBuffer = newBuffer
				}

				// 在指定位置替换内容
				copy(contentBuffer[editIndex:], editBytes)
				debugLog("在索引 %d 更新内容: %s", editIndex, finalContentToUse)

				// 更频繁地检查响应大小是否超出限制
				if int64(len(contentBuffer)) > MaxResponseSize-int64(len(finalContentToUse)) {
					debugLog("响应大小即将超出限制，当前大小: %d, 将添加: %d, 限制: %d",
						len(contentBuffer), len(finalContentToUse), MaxResponseSize)
					// 可以选择截断或返回错误
					break
				}

				// 偶尔检查内存使用情况，避免单次循环占用过多内存
				if lineCount%50 == 0 { // 每50行检查一次
					if int64(len(contentBuffer)) > MaxResponseSize {
						debugLog("响应大小超出限制，当前大小: %d", len(contentBuffer))
						break
					}
				}
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

	finalContent := string(bytes.ReplaceAll(contentBuffer, []byte{0}, []byte{}))
	finalToolCalls := toolCallHandler.GetToolCalls()
	debugLog("内容收集完成，总共处理%d行，最终长度: %d, 工具调用数: %d", lineCount, len(finalContent), len(finalToolCalls))

	// 检查是否有错误发生
	if upstreamError != nil {
		// 使用统一的错误处理函数
		globalErrorHandler.HandleUpstreamError(w, upstreamError)
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
						Content:   finalContent,
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
