package main

import (
	"log/slog"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-contrib/requestid"
	"github.com/gin-gonic/gin"
	"z2api/errors"
	"z2api/utils"
)

// setupRouter 设置并返回 Gin 路由器
func setupRouter() *gin.Engine {
	// 根据调试模式设置 Gin 模式
	if !appConfig.DebugMode {
		gin.SetMode(gin.ReleaseMode)
	}

	// 创建 Gin 引擎
	router := gin.New()

	// 添加中间件
	router.Use(ginLogger())           // 自定义日志中间件
	router.Use(gin.Recovery())        // 恢复中间件
	router.Use(requestid.New())       // Request ID 中间件
	router.Use(setupCORS())           // CORS 中间件
	router.Use(rateLimitMiddleware()) // 限流中间件

	// 注册路由 - 使用 Gin 原生处理器
	v1 := router.Group("/v1")
	{
		v1.GET("/models", GinHandleModels)
		v1.POST("/chat/completions", GinHandleChatCompletions)
	}

	// 健康检查和监控端点
	router.GET("/health", GinHandleHealth)
	router.GET("/", GinHandleHome)
	router.GET("/dashboard", GinHandleDashboard)
	router.GET("/dashboard/stats", GinHandleDashboardStats)
	router.GET("/dashboard/requests", GinHandleDashboardRequests)

	// OPTIONS 处理 - CORS 已在中间件处理，这里只返回成功
	router.OPTIONS("/*path", GinHandleOptions)

	// 404 处理器
	router.NoRoute(GinHandleNotFound)

	return router
}

// setupCORS 设置 CORS 中间件
func setupCORS() gin.HandlerFunc {
	config := cors.Config{
		AllowOrigins:     []string{"*"},
		AllowMethods:     []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Authorization", "Accept", "X-Request-ID"},
		ExposeHeaders:    []string{"Content-Length", "X-Request-ID"},
		AllowCredentials: false,
		MaxAge:           12 * time.Hour,
	}
	return cors.New(config)
}

// ginLogger 自定义日志中间件，使用 slog
func ginLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path
		raw := c.Request.URL.RawQuery

		// 处理请求
		c.Next()

		// 计算延迟
		latency := time.Since(start)
		clientIP := c.ClientIP()
		method := c.Request.Method
		statusCode := c.Writer.Status()
		errorMessage := c.Errors.ByType(gin.ErrorTypePrivate).String()

		// 构建完整路径
		if raw != "" {
			path = path + "?" + raw
		}

		// 使用 slog 记录日志
		if appConfig.DebugMode {
			slog.Info("HTTP Request",
				"status", statusCode,
				"method", method,
				"path", path,
				"ip", clientIP,
				"latency", latency,
				"request_id", requestid.Get(c),
				"error", errorMessage,
			)
		}
	}
}

// rateLimitMiddleware 并发限流中间件
func rateLimitMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 尝试获取信号量
		if !concurrencyLimiter.TryAcquire(1) {
			err := errors.ErrRateLimited.WithDetails("Too many concurrent requests, please try again later")
			utils.ErrorResponse(c, err)
			c.Abort()
			return
		}

		// 确保释放信号量
		defer concurrencyLimiter.Release(1)

		c.Next()
	}
}
