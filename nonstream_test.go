package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
	"z2api/config"

	"github.com/bytedance/sonic"
	"golang.org/x/sync/semaphore"
)

// TestNonStreamResponseAfterFix 专门测试修复后的非流式请求处理功能
func TestNonStreamResponseAfterFix(t *testing.T) {
	// 设置测试环境
	setupTestEnvironment(t)

	// 测试用例结构
	testCases := []struct {
		name           string
		requestBody    OpenAIRequest
		upstreamSSE    []string // 模拟的上游SSE响应
		expectedStatus int
		validateFunc   func(t *testing.T, resp OpenAIResponse)
	}{
		{
			name: "简单非流式请求 - 纯文本响应",
			requestBody: OpenAIRequest{
				Model:       "glm-4.5",
				Stream:      false, // 非流式请求
				Messages:    []Message{{Role: "user", Content: "Hello"}},
				Temperature: floatPtr(0.7),
				MaxTokens:   intPtr(100),
			},
			upstreamSSE: []string{
				`data: {"type":"stream","data":{"phase":"answer","delta_content":"Hello, ","done":false}}`,
				`data: {"type":"stream","data":{"phase":"answer","delta_content":"how can I help you?","done":false}}`,
				`data: {"type":"stream","data":{"phase":"done","done":true,"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}}`,
				`data: [DONE]`,
			},
			expectedStatus: http.StatusOK,
			validateFunc: func(t *testing.T, resp OpenAIResponse) {
				// 验证响应结构
				if len(resp.Choices) != 1 {
					t.Errorf("期望1个choice，得到 %d", len(resp.Choices))
				}
				if resp.Choices[0].Message.Content != "Hello, how can I help you?" {
					t.Errorf("内容不匹配: %s", resp.Choices[0].Message.Content)
				}
				if resp.Choices[0].FinishReason != "stop" {
					t.Errorf("完成原因不正确: %s", resp.Choices[0].FinishReason)
				}
				if resp.Usage.TotalTokens != 15 {
					t.Errorf("token计数不正确: %d", resp.Usage.TotalTokens)
				}
			},
		},
		{
			name: "非流式请求 - 包含思考内容",
			requestBody: OpenAIRequest{
				Model:  "glm-4.5-thinking",
				Stream: false,
				Messages: []Message{
					{Role: "user", Content: "Explain something"},
				},
			},
			upstreamSSE: []string{
				`data: {"type":"stream","data":{"phase":"thinking","delta_content":"<thinking>Let me think...</thinking>","done":false}}`,
				`data: {"type":"stream","data":{"phase":"answer","delta_content":"Here's the explanation","done":false}}`,
				`data: {"type":"stream","data":{"phase":"done","done":true,"usage":{"total_tokens":20}}}`,
				`data: [DONE]`,
			},
			expectedStatus: http.StatusOK,
			validateFunc: func(t *testing.T, resp OpenAIResponse) {
				if resp.Choices[0].Message.Content != "Here's the explanation" {
					t.Errorf("内容处理错误: %s", resp.Choices[0].Message.Content)
				}
				// 根据配置检查思考内容是否被正确处理
				if appConfig.ThinkTagsMode == "think" && !strings.Contains(resp.Choices[0].Message.ReasoningContent, "think") {
					t.Errorf("思考内容未正确处理")
				}
			},
		},
		{
			name: "非流式请求 - 工具调用",
			requestBody: OpenAIRequest{
				Model:  "glm-4.5",
				Stream: false,
				Messages: []Message{
					{Role: "user", Content: "Get weather"},
				},
				Tools: []Tool{
					{
						Type: "function",
						Function: ToolFunction{
							Name:        "get_weather",
							Description: "Get weather information",
							Parameters:  map[string]interface{}{"type": "object"},
						},
					},
				},
			},
			upstreamSSE: []string{
				`data: {"type":"stream","data":{"phase":"tool_call","tool_calls":[{"index":0,"id":"call_123","type":"function","function":{"name":"get_weather","arguments":"{\"location\":\"Beijing\"}"}}],"done":false}}`,
				`data: {"type":"stream","data":{"phase":"done","done":true,"usage":{"total_tokens":25}}}`,
				`data: [DONE]`,
			},
			expectedStatus: http.StatusOK,
			validateFunc: func(t *testing.T, resp OpenAIResponse) {
				if len(resp.Choices) == 0 || len(resp.Choices[0].Message.ToolCalls) == 0 {
					t.Error("工具调用未正确聚合")
					return
				}
				toolCall := resp.Choices[0].Message.ToolCalls[0]
				if toolCall.Function.Name != "get_weather" {
					t.Errorf("工具名称错误: %s", toolCall.Function.Name)
				}
				if resp.Choices[0].FinishReason != "tool_calls" {
					t.Errorf("完成原因应该是tool_calls，得到: %s", resp.Choices[0].FinishReason)
				}
			},
		},
		{
			name: "非流式请求 - 空响应处理",
			requestBody: OpenAIRequest{
				Model:  "glm-4.5",
				Stream: false,
				Messages: []Message{
					{Role: "user", Content: "Test empty"},
				},
			},
			upstreamSSE: []string{
				`data: {"type":"stream","data":{"phase":"done","done":true,"usage":{"total_tokens":5}}}`,
				`data: [DONE]`,
			},
			expectedStatus: http.StatusOK,
			validateFunc: func(t *testing.T, resp OpenAIResponse) {
				if resp.Choices[0].Message.Content != "" {
					t.Errorf("空响应应该返回空内容，得到: %s", resp.Choices[0].Message.Content)
				}
			},
		},
		{
			name: "非流式请求 - 错误处理",
			requestBody: OpenAIRequest{
				Model:  "glm-4.5",
				Stream: false,
				Messages: []Message{
					{Role: "user", Content: "Test error"},
				},
			},
			upstreamSSE: []string{
				`data: {"type":"stream","error":{"code":400,"detail":"Bad request"}}`,
			},
			expectedStatus: http.StatusInternalServerError, // 期望内部错误
			validateFunc:   nil,                             // 错误情况不验证响应内容
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// 创建模拟的上游服务器
			mockUpstream := createMockSSEServer(tc.upstreamSSE)
			defer mockUpstream.Close()

			// 更新配置以使用模拟服务器
			originalURL := appConfig.UpstreamUrl
			appConfig.UpstreamUrl = mockUpstream.URL
			defer func() { appConfig.UpstreamUrl = originalURL }()

			// 准备请求
			body, _ := sonic.Marshal(tc.requestBody)
			req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBuffer(body))
			req.Header.Set("Authorization", "Bearer "+appConfig.DefaultKey)
			req.Header.Set("Content-Type", "application/json")

			// 创建响应记录器
			rr := httptest.NewRecorder()

			// 调用处理函数
			handleChatCompletions(rr, req)

			// 验证状态码
			if rr.Code != tc.expectedStatus {
				t.Errorf("状态码错误: 得到 %v 期望 %v", rr.Code, tc.expectedStatus)
				t.Logf("响应体: %s", rr.Body.String())
				return
			}

			// 如果是成功响应，验证响应内容
			if tc.expectedStatus == http.StatusOK && tc.validateFunc != nil {
				var resp OpenAIResponse
				if err := sonic.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
					t.Fatalf("无法解析响应: %v", err)
				}
				tc.validateFunc(t, resp)
			}
		})
	}
}

// TestNonStreamWithTimeout 测试非流式请求的超时处理
func TestNonStreamWithTimeout(t *testing.T) {
	setupTestEnvironment(t)

	// 创建一个会延迟响应的模拟服务器
	mockUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		
		// 发送初始数据
		fmt.Fprintln(w, `data: {"type":"stream","data":{"phase":"answer","delta_content":"Starting...","done":false}}`)
		flusher.Flush()
		
		// 故意延迟，但不超过context超时
		time.Sleep(100 * time.Millisecond)
		
		// 发送剩余数据
		fmt.Fprintln(w, `data: {"type":"stream","data":{"phase":"answer","delta_content":" Complete!","done":false}}`)
		flusher.Flush()
		fmt.Fprintln(w, `data: {"type":"stream","data":{"phase":"done","done":true,"usage":{"total_tokens":10}}}`)
		flusher.Flush()
		fmt.Fprintln(w, `data: [DONE]`)
		flusher.Flush()
	}))
	defer mockUpstream.Close()

	appConfig.UpstreamUrl = mockUpstream.URL

	requestBody := OpenAIRequest{
		Model:  "glm-4.5",
		Stream: false,
		Messages: []Message{
			{Role: "user", Content: "Test timeout"},
		},
	}

	body, _ := sonic.Marshal(requestBody)
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBuffer(body))
	req.Header.Set("Authorization", "Bearer "+appConfig.DefaultKey)

	// 创建带超时的context
	ctx, cancel := context.WithTimeout(req.Context(), 5*time.Second)
	defer cancel()
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	handleChatCompletions(rr, req)

	// 应该成功完成
	if rr.Code != http.StatusOK {
		t.Errorf("应该成功完成，得到状态码: %d", rr.Code)
		t.Logf("响应: %s", rr.Body.String())
	}

	var resp OpenAIResponse
	if err := sonic.Unmarshal(rr.Body.Bytes(), &resp); err == nil {
		content, ok := resp.Choices[0].Message.Content.(string)
		if !ok {
			t.Errorf("内容不是字符串类型")
		} else if !strings.Contains(content, "Starting... Complete!") {
			t.Errorf("内容未正确聚合: %s", content)
		}
	}
}

// TestNonStreamLargeResponse 测试大响应的处理
func TestNonStreamLargeResponse(t *testing.T) {
	setupTestEnvironment(t)

	// 生成大量的SSE数据
	var sseData []string
	totalChunks := 100
	for i := 0; i < totalChunks; i++ {
		chunk := fmt.Sprintf(`data: {"type":"stream","data":{"phase":"answer","delta_content":"Chunk %d. ","done":false}}`, i)
		sseData = append(sseData, chunk)
	}
	sseData = append(sseData, `data: {"type":"stream","data":{"phase":"done","done":true,"usage":{"total_tokens":500}}}`)
	sseData = append(sseData, `data: [DONE]`)

	mockUpstream := createMockSSEServer(sseData)
	defer mockUpstream.Close()

	appConfig.UpstreamUrl = mockUpstream.URL

	requestBody := OpenAIRequest{
		Model:  "glm-4.5",
		Stream: false,
		Messages: []Message{
			{Role: "user", Content: "Test large response"},
		},
	}

	body, _ := sonic.Marshal(requestBody)
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBuffer(body))
	req.Header.Set("Authorization", "Bearer "+appConfig.DefaultKey)

	rr := httptest.NewRecorder()
	handleChatCompletions(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("大响应处理失败: %d", rr.Code)
		return
	}

	var resp OpenAIResponse
	if err := sonic.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("无法解析响应: %v", err)
	}

	// 验证所有块都被正确聚合
	for i := 0; i < totalChunks; i++ {
		expectedChunk := fmt.Sprintf("Chunk %d. ", i)
		content, ok := resp.Choices[0].Message.Content.(string)
		if !ok {
			t.Errorf("内容不是字符串类型")
			break
		}
		if !strings.Contains(content, expectedChunk) {
			t.Errorf("缺少块 %d", i)
			break
		}
	}
}

// TestStreamVsNonStreamConsistency 测试流式和非流式响应的一致性
func TestStreamVsNonStreamConsistency(t *testing.T) {
	setupTestEnvironment(t)

	sseData := []string{
		`data: {"type":"stream","data":{"phase":"answer","delta_content":"Response ","done":false}}`,
		`data: {"type":"stream","data":{"phase":"answer","delta_content":"content","done":false}}`,
		`data: {"type":"stream","data":{"phase":"done","done":true,"usage":{"total_tokens":10}}}`,
		`data: [DONE]`,
	}

	// 测试非流式
	t.Run("NonStream", func(t *testing.T) {
		mockUpstream := createMockSSEServer(sseData)
		defer mockUpstream.Close()
		appConfig.UpstreamUrl = mockUpstream.URL

		requestBody := OpenAIRequest{
			Model:  "glm-4.5",
			Stream: false,
			Messages: []Message{{Role: "user", Content: "Test"}},
		}

		body, _ := sonic.Marshal(requestBody)
		req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBuffer(body))
		req.Header.Set("Authorization", "Bearer "+appConfig.DefaultKey)

		rr := httptest.NewRecorder()
		handleChatCompletions(rr, req)

		var resp OpenAIResponse
		if err := sonic.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
			t.Fatalf("解析失败: %v", err)
		}

		if resp.Choices[0].Message.Content != "Response content" {
			t.Errorf("非流式内容错误: %s", resp.Choices[0].Message.Content)
		}
	})

	// 测试流式 - 聚合所有块
	t.Run("Stream", func(t *testing.T) {
		mockUpstream := createMockSSEServer(sseData)
		defer mockUpstream.Close()
		appConfig.UpstreamUrl = mockUpstream.URL

		requestBody := OpenAIRequest{
			Model:  "glm-4.5",
			Stream: true,
			Messages: []Message{{Role: "user", Content: "Test"}},
		}

		body, _ := sonic.Marshal(requestBody)
		req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBuffer(body))
		req.Header.Set("Authorization", "Bearer "+appConfig.DefaultKey)

		rr := httptest.NewRecorder()
		handleChatCompletions(rr, req)

		// 聚合流式响应的所有内容
		aggregatedContent := ""
		lines := strings.Split(rr.Body.String(), "\n")
		for _, line := range lines {
			if strings.HasPrefix(line, "data: ") && !strings.Contains(line, "[DONE]") {
				dataStr := strings.TrimPrefix(line, "data: ")
				var chunk OpenAIResponse
				if err := sonic.UnmarshalString(dataStr, &chunk); err == nil {
					if len(chunk.Choices) > 0 && chunk.Choices[0].Delta.Content != "" {
						aggregatedContent += chunk.Choices[0].Delta.Content
					}
				}
			}
		}

		if aggregatedContent != "Response content" {
			t.Errorf("流式内容错误: %s", aggregatedContent)
		}
	})
}

// 辅助函数

func setupTestEnvironment(t *testing.T) {
	// 设置环境变量
	os.Setenv("UPSTREAM_TOKEN", "test-token")
	os.Setenv("API_KEY", "test-key")
	os.Setenv("PORT", "8080")
	os.Setenv("DEBUG_MODE", "false") // 测试时关闭调试输出
	os.Setenv("THINK_TAGS_MODE", "strip")
	os.Setenv("ANON_TOKEN_ENABLED", "false")
	os.Setenv("MAX_CONCURRENT_REQUESTS", "10")

	// 加载配置
	if appConfig == nil {
		var err error
		appConfig, err = loadConfig()
		if err != nil {
			t.Fatalf("加载配置失败: %v", err)
		}
	}

	// 加载模型配置
	if err := config.LoadModels("models.json"); err != nil {
		t.Logf("警告: 无法加载models.json: %v", err)
	}

	// 初始化并发限制器
	if concurrencyLimiter == nil {
		concurrencyLimiter = semaphore.NewWeighted(int64(appConfig.MaxConcurrentRequests))
	}

	// 初始化错误处理器
	if globalErrorHandler == nil {
		globalErrorHandler = NewErrorHandler(appConfig.DebugMode)
	}
}

func createMockSSEServer(sseData []string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 验证请求
		var upstreamReq UpstreamRequest
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &upstreamReq); err != nil {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}

		// 确认是流式请求（修复后总是true）
		if !upstreamReq.Stream {
			// 不能在这里使用t，因为这是http.HandlerFunc的上下文
			// 使用标准日志
			log.Printf("警告: 收到非流式的上游请求，这不应该发生在修复后")
		}

		// 发送SSE响应
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)

		flusher, ok := w.(http.Flusher)
		if !ok {
			return
		}

		for _, data := range sseData {
			fmt.Fprintln(w, data)
			fmt.Fprintln(w) // 空行
			flusher.Flush()
			time.Sleep(10 * time.Millisecond) // 模拟流式延迟
		}
	}))
}

func floatPtr(f float64) *float64 {
	return &f
}

func intPtr(i int) *int {
	return &i
}