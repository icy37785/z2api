package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"z2api/config"

	"golang.org/x/sync/semaphore"

	"github.com/bytedance/sonic"
)

func TestMain(m *testing.M) {
	// 设置必要的环境变量
	os.Setenv("UPSTREAM_TOKEN", "test-token")
	os.Setenv("API_KEY", "test-key")
	os.Setenv("PORT", "8080")
	os.Setenv("DEBUG_MODE", "true")
	os.Setenv("THINK_TAGS_MODE", "strip")
	os.Setenv("ANON_TOKEN_ENABLED", "false")
	os.Setenv("MAX_CONCURRENT_REQUESTS", "100")

	// 运行测试
	exitCode := m.Run()
	os.Exit(exitCode)
}

func TestChatCompletions(t *testing.T) {
	// 创建一个模拟的上游服务器，修改为使用新的SSE格式
	mockUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 验证上游请求总是流式的（修复后的关键特性）
		var upstreamReq UpstreamRequest
		body, _ := io.ReadAll(r.Body)
		if err := sonic.Unmarshal(body, &upstreamReq); err == nil {
			if !upstreamReq.Stream {
				t.Logf("警告: 上游请求不是流式的，这在修复后不应该发生")
			}
		}
		
		w.Header().Set("Content-Type", "text/event-stream")
		// 使用新的上游SSE格式
		fmt.Fprintln(w, `data: {"type":"stream","data":{"phase":"answer","delta_content":"Hello","done":false}}`)
		fmt.Fprintln(w, "")
		fmt.Fprintln(w, `data: {"type":"stream","data":{"phase":"done","done":true,"usage":{"prompt_tokens":5,"completion_tokens":1,"total_tokens":6}}}`)
		fmt.Fprintln(w, "")
		fmt.Fprintln(w, "data: [DONE]")
		fmt.Fprintln(w, "")
	}))
	defer mockUpstream.Close()

	// Setup
	os.Setenv("UPSTREAM_URL", mockUpstream.URL)
	if err := config.LoadModels("models.json"); err != nil {
		t.Fatalf("failed to load models.json: %v", err)
	}
	if err := config.LoadFingerprints("fingerprints.json"); err != nil {
		t.Logf("warning: failed to load fingerprints.json: %v", err)
	}
	var err error
	appConfig, err = loadConfig()
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}
	concurrencyLimiter = semaphore.NewWeighted(int64(appConfig.MaxConcurrentRequests))

	// ... (rest of the test cases are the same)
	testCases := []struct {
		name           string
		requestBody    OpenAIRequest
		expectedStatus int
	}{
		{
			name: "Basic Non-Stream Request (修复后应正常工作)",
			requestBody: OpenAIRequest{
				Model:  "glm-4.5",
				Stream: false, // 明确测试非流式请求
				Messages: []Message{
					{Role: "user", Content: "Hello"},
				},
			},
			expectedStatus: http.StatusOK,
		},
		{
			name: "Streaming Request",
			requestBody: OpenAIRequest{
				Model: "glm-4.5",
				Messages: []Message{
					{Role: "user", Content: "Hello"},
				},
				Stream: true,
			},
			expectedStatus: http.StatusOK,
		},
		{
			name: "Tool Choice Request",
			requestBody: OpenAIRequest{
				Model: "glm-4.5",
				Messages: []Message{
					{Role: "user", Content: "What's the weather like in Boston?"},
				},
				Tools: []Tool{
					{
						Type: "function",
						Function: ToolFunction{
							Name:        "get_current_weather",
							Description: "Get the current weather in a given location",
							Parameters: map[string]interface{}{
								"type": "object",
								"properties": map[string]interface{}{
									"location": map[string]interface{}{
										"type":        "string",
										"description": "The city and state, e.g. San Francisco, CA",
									},
									"unit": map[string]interface{}{
										"type": "string",
										"enum": []string{"celsius", "fahrenheit"},
									},
								},
								"required": []string{"location"},
							},
						},
					},
				},
				ToolChoice: "auto",
			},
			expectedStatus: http.StatusOK,
		},
		{
			name: "Multimodal Request",
			requestBody: OpenAIRequest{
				Model: "glm-4.5v",
				Messages: []Message{
					{
						Role: "user",
						Content: []ContentPart{
							{Type: "text", Text: "What's in this image?"},
							{
								Type: "image_url",
								ImageURL: &ImageURL{
									URL: "data:image/jpeg;base64,...",
								},
							},
						},
					},
				},
			},
			expectedStatus: http.StatusOK,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := sonic.Marshal(tc.requestBody)
			req, _ := http.NewRequest("POST", "/v1/chat/completions", bytes.NewBuffer(body))
			req.Header.Set("Authorization", "Bearer "+appConfig.DefaultKey)
			rr := httptest.NewRecorder()
			handler := http.HandlerFunc(handleChatCompletions)
			handler.ServeHTTP(rr, req)

			if status := rr.Code; status != tc.expectedStatus {
				t.Errorf("handler returned wrong status code: got %v want %v",
					status, tc.expectedStatus)
			}
		})
	}
}

func TestToolChoiceParsing(t *testing.T) {
	// Test string format
	choice1 := parseToolChoice("auto")
	if choice1 == nil || choice1.Type != "auto" {
		t.Errorf("Expected 'auto', got %+v", choice1)
	}

	// Test object format
	choice2 := parseToolChoice(map[string]interface{}{
		"type": "function",
		"function": map[string]interface{}{
			"name": "get_current_weather",
		},
	})
	if choice2 == nil || choice2.Type != "function" || choice2.Function.Name != "get_current_weather" {
		t.Errorf("Expected function with name get_current_weather, got %+v", choice2)
	}

	// Test nil input
	choice3 := parseToolChoice(nil)
	if choice3 != nil {
		t.Errorf("Expected nil, got %+v", choice3)
	}
}

func TestExtractTextContent(t *testing.T) {
	// Test string input
	result1 := extractTextContent("Hello World")
	if result1 != "Hello World" {
		t.Errorf("Expected 'Hello World', got '%s'", result1)
	}

	// Test []ContentPart input
	contentParts := []ContentPart{
		{Type: "text", Text: "Hello"},
		{Type: "text", Text: "World"},
	}
	result2 := extractTextContent(contentParts)
	if result2 != "Hello World " {
		t.Errorf("Expected 'Hello World ', got '%s'", result2)
	}
}
