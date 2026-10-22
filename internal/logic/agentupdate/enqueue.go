package agentupdate

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/lib/pq"

	"github.com/iflyelf/upgrade_mgr/internal/pkg/scope"
	"github.com/iflyelf/upgrade_mgr/internal/protocol"
)

// EnqueueRequest 管理端下发 Agent 自升级的请求。
type EnqueueRequest struct {
	HostIDs     []int64 `json:"host_ids"`
	AllOutdated bool    `json:"all_outdated"`
	Force       bool    `json:"force"`   // 即使哈希一致也重新下发
	Variant     string  `json:"variant"` // 空=自动；legacy=强制旧内核构建
}

// Skip 记录未下发的主机与原因。
type Skip struct {
	HostID   int64  `json:"host_id"`
	IP       string `json:"ip"`
	Hostname string `json:"hostname"`
	AgentID  string `json:"agent_id,omitempty"`
	Reason   string `json:"reason"`
}

// Result 下发结果。
type Result struct {
	TargetVersion string `json:"target_version"`
	Enqueued      int    `json:"enqueued"`
	SkippedCount  int    `json:"skipped_count"`
	Skipped       []Skip `json:"skipped"`
}

const maxSkippedDetail = 200

type hostRow struct {
	ID           int64
	IP           string
	Hostname     string
	AgentID      string
	Arch         string
	Kernel       string
	BuildHash    string
	BuildVariant string
}

// Enqueue 按主机范围下发自升级指令。
//
// 安全/一致性：
//   - 主机查询强制带租户作用域；
//   - 同一 Agent 同时只允许一条未结束的 self_update（数据库唯一索引 + NOT EXISTS）；
//   - 计算目标二进制 SHA256，Agent 已是同一哈希时默认跳过。
func Enqueue(ctx context.Context, db *sql.DB, sc scope.Scope, resolver *Resolver,
	req EnqueueRequest, baseURL string) (Result, error) {
	res := Result{TargetVersion: resolver.Version, Skipped: []Skip{}}
	if len(req.HostIDs) == 0 && !req.AllOutdated {
		return res, fmt.Errorf("请选择主机，或勾选升级全部旧 Agent")
	}
	if len(req.HostIDs) > 1000 {
		return res, fmt.Errorf("单次最多选择 1000 台主机")
	}
	if _, _, err := NormalizeArchVariant("amd64", req.Variant); err != nil {
		return res, err
	}

	// 强制重新升级：先取消这些 Agent 上仍处于活动状态的 self_update 指令。
	// 只取消自升级，不动组件升级等任务指令；insertCommand 仍会检查是否有
	// 其他活动指令，避免在组件升级过程中重启 Agent。
	if req.Force && len(req.HostIDs) > 0 {
		if _, err := db.ExecContext(ctx, `
			UPDATE commands c
			SET status='canceled', finished_at=NOW(),
			    log=COALESCE(c.log,'')||$2
			FROM hosts h
			WHERE c.host_id=h.id AND c.agent_id=h.agent_id
			  AND c.type='self_update'
			  AND c.status IN ('pending','dispatched','accepted','running')
			  AND h.id = ANY($1)`,
			pq.Array(req.HostIDs),
			fmt.Sprintf("[%s] 管理员强制重新下发 Agent 自升级，取消旧指令\n",
				time.Now().Format("2006-01-02 15:04:05"))); err != nil {
			return res, err
		}
	}

	tCond, tArgs := sc.Where("hosts", 1)
	where := []string{tCond, "hosts.agent_id <> ''"}
	args := append([]any{}, tArgs...)
	i := len(tArgs) + 1
	if len(req.HostIDs) > 0 {
		where = append(where, fmt.Sprintf("hosts.id = ANY($%d)", i))
		args = append(args, pq.Array(req.HostIDs))
	}
	rows, err := db.QueryContext(ctx, `
		SELECT hosts.id, hosts.ip, COALESCE(hosts.hostname,''), hosts.agent_id,
		       COALESCE(hosts.arch,''), COALESCE(hosts.kernel,''),
		       COALESCE(hosts.agent_build_hash,''), COALESCE(hosts.agent_build_variant,'')
		FROM hosts
		WHERE `+strings.Join(where, " AND ")+`
		ORDER BY hosts.id`, args...)
	if err != nil {
		return res, err
	}
	defer rows.Close()

	hosts := []hostRow{}
	for rows.Next() {
		var h hostRow
		if err := rows.Scan(&h.ID, &h.IP, &h.Hostname, &h.AgentID, &h.Arch, &h.Kernel,
			&h.BuildHash, &h.BuildVariant); err != nil {
			return res, err
		}
		hosts = append(hosts, h)
	}
	if err := rows.Err(); err != nil {
		return res, err
	}

	for _, h := range hosts {
		skip := func(reason string) {
			res.SkippedCount++
			if len(res.Skipped) < maxSkippedDetail {
				res.Skipped = append(res.Skipped, Skip{
					HostID: h.ID, IP: h.IP, Hostname: h.Hostname,
					AgentID: h.AgentID, Reason: reason,
				})
			}
		}
		target, err := resolveForHost(resolver, h, req.Variant, baseURL)
		if err != nil {
			skip(err.Error())
			continue
		}
		if !req.Force && strings.EqualFold(h.BuildHash, target.SHA256) {
			skip("Agent 已是目标版本")
			continue
		}
		ok, err := insertCommand(ctx, db, h, target)
		if err != nil {
			return res, err
		}
		if !ok {
			skip("已有执行中的指令")
			continue
		}
		res.Enqueued++
	}
	return res, nil
}

// AutoEnqueue 在心跳/注册后按需下发一次自升级。
//
// 冷却 30 分钟，避免自升级失败后在每次心跳中反复重试；
// 同一 Agent 已有任何在途指令时不会再插入（insertCommand 内保证）。
func AutoEnqueue(ctx context.Context, db *sql.DB, agentID string, resolver *Resolver, baseURL string) (bool, error) {
	if strings.TrimSpace(agentID) == "" {
		return false, nil
	}
	var h hostRow
	err := db.QueryRowContext(ctx, `
		SELECT hosts.id, hosts.ip, COALESCE(hosts.hostname,''), hosts.agent_id,
		       COALESCE(hosts.arch,''), COALESCE(hosts.kernel,''),
		       COALESCE(hosts.agent_build_hash,''), COALESCE(hosts.agent_build_variant,'')
		FROM hosts WHERE hosts.agent_id=$1 LIMIT 1`, agentID).
		Scan(&h.ID, &h.IP, &h.Hostname, &h.AgentID, &h.Arch, &h.Kernel, &h.BuildHash, &h.BuildVariant)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	var lastStatus string
	var lastCreated time.Time
	err = db.QueryRowContext(ctx, `
		SELECT status, created_at FROM commands
		WHERE agent_id=$1 AND type='self_update'
		ORDER BY id DESC LIMIT 1`, agentID).Scan(&lastStatus, &lastCreated)
	if err == nil {
		if isActiveStatus(lastStatus) {
			return false, nil
		}
		if time.Since(lastCreated) < 30*time.Minute {
			return false, nil
		}
	} else if err != sql.ErrNoRows {
		return false, err
	}

	target, err := resolveForHost(resolver, h, "", baseURL)
	if err != nil {
		return false, err
	}
	if strings.EqualFold(h.BuildHash, target.SHA256) {
		return false, nil
	}
	return insertCommand(ctx, db, h, target)
}

func resolveForHost(r *Resolver, h hostRow, forcedVariant, baseURL string) (Target, error) {
	arch := strings.TrimSpace(h.Arch)
	if arch == "" {
		return Target{}, fmt.Errorf("主机未上报 Agent 架构")
	}
	variant := forcedVariant
	if variant == "" {
		variant = ChooseVariant(h.BuildVariant, h.Kernel)
	}
	return r.Resolve(arch, variant, baseURL)
}

func insertCommand(ctx context.Context, db *sql.DB, h hostRow, target Target) (bool, error) {
	payload, err := json.Marshal(protocol.CommandPayload{
		ToVersion: target.Version,
		Artifact: protocol.Artifact{
			Version: target.Version,
			Arch:    target.Arch,
			URL:     target.URL,
			SHA256:  target.SHA256,
			Size:    target.Size,
		},
	})
	if err != nil {
		return false, err
	}
	logLine := fmt.Sprintf("[%s] 已下发 Agent 自升级：%s -> %s（%s）\n",
		time.Now().Format("2006-01-02 15:04:05"), target.Version, target.Filename(), shortHash(target.SHA256))
	res, err := db.ExecContext(ctx, `
		INSERT INTO commands (host_id, agent_id, type, payload, status, expire_at, log)
		SELECT $1::bigint,$2::varchar(64),'self_update',$3::jsonb,'pending',NOW()+INTERVAL '24 hours',$4::text
		WHERE NOT EXISTS (
			SELECT 1 FROM commands
			WHERE agent_id=$2::varchar(64) AND status IN ('pending','dispatched','accepted','running')
		)
		ON CONFLICT DO NOTHING`,
		h.ID, h.AgentID, string(payload), logLine)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func isActiveStatus(s string) bool {
	switch s {
	case "pending", "dispatched", "accepted", "running":
		return true
	default:
		return false
	}
}

func shortHash(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
