-- 为已有「外部文件」白名单补充旧系统专用 windows_exporter。
-- 背景：Go 1.21+ 构建的 windows_exporter 不再支持 Windows 7 / Server 2008 R2；
-- 旧系统需使用官方 v0.23.1（Go 1.20 构建）。仅在管理员已配置对应主条目时追加，
-- 不覆盖自定义 URL；默认白名单（未保存过设置）已内置这两条。
UPDATE app_settings
SET value = rtrim(value, E'\n') || E'\n' ||
    'windows-exporter-amd64-legacy=https://github.com/iflyelf/windows_exporter/releases/download/latest/windows_exporter-amd64-legacy.exe'
WHERE key = 'server.external_binaries'
  AND lower(value) LIKE '%windows-exporter-amd64=%'
  AND lower(value) NOT LIKE '%windows-exporter-amd64-legacy=%';

UPDATE app_settings
SET value = rtrim(value, E'\n') || E'\n' ||
    'windows-exporter-arm64-legacy=https://github.com/iflyelf/windows_exporter/releases/download/latest/windows_exporter-arm64-legacy.exe'
WHERE key = 'server.external_binaries'
  AND lower(value) LIKE '%windows-exporter-arm64=%'
  AND lower(value) NOT LIKE '%windows-exporter-arm64-legacy=%';
