-- 回滚多网卡出口 IP 与隧道地址
DROP INDEX IF EXISTS idx_hosts_tunnel_ip;
DROP INDEX IF EXISTS idx_hosts_exit_ip;

ALTER TABLE hosts
    DROP COLUMN IF EXISTS exit_ip,
    DROP COLUMN IF EXISTS tunnel_ip,
    DROP COLUMN IF EXISTS server_tunnel_url,
    DROP COLUMN IF EXISTS agent_server_url,
    DROP COLUMN IF EXISTS agent_default_server_url;
