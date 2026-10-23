-- 组织架构（部门树）同步自 FlyIAM
--
-- 上游：GET /api/departments/export（X-Service-Token），由用户
-- deptIdLv0/1/2、deptNameLv0/1/2 属性推导，包含父级、层级、完整路径与人数。
--
-- 本表为「快照」：每次同步整体替换，避免上游删除部门后本地残留。
-- 无外键引用，替换操作不会影响其他业务数据。

CREATE TABLE IF NOT EXISTS org_departments (
    id          BIGSERIAL PRIMARY KEY,
    dept_id     VARCHAR(200) NOT NULL UNIQUE,
    dept_name   VARCHAR(300) NOT NULL DEFAULT '',
    parent_id   VARCHAR(200) NOT NULL DEFAULT '',
    level       INT          NOT NULL DEFAULT 0,
    full_path   TEXT         NOT NULL DEFAULT '',
    user_count  INT          NOT NULL DEFAULT 0,
    synced_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_org_departments_parent ON org_departments(parent_id);
CREATE INDEX IF NOT EXISTS idx_org_departments_path ON org_departments(full_path);
