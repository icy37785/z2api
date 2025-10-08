package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/bytedance/sonic"
)

// JWTPayload 表示 JWT token 的 payload 部分
type JWTPayload struct {
	ID string `json:"id"`
}

// SignatureResponse 表示签名生成函数的返回值
type SignatureResponse struct {
	Signature string `json:"signature"`
	Timestamp int64  `json:"timestamp"`
}

// decodeJWT 解码 JWT token 的 payload 部分
// 参考: docs/参考/signature.py -> decode_jwt_payload
func decodeJWT(token string) (*JWTPayload, error) {
	// 将 token 按 '.' 分割
	parts := strings.Split(token, ".")
	if len(parts) < 3 {
		return nil, fmt.Errorf("无效的 JWT token 格式")
	}

	// 获取 payload 部分
	payload := parts[1]

	// 处理 Base64 URL 编码的填充字符
	padding := 4 - len(payload)%4
	if padding != 4 {
		payload += strings.Repeat("=", padding)
	}

	// Base64 URL 解码
	decoded, err := base64.URLEncoding.DecodeString(payload)
	if err != nil {
		return nil, fmt.Errorf("Base64 URL 解码失败: %v", err)
	}

	// JSON 解析
	var jwtPayload JWTPayload
	err = sonic.Unmarshal(decoded, &jwtPayload)
	if err != nil {
		return nil, fmt.Errorf("JSON 解析失败: %v", err)
	}

	return &jwtPayload, nil
}

// generateZsSignature 生成 Z.AI API 签名
// 参考: docs/参考/signature.py -> zs 和 generate_zs_signature
func generateZsSignature(userID, requestID string, timestamp int64, userContent string) (*SignatureResponse, error) {
	// 直接使用传入的 userID 参数构建签名字符串
	e := fmt.Sprintf("requestId,%s,timestamp,%d,user_id,%s", requestID, timestamp, userID)

	// 生成签名
	signature, err := generateSignature(e, userContent, timestamp)
	if err != nil {
		return nil, fmt.Errorf("生成签名失败: %v", err)
	}

	return &SignatureResponse{
		Signature: signature,
		Timestamp: timestamp,
	}, nil
}

// extractUserID 从 JWT token 中提取 user_id
func extractUserID(token string) (string, error) {
	payload, err := decodeJWT(token)
	if err != nil {
		return "", err
	}
	return payload.ID, nil
}

// generateSignature 生成 HMAC-SHA256 签名
// 参考: docs/参考/signature.py -> zs 函数
func generateSignature(e, userContent string, timestamp int64) (string, error) {
	// 时间戳字符串
	r := fmt.Sprintf("%d", timestamp)

	// 构建待签名字符串: e|userContent|timestamp
	i := fmt.Sprintf("%s|%s|%s", e, userContent, r)

	// 计算 n = timestamp // (5 * 60 * 1000) (5分钟窗口)
	n := timestamp / (5 * 60 * 1000)

	// 密钥
	key := []byte("junjie")

	// 第一次 HMAC: 使用密钥 "junjie" 和 n 计算哈希
	h1 := hmac.New(sha256.New, key)
	h1.Write([]byte(fmt.Sprintf("%d", n)))
	o := fmt.Sprintf("%x", h1.Sum(nil))

	// 第二次 HMAC: 使用第一次的结果作为密钥，对字符串 i 进行签名
	h2 := hmac.New(sha256.New, []byte(o))
	h2.Write([]byte(i))
	signature := fmt.Sprintf("%x", h2.Sum(nil))

	return signature, nil
}
