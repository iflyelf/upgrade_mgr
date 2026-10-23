// Package extbinprobe 定时探测外部文件源可用性，并导出 Prometheus 指标。
//
// 为什么需要：代缓存的白名单指向公网/内网镜像；一旦上游不可达，
// 主机拉取会失败但控制台本身可能一切正常。定时探测可提前发现并告警。
//
// 设计要点：
//   - 只读取响应头/前 64KB（Range），不落盘、不占用缓存；
//   - 串行探测（默认每分钟），避免对上游产生突发压力；
//   - 单个条目失败不影响其它条目；
//   - 探测结果同时更新 up / probe_seconds / cached / cached_bytes 指标。
package extbinprobe

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/metrics"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/extbin"
)

// DefaultInterval 默认探测间隔。
const DefaultInterval = time.Minute

// probeTimeout 单条目探测超时。
const probeTimeout = 15 * time.Second

// Provider 提供当前白名单与缓存信息（由 handler/svc 注入，避免循环依赖）。
type Provider interface {
	// Entries 返回当前生效的白名单。
	Entries() []extbin.Entry
	// Hosts 返回允许的上游域名。
	Hosts() []string
	// AllowRule 返回内网放行策略。
	AllowRule() extbin.AllowRule
	// CachePath 返回条目对应的缓存文件路径。
	CachePath(e extbin.Entry) string
}

// Prober 定时探测器。
type Prober struct {
	provider Provider
	client   *http.Client
	interval time.Duration
}

// New 创建探测器；interval<=0 使用 DefaultInterval。
func New(p Provider, interval time.Duration) *Prober {
	if interval <= 0 {
		interval = DefaultInterval
	}
	return &Prober{
		provider: p,
		client:   &http.Client{Timeout: probeTimeout},
		interval: interval,
	}
}

// Run 启动循环，直到 ctx 结束。
func (p *Prober) Run(ctx context.Context) {
	// 启动后先等 15 秒：避免与启动时的其它初始化（迁移、Consul 同步）抢占。
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
	}

	p.ProbeAll(ctx)

	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.ProbeAll(ctx)
		}
	}
}

// ProbeAll 探测全部条目并更新指标。
func (p *Prober) ProbeAll(ctx context.Context) {
	entries := p.provider.Entries()
	hosts := p.provider.Hosts()
	rule := p.provider.AllowRule()

	for _, e := range entries {
		if ctx.Err() != nil {
			return
		}
		p.probeOne(ctx, e, hosts, rule)
	}
}

// ProbeOne 探测单个条目（供测试与手动触发）。
func (p *Prober) ProbeOne(ctx context.Context, e extbin.Entry) bool {
	return p.probeOne(ctx, e, p.provider.Hosts(), p.provider.AllowRule())
}

func (p *Prober) probeOne(ctx context.Context, e extbin.Entry, hosts []string, rule extbin.AllowRule) bool {
	start := time.Now()
	checkErr := extbin.CheckHost(e.URL, hosts, rule)
	if checkErr == nil {
		checkErr = p.requestHead(ctx, e)
	}
	elapsed := time.Since(start).Seconds()

	typ := "binary"
	if e.Script {
		typ = "script"
	}
	metrics.ExternalBinaryProbeSeconds.WithLabelValues(e.Name).Set(elapsed)
	if checkErr == nil {
		metrics.ExternalBinaryUp.WithLabelValues(e.Name, typ).Set(1)
		metrics.ExternalBinaryProbeTotal.WithLabelValues(e.Name, "success").Inc()
	} else {
		metrics.ExternalBinaryUp.WithLabelValues(e.Name, typ).Set(0)
		metrics.ExternalBinaryProbeTotal.WithLabelValues(e.Name, "error").Inc()
		log.Printf("⚠️ 外部文件不可用 %s (%s): %v", e.Name, e.URL, checkErr)
	}

	// 缓存状态：命中即记录大小与年龄，否则清零。
	target := p.provider.CachePath(e)
	if info, err := os.Stat(target); err == nil && !info.IsDir() && info.Size() > 0 {
		metrics.ExternalBinaryCached.WithLabelValues(e.Name).Set(1)
		metrics.ExternalBinaryCachedBytes.WithLabelValues(e.Name).Set(float64(info.Size()))
		metrics.ExternalBinaryCacheAge.WithLabelValues(e.Name).Set(time.Since(info.ModTime()).Seconds())
	} else {
		metrics.ExternalBinaryCached.WithLabelValues(e.Name).Set(0)
		metrics.ExternalBinaryCachedBytes.WithLabelValues(e.Name).Set(0)
		metrics.ExternalBinaryCacheAge.WithLabelValues(e.Name).Set(0)
	}
	return checkErr == nil
}

// requestHead 只取响应头 + 少量内容，校验可达性与内容类型。
func (p *Prober) requestHead(ctx context.Context, e extbin.Entry) error {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.URL, nil)
	if err != nil {
		return err
	}
	// Range 只取前 64KB：够判断状态与内容，不浪费带宽。
	req.Header.Set("Range", "bytes=0-65535")
	req.Header.Set("User-Agent", "upgrade-mgr/extbin-probe")

	client := &http.Client{
		Timeout:   probeTimeout,
		Transport: p.client.Transport,
		CheckRedirect: func(next *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("重定向次数过多")
			}
			hosts := p.provider.Hosts()
			rule := p.provider.AllowRule()
			if err := extbin.CheckHost(next.URL.String(), hosts, rule); err != nil {
				return fmt.Errorf("重定向目标被拒绝: %w", err)
			}
			return nil
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	head, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if len(head) == 0 {
		return fmt.Errorf("响应内容为空")
	}
	sample := strings.TrimSpace(strings.ToLower(string(head)))
	if e.Script {
		for _, prefix := range []string{"<!doctype", "<html", "<head", "<body", "<?xml"} {
			if strings.HasPrefix(sample, prefix) {
				return fmt.Errorf("内容不是脚本（HTML/XML 页面）")
			}
		}
	} else if !strings.HasPrefix(sample, "mz") {
		return fmt.Errorf("内容不是 Windows 可执行文件（缺少 MZ 头）")
	}
	return nil
}
