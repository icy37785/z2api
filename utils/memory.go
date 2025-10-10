package utils

import (
	"bytes"
	"io"
	"strings"
	"sync"
	"time"
)

// Predefined pools for common types
var (
	// Buffer pools for different sizes
	smallBufferPool = NewGenericPool(func() *bytes.Buffer {
		return bytes.NewBuffer(make([]byte, 0, 256))
	})

	mediumBufferPool = NewGenericPool(func() *bytes.Buffer {
		return bytes.NewBuffer(make([]byte, 0, 1024))
	})

	largeBufferPool = NewGenericPool(func() *bytes.Buffer {
		return bytes.NewBuffer(make([]byte, 0, 8192))
	})

	// String builder pools
	stringBuilderPool = NewGenericPool(func() *strings.Builder {
		sb := &strings.Builder{}
		sb.Grow(256) // 预分配容量
		return sb
	})

	// Byte slice pools
	byteSlicePool64 = NewGenericPool(func() []byte {
		return make([]byte, 0, 64)
	})

	byteSlicePool256 = NewGenericPool(func() []byte {
		return make([]byte, 0, 256)
	})

	byteSlicePool1K = NewGenericPool(func() []byte {
		return make([]byte, 0, 1024)
	})

	byteSlicePool4K = NewGenericPool(func() []byte {
		return make([]byte, 0, 4096)
	})

	// Common data structure pools
	stringSlicePool = NewGenericPool(func() []string {
		return make([]string, 0, 10)
	})

	errorSlicePool = NewGenericPool(func() []error {
		return make([]error, 0, 5)
	})

	interfaceSlicePool = NewGenericPool(func() []interface{} {
		return make([]interface{}, 0, 10)
	})
)

// GetBuffer 获取适当大小的buffer
func GetBuffer(size int) *bytes.Buffer {
	switch {
	case size <= 256:
		return smallBufferPool.Get()
	case size <= 1024:
		return mediumBufferPool.Get()
	case size <= 8192:
		return largeBufferPool.Get()
	default:
		// 对于超大buffer，直接创建
		return bytes.NewBuffer(make([]byte, 0, size))
	}
}

// PutBuffer 归还buffer到适当的池
func PutBuffer(buf *bytes.Buffer) {
	size := buf.Cap()
	buf.Reset() // 清空内容

	switch {
	case size <= 256:
		smallBufferPool.Put(buf)
	case size <= 1024:
		mediumBufferPool.Put(buf)
	case size <= 8192:
		largeBufferPool.Put(buf)
	// 超大buffer直接让GC回收
	}
}

// GetStringBuilder 获取字符串构建器
func GetStringBuilder() *strings.Builder {
	return stringBuilderPool.Get()
}

// PutStringBuilder 归还字符串构建器
func PutStringBuilder(sb *strings.Builder) {
	sb.Reset()
	stringBuilderPool.Put(sb)
}

// GetByteSlice 获取适当大小的字节切片
func GetByteSlice(size int) []byte {
	switch {
	case size <= 64:
		return byteSlicePool64.Get()
	case size <= 256:
		return byteSlicePool256.Get()
	case size <= 1024:
		return byteSlicePool1K.Get()
	case size <= 4096:
		return byteSlicePool4K.Get()
	default:
		// 对于超大切片，直接创建
		return make([]byte, 0, size)
	}
}

// PutByteSlice 归还字节切片到适当的池
func PutByteSlice(slice []byte) {
	capacity := cap(slice)
	slice = slice[:0] // 重置长度但保留容量

	switch {
	case capacity <= 64:
		byteSlicePool64.Put(slice)
	case capacity <= 256:
		byteSlicePool256.Put(slice)
	case capacity <= 1024:
		byteSlicePool1K.Put(slice)
	case capacity <= 4096:
		byteSlicePool4K.Put(slice)
	// 超大切片直接让GC回收
	}
}

// GetStringSlice 获取字符串切片
func GetStringSlice() []string {
	return stringSlicePool.Get()
}

// PutStringSlice 归还字符串切片
func PutStringSlice(slice []string) {
	if cap(slice) <= 100 { // 只有较小的切片才放回池中
		stringSlicePool.Put(slice[:0])
	}
}

// GetErrorSlice 获取错误切片
func GetErrorSlice() []error {
	return errorSlicePool.Get()
}

// PutErrorSlice 归还错误切片
func PutErrorSlice(slice []error) {
	if cap(slice) <= 10 { // 只有较小的切片才放回池中
		errorSlicePool.Put(slice[:0])
	}
}

// GetInterfaceSlice 获取interface切片
func GetInterfaceSlice() []interface{} {
	return interfaceSlicePool.Get()
}

// PutInterfaceSlice 归还interface切片
func PutInterfaceSlice(slice []interface{}) {
	if cap(slice) <= 20 { // 只有较小的切片才放回池中
		interfaceSlicePool.Put(slice[:0])
	}
}

// MemoryHelper 内存辅助工具
type MemoryHelper struct {
	startTime time.Time
	allocations int64
}

// NewMemoryHelper 创建内存辅助工具
func NewMemoryHelper() *MemoryHelper {
	return &MemoryHelper{
		startTime: time.Now(),
	}
}

// TrackAllocation 跟踪内存分配
func (mh *MemoryHelper) TrackAllocation() {
	mh.allocations++
}

// GetStats 获取内存统计
func (mh *MemoryHelper) GetStats() map[string]interface{} {
	return map[string]interface{}{
		"duration_ms":  time.Since(mh.startTime).Milliseconds(),
		"allocations": mh.allocations,
	}
}

// ResourceGuard 资源保护器
type ResourceGuard struct {
	maxMemory int64
	current   int64
	mu        sync.RWMutex
}

// NewResourceGuard 创建资源保护器
func NewResourceGuard(maxMemory int64) *ResourceGuard {
	return &ResourceGuard{
		maxMemory: maxMemory,
	}
}

// TryAllocate 尝试分配内存
func (rg *ResourceGuard) TryAllocate(size int64) bool {
	rg.mu.Lock()
	defer rg.mu.Unlock()

	if rg.current+size > rg.maxMemory {
		return false
	}

	rg.current += size
	return true
}

// Release 释放内存
func (rg *ResourceGuard) Release(size int64) {
	rg.mu.Lock()
	defer rg.mu.Unlock()

	rg.current -= size
	if rg.current < 0 {
		rg.current = 0
	}
}

// GetCurrentUsage 获取当前使用量
func (rg *ResourceGuard) GetCurrentUsage() int64 {
	rg.mu.RLock()
	defer rg.mu.RUnlock()
	return rg.current
}

// GetMaxUsage 获取最大使用量
func (rg *ResourceGuard) GetMaxUsage() int64 {
	return rg.maxMemory
}

// ZeroCopyReader 零拷贝读取器
type ZeroCopyReader struct {
	data []byte
	pos  int
}

// NewZeroCopyReader 创建零拷贝读取器
func NewZeroCopyReader(data []byte) *ZeroCopyReader {
	return &ZeroCopyReader{
		data: data,
		pos:  0,
	}
}

// Read 实现 io.Reader 接口
func (zcr *ZeroCopyReader) Read(p []byte) (int, error) {
	if zcr.pos >= len(zcr.data) {
		return 0, io.EOF
	}

	n := copy(p, zcr.data[zcr.pos:])
	zcr.pos += n
	return n, nil
}

// ReadByte 读取单个字节
func (zcr *ZeroCopyReader) ReadByte() (byte, error) {
	if zcr.pos >= len(zcr.data) {
		return 0, io.EOF
	}

	b := zcr.data[zcr.pos]
	zcr.pos++
	return b, nil
}

// Remaining 返回剩余字节数
func (zcr *ZeroCopyReader) Remaining() int {
	return len(zcr.data) - zcr.pos
}

// Reset 重置读取器位置
func (zcr *ZeroCopyReader) Reset() {
	zcr.pos = 0
}

// ChunkedWriter 分块写入器
type ChunkedWriter struct {
	buffer    []byte
	chunkSize int
	onChunk   func([]byte) error
}

// NewChunkedWriter 创建分块写入器
func NewChunkedWriter(chunkSize int, onChunk func([]byte) error) *ChunkedWriter {
	return &ChunkedWriter{
		buffer:    GetByteSlice(chunkSize),
		chunkSize: chunkSize,
		onChunk:   onChunk,
	}
}

// Write 实现io.Writer接口
func (cw *ChunkedWriter) Write(p []byte) (int, error) {
	totalWritten := 0

	for len(p) > 0 {
		remainingSpace := cw.chunkSize - len(cw.buffer)
		if remainingSpace == 0 {
			// 缓冲区满，刷新
			if err := cw.flush(); err != nil {
				return totalWritten, err
			}
			remainingSpace = cw.chunkSize
		}

		// 计算本次写入量
		writeSize := len(p)
		if writeSize > remainingSpace {
			writeSize = remainingSpace
		}

		// 写入数据
		cw.buffer = append(cw.buffer, p[:writeSize]...)
		totalWritten += writeSize
		p = p[writeSize:]
	}

	return totalWritten, nil
}

// Flush 刷新缓冲区
func (cw *ChunkedWriter) Flush() error {
	return cw.flush()
}

// flush 内部刷新方法
func (cw *ChunkedWriter) flush() error {
	if len(cw.buffer) == 0 {
		return nil
	}

	// 创建副本传递给回调函数
	chunk := make([]byte, len(cw.buffer))
	copy(chunk, cw.buffer)

	// 重置缓冲区
	cw.buffer = cw.buffer[:0]

	// 调用回调函数
	return cw.onChunk(chunk)
}

// Close 关闭写入器
func (cw *ChunkedWriter) Close() error {
	err := cw.Flush()
	PutByteSlice(cw.buffer)
	cw.buffer = nil
	return err
}