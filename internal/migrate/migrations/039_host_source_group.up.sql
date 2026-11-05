-- consul_mgr 同步按服务组清理缺失主机：记录主机来自哪个服务组。
--
-- 直连 Consul 的清理只针对 source='consul'；consul_mgr 一次同步一个服务组，
-- 必须按 group_id 限定清理范围，避免把其他服务组的主机误删。
ALTER TABLE hosts
    ADD COLUMN IF NOT EXISTS source_group VARCHAR(64) NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_hosts_source_group ON hosts(source, source_group);

COMMENT ON COLUMN hosts.source_group IS '外部同步来源的分组标识（当前用于 consul_mgr group_id）';
