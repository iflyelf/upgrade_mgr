-- 恢复 hostname 映射的默认「仅补空」语义。
UPDATE host_field_mappings
   SET overwrite = FALSE,
       updated_at = NOW()
 WHERE source IN ('consul', 'consul_mgr')
   AND source_key = 'name'
   AND target_kind = 'column'
   AND target_key = 'hostname';
