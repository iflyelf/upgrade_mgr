-- 回滚 Consul 自动清理索引

DROP INDEX IF EXISTS idx_hosts_consul_cleanup;
DROP INDEX IF EXISTS idx_agents_ip;
DROP INDEX IF EXISTS idx_commands_host_status;
