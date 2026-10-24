-- 主机字段映射：把外部来源（Consul ServiceMeta / consul_mgr）的键映射到本地字段
--
-- 背景：原先 internal/logic/sync 把 7 个 Meta 键硬编码映射到固定列
-- （account/app_code/group/department/vendor/os_type/host_type），
-- 新增一个来源键就得改代码重新发版，自定义字段也完全拿不到 Consul 数据。
--
-- 本表把映射关系变成数据：
--   source_key  外部来源的键（Consul ServiceMeta 的键名）
--   target_kind column=写入 hosts 的内置列；custom=写入 hosts.custom_fields
--   target_key  目标列名或自定义字段键
--
-- 安全：target_kind='column' 时 target_key 必须在白名单内（见 CHECK 约束），
-- 杜绝把 Meta 写到 ip/agent_id/source/tenant_id 等身份与控制字段上。
-- target_kind='custom' 时 target_key 必须已在 host_field_defs 中定义，
-- 由应用层校验（字段定义可随时增删，不用外键以免同步被阻塞）。

CREATE TABLE IF NOT EXISTS host_field_mappings (
    id          BIGSERIAL PRIMARY KEY,
    source      VARCHAR(32)  NOT NULL DEFAULT 'consul', -- consul / consul_mgr
    source_key  VARCHAR(128) NOT NULL,
    target_kind VARCHAR(16)  NOT NULL DEFAULT 'custom',
    target_key  VARCHAR(100) NOT NULL,
    enabled     BOOLEAN      NOT NULL DEFAULT TRUE,
    -- overwrite=false（默认）只补齐空值，不覆盖人工维护过的值；
    -- true 表示以外部来源为准，每次同步都覆盖。
    overwrite   BOOLEAN      NOT NULL DEFAULT FALSE,
    sort_order  INT          NOT NULL DEFAULT 100,
    created_at  TIMESTAMPTZ  DEFAULT NOW(),
    updated_at  TIMESTAMPTZ  DEFAULT NOW(),
    CONSTRAINT host_field_mappings_kind_chk
        CHECK (target_kind IN ('column', 'custom')),
    -- 内置列白名单：只允许业务分组/标识类列，不含身份与控制字段。
    CONSTRAINT host_field_mappings_column_chk
        CHECK (target_kind <> 'column' OR target_key IN (
            'hostname', 'consul_node', 'datacenter', 'os_type',
            'account', 'app_code', 'biz_group', 'department',
            'vendor', 'host_type'
        )),
    -- 同一来源下，一个外部键只映射一次（避免互相覆盖导致结果不确定）。
    UNIQUE (source, source_key)
);

CREATE INDEX IF NOT EXISTS idx_host_field_mappings_src
    ON host_field_mappings(source, enabled);

-- 种子数据：与原硬编码行为完全一致，升级后同步结果不变。
-- 管理员可在「人员组织 → 主机字段映射」中增删改。
INSERT INTO host_field_mappings (source, source_key, target_kind, target_key, sort_order)
VALUES
    ('consul', 'name',       'column', 'hostname',   10),
    ('consul', 'os_type',    'column', 'os_type',    20),
    ('consul', 'account',    'column', 'account',    30),
    ('consul', 'app_code',   'column', 'app_code',   40),
    ('consul', 'group',      'column', 'biz_group',  50),
    ('consul', 'department', 'column', 'department', 60),
    ('consul', 'vendor',     'column', 'vendor',     70),
    ('consul', 'host_type',  'column', 'host_type',  80)
ON CONFLICT (source, source_key) DO NOTHING;
