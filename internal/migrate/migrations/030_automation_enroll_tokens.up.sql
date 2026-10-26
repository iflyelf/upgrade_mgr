-- 自动化注册令牌：与一次性令牌共用 enroll_tokens，用 token_type 区分。
--
-- 现有历史数据一律回填为 one_time，行为保持不变：
--   - one_time：15 分钟（可配置）、单次使用、used_at 标记消费；
--   - automation：可永久或自定义到期、可限制使用次数/IP，可注销。

ALTER TABLE enroll_tokens
    ADD COLUMN IF NOT EXISTS token_type VARCHAR(16) NOT NULL DEFAULT 'one_time',
    ADD COLUMN IF NOT EXISTS token_prefix VARCHAR(32) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS max_uses INTEGER NOT NULL DEFAULT 1,
    ADD COLUMN IF NOT EXISTS used_count INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS enabled BOOLEAN NOT NULL DEFAULT TRUE,
    ADD COLUMN IF NOT EXISTS revoked_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS revoked_by VARCHAR(100) DEFAULT '',
    ADD COLUMN IF NOT EXISTS allowed_cidrs TEXT[] NOT NULL DEFAULT '{}',
    ADD COLUMN IF NOT EXISTS last_used_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS last_used_ip VARCHAR(64) DEFAULT '';

-- 永久自动化令牌使用 NULL 表示不过期；一次性令牌仍由业务层强制写入到期时间。
ALTER TABLE enroll_tokens ALTER COLUMN expires_at DROP NOT NULL;

-- 已消费的历史一次性令牌补一个计数，便于审计展示。
UPDATE enroll_tokens
SET used_count = 1
WHERE used_at IS NOT NULL AND used_count = 0;

ALTER TABLE enroll_tokens DROP CONSTRAINT IF EXISTS chk_enroll_token_type;
ALTER TABLE enroll_tokens
    ADD CONSTRAINT chk_enroll_token_type
    CHECK (token_type IN ('one_time', 'automation'));

ALTER TABLE enroll_tokens DROP CONSTRAINT IF EXISTS chk_enroll_token_max_uses;
ALTER TABLE enroll_tokens
    ADD CONSTRAINT chk_enroll_token_max_uses
    CHECK (max_uses >= 0 AND used_count >= 0);

-- 一次性令牌必须有过期时间且只能使用一次；自动化令牌允许 expires_at 为空。
ALTER TABLE enroll_tokens DROP CONSTRAINT IF EXISTS chk_enroll_token_one_time;
ALTER TABLE enroll_tokens
    ADD CONSTRAINT chk_enroll_token_one_time
    CHECK (token_type <> 'one_time' OR (expires_at IS NOT NULL AND max_uses = 1));

CREATE INDEX IF NOT EXISTS idx_enroll_tokens_automation
    ON enroll_tokens(tenant_id, token_type, enabled);
