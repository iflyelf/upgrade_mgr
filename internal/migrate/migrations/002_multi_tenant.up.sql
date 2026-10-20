-- 多租户 + RBAC + 同步日志（对齐权威 schema：业务表来自 001）

-- 1. 租户表
CREATE TABLE IF NOT EXISTS tenants (
    id           SERIAL PRIMARY KEY,
    name         VARCHAR(100) UNIQUE NOT NULL,
    display_name VARCHAR(200) DEFAULT '',
    status       VARCHAR(20) DEFAULT 'active',
    created_at   TIMESTAMPTZ DEFAULT NOW()
);

-- 默认租户（必须在加外键前存在）
INSERT INTO tenants (id, name, display_name, status)
VALUES (1, 'default', '默认租户', 'active')
ON CONFLICT (id) DO NOTHING;
SELECT setval('tenants_id_seq', GREATEST(1, (SELECT MAX(id) FROM tenants)), true);

-- 2. RBAC 用户表（与内置账号 local_users 分离：此表承载租户/角色映射）
CREATE TABLE IF NOT EXISTS users (
    id         BIGSERIAL PRIMARY KEY,
    username   VARCHAR(100) NOT NULL UNIQUE,
    tenant_id  INT NOT NULL DEFAULT 1 REFERENCES tenants(id),
    roles      TEXT[] NOT NULL DEFAULT ARRAY['user'],
    created_at TIMESTAMPTZ DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_users_tenant ON users(tenant_id);

-- 3. 用户字段同步日志（maintenance 清理引用）
CREATE TABLE IF NOT EXISTS sync_logs (
    id           BIGSERIAL PRIMARY KEY,
    source       VARCHAR(64) DEFAULT '',
    status       VARCHAR(24) DEFAULT '',
    total        INT DEFAULT 0,
    message      TEXT DEFAULT '',
    started_at   TIMESTAMPTZ DEFAULT NOW(),
    completed_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_sync_logs_started ON sync_logs(started_at DESC);

-- 4. 业务表加 tenant_id（幂等）+ 默认租户 + 外键
ALTER TABLE hosts         ADD COLUMN IF NOT EXISTS tenant_id INT;
ALTER TABLE components    ADD COLUMN IF NOT EXISTS tenant_id INT;
ALTER TABLE artifacts     ADD COLUMN IF NOT EXISTS tenant_id INT;
ALTER TABLE upgrade_tasks ADD COLUMN IF NOT EXISTS tenant_id INT;

UPDATE hosts         SET tenant_id = 1 WHERE tenant_id IS NULL;
UPDATE components    SET tenant_id = 1 WHERE tenant_id IS NULL;
UPDATE artifacts     SET tenant_id = 1 WHERE tenant_id IS NULL;
UPDATE upgrade_tasks SET tenant_id = 1 WHERE tenant_id IS NULL;

ALTER TABLE hosts         ALTER COLUMN tenant_id SET DEFAULT 1;
ALTER TABLE hosts         ALTER COLUMN tenant_id SET NOT NULL;
ALTER TABLE components    ALTER COLUMN tenant_id SET DEFAULT 1;
ALTER TABLE components    ALTER COLUMN tenant_id SET NOT NULL;
ALTER TABLE artifacts     ALTER COLUMN tenant_id SET DEFAULT 1;
ALTER TABLE artifacts     ALTER COLUMN tenant_id SET NOT NULL;
ALTER TABLE upgrade_tasks ALTER COLUMN tenant_id SET DEFAULT 1;
ALTER TABLE upgrade_tasks ALTER COLUMN tenant_id SET NOT NULL;

-- 外键（用 DO 块保证幂等：约束已存在则跳过）
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'hosts_tenant_id_fkey') THEN
        ALTER TABLE hosts ADD CONSTRAINT hosts_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'components_tenant_id_fkey') THEN
        ALTER TABLE components ADD CONSTRAINT components_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'artifacts_tenant_id_fkey') THEN
        ALTER TABLE artifacts ADD CONSTRAINT artifacts_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'upgrade_tasks_tenant_id_fkey') THEN
        ALTER TABLE upgrade_tasks ADD CONSTRAINT upgrade_tasks_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id);
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_hosts_tenant      ON hosts(tenant_id);
CREATE INDEX IF NOT EXISTS idx_components_tenant ON components(tenant_id);
CREATE INDEX IF NOT EXISTS idx_artifacts_tenant  ON artifacts(tenant_id);
CREATE INDEX IF NOT EXISTS idx_tasks_tenant      ON upgrade_tasks(tenant_id);
