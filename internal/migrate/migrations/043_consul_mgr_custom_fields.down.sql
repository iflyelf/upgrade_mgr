DELETE FROM host_field_mappings
WHERE source='consul_mgr'
  AND source_key IN ('public_ip','it_owner','biz_contact','biz_owner','ops_owner');
DELETE FROM host_field_defs
WHERE field_key IN ('public_ip','it_owner','biz_contact','biz_owner','ops_owner');
