-- 回滚主机列表复合索引

DROP INDEX IF EXISTS idx_hosts_tenant_ip;
DROP INDEX IF EXISTS idx_hosts_tenant_app_code;
DROP INDEX IF EXISTS idx_hosts_tenant_os_family;
DROP INDEX IF EXISTS idx_hosts_tenant_account;
