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
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/bytedance/sonic"

	// 内部包
	"z2api/config"
	"z2api/internal/signature"
	"z2api/types"

	// 第三方包
	"z2api/utils"

	"github.com/andybalholm/brotli"
	"golang.org/x/sync/semaphore"
	"golang.org/x/sync/singleflight"
)

// 优化的 sonic 配置 - 减少配置数量，提高维护性
var (
	// sonicInternal - 用于内部数据处理和流式响应
	// 合并原来的 sonicStream 和 sonicFast，适用于所有内部处理场景
	sonicInternal = sonic.Config{
		EscapeHTML:            false, // 不需要HTML转义
		SortMapKeys:           false, // 不排序键，提高性能
		CompactMarshaler:      true,  // 紧凑输出
		CopyString:            false, // 避免字符串复制
		UseInt64:              false, // 使用更快的整数处理
		UseNumber:             false, // 直接使用数字类型
		DisallowUnknownFields: false, // 忽略未知字段
		NoQuoteTextMarshaler:  true,  // 跳过文本引号处理
	}.Froze()

	// sonicDefault 作为别名，保持向后兼容
	sonicDefault = sonic.ConfigDefault
	// 为了兼容性，保留旧名称的别名
	sonicStream = sonicInternal // 流式处理使用内部配置
	sonicFast   = sonicInternal // 快速处理使用内部配置
)

// Config 配置结构体

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
func loadConfig() (*types.Config, error) {
	maxConcurrent := 100 // 默认值
	if envVal := getEnv("MAX_CONCURRENT_REQUESTS", "100"); envVal != "100" {
		if parsed, err := strconv.Atoi(envVal); err == nil && parsed > 0 && parsed <= 1000 {
			maxConcurrent = parsed
		}
	}

	port := ":" + strings.TrimPrefix(getEnv("PORT", DefaultPort), ":")

	config := &types.Config{
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
	if err := validateConfig(config); err != nil {
		return nil, err
	}

	return config, nil
}

// validateConfig 验证配置合法性，增强端口范围检查
func validateConfig(c *types.Config) error {
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
	MaxResponseSize   int64 = DefaultMaxTokens // 10MB
)

// 全局配置和缓存实例
var (
	appConfig  *types.Config
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

// LiveRequest 实时请求结构

// 全局统计实例
var (
	stats             *types.RequestStats
	liveRequests      []types.LiveRequest
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
	summaryRegex = regexp.MustCompile(`(?s)<summary>.*?</summary>`)
	detailsRegex = regexp.MustCompile(`<details[^>]*>`)

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

// ToolCall 工具调用结构 - 符合OpenAI API标准

// normalizeToolCall 规范化工具调用，确保符合OpenAI API标准
// 为缺失的必需字段生成默认值
func normalizeToolCall(tc types.ToolCall) types.ToolCall {
	// 确保有ID，如果没有则生成
	if tc.ID == "" {
		tc.ID = utils.GenerateToolCallID()
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
func normalizeToolCalls(calls []types.ToolCall) []types.ToolCall {
	if len(calls) == 0 {
		return calls
	}

	normalized := make([]types.ToolCall, len(calls))
	for i, call := range calls {
		normalized[i] = normalizeToolCall(call)
	}
	return normalized
}

// ToolFunction 工具函数结构

// ToolChoiceFunction 工具选择函数结构

// ToolChoice 工具选择结构

// Tool 工具结构

// OpenAIRequest OpenAI 请求结构

// ImageURL 图像URL结构

// VideoURL 视频URL结构

// DocumentURL 文档URL结构

// AudioURL 音频URL结构

// ContentPart 内容部分结构（用于多模态消息）

// Message 消息结构（支持多模态内容）

// UpstreamMessage 上游消息结构（简化格式，仅支持字符串内容）

// UpstreamRequest 上游请求结构

// OpenAIResponse OpenAI 响应结构

// Choice 选择结构

// Delta 增量结构

// Usage 用量结构

// UpstreamData 上游SSE响应结构

// UpstreamError 上游错误结构

// ModelsResponse 模型列表响应

// Model 模型结构

// extractTextContent 从多模态内容中提取文本（使用统一的多模态处理器）
func extractTextContent(content interface{}) string {
	processor := utils.NewMultimodalProcessor("")
	return processor.ExtractText(content)
}

// extractLastUserContent 从 UpstreamRequest 中提取最后一条用户消息的内容
func extractLastUserContent(req types.UpstreamRequest) string {
	// 从后往前遍历消息，找到最后一条 role 为 "user" 的消息
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == "user" {
			return req.Messages[i].Content
		}
	}
	// 如果没有找到用户消息，返回空字符串
	return ""
}

// processMultimodalContent 处理全方位多模态内容，支持图像、视频、文档、音频等（使用统一的多模态处理器）
func processMultimodalContent(parts []types.ContentPart, model string) string {
	processor := utils.NewMultimodalProcessor(model)
	processor.EnableDebugLog = appConfig.DebugMode
	return processor.ExtractText(parts)
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

// parseToolChoice 解析ToolChoice参数，支持多种格式
func parseToolChoice(toolChoice interface{}) *types.ToolChoice {
	if toolChoice == nil {
		return nil
	}

	// 检查是否是字符串格式 ("none", "auto", "required")
	if choiceStr, ok := toolChoice.(string); ok {
		return &types.ToolChoice{
			Type: choiceStr,
		}
	}

	// 检查是否是对象格式
	if choiceMap, ok := toolChoice.(map[string]interface{}); ok {
		toolChoiceObj := &types.ToolChoice{}

		if choiceType, exists := choiceMap["type"]; exists {
			if typeStr, ok := choiceType.(string); ok {
				toolChoiceObj.Type = typeStr
			}
		}

		if function, exists := choiceMap["function"]; exists {
			if funcMap, ok := function.(map[string]interface{}); ok {
				toolChoiceObj.Function = &types.ToolChoiceFunction{}
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
func buildUpstreamParams(req types.OpenAIRequest) map[string]interface{} {
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

// debugLog 兼容旧的调试日志函数，现在使用 utils 包的日志系统
func debugLog(format string, args ...interface{}) {
	// 转换为 utils.LogDebug 需要的键值对格式
	utils.LogDebug(fmt.Sprintf(format, args...))
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
	requestChan chan types.StatsUpdate
	quit        chan struct{}
	wg          sync.WaitGroup
}

// StatsUpdate 统计更新数据

// NewStatsCollector 创建新的统计收集器
func NewStatsCollector(bufferSize int) *StatsCollector {
	sc := &StatsCollector{
		requestChan: make(chan types.StatsUpdate, bufferSize),
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
		batchUpdates := make([]types.StatsUpdate, 0, 10) // 批量处理，最多10个
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
func (sc *StatsCollector) processBatch(updates []types.StatsUpdate) {
	if stats == nil {
		return
	}

	stats.Mutex.Lock()
	defer stats.Mutex.Unlock()

	for _, update := range updates {
		// 更新基本统计
		stats.TotalRequests++
		stats.LastRequestTime = time.Now()

		if update.Status >= 200 && update.Status < 300 {
			stats.SuccessfulRequests++
		} else {
			stats.FailedRequests++
		}

		// 更新端点统计
		switch update.Path {
		case "/v1/chat/completions":
			stats.ApiCallsCount++
		case "/v1/models":
			stats.ModelsCallsCount++
		case "/":
			stats.HomePageViews++
		}

		// 更新token统计
		if update.Tokens > 0 {
			stats.TotalTokensUsed += update.Tokens
		}

		// 更新模型使用统计
		if update.Model != "" {
			stats.ModelUsage[update.Model]++
		}

		// 更新流式统计
		if update.IsStreaming {
			stats.StreamingRequests++
		} else {
			stats.NonStreamingRequests++
		}

		// 更新响应时间统计
		if update.Duration < stats.FastestResponse {
			stats.FastestResponse = update.Duration
		}
		if update.Duration > stats.SlowestResponse {
			stats.SlowestResponse = update.Duration
		}

		// 更新平均响应时间
		totalDuration := stats.AverageResponseTime*float64(stats.TotalRequests-1) + update.Duration
		stats.AverageResponseTime = totalDuration / float64(stats.TotalRequests)

		// 添加实时请求记录（限制数量）
		if len(liveRequests) >= 100 {
			// 移除最旧的请求（简单的滑动窗口）
			liveRequests = liveRequests[1:]
		}
		liveRequests = append(liveRequests, types.LiveRequest{
			ID:        utils.GenerateRequestID(),
			Timestamp: time.Now(),
			Method:    update.Method,
			Path:      update.Path,
			Status:    update.Status,
			Duration:  update.Duration,
			UserAgent: update.UserAgent,
			Model:     update.Model,
		})
	}
}

// Record 记录统计更新
func (sc *StatsCollector) Record(update types.StatsUpdate) {
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
		statsCollector.Record(types.StatsUpdate{
			StartTime:   startTime,
			Path:        path,
			Status:      status,
			Tokens:      tokens,
			Model:       model,
			IsStreaming: isStreaming,
			Duration:    duration,
		})
	}
}

// addLiveRequest 异步添加实时请求记录
func addLiveRequest(method string, path string, status int, duration float64, userAgent string, model string) {
	if statsCollector != nil {
		statsCollector.Record(types.StatsUpdate{
			Path:      path,
			Status:    status,
			Model:     model,
			Duration:  duration,
			UserAgent: userAgent,
			Method:    method,
		})
	}
}

// StatsManager 统计数据管理器，提供线程安全的统计操作
type StatsManager struct {
	stats *types.RequestStats
	mutex sync.RWMutex
}

// NewStatsManager 创建新的统计管理器
func NewStatsManager() *StatsManager {
	return &StatsManager{
		stats: &types.RequestStats{
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
func (sm *StatsManager) GetStats() *types.RequestStats {
	sm.mutex.RLock()
	defer sm.mutex.RUnlock()

	if sm.stats == nil {
		return &types.RequestStats{
			StartTime:  time.Now(),
			ModelUsage: make(map[string]int64),
		}
	}

	// 创建数据副本以避免外部修改
	statsCopy := &types.RequestStats{
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
func validateAndSanitizeInput(req *types.OpenAIRequest) error {
	// 验证消息数量
	if len(req.Messages) == 0 {
		return fmt.Errorf("messages不能为空")
	}
	if len(req.Messages) > MaxMessagesPerRequest { // 限制消息数量
		return fmt.Errorf("消息数量过多，最多支持%d条消息", MaxMessagesPerRequest)
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
		if len(contentText) > MaxContentLength { // 单条消息最大500KB
			return fmt.Errorf("单条消息内容过长，最大支持%d字节", MaxContentLength)
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

// ErrorDetail 错误详情

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


// main is the entry point of the application
func main() {
	// 加载和验证配置（需要先加载配置，以便知道是否是调试模式）
	var err error
	appConfig, err = loadConfig()
	if err != nil {
		log.Fatalf("配置加载失败: %v", err)
	}

	// 初始化日志系统（必须在任何日志调用之前）
	utils.InitLogger(appConfig.DebugMode)
	utils.LogInfo("启动 OpenAI 兼容 API 服务器", "version", "1.0.0")

	// 加载仪表板HTML
	dashboardHTML, err = loadDashboardHTML()
	if err != nil {
		utils.LogWarn("无法加载仪表板文件", "file", "dashboard.html", "error", err)
		// 如果无法加载dashboard.html，使用一个简单的默认HTML
		dashboardHTML = `<html><body><h1>Dashboard Unavailable</h1><p>Dashboard HTML file not found.</p></body></html>`
	}

	// 加载模型配置
	if err := config.LoadModels("assets/models.json"); err != nil {
		utils.LogError("无法加载模型配置文件", "file", "assets/models.json", "error", err)
		log.Fatalf("错误: 无法加载模型配置文件 'assets/models.json': %v", err)
	}

	// 加载浏览器指纹配置
	if err := config.LoadFingerprints("assets/fingerprints.json"); err != nil {
		utils.LogWarn("无法加载浏览器指纹文件", "file", "assets/fingerprints.json", "error", err)
	}

	// 初始化Token缓存
	tokenCache = &TokenCache{}

	// 初始化统计信息
	stats = &types.RequestStats{
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
	utils.LogInfo("初始化 Gin 路由", "handler", "Gin原生")

	router := setupRouter()

	// 初始化全局错误处理器

	utils.LogInfo("服务器配置",
		"port", appConfig.Port,
		"model", DefaultModelName,
		"upstream", appConfig.UpstreamUrl,
		"debug", appConfig.DebugMode,
		"anon_token", appConfig.AnonTokenEnabled,
		"think_tags", appConfig.ThinkTagsMode,
		"concurrency", appConfig.MaxConcurrentRequests,
		"health_endpoint", fmt.Sprintf("http://localhost%s/health", appConfig.Port),
		"dashboard_endpoint", fmt.Sprintf("http://localhost%s/dashboard", appConfig.Port))

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
		utils.LogInfo("收到关闭信号，开始优雅关闭服务器")

		// 停止统计收集器
		if statsCollector != nil {
			statsCollector.Stop()
		}

		// 设置关闭超时
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer shutdownCancel()

		if err := server.Shutdown(shutdownCtx); err != nil {
			utils.LogError("服务器关闭时出现错误", "error", err)
		} else {
			utils.LogInfo("服务器已优雅关闭")
		}
	}()

	if err := server.ListenAndServe(); err != nil {
		utils.LogError("服务器启动失败", "error", err)
		log.Fatal(err)
	}
}

// callUpstreamWithHeaders 调用上游API
// 优化：使用 sonic 对象池进行序列化
func callUpstreamWithHeaders(upstreamReq types.UpstreamRequest, refererChatID string, authToken string, sessionID string) (*http.Response, context.CancelFunc, error) {
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
	requestID := utils.GenerateRequestID()
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
func callUpstreamWithRetry(upstreamReq types.UpstreamRequest, chatID string, authToken string, sessionID string) (*http.Response, context.CancelFunc, error) {
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
