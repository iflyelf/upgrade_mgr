-- 主机关键字搜索与 app_code / 业务组候选值搜索加速。
--
-- 列表 q 使用 ILIKE '%关键字%'，普通 btree 无法命中；pg_trgm 的 GIN 索引
-- 可以让包含匹配走索引。扩展不存在时跳过索引，避免在没有权限的库上阻塞启动
-- （此时功能不受影响，只是大表搜索会走顺序扫描）。
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'pg_trgm') THEN
        EXECUTE 'CREATE INDEX IF NOT EXISTS idx_hosts_ip_trgm ON hosts USING gin (ip gin_trgm_ops)';
        EXECUTE 'CREATE INDEX IF NOT EXISTS idx_hosts_exit_ip_trgm ON hosts USING gin (exit_ip gin_trgm_ops)';
        EXECUTE 'CREATE INDEX IF NOT EXISTS idx_hosts_tunnel_ip_trgm ON hosts USING gin (tunnel_ip gin_trgm_ops)';
        EXECUTE 'CREATE INDEX IF NOT EXISTS idx_hosts_hostname_trgm ON hosts USING gin (hostname gin_trgm_ops)';
        EXECUTE 'CREATE INDEX IF NOT EXISTS idx_hosts_biz_group_trgm ON hosts USING gin (biz_group gin_trgm_ops)';
        EXECUTE 'CREATE INDEX IF NOT EXISTS idx_hosts_app_code_trgm ON hosts USING gin (app_code gin_trgm_ops)';
        EXECUTE 'CREATE INDEX IF NOT EXISTS idx_hosts_account_trgm ON hosts USING gin (account gin_trgm_ops)';
        EXECUTE 'CREATE INDEX IF NOT EXISTS idx_hosts_server_url_override_trgm ON hosts USING gin (server_url_override gin_trgm_ops)';
    END IF;
END $$;
