// Package pglock 提供 PostgreSQL session 级 advisory lock 的跨实例互斥。
//
// 锁绑定在独立连接上，调用方必须在同一连接解锁。Unlock 会把连接归还连接池；
// 若进程退出或连接断开，PostgreSQL 会自动释放会话锁。
package pglock

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/binary"
	"fmt"
	"sync"
	"time"
)

// Lock 已持有的 advisory lock。
type Lock struct {
	conn *sql.Conn
	key  int64
	once sync.Once
}

// Try 非阻塞获取锁。ok=false 表示锁已被其他实例持有。
func Try(ctx context.Context, db *sql.DB, key int64) (*Lock, bool, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("获取锁连接失败: %w", err)
	}

	var got bool
	if err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", key).Scan(&got); err != nil {
		_ = conn.Close()
		return nil, false, fmt.Errorf("获取 advisory lock 失败: %w", err)
	}
	if !got {
		_ = conn.Close()
		return nil, false, nil
	}
	return &Lock{conn: conn, key: key}, true, nil
}

// LockSpec 一次多锁获取中的单个锁。
//
// Shared=true 使用 pg_try_advisory_lock_shared（共享锁之间兼容），
// false 使用 pg_try_advisory_lock（独占锁，与共享锁互斥）。
type LockSpec struct {
	Key    int64
	Shared bool
}

// MultiLock 在同一连接上持有的一组 advisory lock。
type MultiLock struct {
	conn  *sql.Conn
	specs []LockSpec
	once  sync.Once
}

// TryMany 尝试在一次数据库连接上依次获取多个 advisory lock。
//
// 任一锁获取失败时，会释放已经拿到的锁并关闭连接（不会把“半套锁”留给
// 调用方）；调用方可以间隔重试。多锁必须在同一连接上，才能用一条连接
// 同时保护“组件共享锁 + 制品独占锁”，避免额外占用连接池。
func TryMany(ctx context.Context, db *sql.DB, specs []LockSpec) (*MultiLock, bool, error) {
	if db == nil {
		return nil, false, fmt.Errorf("数据库未初始化")
	}
	if len(specs) == 0 {
		return nil, false, fmt.Errorf("锁列表为空")
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("获取锁连接失败: %w", err)
	}

	acquired := make([]LockSpec, 0, len(specs))
	for _, spec := range specs {
		query := "SELECT pg_try_advisory_lock($1)"
		if spec.Shared {
			query = "SELECT pg_try_advisory_lock_shared($1)"
		}
		var got bool
		if err := conn.QueryRowContext(ctx, query, spec.Key).Scan(&got); err != nil {
			releaseSpecs(conn, acquired)
			_ = conn.Close()
			return nil, false, fmt.Errorf("获取 advisory lock 失败: %w", err)
		}
		if !got {
			releaseSpecs(conn, acquired)
			_ = conn.Close()
			return nil, false, nil
		}
		acquired = append(acquired, spec)
	}
	return &MultiLock{conn: conn, specs: acquired}, true, nil
}

// Conn 返回持有这些锁的专用连接。调用方不得自行 Close。
func (l *MultiLock) Conn() *sql.Conn {
	if l == nil {
		return nil
	}
	return l.conn
}

// Unlock 按获取的逆序释放全部锁并归还连接；可重复调用。
func (l *MultiLock) Unlock() {
	if l == nil {
		return
	}
	l.once.Do(func() {
		if l.conn == nil {
			return
		}
		releaseSpecs(l.conn, l.specs)
		_ = l.conn.Close()
	})
}

// releaseSpecs 逆序释放已获取的锁；任一解锁失败则丢弃连接。
func releaseSpecs(conn *sql.Conn, specs []LockSpec) {
	if conn == nil || len(specs) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for i := len(specs) - 1; i >= 0; i-- {
		query := "SELECT pg_advisory_unlock($1)"
		if specs[i].Shared {
			query = "SELECT pg_advisory_unlock_shared($1)"
		}
		if _, err := conn.ExecContext(ctx, query, specs[i].Key); err != nil {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
			return
		}
	}
}

// KeyFromString 把任意长度的业务键稳定映射为 int64 advisory lock key。
//
// 采用 SHA-256 前 8 字节，跨进程/跨副本结果一致；碰撞概率可忽略。
// 注意：PostgreSQL 的 bigint advisory lock 与 (int,int) 锁空间不同，
// 不会与本项目其它使用 pg_try_advisory_lock(bigint) 的锁冲突。
func KeyFromString(s string) int64 {
	sum := sha256.Sum256([]byte(s))
	return int64(binary.BigEndian.Uint64(sum[:8]))
}

// Conn 返回持有锁的专用连接。
//
// 调用方可在这条连接上执行与锁作用域一致的 SQL（例如制品的元数据写入），
// 避免为了执行 SQL 再从连接池借一条连接——大量并发上传时可能耗尽连接池。
// 调用方不得自行 Close 该连接，解锁由 Unlock 统一完成。
func (l *Lock) Conn() *sql.Conn {
	if l == nil {
		return nil
	}
	return l.conn
}

// Unlock 释放锁并归还连接；可重复调用。
func (l *Lock) Unlock() {
	if l == nil {
		return
	}
	l.once.Do(func() {
		if l.conn == nil {
			return
		}
		// 解锁失败说明连接可能已异常。丢弃底层连接，避免把仍持有锁的
		// 会话归还连接池；连接彻底关闭后 PostgreSQL 会释放会话锁。
		//
		// 解锁不能无限等待：数据库不可达时 context.Background() 可能永久阻塞，
		// 进而拖住编排器/关闭流程。超时后直接丢弃连接，由 PostgreSQL 释放会话锁。
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := l.conn.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", l.key); err != nil {
			_ = l.conn.Raw(func(any) error { return driver.ErrBadConn })
		}
		_ = l.conn.Close()
	})
}
