package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/auth"
	"github.com/iflyelf/upgrade_mgr/internal/config"
	"github.com/iflyelf/upgrade_mgr/internal/logic/aitoken"
	"github.com/iflyelf/upgrade_mgr/internal/mcp"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/audit"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

// AI 工具调用渠道。
const (
	channelMCP       = "mcp"
	channelAssistant = "assistant"
)

// AIToolsProvider 实现 mcp.Provider：按当前身份动态列出与执行工具。
//
// 工具内部直接调用现有 HTTP handler（注入当前用户到 context），因此租户隔离、
// 组件 deploy 权限、owner 策略、主机分组范围与业务审计全部沿用，不另写一套权限逻辑。
type AIToolsProvider struct {
	svc      *svc.ServiceContext
	user     *auth.User
	token    *aitoken.Token
	request  *http.Request
	snapshot config.Config
	channel  string
}

// NewAIToolsProvider 构造工具提供方（MCP 与内置助手共用）。
func NewAIToolsProvider(s *svc.ServiceContext, u *auth.User, t *aitoken.Token, r *http.Request, channel string) *AIToolsProvider {
	return &AIToolsProvider{svc: s, user: u, token: t, request: r, snapshot: s.Settings.Snapshot(), channel: channel}
}

// Info 实现 mcp.Provider。
func (p *AIToolsProvider) Info(ctx context.Context) mcp.Info {
	return mcp.Info{
		Name:         p.snapshot.AI.MCPServerName,
		Version:      p.svc.Version,
		Instructions: p.snapshot.AI.MCPInstructions,
	}
}

// ListTools 实现 mcp.Provider。
func (p *AIToolsProvider) ListTools(ctx context.Context) []mcp.ToolInfo {
	specs := p.tools()
	out := make([]mcp.ToolInfo, len(specs))
	for i, s := range specs {
		out[i] = mcp.ToolInfo{
			Name: s.Name, Title: s.Title, Description: s.Description, InputSchema: s.Schema,
			Annotations: &mcp.Annotations{
				Title: s.Title, ReadOnlyHint: s.ReadOnly, DestructiveHint: s.Destructive,
				IdempotentHint: s.ReadOnly,
			},
		}
	}
	return out
}

// CallTool 实现 mcp.Provider：执行工具并记录审计。
func (p *AIToolsProvider) CallTool(ctx context.Context, name string, argsJSON json.RawMessage) (*mcp.CallResult, error) {
	spec, ok := p.find(name)
	if !ok {
		return nil, mcp.ErrUnknownTool
	}
	args := map[string]any{}
	if len(argsJSON) > 0 {
		if err := json.Unmarshal(argsJSON, &args); err != nil {
			return mcp.TextResult("参数必须是 JSON 对象", true), nil
		}
	}
	if spec.NeedsConfirm != nil && spec.NeedsConfirm(args) {
		return mcp.TextResult("该操作会影响生产主机：请先向用户复述影响范围并获得明确同意，再携带 confirm=true 重新调用", true), nil
	}
	text, err := p.execute(ctx, spec, args, argsJSON)
	if err != nil {
		return mcp.TextResult(err.Error(), true), nil
	}
	return mcp.TextResult(text, false), nil
}

// IsReadOnly 判断工具是否只读（未知工具返回 false）。
func (p *AIToolsProvider) IsReadOnly(name string) bool {
	s, ok := p.find(name)
	return ok && s.ReadOnly
}

// Has 判断当前身份是否可见该工具。
func (p *AIToolsProvider) Has(name string) bool {
	_, ok := p.find(name)
	return ok
}

// Execute 直接执行工具（内置助手用户确认后调用，跳过 confirm 检查）。
func (p *AIToolsProvider) Execute(ctx context.Context, name string, args map[string]any) (string, error) {
	spec, ok := p.find(name)
	if !ok {
		return "", fmt.Errorf("工具 %s 不存在或当前身份无权使用", name)
	}
	raw, _ := json.Marshal(args)
	return p.execute(ctx, spec, args, raw)
}

func (p *AIToolsProvider) find(name string) (toolSpec, bool) {
	for _, s := range p.tools() {
		if s.Name == name {
			return s, true
		}
	}
	return toolSpec{}, false
}

// execute 执行工具：调用 handler → 裁剪日志/输出 → 写操作失效缓存 → 审计。
func (p *AIToolsProvider) execute(ctx context.Context, spec toolSpec, args map[string]any, raw json.RawMessage) (string, error) {
	start := time.Now()
	result, err := spec.Call(ctx, args)
	p.audit(spec, raw, start, err)
	if err != nil {
		return "", err
	}
	if !spec.ReadOnly {
		// /mcp 不在缓存中间件的写操作自动失效路径（/api/）内，这里手动失效。
		if c := p.svc.GetCache(); c != nil && c.Enabled() {
			cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			c.DelPrefix(cctx, "http")
			cancel()
		}
	}
	result = truncateLogs(result, p.snapshot.AI.LogMaxBytes)
	data, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("序列化结果失败")
	}
	return string(truncateBytes(data, p.snapshot.AI.ToolOutputMaxBytes)), nil
}

func (p *AIToolsProvider) audit(spec toolSpec, raw json.RawMessage, start time.Time, err error) {
	if p.request == nil {
		return
	}
	details := map[string]any{
		"tool": spec.Name, "channel": p.channel, "read_only": spec.ReadOnly,
		"duration_ms": time.Since(start).Milliseconds(),
	}
	if p.token != nil {
		details["token_id"], details["token_name"] = p.token.ID, p.token.Name
	}
	if p.snapshot.AI.AuditArgs {
		details["args"] = string(truncateBytes(raw, p.snapshot.AI.AuditArgsMaxBytes))
	}
	r := p.request.WithContext(auth.WithUser(p.request.Context(), p.user))
	auditOpDetails(p.svc, r, audit.ActionAIMCPCall, "ai_tool", spec.Name, spec.Name, err, details, nil)
}

type toolSpec struct {
	Name         string
	Title        string
	Description  string
	Schema       map[string]any
	ReadOnly     bool
	Destructive  bool
	NeedsConfirm func(map[string]any) bool
	Call         func(context.Context, map[string]any) (any, error)
}

// writeAllowed 写工具是否对当前身份可见。
//
// MCP：全局 ai.mcp_allow_write + 令牌 allow_write 同时开启；
// 助手：ai.assistant_allow_write（实际执行仍需用户在页面确认）。
func (p *AIToolsProvider) writeAllowed() bool {
	switch p.channel {
	case channelMCP:
		return p.snapshot.AI.MCPAllowWrite && p.token != nil && p.token.AllowWrite
	case channelAssistant:
		return p.snapshot.AI.AssistantAllowWrite
	}
	return false
}

func (p *AIToolsProvider) tools() []toolSpec {
	admin := actorIsPrivileged(p.user) && p.user != nil
	confirm := func(args map[string]any) bool {
		return p.snapshot.AI.MCPRequireConfirm && !getBool(args, "confirm")
	}
	idPath := func(args map[string]any) map[string]string {
		return map[string]string{"id": strconv.Itoa(getInt(args, "id"))}
	}
	pageProps := []map[string]any{
		prop("page", "integer", "页码（默认 1）"),
		prop("size", "integer", fmt.Sprintf("每页条数（默认 %d）", p.snapshot.AI.ToolDefaultPageSize)),
	}

	specs := []toolSpec{
		{Name: "hosts_list", Title: "主机列表", ReadOnly: true,
			Description: "分页查询主机清单。q 匹配 IP/出口IP/隧道地址/主机名/业务分组；可按 app_code、os_family、account、agent_status(online/offline/unknown) 精确筛选。",
			Schema: objSchema(append([]map[string]any{
				prop("q", "string", "关键字"), prop("app_code", "string", "应用代码"),
				prop("os_family", "string", "操作系统"), prop("account", "string", "账号"),
				prop("agent_status", "string", "Agent 状态"),
			}, pageProps...)...),
			Call: p.get(ListHosts, nil, true)},
		{Name: "host_facets", Title: "主机筛选字段取值", ReadOnly: true,
			Description: "返回任务选主机可用的字段及去重取值（如 app_code、biz_group），用于构造 selector.filters。",
			Schema:      objSchema(prop("fields", "string", "逗号分隔字段名，留空使用系统配置")),
			Call:        p.get(HostFacets, nil, false)},
		{Name: "host_upgrade_history", Title: "主机升级历史", ReadOnly: true,
			Description: "按 IP 查询单台主机最近的升级记录。",
			Schema:      objSchema(reqProp("ip", "string", "主机 IP")),
			Call: p.get(HostUpgradeHistory, func(a map[string]any) map[string]string {
				return map[string]string{"ip": getString(a, "ip")}
			}, false)},
		{Name: "agent_stats", Title: "Agent 统计", ReadOnly: true,
			Description: "Agent 版本/系统/架构分布、离线数与需升级数。",
			Schema:      objSchema(),
			Call:        p.get(AgentStats, nil, false)},
		{Name: "components_list", Title: "组件列表", ReadOnly: true,
			Description: "列出全部组件（被升级的软件定义）。",
			Schema:      objSchema(),
			Call:        p.get(ComponentList, nil, false)},
		{Name: "component_get", Title: "组件详情", ReadOnly: true,
			Description: "查询组件详情：部署模式、服务名、安装路径、健康检查等。",
			Schema:      objSchema(reqProp("id", "integer", "组件 ID")),
			Call:        p.get(ComponentGet, idPath, false)},
		{Name: "artifacts_list", Title: "制品列表", ReadOnly: true,
			Description: "分页查询制品（组件的版本二进制），可按组件与版本关键字筛选。",
			Schema: objSchema(append([]map[string]any{
				prop("component_id", "integer", "组件 ID"), prop("q", "string", "版本关键字"),
			}, pageProps...)...),
			Call: p.get(ArtifactListPaged, nil, true)},
		{Name: "tasks_list", Title: "任务列表", ReadOnly: true,
			Description: "分页查询升级/命令任务，可按 kind(upgrade/command)、component_id、status、q、created_by 筛选。",
			Schema: objSchema(append([]map[string]any{
				prop("kind", "string", "upgrade/command"), prop("component_id", "integer", "组件 ID"),
				prop("status", "string", "draft/running/paused/success/failed/canceled/rolled_back"),
				prop("q", "string", "任务名关键字"), prop("created_by", "string", "创建者"),
			}, pageProps...)...),
			Call: p.get(TaskList, nil, true)},
		{Name: "task_get", Title: "任务详情", ReadOnly: true,
			Description: "查询任务详情：批次进度与逐机状态、阶段、退出码、日志（日志截取末尾）。",
			Schema: objSchema(reqProp("id", "integer", "任务 ID"),
				prop("host_page", "integer", "逐机分页页码"), prop("host_size", "integer", "逐机每页条数")),
			Call: p.get(TaskDetail, idPath, false)},
		{Name: "task_preview", Title: "任务预览", ReadOnly: true,
			Description: "预览命中主机数、示例 IP 与分批（不落库）。创建任务前应先调用。参数同 task_create。",
			Schema:      taskCreateSchema(false),
			Call:        p.post(TaskPreview, nil)},
		{Name: "my_permissions", Title: "我的权限", ReadOnly: true,
			Description: "当前身份可操作的组件（deploy 等权限）与主机分组范围。",
			Schema:      objSchema(),
			Call:        p.get(MyPermissions, nil, false)},
	}

	if admin {
		specs = append(specs,
			toolSpec{Name: "audit_logs_list", Title: "审计日志", ReadOnly: true,
				Description: "查询审计日志（管理员）。from/to 格式 YYYY-MM-DD。",
				Schema: objSchema(append([]map[string]any{
					prop("q", "string", "关键字"), prop("username", "string", "用户名"),
					prop("action", "string", "操作，如 task.start"), prop("status", "string", "success/failed"),
					prop("from", "string", "开始日期"), prop("to", "string", "结束日期"),
				}, pageProps...)...),
				Call: p.get(AuditLogList, nil, true)},
			toolSpec{Name: "agent_self_update_status", Title: "Agent 自升级状态", ReadOnly: true,
				Description: "Agent 目标版本、可用二进制与最近自升级记录（管理员）。",
				Schema:      objSchema(),
				Call:        p.get(AgentSelfUpdateStatus, nil, false)},
		)
	}

	if !p.writeAllowed() {
		return specs
	}
	specs = append(specs,
		toolSpec{Name: "task_create", Title: "创建任务", Destructive: true,
			Description: "创建灰度升级（kind=upgrade）或命令任务（kind=command）。任务一律为草稿（draft），不会执行；需再调用 task_start。",
			Schema:      taskCreateSchema(true),
			Call: func(ctx context.Context, args map[string]any) (any, error) {
				if getString(args, "kind") == "command" {
					if !p.snapshot.AI.AllowCommandTasks {
						return nil, fmt.Errorf("平台未允许经 AI 创建命令任务（ai.allow_command_tasks=false）")
					}
					if !admin {
						return nil, fmt.Errorf("命令任务仅管理员可创建")
					}
				}
				return p.post(TaskCreate, nil)(ctx, args)
			}},
		toolSpec{Name: "task_start", Title: "启动任务", Destructive: true,
			Description: "启动草稿任务或恢复已暂停任务，主机将开始执行。需携带 confirm=true。",
			Schema:      confirmSchema(), NeedsConfirm: confirm,
			Call: p.post(TaskStart, idPath)},
		toolSpec{Name: "task_pause", Title: "暂停任务",
			Description: "暂停运行中的任务（已下发的主机会继续完成）。",
			Schema:      objSchema(reqProp("id", "integer", "任务 ID")),
			Call:        p.post(TaskPause, idPath)},
		toolSpec{Name: "task_promote", Title: "推进下一批", Destructive: true,
			Description: "人工推进到下一批次。应先用 task_get 确认当前批次已全部成功。",
			Schema:      objSchema(reqProp("id", "integer", "任务 ID")),
			Call:        p.post(TaskPromote, idPath)},
		toolSpec{Name: "task_cancel", Title: "取消任务", Destructive: true,
			Description: "取消任务（不可恢复）。需携带 confirm=true。",
			Schema:      confirmSchema(), NeedsConfirm: confirm,
			Call: p.post(TaskCancel, idPath)},
		toolSpec{Name: "task_retry", Title: "重试失败主机", Destructive: true,
			Description: "重新执行失败/卡住的主机。task_host_ids 为空表示全部可重试主机；force=true 会强制重置执行中的主机（可能重复执行），此时必须 confirm=true。",
			Schema: objSchema(reqProp("id", "integer", "任务 ID"),
				arrProp("task_host_ids", "integer", "逐机记录 ID（task_get 返回的 hosts[].id）"),
				prop("force", "boolean", "强制重置执行中主机"), prop("confirm", "boolean", "已获用户确认")),
			NeedsConfirm: func(a map[string]any) bool { return getBool(a, "force") && confirm(a) },
			Call: p.postBody(TaskRetry, idPath, func(a map[string]any) map[string]any {
				return map[string]any{"task_host_ids": a["task_host_ids"], "force": getBool(a, "force")}
			})},
	)
	if admin {
		specs = append(specs, toolSpec{Name: "agent_self_update", Title: "Agent 自升级", Destructive: true,
			Description: "向指定主机或全部旧版本 Agent 下发自升级（Agent 会下载新二进制并重启）。需携带 confirm=true（管理员）。",
			Schema: objSchema(arrProp("host_ids", "integer", "主机 ID 列表"),
				prop("all_outdated", "boolean", "升级全部旧 Agent"), prop("force", "boolean", "版本相同也强制"),
				prop("confirm", "boolean", "已获用户确认")),
			NeedsConfirm: confirm,
			Call: p.postBody(AgentSelfUpdate, nil, func(a map[string]any) map[string]any {
				return map[string]any{"host_ids": a["host_ids"], "all_outdated": getBool(a, "all_outdated"),
					"force": getBool(a, "force")}
			})})
	}
	return specs
}

// get 包装 GET handler：参数转为查询串，paged=true 时补默认分页大小。
func (p *AIToolsProvider) get(fn func(*svc.ServiceContext) http.HandlerFunc,
	path func(map[string]any) map[string]string, paged bool) func(context.Context, map[string]any) (any, error) {
	return func(ctx context.Context, args map[string]any) (any, error) {
		q := url.Values{}
		for k, v := range args {
			if k == "confirm" || v == nil {
				continue
			}
			q.Set(k, scalarString(v))
		}
		if paged && q.Get("size") == "" {
			q.Set("size", strconv.Itoa(p.snapshot.AI.ToolDefaultPageSize))
		}
		req := p.newRequest(ctx, http.MethodGet, "/?"+q.Encode(), nil)
		if path != nil {
			for k, v := range path(args) {
				req.SetPathValue(k, v)
			}
		}
		rec := httptest.NewRecorder()
		fn(p.svc)(rec, req)
		return parseResponse(rec)
	}
}

// post 包装 POST handler：参数（去掉 confirm/id）作为 JSON body，path 从参数取。
func (p *AIToolsProvider) post(fn func(*svc.ServiceContext) http.HandlerFunc,
	path func(map[string]any) map[string]string) func(context.Context, map[string]any) (any, error) {
	return p.postBody(fn, path, func(args map[string]any) map[string]any {
		body := map[string]any{}
		for k, v := range args {
			if k != "confirm" && !(path != nil && k == "id") {
				body[k] = v
			}
		}
		return body
	})
}

// postBody 同 post，但由 bodyFn 显式构造请求体（只传 handler 认识的字段）。
func (p *AIToolsProvider) postBody(fn func(*svc.ServiceContext) http.HandlerFunc,
	path func(map[string]any) map[string]string,
	bodyFn func(map[string]any) map[string]any) func(context.Context, map[string]any) (any, error) {
	return func(ctx context.Context, args map[string]any) (any, error) {
		data, _ := json.Marshal(bodyFn(args))
		req := p.newRequest(ctx, http.MethodPost, "/", data)
		if path != nil {
			for k, v := range path(args) {
				req.SetPathValue(k, v)
			}
		}
		rec := httptest.NewRecorder()
		fn(p.svc)(rec, req)
		return parseResponse(rec)
	}
}

// newRequest 构造内部请求：携带当前用户与来源 IP（审计沿用真实客户端地址）。
func (p *AIToolsProvider) newRequest(ctx context.Context, method, target string, body []byte) *http.Request {
	req := httptest.NewRequest(method, target, bytes.NewReader(body))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if p.request != nil {
		req.RemoteAddr = p.request.RemoteAddr
		for _, h := range []string{"X-Forwarded-For", "X-Real-IP", "X-Forwarded-Proto"} {
			if v := p.request.Header.Get(h); v != "" {
				req.Header.Set(h, v)
			}
		}
		req.Host = p.request.Host
		req.TLS = p.request.TLS
	}
	return req.WithContext(auth.WithUser(ctx, p.user))
}

// parseResponse 解析统一响应 {code,message,data}；失败时返回服务端脱敏后的 message。
func parseResponse(rec *httptest.ResponseRecorder) (any, error) {
	var resp struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    any    `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		if rec.Code >= 400 {
			return nil, fmt.Errorf("请求失败（HTTP %d）", rec.Code)
		}
		return nil, fmt.Errorf("响应解析失败")
	}
	if rec.Code >= 400 || resp.Code != 0 {
		if resp.Message == "" {
			resp.Message = fmt.Sprintf("请求失败（HTTP %d）", rec.Code)
		}
		return nil, fmt.Errorf("%s", resp.Message)
	}
	return resp.Data, nil
}

// truncateLogs 递归截取结果中 "log" 字段的末尾 max 字节（逐机日志可能很长）。
func truncateLogs(v any, max int) any {
	if max <= 0 {
		return v
	}
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if s, ok := val.(string); ok && k == "log" && len(s) > max {
				t[k] = "…(前文已截断)\n" + validUTF8Tail(s, max)
				continue
			}
			t[k] = truncateLogs(val, max)
		}
	case []any:
		for i := range t {
			t[i] = truncateLogs(t[i], max)
		}
	}
	return v
}

func validUTF8Tail(s string, max int) string {
	tail := s[len(s)-max:]
	for i := 0; i < len(tail) && i < 4; i++ {
		if tail[i]&0xC0 != 0x80 {
			return tail[i:]
		}
	}
	return tail
}

func scalarString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	default:
		b, _ := json.Marshal(t)
		return string(b)
	}
}

func getString(m map[string]any, key string) string {
	switch v := m[key].(type) {
	case string:
		return v
	case nil:
		return ""
	default:
		return scalarString(v)
	}
}

func getInt(m map[string]any, key string) int {
	switch v := m[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		n, _ := strconv.Atoi(v)
		return n
	}
	return 0
}

func getBool(m map[string]any, key string) bool {
	switch v := m[key].(type) {
	case bool:
		return v
	case string:
		return v == "true" || v == "1"
	}
	return false
}

func objSchema(props ...map[string]any) map[string]any {
	ps, req := map[string]any{}, []string{}
	for _, pr := range props {
		name := pr["name"].(string)
		def := map[string]any{}
		for k, v := range pr {
			if k != "name" && k != "required" {
				def[k] = v
			}
		}
		ps[name] = def
		if pr["required"] == true {
			req = append(req, name)
		}
	}
	s := map[string]any{"type": "object", "properties": ps}
	if len(req) > 0 {
		s["required"] = req
	}
	return s
}

func prop(name, typ, desc string) map[string]any {
	return map[string]any{"name": name, "type": typ, "description": desc}
}

func reqProp(name, typ, desc string) map[string]any {
	m := prop(name, typ, desc)
	m["required"] = true
	return m
}

func arrProp(name, itemType, desc string) map[string]any {
	return map[string]any{"name": name, "type": "array", "items": map[string]any{"type": itemType}, "description": desc}
}

func objProp(name, desc string, inner map[string]any) map[string]any {
	m := map[string]any{"name": name, "description": desc}
	for k, v := range inner {
		m[k] = v
	}
	return m
}

func confirmSchema() map[string]any {
	return objSchema(reqProp("id", "integer", "任务 ID"), prop("confirm", "boolean", "已获用户明确同意时为 true"))
}

// taskCreateSchema 任务创建/预览参数（与 task.CreateRequest 的 JSON 结构一致）。
func taskCreateSchema(withConfirm bool) map[string]any {
	selector := objSchema(
		prop("ip_expr", "string", "IP 段表达式，如 10.1.0.0/24,10.1.2.5-10"),
		prop("exclude_ip_expr", "string", "排除的 IP 段"),
		map[string]any{"name": "filters", "type": "object", "description": "字段 → 取值列表，如 {\"app_code\":[\"PAY\"]}",
			"additionalProperties": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}},
	)
	strategy := objSchema(
		prop("mode", "string", "canary/count/percent"),
		prop("canary_count", "integer", "金丝雀首批主机数"),
		prop("batch_size", "integer", "每批主机数（percent 模式为百分比）"),
		prop("concurrency", "integer", "批内并发"),
		prop("auto_promote", "boolean", "批次成功后自动推进"),
		prop("health_wait", "string", "批间健康等待，如 5m"),
		prop("failure_threshold", "string", "失败率阈值，如 10%"),
		prop("auto_rollback", "boolean", "失败自动回滚"),
	)
	command := objSchema(
		prop("script", "string", "脚本内容（kind=command 必填）"),
		prop("interpreter", "string", "解释器，如 bash / powershell"),
		arrProp("args", "string", "脚本参数"),
		prop("timeout", "string", "超时，如 10m"),
	)
	props := []map[string]any{
		prop("kind", "string", "upgrade（默认）/ command"),
		reqProp("name", "string", "任务名称"),
		prop("component_id", "integer", "组件 ID（upgrade 必填）"),
		prop("version", "string", "目标版本（upgrade 必填）"),
		objProp("selector", "目标主机选择器", selector),
		objProp("strategy", "灰度策略", strategy),
		objProp("command", "命令任务定义", command),
	}
	if withConfirm {
		props = append(props, prop("confirm", "boolean", "已获用户明确同意时为 true"))
	}
	return objSchema(props...)
}

func truncateBytes(data []byte, max int) []byte {
	if max <= 0 || len(data) <= max {
		return data
	}
	const marker = "…(truncated)"
	cut := max - len(marker)
	if cut < 0 {
		cut = 0
	}
	for cut > 0 && data[cut]&0xC0 == 0x80 {
		cut--
	}
	out := append([]byte{}, data[:cut]...)
	return append(out, marker...)
}
