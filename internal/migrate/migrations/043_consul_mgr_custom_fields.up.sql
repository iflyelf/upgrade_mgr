-- consul_mgr 常用 ServiceMeta 字段 → 主机自定义字段。
--
-- 内置列白名单只包含业务分组/标识类列；公网 IP、负责人等按自定义字段落库，
-- 页面「主机 → 编辑」可查看/维护，列表按 show_in_list 展示。
INSERT INTO host_field_defs (field_key, label, field_type, options, show_in_list, show_in_form, editable, builtin, sort_order)
VALUES
    ('public_ip',   '公网 IP',     'text', '', TRUE,  TRUE, TRUE, FALSE, 200),
    ('it_owner',    'IT 负责人',   'text', '', TRUE,  TRUE, TRUE, FALSE, 210),
    ('biz_contact', '业务联系人',  'text', '', FALSE, TRUE, TRUE, FALSE, 220),
    ('biz_owner',   '业务负责人',  'text', '', FALSE, TRUE, TRUE, FALSE, 230),
    ('ops_owner',   '运维负责人',  'text', '', FALSE, TRUE, TRUE, FALSE, 240)
ON CONFLICT (field_key) DO NOTHING;

INSERT INTO host_field_mappings (source, source_key, target_kind, target_key, sort_order)
VALUES
    ('consul_mgr', 'public_ip',   'custom', 'public_ip',   200),
    ('consul_mgr', 'it_owner',    'custom', 'it_owner',    210),
    ('consul_mgr', 'biz_contact', 'custom', 'biz_contact', 220),
    ('consul_mgr', 'biz_owner',   'custom', 'biz_owner',   230),
    ('consul_mgr', 'ops_owner',   'custom', 'ops_owner',   240)
ON CONFLICT (source, source_key) DO NOTHING;
