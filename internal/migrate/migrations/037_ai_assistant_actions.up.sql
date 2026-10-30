-- AI 助手待确认操作：写工具不直接执行，先暂存，用户在页面确认后执行；过期由每日维护自动清理。
CREATE TABLE IF NOT EXISTS ai_assistant_actions (
    id          BIGSERIAL PRIMARY KEY,
    tenant_id   INT NOT NULL DEFAULT 1 REFERENCES tenants(id),
    username    VARCHAR(100) NOT NULL,
    tool        VARCHAR(128) NOT NULL,
    args        JSONB NOT NULL DEFAULT '{}'::jsonb,
    summary     TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at  TIMESTAMPTZ NOT NULL,
    executed_at TIMESTAMPTZ,
    executed_by VARCHAR(100) NOT NULL DEFAULT '',
    result      TEXT NOT NULL DEFAULT '',
    error       TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_ai_assistant_actions_user
    ON ai_assistant_actions(username, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_ai_assistant_actions_expires
    ON ai_assistant_actions(expires_at);

COMMENT ON TABLE ai_assistant_actions IS 'AI 助手待确认操作（写工具需用户在页面确认后执行）';
