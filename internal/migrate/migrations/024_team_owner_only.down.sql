-- 回滚团队级 owner 策略
ALTER TABLE teams DROP COLUMN IF EXISTS owner_only;
