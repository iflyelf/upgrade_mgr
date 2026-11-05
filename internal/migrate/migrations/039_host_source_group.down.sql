DROP INDEX IF EXISTS idx_hosts_source_group;
ALTER TABLE hosts DROP COLUMN IF EXISTS source_group;
