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

// SetExternalURL 注入对外访问地址（server.external_url），主机未上报连接地址时兜底。
func (s *Service) SetExternalURL(fn func() string) { s.externalURL = fn }

// hostPlatformURL 返回下发给主机脚本的平台地址：
// 优先 Agent 心跳上报的当前连接地址（该主机一定能访问），其次 external_url。
func (s *Service) hostPlatformURL(agentURL string) string {
	if u := strings.TrimRight(strings.TrimSpace(agentURL), "/"); u != "" {
		return u
	}
	if s.externalURL != nil {
		return strings.TrimRight(strings.TrimSpace(s.externalURL()), "/")
	}
	return ""
}

// appendPlatformEnv 在 env 中补充 UPGRADE_MGR_SERVER；用户已显式设置时不覆盖。
// 返回新切片，不修改调用方共享的原切片。
func appendPlatformEnv(env []string, platformURL string) []string {
	if platformURL == "" {
		return env
	}
	for _, kv := range env {
		if strings.HasPrefix(kv, legacyPlatformEnv+"=") {
			return env
		}
	}
	out := make([]string, 0, len(env)+1)
	out = append(out, env...)
	return append(out, legacyPlatformEnv+"="+platformURL)
}

// withPlatformEnv 返回补充了平台地址环境变量的命令规格副本。
func withPlatformEnv(spec protocol.CommandSpec, platformURL string) protocol.CommandSpec {
	spec.Env = appendPlatformEnv(spec.Env, platformURL)
	return spec
}
