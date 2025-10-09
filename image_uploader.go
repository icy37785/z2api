package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/google/uuid"
)

// ImageUploader 图片上传器，参考 Python 版本的 ImageUploader 类
type ImageUploader struct {
	authToken  string
	httpClient *http.Client
	uploadURL  string
}

// NewImageUploader 创建新的图片上传器
func NewImageUploader(authToken string) *ImageUploader {
	return &ImageUploader{
		authToken: authToken,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		uploadURL: "https://chat.z.ai/api/upload", // 根据实际API调整
	}
}

// ImageUploadResponse 图片上传响应结构
type ImageUploadResponse struct {
	Success bool   `json:"success"`
	FileID  string `json:"file_id"`
	Message string `json:"message,omitempty"`
}

// UploadBase64Image 上传 base64 编码的图片
func (iu *ImageUploader) UploadBase64Image(base64Data string) (string, error) {
	// 移除 data:image/xxx;base64, 前缀（如果存在）
	if strings.HasPrefix(base64Data, "data:image/") {
		if idx := strings.Index(base64Data, ";base64,"); idx != -1 {
			base64Data = base64Data[idx+8:]
		}
	}

	// 解码 base64 数据
	imageData, err := base64.StdEncoding.DecodeString(base64Data)
	if err != nil {
		debugLog("解码 base64 图片失败: %v", err)
		return "", fmt.Errorf("解码 base64 图片失败: %w", err)
	}

	// 上传图片数据
	return iu.uploadImageData(imageData)
}

// UploadImageFromURL 从 URL 下载图片并上传
func (iu *ImageUploader) UploadImageFromURL(imageURL string) (string, error) {
	// 下载图片
	resp, err := iu.httpClient.Get(imageURL)
	if err != nil {
		debugLog("下载图片失败: %v", err)
		return "", fmt.Errorf("下载图片失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("下载图片失败，状态码: %d", resp.StatusCode)
	}

	// 读取图片数据
	imageData, err := io.ReadAll(resp.Body)
	if err != nil {
		debugLog("读取图片数据失败: %v", err)
		return "", fmt.Errorf("读取图片数据失败: %w", err)
	}

	// 上传图片数据
	return iu.uploadImageData(imageData)
}

// uploadImageData 上传图片数据到服务器
func (iu *ImageUploader) uploadImageData(imageData []byte) (string, error) {
	// 生成唯一文件名
	fileName := fmt.Sprintf("image_%s.jpg", uuid.New().String())

	// 构建上传请求
	uploadReq := map[string]interface{}{
		"filename": fileName,
		"data":     base64.StdEncoding.EncodeToString(imageData),
		"type":     "image",
	}

	// 序列化请求数据
	reqData, err := sonic.Marshal(uploadReq)
	if err != nil {
		return "", fmt.Errorf("序列化上传请求失败: %w", err)
	}

	// 创建 HTTP 请求
	req, err := http.NewRequest("POST", iu.uploadURL, bytes.NewReader(reqData))
	if err != nil {
		return "", fmt.Errorf("创建上传请求失败: %w", err)
	}

	// 设置请求头
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", iu.authToken))
	req.Header.Set("Content-Type", "application/json")

	// 发送请求
	resp, err := iu.httpClient.Do(req)
	if err != nil {
		debugLog("发送上传请求失败: %v", err)
		return "", fmt.Errorf("发送上传请求失败: %w", err)
	}
	defer resp.Body.Close()

	// 读取响应
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("读取上传响应失败: %w", err)
	}

	// 解析响应
	var uploadResp ImageUploadResponse
	if err := sonic.Unmarshal(respBody, &uploadResp); err != nil {
		debugLog("解析上传响应失败: %v, 原始响应: %s", err, string(respBody))
		return "", fmt.Errorf("解析上传响应失败: %w", err)
	}

	if !uploadResp.Success {
		return "", fmt.Errorf("图片上传失败: %s", uploadResp.Message)
	}

	debugLog("图片上传成功，文件ID: %s", uploadResp.FileID)
	return uploadResp.FileID, nil
}

// ProcessMultimodalMessages 处理包含图片的多模态消息
// 参考 Python 版本的 convert_messages 函数
func ProcessMultimodalMessages(messages []Message, authToken string) ([]UpstreamMessage, []map[string]interface{}, error) {
	uploader := NewImageUploader(authToken)
	var processedMessages []UpstreamMessage
	var files []map[string]interface{}

	for _, msg := range messages {
		// 处理字符串内容
		if content, ok := msg.Content.(string); ok {
			processedMessages = append(processedMessages, UpstreamMessage{
				Role:             normalizeRole(msg.Role),
				Content:          content,
				ReasoningContent: msg.ReasoningContent,
			})
			continue
		}

		// 处理多模态内容
		if contentParts, ok := msg.Content.([]interface{}); ok {
			var textContent strings.Builder

			for _, part := range contentParts {
				partMap, ok := part.(map[string]interface{})
				if !ok {
					continue
				}

				partType, _ := partMap["type"].(string)

				switch partType {
				case "text":
					if text, ok := partMap["text"].(string); ok {
						textContent.WriteString(text)
						textContent.WriteString(" ")
					}

				case "image_url":
					if imageURLMap, ok := partMap["image_url"].(map[string]interface{}); ok {
						if url, ok := imageURLMap["url"].(string); ok {
							// 处理 base64 图片
							if strings.HasPrefix(url, "data:image/") {
								fileID, err := uploader.UploadBase64Image(url)
								if err != nil {
									debugLog("上传 base64 图片失败: %v", err)
									continue
								}
								files = append(files, map[string]interface{}{
									"type": "image",
									"id":   fileID,
								})
							} else if strings.HasPrefix(url, "http") {
								// 处理网络图片
								fileID, err := uploader.UploadImageFromURL(url)
								if err != nil {
									debugLog("上传网络图片失败: %v", err)
									continue
								}
								files = append(files, map[string]interface{}{
									"type": "image",
									"id":   fileID,
								})
							}
						}
					}
				}
			}

			processedMessages = append(processedMessages, UpstreamMessage{
				Role:             normalizeRole(msg.Role),
				Content:          textContent.String(),
				ReasoningContent: msg.ReasoningContent,
			})
		}

		// 处理 ContentPart 数组（已有的格式）
		if contentParts, ok := msg.Content.([]ContentPart); ok {
			textContent := processMultimodalContent(contentParts, "")

			// 处理图片
			for _, part := range contentParts {
				if part.Type == "image_url" && part.ImageURL != nil {
					url := part.ImageURL.URL

					if strings.HasPrefix(url, "data:image/") {
						fileID, err := uploader.UploadBase64Image(url)
						if err != nil {
							debugLog("上传 base64 图片失败: %v", err)
							continue
						}
						files = append(files, map[string]interface{}{
							"type": "image",
							"id":   fileID,
						})
					} else if strings.HasPrefix(url, "http") {
						fileID, err := uploader.UploadImageFromURL(url)
						if err != nil {
							debugLog("上传网络图片失败: %v", err)
							continue
						}
						files = append(files, map[string]interface{}{
							"type": "image",
							"id":   fileID,
						})
					}
				}
			}

			processedMessages = append(processedMessages, UpstreamMessage{
				Role:             normalizeRole(msg.Role),
				Content:          textContent,
				ReasoningContent: msg.ReasoningContent,
			})
		}
	}

	return processedMessages, files, nil
}
