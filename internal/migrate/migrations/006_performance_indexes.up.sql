-- 添加缺失的性能关键索引

-- hosts.agent_id 索引：心跳/上报更新频繁使用
CREATE INDEX IF NOT EXISTS idx_hosts_agent_id ON hosts(agent_id);

-- 优化其他高频查询（commands.task_id 已由 001 的 idx_commands_task 覆盖，不重复建）
CREATE INDEX IF NOT EXISTS idx_task_hosts_host_id ON task_hosts(host_id);
