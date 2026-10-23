-- 回滚：移除本迁移追加的默认旧系统条目（保留管理员后来改成的其它 URL 也一并移除，
-- 因为回滚意味着不再声明旧系统兼容条目）。
UPDATE app_settings
SET value = replace(value,
    E'\nwindows-exporter-amd64-legacy=https://github.com/iflyelf/windows_exporter/releases/download/latest/windows_exporter-amd64-legacy.exe', '')
WHERE key = 'server.external_binaries';

UPDATE app_settings
SET value = replace(value,
    'windows-exporter-amd64-legacy=https://github.com/iflyelf/windows_exporter/releases/download/latest/windows_exporter-amd64-legacy.exe', '')
WHERE key = 'server.external_binaries';

UPDATE app_settings
SET value = replace(value,
    E'\nwindows-exporter-arm64-legacy=https://github.com/iflyelf/windows_exporter/releases/download/latest/windows_exporter-arm64-legacy.exe', '')
WHERE key = 'server.external_binaries';

UPDATE app_settings
SET value = replace(value,
    'windows-exporter-arm64-legacy=https://github.com/iflyelf/windows_exporter/releases/download/latest/windows_exporter-arm64-legacy.exe', '')
WHERE key = 'server.external_binaries';
