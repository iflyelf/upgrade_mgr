// Package component 组件定义的模型与数据访问。
package component

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/lib/pq"

	"github.com/iflyelf/upgrade_mgr/internal/pkg/scope"
)

// Component 组件定义（与 components 表对应）。
type Component struct {
	ID           int64      `json:"id"`
	TenantID     int64      `json:"tenant_id"`
	Name         string     `json:"name"`
	DisplayName  string     `json:"display_name"`
	DeployMode   string     `json:"deploy_mode"`
	BinaryName   string     `json:"binary_name"`
	InstallPath  string     `json:"install_path"`
	BackupDir    string     `json:"backup_dir"`
	ExecPath     string     `json:"exec_path"`
	ExecArgs     []string   `json:"exec_args"`
	ExecAsUser   string     `json:"exec_as_user"`
	ExecTimeout  string     `json:"exec_timeout"`
	ExecEnv      []string   `json:"exec_env"`
	WorkDir      string     `json:"work_dir"`
	IgnoreError  bool       `json:"ignore_error"`
	PreInstall   [][]string `json:"pre_install"`
	PostInstall  [][]string `json:"post_install"`
	ServiceName  string     `json:"service_name"`
	ServiceMgr   string     `json:"service_mgr"`
	StopCmd      [][]string `json:"stop_cmd"`
	StartCmd     [][]string `json:"start_cmd"`
	RestartCmd   [][]string `json:"restart_cmd"`
	VersionCmd   [][]string `json:"version_cmd"`
	VersionRegex string     `json:"version_regex"`
	HealthCmd    [][]string `json:"health_cmd"`
	ConsulMaint  bool       `json:"consul_maint"`
	OSMatch      string     `json:"os_match"`
	ArchMatch    string     `json:"arch_match"`
	Enabled      bool       `json:"enabled"`
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
	var id int64
	err := r.db.QueryRowContext(ctx, `
		INSERT INTO components (name, display_name, deploy_mode, binary_name, install_path,
			backup_dir, exec_path, exec_args, exec_as_user, exec_timeout, exec_env, work_dir,
			ignore_error, pre_install, post_install, service_name, service_mgr,
			stop_cmd, start_cmd, restart_cmd, version_cmd, version_regex, health_cmd,
			consul_maint, os_match, arch_match, enabled, tenant_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28)
		RETURNING id`,
		c.Name, c.DisplayName, c.DeployMode, c.BinaryName, c.InstallPath,
		c.BackupDir, c.ExecPath, pq.Array(c.ExecArgs), c.ExecAsUser, defExecTimeout(c.ExecTimeout),
		pq.Array(c.ExecEnv), c.WorkDir, c.IgnoreError, jsonArr(c.PreInstall), jsonArr(c.PostInstall),
		c.ServiceName, c.ServiceMgr, jsonArr(c.StopCmd), jsonArr(c.StartCmd), jsonArr(c.RestartCmd),
		jsonArr(c.VersionCmd), c.VersionRegex, jsonArr(c.HealthCmd),
		c.ConsulMaint, c.OSMatch, c.ArchMatch, c.Enabled, sc.TenantForWrite(),
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
	cond, targs := sc.Where("", 28)
	res, err := r.db.ExecContext(ctx, `
		UPDATE components SET display_name=$2, deploy_mode=$3, binary_name=$4, install_path=$5,
			backup_dir=$6, exec_path=$7, exec_args=$8, exec_as_user=$9, exec_timeout=$10, exec_env=$11,
			work_dir=$12, ignore_error=$13, pre_install=$14, post_install=$15, service_name=$16,
			service_mgr=$17, stop_cmd=$18, start_cmd=$19, restart_cmd=$20, version_cmd=$21,
			version_regex=$22, health_cmd=$23, consul_maint=$24, os_match=$25, arch_match=$26,
			enabled=$27, updated_at=NOW()
		WHERE id=$1 AND `+cond,
		append([]any{
			c.ID, c.DisplayName, c.DeployMode, c.BinaryName, c.InstallPath,
			c.BackupDir, c.ExecPath, pq.Array(c.ExecArgs), c.ExecAsUser, defExecTimeout(c.ExecTimeout),
			pq.Array(c.ExecEnv), c.WorkDir, c.IgnoreError, jsonArr(c.PreInstall), jsonArr(c.PostInstall),
			c.ServiceName, c.ServiceMgr, jsonArr(c.StopCmd), jsonArr(c.StartCmd), jsonArr(c.RestartCmd),
			jsonArr(c.VersionCmd), c.VersionRegex, jsonArr(c.HealthCmd),
			c.ConsulMaint, c.OSMatch, c.ArchMatch, c.Enabled,
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
	ignore_error, pre_install, post_install, service_name, service_mgr,
	stop_cmd, start_cmd, restart_cmd, version_cmd, version_regex, health_cmd,
	consul_maint, os_match, arch_match, enabled`

type scanner interface {
	Scan(dest ...any) error
}

func scanRow(s scanner) (*Component, error) {
	var c Component
	var pre, post, stop, start, restart, ver, health []byte
	err := s.Scan(&c.ID, &c.TenantID, &c.Name, &c.DisplayName, &c.DeployMode, &c.BinaryName, &c.InstallPath,
		&c.BackupDir, &c.ExecPath, pq.Array(&c.ExecArgs), &c.ExecAsUser, &c.ExecTimeout,
		pq.Array(&c.ExecEnv), &c.WorkDir, &c.IgnoreError, &pre, &post, &c.ServiceName, &c.ServiceMgr,
		&stop, &start, &restart, &ver, &c.VersionRegex, &health,
		&c.ConsulMaint, &c.OSMatch, &c.ArchMatch, &c.Enabled)
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
