-- 回滚自动化注册令牌扩展。
-- 先删除 automation 行，避免 expires_at 的旧 NOT NULL 约束无法恢复。
DELETE FROM enroll_tokens WHERE token_type = 'automation';

ALTER TABLE enroll_tokens DROP CONSTRAINT IF EXISTS chk_enroll_token_one_time;
ALTER TABLE enroll_tokens DROP CONSTRAINT IF EXISTS chk_enroll_token_max_uses;
ALTER TABLE enroll_tokens DROP CONSTRAINT IF EXISTS chk_enroll_token_type;
DROP INDEX IF EXISTS idx_enroll_tokens_automation;

ALTER TABLE enroll_tokens ALTER COLUMN expires_at SET NOT NULL;

ALTER TABLE enroll_tokens
    DROP COLUMN IF EXISTS token_type,
    DROP COLUMN IF EXISTS token_prefix,
    DROP COLUMN IF EXISTS max_uses,
    DROP COLUMN IF EXISTS used_count,
    DROP COLUMN IF EXISTS enabled,
    DROP COLUMN IF EXISTS revoked_at,
    DROP COLUMN IF EXISTS revoked_by,
    DROP COLUMN IF EXISTS allowed_cidrs,
    DROP COLUMN IF EXISTS last_used_at,
    DROP COLUMN IF EXISTS last_used_ip;
