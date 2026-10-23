// Package keyedlock 提供「按 key 的互斥锁」。
//
// 用途：同一文件被多个请求同时触发下载时只允许一个真正下载，其余等待后复用结果；
// 不同文件互不阻塞。键在无人使用时自动从映射中删除，避免长期运行后内存无限增长
// （这是直接用 sync.Map 保存 *sync.Mutex 时常见的问题）。
package keyedlock

import "sync"

// Locker 按 key 分配互斥锁。
type Locker struct {
	mu    sync.Mutex // 保护 locks 与每个 entry 的 refs
	locks map[string]*entry
}

type entry struct {
	mu   sync.Mutex
	refs int
}

// New 创建 Locker。
func New() *Locker {
	return &Locker{locks: map[string]*entry{}}
}

// Lock 获取 key 对应的锁并返回解锁函数。
//
// 必须调用返回的函数释放；重复调用解锁函数是安全的（内部只有一次生效）。
func (l *Locker) Lock(key string) func() {
	l.mu.Lock()
	e, ok := l.locks[key]
	if !ok {
		e = &entry{}
		l.locks[key] = e
	}
	e.refs++
	l.mu.Unlock()

	e.mu.Lock()

	var once sync.Once
	return func() {
		once.Do(func() {
			e.mu.Unlock()
			l.mu.Lock()
			e.refs--
			if e.refs == 0 {
				delete(l.locks, key)
			}
			l.mu.Unlock()
		})
	}
}

// Size 返回当前跟踪的 key 数量（测试用）。
func (l *Locker) Size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.locks)
}
