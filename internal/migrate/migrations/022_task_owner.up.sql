-- 任务操作权限：记录最后操作人，并支持「仅本人可操作」策略
--
-- 背景：团队按组件授权只覆盖「创建任务」。若需要限制「启动/取消/重试」
-- 他人创建的任务，需要知道任务创建者（已有 created_by）与当前操作者，
-- 并可选择是否允许管理员以外的人操作他人任务。

ALTER TABLE upgrade_tasks
    ADD COLUMN IF NOT EXISTS updated_by VARCHAR(100) NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_upgrade_tasks_created_by ON upgrade_tasks(created_by);
