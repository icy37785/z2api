package config

import (
	"os"
	"testing"
)

func TestLoadModels(t *testing.T) {
	// 测试正常加载
	if err := LoadModels("../models.json"); err != nil {
		t.Fatalf("加载模型配置失败: %v", err)
	}

	// 验证模型数据是否正确加载
	if modelData == nil {
		t.Fatal("modelData 不应为 nil")
	}

	if len(modelData.Models) == 0 {
		t.Fatal("应该至少加载一个模型")
	}

	// 验证默认模型ID是否正确加载
	if modelData.DefaultModelID == "" {
		t.Error("DefaultModelID 不应为空")
	}

	// 验证模型映射是否正确加载
	if len(modelData.Mappings) == 0 {
		t.Error("应该至少有一个模型映射")
	}
}

func TestGetDefaultModel(t *testing.T) {
	// 确保模型已加载
	if err := LoadModels("../models.json"); err != nil {
		t.Fatalf("加载模型配置失败: %v", err)
	}

	// 测试获取默认模型
	defaultModel, exists := GetDefaultModel()
	if !exists {
		t.Fatal("应该能获取到默认模型")
	}

	// 验证默认模型的ID是否与配置的DefaultModelID匹配
	// 注意：由于我们的DefaultModelID设置为"claude-3-opus-20240229"，但这个模型不存在于models列表中
	// 所以根据我们的实现，应该返回第一个模型作为默认值
	if defaultModel.ID != modelData.Models[0].ID {
		t.Errorf("默认模型ID不匹配，期望 %s，实际 %s", modelData.Models[0].ID, defaultModel.ID)
	}
}

func TestGetModelConfig(t *testing.T) {
	// 确保模型已加载
	if err := LoadModels("../models.json"); err != nil {
		t.Fatalf("加载模型配置失败: %v", err)
	}

	// 测试直接获取存在的模型
	if model, exists := GetModelConfig("glm-4.6"); !exists {
		t.Error("应该能找到 glm-4.6 模型")
	} else {
		if model.ID != "glm-4.6" {
			t.Errorf("模型ID不匹配，期望 glm-4.6，实际 %s", model.ID)
		}
	}

	// 测试通过映射获取模型
	if model, exists := GetModelConfig("gpt-4"); !exists {
		t.Error("应该能通过映射找到 gpt-4 对应的模型")
	} else {
		if model.ID != "glm-4.5" {
			t.Errorf("映射的模型ID不匹配，期望 glm-4.5，实际 %s", model.ID)
		}
	}

	// 测试获取不存在的模型
	if _, exists := GetModelConfig("non-existent-model"); exists {
		t.Error("不应该找到不存在的模型")
	}
}

func TestLoadModelsErrorHandling(t *testing.T) {
	// 测试加载不存在的文件
	if err := LoadModels("non-existent-file.json"); err == nil {
		t.Error("加载不存在的文件应该返回错误")
	}

	// 创建一个空的临时文件用于测试
	tmpFile, err := os.CreateTemp("", "empty-models-*.json")
	if err != nil {
		t.Fatalf("创建临时文件失败: %v", err)
	}
	defer os.Remove(tmpFile.Name())

	// 写入空的模型数组
	if _, err := tmpFile.Write([]byte(`{"models": []}`)); err != nil {
		t.Fatalf("写入临时文件失败: %v", err)
	}
	tmpFile.Close()

	// 测试加载空模型数组
	if err := LoadModels(tmpFile.Name()); err == nil {
		t.Error("加载空模型数组应该返回错误")
	} else {
		expectedError := "no models were loaded"
		if err.Error() != expectedError {
			t.Errorf("错误消息不匹配，期望 '%s'，实际 '%s'", expectedError, err.Error())
		}
	}

	// 创建一个无效的JSON文件
	invalidFile, err := os.CreateTemp("", "invalid-models-*.json")
	if err != nil {
		t.Fatalf("创建临时文件失败: %v", err)
	}
	defer os.Remove(invalidFile.Name())

	// 写入无效的JSON
	if _, err := invalidFile.Write([]byte(`{"models": [`)); err != nil {
		t.Fatalf("写入临时文件失败: %v", err)
	}
	invalidFile.Close()

	// 测试加载无效JSON
	if err := LoadModels(invalidFile.Name()); err == nil {
		t.Error("加载无效JSON应该返回错误")
	}
}
