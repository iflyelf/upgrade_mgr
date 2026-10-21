// Package audit 审计日志：记录敏感操作，满足合规要求。
package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"time"
)

// Event 审计事件。字段与 audit_logs 表（迁移 001）严格对齐。
type Event struct {
	Action       string                 // 操作类型（如 component.create）
	UserID       string                 // 操作用户 ID
	Username     string                 // 操作用户名
	IP           string                 // 客户端 IP → ip_address
	ResourceType string                 // 资源类型（component/task/artifact）
	ResourceID   string                 // 资源 ID
	ResourceName string                 // 资源名称
	Details      map[string]interface{} // 详细信息（JSONB）
	Success      bool                   // 是否成功 → status
	Message      string                 // 错误信息 → error_message
	Timestamp    time.Time
}

// Logger 审计日志记录器。
type Logger struct {
	db *sql.DB
}

// NewLogger 创建审计日志记录器。
func NewLogger(db *sql.DB) *Logger {
	return &Logger{db: db}
}

// Log 记录审计事件（异步，失败不阻塞业务）。
func (l *Logger) Log(ctx context.Context, event Event) {
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}
	
	status := "success"
	if !event.Success {
		status = "failed"
	}
	detailsJSON, _ := json.Marshal(event.Details)

	// 异步写入：审计不阻塞业务。用独立 context，避免请求结束导致写入被取消。
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := l.db.ExecContext(ctx, `
			INSERT INTO audit_logs
				(action, user_id, username, resource_type, resource_id, resource_name,
				 details, ip_address, status, error_message, created_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
			event.Action, event.UserID, event.Username,
			event.ResourceType, event.ResourceID, event.ResourceName,
			detailsJSON, event.IP, status, event.Message, event.Timestamp)
		if err != nil {
			log.Printf("⚠️ 审计日志写入失败: %v", err)
		}
	}()
}

// LogSuccess 记录成功操作。resourceType 如 component/task/artifact；resourceID 为其主键。
func (l *Logger) LogSuccess(ctx context.Context, action, username, ip, resourceType, resourceID string, details map[string]interface{}) {
	l.Log(ctx, Event{
		Action:       action,
		UserID:       username,
		Username:     username,
		IP:           ip,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		Details:      details,
		Success:      true,
	})
}

// LogFailure 记录失败操作。
func (l *Logger) LogFailure(ctx context.Context, action, username, ip, resourceType, resourceID, message string, details map[string]interface{}) {
	l.Log(ctx, Event{
		Action:       action,
		UserID:       username,
		Username:     username,
		IP:           ip,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		Details:      details,
		Success:      false,
		Message:      message,
	})
}

// 敏感操作常量
const (
	ActionComponentCreate = "component.create"
	ActionComponentUpdate = "component.update"
	ActionComponentDelete = "component.delete"
	ActionTaskStart       = "task.start"
	ActionTaskPause       = "task.pause"
	ActionTaskPromote     = "task.promote"
	ActionTaskCancel      = "task.cancel"
	ActionArtifactUpload  = "artifact.upload"
	ActionAgentRegister   = "agent.register"
	ActionEnrollToken     = "enroll_token.create"
	ActionSettingUpdate   = "setting.update"
	ActionUserCreate      = "user.create"
	ActionUserUpdate      = "user.update"
	ActionTenantCreate    = "tenant.create"
	ActionLogin           = "auth.login"
	ActionLogout          = "auth.logout"
	ActionPasswordChange  = "auth.password_change"
)
