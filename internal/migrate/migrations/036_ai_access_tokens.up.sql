-- AI 访问令牌：供 MCP 客户端（OpenCode / Claude Code / Codex 等）以 Bearer 方式访问 /mcp。
--
-- 只保存 SHA-256 哈希，明文仅在创建时返回一次。
-- 令牌继承创建者身份：builtin 用户鉴权时按 local_users 实时取角色/租户/禁用状态
-- （用户被删除或禁用后令牌立即失效）；SSO 用户（不在 local_users）使用创建时的角色快照。
CREATE TABLE IF NOT EXISTS ai_access_tokens (
    id            BIGSERIAL PRIMARY KEY,
    tenant_id     INT NOT NULL DEFAULT 1 REFERENCES tenants(id),
    username      VARCHAR(100) NOT NULL,
    display_name  VARCHAR(200) NOT NULL DEFAULT '',
    auth_provider VARCHAR(32) NOT NULL DEFAULT 'builtin',
    is_admin      BOOLEAN NOT NULL DEFAULT FALSE,
    roles         TEXT[] NOT NULL DEFAULT ARRAY['user']::text[],
    name          VARCHAR(100) NOT NULL DEFAULT '',
    token_hash    CHAR(64) NOT NULL,
    token_prefix  VARCHAR(32) NOT NULL DEFAULT '',
    allow_write   BOOLEAN NOT NULL DEFAULT FALSE,
    expires_at    TIMESTAMPTZ,
    revoked_at    TIMESTAMPTZ,
    revoked_by    VARCHAR(100) NOT NULL DEFAULT '',
    last_used_at  TIMESTAMPTZ,
    last_used_ip  VARCHAR(64) NOT NULL DEFAULT '',
    created_by    VARCHAR(100) NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_ai_access_tokens_hash ON ai_access_tokens(token_hash);
CREATE INDEX IF NOT EXISTS idx_ai_access_tokens_user ON ai_access_tokens(tenant_id, username);
-- 每日维护按「注销/过期时间」清理超过保留期的令牌。
CREATE INDEX IF NOT EXISTS idx_ai_access_tokens_revoked ON ai_access_tokens(revoked_at) WHERE revoked_at IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_ai_access_tokens_expires ON ai_access_tokens(expires_at) WHERE expires_at IS NOT NULL;

COMMENT ON TABLE ai_access_tokens IS 'MCP/AI 访问令牌（仅存哈希）';
