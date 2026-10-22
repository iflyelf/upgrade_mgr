-- Consul 自动清理已移除主机所需索引
--
-- 清理条件：
--   source='consul' AND agent_id 为空 AND last_synced_at < 本轮同步时间
--   AND 没有 agents 记录 / 未完成任务或指令
-- 下面三个索引分别覆盖清理扫描与 NOT EXISTS 保护判断。

CREATE INDEX IF NOT EXISTS idx_hosts_consul_cleanup
    ON hosts(last_synced_at)
    WHERE source='consul' AND (agent_id IS NULL OR agent_id='');

CREATE INDEX IF NOT EXISTS idx_agents_ip
    ON agents(ip);

CREATE INDEX IF NOT EXISTS idx_commands_host_status
    ON commands(host_id, status);
