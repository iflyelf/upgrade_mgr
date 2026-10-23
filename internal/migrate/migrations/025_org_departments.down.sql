-- 回滚组织架构快照表
DROP INDEX IF EXISTS idx_org_departments_path;
DROP INDEX IF EXISTS idx_org_departments_parent;
DROP TABLE IF EXISTS org_departments;
