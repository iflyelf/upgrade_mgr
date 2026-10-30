package setting

import (
	"github.com/iflyelf/upgrade_mgr/internal/config"
)

// aiMeta AI 设置项的页面展示元数据。
//
// 键、类型、取值与校验都来自 config.AIConfig（yaml 键 + 反射），这里只补充
// 页面需要的分组/标签/说明，避免在多处重复维护同一份字段清单。
type aiMeta struct {
	Group   string
	Label   string
	Type    string // 留空时按字段类型推导（bool/int/string）
	Secret  bool
	Options []string
	Note    string
}

const (
	aiGroupMCP       = "AI / MCP 服务"
	aiGroupToken     = "AI 访问令牌"
	aiGroupTool      = "AI 工具输出与审计"
	aiGroupSync      = "AI Skills 与同步"
	aiGroupAssistant = "AI 助手（OpenAI 兼容 LLM）"
)

var aiMetas = map[string]aiMeta{
	"mcp_enabled": {Group: aiGroupMCP, Label: "启用 MCP 服务（/mcp，供 OpenCode / Claude Code / Codex 等接入）"},
	"mcp_allow_write": {Group: aiGroupMCP, Label: "全局允许 MCP 写操作（创建/启动/暂停/推进/取消/重试任务、Agent 自升级）",
		Note: "⚠️ 写操作会影响生产主机。需同时满足：本开关开启 + 令牌勾选「允许写」+ 用户本身具备对应权限。"},
	"mcp_require_confirm": {Group: aiGroupMCP, Label: "高风险工具必须携带 confirm=true",
		Note: "启动/取消任务、强制重试、Agent 自升级需 AI 客户端显式确认后再调用。"},
	"allow_command_tasks": {Group: aiGroupMCP, Label: "允许经 AI 创建命令（脚本）任务",
		Note: "命令任务可在主机上执行任意脚本；仍要求管理员身份与 agent.allow_exec_mode 开启。"},
	"mcp_server_name":    {Group: aiGroupMCP, Label: "MCP 服务名（initialize 返回，也用于客户端配置片段的服务器名）"},
	"mcp_instructions":   {Group: aiGroupMCP, Label: "MCP 服务说明（initialize 返回给 AI 客户端的使用指引）", Type: "text"},
	"mcp_rate_limit":     {Group: aiGroupMCP, Label: "MCP 限流（每令牌每分钟请求数，0=不限）"},
	"mcp_max_body_bytes": {Group: aiGroupMCP, Label: "MCP 单次请求体上限（字节）"},
	"client_token_env": {Group: aiGroupMCP, Label: "客户端配置片段中使用的令牌环境变量名",
		Note: "页面生成 OpenCode / Claude Code / Codex 配置与 stdio 桥命令时引用该环境变量，令牌不写入配置文件。"},

	"token_prefix":   {Group: aiGroupToken, Label: "令牌前缀（仅影响新生成的令牌）"},
	"token_min_role": {Group: aiGroupToken, Label: "允许创建 AI 令牌的最低角色", Type: "enum", Options: config.AIRoleOptions},
	"token_default_ttl": {Group: aiGroupToken, Label: "令牌默认有效期（如 720h）",
		Note: "页面未选择有效期时使用。"},
	"token_max_ttl": {Group: aiGroupToken, Label: "令牌最长有效期（留空=不限制）"},
	"token_allow_permanent": {Group: aiGroupToken, Label: "允许创建永久令牌",
		Note: "关闭后仅能创建带到期时间的令牌；已有永久令牌不受影响，可在页面注销。"},
	"token_max_per_user":   {Group: aiGroupToken, Label: "每个用户最多有效令牌数"},
	"token_retention_days": {Group: aiGroupToken, Label: "已注销/过期令牌保留天数（每日维护自动清理）"},
	"token_touch_interval": {Group: aiGroupToken, Label: "最近使用时间回写间隔（如 1m，降低写库频率）"},

	"tool_default_page_size": {Group: aiGroupTool, Label: "工具列表默认每页条数"},
	"tool_output_max_bytes":  {Group: aiGroupTool, Label: "单次工具返回最大字节数（超出截断，避免撑爆模型上下文）"},
	"log_max_bytes":          {Group: aiGroupTool, Label: "任务逐机日志最大字节数（工具返回时截取末尾）"},
	"audit_args":             {Group: aiGroupTool, Label: "审计记录工具调用参数摘要"},
	"audit_args_max_bytes":   {Group: aiGroupTool, Label: "审计参数摘要最大字节数"},

	"settings_sync_interval": {Group: aiGroupSync, Label: "多副本 AI 设置同步间隔（如 5s）",
		Note: "当前副本保存后立即生效；其他副本按该间隔从数据库同步。修改间隔本身在下次启动生效。"},
	"skill_install_dirs": {Group: aiGroupSync, Label: "Skills 安装目录（客户端=目录，逗号分隔）",
		Note: "页面据此生成安装命令，如 opencode=.opencode/skills,claude=.claude/skills,codex=.agents/skills。"},

	"bridge_binary_prefix":   {Group: aiGroupSync, Label: "stdio 桥二进制文件名前缀（放在 Agent 二进制目录，按 <前缀>-<os>-<arch>[.exe] 下载）"},
	"assistant_action_ttl":   {Group: aiGroupAssistant, Label: "助手待确认操作有效期（如 10m，超时需重新生成）"},
	"llm_max_response_bytes": {Group: aiGroupAssistant, Label: "LLM 单次响应最大字节数"},

	"assistant_enabled": {Group: aiGroupAssistant, Label: "启用内置 AI 助手",
		Note: "⚠️ 启用后，对话内容及工具查询到的主机/任务数据会发送到下方配置的 LLM 服务。"},
	"assistant_allow_write":     {Group: aiGroupAssistant, Label: "助手可生成写操作（需用户在页面逐条确认后执行）"},
	"assistant_system_prompt":   {Group: aiGroupAssistant, Label: "助手系统提示词", Type: "text"},
	"assistant_max_steps":       {Group: aiGroupAssistant, Label: "单轮最多工具调用步数"},
	"assistant_max_history":     {Group: aiGroupAssistant, Label: "最多携带历史消息条数"},
	"assistant_max_input_chars": {Group: aiGroupAssistant, Label: "单条用户消息最大字符数"},
	"assistant_rate_limit":      {Group: aiGroupAssistant, Label: "助手限流（每用户每分钟请求数，0=不限）"},
	"llm_base_url": {Group: aiGroupAssistant, Label: "LLM 接口地址（OpenAI 兼容，如 https://api.openai.com/v1）",
		Note: "服务端请求 {地址}/chat/completions。"},
	"llm_api_key":     {Group: aiGroupAssistant, Label: "LLM API Key", Type: "secret", Secret: true},
	"llm_model":       {Group: aiGroupAssistant, Label: "模型名称"},
	"llm_auth_header": {Group: aiGroupAssistant, Label: "认证请求头名（默认 Authorization；Azure 等可改为 api-key）"},
	"llm_auth_scheme": {Group: aiGroupAssistant, Label: "认证前缀（默认 Bearer，留空则只发送 Key）"},
	"llm_timeout":     {Group: aiGroupAssistant, Label: "LLM 请求超时（如 120s）"},
	"llm_temperature": {Group: aiGroupAssistant, Label: "temperature（0-2，留空使用模型默认）"},
	"llm_max_tokens":  {Group: aiGroupAssistant, Label: "max_tokens（0=不传，使用模型默认）"},
}

// aiRegistry 由 config.AIConfig 字段自动生成 AI 设置项。
func aiRegistry() []Item {
	keys := config.AIYAMLKeys()
	probe := config.Default().AI
	out := make([]Item, 0, len(keys))
	for _, k := range keys {
		k := k
		m, ok := aiMetas[k]
		if !ok {
			// 新增字段忘记补元数据时仍可配置（测试会提示补齐）。
			m = aiMeta{Group: aiGroupMCP, Label: k}
		}
		typ := m.Type
		if typ == "" {
			typ = probe.FieldKind(k)
		}
		out = append(out, Item{
			Key: "ai." + k, Group: m.Group, Label: m.Label, Type: typ,
			Secret: m.Secret, Options: m.Options, Note: m.Note,
			Get: func(c *config.Config) string { v, _ := c.AI.FieldString(k); return v },
			Set: func(c *config.Config, v string) {
				if config.ValidateAIValue(k, v) == nil {
					c.AI.SetField(k, v)
				}
			},
			Validate: func(v string) error { return config.ValidateAIValue(k, v) },
		})
	}
	return out
}

// NeedsAIReload 判断变更是否涉及 AI 设置（当前全部为请求时读取，无需重建对象）。
func NeedsAIReload(applied []string) bool {
	for _, k := range applied {
		if len(k) > 3 && k[:3] == "ai." {
			return true
		}
	}
	return false
}
