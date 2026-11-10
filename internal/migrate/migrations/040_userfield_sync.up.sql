-- 用户字段定义自动同步（对齐 consul_mgr）：单例配置 + 历史日志。
CREATE TABLE IF NOT EXISTS userfield_sync_config (
    id              SMALLINT PRIMARY KEY,
    enabled         BOOLEAN NOT NULL DEFAULT FALSE,
    interval        VARCHAR(20) NOT NULL DEFAULT '6h',
    sync_on_startup BOOLEAN NOT NULL DEFAULT FALSE,
    last_run_at     TIMESTAMPTZ,
    last_status     VARCHAR(20) NOT NULL DEFAULT '',
    last_message    TEXT NOT NULL DEFAULT '',
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS userfield_sync_logs (
    id           BIGSERIAL PRIMARY KEY,
    status       VARCHAR(20) NOT NULL DEFAULT 'running',
    trigger_type VARCHAR(20) NOT NULL DEFAULT 'manual',
    total        INT NOT NULL DEFAULT 0,
    added        INT NOT NULL DEFAULT 0,
    updated      INT NOT NULL DEFAULT 0,
    message      TEXT NOT NULL DEFAULT '',
    started_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_userfield_sync_logs_started ON userfield_sync_logs(started_at DESC);

INSERT INTO userfield_sync_config (id, enabled, interval, sync_on_startup)
VALUES (1, FALSE, '6h', FALSE)
ON CONFLICT (id) DO NOTHING;
