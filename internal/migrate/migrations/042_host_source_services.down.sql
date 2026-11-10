DROP INDEX IF EXISTS idx_hosts_source_services;
ALTER TABLE hosts DROP COLUMN IF EXISTS source_services;
