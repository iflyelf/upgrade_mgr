-- 回滚：移除 node_exporter legacy 条目（仅删除完全匹配默认 URL 的行）。
UPDATE app_settings
SET value = regexp_replace(
    value,
    E'(^|\\n)node-exporter-linux-amd64-legacy=https://github\\.com/iflyelf/node_exporter/releases/download/latest/node_exporter-linux-amd64-legacy\\n?',
    '',
    'gi'
)
WHERE key = 'server.external_binaries'
  AND lower(value) LIKE '%node-exporter-linux-amd64-legacy=%';

UPDATE app_settings
SET value = regexp_replace(
    value,
    E'(^|\\n)node-exporter-linux-arm64-legacy=https://github\\.com/iflyelf/node_exporter/releases/download/latest/node_exporter-linux-arm64-legacy\\n?',
    '',
    'gi'
)
WHERE key = 'server.external_binaries'
  AND lower(value) LIKE '%node-exporter-linux-arm64-legacy=%';
