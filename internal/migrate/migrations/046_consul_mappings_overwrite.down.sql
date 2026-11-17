-- 恢复 Consul / consul_mgr 映射的默认「仅补空」语义。
-- hostname 由迁移 045 管理，这里不回退它。
UPDATE host_field_mappings
   SET overwrite = FALSE,
       updated_at = NOW()
 WHERE source IN ('consul', 'consul_mgr')
   AND NOT (source_key = 'name' AND target_kind = 'column' AND target_key = 'hostname');
