-- consul_mgr 来源的默认字段映射。
--
-- 背景：029 只给直连 Consul（source=consul）写了种子映射；从 consul_mgr
-- 同步（source=consul_mgr）一直没有默认映射，导致同步后 app_code/group
-- 等字段全为空。这里补齐与直连 Consul 一致的 8 条业务映射，并追加
-- consul_mgr 结构化保留键作为兜底（Meta 中缺少 group/node/datacenter 时使用）。
INSERT INTO host_field_mappings (source, source_key, target_kind, target_key, sort_order)
VALUES
    ('consul_mgr', 'name',          'column', 'hostname',    10),
    ('consul_mgr', 'os_type',       'column', 'os_type',     20),
    ('consul_mgr', 'account',       'column', 'account',     30),
    ('consul_mgr', 'app_code',      'column', 'app_code',    40),
    ('consul_mgr', 'group',         'column', 'biz_group',   50),
    ('consul_mgr', 'department',    'column', 'department',  60),
    ('consul_mgr', 'vendor',        'column', 'vendor',      70),
    ('consul_mgr', 'host_type',     'column', 'host_type',   80),
    -- 结构化兜底：sort_order 更大，Meta 中有值时优先用 Meta。
    ('consul_mgr', '__group_name',  'column', 'biz_group',  150),
    ('consul_mgr', '__node',        'column', 'consul_node', 160),
    ('consul_mgr', '__datacenter',  'column', 'datacenter', 170)
ON CONFLICT (source, source_key) DO NOTHING;
