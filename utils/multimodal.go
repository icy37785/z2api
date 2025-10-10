package utils

import (
	"fmt"
	"strings"
	"z2api/types"
)

// MultimodalProcessor 多模态内容处理器
type MultimodalProcessor struct {
	// 配置选项
	EnableDebugLog bool
	Model          string
}

// ProcessResult 处理结果
type ProcessResult struct {
	Text      string              // 提取的文本内容
	Images    []string            // 图片URL列表
	Videos    []string            // 视频URL列表
	Documents []string            // 文档URL列表
	Audios    []string            // 音频URL列表
	Files     []ProcessedFile     // 处理后的文件信息
}

// ProcessedFile 处理后的文件
type ProcessedFile struct {
	Type   string // 文件类型：image, video, document, audio
	URL    string // 原始URL
	FileID string // 上传后的文件ID（如果有）
}

// NewMultimodalProcessor 创建新的多模态处理器
func NewMultimodalProcessor(model string) *MultimodalProcessor {
	return &MultimodalProcessor{
		Model:          model,
		EnableDebugLog: false,
	}
}

// ProcessContent 处理多模态内容，支持多种输入格式
func (p *MultimodalProcessor) ProcessContent(content interface{}) (*ProcessResult, error) {
	result := &ProcessResult{
		Images:    make([]string, 0),
		Videos:    make([]string, 0),
		Documents: make([]string, 0),
		Audios:    make([]string, 0),
		Files:     make([]ProcessedFile, 0),
	}

	switch v := content.(type) {
	case string:
		// 简单文本内容
		result.Text = v
		return result, nil

	case []types.ContentPart:
		// 处理 ContentPart 数组（标准格式）
		return p.processContentParts(v)

	case []interface{}:
		// 处理通用接口数组
		return p.processInterfaceArray(v)

	default:
		// 尝试转换为字符串
		result.Text = fmt.Sprintf("%v", content)
		return result, nil
	}
}

// processContentParts 处理 ContentPart 数组
func (p *MultimodalProcessor) processContentParts(parts []types.ContentPart) (*ProcessResult, error) {
	result := &ProcessResult{
		Images:    make([]string, 0),
		Videos:    make([]string, 0),
		Documents: make([]string, 0),
		Audios:    make([]string, 0),
		Files:     make([]ProcessedFile, 0),
	}

	var textBuilder strings.Builder
	textBuilder.Grow(256) // 预分配容量

	for _, part := range parts {
		switch part.Type {
		case "text":
			if part.Text != "" {
				if textBuilder.Len() > 0 {
					textBuilder.WriteString(" ")
				}
				textBuilder.WriteString(part.Text)
			}

		case "image_url":
			if part.ImageURL != nil && part.ImageURL.URL != "" {
				url := part.ImageURL.URL
				result.Images = append(result.Images, url)
				result.Files = append(result.Files, ProcessedFile{
					Type: "image",
					URL:  url,
				})
				
				if p.EnableDebugLog {
					p.logDebug("检测到图像内容: %s (detail: %s)", url, part.ImageURL.Detail)
				}
			}

		case "video_url":
			if part.VideoURL != nil && part.VideoURL.URL != "" {
				url := part.VideoURL.URL
				result.Videos = append(result.Videos, url)
				result.Files = append(result.Files, ProcessedFile{
					Type: "video",
					URL:  url,
				})
				
				if p.EnableDebugLog {
					p.logDebug("检测到视频内容: %s", url)
				}
			}

		case "document_url":
			if part.DocumentURL != nil && part.DocumentURL.URL != "" {
				url := part.DocumentURL.URL
				result.Documents = append(result.Documents, url)
				result.Files = append(result.Files, ProcessedFile{
					Type: "document",
					URL:  url,
				})
				
				if p.EnableDebugLog {
					p.logDebug("检测到文档内容: %s", url)
				}
			}

		case "audio_url":
			if part.AudioURL != nil && part.AudioURL.URL != "" {
				url := part.AudioURL.URL
				result.Audios = append(result.Audios, url)
				result.Files = append(result.Files, ProcessedFile{
					Type: "audio",
					URL:  url,
				})
				
				if p.EnableDebugLog {
					p.logDebug("检测到音频内容: %s", url)
				}
			}

		default:
			if p.EnableDebugLog {
				p.logDebug("检测到未知内容类型: %s", part.Type)
			}
		}
	}

	result.Text = textBuilder.String()
	
	// 记录统计信息
	if p.EnableDebugLog {
		p.logStats(result)
	}

	return result, nil
}

// processInterfaceArray 处理通用接口数组
func (p *MultimodalProcessor) processInterfaceArray(parts []interface{}) (*ProcessResult, error) {
	result := &ProcessResult{
		Images:    make([]string, 0),
		Videos:    make([]string, 0),
		Documents: make([]string, 0),
		Audios:    make([]string, 0),
		Files:     make([]ProcessedFile, 0),
	}

	var textBuilder strings.Builder
	textBuilder.Grow(256)

	for _, part := range parts {
		partMap, ok := part.(map[string]interface{})
		if !ok {
			continue
		}

		partType, _ := partMap["type"].(string)

		switch partType {
		case "text":
			if text, ok := partMap["text"].(string); ok {
				if textBuilder.Len() > 0 {
					textBuilder.WriteString(" ")
				}
				textBuilder.WriteString(text)
			}

		case "image_url":
			if imageURL, ok := partMap["image_url"].(map[string]interface{}); ok {
				if url, ok := imageURL["url"].(string); ok {
					result.Images = append(result.Images, url)
					result.Files = append(result.Files, ProcessedFile{
						Type: "image",
						URL:  url,
					})
					
					if p.EnableDebugLog {
						detail, _ := imageURL["detail"].(string)
						p.logDebug("检测到图像内容: %s (detail: %s)", url, detail)
					}
				}
			}

		case "video_url":
			if videoURL, ok := partMap["video_url"].(map[string]interface{}); ok {
				if url, ok := videoURL["url"].(string); ok {
					result.Videos = append(result.Videos, url)
					result.Files = append(result.Files, ProcessedFile{
						Type: "video",
						URL:  url,
					})
					
					if p.EnableDebugLog {
						p.logDebug("检测到视频内容: %s", url)
					}
				}
			}

		case "document_url":
			if docURL, ok := partMap["document_url"].(map[string]interface{}); ok {
				if url, ok := docURL["url"].(string); ok {
					result.Documents = append(result.Documents, url)
					result.Files = append(result.Files, ProcessedFile{
						Type: "document",
						URL:  url,
					})
					
					if p.EnableDebugLog {
						p.logDebug("检测到文档内容: %s", url)
					}
				}
			}

		case "audio_url":
			if audioURL, ok := partMap["audio_url"].(map[string]interface{}); ok {
				if url, ok := audioURL["url"].(string); ok {
					result.Audios = append(result.Audios, url)
					result.Files = append(result.Files, ProcessedFile{
						Type: "audio",
						URL:  url,
					})
					
					if p.EnableDebugLog {
						p.logDebug("检测到音频内容: %s", url)
					}
				}
			}

		case "file":
			// 处理通用文件类型
			if fileID, ok := partMap["file_id"].(string); ok {
				fileType := "document"
				if ft, ok := partMap["file_type"].(string); ok {
					fileType = ft
				}
				result.Files = append(result.Files, ProcessedFile{
					Type:   fileType,
					FileID: fileID,
				})
			}

		default:
			if p.EnableDebugLog {
				p.logDebug("检测到未知内容类型: %s", partType)
			}
		}
	}

	result.Text = textBuilder.String()
	
	// 记录统计信息
	if p.EnableDebugLog {
		p.logStats(result)
	}

	return result, nil
}

// ExtractText 仅提取文本内容（便捷方法）
func (p *MultimodalProcessor) ExtractText(content interface{}) string {
	result, err := p.ProcessContent(content)
	if err != nil {
		return ""
	}
	return result.Text
}

// ExtractImages 仅提取图片URL（便捷方法）
func (p *MultimodalProcessor) ExtractImages(content interface{}) []string {
	result, err := p.ProcessContent(content)
	if err != nil {
		return []string{}
	}
	return result.Images
}

// HasMultimedia 检查是否包含多媒体内容
func (p *MultimodalProcessor) HasMultimedia(content interface{}) bool {
	result, err := p.ProcessContent(content)
	if err != nil {
		return false
	}
	
	totalMedia := len(result.Images) + len(result.Videos) + 
		len(result.Documents) + len(result.Audios)
	
	return totalMedia > 0
}

// GetMediaStats 获取媒体统计信息
func (p *MultimodalProcessor) GetMediaStats(content interface{}) map[string]int {
	result, _ := p.ProcessContent(content)
	
	return map[string]int{
		"text":      len(strings.TrimSpace(result.Text)),
		"images":    len(result.Images),
		"videos":    len(result.Videos),
		"documents": len(result.Documents),
		"audios":    len(result.Audios),
		"total":     len(result.Files),
	}
}

// logDebug 记录调试日志
func (p *MultimodalProcessor) logDebug(format string, args ...interface{}) {
	if p.EnableDebugLog {
		LogDebug(fmt.Sprintf("[MultimodalProcessor] "+format, args...))
	}
}

// logStats 记录统计信息
func (p *MultimodalProcessor) logStats(result *ProcessResult) {
	totalMedia := len(result.Images) + len(result.Videos) + 
		len(result.Documents) + len(result.Audios)
	
	if totalMedia > 0 {
		p.logDebug("多模态内容统计: 文本长度(%d) 图像(%d) 视频(%d) 文档(%d) 音频(%d)",
			len(result.Text), len(result.Images), len(result.Videos), 
			len(result.Documents), len(result.Audios))
	}
	
	// 检查模型支持
	if p.Model != "" {
		modelLower := strings.ToLower(p.Model)
		if strings.Contains(modelLower, "vision") || strings.Contains(modelLower, "v") {
			p.logDebug("模型 %s 支持全方位多模态理解", p.Model)
		} else if totalMedia > 0 {
			p.logDebug("模型 %s 可能不支持多模态，仅保留文本内容", p.Model)
		}
	}
}

// ConvertToUpstreamMessage 将处理结果转换为上游消息格式
func (p *MultimodalProcessor) ConvertToUpstreamMessage(msg types.Message) (types.UpstreamMessage, []ProcessedFile) {
	result, _ := p.ProcessContent(msg.Content)
	
	return types.UpstreamMessage{
		Role:             msg.Role,
		Content:          result.Text,
		ReasoningContent: msg.ReasoningContent,
	}, result.Files
}