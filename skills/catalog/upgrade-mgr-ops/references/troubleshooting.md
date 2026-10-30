# 常见故障排查

## 任务预览/创建没有命中主机

- 用 `hosts_list` 按同样的 IP 段或分组确认主机存在于当前租户。
- `task_preview` 返回 `skipped_os` > 0：组件目标系统与主机 `os_family` 不一致（Linux 任务跳过 Windows 主机，反之亦然）。
- 普通用户还受团队授权的主机分组限制，用 `my_permissions` 查看 `host_groups`。

## 主机状态为 offline / 一直 pending

- `hosts_list` 看 `last_seen`：心跳超过离线阈值即视为离线，编排器会跳过离线主机。
- `agent_stats` 查看是否有大量离线或旧版本 Agent。
- 需要登录主机检查 Agent 服务（Linux：`systemctl status upgrade-agent`；也可运行 `upgrade-agent status`）。

## 逐机失败

- `task_get` 查看该主机的 `stage`、`exit_code` 与日志末尾：
  - 下载阶段失败：制品缺失对应 OS/架构，或主机无法访问控制台地址。
  - 校验失败：制品 sha256 与上传时不一致，重新上传制品。
  - 启动/健康检查失败：查看组件的服务名与启动脚本。
- 修复后用 `task_retry` 只重试失败主机。

## 签名被拒（rejected）

- Agent 与控制台时间偏差超过允许范围，或旧 Agent 签名模式不兼容；先对主机做 Agent 自升级（管理员 `agent_self_update`）。

## 工具返回「写操作未开启」「需要确认」

- 写操作需要平台全局开关与令牌「允许写」同时开启，联系管理员在「系统设置 → AI / MCP 服务」或「AI 接入」页调整。
- 「需要确认」表示要先征得用户同意，再携带 `confirm: true` 重新调用。
