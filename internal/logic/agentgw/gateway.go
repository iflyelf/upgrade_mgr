// Package agentgw Agent 网关业务逻辑：原子取件、签名、状态流转、维护租约。
package agentgw

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/pkg/signer"
	"github.com/iflyelf/upgrade_mgr/internal/protocol"
)

// HashToken 对高熵随机 token 做 SHA-256 哈希（token 为 256bit 随机，无需慢哈希）。
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// VerifyToken 校验 agentID 的 Bearer token 是否匹配存储的哈希（时序安全）。
// 返回 nil 表示通过。
func (g *Gateway) VerifyToken(ctx context.Context, agentID, token string) error {
	if agentID == "" || token == "" {
		return fmt.Errorf("缺少 agent 凭证")
	}
	var stored string
	err := g.db.QueryRowContext(ctx, `SELECT token_hash FROM agents WHERE agent_id=$1`, agentID).Scan(&stored)
	if err == sql.ErrNoRows {
		return fmt.Errorf("agent 不存在")
	}
	if err != nil {
		return err
	}
	if subtle.ConstantTimeCompare([]byte(HashToken(token)), []byte(stored)) != 1 {
		return fmt.Errorf("agent 凭证无效")
	}
	return nil
}

// Gateway Agent 网关。
type Gateway struct {
	db       *sql.DB
	consulMu sync.RWMutex
	consul   ConsulWriter // 可选：升级成功回写 Consul Meta（需加锁保护）
}

// ConsulWriter Consul Meta 回写接口（避免循环依赖）。
type ConsulWriter interface {
	WriteServiceMeta(serviceID, key, value string) error
}

// New 创建网关。
func New(db *sql.DB) *Gateway { return &Gateway{db: db} }

// SetConsul 设置 Consul 客户端（可选，svc 初始化后注入）。
func (g *Gateway) SetConsul(c ConsulWriter) {
	g.consulMu.Lock()
	g.consul = c
	g.consulMu.Unlock()
}

// AgentSecret 返回指定 agent 的指令签名密钥。
func (g *Gateway) AgentSecret(ctx context.Context, agentID string) (string, error) {
	var secret string
	err := g.db.QueryRowContext(ctx, `SELECT secret FROM agents WHERE agent_id=$1`, agentID).Scan(&secret)
	return secret, err
}

// Dequeue 原子取出一条待下发指令（FOR UPDATE SKIP LOCKED，避免并发重复取件）。
//
// 返回签名后的指令；无指令时返回 (nil, nil)。
func (g *Gateway) Dequeue(ctx context.Context, agentID string) (*protocol.Command, error) {
	tx, err := g.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var (
		id      int64
		typ     string
		payload []byte
	)
	err = tx.QueryRowContext(ctx, `
		SELECT id, type, payload FROM commands
		WHERE agent_id=$1 AND status='pending' AND (expire_at IS NULL OR expire_at > NOW())
		ORDER BY id
		FOR UPDATE SKIP LOCKED
		LIMIT 1`, agentID).Scan(&id, &typ, &payload)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	// 标记 dispatched + 租约
	lease := time.Now().Add(30 * time.Minute)
	_, err = tx.ExecContext(ctx, `
		UPDATE commands SET status='dispatched', dispatched_at=NOW(), lease_expire_at=$2, attempts=attempts+1
		WHERE id=$1`, id, lease)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	// 获取密钥并签名
	secret, err := g.AgentSecret(ctx, agentID)
	if err != nil {
		return nil, err
	}
	var pl protocol.CommandPayload
	_ = json.Unmarshal(payload, &pl)

	cmd := &protocol.Command{
		ID:        fmt.Sprintf("%d", id),
		Type:      typ,
		Payload:   pl,
		Timestamp: time.Now().Unix(),
		Nonce:     randHex(12),
		// 截断到秒：签名用 Unix 秒，避免 JSON 往返的亚秒精度差异
		ExpireAt: lease.Truncate(time.Second),
	}
	// 注意：payload 列为 JSONB（键序被规范化），不能用 DB 原始字节签名。
	// 用 Go 规范化的 payload 字节签名，与 Agent 侧 json.Marshal(cmd.Payload) 一致。
	canonical, _ := json.Marshal(pl)
	env := signer.Envelope{
		Method: "CMD", Body: canonical,
		Timestamp: cmd.Timestamp, Nonce: cmd.Nonce,
	}
	// 双签名兼容：
	//   - signature   使用旧 Path（仅 ID），供尚未升级的旧 Agent 校验；
	//   - signature_v2 使用 SignedPath（ID|类型|过期时间），新 Agent 优先校验。
	// 这样服务端滚动升级不会强制所有主机重装 Agent。
	env.Path = cmd.ID
	cmd.Signature = signer.Sign(secret, env)
	env.Path = cmd.SignedPath()
	cmd.SignatureV2 = signer.Sign(secret, env)
	return cmd, nil
}

// Ack 记录指令已被接收。
//
// 安全：必须校验指令归属该 agent。commands.id 为 BIGSERIAL（连续可枚举），
// 若不校验 agent_id，任何已认证 Agent 都能猜 id 篡改他人指令状态
// （把别人的升级标记为 failed 造成拒绝服务，或标记 accepted 干扰编排）。
func (g *Gateway) Ack(ctx context.Context, agentID, cmdID, status string) error {
	dbStatus := protocol.CmdAccepted
	if status == "rejected" {
		dbStatus = protocol.CmdFailed
	}
	// 防止覆盖终态（租约回收可能已将 dispatched 置为 timeout）
	res, err := g.db.ExecContext(ctx,
		`UPDATE commands SET status=$3 
		 WHERE id=$1 AND agent_id=$2 AND status IN ('dispatched')`,
		cmdID, agentID, dbStatus)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("指令不存在或不属于当前 Agent")
	}
	return nil
}

// EnterMaintenance 申请维护租约：置主机 maintenance_until，避免升级期间被判掉线。
func (g *Gateway) EnterMaintenance(ctx context.Context, agentID string, expectedSeconds int, willRestart bool) error {
	if expectedSeconds <= 0 {
		expectedSeconds = 600
	}
	until := time.Now().Add(time.Duration(expectedSeconds+120) * time.Second)
	// 全局上限 1h
	if max := time.Now().Add(time.Hour); until.After(max) {
		until = max
	}
	_, err := g.db.ExecContext(ctx, `
		UPDATE hosts SET maintenance_until=$2, maintenance_reason='upgrading', agent_status='upgrading'
		WHERE agent_id=$1`, agentID, until)
	return err
}

// stageLabels 阶段中文名（仅用于展示日志，不影响协议值）。
var stageLabels = map[string]string{
	protocol.StageAccepted:    "已接收",
	protocol.StageMaintenance: "进入维护窗口",
	protocol.StageDownloading: "下载制品",
	protocol.StageDownloaded:  "下载完成",
	protocol.StagePreInstall:  "安装前检查",
	protocol.StageBackup:      "备份当前版本",
	protocol.StageStopping:    "停止服务",
	protocol.StageReplacing:   "替换文件",
	protocol.StageExec:        "执行安装",
	protocol.StageStarting:    "启动服务",
	protocol.StageVerifying:   "验证版本",
	protocol.StagePostInstall: "安装后处理",
	protocol.StageRollback:    "回滚",
	protocol.StageDone:        "完成",
}

func stageLabel(stage string) string {
	if v, ok := stageLabels[stage]; ok {
		return v
	}
	if stage == "" {
		return "未知阶段"
	}
	return stage
}

// UpdateStage 更新主机当前阶段，并把阶段追加到指令与逐机进度日志。
//
// 日志用于任务详情页实时展示「执行到哪一步」。只对仍处于非终态的指令追加，
// 避免心跳/重试在已完成指令上写入噪声。
func (g *Gateway) UpdateStage(ctx context.Context, agentID, cmdID, stage string) error {
	tx, err := g.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var taskHostID sql.NullInt64
	err = tx.QueryRowContext(ctx, `
		SELECT task_host_id FROM commands
		WHERE id=$1 AND agent_id=$2
		  AND status NOT IN ('success','failed','rolled_back','timeout','expired','canceled')`,
		cmdID, agentID).Scan(&taskHostID)
	if err == sql.ErrNoRows {
		// 指令已结束或不属于该 Agent：只更新主机阶段，不追加日志。
		if _, err := tx.ExecContext(ctx,
			`UPDATE hosts SET last_stage=$2 WHERE agent_id=$1`, agentID, stage); err != nil {
			return err
		}
		return tx.Commit()
	}
	if err != nil {
		return err
	}

	// 锁顺序与 Cancel/Report 一致：先 task_hosts，再 commands。
	// 避免取消事务与阶段上报因加锁顺序相反而互相等待。
	if taskHostID.Valid {
		if _, err := tx.ExecContext(ctx, `
			UPDATE task_hosts SET stage=$2
			WHERE id=$1 AND status NOT IN ('success','failed','rolled_back','timeout','canceled')`,
			taskHostID.Int64, stage); err != nil {
			return err
		}
	}

	line := fmt.Sprintf("[%s] 阶段: %s\n", time.Now().Format("2006-01-02 15:04:05"), stageLabel(stage))
	if _, err := tx.ExecContext(ctx, `
		UPDATE commands
		SET status=CASE WHEN status IN ('pending','dispatched','accepted') THEN 'running' ELSE status END,
		    started_at=COALESCE(started_at,NOW()), log=log||$3
		WHERE id=$1 AND agent_id=$2
		  AND status NOT IN ('success','failed','rolled_back','timeout','expired','canceled')`,
		cmdID, agentID, line); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE hosts SET last_stage=$2 WHERE agent_id=$1`, agentID, stage); err != nil {
		return err
	}
	return tx.Commit()
}

// Report 处理结果上报：更新 commands + task_hosts，成功则清除维护窗口。
func (g *Gateway) Report(ctx context.Context, agentID, cmdIDStr string, req protocol.ReportRequest) error {
	tx, err := g.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// 安全与正确性：按 URL 中的 cmdID + agentID 精确定位。
	//
	// 不能再用「该 agent 最近一条」匹配：
	//   1) 越权——Agent 可借此影响非本次上报的指令；
	//   2) 串号——同一 Agent 并发持有多条指令时，结果会写到错误的指令/主机上。
	var cmdID, taskHostID sql.NullInt64
	if cmdIDStr == "" {
		return fmt.Errorf("缺少指令 ID")
	}
	err = tx.QueryRowContext(ctx, `
		SELECT id, task_host_id FROM commands
		WHERE id=$1 AND agent_id=$2`, cmdIDStr, agentID).Scan(&cmdID, &taskHostID)
	if err == sql.ErrNoRows {
		return fmt.Errorf("指令不存在或不属于当前 Agent")
	}
	if err != nil {
		return err
	}

	final := isFinalStatus(req.Status)
	// 锁顺序与 Cancel/UpdateStage 一致：先 task_hosts，再 commands。
	if taskHostID.Valid && final {
		// 防止覆盖终态：租约回收可能已置为 timeout
		_, err = tx.ExecContext(ctx, `
			UPDATE task_hosts SET status=$2, stage=$3, to_version=$4, exit_code=$5, log=$6, finished_at=NOW()
			WHERE id=$1 AND status NOT IN ('success','failed','rolled_back','timeout','canceled')`,
			taskHostID.Int64, req.Status, req.Stage, req.ToVersion, req.ExitCode, req.Log)
		if err != nil {
			return err
		}
	}
	if cmdID.Valid {
		result, _ := json.Marshal(req)
		if final {
			// 防止覆盖终态：租约回收可能已置为 timeout/expired
			_, err = tx.ExecContext(ctx, `
				UPDATE commands SET status=$2, result=$3, log=log||$4, finished_at=NOW() 
				WHERE id=$1 AND status NOT IN ('success','failed','rolled_back','timeout','expired','canceled')`,
				cmdID.Int64, req.Status, result, req.Log)
		} else {
			_, err = tx.ExecContext(ctx, `
				UPDATE commands SET status='running', log=log||$2 
				WHERE id=$1 AND status NOT IN ('success','failed','rolled_back','timeout','expired','canceled')`,
				cmdID.Int64, req.Log)
		}
		if err != nil {
			return err
		}
	}

	// 最终态：清除维护窗口，恢复在线（IP 查询需在事务内完成）
	var hostIP string
	if final {
		_, _ = tx.ExecContext(ctx, `
			UPDATE hosts SET maintenance_until=NULL, maintenance_reason='', agent_status='online', last_stage=$2
			WHERE agent_id=$1`, agentID, req.Stage)

		// 事务内查询 IP（供后续 Consul 写入用）
		if req.Status == protocol.CmdSuccess {
			_ = tx.QueryRowContext(ctx, `SELECT ip FROM hosts WHERE agent_id=$1`, agentID).Scan(&hostIP)
		}
	}

	// 提交事务
	if err := tx.Commit(); err != nil {
		return err
	}

	// 成功 → 回写 Consul Meta（在事务外、读锁下执行，避免写操作在锁内阻塞）
	if final && req.Status == protocol.CmdSuccess && hostIP != "" && req.ToVersion != "" {
		g.consulMu.RLock()
		consulClient := g.consul
		g.consulMu.RUnlock()

		if consulClient != nil {
			// key 以 ip 为标识，value 为升级后版本；具体 serviceID 由 ConsulWriter 内部解析
			_ = consulClient.WriteServiceMeta(hostIP, "upgraded_version", req.ToVersion)
		}
	}

	return nil
}

// ConsumeEnrollToken 消费一次性注册令牌，返回其所属租户 ID。
//
// 校验：token 有效、未使用、未过期、IP 匹配（若限定）。
// 成功后标记已使用，返回 (tenantID, true)；失败返回 (0, false)。
func (g *Gateway) ConsumeEnrollToken(ctx context.Context, token, clientIP string) (int64, bool) {
	var tenantID int64
	var allowedIP sql.NullString
	err := g.db.QueryRowContext(ctx, `
		SELECT tenant_id, allowed_ip FROM enroll_tokens 
		WHERE token=$1 AND used=false AND (expire_at IS NULL OR expire_at > NOW())`,
		token).Scan(&tenantID, &allowedIP)
	if err != nil {
		return 0, false
	}
	// IP 限定检查
	if allowedIP.Valid && allowedIP.String != clientIP {
		return 0, false
	}
	// 原子标记已使用
	_, err = g.db.ExecContext(ctx, `UPDATE enroll_tokens SET used=true, used_at=NOW() WHERE token=$1`, token)
	if err != nil {
		return 0, false
	}
	return tenantID, true
}

func isFinalStatus(s string) bool {
	switch s {
	case protocol.CmdSuccess, protocol.CmdFailed, protocol.CmdRolledBack, protocol.CmdTimeout:
		return true
	}
	return false
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
