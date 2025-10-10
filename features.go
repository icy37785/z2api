package main

import (
	"strings"
	"z2api/config"
	"z2api/types"
	"z2api/utils"
)

// Features 定义模型特性配置
type Features struct {
	ImageGeneration bool     `json:"image_generation"`
	WebSearch       bool     `json:"web_search"`
	AutoWebSearch   bool     `json:"auto_web_search"`
	PreviewMode     bool     `json:"preview_mode"`
	EnableThinking  bool     `json:"enable_thinking"`
	Vision          bool     `json:"vision"`
	Flags           []string `json:"flags,omitempty"`
	MCPServers      []string `json:"mcp_servers,omitempty"`
}

// FeatureConfig 包含特性和相关配置
type FeatureConfig struct {
	Features        Features          `json:"features"`
	BackgroundTasks map[string]bool   `json:"background_tasks"`
	ToolServers     []string          `json:"tool_servers"`
	Variables       map[string]string `json:"variables"`
}

// getModelFeatures 根据模型ID和流式模式动态返回特性配置
// 参考 Python 版本的 getfeatures 函数，提供更细粒度的特性控制
func getModelFeatures(modelID string, streaming bool) FeatureConfig {
	config := FeatureConfig{
		Features: Features{
			ImageGeneration: false,
			WebSearch:       false,
			AutoWebSearch:   false,
			PreviewMode:     false,
			EnableThinking:  streaming, // 流式模式默认启用思考
			Vision:          false,
			Flags:           []string{},
			MCPServers:      []string{},
		},
		BackgroundTasks: map[string]bool{
			"title_generation": false,
			"tags_generation":  false,
		},
		ToolServers: []string{},
		Variables: map[string]string{
			"{{USER_NAME}}":        "User",
			"{{USER_LOCATION}}":    "Unknown",
			"{{CURRENT_DATE}}":     "",
			"{{CURRENT_TIME}}":     "",
			"{{CURRENT_DATETIME}}": "",
		},
	}

	// 根据模型ID设置特定功能
	modelLower := strings.ToLower(modelID)

	// 搜索模型配置 - 参考 Python 版本的逻辑
	switch {
	case strings.Contains(modelLower, "glm-4.6-advanced-search"):
		config.Features.WebSearch = true
		config.Features.AutoWebSearch = true
		config.Features.PreviewMode = true
		config.Features.MCPServers = []string{"advanced-search"}

	case strings.Contains(modelLower, "glm-4.6-search"):
		config.Features.WebSearch = true
		config.Features.AutoWebSearch = true
		config.Features.PreviewMode = true
		config.Features.MCPServers = []string{"deep-web-search"}

	case strings.Contains(modelLower, "search"):
		// 通用搜索模型
		config.Features.WebSearch = true
		config.Features.AutoWebSearch = true
		config.Features.PreviewMode = true
		config.Features.MCPServers = []string{"deep-web-search"}
	}

	// 思考模式配置 - 参考 Python 版本
	switch {
	case strings.Contains(modelLower, "glm-4.6-nothinking"):
		config.Features.EnableThinking = false

	case strings.Contains(modelLower, "nothinking") || strings.Contains(modelLower, "no-thinking"):
		config.Features.EnableThinking = false

	case strings.Contains(modelLower, "glm-4.6"):
		// GLM-4.6 系列模型在流式模式下默认启用思考
		if streaming {
			config.Features.EnableThinking = true
		}
	}

	// 视觉模型配置
	switch {
	case strings.Contains(modelLower, "glm-4.5v"):
		config.Features.Vision = true
		// GLM-4.5v 支持全方位多模态

	case strings.Contains(modelLower, "vision") || strings.Contains(modelLower, "4v"):
		config.Features.Vision = true
	}

	// 图像生成模型
	if strings.Contains(modelLower, "dall-e") || strings.Contains(modelLower, "image-gen") {
		config.Features.ImageGeneration = true
	}

	// 非流式模式调整 - 参考 Python 版本的逻辑
	if !streaming {
		config.Features.EnableThinking = false // 非流式模式禁用思考
		// 非流式模式下禁用 MCP 服务器（如 Python 版本）
		config.Features.MCPServers = []string{}
	}

	return config
}

// ToMap 将 Features 结构体转换为 map[string]interface{}
func (f Features) ToMap() map[string]interface{} {
	return map[string]interface{}{
		"image_generation": f.ImageGeneration,
		"web_search":       f.WebSearch,
		"auto_web_search":  f.AutoWebSearch,
		"preview_mode":     f.PreviewMode,
		"enable_thinking":  f.EnableThinking,
		"vision":           f.Vision,
		"flags":            f.Flags,
		"mcp_servers":      f.MCPServers,
	}
}

// mergeWithModelConfig 将动态特性与models.json配置合并
func mergeWithModelConfig(dynamic FeatureConfig, modelConfig config.ModelConfig) FeatureConfig {
	// 如果models.json中有配置，优先使用
	if modelConfig.Capabilities.Vision {
		dynamic.Features.Vision = true
	}

	if modelConfig.Capabilities.Thinking {
		// 仅在流式模式下启用thinking
		if dynamic.Features.EnableThinking {
			dynamic.Features.EnableThinking = modelConfig.Capabilities.Thinking
		}
	}

	// 注意：config.ModelCapabilities 没有 Search 字段，这里保留为 false
	// 如果需要搜索功能，应该根据模型ID判断

	return dynamic
}

// ConvertedMessages 转换后的消息结构
type ConvertedMessages struct {
	Messages  []types.UpstreamMessage
	ImageURLs []string
	Files     []File
}

// File 文件结构
type File struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// convertMultimodalMessages 转换多模态消息（使用统一的多模态处理器）
func convertMultimodalMessages(messages []types.Message) ConvertedMessages {
	result := ConvertedMessages{
		Messages:  make([]types.UpstreamMessage, 0),
		ImageURLs: make([]string, 0),
		Files:     make([]File, 0),
	}

	processor := utils.NewMultimodalProcessor("")

	for _, msg := range messages {
		// 使用统一处理器处理内容
		processResult, err := processor.ProcessContent(msg.Content)
		
		// 创建上游消息
		upstreamMsg := types.UpstreamMessage{
			Role: msg.Role,
		}
		
		if err == nil {
			upstreamMsg.Content = processResult.Text
			
			// 收集图片URL
			result.ImageURLs = append(result.ImageURLs, processResult.Images...)
			
			// 转换文件信息
			for _, file := range processResult.Files {
				fileID := file.FileID
				if fileID == "" {
					// 如果没有FileID，使用占位符（实际使用时需要上传）
					if strings.HasPrefix(file.URL, "data:image/") {
						fileID = "base64_image"
					} else if strings.HasPrefix(file.URL, "http") {
						fileID = "url_image"
					} else {
						fileID = "unknown"
					}
				}
				
				result.Files = append(result.Files, File{
					Type: file.Type,
					ID:   fileID,
				})
			}
		} else {
			// 如果处理失败，尝试将内容转换为字符串
			if content, ok := msg.Content.(string); ok {
				upstreamMsg.Content = content
			} else {
				upstreamMsg.Content = ""
			}
		}
		
		// 添加推理内容（如果有）
		if msg.ReasoningContent != "" {
			upstreamMsg.ReasoningContent = msg.ReasoningContent
		}
		
		// 如果有内容或工具调用，添加消息
		if upstreamMsg.Content != "" || len(msg.ToolCalls) > 0 || msg.Content == nil {
			result.Messages = append(result.Messages, upstreamMsg)
		}
	}

	return result
}
