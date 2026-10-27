-- 为已有「外部文件」白名单补充 Linux node_exporter（ELF 静态二进制）。
-- 背景：配置非空时不会合并内置默认，已保存过白名单的部署看不到新增的默认条目。
-- 与 028 一致：已有 windows-exporter 主条目（即沿用内置默认）时追加，
-- 已存在同名条目则跳过，不覆盖管理员自定义 URL；未保存过设置的部署直接使用内置默认。
UPDATE app_settings
SET value = rtrim(value, E'\n') || E'\n' ||
    'node-exporter-linux-amd64=https://github.com/iflyelf/node_exporter/releases/download/latest/node_exporter-linux-amd64'
WHERE key = 'server.external_binaries'
  AND lower(value) LIKE '%windows-exporter-amd64=%'
  AND lower(value) NOT LIKE '%node-exporter-linux-amd64=%';

UPDATE app_settings
SET value = rtrim(value, E'\n') || E'\n' ||
    'node-exporter-linux-arm64=https://github.com/iflyelf/node_exporter/releases/download/latest/node_exporter-linux-arm64'
WHERE key = 'server.external_binaries'
  AND lower(value) LIKE '%windows-exporter-amd64=%'
  AND lower(value) NOT LIKE '%node-exporter-linux-arm64=%';
