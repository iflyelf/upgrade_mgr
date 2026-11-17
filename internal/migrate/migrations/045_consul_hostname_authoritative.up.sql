-- Consul / consul_mgr 的主机名以外部清单为准。
--
-- 背景：hostname 映射默认 overwrite=false，Agent 安装/重装后上报的主机名
-- 会覆盖清单里的主机名，导致按主机名识别的运维流程出现偏差。
-- 这里把两个来源的默认 hostname 映射改为覆盖模式：
--   - 同步时有非空 name 就覆盖现有 hostname；
--   - Agent 注册/心跳不再覆盖清单主机名（handler 中按 source/consul_node 保护）。
-- 管理员仍可在「人员组织 → 主机字段映射」中改回「仅补空」。
UPDATE host_field_mappings
   SET overwrite = TRUE,
       updated_at = NOW()
 WHERE source IN ('consul', 'consul_mgr')
   AND source_key = 'name'
   AND target_kind = 'column'
   AND target_key = 'hostname';
