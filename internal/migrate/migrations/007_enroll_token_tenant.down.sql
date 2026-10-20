-- 回滚 enroll_tokens 租户隔离

DROP INDEX IF EXISTS idx_enroll_tokens_tenant;
ALTER TABLE enroll_tokens DROP CONSTRAINT IF EXISTS fk_enroll_tokens_tenant;
ALTER TABLE enroll_tokens DROP COLUMN IF EXISTS tenant_id;
