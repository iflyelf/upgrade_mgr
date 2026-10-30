package config

import (
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// AIConfig AI 能力：MCP 服务、AI 访问令牌、Skills 下发与内置 AI 助手。
//
// 取值来源与其他配置一致：页面设置 > 环境变量 > 配置文件 > 内置默认值。
// 每个字段的环境变量名由 yaml 键自动推导为 AI_<大写键名>（见 AIEnvName），
// 新增字段无需再手工维护环境变量映射。所有项均为热生效：请求时读取设置快照。
type AIConfig struct {
	// ---- MCP 服务 ----
	MCPEnabled        bool   `yaml:"mcp_enabled"`
	MCPAllowWrite     bool   `yaml:"mcp_allow_write"`
	MCPRequireConfirm bool   `yaml:"mcp_require_confirm"`
	AllowCommandTasks bool   `yaml:"allow_command_tasks"`
	MCPServerName     string `yaml:"mcp_server_name"`
	MCPInstructions   string `yaml:"mcp_instructions"`
	MCPRateLimit      int    `yaml:"mcp_rate_limit"`
	MCPMaxBodyBytes   int    `yaml:"mcp_max_body_bytes"`
	ClientTokenEnv    string `yaml:"client_token_env"`

	// ---- AI 访问令牌 ----
	TokenPrefix         string `yaml:"token_prefix"`
	TokenMinRole        string `yaml:"token_min_role"`
	TokenDefaultTTL     string `yaml:"token_default_ttl"`
	TokenMaxTTL         string `yaml:"token_max_ttl"`
	TokenAllowPermanent bool   `yaml:"token_allow_permanent"`
	TokenMaxPerUser     int    `yaml:"token_max_per_user"`
	TokenRetentionDays  int    `yaml:"token_retention_days"`
	TokenTouchInterval  string `yaml:"token_touch_interval"`

	// ---- 工具输出与审计 ----
	ToolDefaultPageSize int  `yaml:"tool_default_page_size"`
	ToolOutputMaxBytes  int  `yaml:"tool_output_max_bytes"`
	LogMaxBytes         int  `yaml:"log_max_bytes"`
	AuditArgs           bool `yaml:"audit_args"`
	AuditArgsMaxBytes   int  `yaml:"audit_args_max_bytes"`

	// ---- 多副本同步 / Skills ----
	SettingsSyncInterval string `yaml:"settings_sync_interval"`
	SkillInstallDirs     string `yaml:"skill_install_dirs"`
	BridgeBinaryPrefix   string `yaml:"bridge_binary_prefix"`

	// ---- 内置 AI 助手（OpenAI 兼容接口） ----
	AssistantEnabled       bool   `yaml:"assistant_enabled"`
	AssistantAllowWrite    bool   `yaml:"assistant_allow_write"`
	AssistantSystemPrompt  string `yaml:"assistant_system_prompt"`
	AssistantMaxSteps      int    `yaml:"assistant_max_steps"`
	AssistantMaxHistory    int    `yaml:"assistant_max_history"`
	AssistantMaxInputChars int    `yaml:"assistant_max_input_chars"`
	AssistantRateLimit     int    `yaml:"assistant_rate_limit"`
	AssistantActionTTL     string `yaml:"assistant_action_ttl"`
	LLMMaxResponseBytes    int    `yaml:"llm_max_response_bytes"`
	LLMBaseURL             string `yaml:"llm_base_url"`
	LLMAPIKey              string `yaml:"llm_api_key"`
	LLMModel               string `yaml:"llm_model"`
	LLMAuthHeader          string `yaml:"llm_auth_header"`
	LLMAuthScheme          string `yaml:"llm_auth_scheme"`
	LLMTimeout             string `yaml:"llm_timeout"`
	LLMTemperature         string `yaml:"llm_temperature"`
	LLMMaxTokens           int    `yaml:"llm_max_tokens"`
}

// 默认文案：均可在页面「AI / MCP」分组或配置文件中覆盖。
const (
	defaultMCPInstructions = "这是 upgrade_mgr 灰度升级控制平台的 MCP 服务。" +
		"只读工具可直接调用；写操作（创建/启动/推进/取消/重试任务、Agent 自升级）会影响生产主机，" +
		"调用前必须向用户复述目标主机、批次策略与影响范围并获得明确同意，再携带 confirm=true 调用。" +
		"创建的任务一律为草稿（draft），需要单独调用 task_start 才会执行。" +
		"优先先用 task_preview 预览命中主机与分批，再创建任务。"
	defaultAssistantPrompt = "你是 upgrade_mgr 灰度升级控制平台的运维助手。" +
		"回答使用简体中文，结论先行、简洁准确。需要数据时调用工具查询，不要编造主机、任务或版本信息。" +
		"写操作不会被直接执行，而是生成待确认卡片由用户在页面上确认；生成写操作前先说明影响范围。" +
		"建议升级前先用 task_preview 预览，并优先使用金丝雀（canary）小批量策略。"
)

// AIIntRanges 整数项允许范围（配置文件加载与页面保存共用，避免两处规则不一致）。
var AIIntRanges = map[string][2]int{
	"mcp_rate_limit":            {0, 100000},
	"mcp_max_body_bytes":        {1024, 64 << 20},
	"token_max_per_user":        {1, 10000},
	"token_retention_days":      {1, 3650},
	"tool_default_page_size":    {1, 500},
	"tool_output_max_bytes":     {1024, 16 << 20},
	"log_max_bytes":             {200, 1 << 20},
	"audit_args_max_bytes":      {0, 64 << 10},
	"assistant_max_steps":       {1, 50},
	"assistant_max_history":     {1, 500},
	"assistant_max_input_chars": {100, 1 << 20},
	"assistant_rate_limit":      {0, 100000},
	"llm_max_tokens":            {0, 1 << 20},
	"llm_max_response_bytes":    {4096, 64 << 20},
}

// AIDurationKeys 时长项：值为是否允许留空。
var AIDurationKeys = map[string]bool{
	"token_default_ttl":      false,
	"token_max_ttl":          true,
	"token_touch_interval":   false,
	"settings_sync_interval": false,
	"llm_timeout":            false,
	"assistant_action_ttl":   false,
}

// AIRoleOptions 令牌创建最低角色候选值。
var AIRoleOptions = []string{"user", "admin", "superadmin"}

func defaultAI() AIConfig {
	return AIConfig{
		MCPEnabled:        true,
		MCPAllowWrite:     false,
		MCPRequireConfirm: true,
		AllowCommandTasks: false,
		MCPServerName:     "upgrade-mgr",
		MCPInstructions:   defaultMCPInstructions,
		MCPRateLimit:      120,
		MCPMaxBodyBytes:   1 << 20,
		ClientTokenEnv:    "UPGRADE_MGR_TOKEN",

		TokenPrefix:         "upm_",
		TokenMinRole:        "user",
		TokenDefaultTTL:     "720h",
		TokenMaxTTL:         "",
		TokenAllowPermanent: true,
		TokenMaxPerUser:     20,
		TokenRetentionDays:  30,
		TokenTouchInterval:  "1m",

		ToolDefaultPageSize: 20,
		ToolOutputMaxBytes:  64 << 10,
		LogMaxBytes:         4000,
		AuditArgs:           true,
		AuditArgsMaxBytes:   1024,

		SettingsSyncInterval: "5s",
		SkillInstallDirs:     "opencode=.opencode/skills,claude=.claude/skills,codex=.agents/skills",
		BridgeBinaryPrefix:   "upgrade-mgr-mcp-bridge",

		AssistantEnabled:       false,
		AssistantAllowWrite:    true,
		AssistantSystemPrompt:  defaultAssistantPrompt,
		AssistantMaxSteps:      8,
		AssistantMaxHistory:    30,
		AssistantMaxInputChars: 8000,
		AssistantRateLimit:     20,
		AssistantActionTTL:     "10m",
		LLMMaxResponseBytes:    4 << 20,
		LLMAuthHeader:          "Authorization",
		LLMAuthScheme:          "Bearer",
		LLMTimeout:             "120s",
	}
}

// AIEnvName 返回 AI 配置项（yaml 键）对应的环境变量名，如 mcp_enabled → AI_MCP_ENABLED。
func AIEnvName(yamlKey string) string { return "AI_" + strings.ToUpper(yamlKey) }

// AIYAMLKeys 返回 AIConfig 全部 yaml 键（按字段顺序），供环境变量绑定与一致性测试使用。
func AIYAMLKeys() []string {
	t := reflect.TypeOf(AIConfig{})
	out := make([]string, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		if k := yamlKey(t.Field(i)); k != "" {
			out = append(out, k)
		}
	}
	return out
}

func yamlKey(f reflect.StructField) string {
	tag := f.Tag.Get("yaml")
	if tag == "" || tag == "-" {
		return ""
	}
	return strings.Split(tag, ",")[0]
}

// applyAIEnv 按 AI_<KEY> 环境变量覆盖 AI 配置（反射遍历，字段与环境变量天然一致）。
func (a *AIConfig) applyEnv() {
	v := reflect.ValueOf(a).Elem()
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		key := yamlKey(t.Field(i))
		if key == "" {
			continue
		}
		raw, ok := os.LookupEnv(AIEnvName(key))
		if !ok || raw == "" {
			continue
		}
		fv := v.Field(i)
		switch fv.Kind() {
		case reflect.Bool:
			fv.SetBool(raw == "true" || raw == "1")
		case reflect.Int:
			if n, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil {
				fv.SetInt(int64(n))
			}
		case reflect.String:
			fv.SetString(raw)
		}
	}
}

// FieldString 读取 AI 配置字段（yaml 键）的字符串形式；未知键返回 ok=false。
func (a *AIConfig) FieldString(yamlKey string) (string, bool) {
	fv, ok := a.field(yamlKey)
	if !ok {
		return "", false
	}
	switch fv.Kind() {
	case reflect.Bool:
		return strconv.FormatBool(fv.Bool()), true
	case reflect.Int:
		return strconv.FormatInt(fv.Int(), 10), true
	default:
		return fv.String(), true
	}
}

// SetField 按 yaml 键写入字符串值（bool 接受 true/1，int 解析失败时保持原值）。
func (a *AIConfig) SetField(yamlKey, raw string) bool {
	fv, ok := a.field(yamlKey)
	if !ok {
		return false
	}
	switch fv.Kind() {
	case reflect.Bool:
		fv.SetBool(strings.TrimSpace(raw) == "true" || strings.TrimSpace(raw) == "1")
	case reflect.Int:
		n, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil {
			return false
		}
		fv.SetInt(int64(n))
	default:
		fv.SetString(raw)
	}
	return true
}

// FieldKind 返回字段类型：bool / int / string；未知键返回空串。
func (a *AIConfig) FieldKind(yamlKey string) string {
	fv, ok := a.field(yamlKey)
	if !ok {
		return ""
	}
	switch fv.Kind() {
	case reflect.Bool:
		return "bool"
	case reflect.Int:
		return "int"
	default:
		return "string"
	}
}

func (a *AIConfig) field(key string) (reflect.Value, bool) {
	v := reflect.ValueOf(a).Elem()
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		if yamlKey(t.Field(i)) == key {
			return v.Field(i), true
		}
	}
	return reflect.Value{}, false
}

// ValidateAIValue 校验单个 AI 配置值（页面保存与配置文件加载共用）。
func ValidateAIValue(key, raw string) error {
	raw = strings.TrimSpace(raw)
	if r, ok := AIIntRanges[key]; ok {
		n, err := strconv.Atoi(raw)
		if err != nil || n < r[0] || n > r[1] {
			return fmt.Errorf("应为 %d-%d 的整数", r[0], r[1])
		}
		return nil
	}
	if allowEmpty, ok := AIDurationKeys[key]; ok {
		if raw == "" {
			if allowEmpty {
				return nil
			}
			return fmt.Errorf("不能为空（示例 30s / 15m / 720h）")
		}
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			return fmt.Errorf("无效时长（示例 30s / 15m / 720h）")
		}
		return nil
	}
	switch key {
	case "token_min_role":
		for _, o := range AIRoleOptions {
			if raw == o {
				return nil
			}
		}
		return fmt.Errorf("只能是 %s", strings.Join(AIRoleOptions, " / "))
	case "mcp_server_name":
		if raw == "" || len(raw) > 64 || !regexpExternalName.MatchString(raw) {
			return fmt.Errorf("1-64 位字母数字、点、下划线、短横线")
		}
	case "client_token_env":
		if !regexpEnvName.MatchString(raw) {
			return fmt.Errorf("必须是合法环境变量名（字母/下划线开头，仅字母数字下划线）")
		}
	case "bridge_binary_prefix":
		if raw == "" || len(raw) > 64 || !regexpExternalName.MatchString(raw) {
			return fmt.Errorf("1-64 位字母数字、点、下划线、短横线")
		}
	case "token_prefix":
		if raw == "" || len(raw) > 16 || !regexpExternalName.MatchString(raw) {
			return fmt.Errorf("1-16 位字母数字、点、下划线、短横线")
		}
	case "llm_base_url":
		if raw == "" {
			return nil
		}
		if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
			return fmt.Errorf("必须以 http:// 或 https:// 开头")
		}
	case "llm_temperature":
		if raw == "" {
			return nil
		}
		f, err := strconv.ParseFloat(raw, 64)
		if err != nil || f < 0 || f > 2 {
			return fmt.Errorf("应为 0-2 之间的数字，留空表示使用模型默认值")
		}
	case "llm_auth_header":
		if raw == "" || strings.ContainsAny(raw, " :\r\n") {
			return fmt.Errorf("必须是合法 HTTP 头名")
		}
	case "skill_install_dirs":
		if _, err := ParseSkillInstallDirs(raw); err != nil {
			return err
		}
	}
	return nil
}

// SkillInstallDir 某个 AI 客户端的 Skills 安装目录。
type SkillInstallDir struct {
	Client string `json:"client"`
	Dir    string `json:"dir"`
}

// ParseSkillInstallDirs 解析 "client=dir,client2=dir2"。
func ParseSkillInstallDirs(raw string) ([]SkillInstallDir, error) {
	var out []SkillInstallDir
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		k, v, ok := strings.Cut(part, "=")
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if !ok || k == "" || v == "" {
			return nil, fmt.Errorf("Skills 安装目录 %q 格式应为 客户端=目录", part)
		}
		if !regexpExternalName.MatchString(k) {
			return nil, fmt.Errorf("客户端名 %q 非法", k)
		}
		if strings.ContainsAny(v, "'\"`$;&|<>\r\n") || strings.Contains(v, "..") {
			return nil, fmt.Errorf("目录 %q 含非法字符", v)
		}
		out = append(out, SkillInstallDir{Client: k, Dir: strings.TrimRight(v, "/")})
	}
	return out, nil
}

// validate 校验全部 AI 配置（配置文件/环境变量加载后调用）。
func (a *AIConfig) validate() error {
	for _, k := range AIYAMLKeys() {
		s, _ := a.FieldString(k)
		if err := ValidateAIValue(k, s); err != nil {
			return fmt.Errorf("ai.%s 配置错误: %w", k, err)
		}
	}
	return nil
}
