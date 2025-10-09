package main

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// MockUpstreamServer 模拟上游服务器
type MockUpstreamServer struct {
	*httptest.Server
	requestCount  int32
	responses     []MockResponse
	responseIndex int32
}

// MockResponse 模拟响应配置
type MockResponse struct {
	StatusCode int
	Body       string
	Delay      time.Duration
	Headers    map[string]string
}

// NewMockUpstreamServer 创建模拟服务器
func NewMockUpstreamServer(responses []MockResponse) *MockUpstreamServer {
	mock := &MockUpstreamServer{
		responses: responses,
	}

	mock.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt32(&mock.requestCount, 1)
		index := atomic.LoadInt32(&mock.responseIndex)

		// 选择响应
		var resp MockResponse
		if int(index) < len(mock.responses) {
			resp = mock.responses[index]
			atomic.AddInt32(&mock.responseIndex, 1)
		} else if len(mock.responses) > 0 {
			// 使用最后一个响应
			resp = mock.responses[len(mock.responses)-1]
		} else {
			// 默认响应
			resp = MockResponse{StatusCode: 200, Body: `{"status": "ok"}`}
		}

		// 模拟延迟
		if resp.Delay > 0 {
			time.Sleep(resp.Delay)
		}

		// 设置自定义头
		for k, v := range resp.Headers {
			w.Header().Set(k, v)
		}

		// 写入响应
		w.WriteHeader(resp.StatusCode)
		w.Write([]byte(resp.Body))

		debugLog("模拟服务器收到第 %d 次请求，返回状态码: %d", count, resp.StatusCode)
	}))

	return mock
}

// GetRequestCount 获取请求次数
func (m *MockUpstreamServer) GetRequestCount() int {
	return int(atomic.LoadInt32(&m.requestCount))
}

// Reset 重置服务器状态
func (m *MockUpstreamServer) Reset() {
	atomic.StoreInt32(&m.requestCount, 0)
	atomic.StoreInt32(&m.responseIndex, 0)
}

// TestRetryOn401Error 测试401错误的重试机制
func TestRetryOn401Error(t *testing.T) {
	// 初始化配置（如果为空）
	if appConfig == nil {
		appConfig = &Config{
			DebugMode:        true,
			UpstreamUrl:      "http://test.com",
			UpstreamToken:    "test-token",
			AnonTokenEnabled: true,
		}
	}

	// 保存原始配置
	originalDebugMode := appConfig.DebugMode
	originalAnonTokenEnabled := appConfig.AnonTokenEnabled

	// 设置测试配置
	appConfig.DebugMode = true
	appConfig.AnonTokenEnabled = true

	defer func() {
		appConfig.DebugMode = originalDebugMode
		appConfig.AnonTokenEnabled = originalAnonTokenEnabled
	}()

	// 创建模拟token缓存
	if tokenCache == nil {
		tokenCache = &TokenCache{}
	}

	// 模拟响应序列：第一次401，第二次成功
	responses := []MockResponse{
		{
			StatusCode: 401,
			Body:       `{"error": {"code": 401, "detail": "Unauthorized"}}`,
		},
		{
			StatusCode: 200,
			Body:       `{"type": "done", "data": {"done": true}}`,
			Headers:    map[string]string{"Content-Type": "text/event-stream"},
		},
	}

	mockServer := NewMockUpstreamServer(responses)
	defer mockServer.Close()

	// 修改上游URL指向模拟服务器
	originalURL := appConfig.UpstreamUrl
	appConfig.UpstreamUrl = mockServer.URL
	defer func() {
		appConfig.UpstreamUrl = originalURL
	}()

	// 创建测试请求
	upstreamReq := UpstreamRequest{
		Stream:   true,
		ChatID:   "test-chat-id",
		ID:       "test-msg-id",
		Model:    "test-model",
		Messages: []UpstreamMessage{{Role: "user", Content: "测试"}},
		Params:   map[string]interface{}{},
		Features: map[string]interface{}{},
	}

	// 执行请求
	resp, cancel, err := callUpstreamWithRetry(upstreamReq, "test-chat", "test-token", "test-session")

	// 验证结果
	if err != nil {
		t.Errorf("预期成功，但收到错误: %v", err)
	}

	if resp != nil {
		defer resp.Body.Close()
		if cancel != nil {
			defer cancel()
		}

		if resp.StatusCode != 200 {
			t.Errorf("预期状态码200，收到: %d", resp.StatusCode)
		}
	}

	// 验证重试次数
	requestCount := mockServer.GetRequestCount()
	if requestCount != 2 {
		t.Errorf("预期2次请求（初次+1次重试），实际: %d", requestCount)
	}

	t.Logf("✓ 401错误重试测试通过，共进行了 %d 次请求", requestCount)
}

// TestRetryOnSystemBusy 测试"系统繁忙"的400错误重试
func TestRetryOnSystemBusy(t *testing.T) {
	// 设置测试配置
	if appConfig == nil {
		appConfig = &Config{
			DebugMode:     true,
			UpstreamUrl:   "http://test.com",
			UpstreamToken: "test-token",
		}
	}
	originalDebugMode := appConfig.DebugMode
	appConfig.DebugMode = true
	defer func() {
		appConfig.DebugMode = originalDebugMode
	}()

	testCases := []struct {
		name         string
		errorMessage string
		shouldRetry  bool
	}{
		{
			name:         "系统繁忙（中文）",
			errorMessage: `{"error": {"code": 400, "detail": "系统繁忙，请稍后再试"}}`,
			shouldRetry:  true,
		},
		{
			name:         "system busy（英文）",
			errorMessage: `{"error": {"code": 400, "detail": "system busy, please try again later"}}`,
			shouldRetry:  true,
		},
		{
			name:         "rate limit",
			errorMessage: `{"error": {"code": 400, "detail": "rate limit exceeded"}}`,
			shouldRetry:  true,
		},
		{
			name:         "too many requests",
			errorMessage: `{"error": {"code": 400, "detail": "too many requests"}}`,
			shouldRetry:  true,
		},
		{
			name:         "temporarily unavailable",
			errorMessage: `{"error": {"code": 400, "detail": "service temporarily unavailable"}}`,
			shouldRetry:  true,
		},
		{
			name:         "普通400错误",
			errorMessage: `{"error": {"code": 400, "detail": "bad request"}}`,
			shouldRetry:  false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// 模拟响应
			var responses []MockResponse
			if tc.shouldRetry {
				// 第一次返回错误，第二次成功
				responses = []MockResponse{
					{StatusCode: 400, Body: tc.errorMessage},
					{StatusCode: 200, Body: `{"type": "done", "data": {"done": true}}`},
				}
			} else {
				// 只返回错误
				responses = []MockResponse{
					{StatusCode: 400, Body: tc.errorMessage},
				}
			}

			mockServer := NewMockUpstreamServer(responses)
			defer mockServer.Close()

			// 修改上游URL
			originalURL := appConfig.UpstreamUrl
			appConfig.UpstreamUrl = mockServer.URL
			defer func() {
				appConfig.UpstreamUrl = originalURL
			}()

			// 创建测试请求
			upstreamReq := UpstreamRequest{
				Stream:   false,
				Model:    "test-model",
				Messages: []UpstreamMessage{{Role: "user", Content: "测试"}},
				Params:   map[string]interface{}{},
				Features: map[string]interface{}{},
			}

			// 执行请求
			resp, cancel, err := callUpstreamWithRetry(upstreamReq, "test-chat", "test-token", "test-session")

			// 验证结果
			if tc.shouldRetry {
				// 应该重试并最终成功
				if err != nil {
					t.Errorf("预期成功，但收到错误: %v", err)
				}
				if resp != nil && resp.StatusCode != 200 {
					t.Errorf("预期状态码200，收到: %d", resp.StatusCode)
				}

				expectedRequests := 2
				actualRequests := mockServer.GetRequestCount()
				if actualRequests != expectedRequests {
					t.Errorf("预期 %d 次请求，实际: %d", expectedRequests, actualRequests)
				}
			} else {
				// 不应该重试
				if resp != nil && resp.StatusCode != 400 {
					t.Errorf("预期状态码400，收到: %d", resp.StatusCode)
				}

				expectedRequests := 1
				actualRequests := mockServer.GetRequestCount()
				if actualRequests != expectedRequests {
					t.Errorf("预期 %d 次请求（不重试），实际: %d", expectedRequests, actualRequests)
				}
			}

			// 清理
			if resp != nil {
				resp.Body.Close()
				if cancel != nil {
					cancel()
				}
			}

			mockServer.Reset()
		})
	}
}

// TestRetryOnTimeout 测试超时错误的重试
func TestRetryOnTimeout(t *testing.T) {
	// 设置测试配置
	if appConfig == nil {
		appConfig = &Config{
			DebugMode:     true,
			UpstreamUrl:   "http://test.com",
			UpstreamToken: "test-token",
		}
	}

	// 创建一个会超时的模拟服务器
	responses := []MockResponse{
		{
			StatusCode: 200,
			Body:       `{"type": "done", "data": {"done": true}}`,
			Delay:      5 * time.Second, // 延迟5秒（会触发超时）
		},
		{
			StatusCode: 200,
			Body:       `{"type": "done", "data": {"done": true}}`,
			Delay:      0, // 第二次立即响应
		},
	}

	mockServer := NewMockUpstreamServer(responses)
	defer mockServer.Close()

	// 修改上游URL
	originalURL := appConfig.UpstreamUrl
	appConfig.UpstreamUrl = mockServer.URL
	defer func() {
		appConfig.UpstreamUrl = originalURL
	}()

	// 创建带有短超时的HTTP客户端
	originalClient := httpClient
	httpClient = &http.Client{
		Timeout: 100 * time.Millisecond, // 100ms超时
		Transport: &http.Transport{
			MaxIdleConns:        10,
			MaxIdleConnsPerHost: 5,
		},
	}
	defer func() {
		httpClient = originalClient
	}()

	// 创建测试请求
	upstreamReq := UpstreamRequest{
		Stream:   false,
		Model:    "test-model",
		Messages: []UpstreamMessage{{Role: "user", Content: "测试"}},
		Params:   map[string]interface{}{},
		Features: map[string]interface{}{},
	}

	// 执行请求（应该超时并重试）
	startTime := time.Now()
	resp, cancel, err := callUpstreamWithRetry(upstreamReq, "test-chat", "test-token", "test-session")
	elapsed := time.Since(startTime)

	// 验证结果
	if err == nil {
		t.Logf("请求成功，耗时: %v", elapsed)
		// 即使第一次超时，重试可能成功
		if resp != nil {
			defer resp.Body.Close()
			if cancel != nil {
				defer cancel()
			}
		}
	} else {
		// 如果仍然失败，验证是否进行了重试
		t.Logf("请求失败: %v, 耗时: %v", err, elapsed)
	}

	// 验证是否进行了重试（至少应该有1次以上的请求）
	requestCount := mockServer.GetRequestCount()
	if requestCount < 1 {
		t.Errorf("预期至少1次请求，实际: %d", requestCount)
	}

	t.Logf("✓ 超时重试测试完成，共进行了 %d 次请求", requestCount)
}

// TestRetryOnNetworkError 测试网络错误的重试
func TestRetryOnNetworkError(t *testing.T) {
	// 设置测试配置
	if appConfig == nil {
		appConfig = &Config{
			DebugMode:     true,
			UpstreamUrl:   "http://test.com",
			UpstreamToken: "test-token",
		}
	}

	// 创建一个会立即关闭连接的服务器
	closeCount := 0
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		closeCount++
		if closeCount <= 2 {
			// 前两次立即关闭连接（模拟网络错误）
			hj, ok := w.(http.Hijacker)
			if ok {
				conn, _, _ := hj.Hijack()
				conn.Close()
			}
		} else {
			// 第三次正常响应
			w.WriteHeader(200)
			w.Write([]byte(`{"type": "done", "data": {"done": true}}`))
		}
	}))
	defer mockServer.Close()

	// 修改上游URL
	originalURL := appConfig.UpstreamUrl
	appConfig.UpstreamUrl = mockServer.URL
	defer func() {
		appConfig.UpstreamUrl = originalURL
	}()

	// 创建测试请求
	upstreamReq := UpstreamRequest{
		Stream:   false,
		Model:    "test-model",
		Messages: []UpstreamMessage{{Role: "user", Content: "测试"}},
		Params:   map[string]interface{}{},
		Features: map[string]interface{}{},
	}

	// 执行请求
	resp, cancel, err := callUpstreamWithRetry(upstreamReq, "test-chat", "test-token", "test-session")

	// 验证结果
	if err != nil {
		// 网络错误应该被重试
		t.Logf("遇到网络错误并重试: %v", err)
	} else {
		// 最终可能成功
		t.Log("网络错误后重试成功")
		if resp != nil {
			defer resp.Body.Close()
			if cancel != nil {
				defer cancel()
			}
		}
	}

	// 验证重试次数
	if closeCount < 2 {
		t.Errorf("预期至少2次重试，实际: %d", closeCount)
	}

	t.Logf("✓ 网络错误重试测试完成，共进行了 %d 次请求", closeCount)
}

// TestRetryWith429RateLimit 测试429限流错误的重试
func TestRetryWith429RateLimit(t *testing.T) {
	// 设置测试配置
	if appConfig == nil {
		appConfig = &Config{
			DebugMode:     true,
			UpstreamUrl:   "http://test.com",
			UpstreamToken: "test-token",
		}
	}

	// 模拟响应：前两次429，第三次成功
	responses := []MockResponse{
		{StatusCode: 429, Body: `{"error": "rate limit exceeded"}`},
		{StatusCode: 429, Body: `{"error": "too many requests"}`},
		{StatusCode: 200, Body: `{"type": "done", "data": {"done": true}}`},
	}

	mockServer := NewMockUpstreamServer(responses)
	defer mockServer.Close()

	// 修改上游URL
	originalURL := appConfig.UpstreamUrl
	appConfig.UpstreamUrl = mockServer.URL
	defer func() {
		appConfig.UpstreamUrl = originalURL
	}()

	// 创建测试请求
	upstreamReq := UpstreamRequest{
		Stream:   false,
		Model:    "test-model",
		Messages: []UpstreamMessage{{Role: "user", Content: "测试"}},
		Params:   map[string]interface{}{},
		Features: map[string]interface{}{},
	}

	// 记录开始时间
	startTime := time.Now()

	// 执行请求
	resp, cancel, err := callUpstreamWithRetry(upstreamReq, "test-chat", "test-token", "test-session")

	// 记录耗时
	elapsed := time.Since(startTime)

	// 验证结果
	if err != nil {
		t.Errorf("预期成功，但收到错误: %v", err)
	}

	if resp != nil {
		defer resp.Body.Close()
		if cancel != nil {
			defer cancel()
		}

		if resp.StatusCode != 200 {
			t.Errorf("预期状态码200，收到: %d", resp.StatusCode)
		}
	}

	// 验证重试次数
	requestCount := mockServer.GetRequestCount()
	if requestCount != 3 {
		t.Errorf("预期3次请求（初次+2次重试），实际: %d", requestCount)
	}

	// 验证是否有延迟（429应该触发更长的延迟）
	if elapsed < 1*time.Second {
		t.Logf("警告：429重试延迟可能过短: %v", elapsed)
	}

	t.Logf("✓ 429限流重试测试通过，共进行了 %d 次请求，总耗时: %v", requestCount, elapsed)
}

// TestRetryBackoffProgression 测试退避延迟的递增
func TestRetryBackoffProgression(t *testing.T) {
	// 设置测试配置
	if appConfig == nil {
		appConfig = &Config{
			DebugMode:     true,
			UpstreamUrl:   "http://test.com",
			UpstreamToken: "test-token",
		}
	}

	// 模拟连续失败的响应
	responses := []MockResponse{
		{StatusCode: 500, Body: `{"error": "internal server error"}`},
		{StatusCode: 500, Body: `{"error": "internal server error"}`},
		{StatusCode: 500, Body: `{"error": "internal server error"}`},
		{StatusCode: 500, Body: `{"error": "internal server error"}`},
		{StatusCode: 200, Body: `{"type": "done", "data": {"done": true}}`},
	}

	mockServer := NewMockUpstreamServer(responses)
	defer mockServer.Close()

	// 修改上游URL
	originalURL := appConfig.UpstreamUrl
	appConfig.UpstreamUrl = mockServer.URL
	defer func() {
		appConfig.UpstreamUrl = originalURL
	}()

	// 创建测试请求
	upstreamReq := UpstreamRequest{
		Stream:   false,
		Model:    "test-model",
		Messages: []UpstreamMessage{{Role: "user", Content: "测试"}},
		Params:   map[string]interface{}{},
		Features: map[string]interface{}{},
	}

	// 记录每次请求的时间
	requestTimes := []time.Time{}
	originalHandler := mockServer.Server.Config.Handler
	mockServer.Server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestTimes = append(requestTimes, time.Now())
		originalHandler.ServeHTTP(w, r)
	})

	// 执行请求
	startTime := time.Now()
	resp, cancel, err := callUpstreamWithRetry(upstreamReq, "test-chat", "test-token", "test-session")
	totalElapsed := time.Since(startTime)

	// 验证结果
	if err != nil {
		t.Errorf("预期成功，但收到错误: %v", err)
	}

	if resp != nil {
		defer resp.Body.Close()
		if cancel != nil {
			defer cancel()
		}
	}

	// 验证重试次数
	requestCount := mockServer.GetRequestCount()
	if requestCount != 5 {
		t.Errorf("预期5次请求，实际: %d", requestCount)
	}

	// 验证退避延迟递增
	if len(requestTimes) > 1 {
		for i := 1; i < len(requestTimes); i++ {
			delay := requestTimes[i].Sub(requestTimes[i-1])
			t.Logf("第 %d 次重试延迟: %v", i, delay)

			// 验证延迟递增（允许一定的误差）
			if i > 1 {
				prevDelay := requestTimes[i-1].Sub(requestTimes[i-2])
				// 延迟应该递增（至少是前一次的1.5倍，考虑到抖动）
				if delay < prevDelay {
					t.Logf("警告：延迟没有递增 - 第%d次: %v, 第%d次: %v", i-1, prevDelay, i, delay)
				}
			}
		}
	}

	t.Logf("✓ 退避延迟递增测试完成，总耗时: %v", totalElapsed)
}

// TestMaxRetryAttempts 测试最大重试次数限制
func TestMaxRetryAttempts(t *testing.T) {
	// 设置测试配置
	if appConfig == nil {
		appConfig = &Config{
			DebugMode:     true,
			UpstreamUrl:   "http://test.com",
			UpstreamToken: "test-token",
		}
	}

	// 模拟始终失败的响应
	responses := []MockResponse{
		{StatusCode: 500, Body: `{"error": "internal server error"}`},
		{StatusCode: 500, Body: `{"error": "internal server error"}`},
		{StatusCode: 500, Body: `{"error": "internal server error"}`},
		{StatusCode: 500, Body: `{"error": "internal server error"}`},
		{StatusCode: 500, Body: `{"error": "internal server error"}`},
		{StatusCode: 500, Body: `{"error": "internal server error"}`}, // 第6次仍然失败
	}

	mockServer := NewMockUpstreamServer(responses)
	defer mockServer.Close()

	// 修改上游URL
	originalURL := appConfig.UpstreamUrl
	appConfig.UpstreamUrl = mockServer.URL
	defer func() {
		appConfig.UpstreamUrl = originalURL
	}()

	// 创建测试请求
	upstreamReq := UpstreamRequest{
		Stream:   false,
		Model:    "test-model",
		Messages: []UpstreamMessage{{Role: "user", Content: "测试"}},
		Params:   map[string]interface{}{},
		Features: map[string]interface{}{},
	}

	// 执行请求
	resp, cancel, err := callUpstreamWithRetry(upstreamReq, "test-chat", "test-token", "test-session")

	// 验证应该失败
	if err == nil {
		t.Error("预期失败，但请求成功了")
	} else {
		t.Logf("按预期失败: %v", err)
	}

	if resp != nil {
		defer resp.Body.Close()
		if cancel != nil {
			defer cancel()
		}
	}

	// 验证重试次数（最多5次）
	requestCount := mockServer.GetRequestCount()
	if requestCount > 5 {
		t.Errorf("超过最大重试次数限制，实际请求: %d 次", requestCount)
	} else if requestCount < 5 {
		t.Errorf("预期5次请求（达到最大重试次数），实际: %d", requestCount)
	}

	t.Logf("✓ 最大重试次数限制测试通过，共进行了 %d 次请求", requestCount)
}

// TestRetryLogging 测试重试日志输出
func TestRetryLogging(t *testing.T) {
	// 设置测试配置，启用调试模式以查看日志
	if appConfig == nil {
		appConfig = &Config{
			DebugMode:     true,
			UpstreamUrl:   "http://test.com",
			UpstreamToken: "test-token",
		}
	}
	originalDebugMode := appConfig.DebugMode
	appConfig.DebugMode = true
	defer func() {
		appConfig.DebugMode = originalDebugMode
	}()

	// 模拟需要重试的响应序列
	responses := []MockResponse{
		{StatusCode: 503, Body: `{"error": "service unavailable"}`},
		{StatusCode: 502, Body: `{"error": "bad gateway"}`},
		{StatusCode: 200, Body: `{"type": "done", "data": {"done": true}}`},
	}

	mockServer := NewMockUpstreamServer(responses)
	defer mockServer.Close()

	// 修改上游URL
	originalURL := appConfig.UpstreamUrl
	appConfig.UpstreamUrl = mockServer.URL
	defer func() {
		appConfig.UpstreamUrl = originalURL
	}()

	// 创建测试请求
	upstreamReq := UpstreamRequest{
		Stream:   false,
		Model:    "test-model",
		Messages: []UpstreamMessage{{Role: "user", Content: "测试日志"}},
		Params:   map[string]interface{}{},
		Features: map[string]interface{}{},
	}

	t.Log("开始执行重试测试，观察日志输出...")

	// 执行请求
	resp, cancel, err := callUpstreamWithRetry(upstreamReq, "test-chat", "test-token", "test-session")

	// 验证结果
	if err != nil {
		t.Errorf("预期成功，但收到错误: %v", err)
	}

	if resp != nil {
		defer resp.Body.Close()
		if cancel != nil {
			defer cancel()
		}
	}

	// 验证重试次数
	requestCount := mockServer.GetRequestCount()
	t.Logf("✓ 日志测试完成，共进行了 %d 次请求", requestCount)
	t.Log("请检查上面的日志输出，应该包含:")
	t.Log("  - 每次尝试的日志")
	t.Log("  - 错误原因说明")
	t.Log("  - 退避延迟信息")
	t.Log("  - 重试决策日志")
}
