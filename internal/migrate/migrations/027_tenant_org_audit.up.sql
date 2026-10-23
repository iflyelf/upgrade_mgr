-- 多租户隔离：角色/团队/服务组/组织架构/审计日志
--
-- 之前只有 hosts/tasks 带 tenant_id，团队与组织是全局共享：
-- 租户 A 的管理员可以看到并修改租户 B 的团队/授权。本迁移补齐隔离。
--
-- 说明：
--   - 默认 tenant_id=1，兼容单租户/历史数据；
--   - 审计日志默认 0（历史数据无法判断租户），新写入会记录实际租户；
--   - 名称唯一约束改为 (tenant_id, name)，允许不同租户使用同名团队/角色/服务组。

ALTER TABLE roles             ADD COLUMN IF NOT EXISTS tenant_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE teams             ADD COLUMN IF NOT EXISTS tenant_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE service_groups    ADD COLUMN IF NOT EXISTS tenant_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE org_departments   ADD COLUMN IF NOT EXISTS tenant_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE audit_logs        ADD COLUMN IF NOT EXISTS tenant_id BIGINT NOT NULL DEFAULT 0;

-- 唯一约束改为租户内唯一
ALTER TABLE roles           DROP CONSTRAINT IF EXISTS roles_name_key;
ALTER TABLE teams           DROP CONSTRAINT IF EXISTS teams_name_key;
ALTER TABLE service_groups  DROP CONSTRAINT IF EXISTS service_groups_name_key;
ALTER TABLE org_departments DROP CONSTRAINT IF EXISTS org_departments_dept_id_key;

CREATE UNIQUE INDEX IF NOT EXISTS uq_roles_tenant_name
    ON roles(tenant_id, name);
CREATE UNIQUE INDEX IF NOT EXISTS uq_teams_tenant_name
    ON teams(tenant_id, name);
CREATE UNIQUE INDEX IF NOT EXISTS uq_service_groups_tenant_name
    ON service_groups(tenant_id, name);
CREATE UNIQUE INDEX IF NOT EXISTS uq_org_departments_tenant_dept
    ON org_departments(tenant_id, dept_id);

CREATE INDEX IF NOT EXISTS idx_roles_tenant          ON roles(tenant_id);
CREATE INDEX IF NOT EXISTS idx_teams_tenant          ON teams(tenant_id);
CREATE INDEX IF NOT EXISTS idx_service_groups_tenant ON service_groups(tenant_id);
CREATE INDEX IF NOT EXISTS idx_org_departments_tenant ON org_departments(tenant_id);
CREATE INDEX IF NOT EXISTS idx_audit_tenant_created  ON audit_logs(tenant_id, created_at DESC);
