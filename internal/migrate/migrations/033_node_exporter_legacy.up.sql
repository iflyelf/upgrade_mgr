-- 为已有「外部文件」白名单补充旧内核专用 node_exporter（kernel 2.6.32-3.10）。
-- 背景：Go 1.23+ 在旧内核（2.6.32-3.10）上报 "procresize: invalid arg"，
-- Go 1.17 是最后支持旧内核的版本。与 028/032 一致：已有主条目时追加，
-- 已存在同名条目则跳过，不覆盖管理员自定义 URL；未保存过设置的部署直接使用内置默认。
UPDATE app_settings
SET value = rtrim(value, E'\n') || E'\n' ||
    'node-exporter-linux-amd64-legacy=https://github.com/iflyelf/node_exporter/releases/download/latest/node_exporter-linux-amd64-legacy'
WHERE key = 'server.external_binaries'
  AND lower(value) LIKE '%node-exporter-linux-amd64=%'
  AND lower(value) NOT LIKE '%node-exporter-linux-amd64-legacy=%';

UPDATE app_settings
SET value = rtrim(value, E'\n') || E'\n' ||
    'node-exporter-linux-arm64-legacy=https://github.com/iflyelf/node_exporter/releases/download/latest/node_exporter-linux-arm64-legacy'
WHERE key = 'server.external_binaries'
  AND lower(value) LIKE '%node-exporter-linux-arm64=%'
  AND lower(value) NOT LIKE '%node-exporter-linux-arm64-legacy=%';
