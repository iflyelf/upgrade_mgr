-- 回滚隧道连通性检查结果
DROP INDEX IF EXISTS idx_hosts_tunnel_check;

ALTER TABLE hosts
    DROP COLUMN IF EXISTS tunnel_check_ok,
    DROP COLUMN IF EXISTS tunnel_check_source,
    DROP COLUMN IF EXISTS tunnel_check_at,
    DROP COLUMN IF EXISTS tunnel_check_latency_ms,
    DROP COLUMN IF EXISTS tunnel_check_detail,
    DROP COLUMN IF EXISTS tunnel_check_requested_at;
