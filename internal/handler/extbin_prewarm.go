package handler

import (
	"context"
	"log"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/metrics"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/extbin"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

// extbinPrewarmDelay 启动后首次预热前的等待，避开迁移/同步等启动期负载。
const extbinPrewarmDelay = 20 * time.Second

// extbinPrewarmInterval 预热检查间隔：缓存过期（TTL）或被清理后自动补齐。
const extbinPrewarmInterval = time.Hour

// StartExternalBinaryPrewarm 后台预下载外部文件白名单（含内置条目）到平台缓存。
//
// 目标主机的脚本先请求 ?local=1（只读本地/缓存，不触发现场下载），
// 预热后无需管理员手动「刷新缓存」即可命中；平台无法出网时只记录日志，
// 不影响服务，管理员仍可上传离线文件。
// 已上传离线文件或缓存未过期的条目跳过；逐个串行下载，避免启动时突发占用带宽。
func StartExternalBinaryPrewarm(ctx context.Context, s *svc.ServiceContext) {
	go func() {
		timer := time.NewTimer(extbinPrewarmDelay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		prewarmExternalBinaries(ctx, s)
		ticker := time.NewTicker(extbinPrewarmInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				prewarmExternalBinaries(ctx, s)
			}
		}
	}()
	log.Printf("✅ 外部文件缓存自动预热已启动（启动后 %s 首次执行，之后每 %s 检查）",
		extbinPrewarmDelay, extbinPrewarmInterval)
}

// prewarmExternalBinaries 执行一轮预热，返回成功下载的条目数（供测试断言）。
func prewarmExternalBinaries(ctx context.Context, s *svc.ServiceContext) int {
	cfg := externalBinaryConfig(s)
	entries, err := extbin.Parse(effectiveExternalBinaries(cfg))
	if err != nil {
		log.Printf("⚠️ 外部文件预热跳过：白名单配置错误: %v", err)
		return 0
	}
	baseDir := externalBinaryBaseDir(cfg)
	ttl := effectiveExternalTTL(cfg)
	hosts := effectiveExternalHosts(cfg)
	rule := externalAllowRule(cfg)
	proxyBase := effectiveExternalProxy(cfg)
	proxyHosts := effectiveExternalProxyHosts(cfg)

	fetched := 0
	for _, e := range entries {
		if ctx.Err() != nil {
			return fetched
		}
		if fileExists(externalBinaryLocalPath(baseDir, e)) {
			continue // 管理员上传的离线文件优先，无需下载
		}
		target := externalBinaryCachePath(baseDir, e)
		if freshFile(target, ttl) {
			continue
		}
		// 目录由 fetchExternalBinaryOnce 创建；下载走同一把文件锁，与主机请求不会重复下载。
		if err := downloadWithLock(ctx, target, e, hosts, rule, proxyBase, proxyHosts, ttl); err != nil {
			if ctx.Err() == nil {
				log.Printf("⚠️ 外部文件预热 %s 失败（可在「外部文件代缓存」上传离线文件）: %v", e.Name, err)
				metrics.ExternalBinaryRequests.WithLabelValues(e.Name, "prewarm_error").Inc()
			}
			continue
		}
		metrics.ExternalBinaryRequests.WithLabelValues(e.Name, "prewarm").Inc()
		fetched++
	}
	if fetched > 0 {
		log.Printf("✅ 外部文件预热完成：本轮下载 %d 个条目", fetched)
	}
	return fetched
}
