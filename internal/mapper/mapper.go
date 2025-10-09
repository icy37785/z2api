package mapper

import (
	"strings"
	"z2api/config"
)

// 模型映射表 - 优雅地定义模型映射关系
var modelMappings = map[string]string{
	"glm-4.6":        "GLM-4-6-API-V1",
	"glm-4.5":        "0727-360B-API",
	"glm-4.5v":       "glm-4.5v",
	"glm-4.5-air":    "0727-106B-API",
	"glm-4.5-search": "0727-360B-API", // search 模型使用 4.5
}

// GetSimpleModelConfig 简洁的模型配置获取函数
func GetSimpleModelConfig(modelID string) config.ModelConfig {
	// 标准化模型ID
	normalizedID := strings.ToLower(strings.TrimSpace(modelID))

	// 获取上游ID
	upstreamID := modelID // 默认使用原始ID
	if mapped, ok := modelMappings[normalizedID]; ok {
		upstreamID = mapped
	}

	// 根据模型名称推断能力
	capabilities := config.ModelCapabilities{
		Vision:   false,
		Tools:    true, // 默认支持工具
		Thinking: true, // 默认支持思考（流式时启用）
	}

	// 特殊模型能力设置
	switch normalizedID {
	case "glm-4.5v":
		capabilities.Vision = true
		capabilities.Tools = false // 视觉模型暂不支持工具
	case "glm-4.5-air":
		capabilities.Thinking = false // Air 模型不支持思考
		capabilities.Tools = false
	case "glm-4.6", "glm-4.5-thinking":
		capabilities.Thinking = true
		capabilities.Tools = true
	}

	// 通过模型名称推断额外能力
	if strings.Contains(normalizedID, "vision") || strings.Contains(normalizedID, "4v") {
		capabilities.Vision = true
	}
	if strings.Contains(normalizedID, "nothinking") || strings.Contains(normalizedID, "air") {
		capabilities.Thinking = false
	}

	return config.ModelConfig{
		ID:           modelID,
		Name:         FormatModelName(modelID),
		UpstreamID:   upstreamID,
		Capabilities: capabilities,
	}
}

// FormatModelName 格式化模型显示名称
func FormatModelName(modelID string) string {
	// 将模型ID转换为更友好的显示名称
	name := strings.ToUpper(modelID)
	name = strings.ReplaceAll(name, "-", " ")
	name = strings.ReplaceAll(name, ".", "")

	// 特殊处理
	switch strings.ToLower(modelID) {
	case "glm-4.5":
		return "GLM 4.5"
	case "glm-4.5v":
		return "GLM 4.5 Vision"
	case "glm-4.5-air":
		return "GLM 4.5 Air"
	case "glm-4.6":
		return "GLM 4.6"
	default:
		return name
	}
}
