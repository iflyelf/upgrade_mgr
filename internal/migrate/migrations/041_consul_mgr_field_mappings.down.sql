DELETE FROM host_field_mappings
WHERE source='consul_mgr'
  AND source_key IN ('name','os_type','account','app_code','group','department',
                     'vendor','host_type','__group_name','__node','__datacenter');
