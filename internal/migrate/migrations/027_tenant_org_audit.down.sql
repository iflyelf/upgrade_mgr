DROP INDEX IF EXISTS idx_audit_tenant_created;
DROP INDEX IF EXISTS idx_org_departments_tenant;
DROP INDEX IF EXISTS idx_service_groups_tenant;
DROP INDEX IF EXISTS idx_teams_tenant;
DROP INDEX IF EXISTS idx_roles_tenant;

DROP INDEX IF EXISTS uq_org_departments_tenant_dept;
DROP INDEX IF EXISTS uq_service_groups_tenant_name;
DROP INDEX IF EXISTS uq_teams_tenant_name;
DROP INDEX IF EXISTS uq_roles_tenant_name;

ALTER TABLE org_departments ADD CONSTRAINT org_departments_dept_id_key UNIQUE (dept_id);
ALTER TABLE service_groups  ADD CONSTRAINT service_groups_name_key UNIQUE (name);
ALTER TABLE teams           ADD CONSTRAINT teams_name_key UNIQUE (name);
ALTER TABLE roles           ADD CONSTRAINT roles_name_key UNIQUE (name);

ALTER TABLE audit_logs      DROP COLUMN IF EXISTS tenant_id;
ALTER TABLE org_departments DROP COLUMN IF EXISTS tenant_id;
ALTER TABLE service_groups  DROP COLUMN IF EXISTS tenant_id;
ALTER TABLE teams           DROP COLUMN IF EXISTS tenant_id;
ALTER TABLE roles           DROP COLUMN IF EXISTS tenant_id;
