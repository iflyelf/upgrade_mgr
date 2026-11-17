-- Consul / consul_mgr 的字段映射默认以外部清单为准。
--
-- 背景：标签 / ServiceMeta 值发生变化时，默认 overwrite=false 只在本地为空时
-- 补齐，已有值不会更新，页面会长期显示旧标签。
-- 这里把两个来源的映射改为覆盖模式：下次同步会自动更新已有主机。
-- hostname 已由迁移 045 单独处理，这里不重复覆盖，避免回滚 046 时误改 045 的策略。
-- 管理员仍可在「人员组织 → 主机字段映射」把个别字段改回「仅补空」。
UPDATE host_field_mappings
   SET overwrite = TRUE,
       updated_at = NOW()
 WHERE source IN ('consul', 'consul_mgr')
   AND NOT (source_key = 'name' AND target_kind = 'column' AND target_key = 'hostname');
