package handler

import (
	"strings"

	"github.com/iflyelf/upgrade_mgr/internal/config"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/extbin"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

// extbinProvider 为定时探测器提供当前白名单与缓存路径。
//
// 放在 handler 包内是为了复用“配置 → 生效白名单 / 缓存路径”的唯一实现，
// 避免探测与下载两处逻辑漂移。
//
// 每次调用都读取设置快照：页面改完白名单后，下一轮探测自动使用新配置（热加载）。
type extbinProvider struct {
	svc *svc.ServiceContext
}

func (p extbinProvider) config() *config.Config { return externalBinaryConfig(p.svc) }

// Entries 当前生效的白名单。
func (p extbinProvider) Entries() []extbin.Entry {
	entries, err := extbin.Parse(effectiveExternalBinaries(p.config()))
	if err != nil {
		return nil
	}
	return entries
}

// Hosts 允许的上游域名。
func (p extbinProvider) Hosts() []string { return effectiveExternalHosts(p.config()) }

// AllowRule 内网放行策略。
func (p extbinProvider) AllowRule() extbin.AllowRule { return externalAllowRule(p.config()) }

// CachePath 条目对应的缓存文件路径。
func (p extbinProvider) CachePath(e extbin.Entry) string {
	return externalBinaryCachePath(externalBinaryBaseDir(p.config()), e)
}

// NewExtbinProvider 供 svc 启动探测器时构造 Provider。
//
// 参数类型使用小写接口实现，避免 svc 依赖 handler 的具体结构：
// 这里导出为返回 extbinprobe.Provider 的重载形式。
func NewExtbinProvider(s *svc.ServiceContext) extbinProvider { return extbinProvider{svc: s} }

// extbinCacheKey 规范化缓存文件名（供测试断言使用）。
func extbinCacheKey(e extbin.Entry) string {
	name := strings.ToLower(e.Name)
	if !e.Script {
		name += ".exe"
	}
	return name
}
