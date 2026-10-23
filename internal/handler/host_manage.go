package handler

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/iflyelf/upgrade_mgr/internal/pkg/audit"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/hostnet"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/httpx"
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
		hasField := req.TunnelIP != nil || req.ServerURLOverride != nil
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

		overrideChanged := req.ServerURLOverride != nil
		// 字段占位符 $2..$10，租户条件从 $11 开始。
		cond, condArgs := reqScope(r).Where("", 11)
		args := append([]any{
			id, req.Hostname, req.Account, req.AppCode, req.BizGroup,
			req.Department, req.Vendor, req.HostType, req.TunnelIP, req.ServerURLOverride,
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

		var ip, agentID string
		var opErr error
		defer func() {
			auditOp(s, r, audit.ActionHostDelete, "host", strconv.FormatInt(id, 10), ip, opErr)
		}()

		tx, err := s.DB.BeginTx(r.Context(), nil)
		if err != nil {
			opErr = err
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		defer tx.Rollback()

		cond, condArgs := reqScope(r).Where("", 2)
		// 先做无锁的租户/存在性检查：跨租户主机直接视为不存在，
		// 避免为了加锁而触碰他人的子表数据。
		err = tx.QueryRowContext(r.Context(),
			`SELECT ip, COALESCE(agent_id,'') FROM hosts WHERE id=$1 AND `+cond,
			append([]any{id}, condArgs...)...).Scan(&ip, &agentID)
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

		// 锁顺序必须与 Report/UpdateStage 的 task_hosts -> commands -> hosts 一致。
		// 若反过来先锁 hosts 再级联删除子表，会与正在落终态的 Agent 上报形成
		// “上报持有 task_hosts 等 hosts，删除持有 hosts 等 task_hosts”的死锁。
		for _, q := range []string{
			`SELECT 1 FROM task_hosts WHERE host_id=$1 FOR UPDATE`,
			`SELECT 1 FROM commands WHERE host_id=$1 FOR UPDATE`,
		} {
			rows, qerr := tx.QueryContext(r.Context(), q, id)
			if qerr != nil {
				opErr = qerr
				httpx.FailErr(w, http.StatusInternalServerError, qerr)
				return
			}
			rows.Close()
			if qerr = rows.Err(); qerr != nil {
				opErr = qerr
				httpx.FailErr(w, http.StatusInternalServerError, qerr)
				return
			}
		}

		// 最后锁主机行并复查，防止检查后被重新注册/改租户。
		err = tx.QueryRowContext(r.Context(),
			`SELECT ip, COALESCE(agent_id,'') FROM hosts WHERE id=$1 AND `+cond+` FOR UPDATE`,
			append([]any{id}, condArgs...)...).Scan(&ip, &agentID)
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

		var activeTasks, activeCommands int
		if err := tx.QueryRowContext(r.Context(), `
			SELECT
				(SELECT COUNT(1) FROM task_hosts
				  WHERE host_id=$1 AND status IN ('pending','running')),
				(SELECT COUNT(1) FROM commands
				  WHERE host_id=$1 AND status IN ('pending','dispatched','accepted','running'))`,
			id).Scan(&activeTasks, &activeCommands); err != nil {
			opErr = err
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		if activeTasks > 0 || activeCommands > 0 {
			opErr = fmt.Errorf("主机仍有未完成任务或指令（任务 %d，指令 %d）", activeTasks, activeCommands)
			httpx.Fail(w, http.StatusConflict,
				"主机存在未完成的任务或指令，请先取消任务或等待执行完成后再删除")
			return
		}

		// 撤销 Agent 凭据；Agent 进程仍会运行，但后续请求将无法通过认证。
		//
		// 历史注册逻辑只写入 agents.ip，不一定回填 agents.host_id，
		// 因此同时按 host_id / ip / 当前 agent_id 清理，避免留下旧 token。
		if _, err := tx.ExecContext(r.Context(),
			`DELETE FROM agents WHERE host_id=$1 OR ip=$2 OR agent_id=$3`,
			id, ip, agentID); err != nil {
			opErr = err
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		res, err := tx.ExecContext(r.Context(), `DELETE FROM hosts WHERE id=$1`, id)
		if err != nil {
			opErr = err
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		if n, _ := res.RowsAffected(); n == 0 {
			opErr = sql.ErrNoRows
			httpx.Fail(w, http.StatusNotFound, "主机不存在或无权访问")
			return
		}
		if err := tx.Commit(); err != nil {
			opErr = err
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		httpx.OK(w, map[string]any{"id": id, "ip": ip})
	}
}
