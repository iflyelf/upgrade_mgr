-- 回滚任务操作人字段
DROP INDEX IF EXISTS idx_upgrade_tasks_created_by;
ALTER TABLE upgrade_tasks DROP COLUMN IF EXISTS updated_by;
