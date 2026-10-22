-- 主机列表分页/过滤复合索引（多租户 + 常用过滤条件）
--
-- 主机规模可能达到几万台：列表固定按 tenant_id 过滤并按 ip 排序，
-- 复合索引可避免「先过滤再全量排序」。app_code / os_family / account
-- 是页面常用筛选条件，同样建立租户前缀索引。

CREATE INDEX IF NOT EXISTS idx_hosts_tenant_ip
    ON hosts(tenant_id, ip);

CREATE INDEX IF NOT EXISTS idx_hosts_tenant_app_code
    ON hosts(tenant_id, app_code);

CREATE INDEX IF NOT EXISTS idx_hosts_tenant_os_family
    ON hosts(tenant_id, os_family);

CREATE INDEX IF NOT EXISTS idx_hosts_tenant_account
    ON hosts(tenant_id, account);
