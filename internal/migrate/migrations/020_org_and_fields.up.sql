-- 人员组织、字段自定义与 FlyIAM 集成基础表
--
-- 目标：与 consul_mgr 的人员模型对齐（用户扩展字段 / 角色 / 团队 / 服务组授权），
-- 并增加「主机字段自定义」能力。所有字段定义均可在页面维护，零硬编码。

-- ============================================================
-- 1) 用户扩展字段定义（与 FlyIAM user_field_defs 结构对齐）
-- 键与 FlyIAM 同步写入 Casdoor Properties 的键一致，
-- 便于后续从 FlyIAM 同步人事信息后直接展示。
-- ============================================================
CREATE TABLE IF NOT EXISTS user_field_defs (
    id            BIGSERIAL PRIMARY KEY,
    field_key     VARCHAR(100) NOT NULL UNIQUE,
    label         VARCHAR(200) NOT NULL,
    field_type    VARCHAR(32)  NOT NULL DEFAULT 'text', -- text/textarea/number/select/date
    options       TEXT         DEFAULT '',              -- JSON 数组，select 用
    show_in_list  BOOLEAN      NOT NULL DEFAULT FALSE,
    show_in_form  BOOLEAN      NOT NULL DEFAULT TRUE,
    editable      BOOLEAN      NOT NULL DEFAULT TRUE,
    builtin       BOOLEAN      NOT NULL DEFAULT FALSE,
    sort_order    INT          NOT NULL DEFAULT 0,
    created_at    TIMESTAMPTZ  DEFAULT NOW(),
    updated_at    TIMESTAMPTZ  DEFAULT NOW()
);

-- 用户扩展字段值：按 (用户, 字段键) 存储，新增字段无需改表。
CREATE TABLE IF NOT EXISTS user_field_values (
    user_id    VARCHAR(100) NOT NULL, -- local_users.username
    field_key  VARCHAR(100) NOT NULL,
    value      TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ DEFAULT NOW(),
    PRIMARY KEY (user_id, field_key)
);
CREATE INDEX IF NOT EXISTS idx_user_field_values_key ON user_field_values(field_key);

-- 内置人事字段（幂等插入）。
INSERT INTO user_field_defs (field_key, label, field_type, options, show_in_list, show_in_form, editable, builtin, sort_order)
VALUES
    ('empCode',     '工号',       'text',   '',                      TRUE,  TRUE,  TRUE, TRUE, 10),
    ('deptNameLv0', '零级部门',   'text',   '',                      FALSE, TRUE,  TRUE, TRUE, 20),
    ('deptNameLv1', '一级部门',   'text',   '',                      TRUE,  TRUE,  TRUE, TRUE, 30),
    ('deptNameLv2', '二级部门',   'text',   '',                      TRUE,  TRUE,  TRUE, TRUE, 40),
    ('compileType', '编制类型',   'select', '["正编","外包","实习"]', FALSE, TRUE,  TRUE, TRUE, 50),
    ('superior',    '上级账号',   'text',   '',                      FALSE, TRUE,  TRUE, TRUE, 60)
ON CONFLICT (field_key) DO NOTHING;

-- ============================================================
-- 2) 角色（可复用权限集合，可被团队引用）
-- ============================================================
CREATE TABLE IF NOT EXISTS roles (
    id          BIGSERIAL PRIMARY KEY,
    name        VARCHAR(100) NOT NULL UNIQUE,
    code        VARCHAR(100) NOT NULL DEFAULT '',
    description TEXT         DEFAULT '',
    permissions TEXT[]       DEFAULT '{}', -- 如 read/write/delete/deploy
    created_at  TIMESTAMPTZ  DEFAULT NOW(),
    updated_at  TIMESTAMPTZ  DEFAULT NOW()
);

-- ============================================================
-- 3) 团队与成员
-- ============================================================
CREATE TABLE IF NOT EXISTS teams (
    id          BIGSERIAL PRIMARY KEY,
    name        VARCHAR(100) NOT NULL UNIQUE,
    code        VARCHAR(100) NOT NULL DEFAULT '',
    description TEXT         DEFAULT '',
    status      SMALLINT     NOT NULL DEFAULT 1,
    created_by  VARCHAR(100) DEFAULT '',
    created_at  TIMESTAMPTZ  DEFAULT NOW(),
    updated_at  TIMESTAMPTZ  DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS team_members (
    id           BIGSERIAL PRIMARY KEY,
    team_id      BIGINT       NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    user_id      VARCHAR(100) NOT NULL, -- local_users.username
    username     VARCHAR(100) DEFAULT '',
    display_name VARCHAR(200) DEFAULT '',
    created_at   TIMESTAMPTZ  DEFAULT NOW(),
    UNIQUE (team_id, user_id)
);
CREATE INDEX IF NOT EXISTS idx_team_members_user ON team_members(user_id);

-- ============================================================
-- 4) 服务组（照搬 consul_mgr 模型，去掉 Consul 连接字段）
--
-- upgrade_mgr 没有 Consul 数据中心的「服务组」，这里把它映射为
-- 「组件集合」：服务组下挂若干组件，团队按服务组授权。
-- ============================================================
CREATE TABLE IF NOT EXISTS service_groups (
    id          BIGSERIAL PRIMARY KEY,
    name        VARCHAR(100) NOT NULL UNIQUE,
    code        VARCHAR(100) NOT NULL DEFAULT '',
    description TEXT         DEFAULT '',
    status      SMALLINT     NOT NULL DEFAULT 1,
    created_by  VARCHAR(100) DEFAULT '',
    created_at  TIMESTAMPTZ  DEFAULT NOW(),
    updated_at  TIMESTAMPTZ  DEFAULT NOW()
);

-- 服务组包含的组件（组内 "服务"）。组与团队授权通过 services 名称引用。
CREATE TABLE IF NOT EXISTS service_group_components (
    id           BIGSERIAL PRIMARY KEY,
    group_id     BIGINT NOT NULL REFERENCES service_groups(id) ON DELETE CASCADE,
    component_id BIGINT NOT NULL REFERENCES components(id) ON DELETE CASCADE,
    UNIQUE (group_id, component_id)
);
CREATE INDEX IF NOT EXISTS idx_sgc_component ON service_group_components(component_id);

-- 团队-服务组授权：permissions 为直接权限，role_ids 引用角色的权限（并集），
-- services 为授权的组件名（空=无权限，"*"=组内全部）。
CREATE TABLE IF NOT EXISTS team_group_permissions (
    id          BIGSERIAL PRIMARY KEY,
    team_id     BIGINT      NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    group_id    BIGINT      NOT NULL REFERENCES service_groups(id) ON DELETE CASCADE,
    permissions TEXT[]      DEFAULT '{}',
    role_ids    BIGINT[]    DEFAULT '{}',
    services    TEXT[]      DEFAULT '{}',
    host_groups TEXT[]      DEFAULT '{}',
    created_at  TIMESTAMPTZ DEFAULT NOW(),
    updated_at  TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE (team_id, group_id)
);

-- ============================================================
-- 5) 主机字段自定义
--
-- 定义与值分离：新增字段只需插入定义，不改 hosts 表结构。
-- 值存 hosts.custom_fields（JSONB），便于一次性返回与索引。
-- ============================================================
CREATE TABLE IF NOT EXISTS host_field_defs (
    id            BIGSERIAL PRIMARY KEY,
    field_key     VARCHAR(100) NOT NULL UNIQUE,
    label         VARCHAR(200) NOT NULL,
    field_type    VARCHAR(32)  NOT NULL DEFAULT 'text', -- text/textarea/number/select/date
    options       TEXT         DEFAULT '',
    show_in_list  BOOLEAN      NOT NULL DEFAULT FALSE,
    show_in_form  BOOLEAN      NOT NULL DEFAULT TRUE,
    editable      BOOLEAN      NOT NULL DEFAULT TRUE,
    -- builtin 与 user_field_defs 对齐：内置字段不可删除、键不可改。
    builtin       BOOLEAN      NOT NULL DEFAULT FALSE,
    sort_order    INT          NOT NULL DEFAULT 0,
    created_at    TIMESTAMPTZ  DEFAULT NOW(),
    updated_at    TIMESTAMPTZ  DEFAULT NOW()
);

ALTER TABLE hosts ADD COLUMN IF NOT EXISTS custom_fields JSONB NOT NULL DEFAULT '{}'::jsonb;

-- 默认创建「默认服务组」，把所有已有组件纳入，避免授权页空白。
INSERT INTO service_groups (name, code, description)
VALUES ('default', 'default', '默认服务组（包含全部组件）')
ON CONFLICT (name) DO NOTHING;

INSERT INTO service_group_components (group_id, component_id)
SELECT g.id, c.id
FROM service_groups g, components c
WHERE g.name = 'default'
ON CONFLICT DO NOTHING;
