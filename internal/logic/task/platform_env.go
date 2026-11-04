package task

import (
	"strings"

	"github.com/iflyelf/upgrade_mgr/internal/protocol"
)

// legacyPlatformEnv 早期任务示例脚本读取的平台地址变量。
//
// 旧脚本写的是 b="${UPGRADE_MGR_SERVER:-http://localhost:8090}"，而 Agent 从未注入该变量，
// 导致脚本在目标机上访问 localhost 下载失败。已保存的旧任务重试时仍是旧脚本，
// 因此下发时由服务端补上该变量，指向主机实际可达的平台地址。
const legacyPlatformEnv = "UPGRADE_MGR_SERVER"

// externalURLEnv 是设置页/配置文件中的 server.external_url。
//
// 示例脚本通过它动态获取服务端对外访问地址，不再硬编码 IP/域名；
// 与 UPGRADE_MGR_SERVER 的区别：后者优先主机当前实际连通的地址，前者始终是对外地址。
const externalURLEnv = "UPGRADE_MGR_EXTERNAL_URL"

// SetExternalURL 注入对外访问地址（server.external_url），主机未上报连接地址时兜底。
func (s *Service) SetExternalURL(fn func() string) { s.externalURL = fn }

// externalURLValue 返回规范化的 server.external_url。
func (s *Service) externalURLValue() string {
	if s.externalURL == nil {
		return ""
	}
	return strings.TrimRight(strings.TrimSpace(s.externalURL()), "/")
}

// hostPlatformURL 返回下发给主机脚本的平台地址：
// 优先 Agent 心跳上报的当前连接地址（该主机一定能访问），其次 external_url。
func (s *Service) hostPlatformURL(agentURL string) string {
	if u := strings.TrimRight(strings.TrimSpace(agentURL), "/"); u != "" {
		return u
	}
	return s.externalURLValue()
}

// appendEnv 在 env 中补充 key=val；用户已显式设置同名变量时不覆盖。
// 返回值总是可安全追加的新切片（或原切片），不会复用调用方共享切片的底层数组。
func appendEnv(env []string, key, val string) []string {
	if val == "" {
		return env
	}
	prefix := key + "="
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			return env
		}
	}
	out := make([]string, 0, len(env)+1)
	out = append(out, env...)
	return append(out, prefix+val)
}

// appendPlatformEnv 补充平台地址变量：
//   - UPGRADE_MGR_SERVER：主机实际可达的平台地址（兼容旧脚本）；
//   - UPGRADE_MGR_EXTERNAL_URL：服务端配置的对外访问地址（示例脚本动态读取）。
func appendPlatformEnv(env []string, platformURL, externalURL string) []string {
	env = appendEnv(env, legacyPlatformEnv, platformURL)
	return appendEnv(env, externalURLEnv, externalURL)
}

// withPlatformEnv 返回补充了平台地址环境变量的命令规格副本。
func withPlatformEnv(spec protocol.CommandSpec, platformURL, externalURL string) protocol.CommandSpec {
	spec.Env = appendPlatformEnv(spec.Env, platformURL, externalURL)
	return spec
}
