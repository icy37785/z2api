package main

// HTTP 状态码常量，避免导入 net/http
const (
	// 2xx 成功
	StatusOK = 200

	// 4xx 客户端错误
	StatusBadRequest          = 400
	StatusUnauthorized        = 401
	StatusForbidden           = 403
	StatusNotFound            = 404
	StatusRequestTimeout      = 408
	StatusUnprocessableEntity = 422
	StatusTooManyRequests     = 429

	// 5xx 服务器错误
	StatusInternalServerError = 500
	StatusBadGateway          = 502
	StatusServiceUnavailable  = 503
	StatusGatewayTimeout      = 504
)

// 请求限制常量
const (
	// 请求限制
	MaxMessagesPerRequest = 100
	MaxContentLength      = 500000
	DefaultMaxTokens      = 10 * 1024 * 1024

	// 端口配置
	DefaultPort = "8080"
)