// Package pglock 提供 PostgreSQL session 级 advisory lock 的跨实例互斥。
//
// 锁绑定在独立连接上，调用方必须在同一连接解锁。Unlock 会把连接归还连接池；
// 若进程退出或连接断开，PostgreSQL 会自动释放会话锁。
package pglock

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"sync"
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
		if _, err := l.conn.ExecContext(context.Background(), "SELECT pg_advisory_unlock($1)", l.key); err != nil {
			_ = l.conn.Raw(func(any) error { return driver.ErrBadConn })
		}
		_ = l.conn.Close()
	})
}
