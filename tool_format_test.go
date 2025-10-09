package main

import (
	"encoding/json"
	"testing"
)

// TestToolCallJSONFormat 验证ToolCall的JSON格式符合OpenAI标准
func TestToolCallJSONFormat(t *testing.T) {
	tests := []struct {
		name     string
		toolCall ToolCall
		wantErr  bool
	}{
		{
			name: "完整的ToolCall",
			toolCall: ToolCall{
				Index: 0, // 内部使用
				ID:    "call_abc123",
				Type:  "function",
				Function: ToolCallFunction{
					Name:      "get_weather",
					Arguments: `{"location":"Beijing"}`,
				},
			},
			wantErr: false,
		},
		{
			name: "缺少ID的ToolCall（应该被规范化）",
			toolCall: ToolCall{
				Type: "function",
				Function: ToolCallFunction{
					Name:      "test_func",
					Arguments: "{}",
				},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 规范化
			normalized := normalizeToolCall(tt.toolCall)

			// 序列化为JSON
			data, err := json.Marshal(normalized)
			if err != nil {
				if !tt.wantErr {
					t.Errorf("序列化失败: %v", err)
				}
				return
			}

			t.Logf("JSON输出: %s", string(data))

			// 解析为map检查字段
			var rawMap map[string]interface{}
			if err := json.Unmarshal(data, &rawMap); err != nil {
				t.Fatalf("解析JSON失败: %v", err)
			}

			// 验证不包含index字段
			if _, hasIndex := rawMap["index"]; hasIndex {
				t.Error("❌ JSON包含非标准的index字段")
			} else {
				t.Log("✓ JSON不包含index字段")
			}

			// 验证必需字段存在
			requiredFields := []string{"id", "type", "function"}
			for _, field := range requiredFields {
				if val, exists := rawMap[field]; !exists {
					t.Errorf("❌ 缺少必需字段 '%s'", field)
				} else if val == nil || val == "" {
					t.Errorf("❌ 必需字段 '%s' 为空", field)
				} else {
					t.Logf("✓ 字段 '%s' 存在且有值", field)
				}
			}

			// 验证function对象
			if funcObj, ok := rawMap["function"].(map[string]interface{}); ok {
				if name, exists := funcObj["name"]; !exists || name == "" {
					t.Error("❌ function.name 缺失或为空")
				} else {
					t.Logf("✓ function.name = %v", name)
				}

				if args, exists := funcObj["arguments"]; !exists || args == "" {
					t.Error("❌ function.arguments 缺失或为空")
				} else {
					t.Logf("✓ function.arguments = %v", args)
				}
			} else {
				t.Error("❌ function 字段不是对象")
			}

			// 验证ID存在
			if normalized.ID == "" {
				t.Error("❌ 规范化后ID为空")
			}

			// 验证Type正确
			if normalized.Type != "function" {
				t.Errorf("❌ Type错误: %s", normalized.Type)
			}
		})
	}
}

// TestToolCallArrayFormat 验证ToolCall数组的格式
func TestToolCallArrayFormat(t *testing.T) {
	toolCalls := []ToolCall{
		{
			Index: 0,
			ID:    "call_1",
			Type:  "function",
			Function: ToolCallFunction{
				Name:      "func1",
				Arguments: "{}",
			},
		},
		{
			Index: 1,
			ID:    "call_2",
			Type:  "function",
			Function: ToolCallFunction{
				Name:      "func2",
				Arguments: `{"key":"value"}`,
			},
		},
	}

	// 规范化
	normalized := normalizeToolCalls(toolCalls)

	// 序列化
	data, err := json.MarshalIndent(normalized, "", "  ")
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}

	t.Logf("完整JSON数组:\n%s", string(data))

	// 解析验证
	var rawArray []map[string]interface{}
	if err := json.Unmarshal(data, &rawArray); err != nil {
		t.Fatalf("解析失败: %v", err)
	}

	if len(rawArray) != 2 {
		t.Errorf("数组长度错误: expected 2, got %d", len(rawArray))
	}

	// 验证每个元素
	for i, item := range rawArray {
		t.Logf("\n验证元素 %d:", i)

		// 不应该有index字段
		if _, hasIndex := item["index"]; hasIndex {
			t.Errorf("❌ 元素 %d 包含index字段", i)
		}

		// 必需字段检查
		for _, field := range []string{"id", "type", "function"} {
			if _, exists := item[field]; !exists {
				t.Errorf("❌ 元素 %d 缺少字段 '%s'", i, field)
			} else {
				t.Logf("✓ 元素 %d 包含字段 '%s'", i, field)
			}
		}
	}
}

// TestNormalizeToolCallWithMissingFields 测试规范化函数
func TestNormalizeToolCallWithMissingFields(t *testing.T) {
	tests := []struct {
		name     string
		input    ToolCall
		checkID  bool
		checkType bool
		checkArgs bool
	}{
		{
			name: "缺少所有可选字段",
			input: ToolCall{
				Function: ToolCallFunction{
					Name: "test",
				},
			},
			checkID:   true,
			checkType: true,
			checkArgs: true,
		},
		{
			name: "只有function.name",
			input: ToolCall{
				Function: ToolCallFunction{
					Name: "my_function",
				},
			},
			checkID:   true,
			checkType: true,
			checkArgs: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := normalizeToolCall(tt.input)

			if tt.checkID && result.ID == "" {
				t.Error("❌ 规范化后ID为空")
			} else if tt.checkID {
				t.Logf("✓ 自动生成ID: %s", result.ID)
			}

			if tt.checkType && result.Type != "function" {
				t.Errorf("❌ Type错误: %s", result.Type)
			} else if tt.checkType {
				t.Logf("✓ Type设置为: %s", result.Type)
			}

			if tt.checkArgs && result.Function.Arguments == "" {
				t.Error("❌ Arguments为空")
			} else if tt.checkArgs {
				t.Logf("✓ Arguments设置为: %s", result.Function.Arguments)
			}
		})
	}
}
