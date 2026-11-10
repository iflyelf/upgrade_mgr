-- 记录每台主机最近一次同步时运行的服务列表，用于「按服务名称同步」的精确清理：
-- 只删除「上次属于该服务、本次已不在该服务范围」的主机，不影响同组其它服务。
ALTER TABLE hosts ADD COLUMN IF NOT EXISTS source_services TEXT[] NOT NULL DEFAULT '{}';
CREATE INDEX IF NOT EXISTS idx_hosts_source_services ON hosts USING GIN (source_services);
