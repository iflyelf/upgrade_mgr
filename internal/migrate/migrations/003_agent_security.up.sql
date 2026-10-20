-- Agent 安全加固：一次性注册令牌 + 指令 nonce 去重

-- 一次性注册令牌（enroll token）：由管理员生成，Agent 注册时校验并消费
CREATE TABLE IF NOT EXISTS enroll_tokens (
    id         BIGSERIAL PRIMARY KEY,
    token_hash VARCHAR(64) NOT NULL UNIQUE,  -- sha256(token) 十六进制
    created_by VARCHAR(100) DEFAULT '',
    note       VARCHAR(255) DEFAULT '',
    expires_at TIMESTAMPTZ NOT NULL,
    used_at    TIMESTAMPTZ,                  -- 非空表示已消费
    used_by_ip VARCHAR(64) DEFAULT '',
    created_at TIMESTAMPTZ DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_enroll_tokens_exp ON enroll_tokens(expires_at);

-- 指令 nonce 去重：Agent 侧防重放的服务端配套（记录已下发 nonce，短期保留）
CREATE TABLE IF NOT EXISTS command_nonces (
    nonce      VARCHAR(64) PRIMARY KEY,
    agent_id   VARCHAR(64) NOT NULL,
    created_at TIMESTAMPTZ DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_command_nonces_created ON command_nonces(created_at);
