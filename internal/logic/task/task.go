// Package task 任务与灰度编排：创建任务→解析目标→分批→生成待下发指令。
package task

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/auth"
	"github.com/iflyelf/upgrade_mgr/internal/logic/artifact"
	"github.com/iflyelf/upgrade_mgr/internal/logic/component"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/ipx"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/scope"
	"github.com/iflyelf/upgrade_mgr/internal/protocol"
)

const (
	// 命令脚本上限，避免 JSONB 行过大和超长指令。
	// 200KB 足以覆盖安装/卸载脚本，同时小于 Agent 单次日志上限。
	maxCommandScriptBytes = 200 * 1024
	// 标准输入（交互确认）上限。
	maxCommandStdinBytes = 4 * 1024
)

// Selector 目标选择器。
type Selector struct {
	IPExpr        string              `json:"ip_expr"`
	ExcludeIPExpr string              `json:"exclude_ip_expr"`
	Filters       map[string][]string `json:"filters"` // app_code/os_family/... → 值列表
}

// Strategy 灰度策略。
type Strategy struct {
	Mode             string `json:"mode"` // canary/count/percent
	CanaryCount      int    `json:"canary_count"`
	BatchSize        int    `json:"batch_size"`
	Concurrency      int    `json:"concurrency"`
	AutoPromote      bool   `json:"auto_promote"`
	HealthWait       string `json:"health_wait"`
	FailureThreshold string `json:"failure_threshold"`
	AutoRollback     bool   `json:"auto_rollback"`
}

// 任务类型：组件升级 / 一次性命令（脚本）。
const (
	KindUpgrade = "upgrade"
	KindCommand = "command"
)

// CreateRequest 创建任务请求。
//
// kind=upgrade（默认）走组件升级流水线；kind=command 直接执行 command.script，
// 不要求 component_id 与制品。
type CreateRequest struct {
	Kind        string               `json:"kind"`
	Name        string               `json:"name"`
	ComponentID int64                `json:"component_id"`
	Version     string               `json:"version"`
	Command     protocol.CommandSpec `json:"command"`
	Selector    Selector             `json:"selector"`
	Strategy    Strategy             `json:"strategy"`
	CreatedBy   string               `json:"created_by"`
	// actor 为内部字段：由 handler 注入当前用户，用于组件级权限校验。
	// 不是 JSON 字段，避免被请求体伪造。
	actor *auth.User `json:"-"`
}

// Task 任务视图。
type Task struct {
	ID          int64                `json:"id"`
	Kind        string               `json:"kind"`
	Name        string               `json:"name"`
	ComponentID int64                `json:"component_id"`
	Version     string               `json:"version"`
	Command     protocol.CommandSpec `json:"command,omitempty"`
	Status      string               `json:"status"`
	Selector    Selector             `json:"selector"`
	Strategy    Strategy             `json:"strategy"`
	TotalHosts  int                  `json:"total_hosts"`
	CreatedAt   time.Time            `json:"created_at"`
}

// Service 任务服务。
type Service struct {
	db       *sql.DB
	compRepo *component.Repo
	artRepo  *artifact.Repo
	// allowExecMode 是否允许下发 exec/replace_exec/script 模式（默认 false，安全）。
	// 用函数在下发时读取，页面修改设置后无需重启即可生效。
	allowExecMode func() bool
	// reconcile 在每轮编排前回收超时指令、同步 task_hosts 终态。
	// 由 svc 注入 agentgw.Gateway.ReconcileTasks；单元测试可不设置。
	reconcile func(context.Context) (int, error)
	// authorize 校验操作者是否有权对组件执行 deploy 权限。
	//
	// 由 handler 注入（org.Service + 操作者），为 nil 时不做限制
	// （保持单元测试与单租户小规模部署的既有行为）。
	authorize func(ctx context.Context, actor *auth.User, componentID int64, componentName string) error
	// hostGroups 返回用户对组件可操作的主机分组（空/含 "*" 表示不限）。
	// 为 nil 时不校验主机范围。
	hostGroups func(ctx context.Context, actor *auth.User, componentName string) (map[string]bool, error)
	// allowTaskOwnerOnly 为 true 时，非管理员只能操作自己创建的任务。
	// 用函数读取设置，页面修改后无需重启。
	allowTaskOwnerOnly func() bool
	// ownerPolicy 返回用户所属团队的任务操作策略：
	//   "on"      → 受限（不看全局开关）
	//   "off"     → 放行（不看全局开关）
	//   "inherit" → 回退全局设置
	//   ""        → 未加入任何团队，回退全局设置
	// 为 nil 时只使用全局开关。
	ownerPolicy func(ctx context.Context, username string) (string, error)
	// shareAllowed 判断 actor 能否操作 creator 创建的任务（同属 off 团队）。
	// 仅在生效策略为 off 时调用；为 nil 时不做共享校验（保守拒绝）。
	shareAllowed func(ctx context.Context, actor, creator string) (bool, error)
	// teamMate 判断两个用户是否同属至少一个团队（详情/日志可见性）。
	teamMate func(ctx context.Context, a, b string) (bool, error)
}

// SetAuthorizer 注入组件级权限校验（由服务端启动时设置）。
func (s *Service) SetAuthorizer(fn func(context.Context, *auth.User, int64, string) error) {
	s.authorize = fn
}

// SetHostGroupResolver 注入主机分组范围解析（由服务端启动时设置）。
func (s *Service) SetHostGroupResolver(fn func(context.Context, *auth.User, string) (map[string]bool, error)) {
	s.hostGroups = fn
}

// SetOwnerOnlyPolicy 注入「仅本人可操作任务」开关（由服务端启动时设置）。
func (s *Service) SetOwnerOnlyPolicy(fn func() bool) { s.allowTaskOwnerOnly = fn }

// SetOwnerPolicyResolver 注入团队级 owner 策略解析（由服务端启动时设置）。
func (s *Service) SetOwnerPolicyResolver(fn func(context.Context, string) (string, error)) {
	s.ownerPolicy = fn
}

// SetOwnerShareChecker 注入「同团队成员可互相操作任务」校验（由服务端启动时设置）。
func (s *Service) SetOwnerShareChecker(fn func(context.Context, string, string) (bool, error)) {
	s.shareAllowed = fn
}

// SetTeamMateChecker 注入「是否同属一个团队」校验（详情/日志可见性）。
func (s *Service) SetTeamMateChecker(fn func(context.Context, string, string) (bool, error)) {
	s.teamMate = fn
}

// CanViewTaskDetail 判断 actor 能否查看任务详情与执行日志。
//
// 规则（与操作权限独立）：
//   - 管理员与内部调用可见；
//   - 无创建者的历史任务可见（无法归属，保持兼容）；
//   - 创建者本人可见；
//   - 与创建者同属至少一个团队的用户可见；
//   - 其余不可见（按任务不存在处理，避免泄露存在性与日志）。
func (s *Service) CanViewTaskDetail(ctx context.Context, actor *auth.User, createdBy string) (bool, error) {
	if isPrivilegedActor(actor) {
		return true, nil
	}
	createdBy = strings.TrimSpace(createdBy)
	if createdBy == "" || createdBy == actor.Username {
		return true, nil
	}
	if s.teamMate == nil {
		return false, nil
	}
	return s.teamMate(ctx, actor.Username, createdBy)
}

// actorUsername 当前请求的操作者；后台/匿名调用返回空串。
func actorUsername(ctx context.Context) string {
	if u, ok := auth.FromContext(ctx); ok && u != nil {
		return strings.TrimSpace(u.Username)
	}
	return ""
}

// updatedByArg 为 UPDATE 追加 updated_by 参数并返回对应 SQL 片段。
//
// baseArgs 为已有参数（占位符 $1..$n）；返回片段使用 $n+1。
// 后台/匿名调用传入空串时保留原值，不覆盖最后一次人工操作。
func updatedByArg(ctx context.Context, baseArgs []any) ([]any, string) {
	args := append(baseArgs, actorUsername(ctx))
	return args, fmt.Sprintf("updated_by=COALESCE(NULLIF($%d,''), updated_by)", len(args))
}

// ownerModeFor 综合团队策略与全局开关，返回该用户的生效策略：
//
//	"on"      → 仅本人可操作
//	"off"     → 同团队（owner_only=off）成员可互相操作
//	"inherit" → 不受限（全局开关关闭，或未加入团队且全局关闭）
func (s *Service) ownerModeFor(ctx context.Context, username string) string {
	globalOn := s.allowTaskOwnerOnly != nil && s.allowTaskOwnerOnly()
	globalMode := "inherit"
	if globalOn {
		globalMode = "on"
	}
	if s.ownerPolicy == nil {
		return globalMode
	}
	policy, err := s.ownerPolicy(ctx, username)
	if err != nil {
		// 查询失败时保守处理：按全局设置（不因为权限查询故障而放宽）。
		return globalMode
	}
	switch policy {
	case "on", "off":
		return policy
	default: // inherit / 未加入团队
		return globalMode
	}
}

// AccessMode 返回用户的任务操作策略（on/off/inherit），
// 供前端决定是否展示操作按钮（写操作仍以服务端校验为准）。
func (s *Service) AccessMode(ctx context.Context, username string) string {
	return s.ownerModeFor(ctx, username)
}

// checkTaskOwner 校验非管理员是否可操作指定任务。
//
// 是否受限由「团队策略 teams.owner_only」与全局开关 agent.task_owner_only 共同决定
// （见 ownerOnlyFor）；管理员（admin/superadmin）始终放行。
// 用于「启动 / 暂停 / 推进 / 取消 / 重试 / 删除」等写操作。
//
// actor 从 ctx 取（handler 已通过 middleware 注入当前用户），
// 这样所有既有方法签名无需改动。
func (s *Service) checkTaskOwner(ctx context.Context, sc scope.Scope, taskID int64) error {
	actor, _ := auth.FromContext(ctx)
	if actor == nil || actor.IsAdmin || actor.IsSuperAdmin() || actor.HasRole(auth.RoleAdmin) {
		return nil
	}
	mode := s.ownerModeFor(ctx, actor.Username)
	if mode == "inherit" {
		return nil
	}
	tCond, tArgs := sc.Where("", 2)
	var createdBy string
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(created_by,'') FROM upgrade_tasks WHERE id=$1 AND `+tCond,
		append([]any{taskID}, tArgs...)...).Scan(&createdBy)
	if err != nil {
		return fmt.Errorf("任务不存在或无权访问")
	}
	if createdBy == "" || createdBy == actor.Username {
		return nil
	}
	if mode == "off" && s.shareAllowed != nil {
		if ok, shareErr := s.shareAllowed(ctx, actor.Username, createdBy); shareErr == nil && ok {
			return nil
		}
	}
	if mode == "off" {
		return fmt.Errorf("该任务由 %s 创建，仅创建者、同团队成员或管理员可操作", createdBy)
	}
	return fmt.Errorf("该任务由 %s 创建，仅创建者或管理员可操作", createdBy)
}

// NewService 创建任务服务。allowExecMode 为 nil 时视为禁止。
func NewService(db *sql.DB, compRepo *component.Repo, artRepo *artifact.Repo, allowExecMode func() bool) *Service {
	if allowExecMode == nil {
		allowExecMode = func() bool { return false }
	}
	return &Service{db: db, compRepo: compRepo, artRepo: artRepo, allowExecMode: allowExecMode}
}

// SetReconciler 注入指令/逐机状态对账函数（由服务端启动时设置）。
func (s *Service) SetReconciler(fn func(context.Context) (int, error)) {
	s.reconcile = fn
}

// PreviewResult 预览命中主机与分批。
type PreviewResult struct {
	TotalHosts int      `json:"total_hosts"`
	Batches    []int    `json:"batches"`    // 各批次主机数
	SampleIPs  []string `json:"sample_ips"` // 前若干个 IP 预览
}

// SetActor 注入当前操作者（供权限校验，不支持从 JSON 伪造）。
func (r *CreateRequest) SetActor(u *auth.User) { r.actor = u }

// isPrivilegedActor 判断操作者是否管理员；nil 表示内部调用（后台/测试），视为特权。
func isPrivilegedActor(actor *auth.User) bool {
	return actor == nil || actor.IsAdmin || actor.IsSuperAdmin() || actor.HasRole(auth.RoleAdmin)
}

// checkDeploy 校验操作者对该组件是否有 deploy 权限。
//
// 规则：
//   - 未注入 authorizer（单机小规模/测试）时放行；
//   - superadmin / admin 直接放行（管理员本就具备全量运维权限）；
//   - 其他用户需通过团队授权获得该组件的 deploy 权限。
func (s *Service) checkDeploy(ctx context.Context, actor *auth.User, componentID int64, name string) error {
	if s.authorize == nil || actor == nil {
		return nil
	}
	return s.authorize(ctx, actor, componentID, name)
}

// checkTaskDeploy 非管理员操作任务前再次校验组件 deploy 权限，
// 避免授权被回收后仍可启动/重试既有任务；命令任务仅管理员可操作。
func (s *Service) checkTaskDeploy(ctx context.Context, sc scope.Scope, taskID int64) error {
	actor, _ := auth.FromContext(ctx)
	if isPrivilegedActor(actor) {
		return nil
	}
	tCond, tArgs := sc.Where("", 2)
	var cid sql.NullInt64
	if err := s.db.QueryRowContext(ctx,
		`SELECT component_id FROM upgrade_tasks WHERE id=$1 AND `+tCond,
		append([]any{taskID}, tArgs...)...).Scan(&cid); err != nil {
		return fmt.Errorf("任务不存在或无权访问")
	}
	if !cid.Valid {
		return fmt.Errorf("命令任务仅管理员可操作")
	}
	comp, err := s.compRepo.Get(ctx, sc, cid.Int64)
	if err != nil {
		return err
	}
	return s.checkDeploy(ctx, actor, comp.ID, comp.Name)
}

// checkHostGroups 校验目标主机是否落在授权的主机分组内。
func (s *Service) checkHostGroups(ctx context.Context, actor *auth.User, componentName string, hosts []hostRow) error {
	if s.hostGroups == nil || actor == nil || componentName == "" {
		return nil
	}
	allowed, err := s.hostGroups(ctx, actor, componentName)
	if err != nil {
		return fmt.Errorf("校验主机范围失败: %w", err)
	}
	if len(allowed) == 0 || allowed["*"] {
		return nil
	}
	var denied []string
	for _, h := range hosts {
		if allowed[h.appCode] || allowed[h.bizGroup] {
			continue
		}
		denied = append(denied, h.ip)
	}
	if len(denied) == 0 {
		return nil
	}
	const maxShow = 10
	shown := denied
	suffix := ""
	if len(shown) > maxShow {
		shown = shown[:maxShow]
		suffix = " 等"
	}
	return fmt.Errorf("以下主机不在授权的主机分组内：%s%s", join(shown, ", "), suffix)
}

// Preview 解析目标并计算分批，不落库。
//
// 与 Create 相同的权限边界：命令任务仅管理员可预览；组件任务需要 deploy 权限。
func (s *Service) Preview(ctx context.Context, sc scope.Scope, req CreateRequest) (*PreviewResult, error) {
	kind := req.Kind
	if kind == "" {
		kind = KindUpgrade
	}
	if kind != KindUpgrade && kind != KindCommand {
		return nil, fmt.Errorf("不支持的任务类型 %q", kind)
	}
	var compName string
	if kind == KindCommand {
		if !isPrivilegedActor(req.actor) {
			return nil, fmt.Errorf("命令任务仅管理员可预览")
		}
	} else if kind == KindUpgrade {
		if req.ComponentID == 0 {
			return nil, fmt.Errorf("component_id 必填")
		}
		comp, err := s.compRepo.Get(ctx, sc, req.ComponentID)
		if err != nil {
			return nil, err
		}
		if err := s.checkDeploy(ctx, req.actor, comp.ID, comp.Name); err != nil {
			return nil, err
		}
		compName = comp.Name
	}

	hosts, err := s.matchHosts(ctx, sc, req.Selector)
	if err != nil {
		return nil, err
	}
	// 与创建一致：预览也不能越过团队授权的主机分组范围。
	if kind == KindUpgrade && compName != "" {
		if err := s.checkHostGroups(ctx, req.actor, compName, hosts); err != nil {
			return nil, err
		}
	}
	batches := splitBatches(len(hosts), req.Strategy)
	sample := []string{}
	for i, h := range hosts {
		if i >= 20 {
			break
		}
		// 多网卡主机展示出口地址，便于运维确认命中的确实是业务网段。
		if h.exitIP != "" {
			sample = append(sample, h.exitIP)
			continue
		}
		sample = append(sample, h.ip)
	}
	return &PreviewResult{TotalHosts: len(hosts), Batches: batches, SampleIPs: sample}, nil
}

// Create 创建任务：落库 upgrade_tasks + task_hosts + task_batches。
func (s *Service) Create(ctx context.Context, sc scope.Scope, req CreateRequest) (int64, error) {
	kind := req.Kind
	if kind == "" {
		kind = KindUpgrade
	}
	if kind != KindUpgrade && kind != KindCommand {
		return 0, fmt.Errorf("不支持的任务类型 %q", kind)
	}
	var compName string
	if kind == KindUpgrade {
		if req.ComponentID == 0 || req.Version == "" {
			return 0, fmt.Errorf("component_id 与 version 必填")
		}
		// 校验组件存在
		comp, err := s.compRepo.Get(ctx, sc, req.ComponentID)
		if err != nil {
			return 0, err
		}
		// 组件级权限：无 deploy 权限的用户不能发任务。
		if err := s.checkDeploy(ctx, req.actor, req.ComponentID, comp.Name); err != nil {
			return 0, err
		}
		compName = comp.Name
	} else {
		// 命令任务没有组件，无法用组件 deploy 权限约束；仅管理员可创建，
		// 避免获得任意主机写入权限的普通用户执行任意脚本。
		if !isPrivilegedActor(req.actor) {
			return 0, fmt.Errorf("命令任务仅管理员可创建")
		}
		// 命令任务：脚本不能为空；版本仅作展示，统一记录为 command。
		if !s.allowExecMode() {
			return 0, fmt.Errorf("服务端已禁用命令执行（agent.allow_exec_mode=false）")
		}
		if len(strings.TrimSpace(req.Command.Script)) == 0 {
			return 0, fmt.Errorf("命令脚本不能为空")
		}
		if req.Version == "" {
			req.Version = "command"
		}
		if len(req.Command.Script) > maxCommandScriptBytes {
			return 0, fmt.Errorf("命令脚本过长（最多 %d 字节）", maxCommandScriptBytes)
		}
		if len(req.Command.Stdin) > maxCommandStdinBytes {
			return 0, fmt.Errorf("标准输入过长（最多 %d 字节）", maxCommandStdinBytes)
		}
	}
	hosts, err := s.matchHosts(ctx, sc, req.Selector)
	if err != nil {
		return 0, err
	}
	if len(hosts) == 0 {
		return 0, fmt.Errorf("目标选择器未命中任何主机")
	}
	// 主机分组范围：团队授权可限定 app_code / biz_group，超出范围的主机拒绝下发。
	if kind == KindUpgrade {
		if err := s.checkHostGroups(ctx, req.actor, compName, hosts); err != nil {
			return 0, err
		}
	}

	selJSON, _ := json.Marshal(req.Selector)
	stratJSON, _ := json.Marshal(req.Strategy)
	cmdJSON, _ := json.Marshal(req.Command)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var componentID any
	if kind == KindUpgrade {
		componentID = req.ComponentID
	}
	var taskID int64
	err = tx.QueryRowContext(ctx, `
		INSERT INTO upgrade_tasks (kind, name, component_id, version, command, selector, strategy, status,
		                           created_by, updated_by, tenant_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,'draft',$8,$8,$9) RETURNING id`,
		kind, req.Name, componentID, req.Version, cmdJSON, selJSON, stratJSON, req.CreatedBy,
		sc.TenantForWrite()).Scan(&taskID)
	if err != nil {
		return 0, err
	}

	// 分批并写 task_hosts
	batchSizes := splitBatches(len(hosts), req.Strategy)
	idx, batchIdx := 0, 0
	for _, size := range batchSizes {
		for j := 0; j < size && idx < len(hosts); j++ {
			h := hosts[idx]
			_, err = tx.ExecContext(ctx, `
				INSERT INTO task_hosts (task_id, host_id, batch_index, status)
				VALUES ($1,$2,$3,'pending') ON CONFLICT (task_id, host_id) DO NOTHING`,
				taskID, h.id, batchIdx)
			if err != nil {
				return 0, err
			}
			idx++
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO task_batches (task_id, batch_index, status, total)
			VALUES ($1,$2,'pending',$3)`, taskID, batchIdx, size)
		if err != nil {
			return 0, err
		}
		batchIdx++
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return taskID, nil
}

// hostRow 任务目标主机。
//
// ip 是注册来源地址（主机身份）；exitIP 是 Agent 探测的出口地址。
// 多网卡主机在页面上可能按出口地址组织网段，因此 IP 段选择器同时匹配两者。
type hostRow struct {
	id     int64
	ip     string
	exitIP string
	// appCode / bizGroup 用于团队授权的主机分组校验。
	appCode  string
	bizGroup string
}

// matchHosts 解析 selector：IP 段展开 ∩ 分组过滤 − 排除。
func (s *Service) matchHosts(ctx context.Context, sc scope.Scope, sel Selector) ([]hostRow, error) {
	// 构建分组过滤 SQL。第一个条件恒为租户隔离，确保不会漏过滤。
	tCond, tArgs := sc.Where("", 1)
	where := []string{tCond}
	args := append([]any{}, tArgs...)
	i := len(args) + 1
	allowedCols := map[string]bool{
		"app_code": true, "os_family": true, "os_type": true, "account": true,
		"biz_group": true, "department": true, "vendor": true, "arch": true,
	}
	for col, vals := range sel.Filters {
		if !allowedCols[col] || len(vals) == 0 {
			continue
		}
		where = append(where, fmt.Sprintf("%s = ANY($%d)", col, i))
		args = append(args, pqStringArray(vals))
		i++
	}

	q := "SELECT id, ip, COALESCE(exit_ip,''), COALESCE(app_code,''), COALESCE(biz_group,'') " +
		"FROM hosts WHERE " + join(where, " AND ")
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// 一个主机可能同时有来源地址与出口地址；两个地址都登记，
	// 命中去重后只保留一条主机记录。多个主机共用同一出口 IP（NAT）
	// 时全部纳入，由命中后的去重逻辑合并。
	byIP := map[string][]int64{}
	all := map[int64]hostRow{}
	for rows.Next() {
		var id int64
		var ip, exitIP, appCode, bizGroup string
		if err := rows.Scan(&id, &ip, &exitIP, &appCode, &bizGroup); err != nil {
			return nil, err
		}
		byIP[ip] = append(byIP[ip], id)
		if exitIP != "" && exitIP != ip {
			byIP[exitIP] = append(byIP[exitIP], id)
		}
		all[id] = hostRow{id: id, ip: ip, exitIP: exitIP, appCode: appCode, bizGroup: bizGroup}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// IP 段展开（若指定）
	var wantIPs map[string]struct{}
	if sel.IPExpr != "" {
		ips, err := ipx.ParseWithExclude(sel.IPExpr, sel.ExcludeIPExpr, 0)
		if err != nil {
			return nil, fmt.Errorf("IP 表达式解析失败: %w", err)
		}
		wantIPs = map[string]struct{}{}
		for _, ip := range ips {
			wantIPs[ip] = struct{}{}
		}
	}

	matched := map[int64]struct{}{}
	for ip, ids := range byIP {
		if wantIPs != nil {
			if _, ok := wantIPs[ip]; !ok {
				continue
			}
		}
		for _, id := range ids {
			matched[id] = struct{}{}
		}
	}
	out := make([]hostRow, 0, len(matched))
	for id := range matched {
		out = append(out, all[id])
	}
	// 稳定排序（按 IP），多网卡主机按出口地址排序更符合运维直觉。
	sortHosts(out)
	return out, nil
}

// splitBatches 依策略计算各批次大小。
func splitBatches(total int, st Strategy) []int {
	if total == 0 {
		return nil
	}
	var batches []int
	remaining := total
	// 金丝雀首批
	if st.Mode == "canary" && st.CanaryCount > 0 && st.CanaryCount < total {
		batches = append(batches, st.CanaryCount)
		remaining -= st.CanaryCount
	}
	// 常规批
	batchSize := st.BatchSize
	if st.Mode == "percent" && st.BatchSize > 0 {
		batchSize = total * st.BatchSize / 100
		// 百分比模式下，批次大小至少为 1（避免 total=3, 20% → 0 的情况）
		if batchSize < 1 {
			batchSize = 1
		}
	}
	if batchSize <= 0 {
		batchSize = remaining // 一批全部
	}
	for remaining > 0 {
		n := batchSize
		if n > remaining {
			n = remaining
		}
		batches = append(batches, n)
		remaining -= n
	}
	return batches
}

// pqStringArray 包装为 pq 数组（避免直接依赖 lib/pq 于签名）。
func pqStringArray(vals []string) any {
	return arrayWrapper(vals)
}
