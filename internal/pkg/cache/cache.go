// Package cache 提供 Redis 缓存封装（单点 / 集群 / 哨兵）。
//
// 设计原则与 consul_mgr 的 cache 包一致：
//   - Redis 是可选依赖，连接失败仅降级为「不缓存」，不阻塞服务启动；
//   - 支持 single / cluster / sentinel 三种模式；
//   - 统一键前缀与默认 TTL，便于多环境/多副本隔离；
//   - 不缓存任何凭据，调用方只传入可安全序列化的业务数据。
package cache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// Mode 连接模式。
type Mode string

const (
	ModeSingle   Mode = "single"
	ModeCluster  Mode = "cluster"
	ModeSentinel Mode = "sentinel"
)

// Options 连接参数。
type Options struct {
	Enabled    bool
	Mode       string
	Host       string
	Port       int
	Addrs      []string
	MasterName string
	Password   string
	DB         int
	TTL        time.Duration
	Prefix     string
}

// Status 运行状态（用于页面展示，不含密码）。
type Status struct {
	Enabled     bool     `json:"enabled"`
	Mode        string   `json:"mode"`
	Addrs       []string `json:"addrs"`
	MasterName  string   `json:"master_name,omitempty"`
	DB          int      `json:"db"`
	RequestedDB int      `json:"requested_db"`
	DBFallback  bool     `json:"db_fallback"`
	TTLSeconds  int      `json:"ttl_seconds"`
	Prefix      string   `json:"prefix"`
	LastError   string   `json:"last_error,omitempty"`
}

// Cache Redis 缓存。零值不可用；请用 New 创建。
type Cache struct {
	mu       sync.RWMutex
	client   redis.UniversalClient
	status   Status
	ttl      time.Duration
	prefix   string
	closeOne sync.Once
}

// defaultTTL 默认页面缓存时长。
const defaultTTL = 10 * time.Second

// maxHTTPTTL 页面缓存上限：列表数据依赖写操作失效 + 短 TTL 控制陈旧度，
// 过长的 TTL 会让 Agent 上报的进度/心跳较晚可见。
const maxHTTPTTL = 30 * time.Second

// New 创建缓存。任何连接/认证问题都只禁用缓存并记日志，不返回错误。
func New(opts Options) *Cache {
	c := &Cache{ttl: opts.TTL, prefix: normalizePrefix(opts.Prefix)}
	if c.ttl <= 0 {
		c.ttl = defaultTTL
	}
	if c.ttl > maxHTTPTTL {
		log.Printf("⚠️ Redis 缓存 TTL %s 超过上限，已按 %s 生效", c.ttl, maxHTTPTTL)
		c.ttl = maxHTTPTTL
	}
	st := Status{
		Mode:        strings.ToLower(strings.TrimSpace(opts.Mode)),
		RequestedDB: opts.DB,
		DB:          opts.DB,
		Prefix:      c.prefix,
		MasterName:  strings.TrimSpace(opts.MasterName),
		TTLSeconds:  int(c.ttl / time.Second),
	}
	if st.Mode == "" {
		st.Mode = string(ModeSingle)
	}
	addrs := normalizeAddrs(opts)
	st.Addrs = addrs

	c.status = st
	if !opts.Enabled {
		return c
	}
	if len(addrs) == 0 {
		st.LastError = "Redis 地址为空"
		c.status = st
		return c
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	mode := Mode(st.Mode)
	effectiveDB := opts.DB
	if mode == ModeCluster {
		// Redis Cluster 只有 DB 0；强行指定其它 DB 会导致所有命令失败。
		if effectiveDB != 0 {
			log.Printf("⚠️ Redis Cluster 只支持 DB 0，已忽略配置的 DB %d", effectiveDB)
			st.DBFallback = true
			effectiveDB = 0
		}
	} else if effectiveDB > 0 {
		if n, ok := probeDatabases(ctx, mode, addrs, opts); ok && effectiveDB >= n {
			// 按用户「使用较大 DB」的意图回退到最大可用库，而不是 0，
			// 降低与其它系统共享 DB 0 时 key 冲突的概率；前缀仍保证逻辑隔离。
			log.Printf("⚠️ Redis 仅支持 DB 0-%d（当前配置 DB %d），已回退到 DB %d；键前缀 %q 仍可隔离数据",
				n-1, effectiveDB, n-1, c.prefix)
			st.DBFallback = true
			effectiveDB = n - 1
		}
	}

	client, err := newClient(mode, addrs, opts, effectiveDB)
	if err != nil {
		st.LastError = err.Error()
		c.status = st
		return c
	}
	if err := client.Ping(ctx).Err(); err != nil {
		// 部分托管 Redis 禁用 CONFIG，probeDatabases 无法判断 DB 范围；
		// SELECT 越界时通常只在 Ping 时报错，这里再尝试 DB 0 兜底。
		if effectiveDB != 0 && isDBRangeError(err) {
			fallbackDB := 0
			if n, ok := probeDatabases(ctx, mode, addrs, opts); ok && n > 0 {
				fallbackDB = n - 1
			}
			log.Printf("⚠️ Redis DB %d 不可用（%v），回退到 DB %d", effectiveDB, err, fallbackDB)
			_ = client.Close()
			client, err = newClient(mode, addrs, opts, fallbackDB)
			if err == nil {
				err = client.Ping(ctx).Err()
			}
			st.DBFallback = true
			effectiveDB = fallbackDB
		}
		if err != nil {
			_ = client.Close()
			st.LastError = err.Error()
			c.status = st
			log.Printf("⚠️ Redis 连接失败，缓存已禁用: %s (%v)", strings.Join(addrs, ","), err)
			return c
		}
	}

	st.DB = effectiveDB
	st.Enabled = true
	c.client = client
	c.status = st
	extra := ""
	if st.DBFallback {
		extra = "（DB 已回退）"
	}
	log.Printf("✅ Redis 缓存已启用: mode=%s addr=%s db=%d%s ttl=%s",
		st.Mode, strings.Join(addrs, ","), effectiveDB, extra, c.ttl)
	return c
}

// normalizePrefix 统一键前缀：
//   - 空值使用 upgrade_mgr:；
//   - 自动补结尾冒号；
//   - 去除空白，避免出现不可见字符。
func normalizePrefix(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return "upgrade_mgr:"
	}
	// 空格替换为下划线（保留可读性），其余空白直接去掉。
	p = strings.ReplaceAll(p, " ", "_")
	p = strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || r == '\r' {
			return -1
		}
		return r
	}, p)
	if !strings.HasSuffix(p, ":") {
		p += ":"
	}
	return p
}

// normalizeAddrs 汇总 Hosts 与 Host/Port；Host 可含端口。
func normalizeAddrs(opts Options) []string {
	var out []string
	for _, raw := range opts.Addrs {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if !strings.Contains(raw, ":") {
			raw = raw + ":6379"
		}
		out = append(out, raw)
	}
	if len(out) == 0 && strings.TrimSpace(opts.Host) != "" {
		host := strings.TrimSpace(opts.Host)
		if !strings.Contains(host, ":") {
			port := opts.Port
			if port <= 0 {
				port = 6379
			}
			host = fmt.Sprintf("%s:%d", host, port)
		}
		out = append(out, host)
	}
	return out
}

// newClient 按模式创建客户端。
func newClient(mode Mode, addrs []string, opts Options, db int) (redis.UniversalClient, error) {
	switch mode {
	case ModeCluster:
		return redis.NewClusterClient(&redis.ClusterOptions{
			Addrs:        addrs,
			Password:     opts.Password,
			DialTimeout:  2 * time.Second,
			ReadTimeout:  2 * time.Second,
			WriteTimeout: 2 * time.Second,
			PoolSize:     20,
			MinIdleConns: 2,
		}), nil
	case ModeSentinel:
		if strings.TrimSpace(opts.MasterName) == "" {
			return nil, fmt.Errorf("sentinel 模式必须配置 master_name")
		}
		return redis.NewFailoverClient(&redis.FailoverOptions{
			MasterName:    strings.TrimSpace(opts.MasterName),
			SentinelAddrs: addrs,
			Password:      opts.Password,
			DB:            db,
			DialTimeout:   2 * time.Second,
			ReadTimeout:   2 * time.Second,
			WriteTimeout:  2 * time.Second,
			PoolSize:      20,
			MinIdleConns:  2,
		}), nil
	default:
		return redis.NewClient(&redis.Options{
			Addr:         addrs[0],
			Password:     opts.Password,
			DB:           db,
			DialTimeout:  2 * time.Second,
			ReadTimeout:  2 * time.Second,
			WriteTimeout: 2 * time.Second,
			PoolSize:     20,
			MinIdleConns: 2,
		}), nil
	}
}

// probeDatabases 查询服务器支持的 DB 数量（CONFIG GET databases）。
// 托管 Redis 通常禁用 CONFIG，此时返回 ok=false，由 Ping/SELECT 结果兜底。
func probeDatabases(ctx context.Context, mode Mode, addrs []string, opts Options) (int, bool) {
	if mode == ModeCluster {
		return 1, true
	}
	// 用 DB 0 探测，避免配置 DB 越界导致探测本身失败。
	client, err := newClient(mode, addrs, opts, 0)
	if err != nil {
		return 0, false
	}
	defer client.Close()
	if err := client.Ping(ctx).Err(); err != nil {
		return 0, false
	}
	res, err := client.Do(ctx, "CONFIG", "GET", "databases").Result()
	if err != nil {
		return 0, false
	}
	// go-redis v9 在 RESP3 下把 CONFIG GET 解析为 map；
	// 兼容旧驱动/代理返回的 []interface{}{key, value}。
	var raw string
	switch v := res.(type) {
	case []interface{}:
		if len(v) < 2 {
			return 0, false
		}
		raw, _ = v[1].(string)
	case map[interface{}]interface{}:
		for k, val := range v {
			if ks, ok := k.(string); ok && strings.EqualFold(ks, "databases") {
				raw, _ = val.(string)
			}
		}
	case map[string]interface{}:
		raw, _ = v["databases"].(string)
	default:
		return 0, false
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

func isDBRangeError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "db index is out of range") ||
		strings.Contains(msg, "invalid db index")
}

// Close 关闭底层连接池（可重复调用）。
//
// 设置页切换 Redis 配置时先关闭旧实例；关闭后 Enabled() 返回 false，
// 正在进行的请求会退化为缓存未命中，不影响业务。
func (c *Cache) Close() {
	if c == nil {
		return
	}
	c.closeOne.Do(func() {
		c.mu.Lock()
		client := c.client
		c.client = nil
		c.mu.Unlock()
		if client != nil {
			_ = client.Close()
		}
	})
}

// Enabled 缓存是否可用。
func (c *Cache) Enabled() bool {
	if c == nil {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.client != nil
}

// TTL 返回实际生效的缓存时长。
func (c *Cache) TTL() time.Duration {
	if c == nil || c.ttl <= 0 {
		return defaultTTL
	}
	return c.ttl
}

// Status 返回状态快照（不含密码）。
func (c *Cache) Status() Status {
	if c == nil {
		return Status{Enabled: false, LastError: "缓存未初始化"}
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	st := c.status
	st.Addrs = append([]string(nil), st.Addrs...)
	return st
}

// Ping 测量当前连接延迟；未启用时直接返回错误。
func (c *Cache) Ping(ctx context.Context) (time.Duration, error) {
	if !c.Enabled() {
		return 0, fmt.Errorf("Redis 未启用或不可用")
	}
	c.mu.RLock()
	client := c.client
	c.mu.RUnlock()
	start := time.Now()
	err := client.Ping(ctx).Err()
	return time.Since(start), err
}

// Test 执行一次 set/get/del，验证读写权限与序列化。
func (c *Cache) Test(ctx context.Context) (time.Duration, error) {
	if !c.Enabled() {
		return 0, fmt.Errorf("Redis 未启用或不可用")
	}
	start := time.Now()
	key := c.Key("_test", fmt.Sprintf("%d", time.Now().UnixNano()))
	val := time.Now().Format(time.RFC3339Nano)
	if err := c.SetBytes(ctx, key, []byte(val), 30*time.Second); err != nil {
		return time.Since(start), err
	}
	got, err := c.GetBytes(ctx, key)
	if err != nil {
		return time.Since(start), err
	}
	c.Del(ctx, key)
	if string(got) != val {
		return time.Since(start), fmt.Errorf("读写结果不一致")
	}
	return time.Since(start), nil
}

// Key 拼接带前缀的键；空片段会被忽略。
func (c *Cache) Key(parts ...string) string {
	prefix := "upgrade_mgr:"
	if c != nil {
		prefix = c.prefix
	}
	var b strings.Builder
	b.WriteString(prefix)
	for i, p := range parts {
		p = strings.Trim(p, ":")
		if p == "" {
			continue
		}
		if i > 0 && b.Len() > len(prefix) {
			b.WriteByte(':')
		}
		b.WriteString(p)
	}
	return b.String()
}

// KeyHash 将可能很长的查询串压缩为短哈希，控制键长度。
func KeyHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:12])
}

// GetJSON 读取并反序列化；未命中或解析失败返回 false。
func (c *Cache) GetJSON(ctx context.Context, key string, dest any) bool {
	data, err := c.GetBytes(ctx, key)
	if err != nil || len(data) == 0 {
		return false
	}
	return json.Unmarshal(data, dest) == nil
}

// SetJSON 序列化写入，ttl<=0 使用默认 TTL。
func (c *Cache) SetJSON(ctx context.Context, key string, value any, ttl time.Duration) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return c.SetBytes(ctx, key, data, ttl)
}

// GetBytes 读取原始值。
func (c *Cache) GetBytes(ctx context.Context, key string) ([]byte, error) {
	if !c.Enabled() {
		return nil, fmt.Errorf("缓存未启用")
	}
	c.mu.RLock()
	client := c.client
	c.mu.RUnlock()
	val, err := client.Get(ctx, key).Bytes()
	if err == redis.Nil {
		return nil, nil
	}
	return val, err
}

// SetBytes 写入原始值。
func (c *Cache) SetBytes(ctx context.Context, key string, data []byte, ttl time.Duration) error {
	if !c.Enabled() {
		return fmt.Errorf("缓存未启用")
	}
	if ttl <= 0 {
		ttl = c.ttl
	}
	c.mu.RLock()
	client := c.client
	c.mu.RUnlock()
	return client.Set(ctx, key, data, ttl).Err()
}

// Del 删除指定键。
func (c *Cache) Del(ctx context.Context, keys ...string) {
	if !c.Enabled() || len(keys) == 0 {
		return
	}
	c.mu.RLock()
	client := c.client
	c.mu.RUnlock()
	if err := client.Del(ctx, keys...).Err(); err != nil {
		log.Printf("⚠️ Redis 删除缓存失败: %v", err)
	}
}

// DelPrefix 按前缀删除（SCAN + DEL，避免 KEYS 阻塞）。
//
// 集群模式下键分散在多个主节点：必须逐 master 扫描，否则只能清掉一个分片。
func (c *Cache) DelPrefix(ctx context.Context, prefix string) int {
	if !c.Enabled() {
		return 0
	}
	c.mu.RLock()
	client := c.client
	c.mu.RUnlock()
	full := c.prefix + strings.TrimPrefix(strings.TrimSpace(prefix), ":")
	match := full + "*"

	if cc, ok := client.(*redis.ClusterClient); ok {
		deleted := 0
		err := cc.ForEachMaster(ctx, func(ctx context.Context, node *redis.Client) error {
			n, err := scanDelete(ctx, node, match)
			deleted += n
			return err
		})
		if err != nil {
			log.Printf("⚠️ Redis 集群按前缀删除失败 prefix=%s: %v", full, err)
		}
		return deleted
	}
	n, err := scanDelete(ctx, client, match)
	if err != nil {
		log.Printf("⚠️ Redis 按前缀删除失败 prefix=%s: %v", full, err)
	}
	return n
}

// scanDelete 在单个节点上 SCAN 并批量 DEL。
func scanDelete(ctx context.Context, client redis.UniversalClient, match string) (int, error) {
	deleted := 0
	var cursor uint64
	for {
		keys, next, err := client.Scan(ctx, cursor, match, 200).Result()
		if err != nil {
			return deleted, err
		}
		if len(keys) > 0 {
			if err := client.Del(ctx, keys...).Err(); err != nil {
				return deleted, err
			}
			deleted += len(keys)
		}
		cursor = next
		if cursor == 0 {
			return deleted, nil
		}
	}
}
