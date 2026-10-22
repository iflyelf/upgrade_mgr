package handler

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/logic/agentupdate"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

// normalizeAgentBuild 校验 Agent 上报的构建信息。
//
// BuildHash 只接受 64 位十六进制；非法值一律丢弃，避免把任意文本写入数据库。
// 旧 Agent 不上报，返回空字符串，注册/心跳逻辑会保留数据库原值。
func normalizeAgentBuild(version, hash, variant string) (string, string, string) {
	version = strings.TrimSpace(version)
	if len(version) > 128 {
		version = version[:128]
	}
	hash = strings.ToLower(strings.TrimSpace(hash))
	if len(hash) != 64 || strings.Trim(hash, "0123456789abcdef") != "" {
		hash = ""
	}
	variant = strings.ToLower(strings.TrimSpace(variant))
	if variant != "legacy" {
		variant = ""
	}
	return version, hash, variant
}

// agentUpdateResolver 构造 Agent 二进制解析器（SHA256 有全局缓存）。
func agentUpdateResolver(s *svc.ServiceContext) *agentupdate.Resolver {
	dir := "./dist"
	if s != nil && s.Config != nil && strings.TrimSpace(s.Config.Server.AgentBinaryDir) != "" {
		dir = s.Config.Server.AgentBinaryDir
	}
	version := ""
	if s != nil {
		version = s.Version
	}
	return agentupdate.NewResolver(dir, version)
}

// markSelfUpdateSuccess 由新 Agent 的心跳确认自升级成功。
//
// Agent 替换二进制后 exit(75)，无法在退出前上报 success；新进程启动后会在心跳
// 中携带自身 SHA256。服务端用目标指令里的 SHA256 精确匹配，避免把其他重启误判为
// 自升级成功。
func markSelfUpdateSuccess(ctx context.Context, db *sql.DB, agentID, buildHash string) int64 {
	if buildHash == "" {
		return 0
	}
	line := fmt.Sprintf("[%s] Agent 已以新版本上线，自升级完成\n", time.Now().Format("2006-01-02 15:04:05"))
	res, err := db.ExecContext(ctx, `
		UPDATE commands
		SET status='success', finished_at=NOW(),
		    result=jsonb_build_object('status','success','stage','done',
		        'to_version', COALESCE(payload->>'to_version','')),
		    log=COALESCE(log,'')||$3
		WHERE agent_id=$1 AND type='self_update'
		  AND status IN ('dispatched','accepted','running')
		  AND payload->'artifact'->>'sha256'=$2`,
		agentID, buildHash, line)
	if err != nil {
		log.Printf("⚠️ 更新 Agent 自升级指令状态失败: %v", err)
		return 0
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		_, _ = db.ExecContext(ctx, `
			UPDATE hosts SET maintenance_until=NULL, maintenance_reason='',
			       agent_status='online', last_stage='done'
			WHERE agent_id=$1`, agentID)
	}
	return n
}

// autoUpdateErrLog 限制同一 Agent 的自升级错误日志频率。
// 目标二进制缺失时每次心跳都会检查失败；若不限频，上万台主机可能刷爆日志。
var autoUpdateErrLog sync.Map // agentID -> time.Time

func logAutoUpdateErr(agentID string, err error) {
	now := time.Now()
	if v, ok := autoUpdateErrLog.Load(agentID); ok {
		if last, ok := v.(time.Time); ok && now.Sub(last) < 10*time.Minute {
			return
		}
	}
	autoUpdateErrLog.Store(agentID, now)
	log.Printf("⚠️ 检查 Agent 自升级失败 agent=%s: %v", agentID, err)
}

// maybeAutoUpdateAgent 在开启 agent.auto_update 时，发现旧 Agent 就下发自升级。
//
// 只在心跳/注册时触发，天然按心跳时间错峰；AutoEnqueue 内部还有 30 分钟冷却。
func maybeAutoUpdateAgent(s *svc.ServiceContext, r *http.Request, agentID string) {
	if s == nil || s.Settings == nil || s.DB == nil || agentID == "" {
		return
	}
	snap := s.Settings.Snapshot()
	if !snap.Agent.AutoUpdate {
		return
	}
	baseURL := installBaseURL(snap.Server.ExternalURL, r, snap.Server.TrustProxy)
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	ok, err := agentupdate.AutoEnqueue(ctx, s.DB, agentID, agentUpdateResolver(s), baseURL)
	if err != nil {
		logAutoUpdateErr(agentID, err)
		return
	}
	autoUpdateErrLog.Delete(agentID)
	if ok {
		log.Printf("⬆️ 已向旧 Agent 下发自升级指令 agent=%s", agentID)
	}
}
