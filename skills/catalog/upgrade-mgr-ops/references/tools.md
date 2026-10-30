# {{.ServerName}} 工具清单

工具实际可见范围取决于令牌身份：管理员工具只对管理员可见；写工具只在
「全局允许 MCP 写操作」开启且令牌勾选「允许写」时可见。

## 只读

| 工具 | 用途 | 主要参数 |
|------|------|----------|
| `hosts_list` | 主机分页列表 | `q`（IP/主机名/分组关键字）、`app_code`、`os_family`、`account`、`agent_status`（online/offline/unknown…）、`page`、`size` |
| `host_facets` | 主机筛选字段及取值 | `fields`（逗号分隔，留空用任务选主机字段） |
| `host_upgrade_history` | 单台主机升级历史 | `ip` |
| `agent_stats` | Agent 版本/OS/架构分布、离线与过期数 | 无 |
| `components_list` | 组件列表 | 无 |
| `component_get` | 组件详情 | `id` |
| `artifacts_list` | 制品分页列表 | `component_id`、`q`、`page`、`size` |
| `tasks_list` | 任务分页列表 | `kind`（upgrade/command）、`component_id`、`status`、`q`、`created_by`、`page`、`size` |
| `task_get` | 任务详情：批次、逐机状态与日志（日志截取末尾） | `id`、`host_page`、`host_size` |
| `task_preview` | 预览命中主机与分批（不落库） | 同 `task_create` |
| `my_permissions` | 当前身份可操作的组件与主机分组 | 无 |
| `audit_logs_list` | 审计日志（管理员） | `q`、`username`、`action`、`status`、`from`、`to`（YYYY-MM-DD）、`page`、`size` |
| `agent_self_update_status` | Agent 自升级目标版本与最近记录（管理员） | 无 |

## 写操作

| 工具 | 用途 | 需 confirm |
|------|------|-----------|
| `task_create` | 创建草稿任务（不执行） | 否 |
| `task_start` | 启动/恢复任务 | 是 |
| `task_pause` | 暂停任务 | 否 |
| `task_promote` | 手动推进下一批 | 否 |
| `task_cancel` | 取消任务 | 是 |
| `task_retry` | 重新执行失败/卡住主机 | `force=true` 时是 |
| `agent_self_update` | 下发 Agent 自升级（管理员） | 是 |

## task_create / task_preview 参数

```json
{
  "kind": "upgrade",
  "name": "nginx-exporter 1.8.2 灰度",
  "component_id": 3,
  "version": "1.8.2",
  "selector": {
    "ip_expr": "10.1.255.24-26,10.1.26.0/24",
    "exclude_ip_expr": "10.1.26.1",
    "filters": { "app_code": ["PAY"], "os_family": ["centos"] }
  },
  "strategy": {
    "mode": "canary",
    "canary_count": 1,
    "batch_size": 10,
    "concurrency": 5,
    "auto_promote": false,
    "health_wait": "2m",
    "failure_threshold": "10%",
    "auto_rollback": true
  }
}
```

- `strategy.mode`：`canary`（首批 `canary_count` 台，其余按 `batch_size`）/ `count`（每批 `batch_size` 台）/ `percent`（每批 `batch_size`%）。
- `selector.ip_expr` 支持单 IP、范围 `a.b.c.d-e`、`a.b.c.d-a.b.c.e`、CIDR，逗号分隔。
- `kind=command` 时改用 `command`：`{"script": "...", "interpreter": "bash", "timeout": "10m"}`，不需要 `component_id`/`version`；需要管理员身份，且平台开启命令任务。
