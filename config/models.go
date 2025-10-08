package config

import (
	"fmt"
	"os"
	"strings"

	json "github.com/bytedance/sonic"
)

// ModelCapabilities 定义了模型的能力
type ModelCapabilities struct {
	Vision   bool `json:"vision"`
	Tools    bool `json:"tools"`
	Thinking bool `json:"thinking"`
}

// ModelConfig 定义了单个模型的完整配置
type ModelConfig struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	UpstreamID   string            `json:"upstream_id"`
	Capabilities ModelCapabilities `json:"capabilities"`
}

// ModelsData 包含从 models.json 加载的所有数据
type ModelsData struct {
	DefaultModelID string            `json:"default_model_id"`
	Mappings       map[string]string `json:"model_mappings"`
	Models         []ModelConfig     `json:"models"`
	// 为了快速查找，我们创建一个map
	modelMap map[string]ModelConfig
}

var modelData *ModelsData

// LoadModels 加载并解析 models.json 文件
func LoadModels(path string) error {
	file, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("failed to read models file: %w", err)
	}

	var data ModelsData
	if err := json.Unmarshal(file, &data); err != nil {
		return fmt.Errorf("failed to parse models JSON: %w", err)
	}

	// 验证配置是否有效
	if len(data.Models) == 0 {
		return fmt.Errorf("no models were loaded")
	}

	// 将模型列表转换为map以便快速查找
	data.modelMap = make(map[string]ModelConfig)
	for _, model := range data.Models {
		data.modelMap[strings.ToLower(model.ID)] = model
	}

	modelData = &data
	return nil
}

// normalizeModelID 将客户端传入的模型ID标准化
func normalizeModelID(id string) string {
	if modelData == nil || modelData.Mappings == nil {
		return strings.ToLower(strings.TrimSpace(id)) // 如果配置未加载，返回标准化的原ID
	}

	normalizedID := strings.ToLower(strings.TrimSpace(id))
	if mappedID, ok := modelData.Mappings[normalizedID]; ok {
		return mappedID
	}
	return normalizedID // 如果没有匹配的映射，返回标准化的原ID
}

// GetModelConfig 根据模型ID获取配置
func GetModelConfig(id string) (ModelConfig, bool) {
	if modelData == nil || modelData.modelMap == nil {
		return ModelConfig{}, false // 配置未加载
	}

	normalizedID := normalizeModelID(id)

	config, ok := modelData.modelMap[normalizedID]
	return config, ok
}

// GetDefaultModel 获取默认模型 (根据 default_model_id)
func GetDefaultModel() (ModelConfig, bool) {
	if modelData == nil || len(modelData.Models) == 0 {
		return ModelConfig{}, false
	}

	// 如果设置了 DefaultModelID，使用它来查找模型
	if modelData.DefaultModelID != "" {
		normalizedID := strings.ToLower(modelData.DefaultModelID)
		if config, ok := modelData.modelMap[normalizedID]; ok {
			return config, true
		}
	}

	// 如果找不到指定的默认模型，回退到第一个模型
	return modelData.Models[0], true
}

// GetAllModels returns a slice of all loaded model configurations.
func GetAllModels() []ModelConfig {
	if modelData == nil {
		return []ModelConfig{}
	}
	return modelData.Models
}
