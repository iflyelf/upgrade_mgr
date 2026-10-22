package handler

import (
	"net/http"

	"github.com/iflyelf/upgrade_mgr/internal/auth"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/audit"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

// auditOp 记录一次敏感操作的审计事件。
//
// 用户身份取自认证中间件注入的 context；IP 取自 X-Forwarded-For 或 RemoteAddr。
// 写入为异步，不影响请求耗时；审计失败只告警，不影响业务返回。
func auditOp(s *svc.ServiceContext, r *http.Request, action, resType, resID, resName string, err error) {
	auditOpDetails(s, r, action, resType, resID, resName, err, nil, nil)
}

// auditOpDetails 同 auditOp，额外记录成功与失败细节（如连带删除的任务数）。
func auditOpDetails(s *svc.ServiceContext, r *http.Request, action, resType, resID, resName string, err error,
	details, extra map[string]any) {
	if s.Audit == nil {
		return
	}
	var userID, username string
	if u, ok := auth.FromContext(r.Context()); ok && u != nil {
		userID, username = u.ID, u.Username
	}
	ev := audit.Event{
		Action:       action,
		UserID:       userID,
		Username:     username,
		IP:           clientIP(s, r),
		ResourceType: resType,
		ResourceID:   resID,
		ResourceName: resName,
		Success:      err == nil,
	}
	if err != nil {
		// 审计留存原始错误（服务端内部），与返回给客户端的脱敏消息相互独立。
		ev.Message = err.Error()
	}
	merged := map[string]any{}
	for k, v := range extra {
		merged[k] = v
	}
	for k, v := range details {
		merged[k] = v
	}
	if len(merged) > 0 {
		ev.Details = merged
	}
	s.Audit.Log(r.Context(), ev)
}

// auditAnon 记录认证前/无登录态的敏感事件（登录、Agent 注册等）。
//
// 这类事件无法从 context 取用户，故由调用方显式传入标识（用户名或 agent id）。
// 登录失败必须留痕：这是识别暴力破解的主要依据。
func auditAnon(s *svc.ServiceContext, r *http.Request, action, actor, resType, resID string, err error) {
	if s.Audit == nil {
		return
	}
	ev := audit.Event{
		Action:       action,
		Username:     actor,
		IP:           clientIP(s, r),
		ResourceType: resType,
		ResourceID:   resID,
		Success:      err == nil,
	}
	if err != nil {
		ev.Message = err.Error()
	}
	s.Audit.Log(r.Context(), ev)
}
