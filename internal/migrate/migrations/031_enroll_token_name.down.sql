DROP INDEX IF EXISTS idx_enroll_tokens_name;
ALTER TABLE enroll_tokens DROP COLUMN IF EXISTS name;
