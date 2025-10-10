package utils

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/gin-gonic/gin"
)

var (
	// Logger 全局日志实例
	Logger *slog.Logger

	// 日志级别
	debugMode bool
)

// InitLogger 初始化日志系统
func InitLogger(debug bool) {
	debugMode = debug

	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}

	opts := &slog.HandlerOptions{
		Level: level,
		// 添加源代码位置
		AddSource: debug,
	}

	// 使用JSON格式输出到标准输出
	handler := slog.NewJSONHandler(os.Stdout, opts)
	Logger = slog.New(handler)

	// 设置为默认logger
	slog.SetDefault(Logger)
}

// LogDebug 调试日志
func LogDebug(msg string, args ...any) {
	if debugMode {
		Logger.Debug(msg, args...)
	}
}

// LogInfo 信息日志
func LogInfo(msg string, args ...any) {
	Logger.Info(msg, args...)
}

// LogWarn 警告日志
func LogWarn(msg string, args ...any) {
	Logger.Warn(msg, args...)
}

// LogError 错误日志
func LogError(msg string, args ...any) {
	Logger.Error(msg, args...)
}

// LogWithContext 带上下文的日志
func LogWithContext(ctx context.Context, level slog.Level, msg string, args ...any) {
	Logger.LogAttrs(ctx, level, msg, attrsFromArgs(args...)...)
}

// GinLoggerMiddleware Gin日志中间件
func GinLoggerMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 请求开始时间
		start := c.GetTime("start_time")
		if start.IsZero() {
			start = time.Now()
		}

		// 处理请求
		c.Next()

		// 计算延迟
		latency := time.Since(start)

		// 获取请求信息
		status := c.Writer.Status()
		method := c.Request.Method
		path := c.Request.URL.Path
		clientIP := c.ClientIP()
		requestID := c.GetString("request_id")
		userAgent := c.GetHeader("User-Agent")

		// 构建日志字段
		fields := []any{
			"status", status,
			"method", method,
			"path", path,
			"ip", clientIP,
			"latency_ms", latency.Milliseconds(),
			"request_id", requestID,
			"user_agent", userAgent,
		}

		// 如果有错误，添加错误信息
		if len(c.Errors) > 0 {
			fields = append(fields, "errors", c.Errors.String())
		}

		// 根据状态码决定日志级别
		if status >= 500 {
			LogError("HTTP Request Error", fields...)
		} else if status >= 400 {
			LogWarn("HTTP Request Warning", fields...)
		} else {
			LogInfo("HTTP Request", fields...)
		}
	}
}

// LogRequestStart 记录请求开始
func LogRequestStart(c *gin.Context, operation string) {
	LogDebug("Request started",
		"operation", operation,
		"method", c.Request.Method,
		"path", c.Request.URL.Path,
		"client_ip", c.ClientIP(),
		"user_agent", c.GetHeader("User-Agent"),
	)
}

// LogRequestEnd 记录请求结束
func LogRequestEnd(c *gin.Context, operation string, success bool, latency time.Duration) {
	fields := []any{
		"operation", operation,
		"success", success,
		"latency_ms", latency.Milliseconds(),
		"status", c.Writer.Status(),
	}

	if success {
		LogInfo("Request completed", fields...)
	} else {
		LogError("Request failed", fields...)
	}
}

// attrsFromArgs 将键值对转换为slog属性
func attrsFromArgs(args ...any) []slog.Attr {
	var attrs []slog.Attr
	for i := 0; i < len(args); i += 2 {
		if i+1 < len(args) {
			key, ok := args[i].(string)
			if ok {
				attrs = append(attrs, slog.Any(key, args[i+1]))
			}
		}
	}
	return attrs
}
