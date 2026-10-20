// Package model 数据模型（组件/制品/任务）。
package model

import "time"

// Component 组件定义
type Component struct {
	ID           int64      `json:"id"`
	Name         string     `json:"name"`
	DisplayName  string     `json:"display_name"`
	DeployMode   string     `json:"deploy_mode"`
	BinaryName   string     `json:"binary_name"`
	InstallPath  string     `json:"install_path"`
	BackupDir    string     `json:"backup_dir"`
	ExecPath     string     `json:"exec_path,omitempty"`
	ExecArgs     []string   `json:"exec_args,omitempty"`
	ExecAsUser   string     `json:"exec_as_user,omitempty"`
	ExecTimeout  string     `json:"exec_timeout,omitempty"`
	ExecEnv      []string   `json:"exec_env,omitempty"`
	WorkDir      string     `json:"work_dir,omitempty"`
	IgnoreError  bool       `json:"ignore_error"`
	PreInstall   [][]string `json:"pre_install,omitempty"`
	PostInstall  [][]string `json:"post_install,omitempty"`
	ServiceName  string     `json:"service_name,omitempty"`
	ServiceMgr   string     `json:"service_mgr"`
	StopCmd      [][]string `json:"stop_cmd,omitempty"`
	StartCmd     [][]string `json:"start_cmd,omitempty"`
	RestartCmd   [][]string `json:"restart_cmd,omitempty"`
	VersionCmd   [][]string `json:"version_cmd,omitempty"`
	VersionRegex string     `json:"version_regex,omitempty"`
	HealthCmd    [][]string `json:"health_cmd,omitempty"`
	ConsulMaint  bool       `json:"consul_maint"`
	OSMatch      string     `json:"os_match,omitempty"`
	ArchMatch    string     `json:"arch_match,omitempty"`
	Enabled      bool       `json:"enabled"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// Artifact 制品
type Artifact struct {
	ID            int64     `json:"id"`
	ComponentID   int64     `json:"component_id"`
	ComponentName string    `json:"component_name,omitempty"`
	Version       string    `json:"version"`
	OSFamily      string    `json:"os_family"`
	OSVersionSpec string    `json:"os_version_spec,omitempty"`
	Arch          string    `json:"arch"`
	Filename      string    `json:"filename"`
	Size          int64     `json:"size"`
	MD5           string    `json:"md5"`
	SHA256        string    `json:"sha256,omitempty"`
	Storage       string    `json:"storage"`
	Path          string    `json:"path"`
	InnerPath     string    `json:"inner_path,omitempty"`
	Notes         string    `json:"notes,omitempty"`
	Status        string    `json:"status"`
	UploadedBy    string    `json:"uploaded_by,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

// Task 升级任务
type Task struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	ComponentID int64     `json:"component_id"`
	Version     string    `json:"version"`
	Selector    string    `json:"selector"` // JSON: {ip_expr, os_family, ...}
	Strategy    string    `json:"strategy"` // JSON: {mode, batch_size, ...}
	Status      string    `json:"status"`
	ScheduleAt  time.Time `json:"schedule_at,omitempty"`
	CreatedBy   string    `json:"created_by,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}
