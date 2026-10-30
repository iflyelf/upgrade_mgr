package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/ai/llm"
	"github.com/iflyelf/upgrade_mgr/internal/auth"
	"github.com/iflyelf/upgrade_mgr/internal/config"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/audit"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/httpx"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

// chatMessage 前端回传的对话历史（只接受 user / assistant 纯文本）。
type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// AIChat AI 助手对话：POST /api/ai/chat。
//
// 请求：{"messages": [{"role": "user", "content": "..."}]}
// 响应：{"reply": "...", "pending_actions": [...], "steps": [...], "usage": {...}}
//
// 服务端不保存对话：历史由前端回传；写操作只生成待确认动作，由用户在页面确认后执行。
func AIChat(s *svc.ServiceContext) http.HandlerFunc {
	limiter := newUserRateLimiter()
	return func(w http.ResponseWriter, r *http.Request) {
		u, ok := auth.FromContext(r.Context())
		if !ok || u == nil {
			httpx.Fail(w, http.StatusUnauthorized, "未认证")
			return
		}
		snap := s.Settings.Snapshot()
		if !snap.AI.AssistantEnabled {
			httpx.Fail(w, http.StatusServiceUnavailable, "AI 助手未启用（设置 → AI 助手）")
			return
		}
		if !assistantConfigured(snap.AI) {
			httpx.Fail(w, http.StatusServiceUnavailable, "AI 助手未配置 LLM 地址或模型（设置 → AI 助手）")
			return
		}
		if snap.AI.AssistantRateLimit > 0 && !limiter.allow(u.Username, snap.AI.AssistantRateLimit) {
			httpx.Fail(w, http.StatusTooManyRequests, "请求过于频繁，请稍后再试")
			return
		}

		var req struct {
			Messages []chatMessage `json:"messages"`
		}
		if err := httpx.DecodeJSON(r, &req); err != nil {
			httpx.Fail(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		if len(req.Messages) == 0 || req.Messages[len(req.Messages)-1].Role != "user" {
			httpx.Fail(w, http.StatusBadRequest, "最后一条消息必须是用户消息")
			return
		}
		history := make([]llm.Message, 0, len(req.Messages))
		for _, m := range req.Messages {
			if m.Role != "user" && m.Role != "assistant" {
				httpx.Fail(w, http.StatusBadRequest, "消息只能包含 user 和 assistant 角色")
				return
			}
			if len([]rune(m.Content)) > snap.AI.AssistantMaxInputChars {
				httpx.Fail(w, http.StatusBadRequest, fmt.Sprintf("单条消息过长（最多 %d 字符）", snap.AI.AssistantMaxInputChars))
				return
			}
			history = append(history, llm.Message{Role: m.Role, Content: m.Content})
		}
		if len(history) > snap.AI.AssistantMaxHistory {
			history = history[len(history)-snap.AI.AssistantMaxHistory:]
		}

		result, err := runAssistant(r.Context(), s, u, r, history, snap)
		if err != nil {
			auditOp(s, r, audit.ActionAIChat, "ai_chat", "", "", err)
			httpx.FailErr(w, http.StatusBadGateway, err)
			return
		}
		auditOpDetails(s, r, audit.ActionAIChat, "ai_chat", "", "", nil,
			map[string]any{"steps": len(result.Steps), "pending": len(result.PendingActions),
				"prompt_tokens": result.Usage.PromptTokens, "completion_tokens": result.Usage.CompletionTokens}, nil)
		httpx.OK(w, result)
	}
}

// pendingAction 待确认操作的数据库行。
type pendingAction struct {
	ID         int64
	TenantID   int64
	Username   string
	Tool       string
	Args       []byte
	Summary    string
	ExpiresAt  time.Time
	ExecutedAt sql.NullTime
}

// loadOwnAction 读取待确认操作并校验归属（本人或 superadmin）、未执行、未过期。
func loadOwnAction(ctx context.Context, s *svc.ServiceContext, u *auth.User, id int64) (*pendingAction, int, string) {
	var a pendingAction
	err := s.DB.QueryRowContext(ctx, `
		SELECT id, tenant_id, username, tool, args, summary, expires_at, executed_at
		FROM ai_assistant_actions WHERE id=$1`, id).Scan(
		&a.ID, &a.TenantID, &a.Username, &a.Tool, &a.Args, &a.Summary, &a.ExpiresAt, &a.ExecutedAt)
	if err == sql.ErrNoRows {
		return nil, http.StatusNotFound, "操作不存在"
	}
	if err != nil {
		return nil, http.StatusInternalServerError, "读取操作失败"
	}
	if a.Username != u.Username && !u.IsSuperAdmin() {
		return nil, http.StatusNotFound, "操作不存在"
	}
	if a.ExecutedAt.Valid {
		return nil, http.StatusConflict, "该操作已执行或已取消"
	}
	if time.Now().After(a.ExpiresAt) {
		return nil, http.StatusGone, "操作已过期，请让助手重新生成"
	}
	return &a, 0, ""
}

// AIExecuteAction 执行待确认操作：POST /api/ai/actions/{id}/execute。
//
// 先原子占用（executed_at 置值）再执行，避免双击或多副本并发导致重复执行。
func AIExecuteAction(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, ok := auth.FromContext(r.Context())
		if !ok || u == nil {
			httpx.Fail(w, http.StatusUnauthorized, "未认证")
			return
		}
		snap := s.Settings.Snapshot()
		if !snap.AI.AssistantEnabled || !snap.AI.AssistantAllowWrite {
			httpx.Fail(w, http.StatusForbidden, "AI 助手写操作未开启")
			return
		}
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if id <= 0 {
			httpx.Fail(w, http.StatusBadRequest, "无效 ID")
			return
		}
		a, code, msg := loadOwnAction(r.Context(), s, u, id)
		if a == nil {
			httpx.Fail(w, code, msg)
			return
		}
		res, err := s.DB.ExecContext(r.Context(), `
			UPDATE ai_assistant_actions SET executed_at=NOW(), executed_by=$2
			WHERE id=$1 AND executed_at IS NULL AND expires_at > NOW()`, id, u.Username)
		if err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		if n, _ := res.RowsAffected(); n == 0 {
			httpx.Fail(w, http.StatusConflict, "该操作已执行或已过期")
			return
		}

		args := map[string]any{}
		_ = json.Unmarshal(a.Args, &args)
		provider := NewAIToolsProvider(s, u, nil, r, channelAssistant)
		result, execErr := provider.Execute(r.Context(), a.Tool, args)
		errText := ""
		if execErr != nil {
			errText = execErr.Error()
		}
		_, _ = s.DB.ExecContext(context.Background(),
			`UPDATE ai_assistant_actions SET result=$2, error=$3 WHERE id=$1`, id, result, errText)
		auditOpDetails(s, r, audit.ActionAIAction, "ai_action", strconv.FormatInt(id, 10), a.Tool, execErr,
			map[string]any{"tool": a.Tool, "summary": a.Summary}, nil)
		if execErr != nil {
			httpx.Fail(w, http.StatusBadRequest, execErr.Error())
			return
		}
		var parsed any
		if json.Unmarshal([]byte(result), &parsed) != nil {
			parsed = result
		}
		httpx.OK(w, map[string]any{"id": id, "tool": a.Tool, "result": parsed})
	}
}

// AICancelAction 放弃待确认操作：POST /api/ai/actions/{id}/cancel。
func AICancelAction(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, ok := auth.FromContext(r.Context())
		if !ok || u == nil {
			httpx.Fail(w, http.StatusUnauthorized, "未认证")
			return
		}
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if id <= 0 {
			httpx.Fail(w, http.StatusBadRequest, "无效 ID")
			return
		}
		a, code, msg := loadOwnAction(r.Context(), s, u, id)
		if a == nil {
			httpx.Fail(w, code, msg)
			return
		}
		if _, err := s.DB.ExecContext(r.Context(), `
			UPDATE ai_assistant_actions SET executed_at=NOW(), executed_by=$2, error='canceled'
			WHERE id=$1 AND executed_at IS NULL`, id, u.Username); err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		httpx.OK(w, map[string]any{"id": id, "canceled": true})
	}
}

// ChatResult 助手响应。
type ChatResult struct {
	Reply          string          `json:"reply"`
	PendingActions []PendingAction `json:"pending_actions"`
	Steps          []Step          `json:"steps"`
	Usage          llm.Usage       `json:"usage"`
	StepLimitHit   bool            `json:"step_limit_hit"`
}

// PendingAction 待确认操作（返回给前端渲染确认卡片）。
type PendingAction struct {
	ID        int64          `json:"id"`
	Tool      string         `json:"tool"`
	Title     string         `json:"title"`
	Args      map[string]any `json:"args"`
	Summary   string         `json:"summary"`
	ExpiresAt time.Time      `json:"expires_at"`
}

// Step 一次工具调用记录。
type Step struct {
	Tool    string `json:"tool"`
	Result  string `json:"result,omitempty"`
	Error   string `json:"error,omitempty"`
	Pending bool   `json:"pending,omitempty"`
}

// toolErrorContent 工具失败时回给模型的内容（json.Marshal 保证转义正确）。
func toolErrorContent(msg string) string {
	b, _ := json.Marshal(map[string]string{"error": msg})
	return string(b)
}

func runAssistant(ctx context.Context, s *svc.ServiceContext, u *auth.User, r *http.Request,
	history []llm.Message, snap config.Config) (*ChatResult, error) {
	// 时长项在保存/加载时已校验为合法正值，这里不再兜底默认值。
	timeout := parseDur(snap.AI.LLMTimeout)
	actionTTL := parseDur(snap.AI.AssistantActionTTL)

	client := llm.New(llm.Config{
		BaseURL: snap.AI.LLMBaseURL, APIKey: snap.AI.LLMAPIKey, Model: snap.AI.LLMModel,
		AuthHeader: snap.AI.LLMAuthHeader, AuthScheme: snap.AI.LLMAuthScheme,
		Temperature: snap.AI.LLMTemperature, MaxTokens: snap.AI.LLMMaxTokens,
		Timeout: timeout, MaxRespBytes: snap.AI.LLMMaxResponseBytes,
	})
	provider := NewAIToolsProvider(s, u, nil, r, channelAssistant)
	tools := buildLLMTools(provider)

	messages := append([]llm.Message{{Role: "system", Content: snap.AI.AssistantSystemPrompt}}, history...)
	out := &ChatResult{PendingActions: []PendingAction{}, Steps: []Step{}}
	titles := map[string]string{}
	for _, t := range provider.ListTools(ctx) {
		titles[t.Name] = t.Title
	}

	for step := 0; step < snap.AI.AssistantMaxSteps; step++ {
		resp, err := client.Chat(ctx, messages, tools)
		if err != nil {
			return nil, err
		}
		out.Usage.PromptTokens += resp.Usage.PromptTokens
		out.Usage.CompletionTokens += resp.Usage.CompletionTokens
		out.Usage.TotalTokens += resp.Usage.TotalTokens
		messages = append(messages, resp.Message)
		if len(resp.Message.ToolCalls) == 0 {
			out.Reply = resp.Message.Content
			return out, nil
		}
		for _, tc := range resp.Message.ToolCalls {
			name := tc.Function.Name
			args := map[string]any{}
			if tc.Function.Arguments != "" {
				if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
					out.Steps = append(out.Steps, Step{Tool: name, Error: "参数不是合法 JSON"})
					messages = append(messages, llm.Message{Role: "tool", ToolCallID: tc.ID, Content: toolErrorContent("参数不是合法 JSON 对象")})
					continue
				}
			}
			switch {
			case !provider.Has(name):
				out.Steps = append(out.Steps, Step{Tool: name, Error: "工具不存在或无权使用"})
				messages = append(messages, llm.Message{Role: "tool", ToolCallID: tc.ID, Content: toolErrorContent("工具不存在或当前身份无权使用")})
			case provider.IsReadOnly(name):
				result, err := provider.Execute(ctx, name, args)
				if err != nil {
					out.Steps = append(out.Steps, Step{Tool: name, Error: err.Error()})
					messages = append(messages, llm.Message{Role: "tool", ToolCallID: tc.ID, Content: toolErrorContent(err.Error())})
					continue
				}
				out.Steps = append(out.Steps, Step{Tool: name, Result: truncate(result, snap.AI.AuditArgsMaxBytes)})
				messages = append(messages, llm.Message{Role: "tool", ToolCallID: tc.ID, Content: result})
			default:
				delete(args, "confirm")
				pa, err := savePendingAction(ctx, s, u, name, titles[name], args, actionTTL)
				if err != nil {
					out.Steps = append(out.Steps, Step{Tool: name, Error: "创建待确认操作失败"})
					messages = append(messages, llm.Message{Role: "tool", ToolCallID: tc.ID, Content: toolErrorContent("创建待确认操作失败")})
					continue
				}
				out.PendingActions = append(out.PendingActions, *pa)
				out.Steps = append(out.Steps, Step{Tool: name, Pending: true})
				note, _ := json.Marshal(map[string]any{"pending": true, "action_id": pa.ID,
					"message": "该写操作已提交给用户在页面确认，不要假设已执行；请向用户说明影响范围并等待确认。"})
				messages = append(messages, llm.Message{Role: "tool", ToolCallID: tc.ID, Content: string(note)})
			}
		}
	}
	out.StepLimitHit = true
	out.Reply = fmt.Sprintf("已达到单轮最多 %d 步工具调用（ai.assistant_max_steps），请缩小问题范围后继续。", snap.AI.AssistantMaxSteps)
	return out, nil
}

// savePendingAction 暂存写操作，摘要由工具名与参数生成（不依赖模型自述）。
func savePendingAction(ctx context.Context, s *svc.ServiceContext, u *auth.User, tool, title string,
	args map[string]any, ttl time.Duration) (*PendingAction, error) {
	argsJSON, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	if title == "" {
		title = tool
	}
	summary := title + " " + string(argsJSON)
	pa := &PendingAction{Tool: tool, Title: title, Args: args, Summary: summary}
	tenant := u.TenantID
	if tenant <= 0 {
		tenant = 1
	}
	err = s.DB.QueryRowContext(ctx, `
		INSERT INTO ai_assistant_actions (tenant_id, username, tool, args, summary, expires_at)
		VALUES ($1,$2,$3,$4,$5,NOW() + $6 * INTERVAL '1 second') RETURNING id, expires_at`,
		tenant, u.Username, tool, argsJSON, summary, int64(ttl/time.Second)).Scan(&pa.ID, &pa.ExpiresAt)
	if err != nil {
		return nil, err
	}
	return pa, nil
}

func buildLLMTools(p *AIToolsProvider) []llm.Tool {
	specs := p.ListTools(context.Background())
	tools := make([]llm.Tool, len(specs))
	for i, s := range specs {
		tools[i] = llm.Tool{Type: "function"}
		tools[i].Function.Name = s.Name
		tools[i].Function.Description = s.Description
		tools[i].Function.Parameters = s.InputSchema
	}
	return tools
}

// truncate 按字节截断（保持 UTF-8 完整），max<=0 表示不截断。
func truncate(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut] + "…"
}

// userRateLimiter 每用户每分钟固定窗口限流。
type userRateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
}

func newUserRateLimiter() *userRateLimiter {
	return &userRateLimiter{buckets: make(map[string]*bucket)}
}

func (r *userRateLimiter) allow(username string, max int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	b, ok := r.buckets[username]
	if !ok || now.Sub(b.windowStart) >= time.Minute {
		r.buckets[username] = &bucket{count: 1, windowStart: now}
		r.cleanup(now)
		return true
	}
	if b.count >= max {
		return false
	}
	b.count++
	return true
}

func (r *userRateLimiter) cleanup(now time.Time) {
	for user, b := range r.buckets {
		if now.Sub(b.windowStart) > 2*time.Minute {
			delete(r.buckets, user)
		}
	}
}
