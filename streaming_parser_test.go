package main

import (
	"testing"
)

func TestStreamingJSONParser(t *testing.T) {
	tests := []struct {
		name     string
		chunks   []string
		expected int // 期望的工具调用数量
	}{
		{
			name: "完整工具调用",
			chunks: []string{
				`{"index": 0, "id": "call_1", "type": "function", "function": {"name": "test", "arguments": "{}"}}`,
			},
			expected: 1,
		},
		{
			name: "分块传输的工具调用",
			chunks: []string{
				`{"index": 0, "id": "call_1", "type": "function", "function": {"name": "test", "arg`,
				`uments": "{\"param\": \"value\"}"}}`,
			},
			expected: 1,
		},
		{
			name: "多个工具调用",
			chunks: []string{
				`{"index": 0, "id": "call_1", "type": "function", "function": {"name": "test1", "arguments": "{}"}}`,
				`{"index": 1, "id": "call_2", "type": "function", "function": {"name": "test2", "arguments": "{}"}}`,
			},
			expected: 2,
		},
		{
			name: "不完整的JSON片段",
			chunks: []string{
				`{"index": 0, "id": "call_1", "type": "function", "function": {"name": "test", "arg`,
			},
			expected: 0, // 不完整的JSON不应该产生工具调用
		},
		{
			name: "带有转义字符的工具调用",
			chunks: []string{
				`{"index": 0, "id": "call_1", "type": "function", "function": {"name": "test", "arguments": "{\"text\": \"hello \\\"world\\\"\"}"}}`,
			},
			expected: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := NewSSEToolCallHandler()

			for _, chunk := range tt.chunks {
				_, err := handler.process(chunk)
				if err != nil {
					t.Errorf("处理块时出错: %v", err)
				}
			}

			toolCalls := handler.GetToolCalls()
			if len(toolCalls) != tt.expected {
				t.Errorf("期望 %d 个工具调用，但得到 %d 个", tt.expected, len(toolCalls))
			}
		})
	}
}

func TestSSEToolCallHandlerReset(t *testing.T) {
	handler := NewSSEToolCallHandler()

	// 添加一个工具调用
	_, err := handler.process(`{"index": 0, "id": "call_1", "type": "function", "function": {"name": "test", "arguments": "{}"}}`)
	if err != nil {
		t.Fatalf("处理工具调用时出错: %v", err)
	}

	// 验证工具调用已添加
	if len(handler.GetToolCalls()) != 1 {
		t.Fatalf("期望 1 个工具调用，但得到 %d 个", len(handler.GetToolCalls()))
	}

	// 重置处理器
	handler.Reset()

	// 验证工具调用已清除
	if len(handler.GetToolCalls()) != 0 {
		t.Errorf("重置后期望 0 个工具调用，但得到 %d 个", len(handler.GetToolCalls()))
	}
}
