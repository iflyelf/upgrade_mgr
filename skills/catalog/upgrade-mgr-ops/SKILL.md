---
name: upgrade-mgr-ops
description: Operate the upgrade_mgr gray-release platform through its MCP server "{{.ServerName}}" - query hosts, agents, components, artifacts, tasks and audit logs; preview, create and drive canary upgrade or command tasks safely. 用于 upgrade_mgr 灰度升级控制平台的主机/组件/任务查询与灰度升级操作。
license: MIT
compatibility: opencode, claude-code, codex (requires MCP server {{.ServerName}})
metadata:
  mcp_server: "{{.ServerName}}"
  mcp_url: "{{.MCPURL}}"
  token_env: "{{.TokenEnv}}"
---

# upgrade_mgr 运维 Skill

通过 MCP 服务 `{{.ServerName}}`（`{{.MCPURL}}`）操作 upgrade_mgr 灰度升级控制平台。
令牌放在环境变量 `{{.TokenEnv}}`，不要写入代码或配置文件，也不要在回复中输出令牌。

## 何时使用

- 查询主机清单、Agent 在线/版本分布、某台主机的升级历史
- 查询组件、制品、升级任务及逐机执行日志
- 为组件发起灰度升级，或推进/暂停/取消/重试已有任务
- 排查升级失败、Agent 离线、主机未命中等问题

## 安全规则（必须遵守）

1. 只读工具可以直接调用；写工具会影响生产主机。
2. 调用任何写工具前，先向用户复述：目标组件与版本、命中主机数与示例 IP、分批策略、是否自动推进/回滚，得到用户明确同意后再调用。
3. 标记需要确认的工具（`task_start`、`task_cancel`、强制 `task_retry`、`agent_self_update`）必须在用户同意后携带 `confirm: true`。不要替用户做决定。
4. `task_create` 只会创建草稿（draft），不会执行；执行需要再调用 `task_start`。
5. 优先小批量金丝雀：`strategy.mode=canary`、较小的 `canary_count`、关闭 `auto_promote`，首批成功后再 `task_promote`。
6. 工具返回「需要确认」「无权限」「写操作未开启」时，如实告诉用户，不要绕过或换其他工具尝试。
7. 命令（脚本）任务风险最高，只有用户明确要求并说明脚本内容时才创建。

## 标准流程：灰度升级一个组件

1. `components_list` 找到组件 ID；`artifacts_list`（带 `component_id`）确认目标版本的制品已上传，且覆盖目标主机的 OS/架构。
2. `host_facets` 查看可用筛选字段；`hosts_list` 确认目标主机在线（`agent_status=online`）。
3. `task_preview`：用与创建相同的参数预览命中主机数、分批和因系统不匹配跳过的主机。
4. 向用户汇报预览结果并征得同意 → `task_create`（得到草稿任务 ID）。
5. 用户确认后 `task_start`（`confirm: true`）。
6. `task_get` 跟踪批次进度与逐机日志；首批成功后经用户同意 `task_promote`；异常时 `task_pause` 或 `task_cancel`。
7. 失败主机排查后可 `task_retry`（指定 `task_host_ids`；仍在执行中的主机需 `force` 并确认）。

详细说明见：

- [references/tools.md](references/tools.md)：全部工具与参数
- [references/troubleshooting.md](references/troubleshooting.md)：常见故障排查

## 回答风格

- 先给结论（成功/失败数量、卡住的批次），再给细节。
- 列主机时给 IP、主机名、状态；超过 20 台时汇总并提示可分页查看。
- 日志较长时只摘录错误相关的几行。
