-- 回滚多租户 + RBAC

ALTER TABLE hosts         DROP CONSTRAINT IF EXISTS hosts_tenant_id_fkey;
ALTER TABLE components    DROP CONSTRAINT IF EXISTS components_tenant_id_fkey;
ALTER TABLE artifacts     DROP CONSTRAINT IF EXISTS artifacts_tenant_id_fkey;
ALTER TABLE upgrade_tasks DROP CONSTRAINT IF EXISTS upgrade_tasks_tenant_id_fkey;

ALTER TABLE hosts         DROP COLUMN IF EXISTS tenant_id;
ALTER TABLE components    DROP COLUMN IF EXISTS tenant_id;
ALTER TABLE artifacts     DROP COLUMN IF EXISTS tenant_id;
ALTER TABLE upgrade_tasks DROP COLUMN IF EXISTS tenant_id;

DROP TABLE IF EXISTS sync_logs;
DROP TABLE IF EXISTS users;
DROP TABLE IF EXISTS tenants;
