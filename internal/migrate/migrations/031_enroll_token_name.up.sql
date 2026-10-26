-- 自动化令牌名称：用于页面区分不同自动化平台/环境。
-- 与 note 分开：name 简短可检索，note 保留补充说明。
ALTER TABLE enroll_tokens
    ADD COLUMN IF NOT EXISTS name VARCHAR(100) NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_enroll_tokens_name
    ON enroll_tokens(tenant_id, name);
