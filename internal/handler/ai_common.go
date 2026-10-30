package handler

import (
	"strings"

	"github.com/iflyelf/upgrade_mgr/internal/config"
	"github.com/iflyelf/upgrade_mgr/internal/mcp"
	"github.com/iflyelf/upgrade_mgr/skills"
)

// mcpPath MCP 服务端点路径（固定，启动时注册路由）。
const mcpPath = "/mcp"

// mcpProtocolVersions 支持的 MCP 协议版本列表。
func mcpProtocolVersions() []string {
	return mcp.SupportedProtocolVersions
}

// assistantConfigured 判断 AI 助手是否已配置（LLM 地址与模型）。
func assistantConfigured(c config.AIConfig) bool {
	return strings.TrimSpace(c.LLMBaseURL) != "" && strings.TrimSpace(c.LLMModel) != ""
}

// skillData 由当前设置构造 Skills 渲染数据。
func skillData(snap config.Config, baseURL string) skills.Data {
	return skills.Data{
		ServerName: snap.AI.MCPServerName,
		MCPURL:     baseURL + mcpPath,
		BaseURL:    baseURL,
		TokenEnv:   snap.AI.ClientTokenEnv,
	}
}

// skillCatalog 返回可用 Skills 清单（已按设置渲染）。
func skillCatalog(snap config.Config, baseURL string) []map[string]any {
	list, err := skills.Catalog(skillData(snap, baseURL))
	if err != nil {
		return []map[string]any{}
	}
	out := make([]map[string]any, len(list))
	for i, m := range list {
		out[i] = map[string]any{
			"name": m.Name, "description": m.Description,
			"license": m.License, "compatibility": m.Compatibility,
			"metadata": m.Metadata, "files": m.Files,
		}
	}
	return out
}

// extractBearer 提取 Authorization: Bearer <token> 中的 token（供 MCP / Skills 下载使用）。
func extractBearer(authHeader string) string {
	const prefix = "Bearer "
	if strings.HasPrefix(authHeader, prefix) {
		return strings.TrimSpace(authHeader[len(prefix):])
	}
	return ""
}
