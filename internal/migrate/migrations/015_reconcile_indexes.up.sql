-- 对账与维护窗口清理的索引优化
--
-- SyncTaskHostsFromCommands 每轮编排都会按 task_host 取「最新一条终态指令」。
-- 014 的 idx_commands_task_host_id 覆盖了 (task_host_id, id DESC)，但没有限定
-- 终态，历史指令很多时仍要扫描大量活动/中间态行。这里补一个终态部分索引。
CREATE INDEX IF NOT EXISTS idx_commands_task_host_terminal
    ON commands(task_host_id, id DESC)
    WHERE task_host_id IS NOT NULL
      AND status IN ('success','failed','rolled_back','timeout','expired');

-- 清理僵尸维护窗口：WHERE maintenance_reason='upgrading' AND NOT EXISTS(活动指令)。
-- 主机量大时避免全表扫描 hosts。
CREATE INDEX IF NOT EXISTS idx_hosts_maintenance_upgrading
    ON hosts(agent_id)
    WHERE maintenance_reason = 'upgrading';

-- ReapExpired 批量取过期指令：现有 idx_commands_active_lease 已覆盖
-- status IN (...) AND lease_expire_at，无需重复建索引。
-- Agent 自升级唯一性/最近一条查询：013 的 uq_commands_agent_active_self_update
-- 与 idx_commands_agent_type_id 已覆盖，无需重复建索引。

