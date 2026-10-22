DROP INDEX IF EXISTS idx_commands_task_host_id;
DROP INDEX IF EXISTS idx_commands_active_stage;
DROP INDEX IF EXISTS idx_commands_active_lease;
DROP INDEX IF EXISTS uq_commands_task_host_active;

ALTER TABLE commands
    DROP COLUMN IF EXISTS last_stage_at,
    DROP COLUMN IF EXISTS last_stage;
