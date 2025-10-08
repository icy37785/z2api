package main

import (
	"encoding/base64"
	"fmt"
	"testing"
	"time"

	json "github.com/bytedance/sonic"
)

// TestDecodeJWT 测试 JWT 解码功能
func TestDecodeJWT(t *testing.T) {
	// 创建一个测试用的 JWT payload
	testPayload := map[string]string{"id": "test-user-123"}
	payloadBytes, _ := json.Marshal(testPayload)
	payloadEncoded := base64.URLEncoding.EncodeToString(payloadBytes)

	// 创建一个简化的 JWT token (只有 header.payload.signature 格式)
	token := "header." + payloadEncoded + ".signature"

	// 测试解码
	result, err := decodeJWT(token)
	if err != nil {
		t.Fatalf("解码 JWT 失败: %v", err)
	}

	if result.ID != "test-user-123" {
		t.Errorf("期望 user_id 为 'test-user-123', 实际为 '%s'", result.ID)
	}
}

// TestGenerateSignature 测试签名生成功能
func TestGenerateSignature(t *testing.T) {
	// 测试参数
	e := "requestId,req-123,timestamp,1234567890,user_id,test-user"
	userContent := "Hello, how are you?"
	timestamp := int64(1234567890)

	// 生成签名
	signature, err := generateSignature(e, userContent, timestamp)
	if err != nil {
		t.Fatalf("生成签名失败: %v", err)
	}

	// 验证签名不为空
	if signature == "" {
		t.Error("生成的签名为空")
	}

	// 验证签名长度（SHA256 哈希应该是 64 个字符）
	if len(signature) != 64 {
		t.Errorf("签名长度应为 64 个字符, 实际为 %d", len(signature))
	}

	// 测试相同输入产生相同签名
	signature2, err := generateSignature(e, userContent, timestamp)
	if err != nil {
		t.Fatalf("第二次生成签名失败: %v", err)
	}

	if signature != signature2 {
		t.Error("相同输入产生了不同的签名")
	}

	// 测试不同输入产生不同签名
	signature3, err := generateSignature(e, "different content", timestamp)
	if err != nil {
		t.Fatalf("生成不同内容签名失败: %v", err)
	}

	if signature == signature3 {
		t.Error("不同输入产生了相同的签名")
	}
}

// TestGenerateZsSignature 测试完整的签名生成流程
func TestGenerateZsSignature(t *testing.T) {
	// 测试参数
	userID := "test-user-456"
	requestID := "req-789"
	timestamp := time.Now().UnixMilli()
	userContent := "What's the weather today?"

	// 生成签名
	result, err := generateZsSignature(userID, requestID, timestamp, userContent)
	if err != nil {
		t.Fatalf("生成 ZS 签名失败: %v", err)
	}

	// 验证结果
	if result.Signature == "" {
		t.Error("签名字段为空")
	}

	if result.Timestamp != timestamp {
		t.Errorf("时间戳不匹配, 期望 %d, 实际 %d", timestamp, result.Timestamp)
	}

	if len(result.Signature) != 64 {
		t.Errorf("签名长度应为 64 个字符, 实际为 %d", len(result.Signature))
	}
}

// TestGenerateZsSignatureWithInvalidToken 测试使用无效 token 的情况
func TestGenerateZsSignatureWithInvalidToken(t *testing.T) {
	// 使用回退的 user_id（模拟 token 解码失败的情况）
	userID := "guest-user-12345"

	// 测试参数
	requestID := "req-test"
	timestamp := time.Now().UnixMilli()
	userContent := "Test content"

	// 生成签名
	result, err := generateZsSignature(userID, requestID, timestamp, userContent)
	if err != nil {
		t.Fatalf("生成签名失败: %v", err)
	}

	// 应该返回结果
	if result == nil {
		t.Error("应该返回签名结果")
	}

	if result.Signature == "" {
		t.Error("签名字段为空")
	}
}

// BenchmarkGenerateSignature 性能测试
func BenchmarkGenerateSignature(b *testing.B) {
	e := "requestId,req-123,timestamp,1234567890,user_id,test-user"
	userContent := "Hello, how are you?"
	timestamp := int64(1234567890)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := generateSignature(e, userContent, timestamp)
		if err != nil {
			b.Fatalf("生成签名失败: %v", err)
		}
	}
}

// 示例使用函数
func Example_generateZsSignature() {
	// 生成签名
	result, err := generateZsSignature("example-user", "req-123", time.Now().UnixMilli(), "Hello, world!")
	if err != nil {
		fmt.Printf("生成签名失败: %v\n", err)
		return
	}

	fmt.Printf("签名: %s\n", result.Signature)
	fmt.Printf("时间戳: %d\n", result.Timestamp)
}

// TestSignatureConsistency 测试 Go 实现与 Python 实现的签名一致性
func TestSignatureConsistency(t *testing.T) {
	// 固定的测试参数（与 Python 脚本中的参数完全相同）
	const TestUserID = "test-user-123" // 从 JWT token 解析出的 user_id
	const TestRequestID = "test-request-456"
	const TestTimestamp = int64(1694169600000) // 2023-09-08 12:00:00 UTC
	const TestUserContent = "这是一个测试消息，用于验证签名算法的一致性"
	const ExpectedSignature = "c26d0bc64a0aac997a300425c7fe2235d7c371f28f9aa4f6051c2436f2d2b815"

	// 生成签名
	result, err := generateZsSignature(TestUserID, TestRequestID, TestTimestamp, TestUserContent)
	if err != nil {
		t.Fatalf("生成 ZS 签名失败: %v", err)
	}

	// 验证签名与 Python 生成的标准签名完全一致
	if result.Signature != ExpectedSignature {
		t.Errorf("签名不一致！\n期望: %s\n实际: %s", ExpectedSignature, result.Signature)
	}

	// 验证时间戳
	if result.Timestamp != TestTimestamp {
		t.Errorf("时间戳不匹配！\n期望: %d\n实际: %d", TestTimestamp, result.Timestamp)
	}

	// 验证签名长度（SHA256 哈希应该是 64 个字符）
	if len(result.Signature) != 64 {
		t.Errorf("签名长度应为 64 个字符, 实际为 %d", len(result.Signature))
	}

	t.Logf("签名一致性测试通过！\n签名: %s\n时间戳: %d", result.Signature, result.Timestamp)
}
