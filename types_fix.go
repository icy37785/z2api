package main

// AssistantMessage 定义助手消息结构
type AssistantMessage struct {
	Role             string     `json:"role"`
	Content          string     `json:"content,omitempty"`
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
}

// ModelConfig 模型配置结构（简化版）
type ModelConfig struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	UpstreamID   string `json:"upstream_id,omitempty"`
	Capabilities struct {
		Vision   bool `json:"vision"`
		Thinking bool `json:"thinking"`
		Search   bool `json:"search"`
	} `json:"capabilities"`
}
