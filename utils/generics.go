package utils

import (
	"context"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
)

// GenericPool 泛型对象池
type GenericPool[T any] struct {
	pool sync.Pool
	new  func() T
}

// NewGenericPool 创建新的泛型对象池
func NewGenericPool[T any](newFunc func() T) *GenericPool[T] {
	return &GenericPool[T]{
		pool: sync.Pool{
			New: func() interface{} {
				return newFunc()
			},
		},
		new: newFunc,
	}
}

// Get 获取对象
func (p *GenericPool[T]) Get() T {
	return p.pool.Get().(T)
}

// Put 归还对象
func (p *GenericPool[T]) Put(item T) {
	p.pool.Put(item)
}

// StatsCollector 泛型统计收集器
type StatsCollector[T any] struct {
	requestChan chan T
	processor   func([]T) error
	batchSize   int
	timeout     time.Duration
	buffer      []T
	mutex       sync.Mutex
	ctx         context.Context
	cancel      context.CancelFunc
	wg          sync.WaitGroup
}

// NewStatsCollector 创建新的泛型统计收集器
func NewStatsCollector[T any](ctx context.Context, bufferSize, batchSize int, timeout time.Duration, processor func([]T) error) *StatsCollector[T] {
	childCtx, cancel := context.WithCancel(ctx)

	sc := &StatsCollector[T]{
		requestChan: make(chan T, bufferSize),
		processor:   processor,
		batchSize:   batchSize,
		timeout:     timeout,
		buffer:      make([]T, 0, batchSize),
		ctx:         childCtx,
		cancel:      cancel,
	}

	sc.start()
	return sc
}

// start 启动统计收集器
func (sc *StatsCollector[T]) start() {
	sc.wg.Add(1)
	go func() {
		defer sc.wg.Done()
		ticker := time.NewTicker(sc.timeout)
		defer ticker.Stop()

		for {
			select {
			case item := <-sc.requestChan:
				sc.addToBuffer(item)
			case <-ticker.C:
				sc.processBatch()
			case <-sc.ctx.Done():
				// 处理剩余数据
				sc.processBatch()
				return
			}
		}
	}()
}

// addToBuffer 添加到缓冲区
func (sc *StatsCollector[T]) addToBuffer(item T) {
	sc.mutex.Lock()
	defer sc.mutex.Unlock()

	sc.buffer = append(sc.buffer, item)
	if len(sc.buffer) >= sc.batchSize {
		sc.processBatchUnsafe()
	}
}

// processBatch 处理批次数据
func (sc *StatsCollector[T]) processBatch() {
	sc.mutex.Lock()
	defer sc.mutex.Unlock()
	sc.processBatchUnsafe()
}

// processBatchUnsafe 不加锁处理批次数据（调用者需要持有锁）
func (sc *StatsCollector[T]) processBatchUnsafe() {
	if len(sc.buffer) == 0 {
		return
	}

	batch := make([]T, len(sc.buffer))
	copy(batch, sc.buffer)
	sc.buffer = sc.buffer[:0] // 清空缓冲区但保留容量

	// 异步处理批次
	go func() {
		if err := sc.processor(batch); err != nil {
			// 记录错误日志
			LogError("处理统计批次失败", "error", err)
		}
	}()
}

// Record 记录数据
func (sc *StatsCollector[T]) Record(item T) error {
	select {
	case sc.requestChan <- item:
		return nil
	case <-sc.ctx.Done():
		return sc.ctx.Err()
	default:
		// 通道已满，直接处理
		sc.addToBuffer(item)
		return nil
	}
}

// Stop 停止收集器
func (sc *StatsCollector[T]) Stop() {
	sc.cancel()
	sc.wg.Wait()
	close(sc.requestChan)
}

// ConcurrentProcessor 并发处理器
type ConcurrentProcessor[T, R any] struct {
	maxWorkers int
	processFn  func(T) (R, error)
}

// NewConcurrentProcessor 创建新的并发处理器
func NewConcurrentProcessor[T, R any](maxWorkers int, processFn func(T) (R, error)) *ConcurrentProcessor[T, R] {
	return &ConcurrentProcessor[T, R]{
		maxWorkers: maxWorkers,
		processFn:  processFn,
	}
}

// ProcessAll 并发处理所有数据
func (cp *ConcurrentProcessor[T, R]) ProcessAll(ctx context.Context, items []T) ([]R, error) {
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(cp.maxWorkers)

	results := make([]R, len(items))
	resultsMutex := sync.Mutex{}

	for i, item := range items {
		i, item := i, item // 创建副本
		g.Go(func() error {
			result, err := cp.processFn(item)
			if err != nil {
				return err
			}

			resultsMutex.Lock()
			results[i] = result
			resultsMutex.Unlock()
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return nil, err
	}

	return results, nil
}

// ProcessStream 流式并发处理
func (cp *ConcurrentProcessor[T, R]) ProcessStream(ctx context.Context, input <-chan T) (<-chan R, <-chan error) {
	output := make(chan R, cp.maxWorkers)
	errChan := make(chan error, 1)

	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(cp.maxWorkers)

	go func() {
		defer close(output)
		defer close(errChan)

		for {
			select {
			case item, ok := <-input:
				if !ok {
					return
				}

				itemCopy := item // 创建副本
				g.Go(func() error {
					result, err := cp.processFn(itemCopy)
					if err != nil {
						return err
					}

					select {
					case output <- result:
					case <-ctx.Done():
						return ctx.Err()
					}
					return nil
				})

			case <-ctx.Done():
				errChan <- ctx.Err()
				return
			}
		}
	}()

	// 等待所有处理完成
	go func() {
		if err := g.Wait(); err != nil {
			select {
			case errChan <- err:
			default:
			}
		}
	}()

	return output, errChan
}

// SafeMap 线程安全的泛型map
type SafeMap[K comparable, V any] struct {
	data map[K]V
	mu   sync.RWMutex
}

// NewSafeMap 创建新的安全map
func NewSafeMap[K comparable, V any]() *SafeMap[K, V] {
	return &SafeMap[K, V]{
		data: make(map[K]V),
	}
}

// Load 加载值
func (sm *SafeMap[K, V]) Load(key K) (V, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	value, exists := sm.data[key]
	return value, exists
}

// Store 存储值
func (sm *SafeMap[K, V]) Store(key K, value V) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.data[key] = value
}

// Delete 删除值
func (sm *SafeMap[K, V]) Delete(key K) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	delete(sm.data, key)
}

// Range 遍历所有键值对
func (sm *SafeMap[K, V]) Range(fn func(K, V) bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	for k, v := range sm.data {
		if !fn(k, v) {
			break
		}
	}
}

// Size 获取大小
func (sm *SafeMap[K, V]) Size() int {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return len(sm.data)
}

// BatchProcessor 批处理器
type BatchProcessor[T any] struct {
	batchSize int
	processFn func([]T) error
	timeout   time.Duration
	buffer    []T
	mutex     sync.Mutex
	lastFlush time.Time
}

// NewBatchProcessor 创建批处理器
func NewBatchProcessor[T any](batchSize int, timeout time.Duration, processFn func([]T) error) *BatchProcessor[T] {
	return &BatchProcessor[T]{
		batchSize: batchSize,
		processFn: processFn,
		timeout:   timeout,
		buffer:    make([]T, 0, batchSize),
	}
}

// Add 添加项目到批次
func (bp *BatchProcessor[T]) Add(item T) error {
	bp.mutex.Lock()
	defer bp.mutex.Unlock()

	bp.buffer = append(bp.buffer, item)

	// 检查是否需要立即处理
	if len(bp.buffer) >= bp.batchSize {
		return bp.flushUnsafe()
	}

	// 检查是否超时
	if time.Since(bp.lastFlush) > bp.timeout {
		return bp.flushUnsafe()
	}

	return nil
}

// Flush 手动刷新缓冲区
func (bp *BatchProcessor[T]) Flush() error {
	bp.mutex.Lock()
	defer bp.mutex.Unlock()
	return bp.flushUnsafe()
}

// flushUnsafe 不安全的刷新（调用者需要持有锁）
func (bp *BatchProcessor[T]) flushUnsafe() error {
	if len(bp.buffer) == 0 {
		return nil
	}

	batch := make([]T, len(bp.buffer))
	copy(batch, bp.buffer)
	bp.buffer = bp.buffer[:0] // 清空缓冲区但保留容量
	bp.lastFlush = time.Now()

	return bp.processFn(batch)
}

// Size 获取当前缓冲区大小
func (bp *BatchProcessor[T]) Size() int {
	bp.mutex.Lock()
	defer bp.mutex.Unlock()
	return len(bp.buffer)
}