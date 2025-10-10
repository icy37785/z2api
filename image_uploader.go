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
	"z2api/types"
	"z2api/utils"
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
	fileName := fmt.Sprintf("image_%s.jpg", utils.GenerateUUID())

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

// ProcessMultimodalMessages 处理包含图片的多模态消息（使用统一的多模态处理器）
// 参考 Python 版本的 convert_messages 函数
func ProcessMultimodalMessages(messages []types.Message, authToken string) ([]types.UpstreamMessage, []map[string]interface{}, error) {
	uploader := NewImageUploader(authToken)
	processor := utils.NewMultimodalProcessor("")
	processor.EnableDebugLog = appConfig.DebugMode

	var processedMessages []types.UpstreamMessage
	var files []map[string]interface{}

	for _, msg := range messages {
		// 使用统一处理器处理内容
		result, err := processor.ProcessContent(msg.Content)

		var textContent string
		if err == nil {
			textContent = result.Text

			// 处理图片上传
			for _, imageURL := range result.Images {
				var fileID string
				var uploadErr error

				if strings.HasPrefix(imageURL, "data:image/") {
					fileID, uploadErr = uploader.UploadBase64Image(imageURL)
				} else if strings.HasPrefix(imageURL, "http") {
					fileID, uploadErr = uploader.UploadImageFromURL(imageURL)
				} else {
					debugLog("不支持的图片URL格式: %s", imageURL)
					continue
				}

				if uploadErr != nil {
					debugLog("上传图片失败: %v", uploadErr)
					continue
				}

				files = append(files, map[string]interface{}{
					"type": "image",
					"id":   fileID,
				})
			}

			// 处理其他媒体文件（如果需要支持视频、文档等）
			for _, file := range result.Files {
				if file.FileID != "" {
					// 如果已有FileID，直接使用
					files = append(files, map[string]interface{}{
						"type": file.Type,
						"id":   file.FileID,
					})
				}
				// 注意：这里可以扩展支持其他文件类型的上传
			}
		} else {
			// 如果处理失败，尝试将内容转换为字符串
			if content, ok := msg.Content.(string); ok {
				textContent = content
			} else {
				textContent = ""
			}
		}

		// 创建上游消息
		processedMessages = append(processedMessages, types.UpstreamMessage{
			Role:             normalizeRole(msg.Role),
			Content:          textContent,
			ReasoningContent: msg.ReasoningContent,
		})
	}

	return processedMessages, files, nil
}
