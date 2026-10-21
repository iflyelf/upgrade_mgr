// Package component 组件定义的模型与数据访问。
package component

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/lib/pq"

	"github.com/iflyelf/upgrade_mgr/internal/pkg/scope"
)

// Component 组件定义（与 components 表对应）。
type Component struct {
	ID          int64    `json:"id"`
	TenantID    int64    `json:"tenant_id"`
	Name        string   `json:"name"`
	DisplayName string   `json:"display_name"`
	DeployMode  string   `json:"deploy_mode"`
	BinaryName  string   `json:"binary_name"`
	InstallPath string   `json:"install_path"`
	BackupDir   string   `json:"backup_dir"`
	ExecPath    string   `json:"exec_path"`
	ExecArgs    []string `json:"exec_args"`
	ExecAsUser  string   `json:"exec_as_user"`
	ExecTimeout string   `json:"exec_timeout"`
	// ScriptURL 是 script 模式的自定义脚本下载地址（绝对 http/https）。
	// 非空时跳过制品查找，Agent 直接从该地址下载并执行。
	ScriptURL   string     `json:"script_url"`
	ExecEnv     []string   `json:"exec_env"`
	WorkDir     string     `json:"work_dir"`
	IgnoreError bool       `json:"ignore_error"`
	PreInstall  [][]string `json:"pre_install"`
	PostInstall [][]string `json:"post_install"`
	// PostInstallScript 是 post_install 阶段执行的脚本内容（非 argv）。
	// Agent 写入临时文件后用 bash/sh 执行，适合较长的服务注册逻辑。
	PostInstallScript string     `json:"post_install_script"`
	ServiceName       string     `json:"service_name"`
	ServiceMgr        string     `json:"service_mgr"`
	StopCmd           [][]string `json:"stop_cmd"`
	StartCmd          [][]string `json:"start_cmd"`
	RestartCmd        [][]string `json:"restart_cmd"`
	VersionCmd        [][]string `json:"version_cmd"`
	VersionRegex      string     `json:"version_regex"`
	HealthCmd         [][]string `json:"health_cmd"`
	ConsulMaint       bool       `json:"consul_maint"`
	OSMatch           string     `json:"os_match"`
	ArchMatch         string     `json:"arch_match"`
	Enabled           bool       `json:"enabled"`
}

// Repo 组件仓储。
type Repo struct {
	db *sql.DB
}

// NewRepo 创建仓储。
func NewRepo(db *sql.DB) *Repo { return &Repo{db: db} }

// Create 新建组件。
func (r *Repo) Create(ctx context.Context, sc scope.Scope, c *Component) (int64, error) {
	if c.DeployMode == "" {
		c.DeployMode = "replace"
	}
	if c.BackupDir == "" {
		c.BackupDir = "/var/lib/upgrade-agent/backup"
	}
	if c.ServiceMgr == "" {
		c.ServiceMgr = "auto"
	}
	c.ScriptURL = strings.TrimSpace(c.ScriptURL)
	if err := ValidateScriptURL(c.ScriptURL); err != nil {
		return 0, err
	}
	var id int64
	err := r.db.QueryRowContext(ctx, `
		INSERT INTO components (name, display_name, deploy_mode, binary_name, install_path,
			backup_dir, exec_path, exec_args, exec_as_user, exec_timeout, exec_env, work_dir,
			ignore_error, pre_install, post_install, post_install_script, service_name, service_mgr,
			stop_cmd, start_cmd, restart_cmd, version_cmd, version_regex, health_cmd,
			consul_maint, os_match, arch_match, enabled, script_url, tenant_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29,$30)
		RETURNING id`,
		c.Name, c.DisplayName, c.DeployMode, c.BinaryName, c.InstallPath,
		c.BackupDir, c.ExecPath, pq.Array(c.ExecArgs), c.ExecAsUser, defExecTimeout(c.ExecTimeout),
		pq.Array(c.ExecEnv), c.WorkDir, c.IgnoreError, jsonArr(c.PreInstall), jsonArr(c.PostInstall),
		c.PostInstallScript, c.ServiceName, c.ServiceMgr, jsonArr(c.StopCmd), jsonArr(c.StartCmd), jsonArr(c.RestartCmd),
		jsonArr(c.VersionCmd), c.VersionRegex, jsonArr(c.HealthCmd),
		c.ConsulMaint, c.OSMatch, c.ArchMatch, c.Enabled, c.ScriptURL, sc.TenantForWrite(),
	).Scan(&id)
	return id, err
}

// List 返回全部组件。
func (r *Repo) List(ctx context.Context, sc scope.Scope) ([]*Component, error) {
	cond, args := sc.Where("", 1)
	rows, err := r.db.QueryContext(ctx, selectCols+` FROM components WHERE `+cond+` ORDER BY name`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Component
	for rows.Next() {
		c, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Get 按 ID 获取。
func (r *Repo) Get(ctx context.Context, sc scope.Scope, id int64) (*Component, error) {
	cond, args := sc.Where("", 2)
	row := r.db.QueryRowContext(ctx, selectCols+` FROM components WHERE id=$1 AND `+cond,
		append([]any{id}, args...)...)
	return scanRow(row)
}

// GetByName 按名称获取。
func (r *Repo) GetByName(ctx context.Context, sc scope.Scope, name string) (*Component, error) {
	cond, args := sc.Where("", 2)
	row := r.db.QueryRowContext(ctx, selectCols+` FROM components WHERE name=$1 AND `+cond,
		append([]any{name}, args...)...)
	return scanRow(row)
}

// Update 更新组件。
func (r *Repo) Update(ctx context.Context, sc scope.Scope, c *Component) error {
	c.ScriptURL = strings.TrimSpace(c.ScriptURL)
	if err := ValidateScriptURL(c.ScriptURL); err != nil {
		return err
	}
	cond, targs := sc.Where("", 30)
	res, err := r.db.ExecContext(ctx, `
		UPDATE components SET display_name=$2, deploy_mode=$3, binary_name=$4, install_path=$5,
			backup_dir=$6, exec_path=$7, exec_args=$8, exec_as_user=$9, exec_timeout=$10, exec_env=$11,
			work_dir=$12, ignore_error=$13, pre_install=$14, post_install=$15, post_install_script=$16,
			service_name=$17, service_mgr=$18, stop_cmd=$19, start_cmd=$20, restart_cmd=$21,
			version_cmd=$22, version_regex=$23, health_cmd=$24, consul_maint=$25, os_match=$26,
			arch_match=$27, enabled=$28, script_url=$29, updated_at=NOW()
		WHERE id=$1 AND `+cond,
		append([]any{
			c.ID, c.DisplayName, c.DeployMode, c.BinaryName, c.InstallPath,
			c.BackupDir, c.ExecPath, pq.Array(c.ExecArgs), c.ExecAsUser, defExecTimeout(c.ExecTimeout),
			pq.Array(c.ExecEnv), c.WorkDir, c.IgnoreError, jsonArr(c.PreInstall), jsonArr(c.PostInstall),
			c.PostInstallScript, c.ServiceName, c.ServiceMgr, jsonArr(c.StopCmd), jsonArr(c.StartCmd),
			jsonArr(c.RestartCmd), jsonArr(c.VersionCmd), c.VersionRegex, jsonArr(c.HealthCmd),
			c.ConsulMaint, c.OSMatch, c.ArchMatch, c.Enabled, c.ScriptURL,
		}, targs...)...,
	)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("组件不存在或无权访问")
	}
	return nil
}

// Delete 删除组件。
func (r *Repo) Delete(ctx context.Context, sc scope.Scope, id int64) error {
	cond, args := sc.Where("", 2)
	res, err := r.db.ExecContext(ctx, `DELETE FROM components WHERE id=$1 AND `+cond,
		append([]any{id}, args...)...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("组件不存在或无权访问")
	}
	return nil
}

const selectCols = `SELECT id, tenant_id, name, display_name, deploy_mode, binary_name, install_path,
	backup_dir, exec_path, exec_args, exec_as_user, exec_timeout, exec_env, work_dir,
	ignore_error, pre_install, post_install, post_install_script, service_name, service_mgr,
	stop_cmd, start_cmd, restart_cmd, version_cmd, version_regex, health_cmd,
	consul_maint, os_match, arch_match, enabled, script_url`

type scanner interface {
	Scan(dest ...any) error
}

func scanRow(s scanner) (*Component, error) {
	var c Component
	var pre, post, stop, start, restart, ver, health []byte
	err := s.Scan(&c.ID, &c.TenantID, &c.Name, &c.DisplayName, &c.DeployMode, &c.BinaryName, &c.InstallPath,
		&c.BackupDir, &c.ExecPath, pq.Array(&c.ExecArgs), &c.ExecAsUser, &c.ExecTimeout,
		pq.Array(&c.ExecEnv), &c.WorkDir, &c.IgnoreError, &pre, &post, &c.PostInstallScript,
		&c.ServiceName, &c.ServiceMgr, &stop, &start, &restart, &ver, &c.VersionRegex, &health,
		&c.ConsulMaint, &c.OSMatch, &c.ArchMatch, &c.Enabled, &c.ScriptURL)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("组件不存在")
	}
	if err != nil {
		return nil, err
	}
	c.PreInstall = unJSONArr(pre)
	c.PostInstall = unJSONArr(post)
	c.StopCmd = unJSONArr(stop)
	c.StartCmd = unJSONArr(start)
	c.RestartCmd = unJSONArr(restart)
	c.VersionCmd = unJSONArr(ver)
	c.HealthCmd = unJSONArr(health)
	return &c, nil
}

func jsonArr(v [][]string) []byte {
	if v == nil {
		v = [][]string{}
	}
	b, _ := json.Marshal(v)
	return b
}

func unJSONArr(b []byte) [][]string {
	var v [][]string
	if len(b) > 0 {
		_ = json.Unmarshal(b, &v)
	}
	return v
}

func defExecTimeout(s string) string {
	if s == "" {
		return "10m"
	}
	return s
}

// ValidateScriptURL 限制 script 模式的自定义下载地址只能使用绝对 http/https URL。
// 服务端与 Agent 各校验一次，避免 file:// 等本地协议被下发执行。
func ValidateScriptURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if len(raw) > 2048 {
		return fmt.Errorf("script_url 过长（最多 2048 字符）")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("script_url 格式错误: %w", err)
	}
	if !strings.EqualFold(u.Scheme, "http") && !strings.EqualFold(u.Scheme, "https") {
		return fmt.Errorf("script_url 仅支持 http/https")
	}
	if u.Host == "" {
		return fmt.Errorf("script_url 必须是绝对地址")
	}
	return nil
}
