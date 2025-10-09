package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"
)

// TestIsRetryableError 测试判断错误是否可重试的函数
func TestIsRetryableError(t *testing.T) {
	tests := []struct {
		name         string
		err          error
		statusCode   int
		responseBody []byte
		want         bool
	}{
		// 网络和超时错误测试
		{
			name:       "context超时错误应该可重试",
			err:        context.DeadlineExceeded,
			statusCode: 0,
			want:       true,
		},
		{
			name:       "EOF错误应该可重试",
			err:        io.EOF,
			statusCode: 0,
			want:       true,
		},
		{
			name:       "意外EOF错误应该可重试",
			err:        io.ErrUnexpectedEOF,
			statusCode: 0,
			want:       true,
		},
		{
			name:       "连接重置错误应该可重试",
			err:        errors.New("connection reset by peer"),
			statusCode: 0,
			want:       true,
		},
		{
			name:       "连接拒绝错误应该可重试",
			err:        errors.New("connection refused"),
			statusCode: 0,
			want:       true,
		},
		{
			name:       "管道破裂错误应该可重试",
			err:        errors.New("broken pipe"),
			statusCode: 0,
			want:       true,
		},
		
		// HTTP状态码测试
		{
			name:       "401未授权应该可重试",
			err:        nil,
			statusCode: http.StatusUnauthorized,
			want:       true,
		},
		{
			name:       "429限流应该可重试",
			err:        nil,
			statusCode: http.StatusTooManyRequests,
			want:       true,
		},
		{
			name:       "500服务器错误应该可重试",
			err:        nil,
			statusCode: http.StatusInternalServerError,
			want:       true,
		},
		{
			name:       "502网关错误应该可重试",
			err:        nil,
			statusCode: http.StatusBadGateway,
			want:       true,
		},
		{
			name:       "503服务不可用应该可重试",
			err:        nil,
			statusCode: http.StatusServiceUnavailable,
			want:       true,
		},
		{
			name:       "504网关超时应该可重试",
			err:        nil,
			statusCode: http.StatusGatewayTimeout,
			want:       true,
		},
		{
			name:       "408请求超时应该可重试",
			err:        nil,
			statusCode: http.StatusRequestTimeout,
			want:       true,
		},
		
		// 特殊的400错误测试
		{
			name:         "400系统繁忙应该可重试",
			err:          nil,
			statusCode:   http.StatusBadRequest,
			responseBody: []byte(`{"error": "系统繁忙，请稍后重试"}`),
			want:         true,
		},
		{
			name:         "400 system busy应该可重试",
			err:          nil,
			statusCode:   http.StatusBadRequest,
			responseBody: []byte(`{"error": "system busy"}`),
			want:         true,
		},
		{
			name:         "400 rate limit应该可重试",
			err:          nil,
			statusCode:   http.StatusBadRequest,
			responseBody: []byte(`{"error": "rate limit exceeded"}`),
			want:         true,
		},
		{
			name:         "400 too many requests应该可重试",
			err:          nil,
			statusCode:   http.StatusBadRequest,
			responseBody: []byte(`{"error": "too many requests"}`),
			want:         true,
		},
		{
			name:         "400 temporarily unavailable应该可重试",
			err:          nil,
			statusCode:   http.StatusBadRequest,
			responseBody: []byte(`{"error": "service temporarily unavailable"}`),
			want:         true,
		},
		
		// 不可重试的错误
		{
			name:         "普通400错误不应该重试",
			err:          nil,
			statusCode:   http.StatusBadRequest,
			responseBody: []byte(`{"error": "invalid parameter"}`),
			want:         false,
		},
		{
			name:       "404未找到不应该重试",
			err:        nil,
			statusCode: http.StatusNotFound,
			want:       false,
		},
		{
			name:       "403禁止访问不应该重试",
			err:        nil,
			statusCode: http.StatusForbidden,
			want:       false,
		},
		{
			name:       "200成功不应该重试",
			err:        nil,
			statusCode: http.StatusOK,
			want:       false,
		},
		{
			name:       "普通错误不应该重试",
			err:        errors.New("some random error"),
			statusCode: 0,
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isRetryableError(tt.err, tt.statusCode, tt.responseBody)
			if got != tt.want {
				t.Errorf("isRetryableError() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestCalculateBackoffDelay 测试退避延迟计算函数
func TestCalculateBackoffDelay(t *testing.T) {
	baseDelay := 100 * time.Millisecond
	maxDelay := 10 * time.Second

	tests := []struct {
		name        string
		attempt     int
		baseDelay   time.Duration
		maxDelay    time.Duration
		minExpected time.Duration
		maxExpected time.Duration
	}{
		{
			name:        "第0次重试（初次）",
			attempt:     0,
			baseDelay:   baseDelay,
			maxDelay:    maxDelay,
			minExpected: 50 * time.Millisecond,  // baseDelay * 0.5 (考虑抖动)
			maxExpected: 150 * time.Millisecond, // baseDelay * 1.5 (考虑抖动)
		},
		{
			name:        "第1次重试",
			attempt:     1,
			baseDelay:   baseDelay,
			maxDelay:    maxDelay,
			minExpected: 100 * time.Millisecond, // baseDelay * 2 * 0.5
			maxExpected: 300 * time.Millisecond, // baseDelay * 2 * 1.5
		},
		{
			name:        "第2次重试",
			attempt:     2,
			baseDelay:   baseDelay,
			maxDelay:    maxDelay,
			minExpected: 200 * time.Millisecond, // baseDelay * 4 * 0.5
			maxExpected: 600 * time.Millisecond, // baseDelay * 4 * 1.5
		},
		{
			name:        "第3次重试",
			attempt:     3,
			baseDelay:   baseDelay,
			maxDelay:    maxDelay,
			minExpected: 400 * time.Millisecond, // baseDelay * 8 * 0.5
			maxExpected: 1200 * time.Millisecond, // baseDelay * 8 * 1.5
		},
		{
			name:        "达到最大延迟",
			attempt:     10, // 很大的尝试次数
			baseDelay:   baseDelay,
			maxDelay:    maxDelay,
			minExpected: 5 * time.Second,  // maxDelay * 0.5
			maxExpected: 10 * time.Second, // maxDelay (不会超过)
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 运行多次以测试随机抖动
			for i := 0; i < 10; i++ {
				got := calculateBackoffDelay(tt.attempt, tt.baseDelay, tt.maxDelay)
				
				if got < tt.minExpected {
					t.Errorf("calculateBackoffDelay(attempt=%d) = %v, 小于最小期望值 %v",
						tt.attempt, got, tt.minExpected)
				}
				if got > tt.maxExpected {
					t.Errorf("calculateBackoffDelay(attempt=%d) = %v, 大于最大期望值 %v",
						tt.attempt, got, tt.maxExpected)
				}
				
				// 确保不小于基础延迟
				if got < tt.baseDelay {
					t.Errorf("calculateBackoffDelay(attempt=%d) = %v, 小于基础延迟 %v",
						tt.attempt, got, tt.baseDelay)
				}
				
				// 确保不大于最大延迟
				if got > tt.maxDelay {
					t.Errorf("calculateBackoffDelay(attempt=%d) = %v, 大于最大延迟 %v",
						tt.attempt, got, tt.maxDelay)
				}
			}
		})
	}
}

// TestCalculateBackoffDelayProgression 测试退避延迟的递增性
func TestCalculateBackoffDelayProgression(t *testing.T) {
	baseDelay := 100 * time.Millisecond
	maxDelay := 10 * time.Second
	
	// 测试延迟是否大致呈指数增长（考虑到随机抖动）
	var prevAvg time.Duration
	
	for attempt := 0; attempt <= 5; attempt++ {
		var total time.Duration
		runs := 100
		
		// 运行多次取平均值，以减少随机性的影响
		for i := 0; i < runs; i++ {
			delay := calculateBackoffDelay(attempt, baseDelay, maxDelay)
			total += delay
		}
		
		avg := total / time.Duration(runs)
		
		t.Logf("尝试 %d: 平均延迟 = %v", attempt, avg)
		
		// 验证平均延迟是否递增（除了第一次）
		if attempt > 0 && avg <= prevAvg {
			t.Errorf("延迟没有递增: 尝试 %d 的平均延迟 (%v) <= 尝试 %d 的平均延迟 (%v)",
				attempt, avg, attempt-1, prevAvg)
		}
		
		prevAvg = avg
	}
}

// MockNetError 模拟网络错误
type MockNetError struct {
	isTimeout   bool
	isTemporary bool
}

func (m MockNetError) Error() string {
	return "mock network error"
}

func (m MockNetError) Timeout() bool {
	return m.isTimeout
}

func (m MockNetError) Temporary() bool {
	return m.isTemporary
}

// TestIsRetryableErrorWithNetError 测试网络错误接口
func TestIsRetryableErrorWithNetError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "超时网络错误应该可重试",
			err:  MockNetError{isTimeout: true, isTemporary: false},
			want: true,
		},
		{
			name: "临时网络错误应该可重试",
			err:  MockNetError{isTimeout: false, isTemporary: true},
			want: true,
		},
		{
			name: "超时且临时的网络错误应该可重试",
			err:  MockNetError{isTimeout: true, isTemporary: true},
			want: true,
		},
		{
			name: "非超时非临时的网络错误不应该重试",
			err:  MockNetError{isTimeout: false, isTemporary: false},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isRetryableError(tt.err, 0, nil)
			if got != tt.want {
				t.Errorf("isRetryableError() = %v, want %v", got, tt.want)
			}
		})
	}
}

// BenchmarkIsRetryableError 性能测试
func BenchmarkIsRetryableError(b *testing.B) {
	err := errors.New("connection reset")
	body := []byte(`{"error": "system busy"}`)
	
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		isRetryableError(err, http.StatusBadRequest, body)
	}
}

// BenchmarkCalculateBackoffDelay 性能测试
func BenchmarkCalculateBackoffDelay(b *testing.B) {
	baseDelay := 100 * time.Millisecond
	maxDelay := 10 * time.Second
	
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		calculateBackoffDelay(3, baseDelay, maxDelay)
	}
}