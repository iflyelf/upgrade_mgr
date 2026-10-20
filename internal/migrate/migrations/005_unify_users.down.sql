-- 回滚统一用户表：恢复独立 users 表（注意：无法恢复已删除表的原始数据，仅重建结构并回填）

CREATE TABLE IF NOT EXISTS users (
    id         BIGSERIAL PRIMARY KEY,
    username   VARCHAR(100) NOT NULL UNIQUE,
    tenant_id  INT NOT NULL DEFAULT 1 REFERENCES tenants(id),
    roles      TEXT[] NOT NULL DEFAULT ARRAY['user'],
    created_at TIMESTAMPTZ DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_users_tenant ON users(tenant_id);

-- 从 local_users 回填
INSERT INTO users (username, tenant_id, roles)
SELECT username, tenant_id, roles FROM local_users
ON CONFLICT (username) DO NOTHING;

DROP INDEX IF EXISTS idx_local_users_tenant;
ALTER TABLE local_users DROP CONSTRAINT IF EXISTS local_users_tenant_id_fkey;
ALTER TABLE local_users DROP COLUMN IF EXISTS roles;
ALTER TABLE local_users DROP COLUMN IF EXISTS tenant_id;
