package main

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"z2api/errors"
	"z2api/types"
	"z2api/utils"
)

// GinHandleDashboard 处理仪表盘页面 (Gin 原生实现)
func GinHandleDashboard(c *gin.Context) {
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(http.StatusOK, dashboardHTML)
}

// GinHandleDashboardStats 处理仪表盘统计端点 (Gin 原生实现)
func GinHandleDashboardStats(c *gin.Context) {
	// 检查 stats 是否已初始化
	if stats == nil {
		err := errors.ErrInternalError.WithDetails("Stats not initialized")
		utils.ErrorResponse(c, err)
		return
	}

	stats.Mutex.RLock()
	defer stats.Mutex.RUnlock()

	// 创建统计管理器实例
	statsManager := NewStatsManager()
	// 获取前3个最常用的模型
	topModels := statsManager.GetTopModels()

	// 创建可序列化的统计响应
	statsResponse := gin.H{
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

	// 使用 Gin 的 JSON 响应方法
	c.JSON(http.StatusOK, statsResponse)
}

// GinHandleDashboardRequests 处理仪表盘实时请求端点 (Gin 原生实现)
func GinHandleDashboardRequests(c *gin.Context) {
	liveRequestsMutex.RLock()
	// 创建切片的副本以避免在编码时持有锁
	requests := make([]types.LiveRequest, len(liveRequests))
	copy(requests, liveRequests)
	liveRequestsMutex.RUnlock()

	// 反转切片，使最新的请求排在前面
	for i, j := 0, len(requests)-1; i < j; i, j = i+1, j-1 {
		requests[i], requests[j] = requests[j], requests[i]
	}

	// 使用 Gin 的 JSON 响应方法
	c.JSON(http.StatusOK, requests)
}

// GinHandleOptions 处理 OPTIONS 请求 (Gin 原生实现)
func GinHandleOptions(c *gin.Context) {
	// CORS 已在中间件中处理，这里只需要返回成功
	c.Status(http.StatusOK)
}

// GinHandleHome 处理根路径 (Gin 原生实现)
func GinHandleHome(c *gin.Context) {
	// 跟踪首页访问量
	if stats != nil {
		stats.Mutex.Lock()
		stats.HomePageViews++
		stats.Mutex.Unlock()
	}

	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(http.StatusOK, "<h1>ZtoApi</h1><p>OpenAI compatible API for Z.ai.</p>")
}

// GinHandleNotFound 处理 404 页面 (Gin 原生实现)
func GinHandleNotFound(c *gin.Context) {
	err := errors.ErrModelNotFound.WithParam(c.Request.URL.Path)
	utils.ErrorResponse(c, err)
}

// StatsResponse 统计响应结构 (用于更好的类型安全)
type StatsResponse struct {
	TotalRequests        int64                `json:"totalRequests"`
	SuccessfulRequests   int64                `json:"successfulRequests"`
	FailedRequests       int64                `json:"failedRequests"`
	LastRequestTime      time.Time            `json:"lastRequestTime"`
	AverageResponseTime  float64              `json:"averageResponseTime"`
	HomePageViews        int64                `json:"homePageViews"`
	ApiCallsCount        int64                `json:"apiCallsCount"`
	ModelsCallsCount     int64                `json:"modelsCallsCount"`
	StreamingRequests    int64                `json:"streamingRequests"`
	NonStreamingRequests int64                `json:"nonStreamingRequests"`
	TotalTokensUsed      int64                `json:"totalTokensUsed"`
	StartTime            time.Time            `json:"startTime"`
	FastestResponse      float64              `json:"fastestResponse"`
	SlowestResponse      float64              `json:"slowestResponse"`
	TopModels            []ModelUsageResponse `json:"topModels"`
}

// ModelUsageResponse 模型使用响应结构
type ModelUsageResponse struct {
	Model string `json:"model"`
	Count int64  `json:"count"`
}

// 辅助函数：设置 JSON 响应头
func setJSONHeaders(c *gin.Context) {
	c.Header("Content-Type", "application/json; charset=utf-8")
	c.Header("Access-Control-Allow-Origin", "*")
}

// 辅助函数：设置 HTML 响应头
func setHTMLHeaders(c *gin.Context) {
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.Header("Access-Control-Allow-Origin", "*")
}
