package toolhandler

import (
	"strings"
	"sync"
)

// ContentBuffer 内容缓冲区，用于按edit_index位置组装内容片段
// 参考Python版本的bytearray实现，使用字节切片提供高效的随机访问和修改
type ContentBuffer struct {
	buffer []byte     // 内容缓冲区
	mu     sync.Mutex // 保护并发访问
}

// NewContentBuffer 创建新的内容缓冲区
func NewContentBuffer() *ContentBuffer {
	return &ContentBuffer{
		buffer: make([]byte, 0, 4096), // 预分配4KB空间
	}
}

// ApplyEdit 在指定位置应用编辑
// editIndex: 编辑开始的字节位置
// editContent: 要插入/替换的内容
//
// 实现逻辑：
// 1. 如果editIndex超出当前缓冲区，用空字节填充
// 2. 确保缓冲区足够长以容纳新内容
// 3. 在指定位置替换内容（覆盖，而非插入）
func (cb *ContentBuffer) ApplyEdit(editIndex int, editContent string) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if editContent == "" {
		return
	}

	editBytes := []byte(editContent)
	requiredLength := editIndex + len(editBytes)

	// 如果edit_index超出当前缓冲区，用空字节填充
	if len(cb.buffer) < editIndex {
		padding := make([]byte, editIndex-len(cb.buffer))
		cb.buffer = append(cb.buffer, padding...)
	}

	// 确保缓冲区足够长以容纳新内容
	if len(cb.buffer) < requiredLength {
		extension := make([]byte, requiredLength-len(cb.buffer))
		cb.buffer = append(cb.buffer, extension...)
	}

	// 在指定位置替换内容（覆盖模式）
	copy(cb.buffer[editIndex:], editBytes)
}

// GetContent 获取当前完整内容
// 返回解码后的字符串，并清理空字节
func (cb *ContentBuffer) GetContent() string {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	// 解码并清理空字节
	content := string(cb.buffer)
	content = strings.ReplaceAll(content, "\x00", "")
	return content
}

// GetRawContent 获取原始字节内容（用于调试）
func (cb *ContentBuffer) GetRawContent() []byte {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	// 返回副本以避免外部修改
	result := make([]byte, len(cb.buffer))
	copy(result, cb.buffer)
	return result
}

// Length 返回缓冲区当前长度
func (cb *ContentBuffer) Length() int {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return len(cb.buffer)
}

// Reset 重置缓冲区
func (cb *ContentBuffer) Reset() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.buffer = cb.buffer[:0]
}

// Clear 清空缓冲区并释放内存
func (cb *ContentBuffer) Clear() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.buffer = nil
	cb.buffer = make([]byte, 0, 4096)
}
