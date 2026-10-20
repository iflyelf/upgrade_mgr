// Package leader Consul Leader 选举（分布式编排器高可用）。
package leader

import (
	"context"
	"errors"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hashicorp/consul/api"
)

// Election Leader 选举。
type Election struct {
	consul *api.Client
	key    string

	// mu 保护 sessionID：Campaign 写入，Resign/Watch 读取，存在并发访问。
	mu        sync.RWMutex
	sessionID string
	isLeader  atomic.Bool
}

// NewElection 创建选举实例。
func NewElection(consul *api.Client, key string) *Election {
	if key == "" {
		key = "service/upgrade-mgr/leader"
	}
	return &Election{consul: consul, key: key}
}

// Run 持续竞选 Leader 并在成为 Leader 时调用 onElected。
//
// Session 失效或失去 Leader 时，取消 onElected 的 ctx 并重新竞选。
// 随 ctx 取消退出。
func (e *Election) Run(ctx context.Context, onElected func(ctx context.Context)) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// 创建新的 session 并竞选
		if err := e.campaignOnce(ctx, onElected); err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			log.Printf("[leader] 竞选失败: %v, 2s 后重试", err)
			time.Sleep(2 * time.Second)
		}
	}
}

// campaignOnce 单次竞选周期：创建 session → 获取锁 → 续约 → 失效后重新竞选。
func (e *Election) campaignOnce(ctx context.Context, onElected func(ctx context.Context)) error {
	// 创建 session
	session := e.consul.Session()
	se := &api.SessionEntry{
		Name:      "upgrade-mgr-leader",
		TTL:       "10s",
		Behavior:  "delete",
		LockDelay: 1 * time.Second,
	}
	sid, _, err := session.Create(se, nil)
	if err != nil {
		return err
	}
	e.mu.Lock()
	e.sessionID = sid
	e.mu.Unlock()

	// 确保退出时销毁 session
	defer func() {
		e.isLeader.Store(false)
		_, _ = session.Destroy(sid, nil)
		e.mu.Lock()
		if e.sessionID == sid {
			e.sessionID = ""
		}
		e.mu.Unlock()
	}()

	// 竞选循环（阻塞直到获得锁或 ctx 取消）
	kv := e.consul.KV()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		acquired, _, err := kv.Acquire(&api.KVPair{
			Key:     e.key,
			Value:   []byte("leader"),
			Session: sid,
		}, nil)
		if err != nil {
			log.Printf("⚠️ Leader 选举失败: %v", err)
			time.Sleep(2 * time.Second)
			continue
		}
		if acquired {
			e.isLeader.Store(true)
			log.Println("✅ 成为 Leader，开始编排任务")
			break
		}

		// 未获得锁，等待重试
		time.Sleep(2 * time.Second)
	}

	// 成为 Leader：启动续约 goroutine 并调用业务回调
	renewCtx, cancelRenew := context.WithCancel(ctx)
	defer cancelRenew()

	renewErrCh := make(chan error, 1)
	go e.renewLoop(renewCtx, sid, renewErrCh)

	// 创建可取消的 ctx 供 onElected 使用（失去 Leader 时取消）
	leaderCtx, cancelLeader := context.WithCancel(ctx)
	defer cancelLeader()

	// 启动业务逻辑
	done := make(chan struct{})
	go func() {
		defer close(done)
		onElected(leaderCtx)
	}()

	// 等待：ctx 取消、续约失败、或业务逻辑退出
	select {
	case <-ctx.Done():
		cancelLeader()
		<-done
		return ctx.Err()
	case err := <-renewErrCh:
		// Session 失效，取消业务逻辑并重新竞选
		log.Printf("⚠️ Leader session 失效: %v，准备重新竞选", err)
		cancelLeader()
		<-done
		return err
	case <-done:
		// 业务逻辑自行退出（理论上不应发生，编排器应持续运行）
		return nil
	}
}

// renewLoop 定期续约 session，失败时通知主循环。
func (e *Election) renewLoop(ctx context.Context, sid string, errCh chan<- error) {
	session := e.consul.Session()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			entry, _, err := session.Renew(sid, nil)
			if err != nil || entry == nil {
				// 续约失败或 session 不存在（已过期）
				e.isLeader.Store(false)
				select {
				case errCh <- err:
				default:
				}
				return
			}
		}
	}
}

// Campaign 竞选 Leader（阻塞直到成为 Leader 或 ctx 取消）。
//
// 已废弃：建议使用 Run 以自动处理 session 失效后的重新竞选。
func (e *Election) Campaign(ctx context.Context) error {
	// 创建 session
	session := e.consul.Session()
	se := &api.SessionEntry{
		Name:      "upgrade-mgr-leader",
		TTL:       "10s",
		Behavior:  "delete",
		LockDelay: 1 * time.Second,
	}
	sid, _, err := session.Create(se, nil)
	if err != nil {
		return err
	}
	e.mu.Lock()
	e.sessionID = sid
	e.mu.Unlock()

	// 定期续约 session
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				_, _ = session.Destroy(sid, nil)
				return
			case <-ticker.C:
				if _, _, err := session.Renew(sid, nil); err != nil {
					// 续约失败：session 可能已过期，立即放弃 Leader 身份避免脑裂。
					e.isLeader.Store(false)
					log.Printf("⚠️ Leader session 续约失败，放弃 Leader 身份: %v", err)
				}
			}
		}
	}()

	// 竞选循环
	kv := e.consul.KV()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// 尝试获取锁
		acquired, _, err := kv.Acquire(&api.KVPair{
			Key:     e.key,
			Value:   []byte("leader"),
			Session: sid,
		}, nil)
		if err != nil {
			log.Printf("⚠️ Leader 选举失败: %v", err)
			time.Sleep(2 * time.Second)
			continue
		}
		if acquired {
			e.isLeader.Store(true)
			log.Println("✅ 成为 Leader，开始编排任务")
			return nil
		}

		// 未获得锁，等待重试
		time.Sleep(2 * time.Second)
	}
}

// IsLeader 是否当前为 Leader。
func (e *Election) IsLeader() bool {
	return e.isLeader.Load()
}

// Resign 主动放弃 Leader。
func (e *Election) Resign() {
	sid := e.currentSession()
	if sid != "" {
		kv := e.consul.KV()
		_, _, _ = kv.Release(&api.KVPair{
			Key:     e.key,
			Session: sid,
		}, nil)
		_, _ = e.consul.Session().Destroy(sid, nil)
		e.isLeader.Store(false)
		e.mu.Lock()
		e.sessionID = ""
		e.mu.Unlock()
		log.Println("已放弃 Leader")
	}
}

// Watch 监听 Leader 变更（用于失败转移）。
func (e *Election) Watch(ctx context.Context, onChange func(isLeader bool)) {
	kv := e.consul.KV()
	opts := &api.QueryOptions{WaitIndex: 0}

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		pair, meta, err := kv.Get(e.key, opts)
		if err != nil {
			time.Sleep(2 * time.Second)
			continue
		}

		opts.WaitIndex = meta.LastIndex

		wasLeader := e.isLeader.Load()
		nowLeader := pair != nil && pair.Session == e.currentSession()
		e.isLeader.Store(nowLeader)

		if wasLeader != nowLeader {
			onChange(nowLeader)
		}
	}
}

// currentSession 并发安全地读取当前 session ID。
func (e *Election) currentSession() string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.sessionID
}
