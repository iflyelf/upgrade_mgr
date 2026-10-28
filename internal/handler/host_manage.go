package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/iflyelf/upgrade_mgr/internal/logic/fielddef"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/audit"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/hostnet"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/httpx"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/scope"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

// hostUpdateRequest 主机可人工维护字段。
//
// 使用指针区分「未提交」与「显式清空」：未提交的字段保持原值，传空字符串则清空。
// OS/ARCH/Agent 状态等由 Agent 心跳或 Consul 上报，不允许页面修改。
type hostUpdateRequest struct {
	Hostname   *string `json:"hostname"`
	Account    *string `json:"account"`
	AppCode    *string `json:"app_code"`
	BizGroup   *string `json:"biz_group"`
	Department *string `json:"department"`
	Vendor     *string `json:"vendor"`
	HostType   *string `json:"host_type"`
	// TunnelIP 是「主机侧隧道 IP」：主机在隧道网段里的地址，仅用于展示与筛选
	// （平台不主动反连主机，指令仍由 Agent 外连取回）。只接受 IP 字面量。
	TunnelIP *string `json:"tunnel_ip"`
	// ServerURLOverride 是「连接地址覆盖」：临时改写该主机 Agent 连接平台的地址。
	//
	// 它不参与隧道建立——隧道由隧道软件（WireGuard/IPSec/frp/专线等）负责，
	// 这里只解决"同一平台存在多个可达入口，需要按主机远程切换"的情况。
	// 支持纯 IP、IP:端口或完整 http(s) URL；留空表示取消覆盖，Agent 回退默认地址。
	ServerURLOverride *string `json:"server_url_override"`
	// CustomFields 主机自定义字段值（键必须在 host_field_defs 中定义）。
	// 不传表示不修改；传入时按键合并（只覆盖传入的键）。
	CustomFields map[string]string `json:"custom_fields"`
}

type hostField struct {
	label string
	value *string
	max   int
}

func (r *hostUpdateRequest) fields() []hostField {
	return []hostField{
		{"主机名", r.Hostname, 255},
		{"账号", r.Account, 128},
		{"app_code", r.AppCode, 128},
		{"业务组", r.BizGroup, 255},
		{"部门", r.Department, 255},
		{"厂商", r.Vendor, 128},
		{"主机类型", r.HostType, 64},
	}
}

// normalizeHostFields 去首尾空白并校验长度/控制字符；nil 表示不修改。
func normalizeHostFields(fields []hostField) error {
	for _, f := range fields {
		if f.value == nil {
			continue
		}
		v := strings.TrimSpace(*f.value)
		if utf8.RuneCountInString(v) > f.max {
			return fmt.Errorf("%s 过长（最多 %d 字符）", f.label, f.max)
		}
		for _, r := range v {
			if r < 0x20 || r == 0x7f {
				return fmt.Errorf("%s 包含控制字符", f.label)
			}
		}
		*f.value = v
	}
	return nil
}

// HostUpdate 更新主机可人工维护字段（管理员，租户隔离）。
func HostUpdate(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id <= 0 {
			httpx.Fail(w, http.StatusBadRequest, "主机 ID 无效")
			return
		}
		var req hostUpdateRequest
		if err := httpx.DecodeJSON(r, &req); err != nil {
			httpx.Fail(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		fields := req.fields()
		if err := normalizeHostFields(fields); err != nil {
			httpx.Fail(w, http.StatusBadRequest, err.Error())
			return
		}
		// 主机侧隧道 IP：只接受 IP 字面量（用于展示/筛选）。
		if req.TunnelIP != nil {
			v := hostnet.NormalizeExitIP(*req.TunnelIP)
			if v == "" && strings.TrimSpace(*req.TunnelIP) != "" {
				httpx.Fail(w, http.StatusBadRequest, "隧道 IP 不是合法的 IP 地址")
				return
			}
			req.TunnelIP = &v
		}
		// 连接地址覆盖：允许 IP / IP:端口 / 完整 URL。
		if req.ServerURLOverride != nil {
			v, err := hostnet.NormalizeAddr(*req.ServerURLOverride)
			if err != nil {
				httpx.Fail(w, http.StatusBadRequest, err.Error())
				return
			}
			req.ServerURLOverride = &v
		}
		hasField := req.TunnelIP != nil || req.ServerURLOverride != nil || len(req.CustomFields) > 0
		for _, f := range fields {
			if f.value != nil {
				hasField = true
				break
			}
		}
		if !hasField {
			httpx.Fail(w, http.StatusBadRequest, "没有可更新的字段")
			return
		}

		var ip string
		var opErr error
		defer func() {
			auditOp(s, r, audit.ActionHostUpdate, "host", strconv.FormatInt(id, 10), ip, opErr)
		}()

		// 自定义字段：按定义校验（未知键丢弃、类型/选项校验），再按键合并。
		var customJSON []byte
		if len(req.CustomFields) > 0 {
			repo, repoErr := fielddef.NewRepo(s.DB, fielddef.KindHost)
			if repoErr != nil {
				httpx.FailErr(w, http.StatusInternalServerError, repoErr)
				return
			}
			defs, defErr := repo.List(r.Context())
			if defErr != nil {
				httpx.FailErr(w, http.StatusInternalServerError, defErr)
				return
			}
			clean, valErr := fielddef.ValidateValues(defs, req.CustomFields)
			if valErr != nil {
				httpx.Fail(w, http.StatusBadRequest, valErr.Error())
				return
			}
			customJSON, _ = json.Marshal(clean)
		}

		overrideChanged := req.ServerURLOverride != nil
		// 字段占位符 $2..$11，租户条件从 $12 开始。
		cond, condArgs := reqScope(r).Where("", 12)
		var customArg any
		if len(customJSON) > 0 {
			customArg = string(customJSON)
		}
		args := append([]any{
			id, req.Hostname, req.Account, req.AppCode, req.BizGroup,
			req.Department, req.Vendor, req.HostType, req.TunnelIP, req.ServerURLOverride,
			customArg,
		}, condArgs...)
		err = s.DB.QueryRowContext(r.Context(), `
			UPDATE hosts SET
				hostname   = COALESCE($2, hostname),
				account    = COALESCE($3, account),
				app_code   = COALESCE($4, app_code),
				biz_group  = COALESCE($5, biz_group),
				department = COALESCE($6, department),
				vendor     = COALESCE($7, vendor),
				host_type  = COALESCE($8, host_type),
				tunnel_ip  = COALESCE($9, tunnel_ip),
				server_url_override = COALESCE($10, server_url_override),
				custom_fields = CASE WHEN $11::text IS NULL THEN custom_fields
				                     ELSE COALESCE(custom_fields,'{}'::jsonb) || $11::jsonb END,
				updated_at = NOW()
			WHERE id=$1 AND `+cond+`
			RETURNING ip`, args...).Scan(&ip)
		if errors.Is(err, sql.ErrNoRows) {
			opErr = err
			httpx.Fail(w, http.StatusNotFound, "主机不存在或无权访问")
			return
		}
		if err != nil {
			opErr = err
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		// 连接地址覆盖变化后自动做一次平台侧拨测，并请求主机侧自检，
		// 列表可直接看到结论，不必等运维手动点「测试」。
		if overrideChanged {
			triggerTunnelCheck(s, id)
		}
		httpx.OK(w, map[string]any{"id": id, "ip": ip})
	}
}

// HostDelete 删除主机（管理员，租户隔离）。
//
// 安全约束：
//   - 存在未完成任务/指令时拒绝删除，避免编排中的主机被抽走；
//   - 删除主机会级联删除 task_hosts / commands 历史；
//   - 同时删除该主机关联的 Agent 记录，撤销其 token，避免留下可继续认证的孤儿。
func HostDelete(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id <= 0 {
			httpx.Fail(w, http.StatusBadRequest, "主机 ID 无效")
			return
		}
		ip, derr := deleteHost(r.Context(), s, reqScope(r), id)
		auditOp(s, r, audit.ActionHostDelete, "host", strconv.FormatInt(id, 10), ip, errOrNil(derr))
		if derr != nil {
			if derr.internal {
				httpx.FailErr(w, derr.status, derr.err)
			} else {
				httpx.Fail(w, derr.status, derr.msg)
			}
			return
		}
		httpx.OK(w, map[string]any{"id": id, "ip": ip})
	}
}

// maxHostBatchDelete 单次批量删除的主机数上限（与分页最大条数一致）。
const maxHostBatchDelete = 500

// HostBatchDelete 批量删除主机：POST /api/hosts/batch-delete {"ids":[1,2,3]}。
//
// 逐台复用单台删除的校验与事务（租户隔离、未完成任务/指令拒绝、撤销 Agent 凭据），
// 每台单独写审计；某台失败不影响其他主机，返回成功列表与逐台失败原因。
func HostBatchDelete(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			IDs []int64 `json:"ids"`
		}
		if err := httpx.DecodeJSON(r, &req); err != nil {
			httpx.Fail(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		ids := uniquePositiveIDs(req.IDs)
		if len(ids) == 0 {
			httpx.Fail(w, http.StatusBadRequest, "请选择要删除的主机")
			return
		}
		if len(ids) > maxHostBatchDelete {
			httpx.Fail(w, http.StatusBadRequest, fmt.Sprintf("单次最多删除 %d 台主机", maxHostBatchDelete))
			return
		}
		type failure struct {
			ID    int64  `json:"id"`
			IP    string `json:"ip,omitempty"`
			Error string `json:"error"`
		}
		deleted := make([]int64, 0, len(ids))
		failed := []failure{}
		sc := reqScope(r)
		for _, id := range ids {
			if r.Context().Err() != nil {
				failed = append(failed, failure{ID: id, Error: "请求已取消"})
				continue
			}
			ip, derr := deleteHost(r.Context(), s, sc, id)
			auditOp(s, r, audit.ActionHostDelete, "host", strconv.FormatInt(id, 10), ip, errOrNil(derr))
			if derr != nil {
				failed = append(failed, failure{ID: id, IP: ip, Error: derr.msg})
				continue
			}
			deleted = append(deleted, id)
		}
		httpx.OK(w, map[string]any{"deleted": deleted, "failed": failed})
	}
}

// uniquePositiveIDs 去重并过滤非法 ID，保留输入顺序。
func uniquePositiveIDs(in []int64) []int64 {
	seen := make(map[int64]bool, len(in))
	out := make([]int64, 0, len(in))
	for _, id := range in {
		if id > 0 && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// deleteError 删除失败：status/msg 返回给客户端，err 为审计用的原始错误。
// internal=true 表示内部错误，客户端消息需经 FailErr 脱敏。
type deleteError struct {
	status   int
	msg      string
	err      error
	internal bool
}

func (e *deleteError) Error() string { return e.err.Error() }

// errOrNil 避免把 nil *deleteError 转成非 nil 的 error 接口值。
func errOrNil(e *deleteError) error {
	if e == nil {
		return nil
	}
	return e
}

func hostNotFound(err error) *deleteError {
	return &deleteError{status: http.StatusNotFound, msg: "主机不存在或无权访问", err: err}
}

func hostInternalErr(err error) *deleteError {
	return &deleteError{status: http.StatusInternalServerError, msg: "删除失败（服务端错误）", err: err, internal: true}
}

// deleteHost 在单个事务内删除一台主机，返回主机 IP（查不到时为空）。
func deleteHost(ctx context.Context, s *svc.ServiceContext, sc scope.Scope, id int64) (string, *deleteError) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", hostInternalErr(err)
	}
	defer tx.Rollback()
	ip, derr := deleteHostTx(ctx, tx, sc, id)
	if derr != nil {
		return ip, derr
	}
	if err := tx.Commit(); err != nil {
		return ip, hostInternalErr(err)
	}
	return ip, nil
}

// deleteHostTx 在调用方事务内删除一台主机（不提交）。
//
// 异步删除作业用它把「删除本批主机」与「记录作业进度」放进同一事务，
// 每台主机前设 SAVEPOINT，单台失败只回滚该台。
func deleteHostTx(ctx context.Context, tx *sql.Tx, sc scope.Scope, id int64) (string, *deleteError) {
	var ip, agentID string
	var err error
	cond, condArgs := sc.Where("", 2)
	// 先做无锁的租户/存在性检查：跨租户主机直接视为不存在，
	// 避免为了加锁而触碰他人的子表数据。
	err = tx.QueryRowContext(ctx,
		`SELECT ip, COALESCE(agent_id,'') FROM hosts WHERE id=$1 AND `+cond,
		append([]any{id}, condArgs...)...).Scan(&ip, &agentID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", hostNotFound(err)
	}
	if err != nil {
		return "", hostInternalErr(err)
	}

	// 锁顺序必须与 Report/UpdateStage 的 task_hosts -> commands -> hosts 一致。
	// 若反过来先锁 hosts 再级联删除子表，会与正在落终态的 Agent 上报形成
	// “上报持有 task_hosts 等 hosts，删除持有 hosts 等 task_hosts”的死锁。
	for _, q := range []string{
		`SELECT 1 FROM task_hosts WHERE host_id=$1 FOR UPDATE`,
		`SELECT 1 FROM commands WHERE host_id=$1 FOR UPDATE`,
	} {
		rows, qerr := tx.QueryContext(ctx, q, id)
		if qerr != nil {
			return ip, hostInternalErr(qerr)
		}
		rows.Close()
		if qerr = rows.Err(); qerr != nil {
			return ip, hostInternalErr(qerr)
		}
	}

	// 最后锁主机行并复查，防止检查后被重新注册/改租户。
	err = tx.QueryRowContext(ctx,
		`SELECT ip, COALESCE(agent_id,'') FROM hosts WHERE id=$1 AND `+cond+` FOR UPDATE`,
		append([]any{id}, condArgs...)...).Scan(&ip, &agentID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", hostNotFound(err)
	}
	if err != nil {
		return ip, hostInternalErr(err)
	}

	var activeTasks, activeCommands int
	if err := tx.QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(1) FROM task_hosts
			  WHERE host_id=$1 AND status IN ('pending','running')),
			(SELECT COUNT(1) FROM commands
			  WHERE host_id=$1 AND status IN ('pending','dispatched','accepted','running'))`,
		id).Scan(&activeTasks, &activeCommands); err != nil {
		return ip, hostInternalErr(err)
	}
	if activeTasks > 0 || activeCommands > 0 {
		return ip, &deleteError{
			status: http.StatusConflict,
			msg:    "主机存在未完成的任务或指令，请先取消任务或等待执行完成后再删除",
			err:    fmt.Errorf("主机仍有未完成任务或指令（任务 %d，指令 %d）", activeTasks, activeCommands),
		}
	}

	// 撤销 Agent 凭据；Agent 进程仍会运行，但后续请求将无法通过认证。
	//
	// 历史注册逻辑只写入 agents.ip，不一定回填 agents.host_id，
	// 因此同时按 host_id / ip / 当前 agent_id 清理，避免留下旧 token。
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM agents WHERE host_id=$1 OR ip=$2 OR agent_id=$3`,
		id, ip, agentID); err != nil {
		return ip, hostInternalErr(err)
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM hosts WHERE id=$1`, id)
	if err != nil {
		return ip, hostInternalErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ip, hostNotFound(sql.ErrNoRows)
	}
	return ip, nil
}
