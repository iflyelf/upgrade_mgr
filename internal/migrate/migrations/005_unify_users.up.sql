-- 统一用户表：消除 local_users（认证）与 users（RBAC）割裂。
--
-- 背景：002 建了独立的 users 表存 tenant_id/roles，但真实认证走 local_users，
-- 两表按 username 无任何关联，导致 RBAC 对所有真实用户都查不到记录（形同虚设）。
-- 本迁移把租户与角色并入 local_users，使其成为唯一用户事实来源。

-- 1) local_users 增加租户与角色
ALTER TABLE local_users ADD COLUMN IF NOT EXISTS tenant_id INT NOT NULL DEFAULT 1;
ALTER TABLE local_users ADD COLUMN IF NOT EXISTS roles TEXT[] NOT NULL DEFAULT ARRAY['user'];

-- 2) 确保默认租户存在（002 已插入，此处幂等兜底）
INSERT INTO tenants (id, name, display_name, status)
VALUES (1, 'default', '默认租户', 'active')
ON CONFLICT (id) DO NOTHING;

-- 3) 补外键（若尚未存在）
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'local_users_tenant_id_fkey'
    ) THEN
        ALTER TABLE local_users
            ADD CONSTRAINT local_users_tenant_id_fkey
            FOREIGN KEY (tenant_id) REFERENCES tenants(id);
    END IF;
END $$;

-- 4) 迁移 users 表中已有的租户/角色数据（按 username 匹配）
UPDATE local_users lu
SET tenant_id = u.tenant_id,
    roles     = u.roles
FROM users u
WHERE u.username = lu.username;

-- 5) is_admin 与 roles 对齐：管理员必须含 admin 角色
UPDATE local_users
SET roles = array_append(roles, 'admin')
WHERE is_admin = TRUE AND NOT ('admin' = ANY(roles));

-- 6) 删除孤立的 users 表（其数据已并入 local_users）
DROP TABLE IF EXISTS users;

-- 7) 索引
CREATE INDEX IF NOT EXISTS idx_local_users_tenant ON local_users(tenant_id);

COMMENT ON COLUMN local_users.tenant_id IS '所属租户；业务查询据此隔离';
COMMENT ON COLUMN local_users.roles IS '角色数组：user/admin/superadmin（superadmin 可跨租户）';
