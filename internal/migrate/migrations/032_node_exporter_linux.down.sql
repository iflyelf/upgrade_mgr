-- 回滚：移除本迁移追加的 node_exporter Linux 默认条目。
UPDATE app_settings
SET value = replace(value,
    E'\nnode-exporter-linux-amd64=https://github.com/iflyelf/node_exporter/releases/download/latest/node_exporter-linux-amd64', '')
WHERE key = 'server.external_binaries';

UPDATE app_settings
SET value = replace(value,
    'node-exporter-linux-amd64=https://github.com/iflyelf/node_exporter/releases/download/latest/node_exporter-linux-amd64', '')
WHERE key = 'server.external_binaries';

UPDATE app_settings
SET value = replace(value,
    E'\nnode-exporter-linux-arm64=https://github.com/iflyelf/node_exporter/releases/download/latest/node_exporter-linux-arm64', '')
WHERE key = 'server.external_binaries';

UPDATE app_settings
SET value = replace(value,
    'node-exporter-linux-arm64=https://github.com/iflyelf/node_exporter/releases/download/latest/node_exporter-linux-arm64', '')
WHERE key = 'server.external_binaries';
