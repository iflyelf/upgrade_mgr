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
	"strings"
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

// AgentSigning 返回签名密钥与旧版 signature 应使用的签名路径。
type AgentSigning struct {
	Secret              string
	LegacySignatureMode string
}

// AgentSigningInfo 查询 Agent 的签名能力，决定 legacy signature 使用 ID 还是 SignedPath。
// 未上报能力（旧 Agent / 历史数据）默认 id，保持向后兼容。
func (g *Gateway) AgentSigningInfo(ctx context.Context, agentID string) (AgentSigning, error) {
	var info AgentSigning
	var storedModes string
	err := g.db.QueryRowContext(ctx,
		`SELECT secret, COALESCE(signature_modes,'') FROM agents WHERE agent_id=$1`,
		agentID).Scan(&info.Secret, &storedModes)
	if err != nil {
		return info, err
	}
	info.LegacySignatureMode = protocol.PreferredLegacySignatureMode(strings.Split(storedModes, ","))
	return info, nil
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

	// 获取密钥与 Agent 上报的签名能力
	signing, err := g.AgentSigningInfo(ctx, agentID)
	if err != nil {
		return nil, err
	}
	secret := signing.Secret
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
	//   - signature    按 Agent 上报的能力选择 Path：旧 Agent 用 id，
	//                  支持 signed_path 的 Agent 用 SignedPath；
	//   - signature_v2 始终使用 SignedPath，新 Agent 优先校验。
	// 能力标识让服务端不再依赖 Agent 版本推断，也无需强制重装。
	if signing.LegacySignatureMode == protocol.SignatureModeSignedPath {
		env.Path = cmd.SignedPath()
	} else {
		env.Path = cmd.ID
	}
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
//
// 兼容：旧 Agent 没有上报签名能力，数据库只能默认 id。SignedPath-only 旧 Agent
// 首次收到 id 签名会拒绝。此时若拒绝原因是“签名不匹配”，服务端自动把该 Agent
// 的 legacy 签名路径切换到 SignedPath 并重排一次指令；反之亦然。这样旧 Agent
// 也能收到 self_update 指令完成自升级，无需人工改 agents.signature_modes。
func (g *Gateway) Ack(ctx context.Context, agentID, cmdID, status, reason string) error {
	tx, err := g.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var (
		attempts int
		dbStatus string
	)
	err = tx.QueryRowContext(ctx, `
		SELECT attempts, status FROM commands
		WHERE id=$1 AND agent_id=$2 FOR UPDATE`, cmdID, agentID).Scan(&attempts, &dbStatus)
	if err == sql.ErrNoRows {
		return fmt.Errorf("指令不存在或不属于当前 Agent")
	}
	if err != nil {
		return err
	}
	// 防止覆盖终态（租约回收可能已将 dispatched 置为 timeout）
	if dbStatus != "dispatched" {
		return fmt.Errorf("指令不存在或不属于当前 Agent")
	}

	if status != "rejected" {
		if _, err := tx.ExecContext(ctx, `UPDATE commands SET status=$2 WHERE id=$1`, cmdID, protocol.CmdAccepted); err != nil {
			return err
		}
		return tx.Commit()
	}

	// 仅探测一次：第一次拒绝若确为签名路径不匹配，则切换 legacy 签名路径并重排。
	if attempts <= 1 && strings.Contains(reason, "签名不匹配") {
		var modes string
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(signature_modes,'') FROM agents WHERE agent_id=$1 FOR UPDATE`, agentID).Scan(&modes); err == nil {
			normalized := protocol.NormalizeSignatureModes(strings.Split(modes, ","))
			// 只切换“明确只有一种 legacy 能力”的旧 Agent；同时支持 id+signed_path 的
			// 新 Agent 不应因单次 v2 校验失败被降级。
			if len(normalized) == 1 &&
				(normalized[0] == protocol.SignatureModeID || normalized[0] == protocol.SignatureModeSignedPath) {
				next := protocol.SignatureModeSignedPath
				if normalized[0] == protocol.SignatureModeSignedPath {
					next = protocol.SignatureModeID
				}
				if _, err := tx.ExecContext(ctx, `UPDATE agents SET signature_modes=$2 WHERE agent_id=$1`, agentID, next); err != nil {
					return err
				}
				line := fmt.Sprintf("[%s] 检测到旧 Agent 签名路径不匹配，自动切换 legacy 签名为 %s 并重试\n",
					time.Now().Format("2006-01-02 15:04:05"), next)
				_, err := tx.ExecContext(ctx, `
					UPDATE commands SET status='pending', dispatched_at=NULL, lease_expire_at=NULL,
					       log=COALESCE(log,'')||$2
					WHERE id=$1`, cmdID, line)
				if err != nil {
					return err
				}
				return tx.Commit()
			}
		}
	}

	line := fmt.Sprintf("[%s] Agent 拒绝指令：%s\n", time.Now().Format("2006-01-02 15:04:05"), truncateReason(reason))
	if _, err := tx.ExecContext(ctx,
		`UPDATE commands SET status=$2, finished_at=NOW(), log=COALESCE(log,'')||$3 WHERE id=$1`,
		cmdID, protocol.CmdFailed, line); err != nil {
		return err
	}
	return tx.Commit()
}

func truncateReason(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	s = sanitizeAndTruncate(s, maxReasonBytes)
	if s == "" {
		return "未提供原因"
	}
	return s
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
	protocol.StageFailed:      "失败",
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

// UpdateStage 更新主机当前阶段，并把阶段与详情追加到指令与逐机进度日志。
//
// 日志用于任务详情页实时展示「执行到哪一步」。只对仍处于非终态的指令追加，
// 避免心跳/重试在已完成指令上写入噪声。每次阶段上报同时续租，
// 防止长时间下载/安装被租约回收误判为超时。
func (g *Gateway) UpdateStage(ctx context.Context, agentID, cmdID, stage, message string) error {
	tx, err := g.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stage = normalizeStage(stage)
	message = sanitizeAndTruncate(message, maxStageMessageBytes)

	var taskHostID sql.NullInt64
	var cmdStatus string
	err = tx.QueryRowContext(ctx, `
		SELECT task_host_id, status FROM commands
		WHERE id=$1 AND agent_id=$2`, cmdID, agentID).Scan(&taskHostID, &cmdStatus)
	if err == sql.ErrNoRows {
		return tx.Commit()
	}
	if err != nil {
		return err
	}
	// 与 Retry/Cancel 使用相同的加锁顺序：先 task_hosts，再 commands。
	// 加锁后重读一次指令状态，避免并发“取消旧指令并重置 pending”时，
	// 本请求仍拿着旧状态把过期进度写进新一轮执行。
	var lockedID int64
	if taskHostID.Valid {
		err = tx.QueryRowContext(ctx,
			`SELECT id FROM task_hosts WHERE id=$1 FOR UPDATE`, taskHostID.Int64).Scan(&lockedID)
	} else {
		err = tx.QueryRowContext(ctx,
			`SELECT id FROM commands WHERE id=$1 FOR UPDATE`, cmdID).Scan(&lockedID)
	}
	if err == sql.ErrNoRows {
		return tx.Commit()
	}
	if err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `
		SELECT status FROM commands WHERE id=$1 AND agent_id=$2`,
		cmdID, agentID).Scan(&cmdStatus); err != nil {
		return err
	}
	// 被“重新执行”取消或已过期的旧指令：旧 Agent 的迟到阶段不能覆盖新一轮进度。
	if cmdStatus == protocol.CmdCanceled || cmdStatus == protocol.CmdExpired {
		return tx.Commit()
	}
	if isFinalStatus(cmdStatus) {
		// 已终态指令只更新主机阶段，不追加日志（保持原有展示语义）。
		if _, err := tx.ExecContext(ctx,
			`UPDATE hosts SET last_stage=$2 WHERE agent_id=$1`, agentID, stage); err != nil {
			return err
		}
		return tx.Commit()
	}

	line := fmt.Sprintf("[%s] 阶段: %s", time.Now().Format("2006-01-02 15:04:05"), stageLabel(stage))
	if message != "" {
		line += " - " + message
	}
	line += "\n"

	// 锁顺序与 Cancel/Report 一致：先 task_hosts，再 commands。
	// 避免取消事务与阶段上报因加锁顺序相反而互相等待。
	if taskHostID.Valid {
		if _, err := tx.ExecContext(ctx, `
			UPDATE task_hosts SET stage=$2, log=COALESCE(log,'')||$3
			WHERE id=$1 AND status NOT IN ('success','failed','rolled_back','timeout','canceled')`,
			taskHostID.Int64, stage, line); err != nil {
			return err
		}
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE commands
		SET status=CASE WHEN status IN ('pending','dispatched','accepted') THEN 'running' ELSE status END,
		    started_at=COALESCE(started_at,NOW()),
		    last_stage=$3, last_stage_at=NOW(),
		    lease_expire_at=CASE WHEN lease_expire_at IS NULL THEN NULL
		                         ELSE GREATEST(lease_expire_at, NOW() + INTERVAL '30 minutes') END,
		    log=COALESCE(log,'')||$4
		WHERE id=$1 AND agent_id=$2
		  AND status NOT IN ('success','failed','rolled_back','timeout','expired','canceled')`,
		cmdID, agentID, stage, line); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE hosts SET last_stage=$2 WHERE agent_id=$1`, agentID, stage); err != nil {
		return err
	}

	// 兜底闭环：新 Agent 的成功/失败终态阶段本身已带明确语义
	// （成功：executor 在版本校验通过后才发 done + “升级成功”；
	//  失败：错误路径发 failed），因此在阶段到达时即可落终态。
	// 最终 report 仍会重试并携带完整日志；这里先保证任务不会永久停在执行中。
	var finalStatus string
	switch {
	case stage == protocol.StageDone && strings.Contains(message, "升级成功"):
		finalStatus = protocol.CmdSuccess
	case stage == protocol.StageFailed:
		finalStatus = protocol.CmdFailed
	}
	if finalStatus != "" {
		note := fmt.Sprintf("[%s] 服务端根据终态阶段自动标记为 %s（最终结果上报会自动覆盖/补充）\n",
			time.Now().Format("2006-01-02 15:04:05"), finalStatus)
		toVersion := cmdPayloadVersion(ctx, tx, cmdID)
		if finalStatus == protocol.CmdSuccess {
			if v := versionFromSuccessMessage(message); v != "" {
				toVersion = v
			}
		}
		if taskHostID.Valid {
			if _, err := tx.ExecContext(ctx, `
				UPDATE task_hosts
				SET status=$2, stage=$3, to_version=CASE WHEN $4<>'' THEN $4 ELSE to_version END,
				    finished_at=NOW(), log=COALESCE(log,'')||$5
				WHERE id=$1 AND status NOT IN ('success','failed','rolled_back','timeout','canceled')`,
				taskHostID.Int64, finalStatus, stage, toVersion, note); err != nil {
				return err
			}
		}
		result, _ := json.Marshal(map[string]any{
			"status": finalStatus, "stage": stage, "to_version": toVersion,
			"auto_finalized": true,
		})
		if _, err := tx.ExecContext(ctx, `
			UPDATE commands
			SET status=$2, last_stage=$3, last_stage_at=NOW(), finished_at=NOW(),
			    result=COALESCE(result, $4), log=COALESCE(log,'')||$5
			WHERE id=$1 AND agent_id=$6
			  AND status NOT IN ('success','failed','rolled_back','timeout','expired','canceled')`,
			cmdID, finalStatus, stage, result, note, agentID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE hosts SET maintenance_until=NULL, maintenance_reason='',
			       agent_status='online', last_stage=$2
			WHERE agent_id=$1`, agentID, stage); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// cmdPayloadVersion 读取指令下发时的目标版本（用于自动终态时补 to_version）。
func cmdPayloadVersion(ctx context.Context, tx *sql.Tx, cmdID string) string {
	var v string
	_ = tx.QueryRowContext(ctx,
		`SELECT COALESCE(payload->>'to_version','') FROM commands WHERE id=$1`, cmdID).Scan(&v)
	return normalizeVersion(v)
}

// versionFromSuccessMessage 从「升级成功，当前版本: X」中提取版本号。
func versionFromSuccessMessage(message string) string {
	const marker = "当前版本:"
	idx := strings.LastIndex(message, marker)
	if idx < 0 {
		return ""
	}
	v := strings.TrimSpace(message[idx+len(marker):])
	if v == "" || strings.ContainsAny(v, "\r\n") {
		return ""
	}
	return normalizeVersion(v)
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
	var cmdStatus string
	if cmdIDStr == "" {
		return fmt.Errorf("缺少指令 ID")
	}
	err = tx.QueryRowContext(ctx, `
		SELECT id, task_host_id, status FROM commands
		WHERE id=$1 AND agent_id=$2`, cmdIDStr, agentID).Scan(&cmdID, &taskHostID, &cmdStatus)
	if err == sql.ErrNoRows {
		return fmt.Errorf("指令不存在或不属于当前 Agent")
	}
	if err != nil {
		return err
	}
	// 与 Retry/Cancel 使用相同的加锁顺序：先 task_hosts，再 commands。
	// 加锁后重读指令状态，避免并发“取消旧指令并重置 pending”时，
	// 本请求仍用旧状态把结果写回刚重置的主机。
	var lockedID int64
	if taskHostID.Valid {
		err = tx.QueryRowContext(ctx,
			`SELECT id FROM task_hosts WHERE id=$1 FOR UPDATE`, taskHostID.Int64).Scan(&lockedID)
	} else {
		err = tx.QueryRowContext(ctx,
			`SELECT id FROM commands WHERE id=$1 FOR UPDATE`, cmdID).Scan(&lockedID)
	}
	if err == sql.ErrNoRows {
		return tx.Commit()
	}
	if err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `
		SELECT status FROM commands WHERE id=$1 AND agent_id=$2`,
		cmdID, agentID).Scan(&cmdStatus); err != nil {
		return err
	}
	// 被“重新执行”取消/回收的旧指令：迟到上报必须完全忽略。
	// 否则旧 Agent 的结果会把刚重置为 pending 的 task_host 改回 success/failed，
	// 或污染新一次执行的进度。
	if cmdStatus == protocol.CmdExpired || cmdStatus == protocol.CmdCanceled {
		return tx.Commit()
	}
	// 已终态的重复上报：命令状态由 SQL 条件保护，但不能再改 task_host，
	// 尤其是成功主机被强制重试后，旧上报不能把它重新标成成功/失败。
	cmdActive := !isFinalStatus(cmdStatus)
	// 指令因 UpdateStage 自动终态时，最终 report 仍可补写 Consul 版本；
	// 但若该主机已有更新的活动指令，或该指令早于本次重试基线，
	// 说明正在进行新一轮执行，旧结果必须忽略。
	if finalReport := isFinalStatus(req.Status); finalReport && !cmdActive {
		if taskHostID.Valid {
			var stale bool
			if err := tx.QueryRowContext(ctx, `
				SELECT th.started_at IS NOT NULL
				       AND COALESCE(c.finished_at, c.created_at) < th.started_at
				FROM task_hosts th JOIN commands c ON c.id=$2
				WHERE th.id=$1`, taskHostID.Int64, cmdID.Int64).Scan(&stale); err == nil && stale {
				return tx.Commit()
			}
		}
		var newerActive int
		if taskHostID.Valid {
			_ = tx.QueryRowContext(ctx, `
				SELECT COUNT(*) FROM commands
				WHERE task_host_id=$1 AND id<>$2
				  AND status IN (`+activeCommandStatusList()+`)`,
				taskHostID.Int64, cmdID.Int64).Scan(&newerActive)
		} else {
			_ = tx.QueryRowContext(ctx, `
				SELECT COUNT(*) FROM commands
				WHERE agent_id=$1 AND id<>$2
				  AND status IN (`+activeCommandStatusList()+`)`,
				agentID, cmdID.Int64).Scan(&newerActive)
		}
		if newerActive > 0 {
			return tx.Commit()
		}
	}

	// 清洗所有会落库的 Agent 文本，尤其是脚本输出中的 NUL / 非法 UTF-8。
	// 版本号目标列是 VARCHAR(64)，阶段列是 VARCHAR(32)，超长会直接让整个
	// 事务失败，因此必须截断。
	req.Stage = normalizeStage(req.Stage)
	req.ToVersion = normalizeVersion(req.ToVersion)
	req.FromVersion = normalizeVersion(req.FromVersion)
	req.Log = sanitizeAndTruncate(req.Log, maxReportLogBytes)
	req.Status = strings.TrimSpace(req.Status)
	if req.Status == "" {
		req.Status = protocol.CmdRunning
	}
	if req.Status != protocol.CmdRunning && !isFinalStatus(req.Status) {
		// 非法状态也按失败落库，避免上报被静默丢弃后任务永久运行。
		req.Log = appendLog(req.Log, fmt.Sprintf("[%s] Agent 上报了未知状态 %q，按失败处理\n",
			time.Now().Format("2006-01-02 15:04:05"), req.Status), maxReportLogBytes)
		req.Status = protocol.CmdFailed
	}

	final := isFinalStatus(req.Status)
	// 锁顺序与 Cancel/UpdateStage 一致：先 task_hosts，再 commands。
	if taskHostID.Valid && final && cmdActive {
		// 防止覆盖终态：租约回收可能已置为 timeout
		_, err = tx.ExecContext(ctx, `
			UPDATE task_hosts
			SET status=$2, stage=COALESCE(NULLIF($3,''),stage),
			    to_version=CASE WHEN $4<>'' THEN $4 ELSE to_version END,
			    exit_code=$5, log=$6, finished_at=NOW()
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
				UPDATE commands SET status=$2, result=$3,
				       last_stage=COALESCE(NULLIF($5,''),last_stage),
				       last_stage_at=NOW(),
				       log=COALESCE(log,'')||$4, finished_at=NOW()
				WHERE id=$1 AND status NOT IN ('success','failed','rolled_back','timeout','expired','canceled')`,
				cmdID.Int64, req.Status, result, req.Log, req.Stage)
		} else {
			_, err = tx.ExecContext(ctx, `
				UPDATE commands SET status='running',
				       last_stage=COALESCE(NULLIF($3,''),last_stage),
				       last_stage_at=NOW(),
				       lease_expire_at=CASE WHEN lease_expire_at IS NULL THEN NULL
				                            ELSE GREATEST(lease_expire_at, NOW() + INTERVAL '30 minutes') END,
				       log=COALESCE(log,'')||$2
				WHERE id=$1 AND status NOT IN ('success','failed','rolled_back','timeout','expired','canceled')`,
				cmdID.Int64, req.Log, req.Stage)
		}
		if err != nil {
			return err
		}
	}

	// 最终态：清除维护窗口，恢复在线（IP 查询需在事务内完成）
	var hostIP string
	if final && cmdActive {
		_, _ = tx.ExecContext(ctx, `
			UPDATE hosts SET maintenance_until=NULL, maintenance_reason='', agent_status='online', last_stage=$2
			WHERE agent_id=$1`, agentID, req.Stage)
	}
	// 成功结果可能先由 UpdateStage 的终态阶段自动落库，最终 report 到达时
	// 指令已是 success；上面的活动指令检查已排除“旧上报碰到新一轮执行”，
	// 这里仍需补写 Consul 版本，否则新 Agent 的 upgraded_version 不会更新。
	consulEligible := false
	if final && req.Status == protocol.CmdSuccess {
		consulEligible = true
		_ = tx.QueryRowContext(ctx, `SELECT ip FROM hosts WHERE agent_id=$1`, agentID).Scan(&hostIP)
	}

	// 提交事务
	if err := tx.Commit(); err != nil {
		return err
	}

	// 成功 → 回写 Consul Meta（在事务外、读锁下执行，避免写操作在锁内阻塞）
	if final && consulEligible && hostIP != "" && req.ToVersion != "" {
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

// activeCommandStatusList 返回活动指令状态的 SQL 字面量列表。
func activeCommandStatusList() string {
	return "'pending','dispatched','accepted','running'"
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
