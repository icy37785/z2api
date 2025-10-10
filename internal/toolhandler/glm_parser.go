package toolhandler

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/bytedance/sonic"
)

// GlmBlock 表示一个GLM块的结构
type GlmBlock struct {
	RawContent string                 // 原始内容
	Data       map[string]interface{} // 解析后的数据
}

// ToolCallInfo 工具调用信息
type ToolCallInfo struct {
	ID        string                 // 工具调用ID
	Name      string                 // 工具名称
	Arguments map[string]interface{} // 参数（已解析为map）
	ArgsRaw   string                 // 原始参数字符串
}

// GlmBlockParser GLM块解析器
type GlmBlockParser struct {
	blockPattern *regexp.Regexp // 用于匹配glm_block标签的正则表达式
}

// NewGlmBlockParser 创建新的GLM块解析器
func NewGlmBlockParser() *GlmBlockParser {
	// 匹配 <glm_block>...</glm_block> 或不完整的块
	// 使用(?s)标志使.匹配换行符
	pattern := regexp.MustCompile(`(?s)<glm_block\s*>(.*?)(?:</glm_block>|$)`)

	return &GlmBlockParser{
		blockPattern: pattern,
	}
}

// ExtractBlocks 从内容中提取所有GLM块
// 返回所有找到的块，包括不完整的块
func (p *GlmBlockParser) ExtractBlocks(content string) []GlmBlock {
	matches := p.blockPattern.FindAllStringSubmatch(content, -1)
	blocks := make([]GlmBlock, 0, len(matches))

	for _, match := range matches {
		if len(match) > 1 {
			blockContent := match[1]
			blocks = append(blocks, GlmBlock{
				RawContent: blockContent,
				Data:       nil, // 延迟解析
			})
		}
	}

	return blocks
}

// ParseToolCall 从GLM块中解析工具调用信息
// 返回工具调用信息和可能的错误
func (p *GlmBlockParser) ParseToolCall(block GlmBlock) (*ToolCallInfo, error) {
	// 修复JSON结构
	fixedContent := p.fixJSONStructure(block.RawContent)

	// 尝试解析JSON
	var toolData map[string]interface{}
	if err := sonic.UnmarshalString(fixedContent, &toolData); err != nil {
		return nil, fmt.Errorf("JSON解析失败: %w", err)
	}

	// 提取metadata
	data, ok := toolData["data"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("缺少data字段")
	}

	metadata, ok := data["metadata"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("缺少metadata字段")
	}

	// 提取工具信息
	toolID, _ := metadata["id"].(string)
	toolName, _ := metadata["name"].(string)
	argumentsRaw, _ := metadata["arguments"].(string)

	if toolID == "" || toolName == "" {
		return nil, fmt.Errorf("工具ID或名称为空")
	}

	// 解析参数
	arguments, err := p.parseArguments(argumentsRaw)
	if err != nil {
		// 参数解析失败不是致命错误，返回空参数
		arguments = make(map[string]interface{})
	}

	return &ToolCallInfo{
		ID:        toolID,
		Name:      toolName,
		Arguments: arguments,
		ArgsRaw:   argumentsRaw,
	}, nil
}

// ParsePartialToolCall 尝试从不完整的块中提取工具信息
// 用于处理流式传输中的部分数据
func (p *GlmBlockParser) ParsePartialToolCall(blockContent string) (*ToolCallInfo, error) {
	// 尝试提取工具ID和名称
	idPattern := regexp.MustCompile(`"id":\s*"([^"]+)"`)
	namePattern := regexp.MustCompile(`"name":\s*"([^"]+)"`)
	argsPattern := regexp.MustCompile(`"arguments":\s*"([^"]*)"?`)

	idMatch := idPattern.FindStringSubmatch(blockContent)
	nameMatch := namePattern.FindStringSubmatch(blockContent)

	if len(idMatch) < 2 || len(nameMatch) < 2 {
		return nil, fmt.Errorf("无法提取工具ID或名称")
	}

	toolID := idMatch[1]
	toolName := nameMatch[1]

	// 尝试提取参数
	var argsRaw string
	argsMatch := argsPattern.FindStringSubmatch(blockContent)
	if len(argsMatch) > 1 {
		argsRaw = argsMatch[1]
	}

	// 尝试解析参数
	arguments, _ := p.parseArguments(argsRaw)
	if arguments == nil {
		arguments = make(map[string]interface{})
	}

	return &ToolCallInfo{
		ID:        toolID,
		Name:      toolName,
		Arguments: arguments,
		ArgsRaw:   argsRaw,
	}, nil
}

// fixJSONStructure 修复JSON结构中的常见问题
// 参考Python版本的实现
func (p *GlmBlockParser) fixJSONStructure(content string) string {
	if content == "" {
		return content
	}

	// 计算括号平衡
	openBraces := strings.Count(content, "{")
	closeBraces := strings.Count(content, "}")

	// 如果闭括号多于开括号，移除多余的闭括号
	if closeBraces > openBraces {
		excess := closeBraces - openBraces
		fixed := content
		for i := 0; i < excess; i++ {
			// 从右侧移除多余的闭括号
			lastBracePos := strings.LastIndex(fixed, "}")
			if lastBracePos != -1 {
				fixed = fixed[:lastBracePos] + fixed[lastBracePos+1:]
			}
		}
		return fixed
	}

	return content
}

// parseArguments 解析参数字符串
// 支持多种格式：JSON字符串、转义的JSON、普通字符串等
func (p *GlmBlockParser) parseArguments(argumentsRaw string) (map[string]interface{}, error) {
	if argumentsRaw == "" || strings.TrimSpace(argumentsRaw) == "" {
		return make(map[string]interface{}), nil
	}

	// 清理参数字符串
	cleaned := p.cleanArgumentsString(argumentsRaw)

	// 尝试解析为JSON
	var result map[string]interface{}
	if err := sonic.UnmarshalString(cleaned, &result); err != nil {
		// 如果解析失败，尝试修复不完整的JSON
		fixed := p.fixIncompleteJSON(cleaned)
		if err := sonic.UnmarshalString(fixed, &result); err != nil {
			// 最后尝试提取键值对
			return p.extractKeyValuePairs(argumentsRaw), nil
		}
	}

	return result, nil
}

// cleanArgumentsString 清理和标准化参数字符串
func (p *GlmBlockParser) cleanArgumentsString(argumentsRaw string) string {
	if argumentsRaw == "" {
		return "{}"
	}

	cleaned := strings.TrimSpace(argumentsRaw)

	// 处理特殊值
	if strings.ToLower(cleaned) == "null" {
		return "{}"
	}

	// 处理转义的JSON字符串
	if strings.HasPrefix(cleaned, `{\"`) && strings.HasSuffix(cleaned, `\"}`) {
		cleaned = strings.ReplaceAll(cleaned, `\"`, `"`)
	} else if strings.HasPrefix(cleaned, `"{\"`) && strings.HasSuffix(cleaned, `\"}"`) {
		// 双重转义
		cleaned = cleaned[1 : len(cleaned)-1]
		cleaned = strings.ReplaceAll(cleaned, `\"`, `"`)
	} else if strings.HasPrefix(cleaned, `"`) && strings.HasSuffix(cleaned, `"`) {
		// 简单的引号包围
		cleaned = cleaned[1 : len(cleaned)-1]
	}

	return cleaned
}

// fixIncompleteJSON 修复不完整的JSON字符串
func (p *GlmBlockParser) fixIncompleteJSON(jsonStr string) string {
	if jsonStr == "" {
		return "{}"
	}

	// 确保以{开头
	if !strings.HasPrefix(jsonStr, "{") {
		jsonStr = "{" + jsonStr
	}

	// 处理不完整的字符串值
	quoteCount := strings.Count(jsonStr, `"`) - strings.Count(jsonStr, `\"`)
	if quoteCount%2 != 0 {
		// 奇数个引号，可能有未闭合的字符串
		jsonStr += `"`
	}

	// 确保以}结尾
	if !strings.HasSuffix(jsonStr, "}") {
		jsonStr += "}"
	}

	return jsonStr
}

// extractKeyValuePairs 从文本中提取键值对
// 作为最后的解析尝试
func (p *GlmBlockParser) extractKeyValuePairs(text string) map[string]interface{} {
	result := make(map[string]interface{})

	// 匹配 "key": "value" 格式
	stringPattern := regexp.MustCompile(`"([^"]+)":\s*"([^"]*)"`)
	matches := stringPattern.FindAllStringSubmatch(text, -1)
	for _, match := range matches {
		if len(match) > 2 {
			result[match[1]] = match[2]
		}
	}

	// 匹配数字值
	numberPattern := regexp.MustCompile(`"([^"]+)":\s*(\d+)`)
	matches = numberPattern.FindAllStringSubmatch(text, -1)
	for _, match := range matches {
		if len(match) > 2 {
			// 尝试解析为数字，失败则保持字符串
			var num interface{}
			if err := sonic.UnmarshalString(match[2], &num); err == nil {
				result[match[1]] = num
			}
		}
	}

	// 匹配布尔值
	boolPattern := regexp.MustCompile(`"([^"]+)":\s*(true|false)`)
	matches = boolPattern.FindAllStringSubmatch(text, -1)
	for _, match := range matches {
		if len(match) > 2 {
			result[match[1]] = match[2] == "true"
		}
	}

	return result
}
