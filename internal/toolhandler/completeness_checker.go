package toolhandler

import (
	"strings"
)

// CompletenessChecker 完整性检查器
// 用于检查工具调用参数是否完整，避免发送不完整的工具调用
type CompletenessChecker struct{}

// NewCompletenessChecker 创建新的完整性检查器
func NewCompletenessChecker() *CompletenessChecker {
	return &CompletenessChecker{}
}

// IsArgumentsComplete 检查参数是否看起来完整
// 参考Python版本的实现逻辑
func (c *CompletenessChecker) IsArgumentsComplete(arguments map[string]interface{}, argumentsRaw string) bool {
	// 空参数检查
	if len(arguments) == 0 {
		return false
	}

	// 检查原始字符串是否看起来完整
	if argumentsRaw == "" || strings.TrimSpace(argumentsRaw) == "" {
		return false
	}

	// 检查是否有明显的截断迹象
	rawStripped := strings.TrimSpace(argumentsRaw)

	// 如果原始字符串不以}或"结尾，可能是截断的
	if !strings.HasSuffix(rawStripped, "}") && !strings.HasSuffix(rawStripped, `"`) {
		return false
	}

	// 检查每个参数值是否完整
	for _, value := range arguments {
		if strValue, ok := value.(string); ok {
			if !c.isStringValueComplete(strValue) {
				return false
			}
		}
	}

	return true
}

// isStringValueComplete 检查字符串值是否完整
func (c *CompletenessChecker) isStringValueComplete(value string) bool {
	// 检查URL是否看起来完整
	if strings.Contains(strings.ToLower(value), "http") {
		// 如果URL太短或以不完整的域名结尾，可能是截断的
		if len(value) < 10 {
			return false
		}
		// 检查常见的不完整URL模式
		if strings.HasSuffix(value, ".go") || strings.HasSuffix(value, ".goo") {
			return false
		}
	}

	// 检查其他可能的截断迹象
	if len(value) > 0 {
		lastChar := value[len(value)-1]
		// 以这些字符结尾可能表示截断
		if lastChar == '.' || lastChar == '/' || lastChar == ':' || lastChar == '=' {
			return false
		}
	}

	return true
}

// IsSignificantImprovement 检查新参数是否比旧参数有显著改进
// 用于决定是否需要发送参数更新
func (c *CompletenessChecker) IsSignificantImprovement(
	oldArgs map[string]interface{},
	newArgs map[string]interface{},
	oldRaw string,
	newRaw string,
) bool {
	// 如果新参数为空，不是改进
	if len(newArgs) == 0 {
		return false
	}

	// 如果新参数有更多键，是改进
	if len(newArgs) > len(oldArgs) {
		return true
	}

	// 检查值的改进
	for key, newValue := range newArgs {
		oldValue, exists := oldArgs[key]

		if !exists {
			// 新增的键
			return true
		}

		// 比较字符串值
		newStr, newIsStr := newValue.(string)
		oldStr, oldIsStr := oldValue.(string)

		if newIsStr && oldIsStr {
			// 如果新值明显更长且更完整，是改进
			if len(newStr) > len(oldStr)+5 { // 至少长5个字符才算显著改进
				return true
			}

			// 如果旧值看起来是截断的，新值更完整，是改进
			if c.isValueTruncated(oldStr) && len(newStr) > len(oldStr) {
				return true
			}
		}
	}

	// 检查原始字符串的改进
	if len(newRaw) > len(oldRaw)+10 { // 原始字符串显著增长
		return true
	}

	return false
}

// isValueTruncated 检查值是否看起来被截断
func (c *CompletenessChecker) isValueTruncated(value string) bool {
	if value == "" {
		return false
	}

	// 检查常见的截断模式
	truncatedSuffixes := []string{".go", ".goo", ".com/", "http"}
	for _, suffix := range truncatedSuffixes {
		if strings.HasSuffix(value, suffix) {
			return true
		}
	}

	return false
}

// ShouldSendArgumentUpdate 判断是否应该发送参数更新
// 更严格的标准，避免频繁的微小更新
func (c *CompletenessChecker) ShouldSendArgumentUpdate(
	lastSent map[string]interface{},
	newArgs map[string]interface{},
) bool {
	// 如果参数完全相同，不发送
	if c.argsEqual(lastSent, newArgs) {
		return false
	}

	// 如果新参数为空但之前有参数，不发送（避免倒退）
	if len(newArgs) == 0 && len(lastSent) > 0 {
		return false
	}

	// 如果新参数有更多键，发送更新
	if len(newArgs) > len(lastSent) {
		return true
	}

	// 检查是否有值变得显著更完整
	for key, newValue := range newArgs {
		lastValue, exists := lastSent[key]

		if !exists {
			// 新增的键
			return true
		}

		newStr, newIsStr := newValue.(string)
		lastStr, lastIsStr := lastValue.(string)

		if newIsStr && lastIsStr {
			// 只有在值显著增长时才发送更新（避免微小变化）
			if len(newStr) > len(lastStr)+5 {
				return true
			}
		} else if newValue != lastValue && newValue != nil {
			// 确保新值不为空
			return true
		}
	}

	return false
}

// argsEqual 比较两个参数map是否相等
func (c *CompletenessChecker) argsEqual(a, b map[string]interface{}) bool {
	if len(a) != len(b) {
		return false
	}

	for key, aVal := range a {
		bVal, exists := b[key]
		if !exists {
			return false
		}

		// 简单的值比较
		aStr, aIsStr := aVal.(string)
		bStr, bIsStr := bVal.(string)

		if aIsStr && bIsStr {
			if aStr != bStr {
				return false
			}
		} else if aVal != bVal {
			return false
		}
	}

	return true
}
